package main

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/argon2"
)

type AdminUser struct {
	Login      string
	Salt       []byte
	Hash       []byte
	Memory     uint32
	Iterations uint32
	Parallel   uint8
}

type AdminSession struct {
	Login     string
	ExpiresAt time.Time
}

var (
	adminUsers    = make(map[string]AdminUser)
	adminSessions = make(map[string]AdminSession)
	adminMu       sync.Mutex
	sessionSecret []byte
)

func loadAdminConfig() error {
	raw := strings.TrimSpace(os.Getenv("ADMIN_USERS"))
	if raw == "" {
		return fmt.Errorf("ADMIN_USERS не задан")
	}

	sessionSecret = []byte(strings.TrimSpace(os.Getenv("SESSION_SECRET")))
	if len(sessionSecret) < 32 {
		return fmt.Errorf("SESSION_SECRET должен содержать минимум 32 символа")
	}

	for _, entry := range strings.Split(raw, ";") {
		parts := strings.SplitN(strings.TrimSpace(entry), ":", 2)
		if len(parts) != 2 || parts[0] == "" {
			return fmt.Errorf("неверный формат ADMIN_USERS")
		}

		var memory, iterations uint32
		var parallel uint8
		var version int

		_, err := fmt.Sscanf(
			parts[1],
			"$argon2id$v=%d$m=%d,t=%d,p=%d",
			&version,
			&memory,
			&iterations,
			&parallel,
		)
		if err != nil || version != 19 {
			return fmt.Errorf("неверный формат хеша для %s", parts[0])
		}

		fields := strings.Split(parts[1], "$")

		if len(fields) != 6 {
			return fmt.Errorf("неверная структура хеша для %s", parts[0])
		}

		salt, err := base64.RawStdEncoding.DecodeString(fields[4])
		if err != nil {
			return err
		}

		hash, err := base64.RawStdEncoding.DecodeString(fields[5])
		if err != nil {
			return err
		}

		if len(salt) < 16 || len(hash) < 32 ||
			memory < 65536 || iterations < 1 || parallel < 1 {
			return fmt.Errorf("недопустимые параметры хеша для %s", parts[0])
		}

		adminUsers[parts[0]] = AdminUser{
			Login:      parts[0],
			Salt:       salt,
			Hash:       hash,
			Memory:     memory,
			Iterations: iterations,
			Parallel:   parallel,
		}
	}

	if len(adminUsers) == 0 {
		return fmt.Errorf("нет администраторов")
	}

	return nil
}

func verifyAdminPassword(user AdminUser, password string) bool {
	hash := argon2.IDKey(
		[]byte(password),
		user.Salt,
		user.Iterations,
		user.Memory,
		user.Parallel,
		uint32(len(user.Hash)),
	)

	return subtle.ConstantTimeCompare(hash, user.Hash) == 1
}

func createAdminSession(login string) (string, error) {
	token := make([]byte, 32)
	if _, err := rand.Read(token); err != nil {
		return "", err
	}

	sessionID := base64.RawURLEncoding.EncodeToString(token)

	adminMu.Lock()
	adminSessions[sessionID] = AdminSession{
		Login:     login,
		ExpiresAt: time.Now().Add(12 * time.Hour),
	}
	adminMu.Unlock()

	return sessionID, nil
}

func isAdminAuthenticated(r *http.Request) bool {
	cookie, err := r.Cookie("admin_session")
	if err != nil {
		return false
	}

	adminMu.Lock()
	defer adminMu.Unlock()

	session, exists := adminSessions[cookie.Value]
	if !exists || time.Now().After(session.ExpiresAt) {
		delete(adminSessions, cookie.Value)
		return false
	}

	return true
}

func requireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !isAdminAuthenticated(r) {
			http.Error(w, "Требуется авторизация", http.StatusUnauthorized)
			return
		}

		next(w, r)
	}
}

const adminLoginHTML = `<!DOCTYPE html>
<html lang="ru">
<head>
	<meta charset="UTF-8">
	<meta name="viewport" content="width=device-width, initial-scale=1">
	<title>Вход в админ-панель</title>
	<style>
		* { box-sizing: border-box; }
		body {
			margin: 0;
			min-height: 100vh;
			display: flex;
			align-items: center;
			justify-content: center;
			background: #f3f4f6;
			font-family: Arial, sans-serif;
		}
		.login {
			width: 100%;
			max-width: 380px;
			padding: 32px;
			background: white;
			border-radius: 14px;
			box-shadow: 0 8px 30px rgba(0,0,0,.08);
		}
		h1 { margin: 0 0 24px; font-size: 24px; }
		label { display: block; margin: 16px 0 7px; }
		input {
			width: 100%;
			padding: 12px;
			border: 1px solid #d1d5db;
			border-radius: 8px;
			font-size: 16px;
		}
		button {
			width: 100%;
			margin-top: 24px;
			padding: 13px;
			border: 0;
			border-radius: 8px;
			background: #222;
			color: white;
			font-size: 16px;
			cursor: pointer;
		}
		.error { color: #c62828; margin-top: 14px; }
	</style>
</head>
<body>
	<form class="login" method="POST" action="/admin/login">
		<h1>Вход в админ-панель</h1>
		<label for="login">Логин</label>
		<input id="login" name="login" autocomplete="username" required>
		<label for="password">Пароль</label>
		<input id="password" name="password" type="password" autocomplete="current-password" required>
		<button type="submit">Войти</button>
		<div class="error" id="error"></div>
	</form>
	<script>
		if (new URLSearchParams(location.search).has("error")) {
			document.getElementById("error").textContent = "Неверный логин или пароль";
		}
	</script>
</body>
</html>`

func adminMaxVisibilityHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	userID, err := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("user_id")), 10, 64)
	show := false

	if err == nil && userID > 0 {
		rawAdmins := strings.TrimSpace(os.Getenv("ADMIN_CHAT_IDS"))
		if rawAdmins == "" {
			rawAdmins = strings.TrimSpace(os.Getenv("ADMIN_CHAT_ID"))
		}

		admins := strings.FieldsFunc(rawAdmins, func(r rune) bool {
			return r == ',' || r == ';' || r == ' ' || r == '\n' || r == '\t'
		})

		for _, adminID := range admins {
			id, parseErr := strconv.ParseInt(strings.TrimSpace(adminID), 10, 64)
			if parseErr == nil && id == userID {
				show = true
				break
			}
		}
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]bool{"show": show})
}

