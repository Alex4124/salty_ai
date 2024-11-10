package main

import (
	"context"
	"fmt"
	"log"
	"math/rand"
	"os"
	"sort"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	openai "github.com/sashabaranov/go-openai"
)

// Карты для хранения соответствий между userID и username
var usernameToUserID = make(map[string]int64)
var userIDToUsername = make(map[int64]string)

// Структуры для дуэлей
var duelRequests = make(map[int64]int64)      // Хранение userID инициатора и оппонента
var duelParticipants = make(map[int][2]int64) // Хранение пар userID
var currentTurn = make(map[int]int)
var userStats = make(map[int64]*UserStat) // Статистика пользователей

// Структура статистики пользователей
type UserStat struct {
	Wins   int
	Losses int
}

// Структуры для русской рулетки
type RussianRouletteGame struct {
	Participants []int64
	CurrentIndex int
	Chambers     [6]bool
	MessageID    int
}

var russianRouletteGames = make(map[int]*RussianRouletteGame)

// Инициализация клиента OpenAI
var openaiClient *openai.Client

func init() {
	// Инициализация клиента OpenAI
	openaiAPIKey := os.Getenv("OPENAI_API_KEY")
	if openaiAPIKey == "" {
		log.Fatal("Переменная окружения OPENAI_API_KEY не установлена")
	}
	openaiClient = openai.NewClient(openaiAPIKey)
}

func main() {
	// Получаем токен из переменной окружения
	token := os.Getenv("TELEGRAM_BOT_TOKEN")
	if token == "" {
		log.Fatal("Переменная окружения TELEGRAM_BOT_TOKEN не установлена")
	}

	// Создаем нового бота с помощью токена
	bot, err := tgbotapi.NewBotAPI(token)
	if err != nil {
		log.Panic(err)
	}

	bot.Debug = false // Отключаем режим отладки для продакшена
	log.Printf("Авторизован как %s", bot.Self.UserName)

	// Создаем канал для получения обновлений от Telegram
	updateConfig := tgbotapi.NewUpdate(0)
	updateConfig.Timeout = 60
	updates := bot.GetUpdatesChan(updateConfig)

	rand.Seed(time.Now().UnixNano())
	for update := range updates {
		var userID int64
		var username string

		if update.Message != nil {
			userID = update.Message.From.ID
			username = update.Message.From.UserName
			if username == "" {
				username = update.Message.From.FirstName
			}
		} else if update.CallbackQuery != nil {
			userID = update.CallbackQuery.From.ID
			username = update.CallbackQuery.From.UserName
			if username == "" {
				username = update.CallbackQuery.From.FirstName
			}
		}

		if username != "" {
			usernameToUserID[username] = userID
			userIDToUsername[userID] = username
		}

		// Пропускаем все обновления, которые не содержат сообщений и обратных вызовов
		if update.Message == nil && update.CallbackQuery == nil {
			continue
		}

		if update.Message != nil {
			// Обработка сообщений GPT
			handleGPT(bot, update.Message)

			// Обработка команд
			if update.Message.IsCommand() {
				switch update.Message.Command() {
				case "stats":
					handleStatsCommand(bot, update.Message)
				}
				continue
			}

			// Обработка сообщений для инициации дуэли или русской рулетки
			loweredText := strings.ToLower(update.Message.Text)
			if strings.Contains(loweredText, "дуэль") {
				handleDuelInitiation(bot, update.Message)
				continue
			} else if strings.Contains(loweredText, "рулетка") {
				handleRouletteInitiation(bot, update.Message)
				continue
			}
		}

		// Обработка нажатий на кнопки
		if update.CallbackQuery != nil {
			callback := update.CallbackQuery
			data := callback.Data
			callbackUserID := callback.From.ID
			chatID := callback.Message.Chat.ID

			switch {
			case strings.HasPrefix(data, "accept_duel"):
				var initiatorID int64
				var messageID int
				fmt.Sscanf(data, "accept_duel|%d|%d", &initiatorID, &messageID)
				handleAcceptDuel(bot, chatID, initiatorID, messageID, callbackUserID)
			case strings.HasPrefix(data, "reject_duel"):
				var initiatorID int64
				fmt.Sscanf(data, "reject_duel|%d", &initiatorID)
				handleRejectDuel(bot, chatID, callback.From.UserName, initiatorID)
			case strings.HasPrefix(data, "shoot"):
				var messageID int
				fmt.Sscanf(data, "shoot|%d", &messageID)
				handleShoot(bot, chatID, messageID, callbackUserID)
			case strings.HasPrefix(data, "accept_roulette"):
				var initiatorID int64
				var messageID int
				fmt.Sscanf(data, "accept_roulette|%d|%d", &initiatorID, &messageID)
				handleAcceptRoulette(bot, chatID, initiatorID, messageID, callbackUserID)
			case strings.HasPrefix(data, "reject_roulette"):
				var initiatorID int64
				fmt.Sscanf(data, "reject_roulette|%d", &initiatorID)
				handleRejectRoulette(bot, chatID, callback.From.UserName, initiatorID)
			case strings.HasPrefix(data, "pull_trigger"):
				var messageID int
				fmt.Sscanf(data, "pull_trigger|%d", &messageID)
				handlePullTrigger(bot, callback, messageID)
			}
		}
	}
}

