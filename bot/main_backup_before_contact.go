package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/smtp"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"

	"github.com/joho/godotenv"
	maxbot "github.com/max-messenger/max-bot-api-client-go"
)

const (
	privacyURL  = "https://xn--80aaebn4a2aiibs.xn--p1ai/politika"
	giftURL     = "https://disk.yandex.ru/d/TUXWwEoOjSgT3Q"
	siteURL     = "https://векторправа.рф"
	vkURL       = "https://vk.com/"
	maxGroupURL = "https://max.ru/id525204324295_biz"
)

type QuizState struct {
	Step int

	Phone            string
	City             string
	DebtAmount       string
	Mortgage         string
	ConsultationType string
	CallTime         string
}

var (
	stateMu        sync.Mutex
	userQuizStates = make(map[int64]*QuizState)
	userPhones     = make(map[int64]string)
)

type WebhookUpdate struct {
	UpdateType string `json:"update_type"`
	ChatID     int64  `json:"chat_id"`
	Message    struct {
		Recipient struct {
			ChatID int64 `json:"chat_id"`
			UserID int64 `json:"user_id"`
		} `json:"recipient"`
		Body struct {
			Text string `json:"text"`
		} `json:"body"`
		Text string `json:"text"`
	} `json:"message"`
}

func consentKeyboard() *maxbot.Keyboard {
	kb := &maxbot.Keyboard{}
	kb.AddRow().AddMessage("Хочу консультацию с юристом")
	kb.AddRow().AddMessage("Обработка данных")
	return kb
}

func phoneKeyboard() *maxbot.Keyboard {
	kb := &maxbot.Keyboard{}
	kb.AddRow().AddMessage("Поделиться номером")
	return kb
}

func giftKeyboard() *maxbot.Keyboard {
	kb := &maxbot.Keyboard{}
	kb.AddRow().AddMessage("Подарок")
	kb.AddRow().AddMessage("Перейти к вопросам")
	return kb
}

func startQuestionsKeyboard() *maxbot.Keyboard {
	kb := &maxbot.Keyboard{}
	kb.AddRow().AddMessage("Перейти к вопросам")
	return kb
}

func debtKeyboard() *maxbot.Keyboard {
	kb := &maxbot.Keyboard{}
	kb.AddRow().AddMessage("Долг более 300 тыс. руб")
	kb.AddRow().AddMessage("Долг менее 300 тыс. руб")
	return kb
}

func yesNoKeyboard() *maxbot.Keyboard {
	kb := &maxbot.Keyboard{}
	kb.AddRow().AddMessage("ДА").AddMessage("НЕТ")
	return kb
}

func consultationKeyboard() *maxbot.Keyboard {
	kb := &maxbot.Keyboard{}
	kb.AddRow().AddMessage("Звонок").AddMessage("Видео")
	return kb
}

func finalKeyboard() *maxbot.Keyboard {
	kb := &maxbot.Keyboard{}
	kb.AddRow().AddMessage("Официальный сайт компании")
	kb.AddRow().AddMessage("Наша страница в ВК")
	kb.AddRow().AddMessage("Наша группа в MAX")
	return kb
}

func send(ctx context.Context, api *maxbot.Api, chatID int64, text string, kb *maxbot.Keyboard) {
	msg := maxbot.NewMessage().SetChat(chatID).SetText(text)
	if kb != nil {
		msg = msg.AddKeyboard(kb)
	}

	if err := api.Messages.Send(ctx, msg); err != nil {
		fmt.Println("Ошибка отправки сообщения:", err)
	}
}

func sendWelcome(ctx context.Context, api *maxbot.Api, chatID int64) {
	stateMu.Lock()
	delete(userQuizStates, chatID)
	delete(userPhones, chatID)
	stateMu.Unlock()

	send(
		ctx,
		api,
		chatID,
		"Для продолжения работы и записи на бесплатную консультацию к юристу ЮК «Вектор Права», напишите свой номер телефона.\n\n"+
			"Нажимая кнопку ниже и продолжая общение в боте, вы соглашаетесь на обработку своих персональных данных.\n\n"+
			"Обработка данных:\n"+privacyURL+"\n\n"+
			"Ваши персональные данные будут защищены законом!\n\n"+
			"Чтобы продолжить, нажмите кнопку «Хочу консультацию с юристом».",
		consentKeyboard(),
	)
}

