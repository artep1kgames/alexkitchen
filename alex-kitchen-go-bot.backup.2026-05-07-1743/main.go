package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/joho/godotenv"
	maxbot "github.com/max-messenger/max-bot-api-client-go"
	"github.com/max-messenger/max-bot-api-client-go/schemes"
)

type CatalogItem struct {
	Name  string
	Price string
}

type CatalogSection struct {
	Button string
	Title  string
	Intro  string
	Items  []CatalogItem
	Footer string
}

type OrderDraft struct {
	Step      string
	Name      string
	Phone     string
	Products  string
	Receive   string
	Comment   string
	UpdatedAt time.Time
}

var (
	ordersMu sync.Mutex
	orders   = map[int64]*OrderDraft{}

	adminPostMu sync.Mutex
	adminPosts  = map[int64]bool{}
)

var catalog = []CatalogSection{
	{
		Button: "🥩 Свежее мясо",
		Title:  "🥩 Свежее мясо",
		Intro:  "Свежая продукция Alex Kitchen / proМясо",
		Items: []CatalogItem{
			{"Свинина окорок", "422 ₽/кг"},
			{"Свинина грудинка", "443 ₽/кг"},
			{"Свинина лопатка", "422 ₽/кг"},
			{"Свинина шея", "631 ₽/кг"},
			{"Свинина вырезка", "681 ₽/кг"},
			{"Свинина карбонад", "543 ₽/кг"},
			{"Свинина корейка", "531 ₽/кг"},
			{"Свиной задок", "234 ₽/кг"},
			{"Говядина шейный отруб", "956 ₽/кг"},
			{"Говядина толстый край", "1031 ₽/кг"},
			{"Говядина лопатка", "956 ₽/кг"},
			{"Говяжьи рёбрышки", "731 ₽/кг"},
			{"Говядина тазобедренный отруб", "956 ₽/кг"},
			{"Мозговые косточки", "290 ₽/кг"},
			{"Куриное бедро", "268 ₽/кг"},
			{"Куриное крыло", "363 ₽/кг"},
			{"Куриная голень", "306 ₽/кг"},
			{"Филе ЦБ", "458 ₽/кг"},
			{"Окорочок", "293 ₽/кг"},
			{"Курочка БР", "254 ₽/кг"},
			{"Грудка на кости", "395 ₽/кг"},
			{"Голень индейки", "349 ₽/кг"},
			{"Филе грудки индейки", "749 ₽/кг"},
			{"Филе бедра индейки", "756 ₽/кг"},
			{"Утка тушка", "528 ₽/кг"},
			{"Утиные окорочка", "829 ₽/кг"},
			{"Перепёлка", "249 ₽/шт"},
		},
		Footer: "Самовывоз: дополнительная скидка 5%",
	},
	{
		Button: "🍖 Крафт",
		Title:  "🍖 Крафтовое мясное производство",
		Intro:  "Деликатесы, копчёности и мясные изделия собственного производства",
		Items: []CatalogItem{
			{"Окорок Альпийский", "975 ₽/кг"},
			{"Буженина домашняя", "1280 ₽/кг"},
			{"Шея яблочно-имбирная", "1370 ₽/кг"},
			{"Карбонад медовый", "1130 ₽/кг"},
			{"Грудинка цыганская", "890 ₽/кг"},
			{"Орех ветчинный мускат", "940 ₽/кг"},
			{"Курица Голден", "595 ₽/кг"},
			{"Перепёлка", "300 ₽/шт"},
			{"Коппа СВ, вызревание от 180 дней", "370 ₽/50 г"},
			{"Хамон СК, вызревание от 60 дней", "360 ₽/100 г"},
			{"Рулька ВК по-баварски чеснок/перец", "790 ₽/кг"},
			{"Уши по-царски чеснок/хрен", "860 ₽/кг"},
			{"Слайсы кабан", "260 ₽/100 г"},
			{"Слайсы дичи", "290 ₽/100 г"},
		},
		Footer: "Самовывоз: скидка 10%",
	},
	{
		Button: "🔥 Гриль",
		Title:  "🔥 Для сковороды, гриля и мангала",
		Intro:  "Купаты, колбаски и шашлык для дома, дачи и мероприятий",
		Items: []CatalogItem{
			{"Купаты классические свинина", "550 ₽/кг"},
			{"Купаты классические курица", "590 ₽/кг"},
			{"Купаты шашлычные свинина", "570 ₽/кг"},
			{"Колбаски мюнхенские курица", "740 ₽/кг"},
			{"Колбаски чоризо свинина", "730 ₽/кг"},
			{"Шашлык Рождённый в СССР, свинина микс", "460 ₽/кг"},
			{"Шашлык Кавказский, свинина микс", "460 ₽/кг"},
			{"Шашлык Клюква/Киви, свиная шея", "830 ₽/кг"},
			{"Шашлык куриный микс", "430 ₽/кг"},
		},
		Footer: "Можно оформить заказ на нужную дату",
	},
	{
		Button: "🍔 Фаст-фуд",
		Title:  "🍔 Фаст-фуд",
		Intro:  "Готовое рваное мясо для бургеров, шаурмы, сэндвичей, доставки и кафе",
		Items: []CatalogItem{
			{"Рваная куриная грудка", "861 ₽/кг"},
			{"Рваная грудка индейки", "1282 ₽/кг"},
			{"Рваная свинина", "680 ₽/кг"},
			{"Рваная говядина", "1732 ₽/кг"},
		},
		Footer: "Мясо уже приготовлено, порвано и заморожено. Подходит для шаурмы, бургеров, сэндвичей и других блюд.",
	},
}

