package main

import (
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
<title>LAN Drop — {{SHARE_DIR}}</title>
<style>
:root { --bg: #0b0f19; --card: #131b2e; --border: #23304d; --accent: #3b82f6; --accent-hover: #2563eb; --text: #f1f5f9; --muted: #94a3b8; --success: #10b981; --danger: #ef4444; }
* { box-sizing: border-box; }
body { margin: 0; background: var(--bg); color: var(--text); font: 15px/1.5 system-ui, -apple-system, sans-serif; }
main { max-width: 860px; margin: 28px auto; padding: 0 16px; }
header { display: flex; justify-content: space-between; align-items: baseline; flex-wrap: wrap; margin-bottom: 20px; gap: 8px; }
h1 { font-size: 26px; margin: 0; font-weight: 700; letter-spacing: -0.5px; }
.path { font-family: monospace; background: #1e293b; color: #38bdf8; padding: 4px 10px; border-radius: 8px; font-size: 13px; word-break: break-all; }
.grid { display: grid; grid-template-columns: 1fr 1fr; gap: 16px; margin-bottom: 16px; }
@media(max-width: 680px) { .grid { grid-template-columns: 1fr; } }
.card { background: var(--card); border: 1px solid var(--border); border-radius: 16px; padding: 20px; box-shadow: 0 10px 30px rgba(0,0,0,0.25); }
h2 { font-size: 17px; margin: 0 0 12px; font-weight: 600; }
.dropzone { border: 2px dashed #334155; border-radius: 12px; padding: 24px 16px; text-align: center; cursor: pointer; transition: 0.2s; display: flex; flex-direction: column; align-items: center; justify-content: center; min-height: 155px; }
.dropzone.dragover, .dropzone:hover { border-color: var(--accent); background: rgba(59,130,246,0.06); }
.drop-title { font-weight: 600; color: var(--accent); margin-bottom: 4px; font-size: 16px; }
.muted { color: var(--muted); font-size: 13px; }
.picker-btns { display: flex; gap: 10px; margin-top: 14px; flex-wrap: wrap; justify-content: center; }
.btn-picker { background: #1e293b; color: var(--text); border: 1px solid var(--border); padding: 7px 14px; border-radius: 8px; font-size: 13px; font-weight: 600; cursor: pointer; transition: 0.15s; user-select: none; }
.btn-picker:hover { background: var(--accent); color: white; border-color: var(--accent); }
textarea { width: 100%; height: 104px; background: #0f1626; color: var(--text); border: 1px solid var(--border); border-radius: 10px; padding: 10px 12px; font: inherit; resize: none; outline: none; }
textarea:focus { border-color: var(--accent); }
.btn { background: var(--accent); color: white; border: 0; border-radius: 10px; padding: 10px 18px; font: 600 14px system-ui; cursor: pointer; transition: 0.15s; margin-top: 10px; display: inline-flex; align-items: center; gap: 8px; }
.btn:hover { background: var(--accent-hover); }
.upload-list { margin-top: 12px; display: flex; flex-direction: column; gap: 8px; }
.up-item { background: #0f1626; border: 1px solid var(--border); border-radius: 10px; padding: 8px 12px; font-size: 13px; }
.up-top { display: flex; justify-content: space-between; margin-bottom: 6px; word-break: break-all; gap: 8px; }
.bar-bg { height: 6px; background: #1e293b; border-radius: 3px; overflow: hidden; }
.bar-fill { height: 100%; background: var(--accent); width: 0%; transition: width 0.1s; }
.files-head { display: flex; justify-content: space-between; align-items: center; margin-bottom: 8px; }
.row { display: flex; justify-content: space-between; align-items: center; gap: 12px; padding: 12px 0; border-top: 1px solid var(--border); }
.row:first-child { border-top: 0; }
.fname { color: var(--text); text-decoration: none; font-weight: 500; word-break: break-all; }
.fname:hover { color: var(--accent); }
.actions { display: flex; align-items: center; gap: 12px; flex-shrink: 0; }
.dl-btn { background: #1e293b; color: var(--text); text-decoration: none; padding: 6px 12px; border-radius: 8px; font-size: 13px; font-weight: 500; transition: 0.15s; }
.dl-btn:hover { background: var(--accent); color: white; }
</style>
</head>
<body>
<main>
  <header>
    <h1>Быстрый обмен</h1>
    <span class="path">{{SHARE_DIR}} · автоудаление через 1ч</span>
  </header>

  <div class="grid">
    <section class="card">
      <h2>Отправить файлы или папки</h2>
      <div class="dropzone" id="dropzone">
        <span class="drop-title">Перетащите файлы или папки сюда</span>
        <span class="muted">Папки автоматически пакуются в .zip · Потоковая загрузка</span>
        <div class="picker-btns">
          <label class="btn-picker" for="picker">Выбрать файлы</label>
          <label class="btn-picker" for="folderPicker">Выбрать папку</label>
        </div>
        <input id="picker" type="file" multiple style="display:none">
        <input id="folderPicker" type="file" webkitdirectory directory multiple style="display:none">
      </div>
      <div class="upload-list" id="uploads"></div>
    </section>

    <section class="card">
      <h2>Отправить текст / ссылку</h2>
      <textarea id="text" placeholder="Вставьте текст или заметку..."></textarea>
      <div style="display:flex;align-items:center;justify-content:space-between">
        <button class="btn" id="sendText">Сохранить .txt</button>
        <span class="muted" id="textStatus"></span>
      </div>
    </section>
  </div>

  <section class="card">
    <div class="files-head">
      <h2 style="margin:0">Файлы в {{SHARE_DIR}}</h2>
      <button class="btn" style="margin:0;padding:6px 12px;font-size:13px" onclick="refresh()">Обновить</button>
    </div>
    <div id="files"><div class="muted" style="padding:12px 0">Загрузка...</div></div>
  </section>
</main>

<script>
const filesEl = document.getElementById('files');
const uploadsEl = document.getElementById('uploads');
const dropzone = document.getElementById('dropzone');
const picker = document.getElementById('picker');
const folderPicker = document.getElementById('folderPicker');

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

async function refresh() {
  try {
    const r = await fetch('/api/files');
    if (!r.ok) throw Error();
    const data = await r.json();
    filesEl.replaceChildren();
    if (!data || !data.length) {
      filesEl.innerHTML = '<div class="muted" style="padding:12px 0">Папка пуста.</div>';
      return;
    }
    for (const f of data) {
      const row = document.createElement('div');
      row.className = 'row';
      const left = document.createElement('div');
      const a = document.createElement('a');
      a.className = 'fname';
      a.href = '/download/' + encodeURIComponent(f.name);
      a.target = '_blank';
      a.textContent = f.name;
      const meta = document.createElement('div');
      meta.className = 'muted';
      meta.textContent = f.size + ' · ' + f.time;
      left.append(a, meta);

      const actions = document.createElement('div');
      actions.className = 'actions';
      const dl = document.createElement('a');
      dl.className = 'dl-btn';
      dl.href = '/download/' + encodeURIComponent(f.name) + '?dl=1';
      dl.textContent = 'Скачать';
      actions.append(dl);

      row.append(left, actions);
      filesEl.append(row);
    }
  } catch {
    filesEl.innerHTML = '<div class="muted" style="padding:12px 0;color:var(--danger)">Ошибка загрузки списка.</div>';
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
        refresh();
      } else {
        barFill.style.background = 'var(--danger)';
        pctSpan.textContent = 'Ошибка';
      }
      resolve();
    };
    xhr.onerror = () => {
      barFill.style.background = 'var(--danger)';
      pctSpan.textContent = 'Сбой сети';
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
  nameSpan.textContent = file.name;
  const pctSpan = document.createElement('span');
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
  nameSpan.textContent = '📁 ' + folderName + '.zip (' + items.length + ' файлов)';
  const pctSpan = document.createElement('span');
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

dropzone.addEventListener('dragover', e => { e.preventDefault(); dropzone.classList.add('dragover'); });
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

document.getElementById('sendText').addEventListener('click', async () => {
  const box = document.getElementById('text');
  const status = document.getElementById('textStatus');
  if (!box.value.trim()) return;
  status.textContent = 'Сохранение...';
  try {
    const r = await fetch('/api/text', { method: 'POST', body: box.value });
    if (!r.ok) throw Error();
    box.value = '';
    status.textContent = 'Сохранено!';
    setTimeout(() => status.textContent = '', 2500);
    refresh();
  } catch {
    status.textContent = 'Ошибка';
  }
});

refresh();
</script>
</body>
</html>`