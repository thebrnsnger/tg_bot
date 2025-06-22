package main

import (
	"bytes"
	"database/sql" // Добавлено для работы с БД
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
	_ "github.com/mattn/go-sqlite3" // Импорт драйвера SQLite
)

const (
	// Обновленный URL API для Hugging Face Inference API с Mistral-Small-3.2-24B-Instruct-2506
	APIURL = "https://api-inference.huggingface.co/models/mistralai/Mistral-Small-3.2-24B-Instruct-2506"
	// Модель, которую мы используем на Hugging Face (указывается в запросе, если API того требует)
	// В данном случае URL уже включает модель, но константа может быть полезна для ясности или других API
	MODEL  = "mistralai/Mistral-Small-3.2-24B-Instruct-2506"
	DBPATH = "database/users.db" // Путь к файлу базы данных
)

// Config хранит токены API
type Config struct {
	TelegramBotToken    string
	HuggingFaceAPIToken string // Переименовано для ясности
}

// ChatMessage представляет сообщение в диалоге (роль и содержимое)
type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// OpenAIRequest - структура запроса, совместимая с OpenAI-подобными API
// Hugging Face Inference API часто имитирует этот формат для chat/instruct моделей
type OpenAIRequest struct {
	Model     string        `json:"model"`
	Messages  []ChatMessage `json:"messages"`
	Stream    bool          `json:"stream"`
	MaxTokens int           `json:"max_tokens"`
	// Temperature float64       `json:"temperature"` // Не все Hugging Face API поддерживают это напрямую в таком формате, но можно оставить
}

// Choice представляет один из вариантов ответа AI
type Choice struct {
	Message ChatMessage `json:"message"`
}

// ChatResponse - структура ответа от AI
type ChatResponse struct {
	Choices []Choice `json:"choices"`
}

// Bot содержит конфигурацию, API-клиенты и соединение с БД
type Bot struct {
	config *Config
	api    *tgbotapi.BotAPI
	db     *sql.DB // Добавлено соединение с БД
}

func main() {
	config := loadConfig()

	if config.TelegramBotToken == "" || config.HuggingFaceAPIToken == "" {
		log.Fatal("Ошибка: Установите TELEGRAM_BOT_TOKEN и HF_API_TOKEN в файле .env")
	}

	// Инициализация базы данных
	db, err := initDB()
	if err != nil {
		log.Fatalf("Ошибка инициализации базы данных: %v", err)
	}
	defer db.Close() // Убедитесь, что соединение с базой данных закрыто

	// Инициализация бота Telegram
	api, err := tgbotapi.NewBotAPI(config.TelegramBotToken)
	if err != nil {
		log.Fatalf("Ошибка создания бота: %v", err)
	}

	bot := &Bot{
		config: config,
		api:    api,
		db:     db, // Присваиваем соединение с БД
	}

	log.Printf("Бот запущен: @%s", api.Self.UserName)

	// Настройка обновлений
	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60

	updates := api.GetUpdatesChan(u)

	// Обработка обновлений
	for update := range updates {
		bot.handleUpdate(update)
	}
}

// loadConfig загружает конфигурацию из переменных окружения или .env файла
func loadConfig() *Config {
	err := godotenv.Load()
	if err != nil {
		fmt.Println("Предупреждение: .env файл не найден, используя переменные окружения")
	}

	return &Config{
		TelegramBotToken:    os.Getenv("TELEGRAM_BOT_TOKEN"),
		HuggingFaceAPIToken: os.Getenv("HF_API_TOKEN"), // Используем HF_API_TOKEN из .env
	}
}

