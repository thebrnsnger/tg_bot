go
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/joho/godotenv"
)

// Константы
const (
	MaxMessageLength = 4096
	APITimeout       = 30 * time.Second
	UpdateTimeout    = 60
	ChunkDelay       = 100 * time.Millisecond
)

// Структуры для API Deepseek
type DeepseekRequest struct {
	Model       string            `json:"model"`
	Messages    []DeepseekMessage `json:"messages"`
	Temperature float64           `json:"temperature,omitempty"`
	MaxTokens   int               `json:"max_tokens,omitempty"`
}

type DeepseekMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type DeepseekResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error,omitempty"`
}

// Bot структура для инкапсуляции логики бота
type Bot struct {
	api       *tgbotapi.BotAPI
	apiKey    string
	client    *http.Client
	logger    *log.Logger
	userStats map[int64]int // Счетчик сообщений пользователей
	debug     bool
}

// NewBot создает новый экземпляр бота
func NewBot(botToken, apiKey string, debug bool) (*Bot, error) {
	api, err := tgbotapi.NewBotAPI(botToken)
	if err != nil {
		return nil, fmt.Errorf("failed to create bot API: %w", err)
	}

	api.Debug = debug

	return &Bot{
		api:       api,
		apiKey:    apiKey,
		client:    &http.Client{Timeout: APITimeout},
		logger:    log.New(os.Stdout, "[BOT] ", log.LstdFlags|log.Lshortfile),
		userStats: make(map[int64]int),
		debug:     debug,
	}, nil
}

// callDeepseekAPI вызывает API Deepseek с улучшенной обработкой ошибок
func (b *Bot) callDeepseekAPI(ctx context.Context, userMessage string) (string, error) {
	if b.apiKey == "" {
		return "", fmt.Errorf("API key is empty")
	}

	b.logger.Printf("🔑 Using API key: %s...", b.apiKey[:min(10, len(b.apiKey))])
	b.logger.Printf("📤 Sending request to Deepseek API")

	url := "https://api.deepseek.com/v1/chat/completions"

	requestBody := DeepseekRequest{
		Model: "deepseek-chat",
		Messages: []DeepseekMessage{
			{
				Role:    "system",
				Content: "You are a helpful assistant. Respond in the same language as the user's message. Be concise but informative.",
			},
			{
				Role:    "user",
				Content: userMessage,
			},
		},
		Temperature: 0.7,
		MaxTokens:   2000,
	}

	jsonData, err := json.Marshal(requestBody)
	if err != nil {
		return "", fmt.Errorf("failed to marshal request: %w", err)
	}

	if b.debug {
		b.logger.Printf("📋 Request payload: %s", string(jsonData))
	}

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(jsonData))
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+b.apiKey)

	startTime := time.Now()
	resp, err := b.client.Do(req)
	duration := time.Since(startTime)

	if err != nil {
		b.logger.Printf("❌ API request failed after %v: %v", duration, err)
		return "", fmt.Errorf("API request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response: %w", err)
	}

	b.logger.Printf("📡 API Response: Status=%d, Duration=%v, Size=%d bytes", 
		resp.StatusCode, duration, len(body))

	if b.debug {
		b.logger.Printf("📄 Response body: %s", string(body))
	}

	if resp.StatusCode != http.StatusOK {
		b.logger.Printf("❌ API error response: %s", string(body))
		return "", fmt.Errorf("API error (status %d): %s", resp.StatusCode, string(body))
	}

	var deepseekResp DeepseekResponse
	if err := json.Unmarshal(body, &deepseekResp); err != nil {
		b.logger.Printf("❌ Failed to parse JSON response: %v", err)
		return "", fmt.Errorf("failed to parse response: %w", err)
	}

	if deepseekResp.Error != nil {
		b.logger.Printf("❌ API returned error: %s", deepseekResp.Error.Message)
		return "", fmt.Errorf("API error: %s", deepseekResp.Error.Message)
	}

	if len(deepseekResp.Choices) == 0 {
		b.logger.Printf("⚠️ No choices in API response")
		return "Извините, не удалось получить ответ от ИИ", nil
	}

	response := strings.TrimSpace(deepseekResp.Choices[0].Message.Content)
	b.logger.Printf("✅ API response received: %d characters", len(response))

	return response, nil
}