var broadcasts = []string{
	"Доброе утро! Alex Kitchen / proМясо уже принимает заказы. Напишите в бот, что нужно подготовить: свежее мясо, деликатесы, шашлык или продукцию для фаст-фуда.",
	"Доброе утро! Сегодня принимаем заказы на свежее мясо, крафтовые мясные изделия и готовое рваное мясо для фаст-фуда. Напишите в бот — поможем с выбором.",
	"Доброе утро! Пора пополнить запасы вкусного мяса. Каталог доступен в боте, заказы принимаем текстом.",
	"Доброе утро! Alex Kitchen на связи. Принимаем заказы на свинину, говядину, курицу, индейку, деликатесы и шашлыки.",
	"Доброе утро! Сегодня можно оформить заказ через бота. Просто напишите, что нужно, количество и удобный способ получения.",
	"Доброе утро! Напоминаем: в боте доступен каталог продукции и приём заказов. Самовывоз — со скидкой.",
	"Доброе утро! Готовим заказы на сегодня. Свежее мясо, крафтовые изделия, шашлык и рваное мясо для фаст-фуда — всё можно заказать через бота.",
	"Доброе утро! Хотите заказать мясо без лишних звонков? Откройте бот, выберите раздел каталога и отправьте заказ текстом.",
	"Доброе утро! Alex Kitchen / proМясо принимает заказы. Для кафе, фаст-фуда и дома — подберём нужную продукцию.",
	"Доброе утро! Каталог обновлён в боте. Пишите заказ сообщением — администратор свяжется для подтверждения.",
}

func mainKeyboard() *maxbot.Keyboard {
	kb := &maxbot.Keyboard{}
	kb.AddRow().AddMessage("📋 Каталог").AddMessage("🛒 Сделать заказ")
	kb.AddRow().AddMessage("🥩 Свежее мясо").AddMessage("🍖 Крафт")
	kb.AddRow().AddMessage("🔥 Гриль").AddMessage("🍔 Фаст-фуд")
	kb.AddRow().AddMessage("🚚 Доставка").AddMessage("💬 Администратор")
	return kb
}

func adminMainKeyboard() *maxbot.Keyboard {
	kb := mainKeyboard()
	kb.AddRow().AddMessage("📝 Пост в канал")
	return kb
}

func keyboardForChat(chatID int64, adminChatIDs []int64) *maxbot.Keyboard {
	if isAdminChat(chatID, adminChatIDs) {
		return adminMainKeyboard()
	}
	return mainKeyboard()
}

func catalogKeyboard() *maxbot.Keyboard {
	kb := &maxbot.Keyboard{}
	kb.AddRow().AddMessage("🥩 Свежее мясо").AddMessage("🍖 Крафт")
	kb.AddRow().AddMessage("🔥 Гриль").AddMessage("🍔 Фаст-фуд")
	kb.AddRow().AddMessage("🛒 Сделать заказ").AddMessage("🏠 Главное меню")
	return kb
}

func orderKeyboard() *maxbot.Keyboard {
	kb := &maxbot.Keyboard{}
	kb.AddRow().AddMessage("📋 Каталог").AddMessage("🚚 Доставка")
	kb.AddRow().AddMessage("💬 Администратор").AddMessage("🏠 Главное меню")
	return kb
}

func receiveKeyboard() *maxbot.Keyboard {
	kb := &maxbot.Keyboard{}
	kb.AddRow().AddMessage("Самовывоз").AddMessage("Доставка")
	kb.AddRow().AddMessage("❌ Отменить заказ")
	return kb
}