// Обработка сообщений, адресованных боту (GPT)
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
		Model: openai.GPT3Dot5Turbo, // Можно использовать openai.GPT4, если у вас есть доступ
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

// Обработка инициации дуэли
func handleDuelInitiation(bot *tgbotapi.BotAPI, message *tgbotapi.Message) {
	chatID := message.Chat.ID
	messageID := message.MessageID
	initiatorID := message.From.ID
	userFirstName := message.From.FirstName

	// Обработка ответа на сообщение
	if message.ReplyToMessage != nil {
		opponentID := message.ReplyToMessage.From.ID
		opponentUsername := message.ReplyToMessage.From.UserName
		if opponentUsername == "" {
			opponentUsername = message.ReplyToMessage.From.FirstName
		}

		if opponentID == bot.Self.ID {
			response := "Вы не можете вызвать бота на дуэль!"
			msg := tgbotapi.NewMessage(chatID, response)
			bot.Send(msg)
			return
		}

		if opponentID == initiatorID {
			response := "Вы не можете вызвать на дуэль самого себя!"
			msg := tgbotapi.NewMessage(chatID, response)
			bot.Send(msg)
			return
		}

		// Отправляем запрос на дуэль
		response := fmt.Sprintf("%s вызывает @%s на дуэль! @%s, вы принимаете дуэль?", userFirstName, opponentUsername, opponentUsername)
		msg := tgbotapi.NewMessage(chatID, response)
		acceptButton := tgbotapi.NewInlineKeyboardButtonData("Принять", fmt.Sprintf("accept_duel|%d|%d", initiatorID, messageID))
		rejectButton := tgbotapi.NewInlineKeyboardButtonData("Отказаться", fmt.Sprintf("reject_duel|%d", initiatorID))
		msg.ReplyMarkup = tgbotapi.NewInlineKeyboardMarkup(tgbotapi.NewInlineKeyboardRow(acceptButton, rejectButton))
		bot.Send(msg)
		duelRequests[initiatorID] = opponentID
		return
	}

	// Обработка упоминаний
	if len(message.Entities) > 0 {
		for _, entity := range message.Entities {
			if entity.Type == "mention" {
				mentionedUser := message.Text[entity.Offset : entity.Offset+entity.Length]
				if mentionedUser != "@"+bot.Self.UserName {
					opponentUsername := strings.TrimPrefix(mentionedUser, "@")
					opponentUserID, ok := getUserIDByUsername(opponentUsername)
					if !ok {
						response := fmt.Sprintf("Не могу найти пользователя %s.", mentionedUser)
						msg := tgbotapi.NewMessage(chatID, response)
						bot.Send(msg)
						continue
					}

					// Отправляем запрос на дуэль
					response := fmt.Sprintf("%s вызывает %s на дуэль! %s, вы принимаете дуэль?", userFirstName, mentionedUser, mentionedUser)
					msg := tgbotapi.NewMessage(chatID, response)
					acceptButton := tgbotapi.NewInlineKeyboardButtonData("Принять", fmt.Sprintf("accept_duel|%d|%d", initiatorID, messageID))
					rejectButton := tgbotapi.NewInlineKeyboardButtonData("Отказаться", fmt.Sprintf("reject_duel|%d", initiatorID))
					msg.ReplyMarkup = tgbotapi.NewInlineKeyboardMarkup(tgbotapi.NewInlineKeyboardRow(acceptButton, rejectButton))
					bot.Send(msg)
					duelRequests[initiatorID] = opponentUserID
					return
				}
			}
		}
	}

	// Если просто написано "дуэль"
	response := "Чтобы вызвать кого-то на дуэль, ответьте на его сообщение или упомяните его."
	msg := tgbotapi.NewMessage(chatID, response)
	bot.Send(msg)
}