func sendPhoneRequest(ctx context.Context, api *maxbot.Api, chatID int64) {
	send(
		ctx,
		api,
		chatID,
		"Напишите свой номер телефона для связи.\n\n"+
			"Мы направим вам подарки:\n\n"+
			"✅ Чек-лист: «Как правильно оценить свое финансовое положение, связанное с долгами».\n"+
			"✅ Чек-лист: «Как понять, могу ли я списать свои долги законно».\n"+
			"✅ Чек-лист: «Как списать долги БЕСПЛАТНО через МФЦ».\n"+
			"✅ Чек-лист: «Самые распространённые “страшилки” МФО и коллекторов».\n\n"+
			"Кроме этого, после консультации вы получите персональный доступ в закрытый чат поддержки, где находятся клиенты компании, которые уже проходят процедуру списания долгов либо уже успешно ее завершили.\n\n"+
			"В нем проходит живое общение по всем интересующим вопросам.\n\n"+
			"Нажмите на кнопку «Поделиться номером» и напишите номер телефона — наш специалист свяжется с вами для детального разбора вашей ситуации с долгами.",
		phoneKeyboard(),
	)
}

func normalizeText(text string) string {
	return strings.TrimSpace(text)
}

func isValidPhone(text string) bool {
	cleaned := strings.TrimSpace(text)
	if cleaned == "" {
		return false
	}

	digitsCount := 0
	for _, r := range cleaned {
		switch {
		case unicode.IsDigit(r):
			digitsCount++
		case r == '+' || r == '-' || r == '(' || r == ')' || unicode.IsSpace(r):
		default:
			return false
		}
	}

	return digitsCount >= 10
}

func isDebtAmount(text string) bool {
	return text == "Долг более 300 тыс. руб" || text == "Долг менее 300 тыс. руб"
}

func isYesNo(text string) bool {
	return text == "ДА" || text == "НЕТ" || text == "Да" || text == "Нет"
}

func isConsultationType(text string) bool {
	return text == "Звонок" || text == "Видео"
}

func startQuiz(ctx context.Context, api *maxbot.Api, chatID int64) {
	stateMu.Lock()
	phone := userPhones[chatID]
	userQuizStates[chatID] = &QuizState{Step: 1, Phone: phone}
	stateMu.Unlock()

	send(ctx, api, chatID, "1. Из какого вы города?", nil)
}

func getState(chatID int64) *QuizState {
	stateMu.Lock()
	defer stateMu.Unlock()
	return userQuizStates[chatID]
}

func deleteState(chatID int64) {
	stateMu.Lock()
	delete(userQuizStates, chatID)
	delete(userPhones, chatID)
	stateMu.Unlock()
}

func setPhone(chatID int64, phone string) {
	stateMu.Lock()
	userPhones[chatID] = phone
	stateMu.Unlock()
}