// sendLongMessage отправляет длинные сообщения частями
func (b *Bot) sendLongMessage(chatID int64, text string) error {
	b.logger.Printf("📤 Sending message to chat %d, length: %d", chatID, len(text))

	if len(text) <= MaxMessageLength {
		msg := tgbotapi.NewMessage(chatID, text)
		msg.ParseMode = "Markdown"
		
		sent, err := b.api.Send(msg)
		if err != nil {
			// Если Markdown не работает, попробуем без форматирования
			b.logger.Printf("⚠️ Markdown failed, trying plain text: %v", err)
			msg.ParseMode = ""
			sent, err = b.api.Send(msg)
		}
		
		if err != nil {
			b.logger.Printf("❌ Failed to send message: %v", err)
			return err
		}
		
		b.logger.Printf("✅ Message sent successfully, ID: %d", sent.MessageID)
		return nil
	}

	// Разбиваем на части
	b.logger.Printf("📝 Splitting long message into chunks")
	chunks := b.splitMessage(text, MaxMessageLength)
	b.logger.Printf("📊 Created %d chunks", len(chunks))

	for i, chunk := range chunks {
		msg := tgbotapi.NewMessage(chatID, chunk)
		msg.ParseMode = "Markdown"
		
		if i > 0 {
			time.Sleep(ChunkDelay)
		}
		
		sent, err := b.api.Send(msg)
		if err != nil {
			// Если Markdown не работает, попробуем без форматирования
			msg.ParseMode = ""
			sent, err = b.api.Send(msg)
		}
		
		if err != nil {
			b.logger.Printf("❌ Failed to send chunk %d/%d: %v", i+1, len(chunks), err)
			return fmt.Errorf("failed to send message chunk %d: %w", i, err)
		}
		
		b.logger.Printf("✅ Chunk %d/%d sent, ID: %d", i+1, len(chunks), sent.MessageID)
	}
	
	return nil
}

// splitMessage разбивает сообщение на части
func (b *Bot) splitMessage(text string, maxLength int) []string {
	if len(text) <= maxLength {
		return []string{text}
	}

	var chunks []string
	words := strings.Fields(text)
	var currentChunk strings.Builder

	for _, word := range words {
		if currentChunk.Len()+len(word)+1 > maxLength {
			if currentChunk.Len() > 0 {
				chunks = append(chunks, currentChunk.String())
				currentChunk.Reset()
			}
		}
		
		if currentChunk.Len() > 0 {
			currentChunk.WriteString(" ")
		}
		currentChunk.WriteString(word)
	}

	if currentChunk.Len() > 0 {
		chunks = append(chunks, currentChunk.String())
	}

	return chunks
}

// handleCommand обрабатывает команды бота
func (b *Bot) handleCommand(update tgbotapi.Update) string {
	command := update.Message.Command()
	userID := update.Message.From.ID

	b.logger.Printf("🎯 Processing command: /%s from user %d", command, userID)

	switch command {
	case "start":
		return fmt.Sprintf(`🤖 *Добро пожаловать!*

Привет, %s! Я ИИ-ассистент на базе Deepseek.
Просто напишите мне любое сообщение, и я отвечу!

Используйте /help для получения дополнительной информации.`, 
			update.Message.From.FirstName)

	case "help":
		return `🤖 *ИИ-Ассистент на базе Deepseek*

*Возможности:*
• 💻 Программирование и код-ревью
• 🧮 Математические вычисления
• 🌐 Переводы текстов
• 📝 Написание и редактирование текстов
• 🤔 Ответы на общие вопросы
• 🎓 Обучение и объяснения

*Команды:*
/start - начать работу
/help - показать помощь
/stats - статистика использования
/debug - переключить режим отладки

Просто напишите ваш вопрос!`

	case "stats":
		count := b.userStats[userID]
		return fmt.Sprintf(`📊 *Ваша статистика:*

Сообщений отправлено: %d
Пользователь ID: %d
Режим отладки: %v

Спасибо за использование бота! 🚀`, count, userID, b.debug)

	case "debug":
		b.debug = !b.debug
		b.api.Debug = b.debug
		status := "выключен"
		if b.debug {
			status = "включен"
		}
		return fmt.Sprintf("🔧 Режим отладки %s", status)

	case "test":
		return "✅ Бот работает нормально! Время: " + time.Now().Format("15:04:05")

	default:
		return "❓ Неизвестная команда. Используйте /help для получения списка команд."
	}
}