// Обработка принятия дуэли
func handleAcceptDuel(bot *tgbotapi.BotAPI, chatID int64, initiatorID int64, messageID int, callbackUserID int64) {
	if opponentID, ok := duelRequests[initiatorID]; ok && callbackUserID == opponentID {
		response := fmt.Sprintf("Дуэль началась между @%s и @%s!", getUsernameByID(initiatorID), getUsernameByID(opponentID))
		msg := tgbotapi.NewMessage(chatID, response)
		bot.Send(msg)
		duelParticipants[messageID] = [2]int64{initiatorID, opponentID}
		currentTurn[messageID] = rand.Intn(2) // Случайно выбираем, кто стреляет первым
		promptNextTurn(bot, chatID, messageID)
		// Удаляем запрос на дуэль
		delete(duelRequests, initiatorID)
	}
}

// Обработка отказа от дуэли
func handleRejectDuel(bot *tgbotapi.BotAPI, chatID int64, username string, initiatorID int64) {
	response := fmt.Sprintf("@%s отклонил дуэль.", username)
	msg := tgbotapi.NewMessage(chatID, response)
	bot.Send(msg)
	delete(duelRequests, initiatorID)
}

// Обработка инициации русской рулетки
func handleRouletteInitiation(bot *tgbotapi.BotAPI, message *tgbotapi.Message) {
	chatID := message.Chat.ID
	messageID := message.MessageID
	initiatorID := message.From.ID
	initiatorUsername := message.From.UserName
	if initiatorUsername == "" {
		initiatorUsername = message.From.FirstName
	}

	// Обработка ответа на сообщение
	if message.ReplyToMessage != nil {
		opponentID := message.ReplyToMessage.From.ID
		opponentUsername := message.ReplyToMessage.From.UserName
		if opponentUsername == "" {
			opponentUsername = message.ReplyToMessage.From.FirstName
		}

		if opponentID == bot.Self.ID {
			response := "Вы не можете сыграть с ботом в русскую рулетку!"
			msg := tgbotapi.NewMessage(chatID, response)
			bot.Send(msg)
			return
		}

		if opponentID == initiatorID {
			response := "Вы не можете сыграть в русскую рулетку сами с собой!"
			msg := tgbotapi.NewMessage(chatID, response)
			bot.Send(msg)
			return
		}

		// Отправляем запрос на игру
		response := fmt.Sprintf("%s предлагает @%s сыграть в русскую рулетку! @%s, вы принимаете вызов?", initiatorUsername, opponentUsername, opponentUsername)
		msg := tgbotapi.NewMessage(chatID, response)
		acceptButton := tgbotapi.NewInlineKeyboardButtonData("Принять", fmt.Sprintf("accept_roulette|%d|%d", initiatorID, messageID))
		rejectButton := tgbotapi.NewInlineKeyboardButtonData("Отказаться", fmt.Sprintf("reject_roulette|%d", initiatorID))
		msg.ReplyMarkup = tgbotapi.NewInlineKeyboardMarkup(tgbotapi.NewInlineKeyboardRow(acceptButton, rejectButton))
		bot.Send(msg)
		duelRequests[initiatorID] = opponentID
		return
	}

	// Обработка упоминаний
	if len(message.Entities) > 0 {
		participants := []int64{initiatorID}
		for _, entity := range message.Entities {
			if entity.Type == "mention" {
				mentionedUsername := strings.TrimPrefix(message.Text[entity.Offset:entity.Offset+entity.Length], "@")
				mentionedUserID, ok := getUserIDByUsername(mentionedUsername)
				if ok && mentionedUserID != initiatorID {
					participants = append(participants, mentionedUserID)
				}
			}
		}

		if len(participants) > 1 {
			startRouletteGame(bot, chatID, messageID, participants)
			return
		}
	}

	// Если просто написано "рулетка"
	startRouletteGame(bot, chatID, messageID, []int64{initiatorID})
}