func handleQuizStep(ctx context.Context, api *maxbot.Api, chatID int64, text string, state *QuizState) {
	stateMu.Lock()
	defer stateMu.Unlock()

	switch state.Step {
	case 1:
		state.City = text
		state.Step = 2
		go send(ctx, api, chatID, "2. Какой размер ваших долгов?\n\nУчитываются даже те кредиты и займы, по которым вы до сих пор платите.", debtKeyboard())
		return

	case 2:
		if !isDebtAmount(text) {
			go send(ctx, api, chatID, "Пожалуйста, выберите один из вариантов кнопкой ниже.", debtKeyboard())
			return
		}
		state.DebtAmount = text
		state.Step = 3
		go send(ctx, api, chatID, "3. Есть ли у Вас ипотека?\n\nУчитывается любая недвижимость, находящаяся в ипотеке.", yesNoKeyboard())
		return

	case 3:
		if !isYesNo(text) {
			go send(ctx, api, chatID, "Пожалуйста, ответьте кнопкой: ДА или НЕТ.", yesNoKeyboard())
			return
		}
		state.Mortgage = text
		state.Step = 4
		go send(ctx, api, chatID, "4. Вам удобна консультация по телефону или по видеосвязи?", consultationKeyboard())
		return

	case 4:
		if !isConsultationType(text) {
			go send(ctx, api, chatID, "Пожалуйста, выберите формат консультации кнопкой ниже.", consultationKeyboard())
			return
		}
		state.ConsultationType = text
		state.Step = 5
		go send(ctx, api, chatID, "5. В какое время вам удобно принять звонок?", nil)
		return

	case 5:
		state.CallTime = text
		stateCopy := *state
		delete(userQuizStates, chatID)
		delete(userPhones, chatID)

		go func() {
			if err := sendApplicationEmail(chatID, &stateCopy); err != nil {
				fmt.Println("Ошибка отправки email:", err)
				send(ctx, api, chatID, "Спасибо за ваши ответы. Но при отправке заявки произошла ошибка. Пожалуйста, попробуйте чуть позже.", nil)
				return
			}

			send(
				ctx,
				api,
				chatID,
				"Спасибо, что ответили на вопросы. Наш специалист свяжется с вами в указанное вами время, просим вас быть на связи.\n\n"+
					"Пока ожидаете связи с нашим специалистом, Вы можете более подробно узнать о нас и посмотреть отзывы о нашей работе:\n\n"+
					"Официальный сайт компании:\n"+siteURL+"\n\n"+
					"Наша страница в ВК:\n"+vkURL+"\n\n"+
					"Наша группа в MAX:\n"+maxGroupURL,
				finalKeyboard(),
			)
		}()
		return
	}
}

func sendApplicationEmail(chatID int64, state *QuizState) error {
	smtpHost := os.Getenv("SMTP_HOST")
	smtpPort := os.Getenv("SMTP_PORT")
	emailFrom := os.Getenv("EMAIL_FROM")
	emailPassword := os.Getenv("EMAIL_PASSWORD")
	emailTo := os.Getenv("EMAIL_TO")

	if smtpHost == "" || smtpPort == "" || emailFrom == "" || emailPassword == "" || emailTo == "" {
		return fmt.Errorf("не заполнены SMTP-переменные в .env")
	}

	subject := "Новая заявка из MAX-бота"
	body := fmt.Sprintf(
		"Новая заявка из MAX-бота\n\n"+
			"Chat ID: %d\n"+
			"Телефон: %s\n"+
			"Город: %s\n"+
			"Размер долга: %s\n"+
			"Ипотека: %s\n"+
			"Формат консультации: %s\n"+
			"Удобное время для звонка: %s\n",
		chatID,
		state.Phone,
		state.City,
		state.DebtAmount,
		state.Mortgage,
		state.ConsultationType,
		state.CallTime,
	)

	message := []byte(
		"To: " + emailTo + "\r\n" +
			"Subject: " + subject + "\r\n" +
			"MIME-Version: 1.0\r\n" +
			"Content-Type: text/plain; charset=\"UTF-8\"\r\n" +
			"\r\n" +
			body,
	)

	auth := smtp.PlainAuth("", emailFrom, emailPassword, smtpHost)
	return smtp.SendMail(smtpHost+":"+smtpPort, auth, emailFrom, []string{emailTo}, message)
}

func extractChatIDAndText(upd WebhookUpdate) (int64, string) {
	chatID := upd.ChatID
	if chatID == 0 {
		chatID = upd.Message.Recipient.ChatID
	}
	if chatID == 0 {
		chatID = upd.Message.Recipient.UserID
	}

	text := upd.Message.Body.Text
	if text == "" {
		text = upd.Message.Text
	}

	return chatID, normalizeText(text)
}

