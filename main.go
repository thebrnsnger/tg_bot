package main

import (
	"bytes"
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

type ClaudeRequest struct {
	Model    string    `json:"model"`
	Messages []Message `json:"messages"`
	MaxTokens int      `json:"max_tokens"`
}

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type ClaudeResponse struct {
	Content []struct {
		Text string `json:"text"`
	} `json:"content"`
}

func callClaudeAPI(apiKey, userMessage string) (string, error) {
	url := "https://api.anthropic.com/v1/messages"
	
	requestBody := ClaudeRequest{
		Model: "claude-3-haiku-20240307",
		Messages: []Message{
			{
				Role:    "user",
				Content: userMessage,
			},
		},
		MaxTokens: 1000,
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
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")

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

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("API error: %s", string(body))
	}

	var claudeResp ClaudeResponse
	if err := json.Unmarshal(body, &claudeResp); err != nil {
		return "", err
	}

	if len(claudeResp.Content) > 0 {
		return claudeResp.Content[0].Text, nil
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

	bot, err := tgbotapi.NewBotAPI(botToken)
	if err != nil {
		log.Panic(err)
	}

	bot.Debug = true
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

		switch {
		case text == "/start":
			response = "Привет! Я ИИ-бот. Задайте мне любой вопрос, и я постараюсь помочь!"
		
		case text == "/help":
			response = `🤖 Я ИИ-ассистент!
			
Просто напишите мне любое сообщение, и я отвечу.
Можете спрашивать о чем угодно:
• Помощь с программированием
• Объяснение сложных тем
• Творческие задачи
• Общие вопросы

Команды:
/start - начать
/help - помощь`

		default:
			msg := tgbotapi.NewMessage(update.Message.Chat.ID, "⏳ Думаю...")
			bot.Send(msg)

			aiResponse, err := callClaudeAPI(apiKey, text)
			if err != nil {
				log.Printf("Ошибка API: %v", err)
				response = "Извините, произошла ошибка при обработке вашего запроса."
			} else {
				response = aiResponse
			}
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