// Обработка принятия русской рулетки
func handleAcceptRoulette(bot *tgbotapi.BotAPI, chatID int64, initiatorID int64, messageID int, callbackUserID int64) {
	if opponentID, ok := duelRequests[initiatorID]; ok && callbackUserID == opponentID {
		startRouletteGame(bot, chatID, messageID, []int64{initiatorID, opponentID})
		// Удаляем запрос на игру
		delete(duelRequests, initiatorID)
	}
}

// Обработка отказа от русской рулетки
func handleRejectRoulette(bot *tgbotapi.BotAPI, chatID int64, username string, initiatorID int64) {
	response := fmt.Sprintf("@%s отклонил игру в русскую рулетку.", username)
	msg := tgbotapi.NewMessage(chatID, response)
	bot.Send(msg)
	delete(duelRequests, initiatorID)
}

// Начало игры в русскую рулетку
func startRouletteGame(bot *tgbotapi.BotAPI, chatID int64, messageID int, participants []int64) {
	if len(participants) < 2 {
		response := "Для игры в русскую рулетку нужно как минимум два участника."
		msg := tgbotapi.NewMessage(chatID, response)
		bot.Send(msg)
		return
	}

	// Создаем игру
	game := &RussianRouletteGame{
		Participants: participants,
		CurrentIndex: rand.Intn(len(participants)),
		MessageID:    messageID,
	}

	// Заряжаем револьвер
	bulletPosition := rand.Intn(6)
	game.Chambers[bulletPosition] = true

	// Сохраняем игру
	russianRouletteGames[messageID] = game

	// Уведомляем участников
	response := fmt.Sprintf("Игра в русскую рулетку началась между %s!", getUsernamesByIDs(participants))
	msg := tgbotapi.NewMessage(chatID, response)
	bot.Send(msg)

	// Запрашиваем ход первого игрока
	promptNextRouletteTurn(bot, chatID, messageID)
}

// Подсказка следующего хода в русской рулетке
func promptNextRouletteTurn(bot *tgbotapi.BotAPI, chatID int64, messageID int) {
	game, ok := russianRouletteGames[messageID]
	if !ok {
		return
	}

	shooterID := game.Participants[game.CurrentIndex]
	response := fmt.Sprintf("Сейчас очередь @%s. Нажмите 'Спустить курок', чтобы сделать ход.", getUsernameByID(shooterID))
	msg := tgbotapi.NewMessage(chatID, response)
	pullTriggerButton := tgbotapi.NewInlineKeyboardButtonData("Спустить курок", fmt.Sprintf("pull_trigger|%d", messageID))
	msg.ReplyMarkup = tgbotapi.NewInlineKeyboardMarkup(tgbotapi.NewInlineKeyboardRow(pullTriggerButton))
	bot.Send(msg)
}