// handleMessage обрабатывает обычные сообщения
func (b *Bot) handleMessage(ctx context.Context, update tgbotapi.Update) error {
	chatID := update.Message.Chat.ID
	userID := update.Message.From.ID
	text := update.Message.Text

	b.logger.Printf("=== 📨 PROCESSING MESSAGE ===")
	b.logger.Printf("👤 User: %s (%d)", update.Message.From.UserName, userID)
	b.logger.Printf("💬 Message: %s", text)
	b.logger.Printf("🏠 Chat ID: %d", chatID)

	// Обновляем статистику
	b.userStats[userID]++
	b.logger.Printf("📈 User message count: %d", b.userStats[userID])

	// Показываем индикатор "печатает"
	typing := tgbotapi.NewChatAction(chatID, tgbotapi.ChatTyping)
	if _, err := b.api.Send(typing); err != nil {
		b.logger.Printf("⚠️ Failed to send typing action: %v", err)
	} else {
		b.logger.Printf("⌨️ Typing indicator sent")
	}

	// Отправляем сообщение о том, что думаем
	thinkingMsg := tgbotapi.NewMessage(chatID, "🤔 Обрабатываю ваш запрос...")
	sentMsg, err := b.api.Send(thinkingMsg)
	if err != nil {
		b.logger.Printf("⚠️ Failed to send thinking message: %v", err)
	} else {
		b.logger.Printf("💭 Thinking message sent with ID: %d", sentMsg.MessageID)
	}

	// Получаем ответ от ИИ
	b.logger.Printf("🚀 Calling Deepseek API...")
	startTime := time.Now()
	response, err := b.callDeepseekAPI(ctx, text)
	apiDuration := time.Since(startTime)
	
	if err != nil {
		b.logger.Printf("❌ API Error after %v: %v", apiDuration, err)
		response = fmt.Sprintf("❌ Произошла ошибка при обращении к ИИ:\n\n`%s`\n\nПопробуйте еще раз позже.", err.Error())
	} else {
		b.logger.Printf("✅ API Response received in %v: %d characters", apiDuration, len(response))
		if b.debug {
			preview := response
			if len(preview) > 100 {
				preview = preview[:100] + "..."
			}
			b.logger.Printf("📄 Response preview: %s", preview)
		}
	}

	// Удаляем сообщение "думаю"
	if sentMsg.MessageID != 0 {
		deleteMsg := tgbotapi.NewDeleteMessage(chatID, sentMsg.MessageID)
		if _, err := b.api.Send(deleteMsg); err != nil {
			b.logger.Printf("⚠️ Failed to delete thinking message: %v", err)
		} else {
			b.logger.Printf("🗑️ Thinking message deleted")
		}
	}

	// Отправляем ответ
	b.logger.Printf("📤 Sending response...")
	if err := b.sendLongMessage(chatID, response); err != nil {
		b.logger.Printf("❌ Failed to send response: %v", err)
		// Отправляем простое сообщение об ошибке
		errorMsg := tgbotapi.NewMessage(chatID, "❌ Ошибка при отправке ответа")
		if _, sendErr := b.api.Send(errorMsg); sendErr != nil {
			b.logger.Printf("❌ Failed to send error message: %v", sendErr)
		}
	} else {
		b.logger.Printf("✅ Response sent successfully")
	}

	b.logger.Printf("=== ✅ MESSAGE PROCESSED ===\n")
	return nil
}