func cancelKeyboard() *maxbot.Keyboard {
	kb := &maxbot.Keyboard{}
	kb.AddRow().AddMessage("❌ Отменить заказ").AddMessage("🏠 Главное меню")
	return kb
}

func phoneKeyboard() *maxbot.Keyboard {
	kb := &maxbot.Keyboard{}
	row := kb.AddRow()

	// Если в SDK MAX есть метод AddContact / AddRequestContact, он сработает.
	// Если метода нет, останется обычная кнопка и пользователь сможет ввести телефон текстом.
	rowValue := reflect.ValueOf(row)
	for _, methodName := range []string{"AddContact", "AddRequestContact", "AddRequestPhone", "AddRequestContactButton"} {
		method := rowValue.MethodByName(methodName)
		if method.IsValid() && method.Type().NumIn() == 1 && method.Type().In(0).Kind() == reflect.String {
			method.Call([]reflect.Value{reflect.ValueOf("📱 Поделиться контактом")})
			kb.AddRow().AddMessage("✍️ Ввести телефон вручную")
			kb.AddRow().AddMessage("❌ Отменить заказ").AddMessage("🏠 Главное меню")
			return kb
		}
	}

	row.AddMessage("📱 Поделиться контактом")
	kb.AddRow().AddMessage("✍️ Ввести телефон вручную")
	kb.AddRow().AddMessage("❌ Отменить заказ").AddMessage("🏠 Главное меню")
	return kb
}

func priceBotLink() string {
	for _, name := range []string{"PRICE_BOT_URL", "BOT_LINK", "BOT_URL"} {
		if value := strings.TrimSpace(os.Getenv(name)); value != "" {
			return value
		}
	}
	return "https://max.ru/id503100167323_bot"
}

func priceButtonKeyboard(link string) *maxbot.Keyboard {
	kb := &maxbot.Keyboard{}
	row := kb.AddRow()

	rowValue := reflect.ValueOf(row)
	for _, methodName := range []string{"AddLink", "AddURL", "AddUrl", "AddLinkButton", "AddURLButton", "AddUrlButton"} {
		method := rowValue.MethodByName(methodName)
		if !method.IsValid() || method.Type().NumIn() != 2 {
			continue
		}
		if method.Type().In(0).Kind() == reflect.String && method.Type().In(1).Kind() == reflect.String {
			method.Call([]reflect.Value{reflect.ValueOf("📋 Прайс"), reflect.ValueOf(link)})
			return kb
		}
	}

	row.AddMessage("📋 Прайс")
	return kb
}

func sendPricePostToChannel(ctx context.Context, api *maxbot.Api, chatID int64, text string) bool {
	link := priceBotLink()

	msg := maxbot.NewMessage().SetChat(chatID).SetText(text).AddKeyboard(priceButtonKeyboard(link))

	if err := api.Messages.Send(ctx, msg); err != nil {
		log.Printf("Ошибка публикации поста в канал chat_id=%d: %v", chatID, err)
		return false
	}

	log.Printf("price post sent to public chat_id=%d", chatID)
	return true
}

func send(ctx context.Context, api *maxbot.Api, chatID int64, text string, kb *maxbot.Keyboard) {
	msg := maxbot.NewMessage().SetChat(chatID).SetText(text)
	if kb != nil {
		msg = msg.AddKeyboard(kb)
	}
	if err := api.Messages.Send(ctx, msg); err != nil {
		log.Printf("Ошибка отправки сообщения в chat_id=%d: %v", chatID, err)
	}
}

