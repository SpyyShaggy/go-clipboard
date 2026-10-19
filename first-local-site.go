package main

import (
	_ "embed"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

//go:embed logo.png
var logoPNG []byte

var (
	portFlag = flag.String("port", ":8000", "Порт сервера (например, :8000)")
	dirFlag  = flag.String("dir", "", "Папка для файлов (по умолчанию $SHARE_DIR или автоопределение)")
	ttlFlag  = flag.Duration("ttl", 1*time.Hour, "Время жизни файлов до автоудаления")

	Port     = ":8000"
	ShareDir = "/mnt/game/site"
	FileTTL  = 1 * time.Hour
)

func resolveShareDir(custom string) string {
	if custom != "" {
		return custom
	}
	if env := os.Getenv("SHARE_DIR"); env != "" {
		return env
	}
	if fi, err := os.Stat("/mnt/game/site"); err == nil && fi.IsDir() {
		return "/mnt/game/site"
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, "Downloads", "lan-share")
	}
	return "./uploads"
}



type FileItem struct {
	Name string `json:"name"`
	Size string `json:"size"`
	Time string `json:"time"`
}

func prettySize(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d Б", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	units := []string{"КБ", "МБ", "ГБ", "ТБ"}
	return fmt.Sprintf("%.1f %s", float64(b)/float64(div), units[exp])
}

func sanitizeName(name string) string {
	name = filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	name = strings.Trim(name, ". ")
	var sb strings.Builder
	for _, r := range name {
		if r >= 32 && !strings.ContainsRune(`<>:"/\|?*`, r) {
			sb.WriteRune(r)
		}
	}
	res := sb.String()
	if res == "" {
		res = "file"
	}
	runes := []rune(res)
	if len(runes) > 180 {
		res = string(runes[:180])
	}
	return res
}

func createUniqueFile(dir, rawName string) (*os.File, string, error) {
	name := sanitizeName(rawName)
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)

	for i := 1; i < 100000; i++ {
		cand := name
		if i > 1 {
			cand = fmt.Sprintf("%s (%d)%s", stem, i, ext)
		}
		fullPath := filepath.Join(dir, cand)
		f, err := os.OpenFile(fullPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
		if err == nil {
			return f, cand, nil
		}
		if !os.IsExist(err) {
			return nil, "", err
		}
	}
	return nil, "", fmt.Errorf("слишком много файлов с таким именем")
}

func handleFiles(w http.ResponseWriter, r *http.Request) {
	entries, err := os.ReadDir(ShareDir)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	type fileWithMod struct {
		item FileItem
		mod  time.Time
	}
	var list []fileWithMod
	for _, e := range entries {
		if e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		list = append(list, fileWithMod{
			item: FileItem{
				Name: e.Name(),
				Size: prettySize(info.Size()),
				Time: info.ModTime().Format("02.01.2006 15:04"),
			},
			mod: info.ModTime(),
		})
	}
	sort.Slice(list, func(i, j int) bool {
		return list[i].mod.After(list[j].mod)
	})
	items := make([]FileItem, len(list))
	for i, v := range list {
		items[i] = v.item
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(items)
}

func handleUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	rawName := strings.TrimPrefix(r.URL.Path, "/api/upload/")
	name, err := url.PathUnescape(rawName)
	if err != nil || name == "" {
		name = "file"
	}

	tempFile, err := os.CreateTemp(ShareDir, ".upload-*")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	tempPath := tempFile.Name()
	defer func() {
		tempFile.Close()
		os.Remove(tempPath)
	}()

	buf := make([]byte, 4*1024*1024)
	if _, err := io.CopyBuffer(tempFile, r.Body, buf); err != nil {
		http.Error(w, "Ошибка приёма потока: "+err.Error(), http.StatusBadRequest)
		return
	}
	if err := tempFile.Close(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	destFile, finalName, err := createUniqueFile(ShareDir, name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	destFile.Close()

	if err := os.Rename(tempPath, filepath.Join(ShareDir, finalName)); err != nil {
		os.Remove(filepath.Join(ShareDir, finalName))
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusCreated)
	w.Write([]byte(finalName))
}

func handleText(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 10*1024*1024))
	if err != nil || len(strings.TrimSpace(string(body))) == 0 {
		http.Error(w, "Пустой или некорректный текст", http.StatusBadRequest)
		return
	}
	name := "Текст " + time.Now().Format("2006-01-02 15-04-05") + ".txt"
	f, _, err := createUniqueFile(ShareDir, name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer f.Close()
	if _, err := f.Write(body); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusCreated)
	w.Write([]byte("OK"))
}

