# PC MCP через HTTPS и SSH

Одна команда запускает MCP на ПК и обратный SSH-проброс на ваш сервер.
OpenAI Tunnel и GitHub для входа не используются. Плагин подключается по
HTTPS с OAuth. Режим `client-secret` позволяет подключаться без ввода пароля
владельца; режим `password` сохраняет страницу входа.

## 1. Собрать на ПК

```bash
git fetch origin
git switch mcp/pc-ssh-proxy
make deps-pc install-pc
```

Нужен Go >=1.27. Для выполнения команд на Ubuntu по-прежнему нужны
bubblewrap >=0.12 и util-linux. cgroup v2/systemd delegation дают RAM/PID limits;
обычный запуск в режиме auto допускается без них. Проверка: `make test-pc-sandbox` и resource integration из [PC_SETUP.md](PC_SETUP.md).

## 2. Один раз настроить сервер

Нужны домен с HTTPS и SSH-доступ с разрешённым **remote port forwarding**.
Apache должен проксировать **весь сайт**, включая `/mcp`, `/oauth/*` и
`/.well-known/*`, на `127.0.0.1:18182` того же сервера.

Для вашего варианта с `.htaccess`:

```apache
RewriteEngine On
RewriteRule ^(.*)$ http://127.0.0.1:18182/$1 [P,L]
```

Либо в готовом HTTPS VirtualHost Apache:

```apache
ProxyRequests Off
ProxyPreserveHost On
ProxyPass / http://127.0.0.1:18182/ connectiontimeout=5 timeout=100
ProxyPassReverse / http://127.0.0.1:18182/
```

HTTPS-сертификат и домен настраиваются на сервере, не на ПК. Используйте
отдельный поддомен для PC MCP, чтобы не менять работающий GitHub MCP.
Не удаляйте заголовки `Authorization` и `WWW-Authenticate` при проксировании.

Для SSH-сервера нужен `GatewayPorts no`: порт `18182` должен слушать только
localhost. Приложение запрашивает `127.0.0.1:18182`, однако окончательный bind
определяет конфигурация sshd. Если используете отдельного SSH-пользователя,
администратор может ограничить его `PermitListen 127.0.0.1:18182` и
`AllowTcpForwarding remote`. Shell, SSH agent и удалённые команды приложение
не использует. На обычном хостинге выясните, разрешён ли `ssh -R`.

## 3. Задать env на ПК

Используйте существующий `start_pc_mcp.sh` или env-файл по примеру
[.pc-mcp.ssh.env.example](.pc-mcp.ssh.env.example). Настройки и секреты могут
оставаться в рабочем каталоге. Внешний private env-файл — рекомендация для
дополнительной защиты, см. [PC_SECURITY.md](PC_SECURITY.md).
Укажите:

| Переменная | Значение |
| --- | --- |
| `PC_MCP_TRANSPORT` | `ssh` |
| `PC_MCP_ROOT` | Существующий каталог проектов, например `/home/you/projects` |
| `PC_MCP_PUBLIC_URL` | HTTPS-домен без `/mcp`, например `https://pc-mcp.example.com` |
| `PC_MCP_SSH_ADDR` | SSH-хост и порт, например `server.example.com:22` |
| `PC_MCP_SSH_USER` | SSH-пользователь |
| `PC_MCP_SSH_KEY_FILE` | Абсолютный путь к приватному SSH-ключу |
| `PC_MCP_SSH_KNOWN_HOSTS` | Абсолютный путь к проверенному `known_hosts` |
| `PC_MCP_OWNER_AUTH` | `password` (default) или `client-secret` без ввода пароля |
| `PC_MCP_OWNER_PASSWORD_HASH` | Хеш пароля, требуется только в режиме `password` |
| `PC_MCP_OAUTH_CLIENT_SECRET` | Случайная строка из команды ниже |

Для режима `password` создайте хеш пароля. Бинарник спросит пароль без отображения ввода:

```bash
"$HOME/.local/bin/pc-mcp" --hash-password
```

Пароль — 16..72 байта. Полученный bcrypt-хеш вставьте в env **в одинарных
кавычках**, чтобы оболочка не подставляла значения `$...`.

Создайте отдельный секрет OAuth-клиента:

```bash
openssl rand -hex 32
```

Этот секрет вставляется в env и в форму ChatGPT. Пароль владельца в форму
создания плагина не вставляется: его вводите только при OAuth-входе.

Ключ SSH должен иметь права `0600` или строже. Ключ и `known_hosts` могут лежать в проектах; вне проектов — рекомендуемый
вариант. Job cache и sandbox SDK не подходят для SSH-конфигурации сервера.
Для зашифрованного ключа задайте `PC_MCP_SSH_KEY_PASSPHRASE`.
Проверьте fingerprint SSH-хоста по доверенному источнику и сохраните его
в `known_hosts` до запуска. Автоматического доверия новому ключу хоста нет.

Для исходящего SSH через ваш SOCKS5 добавьте:

```bash
PC_MCP_SOCKS5_PROXY='socks5h://127.0.0.1:1084'
```

## 4. Запустить

```bash
go build -o pc-mcp ./cmd/pc-mcp
./start_pc_mcp.sh
```

После `SSH forwarding connected` проверьте в браузере
`https://ВАШ-ДОМЕН/healthz`: ответ должен быть `ok`.
Обрыв SSH восстанавливается автоматически. При остановке приложения
проброс закрывается. Пока ПК выключен или отключён, плагин недоступен.

## 5. Создать плагин в ChatGPT

- Подключение: **URL сервера**, `https://ВАШ-ДОМЕН/mcp`.
- Аутентификация: **OAuth**.
- Client ID: `pc-mcp-chatgpt`.
- Client Secret: значение `PC_MCP_OAUTH_CLIENT_SECRET`.
- Метод token endpoint: **client_secret_basic** (поддерживается и `client_secret_post`).
- Scope: `pc`.
- Конечные точки определяются автоматически по серверу. Регистрация/DCR и OIDC
  не используются: Client ID и Client Secret заданы явно.
- Если форма показывает другой callback, скопируйте **точный** URL в
  `PC_MCP_OAUTH_REDIRECT_URI` и перезапустите сервер.

В режиме `password` откроется ваша страница входа: введите пароль владельца и
разрешите доступ. В режиме `client-secret` дополнительная форма пароля не
показывается: клиент обменивает code с уникальным секретом и PKCE. Используйте
этот режим только для личного, нераспространяемого клиента. Обладатель client
secret получает все права владельца. Доступ действует до 7 дней, access token — до часа с
обновлением. Подключения сохраняются на диске и переживают перезапуск сервера:

```
$PC_MCP_STATE/oauth/
├── chatgpt/<id>.json   # подключения из ChatGPT
└── claude/<id>.json    # подключения из Claude
```

В файлах только SHA-256 хеши токенов, сами токены на диск не попадают.
Чтобы разлогинить клиента, удалите его каталог (`rm -r $PC_MCP_STATE/oauth/claude`)
или весь `oauth/` — доступ отзывается сразу, перезапуск не нужен. Смена пароля
сама по себе прежние подключения не отзывает: после неё удалите `oauth/`.
`PC_MCP_OAUTH_PERSIST=false` возвращает прежнее поведение (только память,
перезапуск отзывает все токены).
OAuth выдаёт доступ одному владельцу ко всему настроенному дереву проектов;
разделения прав между несколькими пользователями нет.

В чате напишите **«Покажи доступные каталоги ПК»**: появится карточка выбора.
Открывайте вложенные папки кликом, возвращайтесь по пути сверху, затем нажмите
«Выбрать» и подтвердите каталог с веткой. Последний выбор сохраняется.
После обновления сервера обновите подключение плагина в ChatGPT для загрузки
новых инструментов и UI; авторизация после перезапуска сохраняется.