// sendPrivateToUser отправляет сообщение лично пользователю.
// Важно: при вступлении нового пользователя в общий чат НЕ отправляем приветствие в chat_id группы,
// чтобы его не увидели остальные подписчики.
func sendPrivateToUser(ctx context.Context, api *maxbot.Api, userID int64, text string, kb *maxbot.Keyboard) bool {
	if userID == 0 {
		log.Println("Не удалось отправить личное приветствие: user_id пустой")
		return false
	}

	msg := maxbot.NewMessage().SetText(text)

	// В разных версиях SDK метод адресации пользователя мог называться по-разному.
	// Поэтому пробуем несколько вариантов через reflection, чтобы код был устойчивее к версии SDK.
	msgValue := reflect.ValueOf(msg)
	methodNames := []string{
		"SetUser",
		"SetUserID",
		"SetUserId",
		"SetRecipientUser",
		"SetRecipientUserID",
		"SetRecipientUserId",
	}

	for _, methodName := range methodNames {
		method := msgValue.MethodByName(methodName)
		if !method.IsValid() || method.Type().NumIn() != 1 {
			continue
		}

		argType := method.Type().In(0)
		var result []reflect.Value
		switch argType.Kind() {
		case reflect.Int64:
			result = method.Call([]reflect.Value{reflect.ValueOf(userID)})
		case reflect.Int, reflect.Int32:
			result = method.Call([]reflect.Value{reflect.ValueOf(userID).Convert(argType)})
		default:
			continue
		}

		// Если метод вернул обновленный *Message — используем его.
		if len(result) > 0 && !result[0].IsNil() {
			if updatedMsg, ok := result[0].Interface().(*maxbot.Message); ok {
				msg = updatedMsg
			}
		}

		if kb != nil {
			msg = msg.AddKeyboard(kb)
		}

		if err := api.Messages.Send(ctx, msg); err != nil {
			log.Printf("Ошибка личной отправки user_id=%d через %s: %v", userID, methodName, err)
			return false
		}

		log.Printf("Личное приветствие отправлено user_id=%d через %s", userID, methodName)
		return true
	}

	// Запасной безопасный вариант: НЕ отправляем в общий чат.
	// Если в SDK нет метода отправки по user_id, лучше ничего не отправить, чем показать приветствие всем.
	log.Printf("В SDK не найден метод отправки личного сообщения пользователю user_id=%d. В общий чат приветствие не отправляю.", userID)
	return false
}

func sendToAdmins(ctx context.Context, api *maxbot.Api, adminChatIDs []int64, text string) {
	if len(adminChatIDs) == 0 {
		log.Println("ADMIN_CHAT_ID / ADMIN_CHAT_IDS не указан, сообщение админу не отправлено")
		return
	}
	for _, adminChatID := range adminChatIDs {
		send(ctx, api, adminChatID, text, nil)
	}
}

func menuText() string {
	return `🔥 Alex Kitchen / proМясо

Свежие поставки каждый день.

У нас:
— фермерское мясо
— крафтовые деликатесы
— шашлыки и купаты
— продукция для фаст-фуда

Работаем ежедневно.
Быстро принимаем и подтверждаем заказы.

Выберите раздел ниже или напишите заказ сразу.`
}

func welcomeNewSubscriberText(name string) string {
	name = strings.TrimSpace(name)

	if name == "" {
		name = "Добро пожаловать"
	} else {
		name = name + ", добро пожаловать"
	}

	return fmt.Sprintf(`%s в Alex Kitchen / proМясо.

Я Alex Kitchen — ваш проводник в мир свежего мяса, крафтовых деликатесов, шашлыков, гриля и готовых решений для фаст-фуда.

Здесь вас ждёт гастрономическое удовольствие:
— свежие поставки
— мясо для дома, кафе и доставки
— крафтовые мясные изделия
— шашлыки, купаты и гриль
— продукция для бургеров, шаурмы и сэндвичей

Откройте каталог и выберите то, что нужно. Заказ можно оформить прямо через бота.`, name)
}

func catalogMenuText() string {
	return `📋 Каталог Alex Kitchen

Выберите нужный раздел:

🥩 Свежее мясо — свинина, говядина, курица, индейка, утка
🍖 Крафт — буженина, карбонад, грудинка, деликатесы
🔥 Гриль — купаты, колбаски, шашлыки
🍔 Фаст-фуд — рваное мясо для бургеров, шаурмы, сэндвичей и доставки

Для заказа нажмите «🛒 Сделать заказ».`
}

func sectionText(section CatalogSection) string {
	var b strings.Builder
	b.WriteString(section.Title)
	b.WriteString("\n\n")
	if section.Intro != "" {
		b.WriteString(section.Intro)
		b.WriteString("\n\n")
	}
	for _, item := range section.Items {
		b.WriteString("— ")
		b.WriteString(item.Name)
		b.WriteString(" — ")
		b.WriteString(item.Price)
		b.WriteString("\n")
	}
	if section.Footer != "" {
		b.WriteString("\n")
		b.WriteString(section.Footer)
	}
	b.WriteString("\n\nДля заказа нажмите «🛒 Сделать заказ».")
	return b.String()
}

func orderStartText() string {
	return `🛒 Оформление заказа

Сейчас быстро соберём заявку по шагам.

Шаг 1 из 5.
Напишите ваше имя.`
}

func phoneRequestText() string {
	return `Шаг 5 из 5.
Оставьте телефон для связи.

Можно нажать кнопку «📱 Поделиться контактом», чтобы не вводить номер вручную.

Если кнопка не передаст контакт автоматически — просто напишите номер сообщением.`
}