func handleDownload(w http.ResponseWriter, r *http.Request) {
	rawName := strings.TrimPrefix(r.URL.Path, "/download/")
	name, err := url.PathUnescape(rawName)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	cleanName := filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	if cleanName == "." || cleanName == "/" || strings.HasPrefix(cleanName, ".") {
		http.NotFound(w, r)
		return
	}
	target := filepath.Join(ShareDir, cleanName)
	f, err := os.Open(target)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.IsDir() {
		http.NotFound(w, r)
		return
	}
	if r.URL.Query().Get("dl") == "1" {
		w.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''"+url.PathEscape(cleanName))
	}
	http.ServeContent(w, r, cleanName, info.ModTime(), f)
}

func localIPs() []string {
	var ips []string
	ifaces, _ := net.Interfaces()
	for _, i := range ifaces {
		if i.Flags&net.FlagUp == 0 || i.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := i.Addrs()
		for _, a := range addrs {
			if ipnet, ok := a.(*net.IPNet); ok && ipnet.IP.To4() != nil {
				ips = append(ips, ipnet.IP.String())
			}
		}
	}
	return ips
}

func startAutoCleaner() {
	cleanOnce := func() {
		entries, err := os.ReadDir(ShareDir)
		if err != nil {
			return
		}
		now := time.Now()
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			info, err := e.Info()
			if err != nil {
				continue
			}
			if now.Sub(info.ModTime()) >= FileTTL {
				target := filepath.Join(ShareDir, e.Name())
				if err := os.Remove(target); err == nil {
					fmt.Printf("[AutoClean] Перманентно удален файл (>1ч): %s\n", e.Name())
				}
			}
		}
	}

	go func() {
		cleanOnce()
		ticker := time.NewTicker(1 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			cleanOnce()
		}
	}()
}


func main() {
	flag.Parse()
	Port = *portFlag
	ShareDir = resolveShareDir(*dirFlag)
	FileTTL = *ttlFlag

	if err := os.MkdirAll(ShareDir, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "Ошибка создания директории %s: %v\n", ShareDir, err)
		os.Exit(1)
	}

	startAutoCleaner()

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		html := strings.ReplaceAll(pageHTML, "{{SHARE_DIR}}", ShareDir)
		w.Write([]byte(html))
	})
	http.HandleFunc("/api/files", handleFiles)
	http.HandleFunc("/api/upload/", handleUpload)
	http.HandleFunc("/api/text", handleText)
	http.HandleFunc("/download/", handleDownload)
	http.HandleFunc("/logo.png", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Cache-Control", "public, max-age=86400")
		w.Write(logoPNG)
	})

	fmt.Printf("Сервер запущен. Порт: %s\nПапка: %s (TTL: %v)\nОткройте в браузере:\n", Port, ShareDir, FileTTL)
	for _, ip := range localIPs() {
		fmt.Printf("  http://%s%s/\n", ip, Port)
	}
	if err := http.ListenAndServe(Port, nil); err != nil {
		fmt.Fprintf(os.Stderr, "Ошибка сервера: %v\n", err)
		os.Exit(1)
	}
}

