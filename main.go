package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/joho/godotenv"
	_ "github.com/mattn/go-sqlite3" // Import for SQLite driver
)

const (
	// Обновленный URL API для OpenRouter
	APIURL = "https://openrouter.ai/api/v1/chat/completions" 
	// Пример модели OpenRouter, выберите ту, которая соответствует вашим потребностям
	MODEL = "mistralai/mistral-7b-instruct" 
	DBPATH = "database/users.db"
)

type Config struct {
	TelegramBotToken string
	OpenRouterAPIToken string // Переименовано для ясности
}

type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type OpenAIRequest struct {
	Model       string        `json:"model"`
	Messages    []ChatMessage `json:"messages"`
	Stream      bool          `json:"stream"`
	MaxTokens   int           `json:"max_tokens"`
	Temperature float64       `json:"temperature"`
}

type Choice struct {
	Message ChatMessage `json:"message"`
}

type ChatResponse struct {
	Choices []Choice `json:"choices"`
}

type Bot struct {
	config *Config
	api    *tgbotapi.BotAPI
	db     *sql.DB
}

func main() {
	config := loadConfig()

	if config.TelegramBotToken == "" || config.OpenRouterAPIToken == "" {
		log.Fatal("Ошибка: Установите TELEGRAM_BOT_TOKEN и CHUTES_API_TOKEN в файле .env (CHUTES_API_TOKEN теперь используется для токена OpenRouter)")
	}

	// Инициализация базы данных
	db, err := initDB()
	if err != nil {
		log.Fatalf("Ошибка инициализации базы данных: %v", err)
	}
	defer db.Close() // Убедитесь, что соединение с базой данных закрыто

	// Инициализация бота
	api, err := tgbotapi.NewBotAPI(config.TelegramBotToken)
	if err != nil {
		log.Fatalf("Ошибка создания бота: %v", err)
	}

	bot := &Bot{
		config: config,
		api:    api,
		db:     db,
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

func loadConfig() *Config {
	err := godotenv.Load()
	if err != nil {
		fmt.Println("Предупреждение: .env файл не найден, используя переменные окружения")
	}

	return &Config{
		TelegramBotToken:   os.Getenv("TELEGRAM_BOT_TOKEN"),
		OpenRouterAPIToken: os.Getenv("CHUTES_API_TOKEN"), // Используем CHUTES_API_TOKEN из .env
	}
}

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

func (b *Bot) setUserStyle(userID int64, style string) error {
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

func (b *Bot) getUserStyle(userID int64) (string, error) {
	var style string
	err := b.db.QueryRow("SELECT style FROM users WHERE user_id = ?", userID).Scan(&style)
	if err == sql.ErrNoRows {
		return "friendly", nil // Стиль по умолчанию, если не найден
	}
	if err != nil {
		return "", fmt.Errorf("ошибка при получении стиля пользователя: %w", err)
	}
	return style, nil
}

func (b *Bot) sendWelcome(message *tgbotapi.Message) {
	text := "👋 Привет! Я бот с искусственным интеллектом. Просто напиши мне любое сообщение, и я отвечу с помощью ИИ!\n\nЧтобы выбрать стиль общения, напиши /style"

	msg := tgbotapi.NewMessage(message.Chat.ID, text)
	msg.ReplyToMessageID = message.MessageID

	_, err := b.api.Send(msg)
	if err != nil {
		log.Printf("Ошибка отправки сообщения: %v", err)
	}
}

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
	keyboard.ResizeKeyboard = true

	msg := tgbotapi.NewMessage(message.Chat.ID, "Выбери стиль общения:")
	msg.ReplyMarkup = keyboard
	msg.ReplyToMessageID = message.MessageID

	_, err := b.api.Send(msg)
	if err != nil {
		log.Printf("Ошибка отправки сообщения: %v", err)
	}
}

func (b *Bot) setStyle(message *tgbotapi.Message) {
	styleMapping := map[string]string{
		"Дружелюбный 😊": "friendly",
		"Официальный 🧐": "official",
		"Мемный 🤪":     "meme",
	}

	selectedStyle, ok := styleMapping[message.Text]
	if !ok {
		// Если текст не соответствует известной кнопке стиля, ничего не делаем или отправляем ошибку
		return
	}

	err := b.setUserStyle(message.From.ID, selectedStyle)
	if err != nil {
		log.Printf("Ошибка сохранения стиля: %v", err)
		return
	}

	msg := tgbotapi.NewMessage(message.Chat.ID, fmt.Sprintf("Стиль общения установлен: %s", message.Text))
	msg.ReplyMarkup = tgbotapi.NewRemoveKeyboard(true)
	msg.ReplyToMessageID = message.MessageID

	_, err = b.api.Send(msg)
	if err != nil {
		log.Printf("Ошибка отправки сообщения: %v", err)
	}
}

func (b *Bot) aiChat(message *tgbotapi.Message) {
	userPrompt := strings.TrimSpace(message.Text)

	// Не реагируем на выбор стиля как на чат
	styleButtons := []string{"Дружелюбный 😊", "Официальный 🧐", "Мемный 🤪"}
	for _, btn := range styleButtons {
		if userPrompt == btn {
			b.setStyle(message)
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

	style, err := b.getUserStyle(message.From.ID)
	if err != nil {
		log.Printf("Ошибка получения стиля пользователя: %v", err)
		style = "friendly" // Возвращаемся к дружелюбному стилю по умолчанию
	}

	stylePrompts := map[string]string{
		"friendly": "Отвечай дружелюбно и тепло, с эмодзи.",
		"official": "Отвечай официально, строго и вежливо.",
		"meme":     "Отвечай с юмором и мемами, добавляй забавные фразы.",
	}
	systemPrompt, exists := stylePrompts[style]
	if !exists {
		systemPrompt = stylePrompts["friendly"] // По умолчанию дружелюбный, если стиль не найден
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
		b.api.Send(deleteMsg)

		errorMsg := tgbotapi.NewMessage(message.Chat.ID, fmt.Sprintf("Ошибка при обращении к ИИ: %v", err))
		errorMsg.ReplyToMessageID = message.MessageID
		b.api.Send(errorMsg)
		return
	}

	// Удаляем сообщение "Думаю..."
	deleteMsg := tgbotapi.NewDeleteMessage(message.Chat.ID, sentMsg.MessageID)
	b.api.Send(deleteMsg)

	// Отправляем ответ AI
	responseMsg := tgbotapi.NewMessage(message.Chat.ID, aiResponse)
	responseMsg.ParseMode = tgbotapi.ModeMarkdown // OpenRouter часто возвращает Markdown
	_, err = b.api.Send(responseMsg)
	if err != nil {
		log.Printf("Ошибка отправки ответа AI: %v", err)
	}
}

func (b *Bot) makeAIRequest(systemPrompt, userPrompt string) (string, error) {
	reqBody := OpenAIRequest{
		Model: MODEL,
		Messages: []ChatMessage{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: userPrompt},
		},
		Stream:      false,
		MaxTokens:   1024,
		Temperature: 0.7,
	}

	jsonData, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("ошибка маршалинга запроса: %w", err)
	}

	req, err := http.NewRequest("POST", APIURL, bytes.NewBuffer(jsonData))
	if err != nil {
		return "", fmt.Errorf("ошибка создания запроса: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+b.config.OpenRouterAPIToken) // Используем OpenRouterAPIToken
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{
		Timeout: 60 * time.Second,
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("ошибка выполнения HTTP запроса: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
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
			// Обработка неизвестных команд при необходимости
			msg := tgbotapi.NewMessage(message.Chat.ID, "Неизвестная команда.")
			msg.ReplyToMessageID = message.MessageID
			b.api.Send(msg)
		}
	} else {
		// Обработка обычных текстовых сообщений
		if message.Text != "" {
			b.aiChat(message)
		}
	}
}