func deliveryText() string {
	return `🚚 Доставка и самовывоз

График работы: с 11:00 до 19:00.

Можно оформить:
— самовывоз
— доставку по согласованию с администратором

Для оформления заказа нажмите «🛒 Сделать заказ».`
}

func adminText() string {
	return `💬 Связь с администратором

Напишите ваш вопрос следующим сообщением.
Администратор получит его и свяжется с вами.`
}

func findSectionByButton(text string) *CatalogSection {
	for i := range catalog {
		if catalog[i].Button == text {
			return &catalog[i]
		}
	}
	return nil
}

func startOrder(chatID int64) {
	ordersMu.Lock()
	defer ordersMu.Unlock()
	orders[chatID] = &OrderDraft{Step: "name", UpdatedAt: time.Now()}
}

func cancelOrder(chatID int64) {
	ordersMu.Lock()
	defer ordersMu.Unlock()
	delete(orders, chatID)
}

func getOrder(chatID int64) (*OrderDraft, bool) {
	ordersMu.Lock()
	defer ordersMu.Unlock()
	order, ok := orders[chatID]
	return order, ok
}

func saveOrder(chatID int64, order *OrderDraft) {
	ordersMu.Lock()
	defer ordersMu.Unlock()
	order.UpdatedAt = time.Now()
	orders[chatID] = order
}

func processOrderContact(ctx context.Context, api *maxbot.Api, adminChatIDs []int64, chatID int64, userID int64, userName string, phone string) bool {
	order, ok := getOrder(chatID)
	if !ok || order.Step != "phone" || strings.TrimSpace(phone) == "" {
		return false
	}

	order.Phone = strings.TrimSpace(phone)
	adminMessage := formatOrderForAdmin(order, userID, userName, chatID)
	sendToAdmins(ctx, api, adminChatIDs, adminMessage)
	cancelOrder(chatID)

	send(ctx, api, chatID, "Спасибо! Заявка передана администратору. С вами свяжутся для подтверждения заказа.", mainKeyboard())
	return true
}

func processOrderStep(ctx context.Context, api *maxbot.Api, adminChatIDs []int64, chatID int64, userID int64, userName string, text string) bool {
	order, ok := getOrder(chatID)
	if !ok {
		return false
	}

	if text == "❌ Отменить заказ" || strings.EqualFold(text, "отмена") {
		cancelOrder(chatID)
		send(ctx, api, chatID, "Заказ отменён. Вы вернулись в главное меню.", mainKeyboard())
		return true
	}

	if text == "🏠 Главное меню" {
		cancelOrder(chatID)
		send(ctx, api, chatID, menuText(), keyboardForChat(chatID, adminChatIDs))
		return true
	}

	switch order.Step {
	case "name":
		order.Name = text
		order.Step = "products"
		saveOrder(chatID, order)
		send(ctx, api, chatID, "Шаг 2 из 5.\nНапишите, что нужно заказать и в каком количестве.\n\nНапример: свиная шея 3 кг, купаты 2 кг.", cancelKeyboard())
		return true

	case "products":
		order.Products = text
		order.Step = "receive"
		saveOrder(chatID, order)
		send(ctx, api, chatID, "Шаг 3 из 5.\nВыберите способ получения.", receiveKeyboard())
		return true

	case "receive":
		order.Receive = text
		order.Step = "comment"
		saveOrder(chatID, order)
		send(ctx, api, chatID, "Шаг 4 из 5.\nНапишите комментарий к заказу.\n\nНапример: заберу сегодня после 17:00.\nЕсли комментария нет — напишите «нет».", cancelKeyboard())
		return true

	case "comment":
		order.Comment = text
		order.Step = "phone"
		saveOrder(chatID, order)
		send(ctx, api, chatID, phoneRequestText(), phoneKeyboard())
		return true

	case "phone":
		if text == "📱 Поделиться контактом" {
			send(ctx, api, chatID, "Нажмите системную кнопку отправки контакта, если она появилась. Если нет — просто напишите номер телефона сообщением.", phoneKeyboard())
			return true
		}

		if text == "✍️ Ввести телефон вручную" {
			send(ctx, api, chatID, "Напишите номер телефона сообщением.", cancelKeyboard())
			return true
		}

		order.Phone = text
		adminMessage := formatOrderForAdmin(order, userID, userName, chatID)
		sendToAdmins(ctx, api, adminChatIDs, adminMessage)
		cancelOrder(chatID)
		send(ctx, api, chatID, "Спасибо! Заявка передана администратору. С вами свяжутся для подтверждения заказа.", mainKeyboard())
		return true
	}

	return false
}

