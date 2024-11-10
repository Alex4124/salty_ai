package main

import (
	"context"
	"log"
	"os"
	"strings"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	openai "github.com/sashabaranov/go-openai"
)

// Инициализация клиента OpenAI
var openaiClient *openai.Client

func init() {
	openaiAPIKey := os.Getenv("OPENAI_API_KEY")
	if openaiAPIKey == "" {
		log.Fatal("Переменная окружения OPENAI_API_KEY не установлена")
	}
	openaiClient = openai.NewClient(openaiAPIKey)
}

// Обработка сообщений, адресованных боту
func handleGPT(bot *tgbotapi.BotAPI, message *tgbotapi.Message) {
	chatID := message.Chat.ID
	var userQuery string

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
		Model: openai.GPT4, // Можно использовать GPT-4, если у вас есть доступ
		Messages: []openai.ChatCompletionMessage{
			{
				Role:    "user",
				Content: prompt,
			},
		},
	})
	if err != nil {
		return "", err
	}
	return resp.Choices[0].Message.Content, nil
}
