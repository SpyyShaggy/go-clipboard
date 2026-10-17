# LAN File Share & Clipboard Hub (LAN Drop)

Легковесный, быстрый локальный сервер обмена файлами, медиа и текстом в локальной сети (LAN / Wi-Fi), написанный на Go без сторонних внешних зависимостей.

---

## ⚡ Особенности

- 📦 **Потоковая загрузка (Direct Stream)**: файлы любого размера (видео 4K, архивы, образы) пишутся на диск напрямую чанками без переполнения оперативной памяти.
- 📋 **Быстрый буфер обмена**: отправка текста, ссылок и сниппетов в один клик с автосохранением в `.txt`.
- 🛡 **Безопасность путей**: защита от Path Traversal (`filepath.Base`), санитизация небезопасных символов в названиях файлов, предотвращение коллизий (автоматическое инкрементирование `file (1).ext`).
- 📱 **Адаптивный UI**: встроенный веб-интерфейс в тёмной теме, удобный как на ПК, так и на смартфонах/планшетах.
- 🚀 **Минимализм**: стандартная библиотека Go, нулевые внешние зависимости, компиляция в один самодостаточный бинарник.

---

## 🛠 Требования

- **Go**: версия 1.20+ (протестировано на Go 1.22+ / 1.27+)
- **ОС**: Linux (Arch, CachyOS, Ubuntu, Debian, Fedora и др.), macOS или Windows

---

## 🚀 Восстановление и запуск с нуля

### 1. Клонирование репозитория
```bash
git clone https://github.com/SpyyShaggy/go-clipboard.git
cd go-clipboard
```

### 2. Сборка бинарника
```bash
go build -o lanshare first-local-site.go
chmod +x lanshare
```

### 3. Настройка рабочей папки (опционально)
По умолчанию сервер сохраняет файлы в `/mnt/game/site`. При необходимости вы можете изменить константы `Port` и `ShareDir` в начале файла `first-local-site.go`:
```go
const (
    Port     = ":8000"
    ShareDir = "/mnt/game/site" // или например "./uploads" / "$HOME/Downloads/lan-share"
)
```
После изменения пересоберите бинарник:
```bash
go build -o lanshare first-local-site.go
```

### 4. Ручной запуск
```bash
./lanshare
```
Сервер выведет доступные IP-адреса в вашей локальной сети:
```text
Сервер запущен. Папка: /mnt/game/site
Откройте в браузере:
  http://192.168.1.100:8000/
```
Откройте указанный адрес в любом браузере на телефоне, ноутбуке или ПК в той же локальной сети.

---

## ⚙️ Автозапуск через Systemd (Arch Linux / CachyOS / Manjaro)

Для запуска от имени обычного пользователя (без root-прав) используется пользовательский systemd (`systemd --user`).

### Шаг 1: Создание директории для пользовательских сервисов
```bash
mkdir -p ~/.config/systemd/user
```

### Шаг 2: Установка юнита
Скопируйте готовый файл `lanshare.service` из репозитория:
```bash
cp lanshare.service ~/.config/systemd/user/
```

> **Примечание**: Убедитесь, что пути в `~/.config/systemd/user/lanshare.service` соответствуют вашему расположению папки проекта:
> ```ini
> [Unit]
> Description=LAN File Share Hub
> After=network.target
> 
> [Service]
> ExecStart=/mnt/game/fck/vs-code-projects/another-projects/go-clipboard/lanshare
> WorkingDirectory=/mnt/game/fck/vs-code-projects/another-projects/go-clipboard
> Restart=always
> RestartSec=3
> 
> [Install]
> WantedBy=default.target
> ```

### Шаг 3: Активация и старт
```bash
systemctl --user daemon-reload
systemctl --user enable --now lanshare.service
```

### Полезные команды управления сервисом:
- **Проверить статус**:
  ```bash
  systemctl --user status lanshare.service
  ```
- **Смотреть логи в реальном времени**:
  ```bash
  journalctl --user -u lanshare.service -f
  ```
- **Перезапустить**:
  ```bash
  systemctl --user restart lanshare.service
  ```
- **Остановить**:
  ```bash
  systemctl --user stop lanshare.service
  ```

### Включение работы при выключенном сеансе (Linger):
Чтобы сервер работал в фоне даже если вы вышли из сеанса:
```bash
loginctl enable-linger $USER
```

---

## 🔒 Безопасность

1. **Конфиденциальные данные**: в репозитории отсутствуют токены, пароли, приватные ключи и личные файлы.
2. **Локальная сеть**: сервер предназначен для работы в доверенной локальной сети (домашний Wi-Fi / офисная сеть). Не пробрасывайте порт 8000 в открытый Интернет (WAN) без настройки обратного прокси с авторизацией (Nginx/Caddy с Basic Auth или TLS/VPN).

---

## 📄 Лицензия
MIT License