func formatOrderForAdmin(order *OrderDraft, userID int64, userName string, chatID int64) string {
	return fmt.Sprintf(`🛒 Новая заявка из MAX

Клиент: %s
User ID: %d
Chat ID: %d

Имя: %s
Телефон: %s
Заказ: %s
Получение: %s
Комментарий: %s`, userName, userID, chatID, order.Name, order.Phone, order.Products, order.Receive, order.Comment)
}

func looksLikeOrder(text string) bool {
	lower := strings.ToLower(text)
	words := []string{
		"заказ", "телефон", "+7", "доставка", "самовывоз", "кг", "килограмм",
		"свинина", "говядина", "курица", "индейка", "купаты", "шашлык",
		"шея", "грудинка", "карбонад", "рваная", "филе", "окорок", "лопатка",
	}
	for _, w := range words {
		if strings.Contains(lower, w) {
			return true
		}
	}
	return false
}

func extractContactPhone(message any) string {
	return findStringByFieldName(reflect.ValueOf(message), map[string]bool{
		"Phone":       true,
		"PhoneNumber": true,
		"Telephone":   true,
		"MobilePhone": true,
	}, 0)
}

func findStringByFieldName(v reflect.Value, names map[string]bool, depth int) string {
	if depth > 6 || !v.IsValid() {
		return ""
	}

	if v.Kind() == reflect.Pointer || v.Kind() == reflect.Interface {
		if v.IsNil() {
			return ""
		}
		return findStringByFieldName(v.Elem(), names, depth+1)
	}

	switch v.Kind() {
	case reflect.Struct:
		t := v.Type()
		for i := 0; i < v.NumField(); i++ {
			field := t.Field(i)
			value := v.Field(i)

			if names[field.Name] && value.Kind() == reflect.String {
				phone := strings.TrimSpace(value.String())
				if phone != "" {
					return phone
				}
			}

			if result := findStringByFieldName(value, names, depth+1); result != "" {
				return result
			}
		}

	case reflect.Slice, reflect.Array:
		for i := 0; i < v.Len(); i++ {
			if result := findStringByFieldName(v.Index(i), names, depth+1); result != "" {
				return result
			}
		}

	case reflect.Map:
		for _, key := range v.MapKeys() {
			if key.Kind() == reflect.String && names[key.String()] {
				value := v.MapIndex(key)
				if value.Kind() == reflect.String {
					phone := strings.TrimSpace(value.String())
					if phone != "" {
						return phone
					}
				}
			}
			if result := findStringByFieldName(v.MapIndex(key), names, depth+1); result != "" {
				return result
			}
		}
	}

	return ""
}

func extractInt64ByFieldName(obj any, fieldNames map[string]bool) int64 {
	return findInt64ByFieldName(reflect.ValueOf(obj), fieldNames, 0)
}

func findInt64ByFieldName(v reflect.Value, names map[string]bool, depth int) int64 {
	if depth > 6 || !v.IsValid() {
		return 0
	}

	if v.Kind() == reflect.Pointer || v.Kind() == reflect.Interface {
		if v.IsNil() {
			return 0
		}
		return findInt64ByFieldName(v.Elem(), names, depth+1)
	}

	switch v.Kind() {
	case reflect.Struct:
		t := v.Type()
		for i := 0; i < v.NumField(); i++ {
			field := t.Field(i)
			value := v.Field(i)

			if names[field.Name] {
				switch value.Kind() {
				case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
					return value.Int()
				case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
					return int64(value.Uint())
				}
			}

			if result := findInt64ByFieldName(value, names, depth+1); result != 0 {
				return result
			}
		}

	case reflect.Slice, reflect.Array:
		for i := 0; i < v.Len(); i++ {
			if result := findInt64ByFieldName(v.Index(i), names, depth+1); result != 0 {
				return result
			}
		}

	case reflect.Map:
		for _, key := range v.MapKeys() {
			if key.Kind() == reflect.String && names[key.String()] {
				value := v.MapIndex(key)
				switch value.Kind() {
				case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
					return value.Int()
				case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
					return int64(value.Uint())
				}
			}
			if result := findInt64ByFieldName(v.MapIndex(key), names, depth+1); result != 0 {
				return result
			}
		}
	}

	return 0
}

func extractStringByFieldName(obj any, fieldNames map[string]bool) string {
	return findStringByFieldName(reflect.ValueOf(obj), fieldNames, 0)
}

func isAdminChat(chatID int64, adminChatIDs []int64) bool {
	for _, adminChatID := range adminChatIDs {
		if chatID != 0 && chatID == adminChatID {
			return true
		}
	}
	return false
}