// Обработка нажатия на кнопку "Спустить курок"
func handlePullTrigger(bot *tgbotapi.BotAPI, callback *tgbotapi.CallbackQuery, messageID int) {
	game, ok := russianRouletteGames[messageID]
	if !ok {
		// Игра не найдена
		return
	}
	shooterID := game.Participants[game.CurrentIndex]

	if callback.From.ID != shooterID {
		// Не тот игрок
		response := fmt.Sprintf("Сейчас не ваша очередь, @%s!", getUsernameByID(callback.From.ID))
		msg := tgbotapi.NewMessage(callback.Message.Chat.ID, response)
		bot.Send(msg)
		return
	}

	// Проверяем, есть ли пуля в текущей каморе
	chamberIndex := rand.Intn(6)
	if game.Chambers[chamberIndex] {
		// Игрок проиграл
		response := fmt.Sprintf("Бах! @%s проиграл в русскую рулетку!", getUsernameByID(shooterID))
		msg := tgbotapi.NewMessage(callback.Message.Chat.ID, response)
		bot.Send(msg)

		// Обновляем статистику
		if _, exists := userStats[shooterID]; !exists {
			userStats[shooterID] = &UserStat{}
		}
		userStats[shooterID].Losses++

		// Удаляем игрока из игры
		game.Participants = append(game.Participants[:game.CurrentIndex], game.Participants[game.CurrentIndex+1:]...)

		// Проверяем, остался ли победитель
		if len(game.Participants) == 1 {
			winnerID := game.Participants[0]
			response := fmt.Sprintf("@%s победил в русской рулетке!", getUsernameByID(winnerID))
			msg := tgbotapi.NewMessage(callback.Message.Chat.ID, response)
			bot.Send(msg)

			// Обновляем статистику победителя
			if _, exists := userStats[winnerID]; !exists {
				userStats[winnerID] = &UserStat{}
			}
			userStats[winnerID].Wins++

			// Удаляем игру
			delete(russianRouletteGames, messageID)
			return
		} else if len(game.Participants) == 0 {
			// Игра окончена
			delete(russianRouletteGames, messageID)
			return
		} else {
			// Продолжаем игру
			if game.CurrentIndex >= len(game.Participants) {
				game.CurrentIndex = 0
			}
			promptNextRouletteTurn(bot, callback.Message.Chat.ID, messageID)
			return
		}
	} else {
		// Игрок выжил
		response := fmt.Sprintf("Щелчок! @%s повезло, игра продолжается.", getUsernameByID(shooterID))
		msg := tgbotapi.NewMessage(callback.Message.Chat.ID, response)
		bot.Send(msg)

		// Переходим к следующему игроку
		game.CurrentIndex = (game.CurrentIndex + 1) % len(game.Participants)
		promptNextRouletteTurn(bot, callback.Message.Chat.ID, messageID)
		return
	}
}

// Функция для подсказки следующего хода в дуэли
func promptNextTurn(bot *tgbotapi.BotAPI, chatID int64, messageID int) {
	participants := duelParticipants[messageID]
	turn := currentTurn[messageID]
	shooterID := participants[turn]
	shooterUsername := getUsernameByID(shooterID)
	response := fmt.Sprintf("@%s, ваша очередь стрелять!", shooterUsername)
	msg := tgbotapi.NewMessage(chatID, response)
	shootButton := tgbotapi.NewInlineKeyboardButtonData("Выстрелить", fmt.Sprintf("shoot|%d", messageID))
	msg.ReplyMarkup = tgbotapi.NewInlineKeyboardMarkup(tgbotapi.NewInlineKeyboardRow(shootButton))
	bot.Send(msg)
}

// Обработка выстрела в дуэли
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
		bot.Send(msg)
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
		bot.Send(msg)
		delete(duelParticipants, messageID)
		delete(currentTurn, messageID)
	} else {
		// Меняем очередь
		currentTurn[messageID] = 1 - turn
		promptNextTurn(bot, chatID, messageID)
	}
}

// Получение userID по username
func getUserIDByUsername(username string) (int64, bool) {
	userID, ok := usernameToUserID[username]
	return userID, ok
}

// Получение username по userID
func getUsernameByID(userID int64) string {
	username, ok := userIDToUsername[userID]
	if ok {
		return username
	}
	return fmt.Sprintf("%d", userID)
}

// Получение списка usernames по списку userIDs
func getUsernamesByIDs(userIDs []int64) string {
	var usernames []string
	for _, userID := range userIDs {
		usernames = append(usernames, "@"+getUsernameByID(userID))
	}
	return strings.Join(usernames, ", ")
}

// Функция для вывода общей статистики в виде турнирной таблицы
func handleStatsCommand(bot *tgbotapi.BotAPI, message *tgbotapi.Message) {
	chatID := message.Chat.ID

	// Проверяем, есть ли статистика
	if len(userStats) == 0 {
		response := "Статистика пуста. Никто еще не участвовал в играх."
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
	response := "Турнирная таблица:\n"
	for i, entry := range stats {
		response += fmt.Sprintf("%d. @%s - Побед: %d, Поражений: %d\n", i+1, entry.Username, entry.Wins, entry.Losses)
	}

	msg := tgbotapi.NewMessage(chatID, response)
	bot.Send(msg)
}
