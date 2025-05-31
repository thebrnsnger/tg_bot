package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/joho/godotenv"
)

type DeepseekRequest struct {
	Model    string            `json:"model"`
	Messages []DeepseekMessage `json:"messages"`
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
}

func callDeepseekAPI(apiKey, userMessage string) (string, error) {
	url := "https://api.deepseek.com/v1/chat/completions"

	requestBody := DeepseekRequest{
		Model: "deepseek-chat",
		Messages: []DeepseekMessage{
			{
				Role:    "system",
				Content: "You are a helpful assistant. Respond in the same language as the user's message.",
			},
			{
				Role:    "user",
				Content: userMessage,
			},
		},
	}

	jsonData, err := json.Marshal(requestBody)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequest("POST", url, bytes.NewBuffer(jsonData))
	if err != nil {
		return "", err
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	log.Printf("Status code: %d", resp.StatusCode)
	log.Printf("Response body: %s", string(body))

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("API error (status %d): %s", resp.StatusCode, string(body))
	}

	var deepseekResp DeepseekResponse
	if err := json.Unmarshal(body, &deepseekResp); err != nil {
		return "", fmt.Errorf("failed to parse response: %v", err)
	}

	if len(deepseekResp.Choices) > 0 {
		return deepseekResp.Choices[0].Message.Content, nil
	}

	return "Извините, не удалось получить ответ", nil
}

func main() {
	err := godotenv.Load()
	if err != nil {
		log.Fatal("Ошибка загрузки .env файла")
	}

	botToken := os.Getenv("TELEGRAM_BOT_TOKEN")
	if botToken == "" {
		log.Fatal("TELEGRAM_BOT_TOKEN не найден в .env файле")
	}

	apiKey := os.Getenv("CHUTES_API_TOKEN")
	if apiKey == "" {
		log.Fatal("CHUTES_API_TOKEN не найден в .env файле")
	}

	log.Printf("API Key prefix: %s...", apiKey[:10])

	bot, err := tgbotapi.NewBotAPI(botToken)
	if err != nil {
		log.Panic(err)
	}

	bot.Debug = false
	log.Printf("Бот авторизован как %s", bot.Self.UserName)

	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60
	updates := bot.GetUpdatesChan(u)

	for update := range updates {
		if update.Message == nil {
			continue
		}

		userID := update.Message.From.ID
		text := update.Message.Text

		log.Printf("[%s] %s", update.Message.From.UserName, text)

		var response string

		switch text {
		case "/start":
			response = "Привет! Я ИИ-бот на базе Deepseek. Задайте мне любой вопрос!"

		case "/help":
			response = `🤖 Я ИИ-ассистент на базе Deepseek!

Просто напишите мне любое сообщение, и я отвечу.
Я могу помочь с:
• Программированием
• Математикой
• Переводами
• Общими вопросами

Команды:
/start - начать
/help - помощь`

		default:
			thinkingMsg := tgbotapi.NewMessage(update.Message.Chat.ID, "⏳ Думаю...")
			sentMsg, _ := bot.Send(thinkingMsg)

			aiResponse, err := callDeepseekAPI(apiKey, text)
			if err != nil {
				log.Printf("Ошибка API: %v", err)
				response = fmt.Sprintf("Ошибка: %v", err)
			} else {
				response = aiResponse
			}

			deleteMsg := tgbotapi.NewDeleteMessage(update.Message.Chat.ID, sentMsg.MessageID)
			bot.Send(deleteMsg)
		}

		msg := tgbotapi.NewMessage(update.Message.Chat.ID, response)
		msg.ParseMode = "Markdown"
		
		if len(response) > 4096 {
			for i := 0; i < len(response); i += 4096 {
				end := i + 4096
				if end > len(response) {
					end = len(response)
				}
				partMsg := tgbotapi.NewMessage(update.Message.Chat.ID, response[i:end])
				partMsg.ParseMode = "Markdown"
				bot.Send(partMsg)
				time.Sleep(100 * time.Millisecond)
			}
		} else {
			bot.Send(msg)
		}

		log.Printf("Ответ отправлен пользователю %d", userID)
	}
}