func startAdminPost(chatID int64) {
	adminPostMu.Lock()
	defer adminPostMu.Unlock()
	adminPosts[chatID] = true
}

func cancelAdminPost(chatID int64) {
	adminPostMu.Lock()
	defer adminPostMu.Unlock()
	delete(adminPosts, chatID)
}

func hasAdminPost(chatID int64) bool {
	adminPostMu.Lock()
	defer adminPostMu.Unlock()
	return adminPosts[chatID]
}

func adminPostInstructionText() string {
	return `📝 Пост в канал

Отправьте следующим сообщением текст поста.

Бот опубликует его в канал сразу с кнопкой «📋 Прайс».

Чтобы отменить — напишите «отмена».`
}

func parseEnvInt64(name string) int64 {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return 0
	}
	id, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		log.Printf("Не удалось разобрать %s=%s: %v", name, value, err)
		return 0
	}
	return id
}

func parseAdminIDs() []int64 {
	idsMap := map[int64]bool{}
	var ids []int64

	add := func(id int64) {
		if id != 0 && !idsMap[id] {
			idsMap[id] = true
			ids = append(ids, id)
		}
	}

	add(parseEnvInt64("ADMIN_CHAT_ID"))

	value := strings.TrimSpace(os.Getenv("ADMIN_CHAT_IDS"))
	if value != "" {
		parts := strings.Split(value, ",")
		for _, part := range parts {
			id, err := strconv.ParseInt(strings.TrimSpace(part), 10, 64)
			if err != nil {
				log.Printf("Не удалось разобрать ADMIN_CHAT_IDS: %s", part)
				continue
			}
			add(id)
		}
	}

	return ids
}

func startDailyBroadcast(ctx context.Context, api *maxbot.Api, publicChatID int64) {
	if publicChatID == 0 {
		log.Println("PUBLIC_CHAT_ID не указан — ежедневная рассылка отключена")
		return
	}

	hour := 9
	minute := 0

	if v := os.Getenv("BROADCAST_HOUR"); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil && parsed >= 0 && parsed <= 23 {
			hour = parsed
		}
	}
	if v := os.Getenv("BROADCAST_MINUTE"); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil && parsed >= 0 && parsed <= 59 {
			minute = parsed
		}
	}

	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		lastSentDate := ""

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				now := time.Now()
				date := now.Format("2006-01-02")
				if now.Hour() == hour && now.Minute() == minute && lastSentDate != date {
					index := now.YearDay() % len(broadcasts)
					send(ctx, api, publicChatID, broadcasts[index], nil)
					lastSentDate = date
					log.Printf("Ежедневная рассылка отправлена в chat_id=%d", publicChatID)
				}
			}
		}
	}()

	log.Printf("Ежедневная рассылка включена: %02d:%02d, chat_id=%d", hour, minute, publicChatID)
}

