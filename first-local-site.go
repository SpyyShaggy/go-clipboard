package main

import (
	"encoding/json"
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

const (
	Port     = ":8000"
	ShareDir = "/mnt/game/site"
)

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

func main() {
	if err := os.MkdirAll(ShareDir, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "Ошибка создания директории %s: %v\n", ShareDir, err)
		os.Exit(1)
	}

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(pageHTML))
	})
	http.HandleFunc("/api/files", handleFiles)
	http.HandleFunc("/api/upload/", handleUpload)
	http.HandleFunc("/api/text", handleText)
	http.HandleFunc("/download/", handleDownload)

	fmt.Printf("Сервер запущен. Папка: %s\nОткройте в браузере:\n", ShareDir)
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
<title>LAN Drop — /mnt/game/site</title>
<style>
:root { --bg: #0b0f19; --card: #131b2e; --border: #23304d; --accent: #3b82f6; --accent-hover: #2563eb; --text: #f1f5f9; --muted: #94a3b8; --success: #10b981; --danger: #ef4444; }
* { box-sizing: border-box; }
body { margin: 0; background: var(--bg); color: var(--text); font: 15px/1.5 system-ui, -apple-system, sans-serif; }
main { max-width: 860px; margin: 28px auto; padding: 0 16px; }
header { display: flex; justify-content: space-between; align-items: baseline; flex-wrap: wrap; margin-bottom: 20px; gap: 8px; }
h1 { font-size: 26px; margin: 0; font-weight: 700; letter-spacing: -0.5px; }
.path { font-family: monospace; background: #1e293b; color: #38bdf8; padding: 4px 10px; border-radius: 8px; font-size: 13px; }
.grid { display: grid; grid-template-columns: 1fr 1fr; gap: 16px; margin-bottom: 16px; }
@media(max-width: 680px) { .grid { grid-template-columns: 1fr; } }
.card { background: var(--card); border: 1px solid var(--border); border-radius: 16px; padding: 20px; box-shadow: 0 10px 30px rgba(0,0,0,0.25); }
h2 { font-size: 17px; margin: 0 0 12px; font-weight: 600; }
.dropzone { border: 2px dashed #334155; border-radius: 12px; padding: 28px 16px; text-align: center; cursor: pointer; transition: 0.2s; display: flex; flex-direction: column; align-items: center; justify-content: center; min-height: 155px; }
.dropzone.dragover, .dropzone:hover { border-color: var(--accent); background: rgba(59,130,246,0.06); }
.dropzone input { display: none; }
.drop-title { font-weight: 600; color: var(--accent); margin-bottom: 4px; font-size: 16px; }
.muted { color: var(--muted); font-size: 13px; }
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
    <span class="path">/mnt/game/site</span>
  </header>

  <div class="grid">
    <section class="card">
      <h2>Отправить файлы, фото, видео</h2>
      <label class="dropzone" id="dropzone">
        <span class="drop-title">Нажмите или перетащите файлы</span>
        <span class="muted">Прямая потоковая запись без лимита размера</span>
        <input id="picker" type="file" multiple>
      </label>
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
      <h2 style="margin:0">Файлы в /mnt/game/site</h2>
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

function uploadFile(file) {
  return new Promise(resolve => {
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

    const xhr = new XMLHttpRequest();
    xhr.open('PUT', '/api/upload/' + encodeURIComponent(file.name));
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
    xhr.send(file);
  });
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

dropzone.addEventListener('dragover', e => { e.preventDefault(); dropzone.classList.add('dragover'); });
dropzone.addEventListener('dragleave', () => dropzone.classList.remove('dragover'));
dropzone.addEventListener('drop', e => {
  e.preventDefault();
  dropzone.classList.remove('dragover');
  if (e.dataTransfer.files.length) handleFileList(e.dataTransfer.files);
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