const adminDashboardHTML = `<!DOCTYPE html>
<html lang="ru">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Управление товарами</title>
<style>
* {
    box-sizing: border-box;
}

body {
    margin: 0;
    padding: 30px 20px;
    background: #f3f5f9;
    color: #202938;
    font-family: Arial, sans-serif;
}

header {
    max-width: 1200px;
    margin: 0 auto 28px;
    display: flex;
    justify-content: space-between;
    align-items: center;
    gap: 15px;
}

h1 {
    font-size: 27px;
    font-weight: 700;
    margin: 0;
}

h2 {
    margin: 0 0 22px;
    font-size: 21px;
}

button, input, select, textarea {
    font: inherit;
}

button {
    border: 0;
    border-radius: 10px;
    padding: 12px 18px;
    cursor: pointer;
}

.logout {
    background: #263449;
    color: white;
}

.container {
    max-width: 1200px;
    margin: auto;
}

#search {
    width: 100%;
    padding: 15px 17px;
    border: 1px solid #e0e5ed;
    border-radius: 12px;
    margin-bottom: 22px;
    background: white;
    outline: none;
}

#search:focus,
.product-create input:focus,
.product-create select:focus,
.product-create textarea:focus {
    border-color: #4385e8;
    box-shadow: 0 0 0 3px rgba(67,133,232,.12);
}


.product-create,
.product-create * {
    box-sizing: border-box;
    min-width: 0;
}

.product-create {
    width: 100%;
    max-width: 100%;
    overflow: hidden;
    background: #fff;
    padding: 30px;
    border: 1px solid #e5eaf1;
    border-radius: 20px;
    box-shadow: 0 6px 24px rgba(30,45,70,.06);
    margin-bottom: 28px;
    display: grid;
    grid-template-columns: repeat(2, minmax(0, 1fr));
    gap: 16px;
}

.product-create h2 {
    grid-column: 1 / -1;
    margin: 0 0 8px;
    padding-bottom: 16px;
    border-bottom: 1px solid #edf0f5;
    font-size: 21px;
}

.product-create input,
.product-create select,
.product-create textarea {
    width: 100%;
	max-width: 100%;
    display: block;
    min-width: 0;
    min-height: 48px;
    padding: 13px 15px;
    border: 1px solid #dfe5ee;
    border-radius: 11px;
    background: #fbfcfe;
    color: #263449;
    font-size: 14px;
    outline: none;
    transition: .2s ease;
}

.product-create input:hover,
.product-create select:hover,
.product-create textarea:hover {
    border-color: #b8c9e2;
    background: #fff;
}

.product-create input:focus,
.product-create select:focus,
.product-create textarea:focus {
    border-color: #4385e8;
    background: #fff;
    box-shadow: 0 0 0 3px rgba(67,133,232,.11);
}

.product-create textarea {
    grid-column: 1 / -1;
    min-height: 105px;
    resize: vertical;
}

.product-create input[type="file"] {
    grid-column: 1 / -1;
    min-height: auto;
    padding: 18px;
    border: 1px dashed #b9c9df;
    border-radius: 12px;
    background: #f7f9fc;
    cursor: pointer;
}

.product-create button {
    grid-column: 1 / -1;
    min-height: 50px;
    padding: 15px;
    border-radius: 11px;
    font-size: 15px;
    font-weight: 700;
}


.product {
    background: white;
    padding: 20px;
    border: 1px solid #e9edf3;
    border-radius: 15px;
    margin-bottom: 13px;
    display: grid;
    grid-template-columns: minmax(180px, 1fr) 130px 130px 120px 100px 110px;
    gap: 14px;
    align-items: center;
    box-shadow: 0 3px 12px rgba(30,45,70,.035);
}


.product {
    display: flex;
    flex-direction: column;
    align-items: stretch;
    gap: 16px;
}

.product-edit-grid {
    display: grid;
    grid-template-columns: repeat(2, minmax(0, 1fr));
    gap: 14px;
    width: 100%;
}

.product-edit-field {
    display: flex;
    flex-direction: column;
    gap: 6px;
    min-width: 0;
    font-size: 14px;
    font-weight: 600;
    color: #344054;
}

.product-edit-field input,
.product-edit-field select,
.product-edit-field textarea {
    width: 100%;
    min-width: 0;
    box-sizing: border-box;
    min-height: 44px;
    padding: 10px 12px;
    border: 1px solid #d9e0e9;
    border-radius: 8px;
    background: #fff;
    font: inherit;
    font-weight: 400;
}

.product-edit-field textarea {
    resize: vertical;
    min-height: 90px;
}

.product-image {
    display: flex;
    flex-direction: column;
    align-items: flex-start;
    gap: 10px;
}

.product-active {
    display: flex;
    align-items: center;
    gap: 8px;
}

@media(max-width: 520px) {
    .product-edit-grid {
        grid-template-columns: 1fr;
        gap: 12px;
    }

    .product {
        padding: 16px;
    }

    .product-actions {
        flex-direction: column;
        width: 100%;
    }

    .product-actions button {
        width: 100%;
        min-height: 46px;
    }
}

.product-name {
    font-weight: bold;
}

.product-image {
    display: flex;
    flex-direction: column;
    gap: 8px;
    min-width: 0;
}

.product-image img {
    width: 100px;
    height: 80px;
    object-fit: contain;
    border: 1px solid #eee;
    border-radius: 8px;
    background: #fafafa;
}

.product-image input[type="file"] {
    width: 100%;
    max-width: 180px;
    font-size: 11px;
}

.product-info {
    color: #778196;
    font-size: 13px;
    margin-top: 5px;
}

.product input[type="number"],
.product input[type="text"] {
    width: 100%;
    padding: 10px;
    border: 1px solid #dfe4ec;
    border-radius: 8px;
}

.product input[type="checkbox"] {
    width: 20px;
    height: 20px;
}

.save {
    background: #286bd6;
    color: white;
    width: 100%;
    font-weight: 600;
    transition: background-color .3s, transform .2s;
}

.delete-product-btn {
    background: #dc3545;
    color: #fff;
    padding: 10px 14px;
    cursor: pointer;
    font-size: 14px;
    font-weight: 600;
}

.product-actions {
    display: flex;
    flex-direction: column;
    gap: 8px;
    width: 100%;
}

.product-actions button {
    flex: 1;
    min-width: 0;
    min-height: 42px;
    box-sizing: border-box;
}

.delete-confirm-overlay {
    position: fixed;
    inset: 0;
    z-index: 10000;
    background: rgba(0, 0, 0, 0.6);
    display: flex;
    align-items: center;
    justify-content: center;
    padding: 20px;
}

.delete-confirm-modal {
    background: #fff;
    color: #222;
    width: 100%;
    max-width: 400px;
    padding: 24px;
    border-radius: 12px;
    box-sizing: border-box;
    box-shadow: 0 10px 40px rgba(0, 0, 0, 0.25);
}

.delete-confirm-modal h3 {
    margin: 0 0 12px;
    font-size: 20px;
}

.delete-confirm-modal p {
    margin: 0 0 22px;
    line-height: 1.5;
}

.delete-confirm-actions {
    display: flex;
    gap: 10px;
}

.delete-confirm-actions button {
    flex: 1;
    min-height: 42px;
    border: none;
    border-radius: 6px;
    cursor: pointer;
    font-size: 14px;
    font-weight: 600;
}

.delete-cancel-btn {
    background: #e9ecef;
    color: #222;
}

.delete-confirm-btn {
    background: #dc3545;
    color: #fff;
}

.delete-product-btn:hover {
    background: #b02a37;
}

.delete-product-btn:disabled {
    opacity: 0.6;
    cursor: not-allowed;
}

.save:active {
    transform: scale(.98);
}

.save.saved {
    background: #239b61;
}

.bulk-save-container {
    margin: 25px 0 40px;
}

.save-all {
    position: sticky;
    bottom: 15px;
    z-index: 10;
    padding: 17px;
    font-size: 16px;
    font-weight: bold;
    box-shadow: 0 5px 18px rgba(0,0,0,.18);
}

.save:disabled {
    opacity: .5;
    cursor: wait;
}

.status {
    font-size: 13px;
    color: #778196;
}

#message {
    margin: 15px 0;
}

@media(max-width: 850px) {
    body {
        padding: 18px 12px;
    }

    header {
        align-items: center;
    }

    h1 {
        font-size: 22px;
    }

    .product {
        grid-template-columns: 1fr 1fr;
    }

    .product-image {
        grid-column: 1 / -1;
        flex-direction: row;
        align-items: center;
        flex-wrap: wrap;
    }

    .product-image img {
        width: 90px;
        height: 75px;
    }

    .product-info,
    .product-name,
    .save {
        grid-column: 1 / -1;
    }
}

@media(max-width: 520px) {
    .product-create {
    grid-template-columns: 1fr;
    padding: 19px;
    gap: 12px;
    border-radius: 15px;
}

.product-create h2,
.product-create textarea,
.product-create input[type="file"],
.product-create button {
    grid-column: 1;
}

.product-create h2 {
    font-size: 19px;
}

.product-create input,
.product-create select,
.product-create textarea {
    font-size: 14px;
    min-height: 46px;
}

.product-create textarea {
    min-height: 95px;
}
    h1 {
        font-size: 19px;
    }
}
</style>
</head>
<body>
<header>
    <h1>Управление товарами</h1>
    <form method="POST" action="/admin/logout">
        <button class="logout" type="submit">Выйти</button>
    </form>
</header>

<main class="container">
    <input id="search" type="search" placeholder="Поиск товара...">
    <div class="product-create">
		<h2>Добавить новый товар</h2>

		<input id="newName" type="text" placeholder="Название товара">
		<select id="newSection">
			<option value="">Выберите раздел</option>
		</select>

		<select id="newCategory" disabled>
			<option value="">Сначала выберите раздел</option>
		</select>
		<input id="newPrice" type="number" min="0" step="0.01" placeholder="Цена">
		<select id="newPriceUnit">
			<option value="">Выберите единицу цены</option>
			<option value="100 г">100 г</option>
			<option value="50 г">50 г</option>
			<option value="кг">кг</option>
			<option value="шт">шт</option>
		</select>

		<input id="newStock" type="number" min="0" step="0.001" placeholder="Остаток">
		<select id="newStockUnit">
			<option value="">Выберите единицу остатка</option>
			<option value="г">г</option>
			<option value="кг">кг</option>
			<option value="шт">шт</option>
		</select>

		<textarea id="newDescription" placeholder="Описание товара"></textarea>

		<input id="newImage" type="file" accept="image/jpeg,image/png,image/webp">

		<button id="createProduct" class="save" type="button">Добавить товар</button>
	</div>
	<div id="message">Загрузка товаров...</div>
    <div id="products"></div>

	<div class="bulk-save-container">
		<button id="saveAll" class="save save-all" type="button">
			Сохранить все изменения
		</button>
	</div>
</main>

<script>
let products = [];

function updateCategoryLists() {
    const sectionSelect = document.getElementById('newSection');
    const categorySelect = document.getElementById('newCategory');

    const selectedSection = sectionSelect.value;
    const selectedCategory = categorySelect.value;

    const sections = [...new Set(
        products.map(p => p.section).filter(Boolean)
    )].sort((a, b) => a.localeCompare(b, 'ru'));

    sectionSelect.replaceChildren(
        new Option('Выберите раздел', '')
    );

    sections.forEach(section => {
        sectionSelect.add(new Option(section, section));
    });

    sectionSelect.value = sections.includes(selectedSection)
        ? selectedSection
        : '';

    updateSubcategories(selectedCategory);
}

function updateSubcategories(selectedCategory = '') {
    const section = document.getElementById('newSection').value;
    const categorySelect = document.getElementById('newCategory');

	categorySelect.disabled = !section;
    const categories = [...new Set(
        products
            .filter(p => p.section === section)
            .map(p => p.category)
            .filter(Boolean)
    )].sort((a, b) => a.localeCompare(b, 'ru'));

    categorySelect.replaceChildren(
		new Option(
			section ? 'Выберите категорию' : 'Сначала выберите раздел',
			''
		)
	);

    categories.forEach(category => {
        categorySelect.add(new Option(category, category));
    });

    categorySelect.value = categories.includes(selectedCategory)
        ? selectedCategory
        : '';
}

document.getElementById('newSection').addEventListener('change', () => {
    updateSubcategories();
});

async function loadProducts() {
    const message = document.getElementById('message');
    try {
        const response = await fetch('/api/admin/products');
        if (response.status === 401) {
            location.href = '/admin';
            return;
        }
        if (!response.ok) throw new Error('Не удалось загрузить товары');

        products = await response.json();
		updateCategoryLists();
		renderProducts();
        message.textContent = 'Загружено товаров: ' + products.length;
    } catch (error) {
        message.textContent = error.message;
    }
}


function renderProducts() {
    const container = document.getElementById('products');
    const search = document.getElementById('search').value.toLowerCase();
    container.replaceChildren();

    const sections = [...new Set(
        products.map(p => p.section).filter(Boolean)
    )].sort((a, b) => a.localeCompare(b, 'ru'));

    const priceUnits = ['100 г', '50 г', 'кг', 'шт'];
    const stockUnits = ['г', 'кг', 'шт'];

    function createField(labelText, element) {
        const wrapper = document.createElement('label');
        wrapper.className = 'product-edit-field';

        const label = document.createElement('span');
        label.textContent = labelText;

        wrapper.append(label, element);
        return wrapper;
    }

    function createSelect(options, selectedValue, placeholder) {
        const select = document.createElement('select');

        select.add(new Option(placeholder, ''));

        options.forEach(value => {
            select.add(new Option(value, value));
        });

        select.value = selectedValue || '';
        return select;
    }

    products
        .filter(p => (p.name + ' ' + p.category + ' ' + p.section)
            .toLowerCase().includes(search))
        .forEach(product => {
            const card = document.createElement('div');
            card.className = 'product';

            const heading = document.createElement('div');
            heading.className = 'product-name';
            heading.textContent = 'Редактирование товара';

            const fields = document.createElement('div');
            fields.className = 'product-edit-grid';

            // Название
            const name = document.createElement('input');
            name.type = 'text';
            name.maxLength = 200;
            name.value = product._draftName ?? product.name ?? '';
            name.placeholder = 'Название товара';

            // Раздел
            const section = createSelect(
                sections,
                product._draftSection ?? product.section,
                'Выберите раздел'
            );

            // Категория
            const category = document.createElement('select');

            function updateCardCategories(selectedCategory = '') {
                const selectedSection = section.value;

                const categories = [...new Set(
                    products
                        .filter(p => p.section === selectedSection)
                        .map(p => p.category)
                        .filter(Boolean)
                )].sort((a, b) => a.localeCompare(b, 'ru'));

                category.disabled = !selectedSection;

                category.replaceChildren(
                    new Option(
                        selectedSection
                            ? 'Выберите категорию'
                            : 'Сначала выберите раздел',
                        ''
                    )
                );

                categories.forEach(value => {
                    category.add(new Option(value, value));
                });

                category.value = categories.includes(selectedCategory)
                    ? selectedCategory
                    : '';
            }

            updateCardCategories(
                product._draftCategory ?? product.category
            );

            section.addEventListener('change', () => {
				product._draftSection = section.value;
				updateCardCategories('');
				product._draftCategory = category.value;
			});

            // Цена
            const price = document.createElement('input');
            price.type = 'number';
            price.min = '0';
            price.max = '9999999999.99';
            price.step = '0.01';
            price.placeholder = 'Цена';
            price.value = product._draftPrice ?? product.price ?? '';

            // Единица цены
            const priceUnit = createSelect(
                priceUnits,
                product._draftPriceUnit ?? product.price_unit,
                'Выберите единицу цены'
            );

            // Остаток
            const stock = document.createElement('input');
            stock.type = 'number';
            stock.min = '0';
            stock.max = '999999999.999';
            stock.step = '0.001';
            stock.placeholder = 'Не задано';
            stock.value = product._draftStock ?? product.stock ?? '';

            // Единица остатка
            const stockUnit = createSelect(
                stockUnits,
                product._draftStockUnit ?? product.stock_unit,
                'Выберите единицу остатка'
            );

            // Описание
            const description = document.createElement('textarea');
            description.maxLength = 5000;
            description.rows = 4;
            description.placeholder = 'Описание товара';
            description.value =
                product._draftDescription ?? product.description ?? '';

            fields.append(
                createField('Название товара', name),
                createField('Раздел', section),
                createField('Категория', category),
                createField('Цена', price),
                createField('Единица цены', priceUnit),
                createField('Остаток', stock),
                createField('Единица остатка', stockUnit),
                createField('Описание', description)
            );

            // Изображение
            const imageWrapper = document.createElement('div');
            imageWrapper.className = 'product-image';

            const imageLabel = document.createElement('div');
            imageLabel.textContent = 'Фотография товара';

            const imagePreview = document.createElement('img');
            imagePreview.alt = product.name || 'Фотография товара';
            imagePreview.style.width = '120px';
            imagePreview.style.height = '100px';
            imagePreview.style.objectFit = 'contain';
            imagePreview.style.display = 'block';

            if (product._draftImage || product.image) {
                imagePreview.src = product._draftImage || product.image;
            }

            const imageInput = document.createElement('input');
            imageInput.type = 'file';
            imageInput.accept = 'image/jpeg,image/png,image/webp';

            imageInput.addEventListener('change', () => {
                const file = imageInput.files[0];

                if (!file) return;

                if (!['image/jpeg', 'image/png', 'image/webp'].includes(file.type)) {
                    alert('Выбери фотографию в формате JPG, PNG или WebP');
                    imageInput.value = '';
                    return;
                }

                if (file.size > 5 * 1024 * 1024) {
                    alert('Размер фотографии не должен превышать 5 МБ');
                    imageInput.value = '';
                    return;
                }

                imagePreview.src = URL.createObjectURL(file);
                product._draftImageFile = file;
            });

            imageWrapper.append(imageLabel, imagePreview, imageInput);

            // Активность
            const activeWrapper = document.createElement('label');
            activeWrapper.className = 'product-active';

            const active = document.createElement('input');
            active.type = 'checkbox';
            active.checked = product._draftActive ?? product.active;

            activeWrapper.append(active, document.createTextNode(' Активен'));
			// Отслеживание изменений всех полей карточки
			name.addEventListener('input', () => {
				product._draftName = name.value;
			});

			category.addEventListener('change', () => {
				product._draftCategory = category.value;
			});

			price.addEventListener('input', () => {
				product._draftPrice = price.value;
			});

			priceUnit.addEventListener('change', () => {
				product._draftPriceUnit = priceUnit.value;
			});

			stock.addEventListener('input', () => {
				product._draftStock = stock.value;
			});

			stockUnit.addEventListener('change', () => {
				product._draftStockUnit = stockUnit.value;
			});

			description.addEventListener('input', () => {
				product._draftDescription = description.value;
			});

			active.addEventListener('change', () => {
				product._draftActive = active.checked;
			});

            // Кнопка сохранения
            const save = document.createElement('button');
            save.type = 'button';
            save.className = 'save';
            save.textContent = 'Сохранить';

            save.addEventListener('click', async () => {
                const priceValue = Number(price.value);

                if (!name.value.trim() || !section.value || !category.value) {
                    alert('Заполни название, раздел и категорию товара');
                    return;
                }

                if (price.value === '' ||
                    !Number.isFinite(priceValue) ||
                    priceValue < 0 ||
                    priceValue > 9999999999.99) {
                    alert('Укажи корректную цену');
                    return;
                }

                if (stock.value !== '' &&
                    (!Number.isFinite(Number(stock.value)) ||
                        Number(stock.value) < 0 ||
                        Number(stock.value) > 999999999.999)) {
                    alert('Укажи корректное количество');
                    return;
                }

                save.disabled = true;
                save.textContent = 'Сохранение...';

                try {
                    let imagePath;

                    if (product._draftImageFile) {
                        const formData = new FormData();
                        formData.append('image', product._draftImageFile);

                        const uploadResponse = await fetch(
                            '/api/admin/products/upload-image',
                            {
                                method: 'POST',
                                body: formData
                            }
                        );

                        if (!uploadResponse.ok) {
                            throw new Error(await uploadResponse.text());
                        }

                        const uploadData = await uploadResponse.json();
                        imagePath = uploadData.image;
                    }

                    const response = await fetch('/api/admin/products/update', {
                        method: 'POST',
                        headers: {
                            'Content-Type': 'application/json'
                        },
                        body: JSON.stringify({
							id: product.id,
							name: name.value.trim(),
							section: section.value,
							category: category.value,
							price: priceValue,
							price_unit: priceUnit.value,
							stock: stock.value === '' ? null : Number(stock.value),
							stock_unit: stockUnit.value,
							description: description.value.trim(),
							active: active.checked,
							image: imagePath
						})
                    });

                    if (!response.ok) {
                        throw new Error(await response.text());
                    }

                    product.name = name.value.trim();
                    product.section = section.value;
                    product.category = category.value;
                    product.price = priceValue;
                    product.price_unit = priceUnit.value;
                    product.stock = stock.value === ''
                        ? null
                        : Number(stock.value);
                    product.stock_unit = stockUnit.value;
                    product.description = description.value.trim();
                    product.active = active.checked;

                    if (imagePath) {
                        product.image = imagePath;
                        imagePreview.src = imagePath + '?v=' + Date.now();
                    }

                    delete product._draftImageFile;
                    delete product._draftImage;

                    save.textContent = 'Сохранено ✓';
                    save.classList.add('saved');

                    setTimeout(() => {
                        save.textContent = 'Сохранить';
                        save.classList.remove('saved');
                    }, 2000);

                    updateCategoryLists();

                } catch (error) {
                    alert('Ошибка сохранения: ' + error.message);
                    save.textContent = 'Сохранить';
                    save.classList.remove('saved');
                } finally {
                    save.disabled = false;
                }
            });

            // Кнопка удаления
            const deleteButton = document.createElement('button');
            deleteButton.type = 'button';
            deleteButton.textContent = 'Удалить товар';
            deleteButton.className = 'delete-product-btn';

            deleteButton.addEventListener('click', async () => {
                const overlay = document.createElement('div');
                overlay.className = 'delete-confirm-overlay';

                const modal = document.createElement('div');
                modal.className = 'delete-confirm-modal';

                const title = document.createElement('h3');
                title.textContent = 'Удаление товара';

                const message = document.createElement('p');
                message.textContent =
                    'Вы действительно хотите удалить товар «' +
                    (product.name || 'без названия') + '»?';

                const buttons = document.createElement('div');
                buttons.className = 'delete-confirm-actions';

                const cancelButton = document.createElement('button');
                cancelButton.type = 'button';
                cancelButton.textContent = 'Отмена';
                cancelButton.className = 'delete-cancel-btn';

                const confirmButton = document.createElement('button');
                confirmButton.type = 'button';
                confirmButton.textContent = 'Удалить';
                confirmButton.className = 'delete-confirm-btn';

                buttons.append(cancelButton, confirmButton);
                modal.append(title, message, buttons);
                overlay.append(modal);
                document.body.append(overlay);

                cancelButton.addEventListener('click', () => overlay.remove());

                overlay.addEventListener('click', event => {
                    if (event.target === overlay) overlay.remove();
                });

                confirmButton.addEventListener('click', async () => {
                    confirmButton.disabled = true;
                    confirmButton.textContent = 'Удаление...';

                    try {
                        const response = await fetch('/api/admin/products/delete', {
                            method: 'POST',
                            headers: {
                                'Content-Type': 'application/json'
                            },
                            body: JSON.stringify({ id: product.id })
                        });

                        if (!response.ok) {
                            throw new Error(await response.text());
                        }

                        overlay.remove();
                        await loadProducts();

                    } catch (error) {
                        alert(error.message || 'Ошибка удаления товара');
                        confirmButton.disabled = false;
                        confirmButton.textContent = 'Удалить';
                    }
                });
            });

            // Кнопки внизу карточки
            const actions = document.createElement('div');
            actions.className = 'product-actions';
            actions.append(save, deleteButton);

            card.append(
                heading,
                fields,
                imageWrapper,
                activeWrapper,
                actions
            );

            container.append(card);
        });
}

document.getElementById('search').addEventListener('input', renderProducts);

const saveAllButton = document.getElementById('saveAll');

saveAllButton.addEventListener('click', async () => {
    if (saveAllButton.disabled) return;

    const changedProducts = products.filter(product =>
        product._draftName !== undefined ||
        product._draftSection !== undefined ||
        product._draftCategory !== undefined ||
        product._draftPrice !== undefined ||
        product._draftPriceUnit !== undefined ||
        product._draftStock !== undefined ||
        product._draftStockUnit !== undefined ||
        product._draftDescription !== undefined ||
        product._draftActive !== undefined ||
        product._draftImageFile !== undefined
    );

    if (changedProducts.length === 0) {
        saveAllButton.textContent = 'Нет изменений';
        saveAllButton.classList.add('saved');

        setTimeout(() => {
            saveAllButton.textContent = 'Сохранить все изменения';
            saveAllButton.classList.remove('saved');
        }, 1500);

        return;
    }

    const preparedProducts = [];

    for (const product of changedProducts) {
        const name = product._draftName ?? product.name ?? '';
        const section = product._draftSection ?? product.section ?? '';
        const category = product._draftCategory ?? product.category ?? '';
        const price = product._draftPrice ?? product.price;
        const priceUnit = product._draftPriceUnit ?? product.price_unit ?? '';
        const stock = product._draftStock ?? product.stock;
        const stockUnit = product._draftStockUnit ?? product.stock_unit ?? '';
        const description = product._draftDescription ?? product.description ?? '';
        const active = product._draftActive ?? product.active;

        if (!name.trim() || !section || !category) {
            alert('Заполни название, раздел и категорию товара: ' + name);
            return;
        }

        const priceValue = Number(price);

        if (price === '' || price === null || price === undefined ||
            !Number.isFinite(priceValue) ||
            priceValue < 0 || priceValue > 9999999999.99) {
            alert('Укажи корректную цену у товара: ' + name);
            return;
        }

        if (stock !== null && stock !== undefined && stock !== '' &&
            (!Number.isFinite(Number(stock)) ||
                Number(stock) < 0 || Number(stock) > 999999999.999)) {
            alert('Некорректный остаток у товара: ' + name);
            return;
        }

        preparedProducts.push({
            product,
            data: {
                id: product.id,
                name: name.trim(),
                section,
                category,
                price: priceValue,
                price_unit: priceUnit,
                stock: stock === '' || stock === null || stock === undefined
                    ? null
                    : Number(stock),
                stock_unit: stockUnit,
                description: description.trim(),
                active: Boolean(active),
                image: null
            }
        });
    }

    saveAllButton.disabled = true;
    saveAllButton.textContent = 'Сохранение...';

    try {
        // Сначала загружаем выбранные изображения.
        for (const item of preparedProducts) {
            const file = item.product._draftImageFile;

            if (!file) continue;

            const formData = new FormData();
            formData.append('image', file);

            const uploadResponse = await fetch(
                '/api/admin/products/upload-image',
                {
                    method: 'POST',
                    body: formData
                }
            );

            if (!uploadResponse.ok) {
                throw new Error(await uploadResponse.text());
            }

            const uploadData = await uploadResponse.json();
            item.data.image = uploadData.image;
        }

        // Все товары сохраняются одним запросом.
        const response = await fetch('/api/admin/products/bulk-update', {
            method: 'POST',
            headers: {
                'Content-Type': 'application/json'
            },
            body: JSON.stringify(preparedProducts.map(item => item.data))
        });

        if (!response.ok) {
            throw new Error(await response.text());
        }

        // Обновляем локальные данные только после успешного сохранения.
        for (const item of preparedProducts) {
            const product = item.product;
            const data = item.data;

            product.name = data.name;
            product.section = data.section;
            product.category = data.category;
            product.price = data.price;
            product.price_unit = data.price_unit;
            product.stock = data.stock;
            product.stock_unit = data.stock_unit;
            product.description = data.description;
            product.active = data.active;

            if (data.image) {
                product.image = data.image;
            }

            delete product._draftName;
            delete product._draftSection;
            delete product._draftCategory;
            delete product._draftPrice;
            delete product._draftPriceUnit;
            delete product._draftStock;
            delete product._draftStockUnit;
            delete product._draftDescription;
            delete product._draftActive;
            delete product._draftImageFile;
            delete product._draftImage;
        }

        saveAllButton.textContent = 'Все изменения сохранены ✓';
        saveAllButton.classList.add('saved');

        updateCategoryLists();
        renderProducts();

    } catch (error) {
        alert('Ошибка массового сохранения: ' + error.message);
        saveAllButton.textContent = 'Ошибка сохранения';
    } finally {
        saveAllButton.disabled = false;

        setTimeout(() => {
            saveAllButton.textContent = 'Сохранить все изменения';
            saveAllButton.classList.remove('saved');
        }, 2000);
    }
});

document.getElementById('createProduct').addEventListener('click', async () => {
    const button = document.getElementById('createProduct');
    const message = document.getElementById('message');

    const name = document.getElementById('newName').value.trim();
    const section = document.getElementById('newSection').value.trim();
    const category = document.getElementById('newCategory').value.trim();
    const price = Number(document.getElementById('newPrice').value);
    const priceUnit = document.getElementById('newPriceUnit').value.trim();
    const stockInput = document.getElementById('newStock').value;
    const stockUnit = document.getElementById('newStockUnit').value.trim();
    const description = document.getElementById('newDescription').value.trim();
    const imageFile = document.getElementById('newImage').files[0];

    if (!name || !section || !category ||
        document.getElementById('newPrice').value === '' ||
        !Number.isFinite(price) || price < 0) {
        message.textContent = 'Заполни название, раздел, категорию и корректную цену';
        return;
    }

    if (stockInput !== '' &&
        (!Number.isFinite(Number(stockInput)) || Number(stockInput) < 0)) {
        message.textContent = 'Некорректный остаток';
        return;
    }

    button.disabled = true;
    message.textContent = 'Добавление товара...';

    try {
        let image = '';

        if (imageFile) {
            const formData = new FormData();
            formData.append('image', imageFile);

            const uploadResponse = await fetch('/api/admin/products/upload-image', {
                method: 'POST',
                body: formData
            });

            if (!uploadResponse.ok) {
                throw new Error(await uploadResponse.text());
            }

            const uploadResult = await uploadResponse.json();
            image = uploadResult.image;
        }

        const response = await fetch('/api/admin/products/create', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({
                name,
                section,
                category,
                price,
                price_unit: priceUnit,
                stock: stockInput === '' ? null : Number(stockInput),
                stock_unit: stockUnit,
                description,
                image,
                active: true
            })
        });

        if (!response.ok) {
            throw new Error(await response.text());
        }

        const result = await response.json();

        products.push({
            id: result.id,
            name,
            section,
            category,
            price,
            price_unit: priceUnit,
            stock: stockInput === '' ? null : Number(stockInput),
            stock_unit: stockUnit,
            description,
            image,
            active: true
        });

        renderProducts();

        [
            'newName', 'newSection', 'newCategory',
            'newPrice', 'newPriceUnit', 'newStock',
            'newStockUnit', 'newDescription', 'newImage'
        ].forEach(id => {
            document.getElementById(id).value = '';
        });

        message.textContent = 'Товар успешно добавлен!';
    } catch (error) {
        message.textContent = 'Ошибка: ' + error.message;
    } finally {
        button.disabled = false;
    }
});

loadProducts();
</script>
</body>
</html>`