// initDB инициализирует соединение с SQLite базой данных и создает таблицу users
func initDB() (*sql.DB, error) {
	// Создаем папку database если её нет
	err := os.MkdirAll("database", 0755)
	if err != nil {
		return nil, fmt.Errorf("ошибка создания папки базы данных: %w", err)
	}

	db, err := sql.Open("sqlite3", DBPATH)
	if err != nil {
		return nil, fmt.Errorf("ошибка открытия базы данных: %w", err)
	}

	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS users (
			user_id INTEGER PRIMARY KEY,
			style TEXT DEFAULT 'friendly'
		)
	`)
	if err != nil {
		return nil, fmt.Errorf("ошибка создания таблицы пользователей: %w", err)
	}
	return db, nil
}

// setUserStyle сохраняет или обновляет стиль пользователя в БД
func (b *Bot) setUserStyle(userID int64, style string) error {
	// Использование UPSERT (INSERT OR REPLACE или INSERT OR IGNORE + UPDATE)
	// Для SQLite обычно используется INSERT OR REPLACE INTO или INSERT OR IGNORE + UPDATE
	_, err := b.db.Exec("INSERT OR IGNORE INTO users (user_id, style) VALUES (?, ?)", userID, style)
	if err != nil {
		return fmt.Errorf("ошибка при вставке пользователя: %w", err)
	}
	_, err = b.db.Exec("UPDATE users SET style = ? WHERE user_id = ?", style, userID)
	if err != nil {
		return fmt.Errorf("ошибка при обновлении стиля пользователя: %w", err)
	}
	return nil
}

// getUserStyle получает стиль пользователя из БД или возвращает 'friendly' по умолчанию
func (b *Bot) getUserStyle(userID int64) (string, error) {
	var style string
	err := b.db.QueryRow("SELECT style FROM users WHERE user_id = ?", userID).Scan(&style)
	if err == sql.ErrNoRows {
		return "friendly", nil // Стиль по умолчанию, если пользователь не найден
	}
	if err != nil {
		return "", fmt.Errorf("ошибка при получении стиля пользователя: %w", err)
	}
	return style, nil
}

// sendWelcome отправляет приветственное сообщение
func (b *Bot) sendWelcome(message *tgbotapi.Message) {
	text := "👋 Привет! Я бот с искусственным интеллектом, использующий модель Mistral Small 3.2. Просто напиши мне любое сообщение, и я отвечу!\n\nЧтобы выбрать стиль общения, напиши /style"

	msg := tgbotapi.NewMessage(message.Chat.ID, text)
	msg.ReplyToMessageID = message.MessageID

	_, err := b.api.Send(msg)
	if err != nil {
		log.Printf("Ошибка отправки сообщения: %v", err)
	}
}

// chooseStyle предлагает пользователю выбрать стиль общения через кнопки
func (b *Bot) chooseStyle(message *tgbotapi.Message) {
	keyboard := tgbotapi.NewReplyKeyboard(
		tgbotapi.NewKeyboardButtonRow(
			tgbotapi.NewKeyboardButton("Дружелюбный 😊"),
			tgbotapi.NewKeyboardButton("Официальный 🧐"),
		),
		tgbotapi.NewKeyboardButtonRow(
			tgbotapi.NewKeyboardButton("Мемный 🤪"),
		),
	)
	keyboard.ResizeKeyboard = true // Делает клавиатуру компактной

	msg := tgbotapi.NewMessage(message.Chat.ID, "Выбери стиль общения:")
	msg.ReplyMarkup = keyboard
	msg.ReplyToMessageID = message.MessageID

	_, err := b.api.Send(msg)
	if err != nil {
		log.Printf("Ошибка отправки сообщения: %v", err)
	}
}

// setStyle устанавливает выбранный пользователем стиль
func (b *Bot) setStyle(message *tgbotapi.Message) {
	styleMapping := map[string]string{
		"Дружелюбный 😊": "friendly",
		"Официальный 🧐": "official",
		"Мемный 🤪":     "meme",
	}

	selectedStyle, ok := styleMapping[message.Text]
	if !ok {
		// Если текст не соответствует известной кнопке стиля, ничего не делаем
		return
	}

	err := b.setUserStyle(message.From.ID, selectedStyle)
	if err != nil {
		log.Printf("Ошибка сохранения стиля: %v", err)
		return
	}

	msg := tgbotapi.NewMessage(message.Chat.ID, fmt.Sprintf("Стиль общения установлен: %s", message.Text))
	msg.ReplyMarkup = tgbotapi.NewRemoveKeyboard(true) // Удаляем клавиатуру после выбора
	msg.ReplyToMessageID = message.MessageID

	_, err = b.api.Send(msg)
	if err != nil {
		log.Printf("Ошибка отправки сообщения: %v", err)
	}
}

// aiChat обрабатывает текстовые сообщения и отправляет их в ИИ
func (b *Bot) aiChat(message *tgbotapi.Message) {
	userPrompt := strings.TrimSpace(message.Text)

	// Не реагируем на выбор стиля как на чат-запрос
	styleButtons := []string{"Дружелюбный 😊", "Официальный 🧐", "Мемный 🤪"}
	for _, btn := range styleButtons {
		if userPrompt == btn {
			b.setStyle(message) // Обрабатываем как выбор стиля
			return
		}
	}

	if userPrompt == "" {
		msg := tgbotapi.NewMessage(message.Chat.ID, "Пожалуйста, напиши текстовое сообщение.")
		msg.ReplyToMessageID = message.MessageID
		_, err := b.api.Send(msg)
		if err != nil {
			log.Printf("Ошибка отправки сообщения: %v", err)
		}
		return
	}

	// Получаем стиль пользователя из БД
	style, err := b.getUserStyle(message.From.ID)
	if err != nil {
		log.Printf("Ошибка получения стиля пользователя: %v", err)
		style = "friendly" // Возвращаемся к дружелюбному стилю по умолчанию
	}

	// Формируем системный промпт в зависимости от стиля
	stylePrompts := map[string]string{
		"friendly": "Ты дружелюбный и теплый ассистент, отвечаешь с использованием эмодзи.",
		"official": "Ты официальный, строгий и вежливый ассистент. Отвечай без эмодзи.",
		"meme":     "Ты ассистент, любящий юмор и мемы. Отвечай с забавными фразами и мемами.",
	}
	systemPrompt, exists := stylePrompts[style]
	if !exists {
		systemPrompt = stylePrompts["friendly"] // По умолчанию дружелюбный
	}

	// Отправляем сообщение о том, что думаем
	thinkingMsg := tgbotapi.NewMessage(message.Chat.ID, "⌛ Думаю...")
	thinkingMsg.ReplyToMessageID = message.MessageID
	sentMsg, err := b.api.Send(thinkingMsg)
	if err != nil {
		log.Printf("Ошибка отправки сообщения: %v", err)
		return
	}

	// Запрос к AI
	aiResponse, err := b.makeAIRequest(systemPrompt, userPrompt)
	if err != nil {
		// Удаляем сообщение "Думаю..."
		deleteMsg := tgbotapi.NewDeleteMessage(message.Chat.ID, sentMsg.MessageID)
		b.api.Send(deleteMsg) // Отправляем без проверки ошибки

		errorMsg := tgbotapi.NewMessage(message.Chat.ID, fmt.Sprintf("Ошибка при обращении к ИИ: %v", err))
		errorMsg.ReplyToMessageID = message.MessageID
		b.api.Send(errorMsg)
		return
	}

	// Удаляем сообщение "Думаю..."
	deleteMsg := tgbotapi.NewDeleteMessage(message.Chat.ID, sentMsg.MessageID)
	b.api.Send(deleteMsg) // Отправляем без проверки ошибки

	// Отправляем ответ AI
	responseMsg := tgbotapi.NewMessage(message.Chat.ID, aiResponse)
	responseMsg.ParseMode = tgbotapi.ModeMarkdown // Mistral часто возвращает Markdown
	_, err = b.api.Send(responseMsg)
	if err != nil {
		log.Printf("Ошибка отправки ответа AI: %v", err)
	}
}

// makeAIRequest отправляет запрос к Hugging Face Inference API для чат-моделей
func (b *Bot) makeAIRequest(systemPrompt, userPrompt string) (string, error) {
	reqBody := OpenAIRequest{
		Model: MODEL, // Используем константу MODEL
		Messages: []ChatMessage{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: userPrompt},
		},
		Stream:    false,
		MaxTokens: 1024,
		// Temperature: 0.7, // Опционально, не все HF API поддерживают напрямую
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("ошибка маршалинга запроса: %w", err)
	}

	req, err := http.NewRequest("POST", APIURL, bytes.NewBuffer(jsonData))
	if err != nil {
		return "", fmt.Errorf("ошибка создания HTTP-запроса: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+b.config.HuggingFaceAPIToken)
	req.Header.Set("Content-Type", "application/json") // Важно для JSON-тела

	client := &http.Client{
		Timeout: 90 * time.Second, // Увеличиваем таймаут для больших моделей
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("ошибка выполнения HTTP-запроса: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("API вернул ошибку %d: %s", resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("ошибка чтения тела ответа: %w", err)
	}

	var chatResp ChatResponse
	err = json.Unmarshal(body, &chatResp)
	if err != nil {
		return "", fmt.Errorf("ошибка демаршалинга ответа: %w", err)
	}

	if len(chatResp.Choices) == 0 {
		return "", fmt.Errorf("нет ответа от AI")
	}

	return chatResp.Choices[0].Message.Content, nil
}

// handleUpdate обрабатывает входящие обновления от Telegram
func (b *Bot) handleUpdate(update tgbotapi.Update) {
	if update.Message == nil {
		return
	}

	message := update.Message

	// Обработка команд
	if message.IsCommand() {
		switch message.Command() {
		case "start":
			b.sendWelcome(message)
		case "style":
			b.chooseStyle(message)
		default:
			msg := tgbotapi.NewMessage(message.Chat.ID, "Неизвестная команда. Используйте /start или /style.")
			msg.ReplyToMessageID = message.MessageID
			b.api.Send(msg)
		}
	} else {
		// Обработка обычных текстовых сообщений
		if message.Text != "" {
			b.aiChat(message) // Вызываем функцию для обработки чата
		}
	}
}

// min вспомогательная функция, которая теперь не нужна, но оставлена на всякий случай
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}