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
}

// NewBot создает новый экземпляр бота
func NewBot(botToken, apiKey string) (*Bot, error) {
	api, err := tgbotapi.NewBotAPI(botToken)
	if err != nil {
		return nil, fmt.Errorf("failed to create bot API: %w", err)
	}

	return &Bot{
		api:    api,
		apiKey: apiKey,
		client: &http.Client{Timeout: APITimeout},
		logger: log.New(os.Stdout, "[BOT] ", log.LstdFlags|log.Lshortfile),
		userStats: make(map[int64]int),
	}, nil
}

// callDeepseekAPI вызывает API Deepseek с улучшенной обработкой ошибок
func (b *Bot) callDeepseekAPI(ctx context.Context, userMessage string) (string, error) {
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

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(jsonData))
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+b.apiKey)

	resp, err := b.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("API request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response: %w", err)
	}

	b.logger.Printf("API Status: %d", resp.StatusCode)

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("API error (status %d): %s", resp.StatusCode, string(body))
	}

	var deepseekResp DeepseekResponse
	if err := json.Unmarshal(body, &deepseekResp); err != nil {
		return "", fmt.Errorf("failed to parse response: %w", err)
	}

	if deepseekResp.Error != nil {
		return "", fmt.Errorf("API error: %s", deepseekResp.Error.Message)
	}

	if len(deepseekResp.Choices) == 0 {
		return "Извините, не удалось получить ответ от ИИ", nil
	}

	return strings.TrimSpace(deepseekResp.Choices[0].Message.Content), nil
}

// sendLongMessage отправляет длинные сообщения частями
func (b *Bot) sendLongMessage(chatID int64, text string) error {
	if len(text) <= MaxMessageLength {
		msg := tgbotapi.NewMessage(chatID, text)
		msg.ParseMode = "Markdown"
		_, err := b.api.Send(msg)
		return err
	}

	// Разбиваем на части
	chunks := b.splitMessage(text, MaxMessageLength)
	for i, chunk := range chunks {
		msg := tgbotapi.NewMessage(chatID, chunk)
		msg.ParseMode = "Markdown"
		
		if i > 0 {
			time.Sleep(ChunkDelay)
		}
		
		if _, err := b.api.Send(msg); err != nil {
			return fmt.Errorf("failed to send message chunk %d: %w", i, err)
		}
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

Просто напишите ваш вопрос!`

	case "stats":
		count := b.userStats[userID]
		return fmt.Sprintf(`📊 *Ваша статистика:*

Сообщений отправлено: %d
Пользователь ID: %d

Спасибо за использование бота! 🚀`, count, userID)

	default:
		return "Неизвестная команда. Используйте /help для получения списка команд."
	}
}

// handleMessage обрабатывает обычные сообщения
func (b *Bot) handleMessage(ctx context.Context, update tgbotapi.Update) error {
	chatID := update.Message.Chat.ID
	userID := update.Message.From.ID
	text := update.Message.Text

	// Обновляем статистику
	b.userStats[userID]++

	b.logger.Printf("User: %s (%d), Message: %s", 
		update.Message.From.UserName, userID, text)

	// Показываем индикатор "печатает"
	typing := tgbotapi.NewChatAction(chatID, tgbotapi.ChatTyping)
	b.api.Send(typing)

	// Отправляем сообщение о том, что думаем
	thinkingMsg := tgbotapi.NewMessage(chatID, "🤔 Обрабатываю ваш запрос...")
	sentMsg, err := b.api.Send(thinkingMsg)
	if err != nil {
		b.logger.Printf("Failed to send thinking message: %v", err)
	}

	// Получаем ответ от ИИ
	response, err := b.callDeepseekAPI(ctx, text)
	if err != nil {
		b.logger.Printf("API Error: %v", err)
		response = fmt.Sprintf("❌ Произошла ошибка при обращении к ИИ:\n`%s`\n\nПопробуйте еще раз позже.", err.Error())
	}

	// Удаляем сообщение "думаю"
	if sentMsg.MessageID != 0 {
		deleteMsg := tgbotapi.NewDeleteMessage(chatID, sentMsg.MessageID)
		b.api.Send(deleteMsg)
	}

	// Отправляем ответ
	if err := b.sendLongMessage(chatID, response); err != nil {
		b.logger.Printf("Failed to send response: %v", err)
		// Отправляем простое сообщение об ошибке
		errorMsg := tgbotapi.NewMessage(chatID, "❌ Ошибка при отправке ответа")
		b.api.Send(errorMsg)
	}

	return nil
}

// Run запускает бота
func (b *Bot) Run() error {
	b.logger.Printf("Bot authorized as %s", b.api.Self.UserName)

	u := tgbotapi.NewUpdate(0)
	u.Timeout = UpdateTimeout
	updates := b.api.GetUpdatesChan(u)

	ctx := context.Background()

	for update := range updates {
		if update.Message == nil {
			continue
		}

		// Обрабатываем команды
		if update.Message.IsCommand() {
			response := b.handleCommand(update)
			if err := b.sendLongMessage(update.Message.Chat.ID, response); err != nil {
				b.logger.Printf("Failed to send command response: %v", err)
			}
			continue
		}

		// Обрабатываем обычные сообщения
		go func(upd tgbotapi.Update) {
			if err := b.handleMessage(ctx, upd); err != nil {
				b.logger.Printf("Error handling message: %v", err)
			}
		}(update)
	}

	return nil
}

// loadConfig загружает конфигурацию из переменных окружения
func loadConfig() (botToken, apiKey string, err error) {
	if err := godotenv.Load(); err != nil {
		log.Printf("Warning: .env file not found: %v", err)
	}

	botToken = os.Getenv("TELEGRAM_BOT_TOKEN")
	if botToken == "" {
		return "", "", fmt.Errorf("TELEGRAM_BOT_TOKEN not found in environment")
	}

	apiKey = os.Getenv("CHUTES_API_TOKEN")
	if apiKey == "" {
		return "", "", fmt.Errorf("CHUTES_API_TOKEN not found in environment")
	}

	return botToken, apiKey, nil
}

func main() {
	// Загружаем конфигурацию
	botToken, apiKey, err := loadConfig()
	if err != nil {
		log.Fatal(err)
	}

	log.Printf("Starting bot with API key: %s...", apiKey[:min(10, len(apiKey))])

	// Создаем и запускаем бота
	bot, err := NewBot(botToken, apiKey)
	if err != nil {
		log.Fatal(err)
	}

	if err := bot.Run(); err != nil {
		log.Fatal(err)
	}
}

// min возвращает минимальное из двух чисел
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}