func adminPage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Метод не поддерживается", http.StatusMethodNotAllowed)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")

	if isAdminAuthenticated(r) {
		_, _ = w.Write([]byte(adminDashboardHTML))
		return
	}

	_, _ = w.Write([]byte(adminLoginHTML))
}

func adminLoginHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Метод не поддерживается", http.StatusMethodNotAllowed)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "Некорректный запрос", http.StatusBadRequest)
		return
	}

	login := strings.TrimSpace(r.FormValue("login"))
	password := r.FormValue("password")

	adminMu.Lock()
	user, exists := adminUsers[login]
	adminMu.Unlock()

	if !exists || !verifyAdminPassword(user, password) {
		http.Redirect(w, r, "/admin?error=1", http.StatusSeeOther)
		return
	}

	sessionID, err := createAdminSession(login)
	if err != nil {
		http.Error(w, "Не удалось создать сессию", http.StatusInternalServerError)
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     "admin_session",
		Value:    sessionID,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   r.TLS != nil,
		MaxAge:   12 * 60 * 60,
	})

	http.Redirect(w, r, "/admin", http.StatusSeeOther)
}

func adminLogoutHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Метод не поддерживается", http.StatusMethodNotAllowed)
		return
	}

	cookie, err := r.Cookie("admin_session")
	if err == nil {
		adminMu.Lock()
		delete(adminSessions, cookie.Value)
		adminMu.Unlock()
	}

	http.SetCookie(w, &http.Cookie{
		Name:     "admin_session",
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   r.TLS != nil,
		MaxAge:   -1,
	})

	http.SetCookie(w, &http.Cookie{
		Name:     "admin_session",
		Value:    "",
		Path:     "/admin",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   r.TLS != nil,
		MaxAge:   -1,
	})

	http.Redirect(w, r, "/admin", http.StatusSeeOther)
}

func adminCreateProductHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)

	var input struct {
		Section     string          `json:"section"`
		Category    string          `json:"category"`
		Name        string          `json:"name"`
		Price       float64         `json:"price"`
		PriceUnit   string          `json:"price_unit"`
		Stock       json.RawMessage `json:"stock"`
		StockUnit   string          `json:"stock_unit"`
		Description string          `json:"description"`
		Image       string          `json:"image"`
		Active      *bool           `json:"active"`
	}

	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}

	input.Section = strings.TrimSpace(input.Section)
	input.Category = strings.TrimSpace(input.Category)
	input.Name = strings.TrimSpace(input.Name)
	input.PriceUnit = strings.TrimSpace(input.PriceUnit)
	input.StockUnit = strings.TrimSpace(input.StockUnit)
	input.Description = strings.TrimSpace(input.Description)
	input.Image = strings.TrimSpace(input.Image)

	if input.Section == "" || input.Category == "" || input.Name == "" ||
		input.Price < 0 || math.IsNaN(input.Price) || math.IsInf(input.Price, 0) ||
		input.Price > 9999999999.99 || len(input.Stock) == 0 || input.Active == nil {
		http.Error(w, "invalid product fields", http.StatusBadRequest)
		return
	}

	if len(input.Section) > 100 || len(input.Category) > 100 ||
		len(input.Name) > 200 || len(input.PriceUnit) > 30 ||
		len(input.StockUnit) > 30 || len(input.Description) > 5000 {
		http.Error(w, "product field is too long", http.StatusBadRequest)
		return
	}

	var stockValue interface{}

	if string(input.Stock) != "null" {
		stock, err := strconv.ParseFloat(string(input.Stock), 64)
		if err != nil || math.IsNaN(stock) || math.IsInf(stock, 0) ||
			stock < 0 || stock > 999999999.999 {
			http.Error(w, "invalid stock value", http.StatusBadRequest)
			return
		}
		stockValue = string(input.Stock)
	}

	if input.Image != "" &&
		(!strings.HasPrefix(input.Image, "/static/products/") ||
			strings.Contains(input.Image, "..")) {
		http.Error(w, "invalid image path", http.StatusBadRequest)
		return
	}

	randomBytes := make([]byte, 16)
	if _, err := rand.Read(randomBytes); err != nil {
		http.Error(w, "failed to generate product ID", http.StatusInternalServerError)
		return
	}

	productID := "p-" + base64.RawURLEncoding.EncodeToString(randomBytes)

	query := `
		INSERT INTO products (
			id, section, category, name, price, price_unit,
			stock, stock_unit, active, status, description, image
		)
		VALUES (
			$1, $2, $3, $4, $5, NULLIF($6, ''),
			$7::numeric, NULLIF($8, ''), $9,
			CASE
				WHEN $7::numeric IS NULL THEN NULL
				WHEN $7::numeric <= 0 THEN 'Нет в наличии'
				ELSE 'В наличии'
			END,
			NULLIF($10, ''), NULLIF($11, '')
		)
	`

	_, err := dbPool.Exec(
		context.Background(),
		query,
		productID,
		input.Section,
		input.Category,
		input.Name,
		input.Price,
		input.PriceUnit,
		stockValue,
		input.StockUnit,
		*input.Active,
		input.Description,
		input.Image,
	)

	if err != nil {
		log.Printf("admin product creation failed: %v", err)
		http.Error(w, "product creation failed", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"id":      productID,
	})
}

func adminProductsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	query := `
		SELECT COALESCE(
			json_agg(
				json_build_object(
					'id', id,
					'section', section,
					'category', category,
					'name', name,
					'price', price,
					'price_unit', price_unit,
					'stock', stock,
					'stock_unit', stock_unit,
					'active', active,
					'status', status,
					'low_stock_threshold', low_stock_threshold,
					'description', description,
					'image', image,
					'sort', sort
				)
				ORDER BY sort NULLS LAST, id
			),
			'[]'::json
		)
		FROM products;
	`

	var data []byte

	err := dbPool.QueryRow(context.Background(), query).Scan(&data)
	if err != nil {
		log.Printf("admin products query failed: %v", err)
		http.Error(w, "products unavailable", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(data)
}

func adminUpdateProductHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)

	var input struct {
		ID          string          `json:"id"`
		Section     string          `json:"section"`
		Category    string          `json:"category"`
		Name        string          `json:"name"`
		Price       float64         `json:"price"`
		PriceUnit   string          `json:"price_unit"`
		Stock       json.RawMessage `json:"stock"`
		StockUnit   string          `json:"stock_unit"`
		Description string          `json:"description"`
		Active      *bool           `json:"active"`
		Image       *string         `json:"image"`
	}

	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}

	input.ID = strings.TrimSpace(input.ID)
	input.Section = strings.TrimSpace(input.Section)
	input.Category = strings.TrimSpace(input.Category)
	input.Name = strings.TrimSpace(input.Name)
	input.PriceUnit = strings.TrimSpace(input.PriceUnit)
	input.StockUnit = strings.TrimSpace(input.StockUnit)
	input.Description = strings.TrimSpace(input.Description)

	if input.ID == "" || input.Section == "" ||
		input.Category == "" || input.Name == "" ||
		input.Price < 0 || math.IsNaN(input.Price) ||
		math.IsInf(input.Price, 0) || input.Price > 9999999999.99 ||
		len(input.Stock) == 0 || input.Active == nil {
		http.Error(w, "invalid product fields", http.StatusBadRequest)
		return
	}

	if len(input.Section) > 100 || len(input.Category) > 100 ||
		len(input.Name) > 200 || len(input.PriceUnit) > 30 ||
		len(input.StockUnit) > 30 || len(input.Description) > 5000 {
		http.Error(w, "product field is too long", http.StatusBadRequest)
		return
	}

	var stockValue interface{}

	if string(input.Stock) != "null" {
		stock, err := strconv.ParseFloat(string(input.Stock), 64)
		if err != nil || math.IsNaN(stock) || math.IsInf(stock, 0) ||
			stock < 0 || stock > 999999999.999 {
			http.Error(w, "invalid stock value", http.StatusBadRequest)
			return
		}

		stockValue = string(input.Stock)
	}

	if input.Image != nil {
		imagePath := strings.TrimSpace(*input.Image)

		if imagePath == "" {
			input.Image = nil
		} else if !strings.HasPrefix(imagePath, "/static/products/") ||
			strings.Contains(imagePath, "..") {
			http.Error(w, "invalid image path", http.StatusBadRequest)
			return
		} else {
			input.Image = &imagePath
		}
	}

	query := `
		UPDATE products
		SET
			section = $2,
			category = $3,
			name = $4,
			price = $5,
			price_unit = NULLIF($6, ''),
			stock = $7::numeric,
			stock_unit = NULLIF($8, ''),
			description = NULLIF($9, ''),
			active = $10,
			image = COALESCE($11, image),
			status = CASE
				WHEN $7::numeric IS NULL THEN status
				WHEN $7::numeric <= 0 THEN 'Нет в наличии'
				ELSE 'В наличии'
			END
		WHERE id = $1
	`

	result, err := dbPool.Exec(
		context.Background(),
		query,
		input.ID,
		input.Section,
		input.Category,
		input.Name,
		input.Price,
		input.PriceUnit,
		stockValue,
		input.StockUnit,
		input.Description,
		*input.Active,
		input.Image,
	)

	if err != nil {
		log.Printf("admin product update failed: %v", err)
		http.Error(w, "database update failed", http.StatusInternalServerError)
		return
	}

	if result.RowsAffected() == 0 {
		http.Error(w, "product not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = w.Write([]byte(`{"success":true}`))
}

func adminDeleteProductHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)

	var input struct {
		ID string `json:"id"`
	}

	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}

	input.ID = strings.TrimSpace(input.ID)
	if input.ID == "" {
		http.Error(w, "product ID is required", http.StatusBadRequest)
		return
	}

	ctx := r.Context()

	tx, err := dbPool.Begin(ctx)
	if err != nil {
		log.Printf("admin product delete transaction start failed: %v", err)
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback(ctx)

	// Блокируем товар, чтобы во время удаления
	// не появилась новая резервация.
	var exists bool
	err = tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM products
			WHERE id = $1
			FOR UPDATE
		)
	`, input.ID).Scan(&exists)

	if err != nil {
		log.Printf("admin product delete lock failed: %v", err)
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}

	if !exists {
		http.Error(w, "product not found", http.StatusNotFound)
		return
	}

	// Защищаем активные, подтверждённые и преобразованные
	// в заказ резервации, а также записи с привязкой к заказу.
	var hasProtectedReservations bool

	err = tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM cart_reservations
			WHERE product_id = $1
			  AND (
					status IN ('confirmed', 'converted')
					OR (status = 'active' AND expires_at > now())
					OR NULLIF(BTRIM(order_id), '') IS NOT NULL
			  )
		)
	`, input.ID).Scan(&hasProtectedReservations)

	if err != nil {
		log.Printf("admin product delete reservation check failed: %v", err)
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}

	if hasProtectedReservations {
		http.Error(
			w,
			"Нельзя удалить товар: он связан с активной резервацией или заказом",
			http.StatusConflict,
		)
		return
	}

	// Удаляем только безопасные записи резервов:
	// освобождённые, истёкшие и просроченные активные.
	_, err = tx.Exec(ctx, `
		DELETE FROM cart_reservations
		WHERE product_id = $1
	`, input.ID)

	if err != nil {
		log.Printf("admin product delete reservations failed: %v", err)
		http.Error(w, "reservation cleanup failed", http.StatusInternalServerError)
		return
	}

	_, err = tx.Exec(ctx, `
		DELETE FROM products
		WHERE id = $1
	`, input.ID)

	if err != nil {
		log.Printf("admin product delete failed: %v", err)
		http.Error(w, "product delete failed", http.StatusInternalServerError)
		return
	}

	if err := tx.Commit(ctx); err != nil {
		log.Printf("admin product delete commit failed: %v", err)
		http.Error(w, "database commit failed", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = w.Write([]byte(`{"success":true}`))
}

func adminBulkUpdateProductsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 5<<20)

	var input []struct {
		ID          string          `json:"id"`
		Section     string          `json:"section"`
		Category    string          `json:"category"`
		Name        string          `json:"name"`
		Price       float64         `json:"price"`
		PriceUnit   string          `json:"price_unit"`
		Stock       json.RawMessage `json:"stock"`
		StockUnit   string          `json:"stock_unit"`
		Description string          `json:"description"`
		Active      *bool           `json:"active"`
		Image       *string         `json:"image"`
	}

	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}

	if len(input) == 0 || len(input) > 1000 {
		http.Error(w, "invalid product count", http.StatusBadRequest)
		return
	}

	ctx := r.Context()

	tx, err := dbPool.Begin(ctx)
	if err != nil {
		http.Error(w, "database transaction failed", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback(ctx)

	for _, product := range input {
		product.ID = strings.TrimSpace(product.ID)
		product.Section = strings.TrimSpace(product.Section)
		product.Category = strings.TrimSpace(product.Category)
		product.Name = strings.TrimSpace(product.Name)
		product.PriceUnit = strings.TrimSpace(product.PriceUnit)
		product.StockUnit = strings.TrimSpace(product.StockUnit)
		product.Description = strings.TrimSpace(product.Description)

		if product.ID == "" || product.Section == "" ||
			product.Category == "" || product.Name == "" ||
			product.Price < 0 || math.IsNaN(product.Price) ||
			math.IsInf(product.Price, 0) || product.Price > 9999999999.99 ||
			len(product.Stock) == 0 || product.Active == nil {
			http.Error(w, "invalid product fields", http.StatusBadRequest)
			return
		}

		if len(product.Section) > 100 || len(product.Category) > 100 ||
			len(product.Name) > 200 || len(product.PriceUnit) > 30 ||
			len(product.StockUnit) > 30 || len(product.Description) > 5000 {
			http.Error(w, "product field is too long", http.StatusBadRequest)
			return
		}

		var stockValue interface{}

		if string(product.Stock) != "null" {
			stock, err := strconv.ParseFloat(string(product.Stock), 64)
			if err != nil || math.IsNaN(stock) || math.IsInf(stock, 0) ||
				stock < 0 || stock > 999999999.999 {
				http.Error(w, "invalid stock value", http.StatusBadRequest)
				return
			}

			stockValue = string(product.Stock)
		}

		if product.Image != nil {
			imagePath := strings.TrimSpace(*product.Image)

			if imagePath == "" {
				product.Image = nil
			} else if !strings.HasPrefix(imagePath, "/static/products/") ||
				strings.Contains(imagePath, "..") {
				http.Error(w, "invalid image path", http.StatusBadRequest)
				return
			} else {
				product.Image = &imagePath
			}
		}

		result, err := tx.Exec(ctx, `
			UPDATE products
			SET
				section = $2,
				category = $3,
				name = $4,
				price = $5,
				price_unit = NULLIF($6, ''),
				stock = $7::numeric,
				stock_unit = NULLIF($8, ''),
				description = NULLIF($9, ''),
				active = $10,
				image = COALESCE($11, image),
				status = CASE
					WHEN $7::numeric IS NULL THEN status
					WHEN $7::numeric <= 0 THEN 'Нет в наличии'
					ELSE 'В наличии'
				END
			WHERE id = $1
		`,
			product.ID,
			product.Section,
			product.Category,
			product.Name,
			product.Price,
			product.PriceUnit,
			stockValue,
			product.StockUnit,
			product.Description,
			*product.Active,
			product.Image,
		)

		if err != nil {
			log.Printf("admin bulk product update failed: %v", err)
			http.Error(w, "database update failed", http.StatusInternalServerError)
			return
		}

		if result.RowsAffected() == 0 {
			http.Error(w, "product not found: "+product.ID, http.StatusNotFound)
			return
		}
	}

	if err := tx.Commit(ctx); err != nil {
		http.Error(w, "database commit failed", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = w.Write([]byte(`{"success":true}`))
}

func adminUploadProductImageHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 6<<20)

	if err := r.ParseMultipartForm(6 << 20); err != nil {
		http.Error(w, "image is too large or invalid", http.StatusBadRequest)
		return
	}

	file, _, err := r.FormFile("image")
	if err != nil {
		http.Error(w, "image file is required", http.StatusBadRequest)
		return
	}
	defer file.Close()

	data, err := io.ReadAll(io.LimitReader(file, (5<<20)+1))
	if err != nil {
		http.Error(w, "failed to read image", http.StatusBadRequest)
		return
	}

	if len(data) == 0 || len(data) > 5<<20 {
		http.Error(w, "image must be between 1 byte and 5 MB", http.StatusBadRequest)
		return
	}

	contentType := http.DetectContentType(data)

	var extension string

	switch contentType {
	case "image/jpeg":
		extension = ".jpg"
	case "image/png":
		extension = ".png"
	case "image/webp":
		extension = ".webp"
	default:
		http.Error(w, "only JPG, PNG and WebP images are allowed", http.StatusBadRequest)
		return
	}

	randomBytes := make([]byte, 16)
	if _, err := rand.Read(randomBytes); err != nil {
		http.Error(w, "failed to generate filename", http.StatusInternalServerError)
		return
	}

	filename := base64.RawURLEncoding.EncodeToString(randomBytes) + extension
	dir := filepath.Join("static", "products", "uploads")

	if err := os.MkdirAll(dir, 0755); err != nil {
		http.Error(w, "failed to create image directory", http.StatusInternalServerError)
		return
	}

	path := filepath.Join(dir, filename)

	if err := os.WriteFile(path, data, 0644); err != nil {
		http.Error(w, "failed to save image", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]string{
		"image": "/static/products/uploads/" + filename,
	})
}