// Run запускает бота
func (b *Bot) Run() error {
	b.logger.Printf("🚀 Bot authorized as @%s", b.api.Self.UserName)
	b.logger.Printf("🔧 Debug mode: %v", b.debug)

	// Тестируем API ключ
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	testResponse, err := b.callDeepseekAPI(ctx, "Привет! Ответь одним словом.")
	if err != nil {
		b.logger.Printf("⚠️ API test failed: %v", err)
		b.logger.Printf("🔄 Bot will continue, but API might not work")
	} else {
		b.logger.Printf("✅ API test successful: %s", testResponse)
	}

	u := tgbotapi.NewUpdate(0)
	u.Timeout = UpdateTimeout
	updates := b.api.GetUpdatesChan(u)

	b.logger.Printf("🎧 Bot is listening for updates...")

	ctx = context.Background()

	for update := range updates {
		if update.Message == nil {
			continue
		}

		updateInfo := fmt.Sprintf("Update from %s: %s", 
			update.Message.From.UserName, update.Message.Text)
		b.logger.Printf("📬 %s", updateInfo)

		// Обрабатываем команды
		if update.Message.IsCommand() {
			b.logger.Printf("🎯 Processing command")
			response := b.handleCommand(update)
			if err := b.sendLongMessage(update.Message.Chat.ID, response); err != nil {
				b.logger.Printf("❌ Failed to send command response: %v", err)
			}
			continue
		}

		// Обрабатываем обычные сообщения в горутине
		go func(upd tgbotapi.Update) {
			if err := b.handleMessage(ctx, upd); err != nil {
				b.logger.Printf("❌ Error handling message: %v", err)
			}
		}(update)
	}

	return nil
}

// testAPIConnection тестирует подключение к API
func testAPIConnection(apiKey string) error {
	log.Printf("🧪 Testing API connection...")
	
	bot := &Bot{
		apiKey: apiKey,
		client: &http.Client{Timeout: 10 * time.Second},
		logger: log.New(os.Stdout, "[TEST] ", log.LstdFlags),
		debug:  true,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	response, err := bot.callDeepseekAPI(ctx, "Test")
	if err != nil {
		return fmt.Errorf("API test failed: %w", err)
	}

	log.Printf("✅ API test successful: %s", response)
	return nil
}

// loadConfig загружает конфигурацию из переменных окружения
func loadConfig() (botToken, apiKey string, debug bool, err error) {
	if err := godotenv.Load(); err != nil {
		log.Printf("⚠️ Warning: .env file not found: %v", err)
	}

	botToken = os.Getenv("TELEGRAM_BOT_TOKEN")
	if botToken == "" {
		return "", "", false, fmt.Errorf("TELEGRAM_BOT_TOKEN not found in environment")
	}

	apiKey = os.Getenv("CHUTES_API_TOKEN")
	if apiKey == "" {
		return "", "", false, fmt.Errorf("CHUTES_API_TOKEN not found in environment")
	}

	debug = os.Getenv("DEBUG") == "true"

	return botToken, apiKey, debug, nil
}

func main() {
	log.SetFlags(log.LstdFlags | log.Lshortfile)
	log.Printf("🤖 Starting Deepseek Telegram Bot...")

	// Загружаем конфигурацию
	botToken, apiKey, debug, err := loadConfig()
	if err != nil {
		log.Fatal("❌ Configuration error:", err)
	}

	log.Printf("✅ Configuration loaded")
	log.Printf("🔑 Bot token: %s...", botToken[:min(10, len(botToken))])
	log.Printf("🔑 API key: %s...", apiKey[:min(10, len(apiKey))])
	log.Printf("🔧 Debug mode: %v", debug)

	// Тестируем API перед запуском бота
	if err := testAPIConnection(apiKey); err != nil {
		log.Printf("⚠️ API connection test failed: %v", err)
		log.Printf("🔄 Continuing anyway...")
	}

	// Создаем и запускаем бота
	bot, err := NewBot(botToken, apiKey, debug)
	if err != nil {
		log.Fatal("❌ Failed to create bot:", err)
	}

	log.Printf("🎉 Bot created successfully")

	if err := bot.Run(); err != nil {
		log.Fatal("❌ Bot error:", err)
	}
}

// min возвращает минимальное из двух чисел
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}