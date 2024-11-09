package main

import (
	"fmt"
	"log"
	"math/rand"
	"os"
	"sort"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// Карты для хранения соответствий между userID и username
var usernameToUserID = make(map[string]int64)
var userIDToUsername = make(map[int64]string)

var duelRequests = make(map[int64]int64)      // Хранение userID инициатора и оппонента
var duelParticipants = make(map[int][2]int64) // Хранение пар userID
var currentTurn = make(map[int]int)
var userStats = make(map[int64]*UserStat) // Статистика пользователей

type UserStat struct {
	Wins   int
	Losses int
}

func main() {
	// Получаем токен из переменной окружения
	token := os.Getenv("TELEGRAM_BOT_TOKEN")
	if token == "" {
		log.Fatal("TELEGRAM_BOT_TOKEN не установлен")
	}

	// Создаем нового бота с помощью токена
	bot, err := tgbotapi.NewBotAPI(token)
	if err != nil {
		log.Panic(err)
	}

	bot.Debug = true // Включение режима отладки для логирования запросов и ответов
	log.Printf("Авторизован как %s", bot.Self.UserName)

	// Создаем канал для получения обновлений от Telegram
	updateConfig := tgbotapi.NewUpdate(0)
	updateConfig.Timeout = 60
	updates := bot.GetUpdatesChan(updateConfig)

	rand.Seed(time.Now().UnixNano())
	for update := range updates {
		// Обновляем карты соответствий
		if update.Message != nil {
			userID := update.Message.From.ID
			username := update.Message.From.UserName
			if username == "" {
				username = update.Message.From.FirstName
			}
			usernameToUserID[username] = userID
			userIDToUsername[userID] = username
		} else if update.CallbackQuery != nil {
			userID := update.CallbackQuery.From.ID
			username := update.CallbackQuery.From.UserName
			if username == "" {
				username = update.CallbackQuery.From.FirstName
			}
			usernameToUserID[username] = userID
			userIDToUsername[userID] = username
		}

		// Пропускаем все обновления, которые не содержат сообщений и обратных вызовов
		if update.Message == nil && update.CallbackQuery == nil {
			continue
		}

		if update.Message != nil {
			chatID := update.Message.Chat.ID
			messageID := update.Message.MessageID
			userID := update.Message.From.ID
			userFirstName := update.Message.From.FirstName

			// Обработка команды /stats
			if update.Message.IsCommand() {
				switch update.Message.Command() {
				case "stats":
					handleStatsCommand(bot, update.Message)
				}
				continue
			}

			// Обработка ответа на сообщение для инициации дуэли
			if update.Message.ReplyToMessage != nil {
				// Проверяем, содержит ли сообщение слово "дуэль" или "Дуэль" (независимо от регистра)
				if strings.Contains(strings.ToLower(update.Message.Text), "дуэль") {
					initiateDuelByReply(bot, update.Message)
					continue
				}
			}

			// Проверяем, если сообщение — это дуэль через упоминание
			if strings.Contains(strings.ToLower(update.Message.Text), "дуэль") && len(update.Message.Entities) > 0 {
				for _, entity := range update.Message.Entities {
					if entity.Type == "mention" {
						mentionedUser := update.Message.Text[entity.Offset : entity.Offset+entity.Length]
						if mentionedUser != "@"+bot.Self.UserName {
							// Получаем userID упомянутого пользователя
							opponentUsername := strings.TrimPrefix(mentionedUser, "@")
							opponentUserID, ok := getUserIDByUsername(opponentUsername)
							if !ok {
								// Если мы не знаем userID упомянутого пользователя
								response := fmt.Sprintf("Не могу найти пользователя %s.", mentionedUser)
								msg := tgbotapi.NewMessage(chatID, response)
								bot.Send(msg)
								continue
							}

							// Отправляем запрос на дуэль
							response := fmt.Sprintf("%s вызывает %s на дуэль! %s, вы принимаете дуэль?", userFirstName, mentionedUser, mentionedUser)
							msg := tgbotapi.NewMessage(chatID, response)
							acceptButton := tgbotapi.NewInlineKeyboardButtonData("Принять", fmt.Sprintf("accept_duel|%d|%d", userID, messageID))
							rejectButton := tgbotapi.NewInlineKeyboardButtonData("Отказаться", fmt.Sprintf("reject_duel|%d", userID))
							msg.ReplyMarkup = tgbotapi.NewInlineKeyboardMarkup(tgbotapi.NewInlineKeyboardRow(acceptButton, rejectButton))
							if _, err := bot.Send(msg); err != nil {
								log.Printf("Ошибка отправки сообщения: %v", err)
							}
							duelRequests[userID] = opponentUserID
							continue
						}
					}
				}
			}
		}

		// Обработка нажатий на кнопки дуэли
		if update.CallbackQuery != nil {
			callback := update.CallbackQuery
			data := callback.Data
			callbackUserID := callback.From.ID
			chatID := callback.Message.Chat.ID

			if strings.HasPrefix(data, "accept_duel") {
				var initiatorID int64
				var messageID int
				fmt.Sscanf(data, "accept_duel|%d|%d", &initiatorID, &messageID)

				// Начало дуэли
				if opponentID, ok := duelRequests[initiatorID]; ok && callbackUserID == opponentID {
					response := fmt.Sprintf("Дуэль началась между @%s и @%s!", getUsernameByID(initiatorID), getUsernameByID(opponentID))
					msg := tgbotapi.NewMessage(chatID, response)
					if _, err := bot.Send(msg); err != nil {
						log.Printf("Ошибка отправки сообщения: %v", err)
					}
					duelParticipants[messageID] = [2]int64{initiatorID, opponentID}
					currentTurn[messageID] = rand.Intn(2) // Случайно выбираем, кто стреляет первым
					promptNextTurn(bot, chatID, messageID)
					// Удаляем запрос на дуэль
					delete(duelRequests, initiatorID)
				}
				continue
			} else if strings.HasPrefix(data, "reject_duel") {
				var initiatorID int64
				fmt.Sscanf(data, "reject_duel|%d", &initiatorID)

				// Отклонение дуэли
				response := fmt.Sprintf("@%s отклонил дуэль.", callback.From.UserName)
				msg := tgbotapi.NewMessage(chatID, response)
				if _, err := bot.Send(msg); err != nil {
					log.Printf("Ошибка отправки сообщения: %v", err)
				}
				delete(duelRequests, initiatorID)
				continue
			} else if strings.HasPrefix(data, "shoot") {
				var messageID int
				fmt.Sscanf(data, "shoot|%d", &messageID)
				handleShoot(bot, chatID, messageID, callbackUserID)
				continue
			}
		}
	}
}

// Функция для инициации дуэли по ответу на сообщение
func initiateDuelByReply(bot *tgbotapi.BotAPI, message *tgbotapi.Message) {
	chatID := message.Chat.ID
	messageID := message.MessageID
	initiatorID := message.From.ID
	initiatorName := message.From.FirstName

	// Получаем информацию о пользователе, на чье сообщение ответили
	opponentMessage := message.ReplyToMessage
	opponentID := opponentMessage.From.ID
	opponentUsername := opponentMessage.From.UserName

	// Проверяем, не является ли оппонент ботом
	if opponentID == bot.Self.ID {
		response := "Вы не можете вызвать бота на дуэль!"
		msg := tgbotapi.NewMessage(chatID, response)
		bot.Send(msg)
		return
	}

	// Проверяем, не вызывает ли пользователь сам себя
	if opponentID == initiatorID {
		response := "Вы не можете вызвать на дуэль самого себя!"
		msg := tgbotapi.NewMessage(chatID, response)
		bot.Send(msg)
		return
	}

	// Обновляем карты соответствий
	if opponentUsername == "" {
		opponentUsername = opponentMessage.From.FirstName
	}
	usernameToUserID[opponentUsername] = opponentID
	userIDToUsername[opponentID] = opponentUsername

	// Отправляем запрос на дуэль
	response := fmt.Sprintf("%s вызывает @%s на дуэль! @%s, вы принимаете дуэль?", initiatorName, opponentUsername, opponentUsername)
	msg := tgbotapi.NewMessage(chatID, response)
	acceptButton := tgbotapi.NewInlineKeyboardButtonData("Принять", fmt.Sprintf("accept_duel|%d|%d", initiatorID, messageID))
	rejectButton := tgbotapi.NewInlineKeyboardButtonData("Отказаться", fmt.Sprintf("reject_duel|%d", initiatorID))
	msg.ReplyMarkup = tgbotapi.NewInlineKeyboardMarkup(tgbotapi.NewInlineKeyboardRow(acceptButton, rejectButton))
	if _, err := bot.Send(msg); err != nil {
		log.Printf("Ошибка отправки сообщения: %v", err)
	}
	duelRequests[initiatorID] = opponentID
}

func promptNextTurn(bot *tgbotapi.BotAPI, chatID int64, messageID int) {
	participants := duelParticipants[messageID]
	turn := currentTurn[messageID]
	shooterID := participants[turn]
	shooterUsername := getUsernameByID(shooterID)
	response := fmt.Sprintf("@%s, ваша очередь стрелять!", shooterUsername)
	msg := tgbotapi.NewMessage(chatID, response)
	shootButton := tgbotapi.NewInlineKeyboardButtonData("Выстрелить", fmt.Sprintf("shoot|%d", messageID))
	msg.ReplyMarkup = tgbotapi.NewInlineKeyboardMarkup(tgbotapi.NewInlineKeyboardRow(shootButton))
	if _, err := bot.Send(msg); err != nil {
		log.Printf("Ошибка отправки сообщения: %v", err)
	}
}

func handleShoot(bot *tgbotapi.BotAPI, chatID int64, messageID int, shooterID int64) {
	participants, ok := duelParticipants[messageID]
	if !ok {
		// Нет такой дуэли
		return
	}
	turn := currentTurn[messageID]
	expectedShooterID := participants[turn]

	// Проверяем, что стреляет правильный игрок
	if shooterID != expectedShooterID {
		response := fmt.Sprintf("@%s, сейчас не ваша очередь!", getUsernameByID(shooterID))
		msg := tgbotapi.NewMessage(chatID, response)
		if _, err := bot.Send(msg); err != nil {
			log.Printf("Ошибка отправки сообщения: %v", err)
		}
		return
	}

	// Случайное решение, выстрел успешен или нет
	if rand.Intn(2) == 0 {
		opponentID := participants[1-turn]

		// Обновляем статистику победителя
		if _, exists := userStats[shooterID]; !exists {
			userStats[shooterID] = &UserStat{}
		}
		userStats[shooterID].Wins++

		// Обновляем статистику проигравшего
		if _, exists := userStats[opponentID]; !exists {
			userStats[opponentID] = &UserStat{}
		}
		userStats[opponentID].Losses++

		response := fmt.Sprintf("@%s победил в дуэли!", getUsernameByID(shooterID))
		msg := tgbotapi.NewMessage(chatID, response)
		if _, err := bot.Send(msg); err != nil {
			log.Printf("Ошибка отправки сообщения: %v", err)
		}
		delete(duelParticipants, messageID)
		delete(currentTurn, messageID)
	} else {
		// Меняем очередь
		currentTurn[messageID] = 1 - turn
		promptNextTurn(bot, chatID, messageID)
	}
}

func getUserIDByUsername(username string) (int64, bool) {
	userID, ok := usernameToUserID[username]
	return userID, ok
}

func getUsernameByID(userID int64) string {
	username, ok := userIDToUsername[userID]
	if ok {
		return username
	}
	return fmt.Sprintf("%d", userID)
}

// Функция для вывода общей статистики в виде турнирной таблицы
func handleStatsCommand(bot *tgbotapi.BotAPI, message *tgbotapi.Message) {
	chatID := message.Chat.ID

	// Проверяем, есть ли статистика
	if len(userStats) == 0 {
		response := "Статистика пуста. Никто еще не участвовал в дуэлях."
		msg := tgbotapi.NewMessage(chatID, response)
		bot.Send(msg)
		return
	}

	// Создаем срез для сортировки
	type StatEntry struct {
		Username string
		Wins     int
		Losses   int
	}
	var stats []StatEntry
	for userID, stat := range userStats {
		stats = append(stats, StatEntry{
			Username: getUsernameByID(userID),
			Wins:     stat.Wins,
			Losses:   stat.Losses,
		})
	}

	// Сортируем по количеству побед
	sort.Slice(stats, func(i, j int) bool {
		return stats[i].Wins > stats[j].Wins
	})

	// Формируем сообщение со статистикой
	response := "Турнирная таблица дуэлей:\n"
	for i, entry := range stats {
		response += fmt.Sprintf("%d. @%s - Побед: %d, Поражений: %d\n", i+1, entry.Username, entry.Wins, entry.Losses)
	}

	msg := tgbotapi.NewMessage(chatID, response)
	if _, err := bot.Send(msg); err != nil {
		log.Printf("Ошибка отправки сообщения: %v", err)
	}
}
