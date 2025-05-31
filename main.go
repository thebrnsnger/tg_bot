package main

import (
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/joho/godotenv"
)

type TodoItem struct {
	ID     int    `json:"id"`
	Text   string `json:"text"`
	Done   bool   `json:"done"`
	UserID int64  `json:"user_id"`
}

type TodoList struct {
	Items []TodoItem `json:"items"`
	mu    sync.Mutex
}

var todoLists = make(map[int64]*TodoList)

func (tl *TodoList) AddItem(text string, userID int64) TodoItem {
	tl.mu.Lock()
	defer tl.mu.Unlock()

	item := TodoItem{
		ID:     len(tl.Items) + 1,
		Text:   text,
		Done:   false,
		UserID: userID,
	}
	tl.Items = append(tl.Items, item)
	return item
}

func (tl *TodoList) RemoveItem(id int) bool {
	tl.mu.Lock()
	defer tl.mu.Unlock()

	for i, item := range tl.Items {
		if item.ID == id {
			tl.Items = append(tl.Items[:i], tl.Items[i+1:]...)
			return true
		}
	}
	return false
}

func (tl *TodoList) ToggleItem(id int) bool {
	tl.mu.Lock()
	defer tl.mu.Unlock()

	for i, item := range tl.Items {
		if item.ID == id {
			tl.Items[i].Done = !tl.Items[i].Done
			return true
		}
	}
	return false
}

func (tl *TodoList) ListItems() string {
	tl.mu.Lock()
	defer tl.mu.Unlock()

	if len(tl.Items) == 0 {
		return "📝 Список задач пуст"
	}

	var result strings.Builder
	for _, item := range tl.Items {
		status := "❌"
		if item.Done {
			status = "✅"
		}
		result.WriteString(fmt.Sprintf("%d. %s %s\n", item.ID, status, item.Text))
	}
	return result.String()
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

		time.Sleep(500 * time.Millisecond)

		userID := update.Message.From.ID
		if _, exists := todoLists[userID]; !exists {
			todoLists[userID] = &TodoList{}
		}

		text := update.Message.Text
		var response string

		log.Printf("[%s] %s", update.Message.From.UserName, text)

		switch {
		case strings.HasPrefix(text, "/add "):
			task := strings.TrimPrefix(text, "/add ")
			if task == "" {
				response = "Используйте: /add <текст задачи>"
			} else {
				item := todoLists[userID].AddItem(task, userID)
				response = fmt.Sprintf("✅ Задача добавлена: %s", item.Text)
			}

		case text == "/add":
			response = "Используйте: /add <текст задачи>"

		case strings.HasPrefix(text, "/remove "):
			idStr := strings.TrimPrefix(text, "/remove ")
			id, err := strconv.Atoi(idStr)
			if err != nil {
				response = "❌ Неверный ID задачи"
			} else if todoLists[userID].RemoveItem(id) {
				response = "✅ Задача удалена"
			} else {
				response = "❌ Задача не найдена"
			}

		case strings.HasPrefix(text, "/toggle "):
			idStr := strings.TrimPrefix(text, "/toggle ")
			id, err := strconv.Atoi(idStr)
			if err != nil {
				response = "❌ Неверный ID задачи"
			} else if todoLists[userID].ToggleItem(id) {
				response = "✅ Статус задачи изменен"
			} else {
				response = "❌ Задача не найдена"
			}

		case text == "/list":
			response = todoLists[userID].ListItems()

		case text == "/help" || text == "/start":
			response = `📋 Доступные команды:
/add <текст> - добавить задачу
/remove <id> - удалить задачу
/toggle <id> - изменить статус задачи
/list - показать список задач
/help - показать это сообщение`

		default:
			response = "Используйте /help для просмотра доступных команд"
		}

		msg := tgbotapi.NewMessage(update.Message.Chat.ID, response)
		if _, err := bot.Send(msg); err != nil {
			log.Printf("Ошибка отправки сообщения: %v", err)
		}
	}
}