const pageHTML = `<!doctype html>
<html lang="ru">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>LAN Drop — Быстрый обмен</title>
<link rel="icon" type="image/png" href="/logo.png">
<link rel="preconnect" href="https://fonts.googleapis.com">
<link rel="preconnect" href="https://fonts.gstatic.com" crossorigin>
<link href="https://fonts.googleapis.com/css2?family=Inter:wght@300;400;500;600;700&family=JetBrains+Mono:wght@400;500;600&display=swap" rel="stylesheet">
<style>
:root {
  --bg: #0D0D0F;
  --bg-alt: #121215;
  --surface: #18181B;
  --surface-hover: #1F1F23;
  --surface-active: #27272A;
  --border: rgba(255, 255, 255, 0.08);
  --border-hover: rgba(255, 255, 255, 0.16);
  --border-focus: rgba(255, 255, 255, 0.3);
  --text: #F4F4F5;
  --text-muted: #A1A1AA;
  --text-dim: #71717A;
  --white: #FFFFFF;
  --btn-primary-bg: #F4F4F5;
  --btn-primary-text: #0D0D0F;
  --btn-primary-hover: #FFFFFF;
  --btn-secondary-bg: rgba(255, 255, 255, 0.05);
  --btn-secondary-hover: rgba(255, 255, 255, 0.09);
  --success: #10B981;
  --success-bg: rgba(16, 185, 129, 0.1);
  --danger: #EF4444;
  --danger-bg: rgba(239, 68, 68, 0.1);
  --font-sans: 'Inter', -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif;
  --font-mono: 'JetBrains Mono', 'SFMono-Regular', Consolas, monospace;
  --radius-sm: 6px;
  --radius-md: 10px;
  --radius-lg: 14px;
  --ease: cubic-bezier(0.22, 1, 0.36, 1);
}

*, *::before, *::after {
  box-sizing: border-box;
  margin: 0;
  padding: 0;
}

html {
  scroll-behavior: smooth;
  -webkit-font-smoothing: antialiased;
  -moz-osx-font-smoothing: grayscale;
}

body {
  background: var(--bg);
  color: var(--text);
  font-family: var(--font-sans);
  font-size: 14px;
  line-height: 1.5;
  min-height: 100vh;
  padding: 32px 16px 64px;
}

main {
  max-width: 960px;
  margin: 0 auto;
}

/* ── Header ── */
header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  flex-wrap: wrap;
  gap: 16px;
  margin-bottom: 28px;
  padding-bottom: 20px;
  border-bottom: 1px solid var(--border);
}

.brand {
  display: flex;
  align-items: center;
  gap: 12px;
}

.brand-logo {
  width: 30px;
  height: 30px;
  object-fit: contain;
  display: block;
  border-radius: var(--radius-sm);
  filter: drop-shadow(0 2px 8px rgba(255, 255, 255, 0.12));
  transition: transform 0.2s var(--ease);
}

.brand:hover .brand-logo {
  transform: scale(1.08);
}

.brand h1 {
  font-size: 20px;
  font-weight: 600;
  letter-spacing: -0.02em;
  color: var(--white);
}

.header-meta {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 10px;
  font-size: 12px;
}

.status-dot-wrap {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  padding: 4px 10px;
  background: var(--surface);
  border: 1px solid var(--border);
  border-radius: var(--radius-sm);
  color: var(--text-muted);
}

.status-dot {
  width: 6px;
  height: 6px;
  background: var(--success);
  border-radius: 50%;
  box-shadow: 0 0 8px var(--success);
}

.path-pill {
  font-family: var(--font-mono);
  background: var(--surface);
  border: 1px solid var(--border);
  color: var(--text-muted);
  padding: 4px 10px;
  border-radius: var(--radius-sm);
  max-width: 380px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.ttl-pill {
  color: var(--text-dim);
  font-size: 11px;
}

/* ── Grid Layout ── */
.grid {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: 18px;
  margin-bottom: 20px;
}

@media (max-width: 740px) {
  .grid {
    grid-template-columns: 1fr;
  }
}

/* ── Cards ── */
.card {
  background: var(--surface);
  border: 1px solid var(--border);
  border-radius: var(--radius-lg);
  padding: 22px;
  box-shadow: 0 8px 24px rgba(0, 0, 0, 0.35);
  transition: border-color 0.2s var(--ease);
}

.card:hover {
  border-color: var(--border-hover);
}

.card-title {
  font-size: 14px;
  font-weight: 600;
  color: var(--text);
  margin-bottom: 14px;
  display: flex;
  align-items: center;
  justify-content: space-between;
  letter-spacing: -0.01em;
}

/* ── Dropzone ── */
.dropzone {
  border: 1px dashed var(--border-hover);
  background: var(--bg-alt);
  border-radius: var(--radius-md);
  padding: 28px 18px;
  text-align: center;
  cursor: pointer;
  transition: all 0.2s var(--ease);
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  min-height: 165px;
  position: relative;
}

.dropzone.dragover, .dropzone:hover {
  border-color: rgba(255, 255, 255, 0.35);
  background: rgba(255, 255, 255, 0.02);
}

.drop-icon {
  margin-bottom: 10px;
  color: var(--text-dim);
  transition: color 0.2s, transform 0.2s var(--ease);
}

.dropzone:hover .drop-icon, .dropzone.dragover .drop-icon {
  color: var(--white);
  transform: translateY(-2px);
}

.drop-title {
  font-size: 14px;
  font-weight: 500;
  color: var(--text);
  margin-bottom: 4px;
}

.drop-sub {
  font-size: 12px;
  color: var(--text-dim);
  margin-bottom: 16px;
}

.picker-btns {
  display: flex;
  gap: 8px;
  flex-wrap: wrap;
  justify-content: center;
}

/* ── Buttons ── */
.btn {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  gap: 6px;
  border: none;
  border-radius: var(--radius-sm);
  font-family: var(--font-sans);
  font-size: 13px;
  font-weight: 500;
  padding: 8px 14px;
  cursor: pointer;
  transition: all 0.15s var(--ease);
  user-select: none;
  text-decoration: none;
}

.btn-primary {
  background: var(--btn-primary-bg);
  color: var(--btn-primary-text);
}

.btn-primary:hover {
  background: var(--btn-primary-hover);
  box-shadow: 0 0 12px rgba(255, 255, 255, 0.15);
}

.btn-secondary {
  background: var(--btn-secondary-bg);
  color: var(--text);
  border: 1px solid var(--border);
}

.btn-secondary:hover {
  background: var(--btn-secondary-hover);
  border-color: var(--border-hover);
  color: var(--white);
}

.btn-sm {
  font-size: 12px;
  padding: 5px 10px;
}

/* ── Textarea ── */
.textarea-wrap {
  display: flex;
  flex-direction: column;
  gap: 12px;
  height: 100%;
}

textarea {
  width: 100%;
  height: 115px;
  background: var(--bg-alt);
  color: var(--text);
  border: 1px solid var(--border);
  border-radius: var(--radius-md);
  padding: 12px;
  font-family: var(--font-sans);
  font-size: 13px;
  resize: none;
  outline: none;
  transition: border-color 0.2s, background 0.2s;
}

textarea:focus {
  border-color: var(--border-focus);
  background: #141418;
}

textarea::placeholder {
  color: var(--text-dim);
}

.note-actions {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
}

.kbd-hint {
  font-size: 11px;
  color: var(--text-dim);
  font-family: var(--font-mono);
}

.status-msg {
  font-size: 12px;
  color: var(--text-muted);
}

.status-msg.success { color: var(--success); }
.status-msg.error { color: var(--danger); }

/* ── Upload List ── */
.upload-list {
  margin-top: 14px;
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.up-item {
  background: var(--bg-alt);
  border: 1px solid var(--border);
  border-radius: var(--radius-sm);
  padding: 10px 12px;
  font-size: 12px;
}

.up-top {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 6px;
  gap: 8px;
}

.up-name {
  font-weight: 500;
  color: var(--text);
  word-break: break-all;
  display: flex;
  align-items: center;
  gap: 6px;
}

.up-pct {
  font-family: var(--font-mono);
  color: var(--text-dim);
  font-size: 11px;
  flex-shrink: 0;
}

.bar-bg {
  height: 4px;
  background: rgba(255, 255, 255, 0.06);
  border-radius: 2px;
  overflow: hidden;
}

.bar-fill {
  height: 100%;
  background: var(--text);
  width: 0%;
  border-radius: 2px;
  transition: width 0.12s linear, background-color 0.2s;
}

/* ── Files Section ── */
.files-card {
  margin-top: 4px;
}

.files-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  flex-wrap: wrap;
  gap: 12px;
  margin-bottom: 16px;
  padding-bottom: 14px;
  border-bottom: 1px solid var(--border);
}

.files-title-group {
  display: flex;
  align-items: center;
  gap: 10px;
}

.badge {
  font-family: var(--font-mono);
  font-size: 11px;
  font-weight: 500;
  background: var(--bg-alt);
  border: 1px solid var(--border);
  color: var(--text-muted);
  padding: 2px 7px;
  border-radius: 12px;
}

.files-controls {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
}

.search-input {
  background: var(--bg-alt);
  border: 1px solid var(--border);
  border-radius: var(--radius-sm);
  color: var(--text);
  font-family: var(--font-sans);
  font-size: 12px;
  padding: 6px 10px;
  outline: none;
  width: 190px;
  transition: all 0.2s;
}

.search-input:focus {
  border-color: var(--border-focus);
  width: 230px;
}

.search-input::placeholder {
  color: var(--text-dim);
}

/* ── File Row ── */
.files-list {
  display: flex;
  flex-direction: column;
}

.file-row {
  display: flex;
  justify-content: space-between;
  align-items: center;
  gap: 14px;
  padding: 12px 6px;
  border-bottom: 1px solid rgba(255, 255, 255, 0.04);
  transition: background 0.15s var(--ease);
  border-radius: var(--radius-sm);
}

.file-row:last-child {
  border-bottom: none;
}

.file-row:hover {
  background: rgba(255, 255, 255, 0.02);
}

.file-info {
  display: flex;
  align-items: center;
  gap: 12px;
  min-width: 0;
  flex: 1;
}

.file-icon {
  font-size: 16px;
  color: var(--text-dim);
  flex-shrink: 0;
}

.file-details {
  min-width: 0;
  display: flex;
  flex-direction: column;
  gap: 2px;
}

.fname {
  color: var(--text);
  text-decoration: none;
  font-weight: 500;
  font-size: 13px;
  word-break: break-all;
  transition: color 0.15s;
}

.fname:hover {
  color: var(--white);
  text-decoration: underline;
}

.fmeta {
  font-family: var(--font-mono);
  font-size: 11px;
  color: var(--text-dim);
  display: flex;
  align-items: center;
  gap: 8px;
}

.factions {
  display: flex;
  align-items: center;
  gap: 6px;
  flex-shrink: 0;
}

.empty-state {
  text-align: center;
  padding: 36px 16px;
  color: var(--text-dim);
  font-size: 13px;
}

.empty-icon {
  font-size: 24px;
  margin-bottom: 8px;
  opacity: 0.4;
}
</style>
</head>
<body>

<main>
  <header>
    <div class="brand">
      <img src="/logo.png" alt="LAN Drop" class="brand-logo" width="30" height="30">
      <h1>LAN Drop</h1>
    </div>
    <div class="header-meta">
      <div class="status-dot-wrap">
        <span class="status-dot"></span>
        <span>Онлайн</span>
      </div>
      <span class="path-pill" title="{{SHARE_DIR}}">{{SHARE_DIR}}</span>
      <span class="ttl-pill">автоудаление через 1ч</span>
    </div>
  </header>

  <div class="grid">
    <!-- Card 1: Files / Folders Dropzone -->
    <section class="card">
      <div class="card-title">
        <span>Отправить файлы</span>
      </div>
      <div class="dropzone" id="dropzone">
        <div class="drop-icon">
          <svg width="28" height="28" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round">
            <path d="M21 15v4a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-4"></path>
            <polyline points="17 8 12 3 7 8"></polyline>
            <line x1="12" y1="3" x2="12" y2="15"></line>
          </svg>
        </div>
        <div class="drop-title">Перетащите файлы или папки сюда</div>
        <div class="drop-sub">Папки пакуются в .zip на лету · Потоковая передача</div>
        <div class="picker-btns">
          <label class="btn btn-secondary btn-sm" for="picker">Выбрать файлы</label>
          <label class="btn btn-secondary btn-sm" for="folderPicker">Выбрать папку</label>
        </div>
        <input id="picker" type="file" multiple style="display:none">
        <input id="folderPicker" type="file" webkitdirectory directory multiple style="display:none">
      </div>
      <div class="upload-list" id="uploads"></div>
    </section>

    <!-- Card 2: Quick Note / Text -->
    <section class="card">
      <div class="textarea-wrap">
        <div class="card-title">
          <span>Быстрая заметка / ссылка</span>
        </div>
        <textarea id="text" placeholder="Вставьте текст, ссылку или код..."></textarea>
        <div class="note-actions">
          <span class="kbd-hint">Ctrl + Enter для отправки</span>
          <div style="display:flex;align-items:center;gap:10px">
            <span class="status-msg" id="textStatus"></span>
            <button class="btn btn-primary btn-sm" id="sendText">Сохранить .txt</button>
          </div>
        </div>
      </div>
    </section>
  </div>

  <!-- Card 3: Files in share directory -->
  <section class="card files-card">
    <div class="files-header">
      <div class="files-title-group">
        <span class="card-title" style="margin:0">Хранилище</span>
        <span class="badge" id="fileCount">0</span>
      </div>
      <div class="files-controls">
        <input type="text" id="searchInput" class="search-input" placeholder="Поиск по имени...">
        <button class="btn btn-secondary btn-sm" id="refreshBtn" onclick="refresh()">
          <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
            <polyline points="23 4 23 10 17 10"></polyline>
            <polyline points="1 20 1 14 7 14"></polyline>
            <path d="M3.51 9a9 9 0 0 1 14.85-3.36L23 10M1 14l4.64 4.36A9 9 0 0 0 20.49 15"></path>
          </svg>
          Обновить
        </button>
      </div>
    </div>
    <div class="files-list" id="files">
      <div class="empty-state">Загрузка файлов...</div>
    </div>
  </section>
</main>

<script>
const filesEl = document.getElementById('files');
const uploadsEl = document.getElementById('uploads');
const dropzone = document.getElementById('dropzone');
const picker = document.getElementById('picker');
const folderPicker = document.getElementById('folderPicker');
const fileCountEl = document.getElementById('fileCount');
const searchInput = document.getElementById('searchInput');

let cachedFiles = [];

// CRC32 table for client-side ZIP packaging
const CRC32_TABLE = new Uint32Array(256);
for (let i = 0; i < 256; i++) {
  let c = i;
  for (let k = 0; k < 8; k++) {
    c = (c & 1) ? (0xedb88320 ^ (c >>> 1)) : (c >>> 1);
  }
  CRC32_TABLE[i] = c;
}

async function getFileCrc32(file) {
  let c = 0xffffffff;
  const reader = file.stream().getReader();
  while (true) {
    const { done, value } = await reader.read();
    if (done) break;
    for (let i = 0; i < value.length; i++) {
      c = CRC32_TABLE[(c ^ value[i]) & 0xff] ^ (c >>> 8);
    }
  }
  return (c ^ 0xffffffff) >>> 0;
}

function dosDateTime(date = new Date()) {
  const d = ((date.getFullYear() - 1980) << 9) | ((date.getMonth() + 1) << 5) | date.getDate();
  const t = (date.getHours() << 11) | (date.getMinutes() << 5) | (date.getSeconds() >> 1);
  return { time: t & 0xffff, date: d & 0xffff };
}

async function createZipBlob(items, onProgress) {
  const encoder = new TextEncoder();
  const parts = [];
  const cdEntries = [];
  let offset = 0;
  const { time, date } = dosDateTime();

  for (let idx = 0; idx < items.length; idx++) {
    const item = items[idx];
    if (onProgress) onProgress(idx + 1, items.length);
    const cleanPath = item.path.replace(/\\/g, '/');
    const nameBytes = encoder.encode(cleanPath);
    const file = item.file;
    const crc = await getFileCrc32(file);
    const size = file.size;

    const localHeader = new Uint8Array(30 + nameBytes.length);
    const view = new DataView(localHeader.buffer);
    view.setUint32(0, 0x04034b50, true);
    view.setUint16(4, 20, true);
    view.setUint16(6, 0x0800, true);
    view.setUint16(8, 0, true);
    view.setUint16(10, time, true);
    view.setUint16(12, date, true);
    view.setUint32(14, crc, true);
    view.setUint32(18, size, true);
    view.setUint32(22, size, true);
    view.setUint16(26, nameBytes.length, true);
    view.setUint16(28, 0, true);
    localHeader.set(nameBytes, 30);

    parts.push(localHeader);
    parts.push(file);

    cdEntries.push({ nameBytes, crc, size, offset, time, date });
    offset += localHeader.length + size;
  }

  const cdStartOffset = offset;
  let cdSize = 0;

  for (const cd of cdEntries) {
    const header = new Uint8Array(46 + cd.nameBytes.length);
    const view = new DataView(header.buffer);
    view.setUint32(0, 0x02014b50, true);
    view.setUint16(4, 20, true);
    view.setUint16(6, 20, true);
    view.setUint16(8, 0x0800, true);
    view.setUint16(10, 0, true);
    view.setUint16(12, cd.time, true);
    view.setUint16(14, cd.date, true);
    view.setUint32(16, cd.crc, true);
    view.setUint32(20, cd.size, true);
    view.setUint32(24, cd.size, true);
    view.setUint16(28, cd.nameBytes.length, true);
    view.setUint16(30, 0, true);
    view.setUint16(32, 0, true);
    view.setUint16(34, 0, true);
    view.setUint16(36, 0, true);
    view.setUint32(38, 0, true);
    view.setUint32(42, cd.offset, true);
    header.set(cd.nameBytes, 46);

    parts.push(header);
    cdSize += header.length;
  }

  const eocd = new Uint8Array(22);
  const eocdView = new DataView(eocd.buffer);
  eocdView.setUint32(0, 0x06054b50, true);
  eocdView.setUint16(4, 0, true);
  eocdView.setUint16(6, 0, true);
  eocdView.setUint16(8, cdEntries.length, true);
  eocdView.setUint16(10, cdEntries.length, true);
  eocdView.setUint32(12, cdSize, true);
  eocdView.setUint32(16, cdStartOffset, true);
  eocdView.setUint16(20, 0, true);
  parts.push(eocd);

  return new Blob(parts, { type: 'application/zip' });
}

function getFileIcon(filename) {
  const ext = filename.split('.').pop().toLowerCase();
  if (['zip', 'tar', 'gz', '7z', 'rar'].includes(ext)) return '📦';
  if (['png', 'jpg', 'jpeg', 'gif', 'svg', 'webp'].includes(ext)) return '🖼️';
  if (['mp4', 'mkv', 'avi', 'mov', 'webm'].includes(ext)) return '🎬';
  if (['mp3', 'wav', 'flac', 'ogg'].includes(ext)) return '🎵';
  if (['txt', 'md', 'log'].includes(ext)) return '📄';
  if (['pdf'].includes(ext)) return '📑';
  if (['go', 'js', 'ts', 'py', 'json', 'html', 'css', 'sh'].includes(ext)) return '⚙️';
  return '📁';
}

function renderFileList(items) {
  filesEl.replaceChildren();
  if (!items || !items.length) {
    const q = searchInput.value.trim();
    filesEl.innerHTML = '<div class="empty-state"><div class="empty-icon"><img src="/logo.png" width="36" height="36" style="opacity:0.3;filter:grayscale(40%);vertical-align:middle" alt=""></div>' +
      (q ? 'Ничего не найдено по запросу "' + escapeHtml(q) + '"' : 'Папка пуста. Отправьте файл или заметку выше.') +
      '</div>';
    return;
  }

  for (const f of items) {
    const row = document.createElement('div');
    row.className = 'file-row';

    const info = document.createElement('div');
    info.className = 'file-info';

    const icon = document.createElement('span');
    icon.className = 'file-icon';
    icon.textContent = getFileIcon(f.name);

    const details = document.createElement('div');
    details.className = 'file-details';

    const a = document.createElement('a');
    a.className = 'fname';
    a.href = '/download/' + encodeURIComponent(f.name);
    a.target = '_blank';
    a.textContent = f.name;

    const meta = document.createElement('div');
    meta.className = 'fmeta';
    meta.textContent = f.size + '  ·  ' + f.time;

    details.append(a, meta);
    info.append(icon, details);

    const actions = document.createElement('div');
    actions.className = 'factions';

    const copyBtn = document.createElement('button');
    copyBtn.className = 'btn btn-secondary btn-sm';
    copyBtn.textContent = 'Копировать';
    copyBtn.title = 'Скопировать ссылку на файл';
    copyBtn.onclick = () => {
      const url = window.location.origin + '/download/' + encodeURIComponent(f.name);
      navigator.clipboard.writeText(url).then(() => {
        copyBtn.textContent = 'Скопировано!';
        setTimeout(() => copyBtn.textContent = 'Копировать', 1600);
      });
    };

    const dl = document.createElement('a');
    dl.className = 'btn btn-primary btn-sm';
    dl.href = '/download/' + encodeURIComponent(f.name) + '?dl=1';
    dl.textContent = 'Скачать';

    actions.append(copyBtn, dl);
    row.append(info, actions);
    filesEl.append(row);
  }
}

function escapeHtml(str) {
  const p = document.createElement('p');
  p.textContent = str;
  return p.innerHTML;
}

function filterFiles() {
  const query = searchInput.value.trim().toLowerCase();
  if (!query) {
    renderFileList(cachedFiles);
    return;
  }
  const filtered = cachedFiles.filter(f => f.name.toLowerCase().includes(query));
  renderFileList(filtered);
}

searchInput.addEventListener('input', filterFiles);

async function refresh() {
  try {
    const r = await fetch('/api/files');
    if (!r.ok) throw Error();
    const data = await r.json();
    cachedFiles = data || [];
    fileCountEl.textContent = cachedFiles.length;
    filterFiles();
  } catch {
    filesEl.innerHTML = '<div class="empty-state" style="color:var(--danger)">Ошибка при загрузке списка файлов.</div>';
  }
}

function uploadFileWithProgress(blobOrFile, filename, itemEl, barFill, pctSpan) {
  return new Promise(resolve => {
    const xhr = new XMLHttpRequest();
    xhr.open('PUT', '/api/upload/' + encodeURIComponent(filename));
    xhr.upload.onprogress = e => {
      if (e.lengthComputable) {
        const p = Math.round((e.loaded / e.total) * 100);
        barFill.style.width = p + '%';
        pctSpan.textContent = p + '%';
      }
    };
    xhr.onload = () => {
      if (xhr.status === 201) {
        barFill.style.width = '100%';
        barFill.style.background = 'var(--success)';
        pctSpan.textContent = 'Готово';
        pctSpan.style.color = 'var(--success)';
        setTimeout(() => itemEl.remove(), 4000);
        refresh();
      } else {
        barFill.style.background = 'var(--danger)';
        pctSpan.textContent = 'Ошибка';
        pctSpan.style.color = 'var(--danger)';
      }
      resolve();
    };
    xhr.onerror = () => {
      barFill.style.background = 'var(--danger)';
      pctSpan.textContent = 'Сбой сети';
      pctSpan.style.color = 'var(--danger)';
      resolve();
    };
    xhr.send(blobOrFile);
  });
}

function uploadFile(file) {
  const item = document.createElement('div');
  item.className = 'up-item';
  const top = document.createElement('div');
  top.className = 'up-top';
  const nameSpan = document.createElement('span');
  nameSpan.className = 'up-name';
  nameSpan.textContent = '📄 ' + file.name;
  const pctSpan = document.createElement('span');
  pctSpan.className = 'up-pct';
  pctSpan.textContent = '0%';
  top.append(nameSpan, pctSpan);

  const barBg = document.createElement('div');
  barBg.className = 'bar-bg';
  const barFill = document.createElement('div');
  barFill.className = 'bar-fill';
  barBg.append(barFill);
  item.append(top, barBg);
  uploadsEl.prepend(item);

  return uploadFileWithProgress(file, file.name, item, barFill, pctSpan);
}

async function packageAndUploadFolder(folderName, items) {
  const itemEl = document.createElement('div');
  itemEl.className = 'up-item';
  const top = document.createElement('div');
  top.className = 'up-top';
  const nameSpan = document.createElement('span');
  nameSpan.className = 'up-name';
  nameSpan.textContent = '📦 ' + folderName + '.zip (' + items.length + ' файлов)';
  const pctSpan = document.createElement('span');
  pctSpan.className = 'up-pct';
  pctSpan.textContent = 'Подготовка...';
  top.append(nameSpan, pctSpan);

  const barBg = document.createElement('div');
  barBg.className = 'bar-bg';
  const barFill = document.createElement('div');
  barFill.className = 'bar-fill';
  barFill.style.width = '10%';
  barBg.append(barFill);
  itemEl.append(top, barBg);
  uploadsEl.prepend(itemEl);

  try {
    const zipBlob = await createZipBlob(items, (curr, total) => {
      const p = Math.round((curr / total) * 35);
      barFill.style.width = (10 + p) + '%';
      pctSpan.textContent = 'Архивация ' + curr + '/' + total;
    });

    pctSpan.textContent = 'Отправка...';
    await uploadFileWithProgress(zipBlob, folderName + '.zip', itemEl, barFill, pctSpan);
  } catch (err) {
    barFill.style.background = 'var(--danger)';
    pctSpan.textContent = 'Ошибка';
    pctSpan.style.color = 'var(--danger)';
    console.error(err);
  }
}

async function readAllDirectoryFiles(dirEntry, prefix = '') {
  const reader = dirEntry.createReader();
  const result = [];
  const readBatch = () => new Promise((resolve, reject) => {
    reader.readEntries(resolve, reject);
  });
  while (true) {
    const batch = await readBatch();
    if (!batch || batch.length === 0) break;
    for (const child of batch) {
      if (child.isFile) {
        const file = await new Promise((res, rej) => child.file(res, rej));
        result.push({ path: prefix + child.name, file });
      } else if (child.isDirectory) {
        const sub = await readAllDirectoryFiles(child, prefix + child.name + '/');
        result.push(...sub);
      }
    }
  }
  return result;
}

async function handleFileList(list) {
  for (const file of list) {
    await uploadFile(file);
  }
}

picker.addEventListener('change', e => {
  handleFileList(e.target.files);
  e.target.value = '';
});

folderPicker.addEventListener('change', async e => {
  const files = Array.from(e.target.files);
  e.target.value = '';
  if (!files.length) return;
  const folderMap = new Map();
  for (const f of files) {
    const rel = f.webkitRelativePath || f.name;
    const parts = rel.split('/');
    const rootName = parts[0] || 'folder';
    if (!folderMap.has(rootName)) folderMap.set(rootName, []);
    folderMap.get(rootName).push({ path: rel, file: f });
  }
  for (const [folderName, items] of folderMap) {
    await packageAndUploadFolder(folderName, items);
  }
});

dropzone.addEventListener('dragover', e => {
  e.preventDefault();
  dropzone.classList.add('dragover');
});

dropzone.addEventListener('dragleave', () => dropzone.classList.remove('dragover'));

dropzone.addEventListener('drop', async e => {
  e.preventDefault();
  dropzone.classList.remove('dragover');
  const items = e.dataTransfer.items;
  if (!items || !items.length) {
    if (e.dataTransfer.files.length) handleFileList(e.dataTransfer.files);
    return;
  }

  const entries = [];
  for (let i = 0; i < items.length; i++) {
    const entry = items[i].webkitGetAsEntry ? items[i].webkitGetAsEntry() : null;
    if (entry) entries.push(entry);
  }

  if (!entries.length) {
    handleFileList(e.dataTransfer.files);
    return;
  }

  for (const entry of entries) {
    if (entry.isDirectory) {
      const files = await readAllDirectoryFiles(entry, entry.name + '/');
      if (files.length) {
        await packageAndUploadFolder(entry.name, files);
      }
    } else if (entry.isFile) {
      const file = await new Promise((res, rej) => entry.file(res, rej));
      await uploadFile(file);
    }
  }
});

async function sendNote() {
  const box = document.getElementById('text');
  const status = document.getElementById('textStatus');
  if (!box.value.trim()) return;
  status.textContent = 'Сохранение...';
  status.className = 'status-msg';
  try {
    const r = await fetch('/api/text', { method: 'POST', body: box.value });
    if (!r.ok) throw Error();
    box.value = '';
    status.textContent = 'Сохранено!';
    status.className = 'status-msg success';
    setTimeout(() => {
      status.textContent = '';
      status.className = 'status-msg';
    }, 2500);
    refresh();
  } catch {
    status.textContent = 'Ошибка сохранения';
    status.className = 'status-msg error';
  }
}

document.getElementById('sendText').addEventListener('click', sendNote);

document.getElementById('text').addEventListener('keydown', e => {
  if ((e.ctrlKey || e.metaKey) && e.key === 'Enter') {
    e.preventDefault();
    sendNote();
  }
});

refresh();
</script>
</body>
</html>`