func main() {
	if err := godotenv.Load(); err != nil {
		fmt.Println(".env не найден, пробуем переменные системы")
	}

	token := strings.TrimSpace(os.Getenv("BOT_TOKEN"))
	if token == "" {
		log.Fatal("BOT_TOKEN не указан. Добавьте его в .env")
	}

	adminChatIDs := parseAdminIDs()
	publicChatID := parseEnvInt64("PUBLIC_CHAT_ID")

	api, err := maxbot.New(token, maxbot.WithApiTimeout(20*time.Second))
	if err != nil {
		log.Fatalf("Ошибка создания клиента MAX: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()

	info, err := api.Bots.GetBot(ctx)
	fmt.Printf("Bot info: %#v\n", info)
	fmt.Printf("Bot info error: %#v\n", err)

	startDailyBroadcast(ctx, api, publicChatID)

	fmt.Println("Alex Kitchen Go bot started")
	fmt.Printf("Admins: %v\n", adminChatIDs)
	fmt.Printf("Public chat: %d\n", publicChatID)

	for upd := range api.GetUpdates(ctx) {
		switch u := upd.(type) {

		case *schemes.BotStartedUpdate:
			chatID := u.ChatId
			log.Printf("bot_started chat_id=%d", chatID)
			send(ctx, api, chatID, menuText(), keyboardForChat(chatID, adminChatIDs))

		case *schemes.UserAddedToChatUpdate:
			// Новый пользователь добавлен в чат/паблик.
			// Приветствие отправляем только лично пользователю, НЕ в общий чат.
			userID := extractInt64ByFieldName(u, map[string]bool{
				"UserId": true,
				"UserID": true,
			})
			userName := extractStringByFieldName(u, map[string]bool{
				"Name":      true,
				"FirstName": true,
				"Username":  true,
			})

			log.Printf("user_added_to_chat user_id=%d name=%q", userID, userName)
			sendPrivateToUser(ctx, api, userID, welcomeNewSubscriberText(userName), mainKeyboard())

		case *schemes.MessageCreatedUpdate:
			chatID := u.Message.Recipient.ChatId
			text := strings.TrimSpace(u.GetText())
			userName := u.Message.Sender.Name
			userID := u.Message.Sender.UserId

			log.Printf("message chat_id=%d user_id=%d text=%q", chatID, userID, text)

			if phone := extractContactPhone(u.Message); phone != "" {
				log.Printf("contact received chat_id=%d user_id=%d phone=%s", chatID, userID, phone)
				if processOrderContact(ctx, api, adminChatIDs, chatID, userID, userName, phone) {
					continue
				}
			}

			if text == "" {
				continue
			}

			if isAdminChat(chatID, adminChatIDs) && hasAdminPost(chatID) {
				if strings.EqualFold(text, "отмена") || text == "❌ Отменить заказ" || text == "🏠 Главное меню" {
					cancelAdminPost(chatID)
					send(ctx, api, chatID, "Публикация отменена.", keyboardForChat(chatID, adminChatIDs))
					continue
				}

				if publicChatID == 0 {
					send(ctx, api, chatID, "PUBLIC_CHAT_ID не указан в .env — не могу опубликовать пост.", keyboardForChat(chatID, adminChatIDs))
					continue
				}

				if sendPricePostToChannel(ctx, api, publicChatID, text) {
					cancelAdminPost(chatID)
					send(ctx, api, chatID, "Готово. Пост опубликован в канал с кнопкой «📋 Прайс».", keyboardForChat(chatID, adminChatIDs))
				} else {
					send(ctx, api, chatID, "Не получилось опубликовать пост. Проверьте логи и попробуйте ещё раз.", keyboardForChat(chatID, adminChatIDs))
				}
				continue
			}

			if processOrderStep(ctx, api, adminChatIDs, chatID, userID, userName, text) {
				continue
			}

			switch text {
			case "/start", "старт", "Старт", "Начать", "🏠 Главное меню":
				cancelOrder(chatID)
				send(ctx, api, chatID, menuText(), keyboardForChat(chatID, adminChatIDs))

			case "📋 Каталог", "Каталог", "каталог", "меню", "Меню":
				send(ctx, api, chatID, catalogMenuText(), catalogKeyboard())

			case "🛒 Сделать заказ", "🛒 Заказ", "Заказ", "заказ":
				startOrder(chatID)
				send(ctx, api, chatID, orderStartText(), cancelKeyboard())

			case "📝 Пост в канал", "пост", "Пост", "пост в канал", "Пост в канал":
				if !isAdminChat(chatID, adminChatIDs) {
					send(ctx, api, chatID, "Эта команда доступна только администратору.", mainKeyboard())
					continue
				}
				cancelOrder(chatID)
				startAdminPost(chatID)
				send(ctx, api, chatID, adminPostInstructionText(), cancelKeyboard())

			case "🚚 Доставка", "Доставка", "доставка":
				send(ctx, api, chatID, deliveryText(), orderKeyboard())

			case "💬 Администратор", "💬 Связь", "Связь", "админ", "Админ":
				send(ctx, api, chatID, adminText(), orderKeyboard())

			case "❌ Отменить заказ":
				cancelOrder(chatID)
				send(ctx, api, chatID, "Заказ отменён.", mainKeyboard())

			default:
				if section := findSectionByButton(text); section != nil {
					send(ctx, api, chatID, sectionText(*section), catalogKeyboard())
					continue
				}

				adminMessage := fmt.Sprintf(`Новое сообщение из MAX

Клиент: %s
User ID: %d
Chat ID: %d

Сообщение:
%s`, userName, userID, chatID, text)

				sendToAdmins(ctx, api, adminChatIDs, adminMessage)

				if looksLikeOrder(text) {
					send(ctx, api, chatID, "Спасибо! Сообщение похоже на заказ, я передал его администратору. Для оформления по шагам нажмите «🛒 Сделать заказ».", orderKeyboard())
				} else {
					send(ctx, api, chatID, "Спасибо! Сообщение передано администратору. Если хотите оформить заказ — нажмите «🛒 Сделать заказ».", mainKeyboard())
				}
			}

		case *schemes.MessageEditedUpdate:
			log.Printf("message_edited")
			continue

		case *schemes.MessageRemovedUpdate:
			log.Printf("message_removed")
			continue

		default:
			log.Printf("unknown update type: %T", upd)
		}
	}
}