func handleBotUpdate(ctx context.Context, api *maxbot.Api, upd WebhookUpdate) {
	chatID, text := extractChatIDAndText(upd)
	if chatID == 0 {
		fmt.Printf("Не смог определить chat_id из update: %+v\n", upd)
		return
	}

	if upd.UpdateType == "bot_started" {
		sendWelcome(ctx, api, chatID)
		return
	}

	if upd.UpdateType != "" && upd.UpdateType != "message_created" {
		fmt.Println("Пропускаю update_type:", upd.UpdateType)
		return
	}

	if text == "" {
		return
	}

	fmt.Println("ChatID:", chatID, "Сообщение:", text)

	if state := getState(chatID); state != nil {
		handleQuizStep(ctx, api, chatID, text, state)
		return
	}

	switch text {
	case "/start", "Старт", "Начать":
		sendWelcome(ctx, api, chatID)
		return

	case "Хочу консультацию с юристом":
		sendPhoneRequest(ctx, api, chatID)
		return

	case "Обработка данных":
		send(ctx, api, chatID, "Политика обработки персональных данных:\n"+privacyURL, consentKeyboard())
		return

	case "Поделиться номером":
		send(ctx, api, chatID, "Напишите номер телефона сообщением в чат. Например: 89991234567", nil)
		return

	case "Подарок":
		send(ctx, api, chatID, "Обещанные полезные файлы можно забрать здесь:\n"+giftURL, startQuestionsKeyboard())
		return

	case "Перейти к вопросам":
		startQuiz(ctx, api, chatID)
		return

	case "Официальный сайт компании":
		send(ctx, api, chatID, siteURL, finalKeyboard())
		return

	case "Наша страница в ВК":
		send(ctx, api, chatID, vkURL, finalKeyboard())
		return

	case "Наша группа в MAX":
		send(ctx, api, chatID, maxGroupURL, finalKeyboard())
		return
	}

	if isValidPhone(text) {
		setPhone(chatID, text)
		send(ctx, api, chatID, "Обещанные полезные файлы можно забрать по кнопке «Подарок».", giftKeyboard())
		return
	}

	sendWelcome(ctx, api, chatID)
}

func webhookHandler(ctx context.Context, api *maxbot.Api, secret string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		if secret != "" && r.Header.Get("X-Max-Bot-Api-Secret") != secret {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}

		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
		if err != nil {
			http.Error(w, "failed to read body", http.StatusBadRequest)
			return
		}
		defer r.Body.Close()

		var upd WebhookUpdate
		if err := json.Unmarshal(body, &upd); err != nil {
			fmt.Println("Ошибка парсинга webhook update:", err, "body:", string(body))
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success":true}`))

		go handleBotUpdate(ctx, api, upd)
	}
}

func main() {
	if err := godotenv.Load(); err != nil {
		fmt.Println("⚠️ .env не найден, пробуем переменные системы")
	}

	token := os.Getenv("BOT_TOKEN")
	if token == "" {
		fmt.Println("Ошибка: переменная BOT_TOKEN не найдена")
		return
	}

	listenAddr := os.Getenv("LISTEN_ADDR")
	if listenAddr == "" {
		listenAddr = ":8080"
	}

	webhookSecret := os.Getenv("WEBHOOK_SECRET")
	if webhookSecret == "" {
		fmt.Println("⚠️ WEBHOOK_SECRET не задан. Webhook будет принимать запросы без проверки X-Max-Bot-Api-Secret")
	}

	api, err := maxbot.New(token, maxbot.WithApiTimeout(20*time.Second))
	if err != nil {
		fmt.Println("Ошибка создания клиента MAX:", err)
		return
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()

	info, err := api.Bots.GetBot(ctx)
	fmt.Printf("Bot info: %#v\n", info)
	fmt.Printf("Bot info error: %#v\n", err)

	mux := http.NewServeMux()
	mux.HandleFunc("/webhook", webhookHandler(ctx, api, webhookSecret))
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	server := &http.Server{
		Addr:              listenAddr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
	}

	go func() {
		fmt.Println("Webhook bot запущен. Слушаю", listenAddr)
		fmt.Println("Endpoint: /webhook")
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			fmt.Println("Ошибка HTTP-сервера:", err)
			stop()
		}
	}()

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		fmt.Println("Ошибка graceful shutdown:", err)
	}
	fmt.Println("Бот остановлен")
}
