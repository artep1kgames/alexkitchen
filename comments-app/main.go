package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"
)

func generateIdempotenceKey() (string, error) {
	b := make([]byte, 16)

	if _, err := rand.Read(b); err != nil {
		return "", err
	}

	return hex.EncodeToString(b), nil
}

var dbPool *pgxpool.Pool

type Comment struct {
	ID        int64  `json:"id"`
	PostID    string `json:"post_id"`
	UserID    string `json:"user_id"`
	Name      string `json:"name"`
	Username  string `json:"username"`
	Text      string `json:"text"`
	CreatedAt string `json:"created_at"`
}

type Store struct {
	mu       sync.Mutex
	Comments []Comment `json:"comments"`
}

var dataDir = "."

func dataFile(name string) string {
	return filepath.Join(dataDir, name)
}

var storePath = "comments.json"
var store = Store{}
var pageTpl = template.Must(template.New("page").Parse(pageHTML))

func loadStore() {
	data, err := os.ReadFile(storePath)
	if err != nil {
		store.Comments = []Comment{}
		return
	}
	_ = json.Unmarshal(data, &store)
}

func saveStore() error {
	data, err := json.MarshalIndent(&store, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(storePath, data, 0644)
}

func commentsPage(w http.ResponseWriter, r *http.Request) {
	postID := strings.TrimSpace(r.URL.Query().Get("post"))
	if postID == "" {
		postID = strings.TrimSpace(r.URL.Query().Get("WebAppStartParam"))
	}
	if postID == "" {
		postID = "waiting"
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = pageTpl.Execute(w, map[string]string{"PostID": postID})
}

func apiList(w http.ResponseWriter, r *http.Request) {
	postID := strings.TrimSpace(r.URL.Query().Get("post"))

	store.mu.Lock()
	defer store.mu.Unlock()

	var result []Comment
	for _, c := range store.Comments {
		if c.PostID == postID {
			result = append(result, c)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(result)
}

func apiAdd(w http.ResponseWriter, r *http.Request) {
	var req struct {
		PostID   string `json:"post_id"`
		UserID   string `json:"user_id"`
		Name     string `json:"name"`
		Username string `json:"username"`
		Text     string `json:"text"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)

	req.PostID = strings.TrimSpace(req.PostID)
	req.UserID = strings.TrimSpace(req.UserID)
	req.Name = strings.TrimSpace(req.Name)
	req.Username = strings.TrimSpace(req.Username)
	req.Text = strings.TrimSpace(req.Text)

	if req.PostID == "" || req.Text == "" {
		http.Error(w, "post_id and text are required", http.StatusBadRequest)
		return
	}
	if req.Name == "" {
		req.Name = "Пользователь MAX"
	}

	store.mu.Lock()
	defer store.mu.Unlock()

	comment := Comment{
		ID:        time.Now().UnixNano(),
		PostID:    req.PostID,
		UserID:    req.UserID,
		Name:      req.Name,
		Username:  req.Username,
		Text:      req.Text,
		CreatedAt: time.Now().Format("02.01.2006 15:04"),
	}

	store.Comments = append(store.Comments, comment)
	if err := saveStore(); err != nil {
		store.Comments = store.Comments[:len(store.Comments)-1]
		log.Printf("Не удалось сохранить комментарий: %v", err)
		http.Error(w, "failed to save comment", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(comment)
}

func appPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(appHTML))
}

func shopPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(shopHTML))
}

func health(w http.ResponseWriter, r *http.Request) {
	_, _ = w.Write([]byte("ok"))
}

func initDB() {
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		log.Fatal("DATABASE_URL не задан")
	}

	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		log.Fatalf("Ошибка конфигурации PostgreSQL: %v", err)
	}

	dbPool, err = pgxpool.NewWithConfig(context.Background(), config)
	if err != nil {
		log.Fatalf("Ошибка подключения к PostgreSQL: %v", err)
	}

	if err := dbPool.Ping(context.Background()); err != nil {
		log.Fatalf("PostgreSQL недоступен: %v", err)
	}

	log.Println("PostgreSQL подключён успешно")
}

func main() {
	if err := godotenv.Load(); err != nil {
		log.Println("Файл .env не найден, используются системные переменные окружения")
	}

	if configured := strings.TrimSpace(os.Getenv("DATA_DIR")); configured != "" {

		dataDir = configured

	}

	if err := os.MkdirAll(dataDir, 0755); err != nil {

		log.Fatalf("Не удалось создать каталог данных: %v", err)

	}

	storePath = dataFile("comments.json")

	loadStore()
	if err := loadAdminConfig(); err != nil {
		log.Fatal("Ошибка конфигурации администраторов: ", err)
	}

	initDB()
	defer dbPool.Close()

	http.HandleFunc("/health", health)
	http.HandleFunc("/app", appPage)
	http.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir("static"))))
	http.HandleFunc("/shop", shopPage)
	http.HandleFunc("/api/products", productsHandler)
	http.HandleFunc("/api/cart/reserve", cartReserveHandler)
	http.HandleFunc("/api/cart/clear", cartClearHandler)
	http.HandleFunc("/api/order", orderHandler)
	http.HandleFunc("/api/order/pay", yooKassaPaymentHandler)
	http.HandleFunc("/api/yookassa/webhook", yooKassaWebhookHandler)
	http.HandleFunc("/api/order/payment-status", orderPaymentStatusHandler)
	http.HandleFunc("/comments", commentsPage)
	http.HandleFunc("/api/comments", apiList)
	http.HandleFunc("/api/comments/add", apiAdd)
	http.HandleFunc("/admin", adminPage)
	http.HandleFunc("/api/admin/max-visibility", adminMaxVisibilityHandler)
	http.HandleFunc("/admin/login", adminLoginHandler)
	http.HandleFunc("/admin/logout", adminLogoutHandler)
	http.Handle("/api/admin/products", requireAdmin(adminProductsHandler))
	http.Handle("/api/admin/products/update", requireAdmin(adminUpdateProductHandler))
	http.Handle("/api/admin/products/delete", requireAdmin(adminDeleteProductHandler))
	http.Handle("/api/admin/products/create", requireAdmin(adminCreateProductHandler))
	http.Handle("/api/admin/products/upload-image", requireAdmin(adminUploadProductImageHandler))
	http.Handle("/api/admin/products/bulk-update", requireAdmin(adminBulkUpdateProductsHandler))

	log.Println("comments app started on :8081")
	log.Fatal(http.ListenAndServe(":8081", nil))
}

const pageHTML = `<!doctype html>
<html lang="ru">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1, viewport-fit=cover">
<title>Комментарии</title>
<script src="https://st.max.ru/js/max-web-app.js">




</script>
<style>
*{box-sizing:border-box}
html,body{margin:0;padding:0;height:100%;font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",Arial,sans-serif;color:#111}
body{
  background:#82c5ff;
  overflow:hidden;
}
.app{
  height:100vh;
  display:flex;
  flex-direction:column;
  background:
    radial-gradient(circle at 20% 20%, rgba(255,255,255,.25), transparent 24%),
    linear-gradient(180deg,#bde2ff 0%,#7bc2ff 45%,#71bcf6 100%);
}
.header{
  height:64px;
  background:#fff;
  display:flex;
  align-items:center;
  justify-content:center;
  position:relative;
  box-shadow:0 1px 0 rgba(0,0,0,.08);
  flex:0 0 auto;
  border-bottom-left-radius:18px;
  border-bottom-right-radius:18px;
}
.header-title{font-size:21px;font-weight:800}
.close{
  position:absolute;left:18px;top:17px;
  width:30px;height:30px;border:0;background:transparent;font-size:34px;line-height:24px;color:#111;
}
.menu{
  position:absolute;right:18px;top:16px;width:34px;height:34px;border-radius:50%;
  border:2px solid #222;background:#fff;font-size:18px;font-weight:800;
}
.toolbar{
  display:flex;align-items:center;justify-content:center;gap:12px;
  padding:14px 16px 8px;
  flex:0 0 auto;
}
.pill{
  background:#fff;
  border-radius:999px;
  padding:12px 28px;
  font-size:18px;
  font-weight:700;
  box-shadow:0 2px 8px rgba(0,0,0,.06);
}
.day{
  align-self:center;
  background:rgba(26,132,207,.70);
  color:#fff;
  border-radius:999px;
  padding:6px 14px;
  font-size:16px;
  margin:4px 0 10px;
  flex:0 0 auto;
}
.chat{
  flex:1 1 auto;
  overflow-y:auto;
  padding:0 14px 96px;
}
.row{
  display:flex;
  align-items:flex-end;
  gap:9px;
  margin:10px 0;
}
.avatar{
  width:42px;height:42px;border-radius:50%;
  background:linear-gradient(135deg,#177de8,#78c8ff);
  color:#fff;
  display:flex;align-items:center;justify-content:center;
  font-weight:800;font-size:18px;
  flex:0 0 auto;
  box-shadow:0 1px 4px rgba(0,0,0,.16);
}
.bubble{
  background:#fff;
  border-radius:18px 18px 18px 4px;
  padding:10px 12px 9px;
  max-width:78%;
  box-shadow:0 1px 4px rgba(0,0,0,.12);
}
.name{font-size:17px;font-weight:800;line-height:1.28;color:#17181c;}
.text{
  font-size:19px;
  line-height:1.28;
  white-space:pre-wrap;
  word-break:break-word;
}
.time{
  color:#808080;
  font-size:13px;
  text-align:right;
  margin-top:3px;
}
.empty{
  margin:40px auto 0;
  background:rgba(255,255,255,.82);
  color:#333;
  border-radius:18px;
  padding:18px;
  max-width:320px;
  text-align:center;
  font-size:17px;
}
.composer{
  position:fixed;
  left:0;right:0;bottom:0;
  padding:10px 10px calc(10px + env(safe-area-inset-bottom));
  background:rgba(255,255,255,.96);
  display:flex;
  align-items:center;
  gap:8px;
  border-top-left-radius:22px;
  border-top-right-radius:22px;
  box-shadow:0 -2px 12px rgba(0,0,0,.12);
}
.attach{
  width:42px;height:42px;border:0;background:transparent;font-size:30px;color:#222;
  flex:0 0 auto;
}
.input{
  flex:1;
  min-height:44px;
  max-height:110px;
  border:0;
  outline:none;
  resize:none;
  border-radius:16px;
  background:#f2f3f5;
  padding:12px 14px;
  font-size:18px;
  color:#111;
}
.send{
  width:48px;height:48px;border:0;border-radius:50%;
  background:#1687f5;color:#fff;font-size:30px;font-weight:900;
  display:flex;align-items:center;justify-content:center;
  flex:0 0 auto;
}
.send:active{transform:scale(.96)}
.userbox{display:none}

.hero{
  position:relative!important;
  padding-left:132px!important;
  box-sizing:border-box!important;
}
.hero-logo{
  position:absolute!important;
  left:18px!important;
  top:50%!important;
  transform:translateY(-50%)!important;
  width:88px!important;
  height:88px!important;
  border-radius:50%!important;
  z-index:5!important;
}
.hero h1,
.hero p{
  margin-left:0!important;
  padding-left:0!important;
  max-width:100%!important;
}

.qty-wrap{display:flex;align-items:center;gap:10px;margin-top:14px;}
.qty-btn{width:38px;height:38px;border:none;border-radius:12px;background:#1688f8;color:#fff;font-size:24px;font-weight:900;}
.qty-num{min-width:70px;text-align:center;font-size:17px;font-weight:800;color:#111;}

.to-cart-btn{margin-top:12px;width:100%;height:46px;border:0;border-radius:16px;background:#111;color:#fff;font-size:17px;font-weight:900;}

.cart-modal{position:fixed;inset:0;background:rgba(0,0,0,.45);z-index:99999;display:flex;align-items:flex-end;}
.cart-panel{width:100%;max-height:82vh;overflow:auto;background:#fff;border-radius:28px 28px 0 0;padding:22px;box-sizing:border-box;}
.cart-title{font-size:30px;font-weight:900;margin-bottom:14px;color:#111;}
.cart-item{background:#f3f5f8;border-radius:20px;padding:16px;margin-bottom:12px;}
.cart-name{font-size:16px;font-weight:800;color:#17181c;line-height:1.3;margin-bottom:8px;}
.cart-price{font-size:16px;color:#7b8494;margin-bottom:12px;}
.cart-line{display:flex;align-items:center;justify-content:space-between;gap:10px;}
.cart-remove{border:0;background:#fff;color:#e53935;border-radius:14px;padding:10px 12px;font-weight:900;}
.cart-empty{background:#f3f5f8;border-radius:20px;padding:18px;color:#7b8494;font-size:17px;}
.cart-close,.cart-submit{width:100%;height:54px;border:0;border-radius:18px;font-size:18px;font-weight:900;margin-top:10px;}
.cart-submit{background:#1688f8;color:#fff;}
.cart-close{background:#eef0f3;color:#111;}

.checkout-field{width:100%;box-sizing:border-box;border:1px solid #dde3ea;border-radius:16px;padding:14px 16px;font-size:17px;margin-top:10px;}
.checkout-label{font-size:15px;font-weight:900;color:#6f7785;margin-top:14px;}
.checkout-total{background:#111;color:#fff;border-radius:18px;padding:16px;margin:14px 0;font-size:20px;font-weight:900;}
.checkout-choice{display:flex;gap:10px;margin-top:10px;}
.checkout-choice button{flex:1;height:46px;border:0;border-radius:16px;background:#eef0f3;color:#111;font-size:16px;font-weight:900;}
.checkout-choice button.active{background:#1688f8;color:#fff;}

.cart-panel{width:100%;max-height:88vh;overflow:auto;background:#fff;border-radius:30px 30px 0 0;padding:22px 18px;box-sizing:border-box;}
.cart-title{font-size:28px;font-weight:700;margin-bottom:4px;color:#4f5663;text-align:center;}
.cart-subtitle{font-size:15px;color:#7b8494;text-align:center;margin-bottom:18px;}
.cart-item{display:grid;grid-template-columns:82px 1fr auto;gap:14px;align-items:center;background:#fff;border-radius:0;padding:14px 0;margin-bottom:0;border-bottom:1px solid #edf0f3;}
.cart-img{width:82px;height:82px;border-radius:20px;object-fit:cover;background:#f3f5f8;}
.cart-img-placeholder{width:82px;height:82px;border-radius:20px;background:#f3f5f8;display:flex;align-items:center;justify-content:center;font-size:28px;}
.cart-name{font-size:16px;font-weight:700;color:#555e6d;line-height:1.3;margin-bottom:8px;}
.cart-price{font-size:15px;color:#777;margin-bottom:10px;}
.cart-line{display:flex;align-items:center;gap:8px;justify-content:flex-start;}
.cart-line .qty-btn{width:34px;height:34px;border-radius:12px;background:#f4f4f6;color:#202124;font-size:22px;}
.cart-line .qty-num{min-width:58px;font-size:15px;font-weight:800;}
.cart-remove{width:34px;height:34px;border:0;background:#f4f4f6;color:#222;border-radius:12px;font-weight:900;font-size:17px;padding:0;}
.cart-total-line{font-size:17px;font-weight:800;color:#17181c;white-space:nowrap;align-self:center;}
.cart-summary{background:#111;color:#fff;border-radius:22px;padding:16px;margin:18px 0 10px;font-size:20px;font-weight:900;text-align:center;}

/* final typography override */
.title{
  color:#596170!important;
  font-weight:700!important;
}
.desc{
  color:#8a93a3!important;
}
.name{
  color:#17181c!important;
  font-weight:800!important;
}
.price{
  color:#17181c!important;
  font-weight:800!important;
}
.cart-name{
  color:#17181c!important;
  font-weight:800!important;
}
.cart-total-line{
  color:#17181c!important;
  font-weight:800!important;
}

/* FINAL COLORS */
.title{
  color:#596170!important;
  font-weight:700!important;
}
.desc{
  color:#8a93a3!important;
}
.name{
  color:#17181c!important;
  font-weight:800!important;
}
.price{
  color:#17181c!important;
  font-weight:800!important;
}
.cart-name{
  color:#17181c!important;
  font-weight:800!important;
}
.cart-total-line{
  color:#17181c!important;
  font-weight:800!important;
}


.subcats-grid{
  display:grid;
  grid-template-columns:repeat(2, minmax(0, 1fr));
  gap:12px;
  padding:0 16px 120px;
}

.subcat{
  min-height:145px;
  border:0;
  border-radius:24px;
  background:#fff;
  box-shadow:0 12px 30px rgba(20,25,35,.07);
  padding:16px;
  text-align:left;
}

.subcat .icon{
  width:54px;
  height:54px;
  border-radius:18px;
  background:#f4f5f7;
  display:flex;
  align-items:center;
  justify-content:center;
  font-size:26px;
  margin-bottom:16px;
}

.subcat .title{
  font-size:22px;
  line-height:1.12;
  font-weight:900;
  color:#596170;
  margin-bottom:8px;
}

.subcat .desc{
  font-size:15px;
  line-height:1.25;
  color:#8a92a1;
}


.cat-icon,
.category-icon,
.icon{
  width:64px !important;
  height:64px !important;
  min-width:64px !important;
  min-height:64px !important;
  display:flex !important;
  align-items:center !important;
  justify-content:center !important;
  border-radius:18px !important;
  background:#f4f5f7 !important;
  font-size:32px !important;
  line-height:1 !important;
  box-sizing:border-box !important;
}


.icon{
  width:60px !important;
  height:60px !important;
  min-width:60px !important;
  min-height:60px !important;
  padding:0 !important;
  border-radius:18px !important;
  display:flex !important;
  align-items:center !important;
  justify-content:center !important;
  background:#f4f5f7 !important;
  overflow:hidden !important;
  box-sizing:border-box !important;
}

.icon .icon-emoji{
  display:block !important;
  width:34px !important;
  height:34px !important;
  font-size:30px !important;
  line-height:34px !important;
  text-align:center !important;
  transform:scale(1) !important;
  transform-origin:center center !important;
}

.icon img{
  width:34px !important;
  height:34px !important;
  object-fit:contain !important;
  border-radius:0 !important;
  background:transparent !important;
}


.grid{
  display:grid !important;
  grid-template-columns:repeat(2, minmax(0, 1fr)) !important;
  gap:14px !important;
  padding:0 18px 120px !important;
  align-items:stretch !important;
}

.cat{
  width:100% !important;
  height:190px !important;
  min-height:190px !important;
  max-height:190px !important;
  padding:18px !important;
  margin:0 !important;
  box-sizing:border-box !important;
  display:flex !important;
  flex-direction:column !important;
  align-items:flex-start !important;
  justify-content:flex-start !important;
  overflow:hidden !important;
}

.cat .icon{
  flex:0 0 60px !important;
}

.cat .title{
  min-height:44px !important;
  max-height:44px !important;
  margin:0 !important;
  overflow:hidden !important;
}

.cat .desc{
  margin-top:8px !important;
}


.cat{
  height:190px !important;
  min-height:190px !important;
  max-height:190px !important;
  box-sizing:border-box !important;
}

.cat .title,
.cat .name{
  font-size:22px !important;
  line-height:1.12 !important;
  min-height:50px !important;
  max-height:50px !important;
  overflow:hidden !important;
  display:-webkit-box !important;
  -webkit-box-orient:vertical !important;
  -webkit-line-clamp:2 !important;
  word-break:normal !important;
  overflow-wrap:break-word !important;
}

.cat .desc{
  max-height:42px !important;
  overflow:hidden !important;
}


.cat{
  height:190px !important;
  min-height:190px !important;
  max-height:190px !important;
  box-sizing:border-box !important;
}

.cat .title,
.cat .name{
  font-size:22px !important;
  line-height:1.12 !important;
  min-height:50px !important;
  max-height:50px !important;
  overflow:hidden !important;
  display:-webkit-box !important;
  -webkit-box-orient:vertical !important;
  -webkit-line-clamp:2 !important;
  word-break:normal !important;
  overflow-wrap:break-word !important;
}

.cat .desc{
  max-height:42px !important;
  overflow:hidden !important;
}


.cat[data-id="social"] .title,
.cat[data-id="social"] .name{
  font-size:16px !important;
  line-height:1.05 !important;
  letter-spacing:-0.2px !important;
}


.social-title{
  font-size:15px !important;
  line-height:1.05 !important;
  letter-spacing:-0.3px !important;
}


.grid{
  display:grid !important;
  grid-template-columns:repeat(2, minmax(0, 1fr)) !important;
  grid-auto-rows:190px !important;
  gap:14px !important;
  align-items:stretch !important;
}

.cat{
  width:100% !important;
  height:190px !important;
  min-height:190px !important;
  max-height:190px !important;
  box-sizing:border-box !important;
  margin:0 !important;
  overflow:hidden !important;
  display:flex !important;
  flex-direction:column !important;
  align-items:flex-start !important;
}

.cat .title{
  width:100% !important;
  min-width:0 !important;
  overflow-wrap:break-word !important;
  word-break:normal !important;
}


.grid{
  display:grid !important;
  grid-template-columns:1fr 1fr !important;
  column-gap:14px !important;
  row-gap:14px !important;
  padding:0 18px 120px !important;
  width:100% !important;
  box-sizing:border-box !important;
  align-items:stretch !important;
}

.cat{
  width:100% !important;
  min-width:0 !important;
  height:190px !important;
  min-height:190px !important;
  max-height:190px !important;
  margin:0 !important;
  padding:18px !important;
  box-sizing:border-box !important;

  display:flex !important;
  flex-direction:column !important;
  align-items:flex-start !important;
  justify-content:flex-start !important;

  border-radius:24px !important;
  overflow:hidden !important;
}

.cat .icon{
  width:60px !important;
  height:60px !important;
  min-width:60px !important;
  min-height:60px !important;
  flex:0 0 60px !important;
  margin:0 0 16px 0 !important;
}

.cat .title{
  width:100% !important;
  min-width:0 !important;
  margin:0 !important;
  font-size:20px !important;
  line-height:1.08 !important;
  min-height:44px !important;
  max-height:44px !important;
  overflow:hidden !important;

  display:-webkit-box !important;
  -webkit-box-orient:vertical !important;
  -webkit-line-clamp:2 !important;

  overflow-wrap:anywhere !important;
}

.cat .desc{
  width:100% !important;
  min-width:0 !important;
  margin:8px 0 0 0 !important;
  font-size:15px !important;
  line-height:1.25 !important;
  max-height:38px !important;
  overflow:hidden !important;
}

</style>
</head>
<body>
<div class="app">
  <div class="header">
    <button class="close" onclick="closeApp()">×</button>
    <div class="header-title">Alex Kitchen | Обсуждение</div>
    <button class="menu">Р’В·Р’В·Р’В·</button>
  </div>

  <div class="toolbar">
    <div class="pill"><span id="count">0</span> комментариев</div>
  </div>

  <div class="day">Сегодня</div>

  <div class="chat" id="comments"></div>

  <div class="composer">
    <button class="attach">РІРЉВ</button>
    <textarea id="text" class="input" placeholder="Сообщение" rows="1"></textarea>
    <button class="send" onclick="sendComment()">↑</button>
  </div>
</div>

<script>
let postID = "{{.PostID}}";
let maxUser = {
  user_id: "",
  name: "Пользователь MAX",
  username: "",
  photo_url: ""
};

function readStartParam(){
  try{
    const wa = window.WebApp || null;
    const p = wa && wa.initDataUnsafe ? wa.initDataUnsafe.start_param : "";
    if(p) postID = String(p);
  }catch(e){}
}

function readMaxUser(){
  try{
    const wa = window.WebApp || null;
    if(!wa) return;

    const u = wa.initDataUnsafe && wa.initDataUnsafe.user ? wa.initDataUnsafe.user : null;
    if(!u) return;

    const first = u.first_name || "";
    const last = u.last_name || "";
    const full = [first,last].filter(Boolean).join(" ");

    maxUser = {
      user_id: String(u.id || ""),
      name: full || u.username || "Пользователь MAX",
      username: u.username || "",
      photo_url: u.photo_url || ""
    };

    if(wa.ready) wa.ready();
    if(wa.expand) wa.expand();
  }catch(e){}
}

function closeApp(){
  try{
    if(window.WebApp && window.WebApp.close) window.WebApp.close();
  }catch(e){}
}

async function loadComments(){
  const res = await fetch("/api/comments?post=" + encodeURIComponent(postID));
  const data = await res.json();
  const box = document.getElementById("comments");
  const count = document.getElementById("count");

  count.innerText = data ? data.length : 0;

  if(!data || data.length === 0){
    box.innerHTML = '<div class="empty">Пока нет комментариев.<br>Будьте первым.</div>';
    return;
  }

  let html = "";
  for(const c of data){
    const avatar = initials(c.name);
    html += '<div class="row">';
    html += '<div class="avatar">' + avatar + '</div>';
    html += '<div class="bubble">';
    html += '<div class="name">' + escapeHtml(c.name) + '</div>';
    html += '<div class="text">' + escapeHtml(c.text) + '</div>';
    html += '<div class="time">' + escapeHtml(c.created_at || "") + '</div>';
    html += '</div>';
    html += '</div>';
  }

  box.innerHTML = html;
  box.scrollTop = box.scrollHeight;
}

async function sendComment(){
  const input = document.getElementById("text");
  const text = input.value.trim();

  if(!text){
    return;
  }

  await fetch("/api/comments/add", {
    method: "POST",
    headers: {"Content-Type":"application/json"},
    body: JSON.stringify({
      post_id: postID,
      user_id: maxUser.user_id,
      name: maxUser.name,
      username: maxUser.username,
      text: text
    })
  });

  input.value = "";
  input.style.height = "44px";
  await loadComments();

  try{
    if(window.WebApp && window.WebApp.HapticFeedback){
      window.WebApp.HapticFeedback.notificationOccurred("success");
    }
  }catch(e){}
}

function initials(name){
  const s = String(name || "П").trim();
  return escapeHtml(s.slice(0,1).toUpperCase());
}

function escapeHtml(s){
  return String(s || "").replace(/[&<>"']/g,function(m){
    return {"&":"&amp;","<":"&lt;",">":"&gt;",'"':"&quot;","'":"&#039;"}[m];
  });
}

const input = document.getElementById("text");
input.addEventListener("input", function(){
  this.style.height = "44px";
  this.style.height = Math.min(this.scrollHeight,110) + "px";
});

input.addEventListener("keydown", function(e){
  if(e.key === "Enter" && !e.shiftKey){
    e.preventDefault();
    sendComment();
  }
});

readStartParam();
readMaxUser();
loadComments();
setInterval(loadComments,5000);
</script>
</body>
</html>`

const shopHTML = `<!doctype html>
<html lang="ru">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1, viewport-fit=cover">
<title>proМясо</title>
<script src="https://st.max.ru/js/max-web-app.js"></script>
<style>
*{box-sizing:border-box}
body{margin:0;padding:0;background:#f4f5f7;font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,Arial,sans-serif;color:#111}
.app{max-width:520px;margin:0 auto;min-height:100vh;background:#f4f5f7;padding-bottom:120px}

.hero{position:sticky;top:0;z-index:50;background:rgba(255,255,255,.94);backdrop-filter:blur(16px);padding:16px 18px 18px;border-bottom:1px solid rgba(0,0,0,.05)}
.hero-top{display:flex;align-items:center;gap:14px}
.hero-logo{width:72px;height:72px;border-radius:24px;object-fit:cover;box-shadow:0 8px 24px rgba(0,0,0,.14);flex-shrink:0;background:#fff}
.hero h1{margin:0;font-size:30px;font-weight:900;letter-spacing:-.04em;color:#111}
.hero p{margin:4px 0 0;color:#6d7380;font-size:14px;line-height:1.35}
.search-wrap{margin-top:16px}
.search{width:100%;height:52px;border:0;border-radius:18px;background:#eceff3;padding:0 18px;font-size:16px;box-sizing:border-box;outline:none}

.banner{
  margin:14px 18px 18px;
  border-radius:24px;
  overflow:hidden;
  color:#fff;
  padding:22px 18px;
  position:relative;
  min-height:240px;
  background:
    linear-gradient(
      180deg,
      rgba(0,0,0,.15) 0%,
      rgba(0,0,0,.35) 55%,
      rgba(0,0,0,.72) 100%
    ),
    url("/static/products/header-meat.jpg") center 52% / cover no-repeat;
  box-shadow:0 8px 28px rgba(0,0,0,.18);
}
.banner-title{font-size:22px;font-weight:900;line-height:1.1;margin-bottom:8px;max-width:300px;letter-spacing:-.035em}
.banner-text{color:rgba(255,255,255,.78);font-size:13px;line-height:1.4;max-width:320px}
.banner-chip{display:inline-flex;align-items:center;gap:8px;background:rgba(255,255,255,.14);border-radius:999px;padding:8px 12px;margin-top:12px;font-size:13px;font-weight:800}

.about-block{
  margin:18px;
  background:#fff;
  border-radius:24px;
  padding:18px;
  box-shadow:0 6px 20px rgba(0,0,0,.06)
}
.about-title{
  font-size:24px;
  font-weight:800;
  margin-bottom:14px
}
.about-text{
  color:#555;
  line-height:1.6;
  font-size:15px
}
.about-schedule{
  margin-top:14px;
  background:#f5f5f5;
  border-radius:14px;
  padding:12px;
  font-weight:700
}
.delivery-block{
  margin:18px;
  background:#fff;
  border-radius:24px;
  padding:18px;
  box-shadow:0 6px 20px rgba(0,0,0,.06)
}

.grid{
  display:grid;
  grid-template-columns:minmax(0,1fr) minmax(0,1fr);
  gap:14px;
  padding:0 18px 120px;
  width:100%;
  box-sizing:border-box;
}
.grid::-webkit-scrollbar{display:none}
.cat{
  width:100%;
  min-width:0;
  height:190px;
  min-height:190px;
  max-height:190px;
  border:0;
  border-radius:26px;
  background:#fff;
  padding:18px;
  text-align:left;
  box-sizing:border-box;
  box-shadow:0 8px 28px rgba(0,0,0,.06);
  overflow:hidden;
  display:flex;
  flex-direction:column;
  align-items:flex-start;
}
.icon{width:56px;height:56px;border-radius:18px;display:flex;align-items:center;justify-content:center;background:#f4f5f7;font-size:28px;margin-bottom:14px}
.title{
  width:100%;
  min-width:0;
  font-size:18px;
  font-weight:900;
  line-height:1.2;
  overflow-wrap:anywhere;
  word-break:normal;
}
.desc{font-size:12px;color:#8a93a3;margin-top:8px;line-height:1.35;}

.topbar{position:sticky;top:0;z-index:60;background:rgba(244,245,247,.92);backdrop-filter:blur(16px);display:flex;align-items:center;gap:10px;padding:14px 18px;margin:0 -14px 14px}
.back{border:0;background:#fff;border-radius:16px;padding:11px 14px;font-size:16px;font-weight:900;box-shadow:0 4px 12px rgba(0,0,0,.07)}
.section{font-size:22px;font-weight:900;letter-spacing:-.03em}

.products{padding:0 18px 120px}
.product-img{width:100%;height:220px;object-fit:cover;border-radius:24px;display:block;margin-bottom:16px}
.card{background:#fff;border-radius:30px;padding:18px;margin-bottom:18px;box-shadow:0 10px 32px rgba(0,0,0,.06)}
.name{font-size:22px;font-weight:900;line-height:1.15;letter-spacing:-.03em}
.note{margin-top:8px;color:#6d7380;font-size:14px;line-height:1.45}
.row{display:flex;align-items:center;justify-content:space-between;gap:12px;margin-top:16px}
.price{font-size:24px;font-weight:900;letter-spacing:-.03em;color:#111}

.qty-wrap{display:flex;align-items:center;justify-content:center;gap:12px;margin-top:16px}
.qty-btn{width:44px;height:44px;border:0;border-radius:16px;background:#f1f3f6;color:#111;font-size:24px;font-weight:900}
.qty-num{min-width:92px;text-align:center;font-size:18px;font-weight:900;color:#111}
.to-cart-btn{width:100%;height:56px;border:0;border-radius:20px;background:#111;color:#fff;font-size:17px;font-weight:900;margin-top:18px}

.bottom{position:fixed;left:0;right:0;bottom:0;z-index:100;padding:14px 16px calc(env(safe-area-inset-bottom) + 14px);background:rgba(255,255,255,.94);backdrop-filter:blur(20px);border-top:1px solid rgba(0,0,0,.06)}
.cart{width:100%;height:58px;border:0;border-radius:22px;background:#111;color:#fff;font-size:18px;font-weight:900;box-shadow:0 10px 28px rgba(0,0,0,.18)}
.hidden{display:none}

.cart-modal{position:fixed;inset:0;background:rgba(0,0,0,.45);z-index:99999;display:flex;align-items:flex-end}
.cart-panel{width:100%;max-height:82vh;overflow:auto;background:#fff;border-radius:28px 28px 0 0;padding:22px;box-sizing:border-box}
.cart-title{font-size:30px;font-weight:900;margin-bottom:14px;color:#111}
.cart-item{background:#f3f5f8;border-radius:20px;padding:16px;margin-bottom:12px}
.cart-name{font-size:20px;font-weight:900;color:#111;margin-bottom:6px}
.cart-price{font-size:16px;color:#7b8494;margin-bottom:12px}
.cart-line{display:flex;align-items:center;justify-content:space-between;gap:10px}
.cart-remove{border:0;background:#fff;color:#e53935;border-radius:14px;padding:10px 12px;font-weight:900}
.cart-empty{background:#f3f5f8;border-radius:20px;padding:18px;color:#7b8494;font-size:17px}
.cart-close,.cart-submit{width:100%;height:54px;border:0;border-radius:18px;font-size:18px;font-weight:900;margin-top:10px}
.cart-submit{background:#1688f8;color:#fff}
.cart-close{background:#eef0f3;color:#111}

.checkout-field{width:100%;box-sizing:border-box;border:1px solid #dde3ea;border-radius:16px;padding:14px 16px;font-size:17px;margin-top:10px}
.checkout-label{font-size:15px;font-weight:900;color:#6f7785;margin-top:14px}
.checkout-total{background:#111;color:#fff;border-radius:18px;padding:16px;margin:14px 0;font-size:20px;font-weight:900}
.checkout-choice{display:flex;gap:10px;margin-top:10px}
.checkout-choice button{flex:1;height:46px;border:0;border-radius:16px;background:#eef0f3;color:#111;font-size:16px;font-weight:900}
.checkout-choice button.active{background:#1688f8;color:#fff}

.cart-panel{width:100%;max-height:88vh;overflow:auto;background:#fff;border-radius:30px 30px 0 0;padding:22px 18px;box-sizing:border-box;}
.cart-title{font-size:28px;font-weight:700;margin-bottom:4px;color:#4f5663;text-align:center;}
.cart-subtitle{font-size:15px;color:#7b8494;text-align:center;margin-bottom:18px;}
.cart-item{display:grid;grid-template-columns:82px 1fr auto;gap:14px;align-items:center;background:#fff;border-radius:0;padding:14px 0;margin-bottom:0;border-bottom:1px solid #edf0f3;}
.cart-img{width:82px;height:82px;border-radius:20px;object-fit:cover;background:#f3f5f8;}
.cart-img-placeholder{width:82px;height:82px;border-radius:20px;background:#f3f5f8;display:flex;align-items:center;justify-content:center;font-size:28px;}
.cart-name{font-size:16px;font-weight:700;color:#555e6d;line-height:1.3;margin-bottom:8px;}
.cart-price{font-size:15px;color:#777;margin-bottom:10px;}
.cart-line{display:flex;align-items:center;gap:8px;justify-content:flex-start;}
.cart-line .qty-btn{width:34px;height:34px;border-radius:12px;background:#f4f4f6;color:#202124;font-size:22px;}
.cart-line .qty-num{min-width:58px;font-size:15px;font-weight:800;}
.cart-remove{width:34px;height:34px;border:0;background:#f4f4f6;color:#222;border-radius:12px;font-weight:900;font-size:17px;padding:0;}
.cart-total-line{font-size:17px;font-weight:700;color:#5f6775;white-space:nowrap;align-self:center;}
.cart-summary{background:#111;color:#fff;border-radius:22px;padding:16px;margin:18px 0 10px;font-size:20px;font-weight:900;text-align:center;}

/* soft mobile text override */
.title{
  color:#555e6d!important;
  font-weight:700!important;
  line-height:1.22!important;
}
.name{
  color:#555e6d!important;
  font-weight:700!important;
}
.price{
  color:#5f6775!important;
  font-weight:700!important;
}
.section{
  color:#4f5663!important;
  font-weight:700!important;
}
.cart-name{
  color:#555e6d!important;
  font-weight:700!important;
}
.cart-total-line{
  color:#5f6775!important;
  font-weight:700!important;
}

/* SHOP FINAL COLORS */
.title{
  color:#596170!important;
  font-weight:700!important;
}
.desc{
  color:#8a93a3!important;
}
.name{
  color:#17181c!important;
  font-weight:800!important;
}
.price{
  color:#17181c!important;
  font-weight:800!important;
}
.cart-name{
  color:#17181c!important;
  font-weight:800!important;
}
.cart-total-line{
  color:#17181c!important;
  font-weight:800!important;
}


.subcats-grid{
  display:grid;
  grid-template-columns:repeat(2, minmax(0, 1fr));
  gap:12px;
  padding:0 16px 120px;
}

.subcat{
  min-height:145px;
  border:0;
  border-radius:24px;
  background:#fff;
  box-shadow:0 12px 30px rgba(20,25,35,.07);
  padding:16px;
  text-align:left;
}

.subcat .icon{
  width:54px;
  height:54px;
  border-radius:18px;
  background:#f4f5f7;
  display:flex;
  align-items:center;
  justify-content:center;
  font-size:26px;
  margin-bottom:16px;
}

.subcat .title{
  font-size:22px;
  line-height:1.12;
  font-weight:900;
  color:#596170;
  margin-bottom:8px;
}

.subcat .desc{
  font-size:15px;
  line-height:1.25;
  color:#8a92a1;
}


/* === SHOP GRID FINAL FIX === */
.grid{
  display:grid !important;
  grid-template-columns:repeat(2, minmax(0, 1fr)) !important;
  grid-auto-rows:190px !important;

  width:100% !important;
  max-width:100% !important;
  min-width:0 !important;

  gap:14px !important;
  column-gap:14px !important;
  row-gap:14px !important;

  padding-left:18px !important;
  padding-right:18px !important;
  padding-bottom:120px !important;

  margin:0 !important;
  box-sizing:border-box !important;

  justify-content:stretch !important;
  align-items:stretch !important;
}

.grid > .cat{
  width:100% !important;
  max-width:100% !important;
  min-width:0 !important;

  height:190px !important;
  min-height:190px !important;
  max-height:190px !important;

  margin:0 !important;
  padding:18px !important;

  box-sizing:border-box !important;
  overflow:hidden !important;

  display:flex !important;
  flex-direction:column !important;
  align-items:flex-start !important;
  justify-content:flex-start !important;

  border-radius:26px !important;
}

.grid > .cat .icon{
  width:56px !important;
  height:56px !important;
  min-width:56px !important;
  min-height:56px !important;
  flex:0 0 56px !important;

  margin:0 0 14px 0 !important;

  display:flex !important;
  align-items:center !important;
  justify-content:center !important;
}

.grid > .cat .title{
  width:100% !important;
  min-width:0 !important;

  margin:0 !important;
  padding:0 !important;

  font-size:18px !important;
  line-height:1.2 !important;
  font-weight:900 !important;

  overflow:hidden !important;
  overflow-wrap:anywhere !important;
  word-break:normal !important;
}

.grid > .cat .desc{
  width:100% !important;
  min-width:0 !important;

  margin-top:8px !important;
  padding:0 !important;

  font-size:15px !important;
  line-height:1.25 !important;

  overflow:hidden !important;
}

/* === END SHOP GRID FINAL FIX === */



/* === SHOP FONT FIX === */
.grid > .cat .title{
  font-size:15px !important;
  line-height:1.12 !important;
  font-weight:900 !important;
  min-height:auto !important;
  max-height:none !important;
  overflow:visible !important;
  display:block !important;
  white-space:normal !important;
  overflow-wrap:break-word !important;
  word-break:normal !important;
  hyphens:none !important;
}

.grid > .cat .desc{
  font-size:13px !important;
  line-height:1.25 !important;
  margin-top:7px !important;
  max-height:none !important;
  overflow:visible !important;
  white-space:normal !important;
}
/* === END SHOP FONT FIX === */

</style>
</head>
<body>
<div class="app">
  <div id="home">
    <div class="hero">
      <div class="hero-top">
        <img class="hero-logo" src="/static/products/alex-kitchen-logo.png?v=2" alt="Alex Kitchen">
        <div>
          <h1>proМясо 2.0</h1>
          <p>Настоящие гастрономические приключения начинаются тут.</p>
        </div>
      </div>

      <button
        id="adminEntryButton"
        type="button"
        style="display:none;margin-top:14px;padding:10px 18px;border:0;border-radius:10px;background:#d9a441;color:#1b1b1b;font-weight:700;cursor:pointer;"
        onclick="location.href='/admin'">
        Панель администратора
      </button>
    </div>

    <div class="banner">
      <div class="banner-title">Свежайшее мясо и фермерские деликатесы</div>
      <div class="banner-text">
        Частный мясной цех Alex Kitchen / proМясо.<br>
        Свежая продукция напрямую с производства.
      </div>

      <div style="margin-top:18px;padding-top:16px;border-top:1px solid rgba(255,255,255,.12);">
    <div style="display:flex;flex-direction:column;gap:10px;font-size:16px;font-weight:400;line-height:1.4;">
      <div>🚚 Доставка по Ногинску, Электростали и Черноголовке</div>
      <div>📍 Пункты выдачи в наших заведениях</div>
      <div>⭐ Продукция напрямую от производителя</div>
    </div>
  </div>
    </div>

    <div class="grid" id="categories"></div>
  </div>

  <div id="categoryView" class="hidden">
    <div class="topbar">
      <button class="back" onclick="showHome()">← Назад</button>
      <div class="section" id="categoryTitle"></div>
    </div>
    <div class="products" id="products"></div>
  </div>
</div>


<div class="bottom">
  <button class="cart" id="cartBtn" onclick="openCart()">🛒 Корзина пока пуста</button>
</div>

<script>
const categories = [
  {id:"meat", icon:"🥩", title:"МЯСО", desc:"Свежее мясо"},
  {id:"delicacies", icon:"🥓", title:"МЯСНЫЕ ДЕЛИКАТЕСЫ", desc:"ВК, сыровяленые и деликатесы к пенному"},
  {id:"semi", icon:"🥟", title:"ПОЛУФАБРИКАТЫ", desc:"Для сковороды и гриля"},
  {id:"fish", icon:"🐟", title:"РЫБА И МОРЕПРОДУКТЫ", desc:"Рыба и морепродукты"},
  {
    id:"delivery",
    icon:"📍",
    title:"ПВЗ+ДОСТАВКА",
    desc:"Пункты выдачи и доставка",
    content:"<div class=\"name\">📍 Где мы находимся</div><div class=\"note\" style=\"font-size:16px;line-height:1.7;margin-top:14px;\"><b>🏭 Частный мясной цех proМясо</b><br><br>Московская область,<br>Богородский городской округ.<br><br>Мясная продукция оптом и в розницу.<br><br><b>🕒 Режим работы:</b><br>Четверг, Пятница, Суббота:<br>11:00 — 19:00<br><br>В остальные дни работаем как пункт выдачи заказов по предварительной договорённости.<br><br><b>🚚 Доставка:</b><br>Ногинск, Электросталь, Черноголовка.<br><br>Стоимость доставки: 300 ₽<br>Бесплатная доставка при заказе от 4000 ₽<br><br><b>📍 Адреса ПВЗ:</b><br><br>г. Электросталь, проспект Ленина, 12<br>Сеть фаст-фуд СОЮЗ Перекус<br><br>г. Ногинск, Ямкино, ул. Кооперативная, д. 30<br>Сеть фаст-фуд СОЮЗ Перекус</div><div style=\"margin-top:18px;border-radius:20px;overflow:hidden;background:#f5f5f5;height:260px;\"><iframe src=\"https://yandex.ru/map-widget/v1/?oid=85571061262&ol=biz\" width=\"100%\" height=\"260\" frameborder=\"0\" allowfullscreen=\"true\" style=\"border:0;display:block;\"></iframe></div><a href=\"https://yandex.ru/maps/org/chastny_myasnoy_tsekh/85571061262/\" target=\"_blank\" style=\"display:block;margin-top:18px;background:#e9e9eb;color:#444;text-align:center;text-decoration:none;border-radius:16px;padding:14px;font-size:15px;font-weight:700;border:1px solid #d7d7d9;\">Открыть маршрут в Яндекс Картах</a>"
  },
  {id:"social", icon:"❤️", title:"СОЦИУМ", desc:"Наши социальные проекты", items:[["Скоро добавим информацию","—","Раздел готовится"]]}
];

async function loadProductsCatalog(){
  try {
    const response = await fetch("/api/products");
    if(!response.ok) throw new Error("HTTP " + response.status);

    const products = await response.json();

    lastCatalogSignature = getCatalogSignature(products);

    const sectionMap = {
      "МЯСО": {id:"meat", icon:"🥩", desc:"Свежее мясо"},
      "МЯСНЫЕ ДЕЛИКАТЕСЫ": {id:"delicacies", icon:"🥓", desc:"ВК, сыровяленые и деликатесы к пенному"},
      "ПОЛУФАБРИКАТЫ": {id:"semi", icon:"🥟", desc:"Для сковороды и гриля"},
      "РЫБА И МОРЕПРОДУКТЫ": {id:"fish", icon:"🐟", desc:"Рыба и морепродукты"}
    };

    const dynamic = {};

    for(const product of products){

      const sectionName = product.section || "ДРУГОЕ";
      if(!dynamic[sectionName]){
        const meta = sectionMap[sectionName] || {id:"section-" + Object.keys(dynamic).length, icon:"📦", desc:"Товары"};
        dynamic[sectionName] = {
          id:meta.id,
          icon:meta.icon,
          title:sectionName,
          desc:meta.desc,
          subcategories:[]
        };
      }

      const section = dynamic[sectionName];
      const categoryName = product.category || "Товары";

      let sub = section.subcategories.find(x => x.title === categoryName);
      if(!sub){
        sub = {
          id:section.id + "-" + section.subcategories.length,
          icon:"🥩",
          title:categoryName,
          desc:categoryName,
          items:[]
        };
        section.subcategories.push(sub);
      }

      const price = Number(product.price || 0);
      const unit = product.price_unit || "шт";
      const priceText = price.toLocaleString("ru-RU") + " ₽/" + unit;

      sub.items.push([
        product.name,
        priceText,
        product.description || "",
        product.image || "",
        product.id,
        product.stock,
        product.stock_unit || product.price_unit || "шт",
        product.active
      ]);
    }

    const special = categories.filter(x => x.id === "delivery" || x.id === "social");
    categories.splice(0, categories.length, ...Object.values(dynamic), ...special);

    renderCategories();
  } catch(error) {
    console.error("Ошибка загрузки каталога:", error);
    renderCategories();
  }
}
function getCatalogSignature(products){
  return JSON.stringify(products.map(product => ({
    id: product.id,
    section: product.section,
    category: product.category,
    name: product.name,
    price: product.price,
    price_unit: product.price_unit,
    description: product.description,
    image: product.image,
    active: product.active,
    sort: product.sort
  })));
}

loadProductsCatalog();
let cart = [];

try {
  const savedCart = localStorage.getItem("alex_cart");
  if(savedCart){
    const parsedCart = JSON.parse(savedCart);
    if(Array.isArray(parsedCart)){
      cart = parsedCart;
    }
  }
} catch(e) {
  console.error("Не удалось восстановить корзину:", e);
}

let cartToken = localStorage.getItem("alex_cart_token");

if(!cartToken){
  cartToken = crypto.randomUUID();
  localStorage.setItem("alex_cart_token", cartToken);
}

let cartExpiresAt = Number(
  localStorage.getItem("alex_cart_expires_at") || 0
);

if(!Number.isFinite(cartExpiresAt)){
  cartExpiresAt = 0;
}

// Однократно очищаем старую корзину без сохранённого срока резерва
if(cart.length > 0 && cartExpiresAt <= 0){
  cartExpiresAt = Date.now();
  localStorage.setItem("alex_cart_expires_at", String(cartExpiresAt));
}

let latestProductStocks = {};
let lastCatalogSignature = "";
let stockRefreshBusy = false;

async function refreshProductStocks(){
  if(stockRefreshBusy) return;

  stockRefreshBusy = true;

  try {
    const response = await fetch("/api/products");
    if(!response.ok) throw new Error("HTTP " + response.status);

    const products = await response.json();

    const catalogSignature = getCatalogSignature(products);

    if(lastCatalogSignature && catalogSignature !== lastCatalogSignature){
      const savedQuantities = getSelectedProductQuantities();
      const categoryId = currentCategoryId;

      await loadProductsCatalog();

      if(categoryId){
        openCategory(categoryId, savedQuantities);
      }
    }

    lastCatalogSignature = catalogSignature;

    const stocks = {};

    for(const product of products){
      const availableStock = product.available_stock;

      stocks[product.id] = {
        stock: availableStock === null || availableStock === undefined || availableStock === ""
          ? null
          : Number(availableStock),
        stock_unit: product.stock_unit || product.price_unit || "шт",
        active: product.active !== false,
        image: product.image || ""
      };
    }

    latestProductStocks = stocks;

    for(const section of categories){
      for(const sub of section.subcategories || []){
        for(const item of sub.items || []){
          const current = stocks[item[4]];
          if(!current) continue;

          item[5] = current.stock;
          item[6] = current.stock_unit;
          item[7] = current.active;
        }
      }
    }

    for(const item of cart){
      const current = stocks[item.product_id];
      if(!current) continue;

      item.stock = current.stock;
      item.stock_unit = current.stock_unit;
    }

    document.querySelectorAll(".card[data-product-id]").forEach(card => {
      const current = stocks[card.dataset.productId];
      if(!current) return;

      if(current.image){
        let image = card.querySelector(".product-img");

        if(!image){
          image = document.createElement("img");
          image.className = "product-img";
          image.alt = card.querySelector(".name")?.textContent || "";
          card.insertBefore(image, card.firstChild);
        }

        if(image.getAttribute("src") !== current.image){
          image.src = current.image + "?v=" + Date.now();
        }
      }

      const button = card.querySelector(".to-cart-btn");

      if(!current.active || (current.stock !== null && current.stock <= 0)){
        button.textContent = "Нет в наличии";
        button.disabled = true;
      } else if(current.stock !== null && Number(card.dataset.selectedQty || 0) > current.stock){
        button.textContent = "В наличии: " + current.stock + " " + current.stock_unit;
        button.disabled = true;
      } else {
        button.textContent = "В корзину";
        button.disabled = false;
      }
    });

  } catch(error) {
    console.error("Ошибка обновления остатков:", error);
  } finally {
    stockRefreshBusy = false;
  }
}

refreshProductStocks();
setInterval(refreshProductStocks, 5000);

// Проверяем истечение резерва корзины каждую секунду
clearExpiredCart();
setInterval(clearExpiredCart, 1000);

function renderCategories(){
  const box = document.getElementById("categories");
  box.innerHTML = "";
  for(const c of categories){
    const btn = document.createElement("button");
    btn.className = "cat";
    btn.onclick = function(){ openCategory(c.id); };
    btn.innerHTML =
      '<div class="icon">' + (String(c.icon).startsWith('/static/') ? '<img src="' + escapeHtml(c.icon) + '">' : '<span class="icon-emoji">' + escapeHtml(c.icon) + '</span>') + '</div>' +
      '<div class="title' + (c.id === "social" ? ' social-title' : '') + '">' + escapeHtml(c.title) + '</div>' +
      '<div class="desc">' + escapeHtml(c.desc) + '</div>';
    box.appendChild(btn);
  }
}

let currentCategoryId = null;
function getSelectedProductQuantities(){
  const quantities = {};
  document.querySelectorAll(".card[data-product-id]").forEach(card => {
    quantities[card.dataset.productId] = Number(card.dataset.selectedQty || 0);
  });
  return quantities;
}
function openCategory(id, savedQuantities = null){
  currentCategoryId = id;
  setTimeout(function(){
    window.scrollTo(0, 0);
    document.documentElement.scrollTop = 0;
    document.body.scrollTop = 0;
  }, 0);
  let c = categories.find(x => x.id === id);
  let parent = null;

  if(!c){
    for(const cat of categories){
      if(cat.subcategories){
        const sub = cat.subcategories.find(x => x.id === id);
        if(sub){
          c = sub;
          parent = cat;
          break;
        }
      }
    }
  }

  if(!c) return;

  document.getElementById("home").classList.add("hidden");
  document.getElementById("categoryView").classList.remove("hidden");
  document.getElementById("categoryTitle").textContent = parent ? parent.title + " — " + c.title : c.title;

  const box = document.getElementById("products");
  box.innerHTML = "";

  if(c.subcategories){
    box.className = "subcats-grid";
    for(const sub of c.subcategories){
      const btn = document.createElement("button");
      btn.className = "subcat";
      btn.onclick = function(){ openCategory(sub.id); };
      btn.innerHTML =
        '<div class="icon">' + sub.icon + '</div>' +
        '<div class="title">' + escapeHtml(sub.title) + '</div>' +
        '<div class="desc">' + escapeHtml(sub.desc) + '</div>';
      box.appendChild(btn);
    }
    return;
  }

  if(c.content){
    box.className = "products";
    const card = document.createElement("div");
    card.className = "card";
    card.innerHTML = c.content;
    box.appendChild(card);
    return;
  }

  box.className = "products";

for(const item of c.items){
  const card = document.createElement("div");
  card.className = "card";
  card.dataset.productId = item[4] || "";

  const productId = item[4];
  const stock = item[5] === null || item[5] === undefined || item[5] === ""
    ? null
    : Number(item[5]);
  const stockUnit = item[6] || "шт";
  const active = item[7] !== false;
  const unavailable = !active || (stock !== null && stock <= 0);

  card.innerHTML =
    (item[3] ? '<img class="product-img" src="' + escapeHtml(item[3]) + '" alt="' + escapeHtml(item[0]) + '">' : '') +
    '<div class="name">' + escapeHtml(item[0]) + '</div>' +
    '<div class="note">' + escapeHtml(item[2]) + '</div>' +
    '<div class="row"><div class="price">' + escapeHtml(item[1]) + '</div></div>' +
    '<div class="qty-wrap">' +
      '<button class="qty-btn minus">−</button>' +
      '<div class="qty-num">' + formatQty(item[1], getStep(item[1])) + '</div>' +
      '<button class="qty-btn plus">+</button>' +
    '</div>' +
    '<button class="to-cart-btn">' +
      (unavailable ? "Нет в наличии" : "В корзину") +
    '</button>';

  if(productId){
    let qty = getStep(item[1]);
    if(savedQuantities && Number(savedQuantities[productId]) > 0){
      qty = Number(savedQuantities[productId]);
    } else if(stock !== null) {
      qty = Math.min(qty, stock);
    }

    if(stock !== null) qty = Math.min(qty, stock);
    card.dataset.selectedQty = String(qty);

    const qtyNode = card.querySelector(".qty-num");
    qtyNode.textContent = formatQty(item[1], qty);
    const addButton = card.querySelector(".to-cart-btn");
    const plusButton = card.querySelector(".plus");


    addButton.onclick = function(){
      const current = latestProductStocks[productId] || {
        stock: stock,
        stock_unit: stockUnit,
        active: active
      };

      addToCart(
        item[0],
        item[1],
        qty,
        item[3] || "",
        productId,
        current.stock,
        current.stock_unit,
        current.active
      );
    };

    function updateAddButton(){
      const current = latestProductStocks[productId] || {
        stock: stock,
        stock_unit: stockUnit,
        active: active
      };

      const currentStock = current.stock;
      const currentUnit = current.stock_unit;

      const unavailable = !current.active ||
        (currentStock !== null && currentStock <= 0);

      const exceeds = currentStock !== null && qty > currentStock;

      if(unavailable){
        addButton.textContent = "Нет в наличии";
        addButton.disabled = true;
      } else if(exceeds){
        addButton.textContent = "В наличии: " + currentStock + " " + currentUnit;
        addButton.disabled = true;
      } else {
        addButton.textContent = "В корзину";
        addButton.disabled = false;
      }
    }

    card.querySelector(".minus").onclick = function(){
      const step = getStep(item[1]);
      qty = Math.max(step, Math.round((qty - step) * 1000) / 1000);
      qtyNode.textContent = formatQty(item[1], qty);
      card.dataset.selectedQty = String(qty);
      updateAddButton();
    };

    plusButton.onclick = function(){
      const step = getStep(item[1]);
      qty = Math.round((qty + step) * 1000) / 1000;
      qtyNode.textContent = formatQty(item[1], qty);
      card.dataset.selectedQty = String(qty);
      updateAddButton();
    };

  updateAddButton();
  }

  box.appendChild(card);
}
}

function showHome(){
  currentCategoryId = null;
  document.getElementById("categoryView").classList.add("hidden");
  document.getElementById("home").classList.remove("hidden");
}

async function addToCart(name, price, qty, image, productId, stock, stockUnit, active){
  if(price === "—" || !active || (stock !== null && stock <= 0)) return;

  const existing = cart.find(x => x.product_id === productId);
  const currentQty = existing ? existing.qty : 0;
  const newQty = Math.round((currentQty + qty) * 1000) / 1000;

  try {
    const response = await fetch("/api/cart/reserve", {
      method: "POST",
      headers: {
        "Content-Type": "application/json"
      },
      body: JSON.stringify({
        cart_token: cartToken,
        product_id: productId,
        quantity: newQty
      })
    });

    const result = await response.json();

    if(!response.ok || !result.ok){
      if(result.available_stock !== undefined){
        alert("Доступно только " + result.available_stock + " " + stockUnit);
      } else {
        alert("Не удалось зарезервировать товар. " + (result.error || ""));
      }

      await refreshProductStocks();
      return;
    }

    if(existing){
      existing.qty = newQty;
      existing.stock = stock;
      existing.stock_unit = stockUnit;
    }else{
      cart.push({
        product_id: productId,
        name: name,
        price: price,
        qty: qty,
        image: image || "",
        stock: stock,
        stock_unit: stockUnit
      });
    }

    startCartExpiration();
    updateCart();

    const btn = document.getElementById("cartBtn");
    btn.textContent = "✅ Добавлено в корзину";
    setTimeout(updateCart, 700);

    await refreshProductStocks();

  } catch(error) {
    console.error("Ошибка резервирования:", error);
    alert("Не удалось связаться с сервером. Товар не добавлен в корзину.");
  }
}

function startCartExpiration(){
  if(cart.length === 0){
    cartExpiresAt = 0;
    localStorage.removeItem("alex_cart_expires_at");
    return;
  }

  cartExpiresAt = Date.now() + 15 * 60 * 1000;
  localStorage.setItem("alex_cart_expires_at", String(cartExpiresAt));
}

function updateCart(){
  localStorage.setItem("alex_cart", JSON.stringify(cart));
  const btn = document.getElementById("cartBtn");
  const count = cart.length;
  btn.textContent = count ? "🛒 Корзина: " + count + " поз." : "🛒 Корзина пока пуста";
}

function openCart(){
  const old = document.getElementById("cartModal");
  if(old) old.remove();

  const modal = document.createElement("div");
  modal.id = "cartModal";
  modal.className = "cart-modal";

  let html = '<div class="cart-panel">';
  html += '<div style="display:flex;align-items:center;justify-content:space-between;gap:12px;margin-bottom:16px;">';
  html += '<div class="cart-title" style="margin-bottom:0;">Корзина</div>';
  html += '<button onclick="closeCart()" style="width:38px;height:38px;border-radius:999px;border:0;background:#f0f0f0;color:#333;font-size:22px;font-weight:800;line-height:1;">×</button>';
  html += '</div>';
  html += '<div class="cart-subtitle">' + cart.length + ' товаров</div>';

  if(!cart.length){
    html += '<div class="cart-empty">Корзина пока пуста. Добавьте товары из каталога.</div>';
  }else{
    for(let i=0; i<cart.length; i++){
      const x = cart[i];
      const img = x.image
        ? '<img class="cart-img" src="' + escapeHtml(x.image) + '" alt="' + escapeHtml(x.name) + '">'
        : '<div class="cart-img-placeholder">СЂСџТђВ©</div>';

      html += '<div class="cart-item">';
      html += img;

      html += '<div>';
      html += '<div class="cart-name">' + escapeHtml(x.name) + '</div>';
      html += '<div class="cart-price">' + escapeHtml(x.price) + '</div>';
      html += '<div class="cart-line">';
      html += '<button class="cart-remove" onclick="cartRemove(' + i + ')">🗑</button>';
      html += '<button class="qty-btn" onclick="cartMinus(' + i + ')">−</button>';
      html += '<div class="qty-num">' + escapeHtml(formatQty(x.price, x.qty)) + '</div>';
      html += '<button class="qty-btn" onclick="cartPlus(' + i + ')">+</button>';
      html += '</div>';
      html += '</div>';

      html += '<div class="cart-total-line">' + escapeHtml(formatMoney(calcLineTotal(x.price, x.qty))) + '</div>';

      html += '</div>';
    }

    html += '<div class="cart-summary">Итого: ' + escapeHtml(formatMoney(calcCartTotal())) + '<br><span style="font-size:15px">Предоплата: 300 ₽</span></div>';
  }

  html += '<button class="cart-submit" onclick="sendCartOrder()">Оформить заказ</button>';

  html += '</div>';


  modal.innerHTML = html;
  document.body.appendChild(modal);

  let lastY = 0;
  let startY = 0;
  let startScrollTop = 0;

  modal.addEventListener("touchstart", function(e){
    startY = e.touches[0].clientY;
    lastY = startY;
    startScrollTop = modal.scrollTop;
  }, {passive:true});

  modal.addEventListener("touchmove", function(e){
    lastY = e.touches[0].clientY;
  }, {passive:true});

  modal.addEventListener("touchend", function(){
    const diff = lastY - startY;

    if(startScrollTop <= 0 && diff > 120){
      closeCart();
    }
  });
}


async function cartPlus(i){
  const x = cart[i];
  if(!x) return;

  const current = latestProductStocks[x.product_id];
  const stock = current ? current.stock : x.stock;
  const unit = current ? current.stock_unit : x.stock_unit;
  const active = current ? current.active : true;

  if(!active){
    alert("Товар больше недоступен");
    return;
  }

  const step = getStep(x.price);
  const newQty = Math.round((x.qty + step) * 1000) / 1000;

  try {
    const response = await fetch("/api/cart/reserve", {
      method: "POST",
      headers: {
        "Content-Type": "application/json"
      },
      body: JSON.stringify({
        cart_token: cartToken,
        product_id: x.product_id,
        quantity: newQty
      })
    });

    const result = await response.json();

    if(!response.ok || !result.ok){
      alert(result.available_stock !== undefined
        ? "Доступно только " + result.available_stock + " " + unit
        : "Не удалось изменить резерв товара");
      await refreshProductStocks();
      return;
    }

    x.qty = newQty;
    x.stock = stock;
    x.stock_unit = unit;

    startCartExpiration();
    updateCart();
    openCart();
    await refreshProductStocks();

  } catch(error) {
    console.error("Ошибка изменения резерва:", error);
    alert("Не удалось связаться с сервером");
  }
}



async function cartMinus(i){
  const x = cart[i];
  if(!x) return;

  const step = getStep(x.price);
  const newQty = Math.round((x.qty - step) * 1000) / 1000;

  try {
    const response = await fetch("/api/cart/reserve", {
      method: "POST",
      headers: {
        "Content-Type": "application/json"
      },
      body: JSON.stringify({
        cart_token: cartToken,
        product_id: x.product_id,
        quantity: Math.max(0, newQty)
      })
    });

    const result = await response.json();

    if(!response.ok || !result.ok){
      alert("Не удалось изменить резерв товара");
      return;
    }

    if(newQty <= 0){
      cart.splice(i, 1);
    } else {
      x.qty = newQty;
    }

    startCartExpiration();
    updateCart();
    openCart();
    await refreshProductStocks();

  } catch(error) {
    console.error("Ошибка изменения резерва:", error);
    alert("Не удалось связаться с сервером");
  }
}



async function cartRemove(i){
  const x = cart[i];
  if(!x) return;

  try {
    const response = await fetch("/api/cart/reserve", {
      method: "POST",
      headers: {
        "Content-Type": "application/json"
      },
      body: JSON.stringify({
        cart_token: cartToken,
        product_id: x.product_id,
        quantity: 0
      })
    });

    const result = await response.json();

    if(!response.ok || !result.ok){
      alert("Не удалось освободить резерв товара");
      return;
    }

    cart.splice(i, 1);

    startCartExpiration();
    updateCart();
    openCart();
    await refreshProductStocks();

  } catch(error) {
    console.error("Ошибка удаления товара:", error);
    alert("Не удалось связаться с сервером");
  }
}


function closeCart(){
  const modal = document.getElementById("cartModal");
  if(modal) modal.remove();
}

async function clearExpiredCart(){
  if(!cart.length || !cartExpiresAt || Date.now() < cartExpiresAt) return;

  const pendingOrderID = localStorage.getItem("alex_pending_order_id");

  if(pendingOrderID){
    try {
      const res = await fetch(
        "/api/order/payment-status?order_id=" + encodeURIComponent(pendingOrderID),
        {cache: "no-store"}
      );

      if(!res.ok) return;

      const payment = await res.json();

      // Реальный платёж существует — корзину не трогаем
      if(payment.has_payment_id) return;

      // Успешная оплата — ждём штатной обработки
      if(payment.status === "succeeded") return;

      // Отменённую оплату можно очистить после истечения резерва
      if(payment.status === "canceled"){
        localStorage.removeItem("alex_pending_order_id");
      } else if(payment.status === "creating"){
        const updatedAt = Date.parse(payment.updated_at);

        // Не трогаем свежую попытку создания платежа
        if(!Number.isFinite(updatedAt) || Date.now() - updatedAt < 5 * 60 * 1000){
          return;
        }

        // Зависшая попытка без payment_id
        localStorage.removeItem("alex_pending_order_id");
      } else {
        // Неизвестный статус — безопаснее сохранить корзину
        return;
      }
    } catch(error) {
      console.error("Не удалось проверить оплату:", error);
      return;
    }
  }

  const oldToken = cartToken;

  cart = [];
  cartExpiresAt = 0;

  localStorage.removeItem("alex_cart");
  localStorage.removeItem("alex_cart_expires_at");

  cartToken = crypto.randomUUID();
  localStorage.setItem("alex_cart_token", cartToken);

  updateCart();
  closeCart();

  try {
    const res = await fetch("/api/cart/clear", {
      method: "POST",
      headers: {"Content-Type": "application/json"},
      body: JSON.stringify({cart_token: oldToken})
    });

    if(!res.ok){
      console.error("Не удалось освободить резервы:", res.status);
    }
  } catch(error) {
    console.error("Ошибка освобождения резервов:", error);
  }

  await refreshProductStocks();

  alert("Время резервирования истекло. Корзина очищена.");
}

function getUnitForPrice(price){
  price = String(price || "");
  if(price.includes("1л") || price.includes("5л") || price.includes("/л")) return "л";
  if(price.includes("/50 г") || price.includes("/100 г")) return "г";
  if(price.includes("/шт")) return "шт";
  return "кг";
}

function parsePrice(price){
  const m = String(price || "").match(/(\d+)/);
  return m ? parseInt(m[1], 10) : 0;
}

function calcLineTotal(price, qty){
  const p = String(price || "");

  if(p.includes("1л") || p.includes("5л") || p.includes("/л")){
    const one = p.match(/1л\s*[—-]\s*(\d+)/);
    const five = p.match(/5л\s*[—-]\s*(\d+)/);

    const onePrice = one ? parseInt(one[1], 10) : parsePrice(p);
    const fivePrice = five ? parseInt(five[1], 10) : 0;

    if(fivePrice && qty >= 5){
      const packs = Math.floor(qty / 5);
      const rest = qty - packs * 5;
      return packs * fivePrice + rest * onePrice;
    }

    return Math.round(onePrice * qty);
  }

  const rub = parsePrice(price);

  if(p.includes("/кг")) return Math.round(rub * qty);
  if(p.includes("/100 г")) return Math.round(rub * (qty / 100));
  if(p.includes("/50 г")) return Math.round(rub * (qty / 50));
  if(p.includes("/шт")) return Math.round(rub * qty);

  return 0;
}

function calcCartTotal(){
  return cart.reduce(function(sum, x){
    return sum + calcLineTotal(x.price, x.qty);
  }, 0);
}

function formatMoney(n){
  return String(n).replace(/\B(?=(\d{3})+(?!\d))/g, " ") + " ₽";
}

let receiveType = "Самовывоз";

function setReceiveType(type){
  receiveType = type;
  const p = document.getElementById("pickupBtn");
  const d = document.getElementById("deliveryBtn");
  if(p && d){
    p.className = type === "Самовывоз" ? "active" : "";
    d.className = type === "Доставка" ? "active" : "";
  }
}

async function finishOrder(){
  const pendingOrderID = localStorage.getItem("alex_pending_order_id");

  if(pendingOrderID){
    payWithYooKassa(pendingOrderID);
    return;
  }

  const name = document.getElementById("clientName").value.trim();
  const phone = document.getElementById("clientPhone").value.trim();
  const comment = document.getElementById("clientComment").value.trim();

  if(!name){ alert("Введите имя"); return; }
  if(!phone){ alert("Введите телефон"); return; }

  const items = cart.map(function(x){
    return {
      product_id: x.product_id,
      name: x.name,
      price: x.price,
      qty: x.qty,
      unit: getUnitForPrice(x.price),
      line: calcLineTotal(x.price, x.qty)
    };
  });

  let maxUserID = 0;
  try{
    const wa = window.WebApp || null;
    if(wa && wa.initDataUnsafe && wa.initDataUnsafe.user && wa.initDataUnsafe.user.id){
      maxUserID = Number(wa.initDataUnsafe.user.id) || 0;
    }
  }catch(e){}

  const payload = {
    name: name,
    cart_token: cartToken,
    phone: phone,
    receive: receiveType,
    comment: comment,
    total: calcCartTotal(),
    items: items,
    user_id: maxUserID
  };

  try{
    const res = await fetch("/api/order", {
      method: "POST",
      headers: {"Content-Type":"application/json"},
      body: JSON.stringify(payload)
    });

    if(!res.ok){
      const t = await res.text();
      alert("Не удалось отправить заказ: " + t);
      return;
    }

    const data = await res.json();

    localStorage.setItem("alex_pending_order_id", data.order_id);

    updateCart();
    closeCart();

    const modal = document.createElement("div");
    modal.id = "cartModal";
    modal.className = "cart-modal";
    modal.innerHTML = '<div class="cart-panel" style="text-align:center;">'
      + '<div style="font-size:42px;margin-bottom:12px;">✅</div>'
      + '<div class="cart-title">Спасибо за заказ!</div>'
      + '<div style="font-size:16px;line-height:1.5;color:#333;margin:14px 0;">'
      + 'Заказ создан. Для передачи заказа администратору внесите предоплату.<br>В ближайшее время мы свяжемся с вами для подтверждения.'
      + '</div>'
      + '<div style="background:#f5f5f5;border-radius:18px;padding:14px;margin:14px 0;font-size:15px;line-height:1.45;">'
      + 'Для бронирования заказа необходимо внести предоплату <b>300 ₽</b>.<br><br>'
      + '<button type="button" onclick="payWithYooKassa(\'' + data.order_id + '\')" style="width:100%;height:54px;border:0;border-radius:18px;background:#ffdd2d;color:#111;font-size:17px;font-weight:900;">Оплатить 300 ₽</button>'
      + '</div>'
      + '<button class="cart-submit" onclick="location.reload()">Вернуться в магазин</button>'
      + '</div>';
    document.body.appendChild(modal);
  }catch(e){
    alert("Ошибка отправки заказа: " + e.message);
  }
}




async function payWithYooKassa(orderID){
  try{
    const res = await fetch("/api/order/pay", {
      method: "POST",
      headers: {"Content-Type":"application/json"},
      body: JSON.stringify({order_id: orderID})
    });

    const responseText = await res.text();
    let data = {};

    try {
      data = JSON.parse(responseText);
    } catch(e) {}

    if(!res.ok || !data.confirmation_url){
      throw new Error(data.error || responseText || "Не удалось создать платёж");
    }

    window.location.href = data.confirmation_url;
  }catch(e){
    alert("Не удалось перейти к оплате: " + e.message);
  }
}

async function checkReturnedPayment(){
  const params = new URLSearchParams(window.location.search);

  if(params.get("payment") !== "return"){
    return;
  }

  const orderID = localStorage.getItem("alex_pending_order_id");

  if(!orderID){
    return;
  }

  const deadline = Date.now() + 60000;

  while(Date.now() < deadline){
    try{
      const res = await fetch(
        "/api/order/payment-status?order_id=" + encodeURIComponent(orderID),
        {cache: "no-store"}
      );

      if(res.ok){
        const data = await res.json();

        if(data.status === "succeeded"){
          cart = [];
          localStorage.removeItem("alex_pending_order_id");

          cartToken = crypto.randomUUID();
          localStorage.setItem("alex_cart_token", cartToken);

          updateCart();

          alert("Предоплата 300 ₽ получена. Корзина очищена.");

          const url = new URL(window.location.href);
          url.searchParams.delete("payment");
          history.replaceState({}, "", url.pathname + url.search + url.hash);

          return;
        }

        if(data.status === "canceled"){
          alert("Платёж отменён. Корзина и резерв товаров сохранены. Можно повторить оплату.");
          return;
        }
      }
    }catch(e){
      console.error("Ошибка проверки оплаты:", e);
    }

    await new Promise(resolve => setTimeout(resolve, 2500));
  }

  alert("Подтверждение оплаты пока не получено. Корзина и резерв сохранены. Попробуйте проверить позже.");
}

window.addEventListener("load", checkReturnedPayment);

function sendCartOrder(){
  if(!cart.length){
    alert("Корзина пока пуста");
    return;
  }

  closeCart();

  const modal = document.createElement("div");
  modal.id = "cartModal";
  modal.className = "cart-modal";

  let html = '<div class="cart-panel">';
  html += '<div class="cart-title">Оформление заказа</div>';
  html += '<div class="checkout-total">Итого: ' + formatMoney(calcCartTotal()) + '<br><span style="font-size:15px">Предоплата: 300 ₽</span></div>';

  html += '<div class="checkout-label">Ваше имя</div>';
  html += '<input id="clientName" class="checkout-field" placeholder="Например, Алексей">';

  html += '<div class="checkout-label">Телефон</div>';
  html += '<input id="clientPhone" class="checkout-field" placeholder="+7..." inputmode="tel">';

  html += '<div class="checkout-label">Получение</div>';
  html += '<div class="checkout-choice">';
  html += '<button id="pickupBtn" class="active" onclick="setReceiveType(\'Самовывоз\')">Самовывоз</button>';
  html += '<button id="deliveryBtn" onclick="setReceiveType(\'Доставка\')">Доставка</button>';
  html += '</div>';

  html += '<div class="checkout-label">Комментарий</div>';
  html += '<textarea id="clientComment" class="checkout-field" rows="3" placeholder="Например: заберу вечером, нужен звонок"></textarea>';

  html += '<button class="cart-submit" id="finishOrderBtn" type="button">Подтвердить заказ</button>';
  html += '<button class="cart-close" onclick="openCart()">Назад в корзину</button>';
  html += '</div>';

  modal.innerHTML = html;
  document.body.appendChild(modal);

  const finishBtn = document.getElementById("finishOrderBtn");
  if(finishBtn){
    finishBtn.onclick = function(e){
      if(e){
        e.preventDefault();
        e.stopPropagation();
      }
      alert("Кнопка подтверждения нажалась");
      finishOrder();
      return false;
    };
  } else {
    alert("Кнопка подтверждения не найдена");
  }
}

function escapeHtml(s){
  return String(s || "").replace(/[&<>"']/g,function(m){
    return {"&":"&amp;","<":"&lt;",">":"&gt;",'"':"&quot;","'":"&#039;"}[m];
  });
}

try{
  if(window.WebApp){
    window.WebApp.ready();
    window.WebApp.expand();
  }
}catch(e){}

function getStep(price){
  price = String(price || "");

  if(price.includes("1л") || price.includes("5л") || price.includes("/л")) return 1;
  if(price.includes("/50 г")) return 50;
  if(price.includes("/100 г")) return 100;
  if(price.includes("/шт")) return 1;

  return 0.5;
}

function formatQty(price, qty){
  price = String(price || "");

  if(price.includes("1л") || price.includes("5л") || price.includes("/л")){
    return qty + " л";
  }

  if(price.includes("/50 г") || price.includes("/100 г")){
    return qty + " г";
  }

  if(price.includes("/шт")){
    return qty + " шт";
  }

  return qty.toFixed(1).replace(".0","") + " кг";
}

async function checkAdminEntry() {
  try {
    const wa = window.WebApp || null;
    const user = wa && wa.initDataUnsafe && wa.initDataUnsafe.user;
    const userID = user && user.id ? String(user.id) : "";

    if (!userID) return;

    const response = await fetch(
      "/api/admin/max-visibility?user_id=" + encodeURIComponent(userID)
    );

    if (!response.ok) return;

    const result = await response.json();

    if (result.show) {
      const button = document.getElementById("adminEntryButton");
      if (button) button.style.display = "inline-block";
    }
  } catch (error) {
    console.error("Admin visibility check failed:", error);
  }
}

checkAdminEntry();

renderCategories();
updateCart();
</script>
</body>
</html>`

const appHTML = `<!doctype html>
<html lang="ru">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1, viewport-fit=cover">
<title>Alex Kitchen</title>
<script src="https://st.max.ru/js/max-web-app.js"></script>
<style>
body{margin:0;background:#f4f6fa;font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",Arial,sans-serif}
.wrap{padding:24px}
.product-img{width:100%;height:160px;object-fit:cover;border-radius:18px;margin-bottom:10px;display:block;}
.card{background:#fff;border-radius:22px;padding:22px;box-shadow:0 6px 18px rgba(0,0,0,.08)}
h1{margin:0 0 8px;font-size:24px}
p{color:#667085;font-size:16px}

.hero{
  position:relative!important;
  padding-left:132px!important;
  box-sizing:border-box!important;
}
.hero-logo{
  position:absolute!important;
  left:18px!important;
  top:50%!important;
  transform:translateY(-50%)!important;
  width:88px!important;
  height:88px!important;
  border-radius:50%!important;
  z-index:5!important;
}
.hero h1,
.hero p{
  margin-left:0!important;
  padding-left:0!important;
  max-width:100%!important;
}

.qty-wrap{display:flex;align-items:center;gap:10px;margin-top:14px;}
.qty-btn{width:38px;height:38px;border:none;border-radius:12px;background:#1688f8;color:#fff;font-size:24px;font-weight:900;}
.qty-num{min-width:70px;text-align:center;font-size:17px;font-weight:800;color:#111;}

.to-cart-btn{margin-top:12px;width:100%;height:46px;border:0;border-radius:16px;background:#111;color:#fff;font-size:17px;font-weight:900;}

.cart-modal{position:fixed;inset:0;background:rgba(0,0,0,.45);z-index:99999;display:flex;align-items:flex-end;}
.cart-panel{width:100%;max-height:82vh;overflow:auto;background:#fff;border-radius:28px 28px 0 0;padding:22px;box-sizing:border-box;}
.cart-title{font-size:30px;font-weight:900;margin-bottom:14px;color:#111;}
.cart-item{background:#f3f5f8;border-radius:20px;padding:16px;margin-bottom:12px;}
.cart-name{font-size:20px;font-weight:900;color:#111;margin-bottom:6px;}
.cart-price{font-size:16px;color:#7b8494;margin-bottom:12px;}
.cart-line{display:flex;align-items:center;justify-content:space-between;gap:10px;}
.cart-remove{border:0;background:#fff;color:#e53935;border-radius:14px;padding:10px 12px;font-weight:900;}
.cart-empty{background:#f3f5f8;border-radius:20px;padding:18px;color:#7b8494;font-size:17px;}
.cart-close,.cart-submit{width:100%;height:54px;border:0;border-radius:18px;font-size:18px;font-weight:900;margin-top:10px;}
.cart-submit{background:#1688f8;color:#fff;}
.cart-close{background:#eef0f3;color:#111;}

.checkout-field{width:100%;box-sizing:border-box;border:1px solid #dde3ea;border-radius:16px;padding:14px 16px;font-size:17px;margin-top:10px;}
.checkout-label{font-size:15px;font-weight:900;color:#6f7785;margin-top:14px;}
.checkout-total{background:#111;color:#fff;border-radius:18px;padding:16px;margin:14px 0;font-size:20px;font-weight:900;}
.checkout-choice{display:flex;gap:10px;margin-top:10px;}
.checkout-choice button{flex:1;height:46px;border:0;border-radius:16px;background:#eef0f3;color:#111;font-size:16px;font-weight:900;}
.checkout-choice button.active{background:#1688f8;color:#fff;}

.cart-panel{width:100%;max-height:88vh;overflow:auto;background:#fff;border-radius:30px 30px 0 0;padding:22px 18px;box-sizing:border-box;}
.cart-title{font-size:28px;font-weight:700;margin-bottom:4px;color:#4f5663;text-align:center;}
.cart-subtitle{font-size:15px;color:#7b8494;text-align:center;margin-bottom:18px;}
.cart-item{display:grid;grid-template-columns:82px 1fr auto;gap:14px;align-items:center;background:#fff;border-radius:0;padding:14px 0;margin-bottom:0;border-bottom:1px solid #edf0f3;}
.cart-img{width:82px;height:82px;border-radius:20px;object-fit:cover;background:#f3f5f8;}
.cart-img-placeholder{width:82px;height:82px;border-radius:20px;background:#f3f5f8;display:flex;align-items:center;justify-content:center;font-size:28px;}
.cart-name{font-size:16px;font-weight:700;color:#555e6d;line-height:1.3;margin-bottom:8px;}
.cart-price{font-size:15px;color:#777;margin-bottom:10px;}
.cart-line{display:flex;align-items:center;gap:8px;justify-content:flex-start;}
.cart-line .qty-btn{width:34px;height:34px;border-radius:12px;background:#f4f4f6;color:#202124;font-size:22px;}
.cart-line .qty-num{min-width:58px;font-size:15px;font-weight:800;}
.cart-remove{width:34px;height:34px;border:0;background:#f4f4f6;color:#222;border-radius:12px;font-weight:900;font-size:17px;padding:0;}
.cart-total-line{font-size:17px;font-weight:700;color:#5f6775;white-space:nowrap;align-self:center;}
.cart-summary{background:#111;color:#fff;border-radius:22px;padding:16px;margin:18px 0 10px;font-size:20px;font-weight:900;text-align:center;}

/* soft mobile text override */
.title{
  color:#555e6d!important;
  font-weight:700!important;
  line-height:1.22!important;
}
.name{
  color:#555e6d!important;
  font-weight:700!important;
}
.price{
  color:#5f6775!important;
  font-weight:700!important;
}
.section{
  color:#4f5663!important;
  font-weight:700!important;
}
.cart-name{
  color:#555e6d!important;
  font-weight:700!important;
}
.cart-total-line{
  color:#5f6775!important;
  font-weight:700!important;
}


.subcats-grid{
  display:grid;
  grid-template-columns:repeat(2, minmax(0, 1fr));
  gap:12px;
  padding:0 16px 120px;
}

.subcat{
  min-height:145px;
  border:0;
  border-radius:24px;
  background:#fff;
  box-shadow:0 12px 30px rgba(20,25,35,.07);
  padding:16px;
  text-align:left;
}

.subcat .icon{
  width:54px;
  height:54px;
  border-radius:18px;
  background:#f4f5f7;
  display:flex;
  align-items:center;
  justify-content:center;
  font-size:26px;
  margin-bottom:16px;
}

.subcat .title{
  font-size:22px;
  line-height:1.12;
  font-weight:900;
  color:#596170;
  margin-bottom:8px;
}

.subcat .desc{
  font-size:15px;
  line-height:1.25;
  color:#8a92a1;
}

</style>
</head>
<body>
<div class="wrap">
  <div class="card">
    <h1>Alex Kitchen</h1>
    <p>Открываем мини-приложение...</p>
  </div>
</div>

<script>
function go(){
  try{
    const wa = window.WebApp || null;
    if(wa && wa.ready) wa.ready();
    if(wa && wa.expand) wa.expand();

    let p = "";
    if(wa && wa.initDataUnsafe && wa.initDataUnsafe.start_param){
      p = String(wa.initDataUnsafe.start_param);
    }

    if(!p || p === "shop" || p === "shop_v2" || p === "open_shop"){
      location.replace("/shop");
      return;
    }

    p = p.replace(/^comments_/, "");
    location.replace("/comments?post=" + encodeURIComponent(p));
  }catch(e){
    location.replace("/shop");
  }
}
go();
</script>
</body>
</html>`

type ShopOrderItem struct {
	ProductID string  `json:"product_id"`
	Name      string  `json:"name"`
	Price     string  `json:"price"`
	Qty       float64 `json:"qty"`
	Unit      string  `json:"unit"`
	Line      int     `json:"line"`
}

type YooKassaCreateRequest struct {
	Amount struct {
		Value    string `json:"value"`
		Currency string `json:"currency"`
	} `json:"amount"`

	Capture      bool `json:"capture"`
	Confirmation struct {
		Type      string `json:"type"`
		ReturnURL string `json:"return_url"`
	} `json:"confirmation"`

	Description string            `json:"description"`
	Metadata    map[string]string `json:"metadata"`
}

type YooKassaCreateResponse struct {
	ID     string `json:"id"`
	Status string `json:"status"`

	Confirmation struct {
		ConfirmationURL string `json:"confirmation_url"`
	} `json:"confirmation"`
}

type YooKassaErrorResponse struct {
	Type        string `json:"type"`
	Description string `json:"description"`
	Parameter   string `json:"parameter"`
	Code        string `json:"code"`
}

type YooKassaWebhookRequest struct {
	Event  string `json:"event"`
	Object struct {
		ID string `json:"id"`
	} `json:"object"`
}

type YooKassaPaymentDetails struct {
	ID     string `json:"id"`
	Status string `json:"status"`

	Amount struct {
		Value    string `json:"value"`
		Currency string `json:"currency"`
	} `json:"amount"`

	Metadata map[string]string `json:"metadata"`
}

type ShopOrderRequest struct {
	Name      string          `json:"name"`
	CartToken string          `json:"cart_token"`
	Phone     string          `json:"phone"`
	Receive   string          `json:"receive"`
	Comment   string          `json:"comment"`
	Total     int             `json:"total"`
	Items     []ShopOrderItem `json:"items"`
	UserID    int64           `json:"user_id"`
}

type PendingOrder struct {
	ClientText string          `json:"-"`
	OrderID    string          `json:"order_id"`
	CreatedAt  string          `json:"created_at"`
	Status     string          `json:"status"`
	Prepayment int             `json:"prepayment"`
	UserID     int64           `json:"user_id"`
	Name       string          `json:"name"`
	Phone      string          `json:"phone"`
	Receive    string          `json:"receive"`
	Comment    string          `json:"comment,omitempty"`
	Total      int             `json:"total"`
	Items      []ShopOrderItem `json:"items"`
	AdminText  string          `json:"admin_text"`
}

var pendingOrders sync.Map

func savePendingOrder(order PendingOrder) error {
	f, err := os.OpenFile(
		dataFile("pending-orders.jsonl"),
		os.O_CREATE|os.O_WRONLY|os.O_APPEND,
		0644,
	)
	if err != nil {
		return err
	}
	defer f.Close()

	return json.NewEncoder(f).Encode(order)
}

func productsHandler(w http.ResponseWriter, r *http.Request) {
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
          'available_stock',
              CASE
                  WHEN stock IS NULL THEN NULL
                  ELSE GREATEST(
                      stock - COALESCE((
                          SELECT SUM(cr.quantity)
                          FROM cart_reservations cr
                          WHERE cr.product_id = products.id
                            AND (
                                cr.status = 'confirmed'
                                OR (cr.status = 'active' AND cr.expires_at > now())
                            )
                      ), 0),
                      0
                  )
              END,
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
		log.Printf("products query failed: %v", err)
		http.Error(w, "products unavailable", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(data)
}

type CartReserveRequest struct {
	CartToken string  `json:"cart_token"`
	ProductID string  `json:"product_id"`
	Quantity  float64 `json:"quantity"`
}

func cartReserveHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req CartReserveRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}

	req.CartToken = strings.TrimSpace(req.CartToken)
	req.ProductID = strings.TrimSpace(req.ProductID)

	if req.CartToken == "" || req.ProductID == "" || req.Quantity < 0 {
		http.Error(w, "invalid cart or quantity", http.StatusBadRequest)
		return
	}

	ctx := r.Context()
	tx, err := dbPool.Begin(ctx)
	if err != nil {
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback(ctx)
	if req.Quantity == 0 {
		_, err := tx.Exec(ctx, `
        UPDATE cart_reservations
        SET status = 'released',
            updated_at = now()
        WHERE cart_token = $1
          AND product_id = $2
          AND status = 'active'
    `, req.CartToken, req.ProductID)

		if err != nil {
			http.Error(w, "database error", http.StatusInternalServerError)
			return
		}

		_, err = tx.Exec(ctx, `
			UPDATE cart_reservations
			SET expires_at = now() + interval '15 minutes',
			    updated_at = now()
			WHERE cart_token = $1
			  AND status = 'active'
			  AND expires_at > now()
		`, req.CartToken)

		if err != nil {
			http.Error(w, "database error", http.StatusInternalServerError)
			return
		}

		if err := tx.Commit(ctx); err != nil {
			http.Error(w, "database error", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"ok": true,
		})
		return
	}

	var stock *float64
	var active bool

	err = tx.QueryRow(ctx, `
		SELECT stock, active
		FROM products
		WHERE id = $1
		FOR UPDATE
	`, req.ProductID).Scan(&stock, &active)

	if err != nil {
		http.Error(w, "product not found", http.StatusNotFound)
		return
	}

	if !active {
		http.Error(w, "product unavailable", http.StatusConflict)
		return
	}

	var reserved float64
	err = tx.QueryRow(ctx, `
		SELECT COALESCE(SUM(quantity), 0)
		FROM cart_reservations
		WHERE product_id = $1
		  AND (
          status = 'confirmed'
          OR (status = 'active' AND expires_at > now())
      )
		  AND cart_token <> $2
	`, req.ProductID, req.CartToken).Scan(&reserved)

	if err != nil {
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}

	if stock != nil && reserved+req.Quantity > *stock {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"ok":              false,
			"available_stock": *stock - reserved,
			"error":           "not enough stock",
		})
		return
	}

	_, err = tx.Exec(ctx, `
		UPDATE cart_reservations
		SET quantity = $1,
		    updated_at = now(),
		    expires_at = now() + interval '15 minutes'
		WHERE cart_token = $2
		  AND product_id = $3
		  AND status = 'active'
	`, req.Quantity, req.CartToken, req.ProductID)

	if err != nil {
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}

	var exists bool
	err = tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM cart_reservations
			WHERE cart_token = $1
			  AND product_id = $2
			  AND status = 'active'
		)
	`, req.CartToken, req.ProductID).Scan(&exists)

	if err != nil {
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}

	if !exists {
		_, err = tx.Exec(ctx, `
			INSERT INTO cart_reservations
				(cart_token, product_id, quantity, expires_at)
			VALUES ($1, $2, $3, now() + interval '15 minutes')
		`, req.CartToken, req.ProductID, req.Quantity)

		if err != nil {
			http.Error(w, "reservation failed", http.StatusInternalServerError)
			return
		}
	}

	_, err = tx.Exec(ctx, `
		UPDATE cart_reservations
		SET expires_at = now() + interval '15 minutes',
		    updated_at = now()
		WHERE cart_token = $1
		  AND status = 'active'
		  AND expires_at > now()
	`, req.CartToken)

	if err != nil {
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}

	if err := tx.Commit(ctx); err != nil {
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"ok": true,
	})
}

func cartClearHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		CartToken string `json:"cart_token"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}

	req.CartToken = strings.TrimSpace(req.CartToken)
	if req.CartToken == "" {
		http.Error(w, "cart token is required", http.StatusBadRequest)
		return
	}

	_, err := dbPool.Exec(r.Context(), `
		UPDATE cart_reservations
		SET status = 'released',
		    updated_at = now()
		WHERE cart_token = $1
		  AND status = 'active'
	`, req.CartToken)

	if err != nil {
		log.Printf("cart clear failed: %v", err)
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"ok": true,
	})
}

func bindCartReservationsToOrder(cartToken, orderID string, items []ShopOrderItem) error {
	ctx := context.Background()

	cartToken = strings.TrimSpace(cartToken)
	orderID = strings.TrimSpace(orderID)

	if cartToken == "" || orderID == "" {
		return fmt.Errorf("не указан токен корзины или номер заказа")
	}

	quantities := make(map[string]float64)

	for _, item := range items {
		id := strings.TrimSpace(item.ProductID)
		if id == "" || item.Qty <= 0 {
			return fmt.Errorf("некорректный товар или количество в заказе")
		}
		quantities[id] += item.Qty
	}

	if len(quantities) == 0 {
		return fmt.Errorf("заказ пуст")
	}

	productIDs := make([]string, 0, len(quantities))
	for id := range quantities {
		productIDs = append(productIDs, id)
	}

	tx, err := dbPool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("не удалось начать транзакцию: %w", err)
	}
	defer tx.Rollback(ctx)

	// Блокируем товары в одинаковом порядке, чтобы избежать гонок с резервированием.
	productRows, err := tx.Query(ctx, `
		SELECT id, active
		FROM products
		WHERE id = ANY($1)
		ORDER BY id
		FOR UPDATE
	`, productIDs)
	if err != nil {
		return fmt.Errorf("не удалось заблокировать товары: %w", err)
	}

	lockedProducts := make(map[string]bool)

	for productRows.Next() {
		var id string
		var active bool

		if err := productRows.Scan(&id, &active); err != nil {
			productRows.Close()
			return fmt.Errorf("ошибка проверки товара: %w", err)
		}

		lockedProducts[id] = active
	}

	if err := productRows.Err(); err != nil {
		productRows.Close()
		return fmt.Errorf("ошибка чтения товаров: %w", err)
	}
	productRows.Close()

	for id := range quantities {
		active, exists := lockedProducts[id]
		if !exists {
			return fmt.Errorf("товар %s не найден", id)
		}
		if !active {
			return fmt.Errorf("товар %s больше недоступен", id)
		}
	}

	// Проверяем все активные резервы этой корзины.
	reservationRows, err := tx.Query(ctx, `
		SELECT product_id, quantity::double precision, expires_at
		FROM cart_reservations
		WHERE cart_token = $1
		  AND status = 'active'
		ORDER BY product_id
		FOR UPDATE
	`, cartToken)
	if err != nil {
		return fmt.Errorf("не удалось проверить резервы: %w", err)
	}

	reserved := make(map[string]float64)

	for reservationRows.Next() {
		var productID string
		var quantity float64
		var expiresAt time.Time

		if err := reservationRows.Scan(&productID, &quantity, &expiresAt); err != nil {
			reservationRows.Close()
			return fmt.Errorf("ошибка чтения резерва: %w", err)
		}

		if !expiresAt.After(time.Now()) {
			reservationRows.Close()
			return fmt.Errorf("резерв товара %s истёк. Обновите корзину", productID)
		}

		reserved[productID] += quantity
	}

	if err := reservationRows.Err(); err != nil {
		reservationRows.Close()
		return fmt.Errorf("ошибка чтения резервов: %w", err)
	}
	reservationRows.Close()

	if len(reserved) != len(quantities) {
		return fmt.Errorf("состав корзины не совпадает с резервами. Обновите страницу")
	}

	for id, quantity := range quantities {
		reservedQuantity, exists := reserved[id]
		if !exists || reservedQuantity < quantity-0.0005 || reservedQuantity > quantity+0.0005 {
			return fmt.Errorf("резерв товара %s не совпадает с количеством в заказе", id)
		}
	}

	// Привязываем резервы к заказу и продлеваем их на 30 минут.
	_, err = tx.Exec(ctx, `
		UPDATE cart_reservations
		SET order_id = $2,
		    expires_at = now() + interval '15 minutes',
		    updated_at = now()
		WHERE cart_token = $1
		  AND status = 'active'
	`, cartToken, orderID)
	if err != nil {
		return fmt.Errorf("не удалось привязать резервы к заказу: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("не удалось сохранить резервы: %w", err)
	}

	return nil
}

func validateOrderStock(items []ShopOrderItem) string {
	quantities := make(map[string]float64)

	for _, item := range items {
		id := strings.TrimSpace(item.ProductID)

		if id == "" {
			return "Не удалось определить товар. Обновите страницу и попробуйте снова."
		}

		if item.Qty <= 0 {
			return "Некорректное количество товара."
		}

		quantities[id] += item.Qty
	}

	for id, qty := range quantities {
		var active bool
		var stock sql.NullFloat64

		err := dbPool.QueryRow(
			context.Background(),
			`SELECT active, stock::double precision FROM products WHERE id = $1`,
			id,
		).Scan(&active, &stock)

		if err != nil {
			return "Не удалось проверить наличие товара. Попробуйте позже."
		}

		if !active {
			return "Один из товаров больше недоступен."
		}

		if stock.Valid && qty > stock.Float64 {
			return fmt.Sprintf(
				"Недостаточно товара на складе. Доступно: %s",
				strconv.FormatFloat(stock.Float64, 'f', -1, 64),
			)
		}
	}

	return ""
}

func orderHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req ShopOrderRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}

	req.Name = strings.TrimSpace(req.Name)
	req.Phone = strings.TrimSpace(req.Phone)
	req.Receive = strings.TrimSpace(req.Receive)
	req.Comment = strings.TrimSpace(req.Comment)

	if req.Name == "" || req.Phone == "" || len(req.Items) == 0 {
		http.Error(w, "name, phone and items are required", http.StatusBadRequest)
		return
	}

	if strings.TrimSpace(req.CartToken) == "" {
		http.Error(w, "cart_token is required", http.StatusBadRequest)
		return
	}

	orderID := time.Now().Format("20060102-150405.000000000")

	if err := bindCartReservationsToOrder(req.CartToken, orderID, req.Items); err != nil {
		log.Printf("bind cart reservations failed order_id=%s: %v", orderID, err)
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}

	var b strings.Builder
	b.WriteString("🛒 Новый заказ из mini-app\n")
	b.WriteString("№ " + orderID + "\n\n")
	b.WriteString("👤 Имя: " + req.Name + "\n")
	b.WriteString("📞 Телефон: " + req.Phone + "\n")
	b.WriteString("🚚 Получение: " + req.Receive + "\n\n")
	b.WriteString("🥩 Состав заказа:\n")

	for i, it := range req.Items {
		b.WriteString(strconv.Itoa(i+1) + ". " + it.Name + " — " + formatQtyServer(it.Qty, it.Unit) + " — " + it.Price)
		if it.Line > 0 {
			b.WriteString(" = " + formatRubServer(it.Line))
		}
		b.WriteString("\n")
	}

	b.WriteString("\n💰 Итого: " + formatRubServer(req.Total) + "\n")
	b.WriteString("💳 Предоплата: 300 ₽\n")

	if req.Comment != "" {
		b.WriteString("\n💬 Комментарий: " + req.Comment + "\n")
	}

	// До подтверждения оплаты заказ никому в MAX не отправляем.
	// Сохраняем заказ со статусом ожидания предоплаты.
	pendingOrder := PendingOrder{
		OrderID:    orderID,
		CreatedAt:  time.Now().Format(time.RFC3339),
		Status:     "awaiting_payment",
		Prepayment: 300,
		UserID:     req.UserID,
		Name:       req.Name,
		Phone:      req.Phone,
		Receive:    req.Receive,
		Comment:    req.Comment,
		Total:      req.Total,
		Items:      req.Items,
		AdminText:  b.String(),
	}

	if err := savePendingOrder(pendingOrder); err != nil {
		log.Printf("save pending order failed order_id=%s: %v", orderID, err)

		_, releaseErr := dbPool.Exec(
			context.Background(),
			`
      UPDATE cart_reservations
      SET status = 'released',
          updated_at = now()
      WHERE order_id = $1
        AND status = 'active'
      `,
			orderID,
		)

		if releaseErr != nil {
			log.Printf("release reservations failed order_id=%s: %v", orderID, releaseErr)
		}

		http.Error(w, "cannot save order", http.StatusInternalServerError)
		return
	}

	log.Printf(
		"order awaiting payment order_id=%s user_id=%d total=%d",
		orderID,
		req.UserID,
		req.Total,
	)

	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"ok":true,"order_id":"` + orderID + `"}`))
}

type OrderConfirmRequest struct {
	OrderID string `json:"order_id"`
}

func loadPendingOrder(orderID string) (PendingOrder, error) {
	raw, err := os.ReadFile(dataFile("pending-orders.jsonl"))
	if err != nil {
		return PendingOrder{}, err
	}

	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")

	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}

		var order PendingOrder
		if err := json.Unmarshal([]byte(line), &order); err != nil {
			continue
		}

		if order.OrderID == orderID {
			return order, nil
		}
	}

	return PendingOrder{}, fmt.Errorf("order not found")
}

func isOrderConfirmed(orderID string) bool {
	raw, err := os.ReadFile(dataFile("confirmed-orders.txt"))
	if err != nil {
		return false
	}

	for _, line := range strings.Split(string(raw), "\n") {
		parts := strings.Fields(strings.TrimSpace(line))
		if len(parts) > 0 && parts[0] == orderID {
			return true
		}
	}

	return false
}

func orderPaymentStatusHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	orderID := strings.TrimSpace(r.URL.Query().Get("order_id"))
	if orderID == "" {
		http.Error(w, "order_id is required", http.StatusBadRequest)
		return
	}

	var status string
	var hasPaymentID bool
	var updatedAt time.Time

	err := dbPool.QueryRow(
		r.Context(),
		`SELECT status,
                COALESCE(payment_id, '') <> '',
                updated_at
         FROM yookassa_payments
         WHERE order_id = $1`,
		orderID,
	).Scan(&status, &hasPaymentID, &updatedAt)

	if err != nil {
		http.Error(w, "payment not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"ok":             true,
		"status":         status,
		"has_payment_id": hasPaymentID,
		"updated_at":     updatedAt,
	})
}

func markOrderConfirmed(orderID string) error {
	f, err := os.OpenFile(
		dataFile("confirmed-orders.txt"),
		os.O_CREATE|os.O_WRONLY|os.O_APPEND,
		0644,
	)
	if err != nil {
		return err
	}
	defer f.Close()

	_, err = fmt.Fprintf(
		f,
		"%s %s\n",
		orderID,
		time.Now().Format(time.RFC3339),
	)

	return err
}

func yooKassaPaymentHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		OrderID string `json:"order_id"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}

	req.OrderID = strings.TrimSpace(req.OrderID)
	if req.OrderID == "" {
		http.Error(w, "order_id is required", http.StatusBadRequest)
		return
	}

	_, err := loadPendingOrder(req.OrderID)
	if err != nil {
		http.Error(w, "order not found", http.StatusNotFound)
		return
	}

	if isOrderConfirmed(req.OrderID) {
		http.Error(w, "order already confirmed", http.StatusConflict)
		return
	}

	shopID := strings.TrimSpace(os.Getenv("YOOKASSA_SHOP_ID"))
	secretKey := strings.TrimSpace(os.Getenv("YOOKASSA_SECRET_KEY"))
	baseURL := strings.TrimRight(strings.TrimSpace(os.Getenv("PUBLIC_BASE_URL")), "/")

	if shopID == "" || secretKey == "" || baseURL == "" {
		log.Println("YooKassa configuration is incomplete")
		http.Error(w, "payment service is not configured", http.StatusInternalServerError)
		return
	}

	// Получаем или создаём запись платежа для этого заказа.
	newKey, keyErr := generateIdempotenceKey()
	if keyErr != nil {
		http.Error(w, "payment initialization failed", http.StatusInternalServerError)
		return
	}

	_, err = dbPool.Exec(
		r.Context(),
		`INSERT INTO yookassa_payments
      (order_id, idempotence_key, amount, status)
    VALUES ($1, $2, 1.00, 'creating')
    ON CONFLICT (order_id) DO NOTHING`,
		req.OrderID,
		newKey,
	)
	if err != nil {
		log.Printf("YooKassa payment record creation failed order_id=%s: %v", req.OrderID, err)
		http.Error(w, "payment initialization failed", http.StatusInternalServerError)
		return
	}

	var idempotenceKey string
	var paymentID string
	var confirmationURL string
	var paymentStatus string

	err = dbPool.QueryRow(
		r.Context(),
		`SELECT idempotence_key,
		        COALESCE(payment_id, ''),
		        COALESCE(confirmation_url, ''),
		        status
		 FROM yookassa_payments
		 WHERE order_id = $1`,
		req.OrderID,
	).Scan(&idempotenceKey, &paymentID, &confirmationURL, &paymentStatus)

	if err != nil {
		log.Printf("YooKassa payment record lookup failed order_id=%s: %v", req.OrderID, err)
		http.Error(w, "payment record not found", http.StatusInternalServerError)
		return
	}

	if confirmationURL != "" && paymentStatus != "canceled" {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"ok":               true,
			"confirmation_url": confirmationURL,
		})
		return
	}

	// Если предыдущий платёж отменён — создаём новый ключ
	// и очищаем данные старого платежа.
	if paymentStatus == "canceled" {
		idempotenceKey, err = generateIdempotenceKey()
		if err != nil {
			log.Printf("YooKassa idempotence key generation failed: %v", err)
			http.Error(w, "payment initialization failed", http.StatusInternalServerError)
			return
		}

		_, err = dbPool.Exec(
			r.Context(),
			`UPDATE yookassa_payments
                  SET idempotence_key = $2,
                      payment_id = NULL,
                      confirmation_url = NULL,
                      status = 'creating',
                      updated_at = now()
                  WHERE order_id = $1
                    AND status = 'canceled'`,
			req.OrderID,
			idempotenceKey,
		)
		if err != nil {
			log.Printf("YooKassa canceled payment reset failed order_id=%s: %v", req.OrderID, err)
			http.Error(w, "payment initialization failed", http.StatusInternalServerError)
			return
		}
	}

	// Если ключ ещё не создан — генерируем его.
	if idempotenceKey == "" {
		idempotenceKey, err = generateIdempotenceKey()
		if err != nil {
			log.Printf("YooKassa idempotence key generation failed: %v", err)
			http.Error(w, "payment initialization failed", http.StatusInternalServerError)
			return
		}

		_, err = dbPool.Exec(
			r.Context(),
			`UPDATE yookassa_payments
			 SET idempotence_key = $2,
			     updated_at = now()
			 WHERE order_id = $1
			   AND idempotence_key = ''`,
			req.OrderID,
			idempotenceKey,
		)
		if err != nil {
			log.Printf("YooKassa idempotence key save failed order_id=%s: %v", req.OrderID, err)
			http.Error(w, "payment initialization failed", http.StatusInternalServerError)
			return
		}

		// Повторно читаем ключ: другой параллельный запрос мог сохранить свой.
		err = dbPool.QueryRow(
			r.Context(),
			`SELECT idempotence_key
			 FROM yookassa_payments
			 WHERE order_id = $1`,
			req.OrderID,
		).Scan(&idempotenceKey)
		if err != nil {
			http.Error(w, "payment initialization failed", http.StatusInternalServerError)
			return
		}
	}

	var paymentRequest YooKassaCreateRequest
	paymentRequest.Amount.Value = "1.00"
	paymentRequest.Amount.Currency = "RUB"
	paymentRequest.Capture = true
	paymentRequest.Confirmation.Type = "redirect"
	paymentRequest.Confirmation.ReturnURL = baseURL + "/shop?payment=return"
	paymentRequest.Description = "Оплата доставки заказа " + req.OrderID
	paymentRequest.Metadata = map[string]string{
		"order_id": req.OrderID,
	}

	requestBody, err := json.Marshal(paymentRequest)
	if err != nil {
		http.Error(w, "payment request error", http.StatusInternalServerError)
		return
	}

	client := &http.Client{Timeout: 20 * time.Second}

	var responseData []byte
	var responseStatusCode int

	for attempt := 0; attempt < 2; attempt++ {
		httpRequest, err := http.NewRequestWithContext(
			r.Context(),
			http.MethodPost,
			"https://api.yookassa.ru/v3/payments",
			bytes.NewReader(requestBody),
		)
		if err != nil {
			http.Error(w, "payment request error", http.StatusInternalServerError)
			return
		}

		httpRequest.SetBasicAuth(shopID, secretKey)
		httpRequest.Header.Set("Content-Type", "application/json")
		httpRequest.Header.Set("Idempotence-Key", idempotenceKey)

		response, err := client.Do(httpRequest)
		if err != nil {
			log.Printf("YooKassa request failed order_id=%s: %v", req.OrderID, err)
			http.Error(w, "payment provider unavailable", http.StatusBadGateway)
			return
		}

		responseStatusCode = response.StatusCode
		responseData, err = io.ReadAll(io.LimitReader(response.Body, 1<<20))
		response.Body.Close()

		if err != nil {
			http.Error(w, "payment response error", http.StatusBadGateway)
			return
		}

		if responseStatusCode >= 200 && responseStatusCode < 300 {
			break
		}

		var providerError YooKassaErrorResponse
		_ = json.Unmarshal(responseData, &providerError)

		if attempt == 0 &&
			providerError.Code == "invalid_request" &&
			providerError.Parameter == "Idempotence-Key" {

			newKey, keyErr := generateIdempotenceKey()
			if keyErr != nil {
				log.Printf("YooKassa idempotence key generation failed: %v", keyErr)
				http.Error(w, "payment initialization failed", http.StatusInternalServerError)
				return
			}

			tag, updateErr := dbPool.Exec(
				r.Context(),
				`UPDATE yookassa_payments
              SET idempotence_key = $3,
                  updated_at = now()
              WHERE order_id = $1
                AND idempotence_key = $2
                AND status = 'creating'
                AND payment_id IS NULL`,
				req.OrderID,
				idempotenceKey,
				newKey,
			)
			if updateErr != nil {
				log.Printf("YooKassa idempotence key update failed order_id=%s: %v", req.OrderID, updateErr)
				http.Error(w, "payment initialization failed", http.StatusInternalServerError)
				return
			}

			if tag.RowsAffected() != 1 {
				log.Printf("YooKassa idempotence key was changed concurrently order_id=%s", req.OrderID)
				http.Error(w, "payment initialization conflict", http.StatusConflict)
				return
			}

			idempotenceKey = newKey
			continue
		}

		break
	}

	if responseStatusCode < 200 || responseStatusCode >= 300 {
		log.Printf(
			"YooKassa returned HTTP %d for order_id=%s: %s",
			responseStatusCode,
			req.OrderID,
			string(responseData),
		)
		http.Error(w, "payment creation failed", http.StatusBadGateway)
		return
	}

	var paymentResponse YooKassaCreateResponse
	if err := json.Unmarshal(responseData, &paymentResponse); err != nil {
		http.Error(w, "invalid payment response", http.StatusBadGateway)
		return
	}

	if paymentResponse.ID == "" || paymentResponse.Confirmation.ConfirmationURL == "" {
		http.Error(w, "payment link missing", http.StatusBadGateway)
		return
	}

	_, err = dbPool.Exec(
		r.Context(),
		`UPDATE yookassa_payments
		 SET payment_id = $2,
		     status = $3,
		     confirmation_url = $4,
		     updated_at = now()
		 WHERE order_id = $1`,
		req.OrderID,
		paymentResponse.ID,
		paymentResponse.Status,
		paymentResponse.Confirmation.ConfirmationURL,
	)
	if err != nil {
		log.Printf("YooKassa payment save failed order_id=%s: %v", req.OrderID, err)
		http.Error(w, "payment save failed", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"ok":               true,
		"confirmation_url": paymentResponse.Confirmation.ConfirmationURL,
	})
}

func yooKassaWebhookHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var notification YooKassaWebhookRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&notification); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}

	// Обрабатываем только успешную или отменённую оплату.
	if notification.Event != "payment.succeeded" &&
		notification.Event != "payment.canceled" {
		w.WriteHeader(http.StatusOK)
		return
	}

	paymentID := strings.TrimSpace(notification.Object.ID)
	if paymentID == "" {
		http.Error(w, "payment id is empty", http.StatusBadRequest)
		return
	}

	shopID := strings.TrimSpace(os.Getenv("YOOKASSA_SHOP_ID"))
	secretKey := strings.TrimSpace(os.Getenv("YOOKASSA_SECRET_KEY"))

	if shopID == "" || secretKey == "" {
		log.Println("YooKassa webhook: credentials are missing")
		http.Error(w, "payment service is not configured", http.StatusInternalServerError)
		return
	}

	// Проверяем настоящий статус платежа через API ЮKassa.
	apiRequest, err := http.NewRequestWithContext(
		r.Context(),
		http.MethodGet,
		"https://api.yookassa.ru/v3/payments/"+url.PathEscape(paymentID),
		nil,
	)
	if err != nil {
		http.Error(w, "payment verification failed", http.StatusInternalServerError)
		return
	}

	apiRequest.SetBasicAuth(shopID, secretKey)

	client := &http.Client{Timeout: 20 * time.Second}
	apiResponse, err := client.Do(apiRequest)
	if err != nil {
		log.Printf("YooKassa verification request failed payment_id=%s: %v", paymentID, err)
		http.Error(w, "payment provider unavailable", http.StatusBadGateway)
		return
	}
	defer apiResponse.Body.Close()

	if apiResponse.StatusCode != http.StatusOK {
		log.Printf("YooKassa verification returned HTTP %d payment_id=%s", apiResponse.StatusCode, paymentID)
		http.Error(w, "payment verification failed", http.StatusBadGateway)
		return
	}

	var payment YooKassaPaymentDetails
	if err := json.NewDecoder(io.LimitReader(apiResponse.Body, 1<<20)).Decode(&payment); err != nil {
		http.Error(w, "invalid payment response", http.StatusBadGateway)
		return
	}

	if payment.ID != paymentID {
		http.Error(w, "payment id mismatch", http.StatusBadRequest)
		return
	}

	amount, err := strconv.ParseFloat(payment.Amount.Value, 64)
	if err != nil || amount != 1 || payment.Amount.Currency != "RUB" {
		log.Printf("YooKassa invalid amount or currency payment_id=%s", paymentID)
		http.Error(w, "invalid payment amount", http.StatusBadRequest)
		return
	}

	orderID := strings.TrimSpace(payment.Metadata["order_id"])
	if orderID == "" {
		http.Error(w, "order id missing in payment metadata", http.StatusBadRequest)
		return
	}

	if notification.Event == "payment.succeeded" && payment.Status != "succeeded" {
		http.Error(w, "payment is not succeeded", http.StatusBadRequest)
		return
	}

	if notification.Event == "payment.canceled" && payment.Status != "canceled" {
		http.Error(w, "payment is not canceled", http.StatusBadRequest)
		return
	}

	order, err := loadPendingOrder(orderID)
	if err != nil {
		log.Printf("YooKassa order not found order_id=%s: %v", orderID, err)
		http.Error(w, "order not found", http.StatusNotFound)
		return
	}

	// Проверяем привязку платежа к заказу и меняем статусы в одной транзакции.
	tx, err := dbPool.Begin(r.Context())
	if err != nil {
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}
	defer tx.Rollback(r.Context())

	var dbOrderID string
	var dbPaymentStatus string

	err = tx.QueryRow(
		r.Context(),
		`SELECT order_id, status
		 FROM yookassa_payments
		 WHERE payment_id = $1
		 FOR UPDATE`,
		paymentID,
	).Scan(&dbOrderID, &dbPaymentStatus)

	if err != nil {
		log.Printf("YooKassa payment not found in database payment_id=%s: %v", paymentID, err)
		http.Error(w, "payment not found", http.StatusNotFound)
		return
	}

	if dbOrderID != orderID {
		http.Error(w, "order id mismatch", http.StatusBadRequest)
		return
	}

	// Повторное уведомление об уже обработанном платеже.
	if dbPaymentStatus == payment.Status {
		if err := tx.Commit(r.Context()); err != nil {
			http.Error(w, "database error", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
		return
	}

	if notification.Event == "payment.canceled" {
		_, err = tx.Exec(
			r.Context(),
			`UPDATE yookassa_payments
      SET status = 'canceled', updated_at = now()
      WHERE payment_id = $1`,
			paymentID,
		)
		if err != nil {
			http.Error(w, "database error", http.StatusInternalServerError)
			return
		}

		// Резерв товаров сохраняем для повторной попытки оплаты.

		if err := tx.Commit(r.Context()); err != nil {
			http.Error(w, "database error", http.StatusInternalServerError)
			return
		}

		log.Printf("YooKassa payment canceled; reservation kept order_id=%s", orderID)
		w.WriteHeader(http.StatusOK)
		return
	}

	// Успешная оплата: резервируем товары до обработки заказа.
	result, err := tx.Exec(
		r.Context(),
		`UPDATE cart_reservations
		 SET status = 'confirmed', updated_at = now()
		 WHERE order_id = $1
		   AND status = 'active'`,
		orderID,
	)
	if err != nil {
		http.Error(w, "reservation confirmation failed", http.StatusInternalServerError)
		return
	}

	rows := result.RowsAffected()
	if rows == 0 {
		log.Printf("YooKassa: no active reservations for order_id=%s", orderID)
		http.Error(w, "order reservations not found", http.StatusConflict)
		return
	}

	_, err = tx.Exec(
		r.Context(),
		`UPDATE yookassa_payments
		 SET status = 'succeeded', updated_at = now()
		 WHERE payment_id = $1`,
		paymentID,
	)
	if err != nil {
		http.Error(w, "payment status update failed", http.StatusInternalServerError)
		return
	}

	if err := tx.Commit(r.Context()); err != nil {
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}

	// Заказ уже был подтверждён ранее — повторно не уведомляем.
	if !isOrderConfirmed(orderID) {
		if err := sendOrderToMaxAdmins(
			order.AdminText,
			order.UserID,
			order.OrderID,
		); err != nil {
			log.Printf("MAX admin notification failed order_id=%s: %v", orderID, err)
		}

		if order.UserID != 0 {
			clientText := "✅ Оплата доставки получена!\n\n" +
				"Заказ № " + order.OrderID + "\n\n" +
				"💳 Предоплата за доставку: 300 ₽ — оплачена.\n" +
				"📦 Стоимость товаров оплачивается отдельно при получении.\n\n" +
				"Заказ передан администраторам."

			if err := sendMaxMessageToUser(order.UserID, clientText); err != nil {
				log.Printf("MAX client notification failed order_id=%s: %v", orderID, err)
			}
		}

		if err := markOrderConfirmed(orderID); err != nil {
			log.Printf("mark confirmed failed order_id=%s: %v", orderID, err)
		}
	}

	log.Printf("YooKassa payment succeeded order_id=%s payment_id=%s", orderID, paymentID)
	w.WriteHeader(http.StatusOK)
}

func formatQtyServer(qty float64, unit string) string {
	unit = strings.TrimSpace(unit)
	if unit == "" {
		unit = "кг"
	}

	if unit == "кг" {
		if qty == float64(int(qty)) {
			return strconv.Itoa(int(qty)) + " кг"
		}
		return strings.TrimRight(strings.TrimRight(strconv.FormatFloat(qty, 'f', 1, 64), "0"), ".") + " кг"
	}

	if qty == float64(int(qty)) {
		return strconv.Itoa(int(qty)) + " " + unit
	}

	return strings.TrimRight(strings.TrimRight(strconv.FormatFloat(qty, 'f', 1, 64), "0"), ".") + " " + unit
}

func formatRubServer(n int) string {
	return strconv.Itoa(n) + " ₽"
}

func sendOrderToMaxAdmins(text string, userID int64, orderID string) error {
	token := strings.TrimSpace(os.Getenv("BOT_TOKEN"))
	if token == "" {
		token = strings.TrimSpace(os.Getenv("MAX_BOT_TOKEN"))
	}
	if token == "" {
		return fmt.Errorf("BOT_TOKEN / MAX_BOT_TOKEN is empty")
	}

	rawAdmins := strings.TrimSpace(os.Getenv("ADMIN_CHAT_IDS"))
	if rawAdmins == "" {
		rawAdmins = strings.TrimSpace(os.Getenv("ADMIN_CHAT_ID"))
	}
	if rawAdmins == "" {
		return fmt.Errorf("ADMIN_CHAT_ID / ADMIN_CHAT_IDS is empty")
	}

	admins := strings.FieldsFunc(rawAdmins, func(r rune) bool {
		return r == ',' || r == ';' || r == ' ' || r == '\n' || r == '\t'
	})

	var lastErr error
	for _, a := range admins {
		a = strings.TrimSpace(a)
		if a == "" {
			continue
		}

		chatID, err := strconv.ParseInt(a, 10, 64)
		if err != nil {
			lastErr = err
			continue
		}

		if err := sendMaxMessage(
			token,
			chatID,
			text,
			orderStatusKeyboard(userID, orderID),
		); err != nil {
			log.Printf(
				"order admin message failed chat_id=%d order_id=%s: %v",
				chatID,
				orderID,
				err,
			)
			lastErr = err
			continue
		}

		log.Printf(
			"order admin message sent chat_id=%d order_id=%s",
			chatID,
			orderID,
		)
	}

	return lastErr
}

func orderStatusKeyboard(userID int64, orderID string) []any {
	if userID == 0 {
		return []any{}
	}
	return []any{
		map[string]any{
			"type": "inline_keyboard",
			"payload": map[string]any{
				"buttons": [][]map[string]string{
					{
						{"type": "message", "text": "✅ Заказ взят|" + strconv.FormatInt(userID, 10) + "|" + orderID},
					},
					{
						{"type": "message", "text": "🥩 Заказ собран|" + strconv.FormatInt(userID, 10) + "|" + orderID},
					},
					{
						{"type": "message", "text": "🚚 Заказ в доставке|" + strconv.FormatInt(userID, 10) + "|" + orderID},
					},
					{
						{"type": "message", "text": "❌ Заказ отменён|" + strconv.FormatInt(userID, 10) + "|" + orderID},
					},
				},
			},
		},
	}
}

func sendMaxMessage(token string, chatID int64, text string, attachments []any) error {
	body := map[string]any{
		"text":        text,
		"attachments": attachments,
		"notify":      true,
	}

	data, _ := json.Marshal(body)

	u := "https://platform-api.max.ru/messages?v=1.2.5&chat_id=" + url.QueryEscape(strconv.FormatInt(chatID, 10))
	httpReq, err := http.NewRequest(http.MethodPost, u, bytes.NewReader(data))
	if err != nil {
		return err
	}

	httpReq.Header.Set("Authorization", token)
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("MAX send failed: status=%d body=%s", resp.StatusCode, string(raw))
	}

	return nil
}

func sendMaxMessageToUserWithAttachments(token string, userID int64, text string, attachments []any) error {
	body := map[string]any{
		"text":        text,
		"attachments": attachments,
		"notify":      true,
	}

	data, _ := json.Marshal(body)

	u := "https://platform-api.max.ru/messages?v=1.2.5&user_id=" +
		url.QueryEscape(strconv.FormatInt(userID, 10))

	httpReq, err := http.NewRequest(http.MethodPost, u, bytes.NewReader(data))
	if err != nil {
		return err
	}

	httpReq.Header.Set("Authorization", token)
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(resp.Body)
		return fmt.Errorf(
			"MAX send to admin user failed: status=%d body=%s",
			resp.StatusCode,
			string(raw),
		)
	}

	return nil
}

func sendMaxMessageToUser(userID int64, text string) error {
	token := strings.TrimSpace(os.Getenv("BOT_TOKEN"))
	if token == "" {
		token = strings.TrimSpace(os.Getenv("MAX_BOT_TOKEN"))
	}
	if token == "" {
		return fmt.Errorf("BOT_TOKEN / MAX_BOT_TOKEN is empty")
	}

	body := map[string]any{
		"text":   text,
		"notify": true,
	}

	data, _ := json.Marshal(body)

	u := "https://platform-api.max.ru/messages?v=1.2.5&user_id=" + url.QueryEscape(strconv.FormatInt(userID, 10))
	httpReq, err := http.NewRequest(http.MethodPost, u, bytes.NewReader(data))
	if err != nil {
		return err
	}

	httpReq.Header.Set("Authorization", token)
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("MAX send to user failed: status=%d body=%s", resp.StatusCode, string(raw))
	}

	return nil
}