## 6. Подключить в Claude (claude.ai, Desktop, мобильное приложение)

Тот же сервер, тот же Client ID и Client Secret работают одновременно с ChatGPT.
Callback'и Claude (`https://claude.ai/api/mcp/auth_callback` и
`https://claude.com/api/mcp/auth_callback`) разрешены сервером всегда, отдельно
настраивать их не нужно. OpenAI Tunnel в Claude не работает: нужен режим `ssh`
или `http` с публичным HTTPS.

- **Настройки → Коннекторы → Добавить свой коннектор**.
- URL: `https://ВАШ-ДОМЕН/mcp` — точно так же, как в ChatGPT, с `/mcp` и без
  `/` в конце, домен в нижнем регистре.
- Аутентификация: **Use your own OAuth client** (в старом интерфейсе —
  **Advanced settings**).
- OAuth Client ID: значение `PC_MCP_OAUTH_CLIENT_ID` (по умолчанию `pc-mcp-chatgpt`).
- OAuth Client Secret: значение `PC_MCP_OAUTH_CLIENT_SECRET`.

DCR и CIMD сервер не поддерживает, поэтому без Client ID Claude подключиться не сможет.
Настройки аутентификации коннектора в Claude менять нельзя: при ошибке удалите
коннектор и добавьте заново. Если вход прерывается JSON с `invalid_request`,
поле `error_description` называет параметр, который отклонён.

## Диагностика запросов клиента

Чтобы посмотреть, какие идентификаторы (чат, сессия) передаёт ChatGPT или
Claude, запустите сервер с `PC_MCP_DEBUG_REQUESTS=true`. Каждый
авторизованный запрос к `/mcp` дописывается строкой JSON в
`$PC_MCP_STATE/debug/mcp-requests.jsonl` (0600, не больше 10 MiB).

- Пишутся: клиент (`chatgpt`/`claude`), заголовки HTTP, метод MCP, имя
  инструмента, **имена** аргументов, структура `_meta`, `clientInfo`.
- Значения, которые могут быть идентификаторами, заменены метками
  `{"tag","len","shape"}`: одинаковое значение — одинаковый `tag` в пределах
  одного запуска сервера. `Authorization` и `Cookie` скрыты полностью.
  Значения аргументов, содержимое файлов и команды не пишутся.
- Не перезапускайте сервер между сравниваемыми чатами: ключ меток новый при
  каждом запуске.

Файлом можно поделиться. После проверки выключите опцию и удалите `debug/`.

## Другие режимы

- **OpenAI Tunnel:** `PC_MCP_TRANSPORT=tunnel`, прежние `CONTROL_PLANE_*`.
  В плагине — Tunnel, без дополнительной аутентификации.
- **Готовый reverse proxy без встроенного SSH:** `PC_MCP_TRANSPORT=http`.
  Нужны те же OAuth/env, кроме `PC_MCP_SSH_*`; проксируйте на локальный
  `127.0.0.1:8182`. HTTP-сервер всегда слушает только loopback.
- **Локальный клиент:** `PC_MCP_TRANSPORT=stdio`.

Сеть jobs разрешается отдельно: `PC_MCP_ALLOW_NETWORK=true`, затем карточка `network:true`. Proxy env устанавливается сервером; raw host network требует explicit operator opt-in. См. [PC_SETUP.md](PC_SETUP.md).
Разрешения пространства ChatGPT на HTTPS-плагины по-прежнему применяются.

Для публичного endpoint настройте per-IP request/connection limits на Apache/WAF; одного RewriteRule недостаточно. Proxy должен **перезаписывать** X-Forwarded-For actual peer адресом; PC_MCP_TRUSTED_PROXIES содержит только точные CIDR доверенного proxy. При SSH forwarding реальный локальный peer обычно loopback. Application limits не защищают внешний канал от volumetric DDoS.
