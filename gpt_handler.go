// gpt_handler.go

package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	openai "github.com/sashabaranov/go-openai"
)

// Инициализация клиента OpenAI
var openaiClient *openai.Client

// Карты для ограничения частоты запросов
var userRequestTimes = make(map[int64]time.Time)
var requestInterval = time.Minute

// Переменные для отслеживания использования токенов
var totalTokensUsed = 0
var tokenUsageLimit = 100000 // Установите лимит токенов
var tokenUsageMutex sync.Mutex

func init() {
	// Инициализация клиента OpenAI
	openaiAPIKey := os.Getenv("OPENAI_API_KEY")
	if openaiAPIKey == "" {
		log.Fatal("Переменная окружения OPENAI_API_KEY не установлена")
	}
	openaiClient = openai.NewClient(openaiAPIKey)

	// Запускаем ежедневный сброс использования токенов
	go resetTokenUsageDaily()
}

// Обработка сообщений, адресованных боту (GPT)
func handleGPT(bot *tgbotapi.BotAPI, message *tgbotapi.Message) {
	chatID := message.Chat.ID
	userID := message.From.ID
	var userQuery string

	// Проверка ограничения частоты запросов
	if lastRequestTime, ok := userRequestTimes[userID]; ok {
		if time.Since(lastRequestTime) < requestInterval {
			remainingTime := requestInterval - time.Since(lastRequestTime)
			response := fmt.Sprintf("Пожалуйста, подождите %v перед следующим запросом.", remainingTime.Round(time.Second))
			msg := tgbotapi.NewMessage(chatID, response)
			bot.Send(msg)
			return
		}
	}

	// Проверяем, упомянут ли бот или является ли сообщение ответом на сообщение бота
	isReplyToBot := message.ReplyToMessage != nil && message.ReplyToMessage.From.ID == bot.Self.ID
	mentionsBot := false
	for _, entity := range message.Entities {
		if entity.Type == "mention" {
			mentionedUser := message.Text[entity.Offset : entity.Offset+entity.Length]
			if mentionedUser == "@"+bot.Self.UserName {
				mentionsBot = true
				break
			}
		}
	}

	if isReplyToBot || mentionsBot {
		// Извлекаем запрос пользователя
		if mentionsBot {
			// Удаляем упоминание бота из текста
			userQuery = strings.ReplaceAll(message.Text, "@"+bot.Self.UserName, "")
			userQuery = strings.TrimSpace(userQuery)
		} else if isReplyToBot {
			userQuery = message.Text
		}

		if userQuery == "" {
			response := "Пожалуйста, введите вопрос после упоминания бота."
			msg := tgbotapi.NewMessage(chatID, response)
			bot.Send(msg)
			return
		}

		// Обновляем время последнего запроса
		userRequestTimes[userID] = time.Now()

		// Проверяем, достигнут ли лимит использования токенов
		tokenUsageMutex.Lock()
		if totalTokensUsed >= tokenUsageLimit {
			tokenUsageMutex.Unlock()
			response := "Достигнут лимит использования токенов. Попробуйте позже."
			msg := tgbotapi.NewMessage(chatID, response)
			bot.Send(msg)
			return
		}
		tokenUsageMutex.Unlock()

		// Отправляем "typing action"
		typingMsg := tgbotapi.NewChatAction(chatID, tgbotapi.ChatTyping)
		bot.Send(typingMsg)

		// Отправляем запрос в OpenAI API
		responseText, err := getGPTResponse(userQuery)
		if err != nil {
			log.Printf("Ошибка при получении ответа от GPT: %v", err)
			response := "Извините, произошла ошибка при обработке вашего запроса."
			msg := tgbotapi.NewMessage(chatID, response)
			bot.Send(msg)
			return
		}

		// Отправляем ответ обратно в чат
		msg := tgbotapi.NewMessage(chatID, responseText)
		bot.Send(msg)
	}
}

// Функция для получения ответа от OpenAI GPT
func getGPTResponse(prompt string) (string, error) {
	ctx := context.Background()

	resp, err := openaiClient.CreateChatCompletion(ctx, openai.ChatCompletionRequest{
		Model:     openai.GPT3Dot5Turbo,
		MaxTokens: 150, // Ограничение длины ответа
		Messages: []openai.ChatCompletionMessage{
			{
				Role:    "user",
				Content: prompt,
			},
		},
	})
	if err != nil {
		// Проверяем тип ошибки
		if apiErr, ok := err.(*openai.APIError); ok {
			switch apiErr.HTTPStatusCode {
			case 429:
				return "", fmt.Errorf("Превышена квота использования API. Пожалуйста, проверьте настройки оплаты в вашем аккаунте OpenAI.")
			case 401:
				return "", fmt.Errorf("Недействительный API-ключ OpenAI.")
			default:
				return "", fmt.Errorf("Ошибка API OpenAI: %v", apiErr.Message)
			}
		}
		return "", err
	}

	// Обновляем общее количество использованных токенов
	tokenUsageMutex.Lock()
	totalTokensUsed += resp.Usage.TotalTokens
	tokenUsageMutex.Unlock()

	return resp.Choices[0].Message.Content, nil
}

// Функция сброса использования токенов ежедневно
func resetTokenUsageDaily() {
	for {
		now := time.Now()
		nextReset := time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, now.Location())
		durationUntilReset := nextReset.Sub(now)
		time.Sleep(durationUntilReset)
		tokenUsageMutex.Lock()
		totalTokensUsed = 0
		tokenUsageMutex.Unlock()
	}
}
