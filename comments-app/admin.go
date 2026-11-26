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
*{box-sizing:border-box}
body{margin:0;padding:18px 24px;background:#eef1f5;color:#202938;font-family:Arial,sans-serif}
header{width:min(98%,1900px);margin:0 auto 16px;display:flex;justify-content:space-between;align-items:center;gap:15px}
h1{margin:0;font-size:26px}h2{margin:0 0 14px;font-size:19px}
button,input,select,textarea{font:inherit}button{border:0;border-radius:4px;padding:9px 13px;cursor:pointer}
.logout{background:#263449;color:#fff}
.container,main.container{width:min(98%,1900px);max-width:1900px;margin:0 auto}
#search{width:100%;margin:0 0 14px;padding:11px 13px;border:1px solid #c4cedb;border-radius:4px;background:#fff}
.product-create{width:100%;margin:0 0 16px;padding:16px 18px;display:grid;grid-template-columns:repeat(4,minmax(130px,1fr));gap:9px 10px;background:#fff;border:1px solid #bfc9d6;border-radius:5px}
.product-create h2{grid-column:1/-1;margin:0;padding-bottom:9px;border-bottom:1px solid #d2dbe6}
.product-create input,.product-create select,.product-create textarea{width:100%;min-width:0;min-height:38px;padding:7px 9px;border:1px solid #c4cedb;border-radius:3px;background:#fff;font-size:13px}
.product-create textarea,.product-create input[type=file],.product-create button{grid-column:1/-1}
.product-create textarea{min-height:60px;resize:vertical}
#message{margin:12px 0;color:#566579;font-size:13px}

/* Single Excel-like grid: header and each product use the same 11 columns. */
.sheet-scroll{width:100%;overflow-x:auto;border:1px solid #aeb9c8;background:#fff}
.product-sheet-head,.product{display:grid;grid-template-columns:190px 150px 160px 115px 125px 115px 125px minmax(260px,1.5fr) 180px 105px 125px;min-width:1750px}
.product-sheet-head{background:#cbd8e8;color:#24354a;font-size:12px;font-weight:700;position:sticky;top:0;z-index:2}
.product-sheet-head span{padding:9px 8px;border-right:1px solid #aeb9c8;border-bottom:1px solid #aeb9c8}
#products{min-width:1750px}
.product{gap:0;align-items:stretch;margin:0;padding:0;border:0;border-bottom:1px solid #b9c4d2;border-radius:0;box-shadow:none;background:#fff}
.product:nth-child(even){background:#f5f8fc}
.product-name{display:none}
.product-edit-grid{display:contents}
.product-edit-field{display:flex;flex-direction:column;justify-content:flex-start;min-width:0;gap:5px;padding:8px;border-right:1px solid #d2dbe6;border-bottom:1px solid #d2dbe6;color:#344054;font-size:11px;font-weight:600}
.product-edit-field input,.product-edit-field select,.product-edit-field textarea{width:100%;min-width:0;min-height:36px;padding:6px;border:1px solid #c4cedb;border-radius:2px;background:#fff;font-size:12px;font-weight:400}
.product-edit-field textarea{min-height:80px;resize:vertical}
.product-image{display:flex;flex-direction:column;align-items:center;gap:8px;min-width:0;padding:8px;border-right:1px solid #d2dbe6;border-bottom:1px solid #d2dbe6;font-size:11px}
.product-image img{width:155px!important;height:125px!important;max-width:100%;object-fit:contain;display:block;background:#fff;border:1px solid #c4cedb;cursor:zoom-in}
.product-image input[type=file]{width:100%;max-width:100%;font-size:10px}
.product-active{display:flex;align-items:center;justify-content:center;gap:6px;padding:8px;border-right:1px solid #d2dbe6;border-bottom:1px solid #d2dbe6;font-size:12px}
.product-active input[type=checkbox]{width:18px;height:18px}
.product-actions{display:flex;flex-direction:column;justify-content:center;gap:7px;min-width:0;padding:8px;border-bottom:1px solid #d2dbe6}
.product-actions button{width:100%;min-width:0;min-height:36px;padding:6px;border-radius:3px;font-size:11px}
.save{width:100%;background:#286bd6;color:#fff;font-weight:600}
.save:disabled{opacity:.5;cursor:wait}.save.saved{background:#239b61}
.delete-product-btn{background:#dc3545;color:#fff}.delete-product-btn:hover{background:#b02a37}
.save-all{position:sticky;bottom:12px;width:100%}.bulk-save-container{margin:20px 0 35px}
.delete-confirm-overlay{position:fixed;inset:0;z-index:10000;display:flex;align-items:center;justify-content:center;padding:20px;background:rgba(0,0,0,.6)}
.delete-confirm-modal{width:100%;max-width:400px;padding:24px;border-radius:8px;background:#fff;color:#222;box-shadow:0 10px 40px rgba(0,0,0,.25)}
.delete-confirm-modal h3{margin:0 0 12px;font-size:20px}.delete-confirm-modal p{margin:0 0 22px;line-height:1.5}
.delete-confirm-actions{display:flex;gap:10px}.delete-confirm-actions button{flex:1;min-height:42px;font-size:14px;font-weight:600}
.delete-cancel-btn{background:#e9ecef;color:#222}.delete-confirm-btn{background:#dc3545;color:#fff}
.status,.product-info{color:#778196;font-size:12px}

#catalogSections .catalog-section{border:1px solid #c5d0dd;margin:10px 0;padding:12px;border-radius:4px}
.catalog-section-head{display:flex;flex-wrap:wrap;align-items:center;gap:8px;background:#f2f6fa;padding:8px}
.catalog-section-head strong{flex:1;min-width:160px}
.catalog-category{display:flex;flex-wrap:wrap;align-items:center;gap:8px;padding:8px;border-bottom:1px solid #e0e6ee}
.catalog-category span{flex:1;min-width:150px}
.catalog-section button,.catalog-category button{padding:7px 10px}
@media(max-width:760px){body{padding:10px}header,.container,main.container{width:100%}h1{font-size:21px}.product-create{grid-template-columns:repeat(2,minmax(0,1fr))}}
.catalog-edit-overlay{position:fixed;inset:0;z-index:11000;background:rgba(20,32,48,.58);display:flex;align-items:center;justify-content:center;padding:18px}
.catalog-edit-dialog{width:min(520px,100%);background:#fff;border-radius:10px;box-shadow:0 16px 50px rgba(0,0,0,.28);padding:22px;color:#24354a}
.catalog-edit-dialog h3{margin:0 0 16px;font-size:20px}
.catalog-edit-form{display:grid;gap:12px}
.catalog-edit-form label{display:grid;gap:5px;font-size:13px;font-weight:600}
.catalog-edit-form input,.catalog-edit-form textarea{width:100%;box-sizing:border-box;padding:10px;border:1px solid #c5d0dd;border-radius:5px;font:inherit;font-weight:400}
.catalog-edit-form textarea{min-height:80px;resize:vertical}
.catalog-edit-actions{display:flex;justify-content:flex-end;gap:10px;margin-top:5px}
.catalog-edit-actions button{padding:10px 18px;border-radius:5px}
.catalog-edit-cancel{background:#e9eef4;color:#24354a}
.catalog-edit-save{background:#286bd6;color:#fff}

[hidden]{display:none!important}
</style>
</head>
<body>
<header>
    <h1>Управление товарами</h1>
    <form method="POST" action="/admin/logout">
        <button class="logout" type="submit">Выйти</button>
    </form>
</header>


    <nav id="adminTabs" style="display:flex;gap:8px;margin:0 0 14px">
      <button type="button" data-tab="products" style="background:#286bd6;color:white">Товары</button>
      <button type="button" data-tab="catalog" style="background:#d7e0eb;color:#24354a">Секции и категории</button>
    </nav>
    <section id="catalogPanel" hidden style="background:#fff;border:1px solid #bfc9d6;border-radius:5px;padding:16px;margin-bottom:16px">
      <h2>Секции и категории каталога</h2>
      <div style="display:grid;grid-template-columns:repeat(auto-fit,minmax(160px,1fr));gap:8px;margin-bottom:14px">
        <input id="catalogSectionName" placeholder="Название нового раздела">
        <input id="catalogSectionDesc" placeholder="Описание раздела">
        <input id="catalogSectionIcon" placeholder="Иконка, например 🥩" value="📦">
        <input id="catalogSectionSort" type="number" placeholder="Порядок" value="0">
        <button id="catalogAddSection" class="save" type="button">Добавить раздел</button>
      </div>
      <div id="catalogMessage" class="status"></div>
      <div id="catalogSections"></div>
    </section>
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
    <div class="sheet-scroll">
      <div class="product-sheet-head" aria-hidden="true">
        <span>Название</span><span>Раздел</span><span>Категория</span><span>Цена</span>
        <span>Ед. цены</span><span>Остаток</span><span>Ед. остатка</span><span>Описание</span>
        <span>Фото</span><span>Активен</span><span>Действия</span>
      </div>
      <div id="products"></div>
    </div>

	<div class="bulk-save-container">
		<button id="saveAll" class="save save-all" type="button">
			Сохранить все изменения
		</button>
	</div>
</main>

	<div id="catalogEditOverlay" class="catalog-edit-overlay" hidden>
 <div class="catalog-edit-dialog" role="dialog" aria-modal="true" aria-labelledby="catalogEditTitle">
  <h3 id="catalogEditTitle">Редактирование</h3>
  <form id="catalogEditForm" class="catalog-edit-form"></form>
 </div>
</div>
<script>
function adminNotice(message){
 let toast=document.getElementById('adminToast');
 if(!toast){toast=document.createElement('div');toast.id='adminToast';toast.style.cssText='position:fixed;right:18px;top:18px;z-index:12000;max-width:min(420px,calc(100vw - 36px));padding:12px 16px;background:#24354a;color:white;border-radius:7px;box-shadow:0 8px 30px rgba(0,0,0,.22);font-size:14px';document.body.append(toast);}
 toast.textContent=message;toast.hidden=false;
 clearTimeout(window.adminToastTimer);window.adminToastTimer=setTimeout(()=>toast.hidden=true,4500);
}


let products = [];

function updateCategoryLists() {
    const sectionSelect = document.getElementById('newSection');
    const categorySelect = document.getElementById('newCategory');

    const selectedSection = sectionSelect.value;
    const selectedCategory = categorySelect.value;

    const sections = [...new Set(
        (managedCatalogLoaded ? managedCatalog.map(s => s.name) : products.map(p => p.section)).filter(Boolean)
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
        (managedCatalogLoaded ? (managedCatalog.find(s=>s.name===section)?.categories||[]).map(c=>c.name) : products.filter(p => p.section === section).map(p => p.category)).filter(Boolean)
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
        (managedCatalogLoaded ? managedCatalog.map(s => s.name) : products.map(p => p.section)).filter(Boolean)
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
            heading.textContent = product._draftName ?? product.name ?? 'Товар';

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
                    (managedCatalogLoaded ? (managedCatalog.find(s => s.name === selectedSection)?.categories || []).map(c => c.name) : products.filter(p => p.section === selectedSection).map(p => p.category)).filter(Boolean)
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
            imagePreview.style.width = '155px';
            imagePreview.style.height = '125px';
            imagePreview.style.objectFit = 'contain';
            imagePreview.style.display = 'block';

            if (product._draftImageFile) {
                imagePreview.src = URL.createObjectURL(product._draftImageFile);
            } else if (product._draftImage || product.image) {
                imagePreview.src = product._draftImage || product.image;
            }

            const imageInput = document.createElement('input');
            imageInput.type = 'file';
            imageInput.accept = 'image/jpeg,image/png,image/webp';

            imageInput.addEventListener('change', () => {
                const file = imageInput.files[0];

                if (!file) return;

                if (!['image/jpeg', 'image/png', 'image/webp'].includes(file.type)) {
                    adminNotice('Выбери фотографию в формате JPG, PNG или WebP');
                    imageInput.value = '';
                    return;
                }

                if (file.size > 5 * 1024 * 1024) {
                    adminNotice('Размер фотографии не должен превышать 5 МБ');
                    imageInput.value = '';
                    return;
                }

                imagePreview.src = URL.createObjectURL(file);
                product._draftImageFile = file;
            });

            imageWrapper.append(imageLabel, imagePreview, imageInput);
            imagePreview.addEventListener('click', () => {
                if (imagePreview.src) window.open(imagePreview.src, '_blank', 'noopener');
            });

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
                    adminNotice('Заполни название, раздел и категорию товара');
                    return;
                }

                if (price.value === '' ||
                    !Number.isFinite(priceValue) ||
                    priceValue < 0 ||
                    priceValue > 9999999999.99) {
                    adminNotice('Укажи корректную цену');
                    return;
                }

                if (stock.value !== '' &&
                    (!Number.isFinite(Number(stock.value)) ||
                        Number(stock.value) < 0 ||
                        Number(stock.value) > 999999999.999)) {
                    adminNotice('Укажи корректное количество');
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
                    adminNotice('Ошибка сохранения: ' + error.message);
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
                        adminNotice(error.message || 'Ошибка удаления товара');
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
            adminNotice('Заполни название, раздел и категорию товара: ' + name);
            return;
        }

        const priceValue = Number(price);

        if (price === '' || price === null || price === undefined ||
            !Number.isFinite(priceValue) ||
            priceValue < 0 || priceValue > 9999999999.99) {
            adminNotice('Укажи корректную цену у товара: ' + name);
            return;
        }

        if (stock !== null && stock !== undefined && stock !== '' &&
            (!Number.isFinite(Number(stock)) ||
                Number(stock) < 0 || Number(stock) > 999999999.999)) {
            adminNotice('Некорректный остаток у товара: ' + name);
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
        adminNotice('Ошибка массового сохранения: ' + error.message);
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


let managedCatalog = [];
let managedCatalogLoaded = false;
const catalogPanel = document.getElementById('catalogPanel');
const productTabElements = [
  document.getElementById('search'), document.querySelector('.product-create'),
  document.querySelector('.sheet-scroll'), document.querySelector('.bulk-save-container'),
  document.getElementById('message')
];
document.querySelectorAll('#adminTabs button').forEach(btn => btn.addEventListener('click', () => {
  const isCatalog = btn.dataset.tab === 'catalog';
  catalogPanel.hidden = !isCatalog;
  productTabElements.forEach(el => { if(el) el.hidden = isCatalog; });
  document.querySelectorAll('#adminTabs button').forEach(b => {
    b.style.background = b===btn ? '#286bd6' : '#d7e0eb';
    b.style.color = b===btn ? 'white' : '#24354a';
  });
  if(isCatalog) loadManagedCatalog();
}));
async function catalogRequest(payload) {
  const res=await fetch('/api/admin/catalog/structure',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(payload)});
  if(!res.ok) throw new Error(await res.text());
  return res.json();
}
function catalogNotice(text){
 const msg=document.getElementById('catalogMessage');
 if(msg) msg.textContent=text;
}
function catalogEditDialog(title, fields){
 return new Promise(resolve=>{
  const overlay=document.getElementById('catalogEditOverlay');
  const form=document.getElementById('catalogEditForm');
  document.getElementById('catalogEditTitle').textContent=title;
  form.replaceChildren();
  fields.forEach(field=>{
   const label=document.createElement('label');label.textContent=field.label;
   const input=field.type==='textarea'?document.createElement('textarea'):document.createElement('input');
   if(field.type!=='textarea') input.type=field.type||'text';
   input.name=field.name;input.value=field.value??'';
   if(field.type==='number') input.step='1';
   label.append(input);form.append(label);
  });
  const actions=document.createElement('div');actions.className='catalog-edit-actions';
  const cancel=document.createElement('button');cancel.type='button';cancel.className='catalog-edit-cancel';cancel.textContent='Отмена';
  const save=document.createElement('button');save.type='submit';save.className='catalog-edit-save';save.textContent='Сохранить';
  actions.append(cancel,save);form.append(actions);
  const finish=value=>{overlay.hidden=true;overlay.onclick=null;form.onsubmit=null;cancel.onclick=null;resolve(value);};
  cancel.onclick=()=>finish(null);
  overlay.onclick=e=>{if(e.target===overlay)finish(null);};
  form.onsubmit=e=>{e.preventDefault();const data={};fields.forEach(f=>{const el=form.elements.namedItem(f.name);data[f.name]=f.type==='number'?Number(el.value||0):el.value.trim();});finish(data);};
  overlay.hidden=false;form.querySelector('input,textarea')?.focus();
 });
}
function catalogConfirmDialog(message){
 return new Promise(resolve=>{
  const overlay=document.getElementById('catalogEditOverlay');
  const form=document.getElementById('catalogEditForm');
  document.getElementById('catalogEditTitle').textContent='Подтверждение';
  form.replaceChildren();
  const text=document.createElement('p');text.textContent=message;text.style.margin='0 0 8px';form.append(text);
  const actions=document.createElement('div');actions.className='catalog-edit-actions';
  const cancel=document.createElement('button');cancel.type='button';cancel.className='catalog-edit-cancel';cancel.textContent='Отмена';
  const yes=document.createElement('button');yes.type='button';yes.className='delete-confirm-btn';yes.textContent='Удалить';
  actions.append(cancel,yes);form.append(actions);
  const finish=value=>{overlay.hidden=true;overlay.onclick=null;cancel.onclick=null;yes.onclick=null;resolve(value);};
  cancel.onclick=()=>finish(false);yes.onclick=()=>finish(true);
  overlay.onclick=e=>{if(e.target===overlay)finish(false);};
  overlay.hidden=false;
 });
}
async function loadManagedCatalog() {
 const box=document.getElementById('catalogSections');
 try{
  const res=await fetch('/api/catalog/structure',{cache:'no-store'});
  if(!res.ok)throw new Error('Не удалось загрузить каталог');
  const nextCatalog=await res.json();
  const changed=JSON.stringify(managedCatalog)!==JSON.stringify(nextCatalog);
  // Match stable IDs so renaming a category does not invalidate product drafts.
  function renamedSelection(sectionName, categoryName) {
   const oldSection=managedCatalog.find(s=>s.name===sectionName);
   const nextSection=oldSection && nextCatalog.find(s=>s.id===oldSection.id);
   if(!nextSection) return [sectionName,categoryName];
   const oldCategory=(oldSection.categories||[]).find(c=>c.name===categoryName);
   const nextCategory=oldCategory && (nextSection.categories||[]).find(c=>c.id===oldCategory.id);
   return [nextSection.name,nextCategory ? nextCategory.name : categoryName];
  }
  if(changed){
   for(const p of products){
    const draft=renamedSelection(p._draftSection ?? p.section,p._draftCategory ?? p.category);
    const saved=renamedSelection(p.section,p.category);
    [p.section,p.category]=saved;
    if(p._draftSection!==undefined) p._draftSection=draft[0];
    if(p._draftCategory!==undefined) p._draftCategory=draft[1];
   }
  }
  const newSection=document.getElementById('newSection');
  const newCategory=document.getElementById('newCategory');
  const selection=renamedSelection(newSection.value,newCategory.value);
  managedCatalog=nextCatalog;managedCatalogLoaded=true;box.replaceChildren();
  managedCatalog.forEach(section=>{
   const card=document.createElement('div');card.className='catalog-section';
   const head=document.createElement('div');head.className='catalog-section-head';
   const title=document.createElement('strong');title.textContent=(section.icon||'📦')+' '+section.name+' · порядок '+section.sort;
   const edit=document.createElement('button');edit.textContent='Редактировать раздел';
   edit.onclick=async()=>{
    const v=await catalogEditDialog('Редактирование раздела',[
     {name:'name',label:'Название',value:section.name},{name:'description',label:'Описание',type:'textarea',value:section.description||''},
     {name:'icon',label:'Иконка',value:section.icon||'📦'},{name:'sort',label:'Порядок отображения',type:'number',value:section.sort}
    ]);if(!v||!v.name)return;
    try{await catalogRequest({action:'update_section',id:section.id,...v});await loadManagedCatalog();catalogNotice('Раздел сохранён.');}
    catch(e){catalogNotice('Ошибка: '+e.message);}
   };
   const del=document.createElement('button');del.textContent='Удалить раздел';del.className='delete-product-btn';
   del.onclick=async()=>{if(!await catalogConfirmDialog('Удалить пустой раздел «'+section.name+'»?'))return;
    try{await catalogRequest({action:'delete_section',id:section.id});await loadManagedCatalog();catalogNotice('Раздел удалён.');}catch(e){catalogNotice('Ошибка: '+e.message);}};
   const add=document.createElement('button');add.textContent='+ Категория';add.className='save';
   add.onclick=async()=>{const v=await catalogEditDialog('Новая категория',[
    {name:'name',label:'Название категории',value:''},{name:'sort',label:'Порядок отображения',type:'number',value:0}
   ]);if(!v||!v.name)return;
    try{await catalogRequest({action:'create_category',section_id:section.id,...v});await loadManagedCatalog();catalogNotice('Категория добавлена.');}catch(e){catalogNotice('Ошибка: '+e.message);}};
   head.append(title,edit,del,add);card.append(head);
   (section.categories||[]).forEach(cat=>{
    const row=document.createElement('div');row.className='catalog-category';
    const label=document.createElement('span');label.textContent=cat.name+' · порядок '+cat.sort;
    const change=document.createElement('button');change.textContent='Редактировать';
    change.onclick=async()=>{const v=await catalogEditDialog('Редактирование категории',[
     {name:'name',label:'Название',value:cat.name},{name:'sort',label:'Порядок отображения',type:'number',value:cat.sort}
    ]);if(!v||!v.name)return;
     try{await catalogRequest({action:'update_category',id:cat.id,...v});await loadManagedCatalog();catalogNotice('Категория сохранена.');}catch(e){catalogNotice('Ошибка: '+e.message);}};
    const remove=document.createElement('button');remove.textContent='Удалить';remove.className='delete-product-btn';
    remove.onclick=async()=>{if(!await catalogConfirmDialog('Удалить пустую категорию «'+cat.name+'»?'))return;
     try{await catalogRequest({action:'delete_category',id:cat.id});await loadManagedCatalog();catalogNotice('Категория удалена.');}catch(e){catalogNotice('Ошибка: '+e.message);}};
    row.append(label,change,remove);card.append(row);
   });
   box.append(card);
  });
  catalogNotice('Разделов: '+managedCatalog.length+' · структура обновлена.');
  updateCategoryLists();
  newSection.value=selection[0];
  updateSubcategories(selection[1]);
  if(changed) renderProducts();
 }catch(e){catalogNotice(e.message);}
}
document.getElementById('catalogAddSection').addEventListener('click',async()=>{
 const name=document.getElementById('catalogSectionName').value.trim();if(!name){catalogNotice('Введите название раздела.');return;}
 try{await catalogRequest({action:'create_section',name,description:document.getElementById('catalogSectionDesc').value,
 icon:document.getElementById('catalogSectionIcon').value,sort:Number(document.getElementById('catalogSectionSort').value||0)});
 document.getElementById('catalogSectionName').value='';document.getElementById('catalogSectionDesc').value='';
 await loadManagedCatalog();updateCategoryLists();
 }catch(e){adminNotice(e.message)}
});

loadManagedCatalog().then(loadProducts);
</script>
</body>
</html>`

// catalogStructureHandler exposes active sections/categories for the storefront.
func catalogStructureHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	rows, err := dbPool.Query(r.Context(), `
		SELECT s.id, s.name, s.description, s.icon, s.sort_order,
		       c.id, c.name, c.sort_order
		FROM product_sections s
		LEFT JOIN product_categories c ON c.section_id=s.id AND c.active=true
		WHERE s.active=true
		ORDER BY s.sort_order, s.name, c.sort_order, c.name`)
	if err != nil {
		http.Error(w, "catalog unavailable", http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	type category struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
		Sort int    `json:"sort"`
	}
	type section struct {
		ID          int64      `json:"id"`
		Name        string     `json:"name"`
		Description string     `json:"description"`
		Icon        string     `json:"icon"`
		Sort        int        `json:"sort"`
		Categories  []category `json:"categories"`
	}
	list := []section{}
	index := map[int64]int{}
	for rows.Next() {
		var sid int64
		var name, desc, icon string
		var sort int
		var cid *int64
		var cname *string
		var csort *int
		if err := rows.Scan(&sid, &name, &desc, &icon, &sort, &cid, &cname, &csort); err != nil {
			http.Error(w, "catalog read failed", 500)
			return
		}
		pos, ok := index[sid]
		if !ok {
			pos = len(list)
			index[sid] = pos
			list = append(list, section{ID: sid, Name: name, Description: desc, Icon: icon, Sort: sort, Categories: []category{}})
		}
		if cid != nil {
			list[pos].Categories = append(list[pos].Categories, category{ID: *cid, Name: *cname, Sort: *csort})
		}
	}
	if err := rows.Err(); err != nil {
		http.Error(w, "catalog read failed", 500)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(list)
}

func adminCatalogStructureHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64*1024)
	var req struct {
		Action      string `json:"action"`
		ID          int64  `json:"id"`
		SectionID   int64  `json:"section_id"`
		Name        string `json:"name"`
		Description string `json:"description"`
		Icon        string `json:"icon"`
		Sort        int    `json:"sort"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON", 400)
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	req.Description = strings.TrimSpace(req.Description)
	req.Icon = strings.TrimSpace(req.Icon)
	if len(req.Name) > 100 || len(req.Description) > 500 || len(req.Icon) > 20 {
		http.Error(w, "слишком длинное поле", 400)
		return
	}
	ctx := r.Context()
	switch req.Action {
	case "create_section":
		if req.Name == "" {
			http.Error(w, "Укажите название раздела", 400)
			return
		}
		_, err := dbPool.Exec(ctx, `INSERT INTO product_sections(name,description,icon,sort_order) VALUES($1,$2,COALESCE(NULLIF($3,''),'📦'),$4)`, req.Name, req.Description, req.Icon, req.Sort)
		if err != nil {
			http.Error(w, "Раздел уже существует или ошибка БД", 409)
			return
		}
	case "update_section":
		if req.ID <= 0 || req.Name == "" {
			http.Error(w, "Некорректный раздел", 400)
			return
		}
		tx, err := dbPool.Begin(ctx)
		if err != nil {
			http.Error(w, "database error", 500)
			return
		}
		defer tx.Rollback(ctx)
		var old string
		if err = tx.QueryRow(ctx, `SELECT name FROM product_sections WHERE id=$1 FOR UPDATE`, req.ID).Scan(&old); err != nil {
			http.Error(w, "Раздел не найден", 404)
			return
		}
		_, err = tx.Exec(ctx, `UPDATE product_sections SET name=$2,description=$3,icon=COALESCE(NULLIF($4,''),'📦'),sort_order=$5 WHERE id=$1`, req.ID, req.Name, req.Description, req.Icon, req.Sort)
		if err == nil && old != req.Name {
			_, err = tx.Exec(ctx, `UPDATE products SET section=$2 WHERE section=$1`, old, req.Name)
		}
		if err != nil {
			http.Error(w, "Не удалось сохранить раздел (возможно, имя занято)", 409)
			return
		}
		if err = tx.Commit(ctx); err != nil {
			http.Error(w, "database error", 500)
			return
		}
	case "delete_section":
		if req.ID <= 0 {
			http.Error(w, "Некорректный ID", 400)
			return
		}
		var name string
		if err := dbPool.QueryRow(ctx, `SELECT name FROM product_sections WHERE id=$1`, req.ID).Scan(&name); err != nil {
			http.Error(w, "Раздел не найден", 404)
			return
		}
		var count int
		if err := dbPool.QueryRow(ctx, `SELECT count(*) FROM products WHERE section=$1`, name).Scan(&count); err != nil {
			http.Error(w, "database error", 500)
			return
		}
		if count > 0 {
			http.Error(w, "Нельзя удалить раздел: в нём есть товары. Сначала перенесите или удалите их.", 409)
			return
		}
		_, err := dbPool.Exec(ctx, `DELETE FROM product_sections WHERE id=$1`, req.ID)
		if err != nil {
			http.Error(w, "Не удалось удалить раздел", 500)
			return
		}
	case "create_category":
		if req.SectionID <= 0 || req.Name == "" {
			http.Error(w, "Выберите раздел и название категории", 400)
			return
		}
		_, err := dbPool.Exec(ctx, `INSERT INTO product_categories(section_id,name,sort_order) VALUES($1,$2,$3)`, req.SectionID, req.Name, req.Sort)
		if err != nil {
			http.Error(w, "Категория уже существует или раздел не найден", 409)
			return
		}
	case "update_category":
		if req.ID <= 0 || req.Name == "" {
			http.Error(w, "Некорректная категория", 400)
			return
		}
		tx, err := dbPool.Begin(ctx)
		if err != nil {
			http.Error(w, "database error", 500)
			return
		}
		defer tx.Rollback(ctx)
		var old, section string
		if err = tx.QueryRow(ctx, `SELECT c.name,s.name FROM product_categories c JOIN product_sections s ON s.id=c.section_id WHERE c.id=$1 FOR UPDATE`, req.ID).Scan(&old, &section); err != nil {
			http.Error(w, "Категория не найдена", 404)
			return
		}
		_, err = tx.Exec(ctx, `UPDATE product_categories SET name=$2,sort_order=$3 WHERE id=$1`, req.ID, req.Name, req.Sort)
		if err == nil && old != req.Name {
			_, err = tx.Exec(ctx, `UPDATE products SET category=$3 WHERE section=$1 AND category=$2`, section, old, req.Name)
		}
		if err != nil {
			http.Error(w, "Не удалось сохранить категорию (возможно, имя занято)", 409)
			return
		}
		if err = tx.Commit(ctx); err != nil {
			http.Error(w, "database error", 500)
			return
		}
	case "delete_category":
		if req.ID <= 0 {
			http.Error(w, "Некорректный ID", 400)
			return
		}
		var section, name string
		if err := dbPool.QueryRow(ctx, `SELECT c.name,s.name FROM product_categories c JOIN product_sections s ON s.id=c.section_id WHERE c.id=$1`, req.ID).Scan(&name, &section); err != nil {
			http.Error(w, "Категория не найдена", 404)
			return
		}
		var count int
		if err := dbPool.QueryRow(ctx, `SELECT count(*) FROM products WHERE section=$1 AND category=$2`, section, name).Scan(&count); err != nil {
			http.Error(w, "database error", 500)
			return
		}
		if count > 0 {
			http.Error(w, "В категории есть товары. Сначала перенесите или удалите их.", 409)
			return
		}
		_, err := dbPool.Exec(ctx, `DELETE FROM product_categories WHERE id=$1`, req.ID)
		if err != nil {
			http.Error(w, "Не удалось удалить категорию", 500)
			return
		}
	default:
		http.Error(w, "unknown action", 400)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = w.Write([]byte(`{"success":true}`))
}

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
