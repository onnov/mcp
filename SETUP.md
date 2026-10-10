# Установка и обновление

## Обновление текущего сервера

Новая ветка не меняет работающий хостинг. Перед обновлением проверьте сборку и
сохраните предыдущий бинарник для отката.

```sh
git fetch origin
git switch mcp/architecture-repository-picker
go test -race ./...
go vet ./...
node --test internal/ui/picker_test.cjs
CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o github_public_mcp ./cmd/github-mcp
```

Корневой `main.go` совместим с `go build main.go`. Старый `start_mcp.sh` не
переписывается: проверьте его exports самостоятельно, не публикуйте реальные
секреты. Для новой установки есть `scripts/start.sh` и `.env.example`.

- Удалите `MCP_WRITE_REPOS`: репозитории определяются правами пользователя GitHub.
  Если переменная осталась, сервер сообщит, что она игнорируется.
- Настройте постоянный writable `MCP_STATE_DIR` для сохранённого выбора.
- `MCP_ALLOWED_USERS` необязателен; для личного сервера предпочтителен
  `GITHUB_ALLOWED_USER_ID`, числовой неизменяемый ID аккаунта.
- GitHub OAuth запрашивает `repo workflow`. Заново авторизуйте GitHub после
  обновления для private repos и workflow-файлов. Организации могут требовать
  отдельного одобрения OAuth App.
- MCP scope для ChatGPT остаётся `github`; MCP Client ID/Secret можно сохранить.
  После обновления сервера обновите инструменты подключения в ChatGPT (Refresh)
  и переподключитесь для новой OAuth-авторизации.

## Две разные пары credentials

| Пара | Источник | Использование |
|---|---|---|
| `GITHUB_CLIENT_ID` / `GITHUB_CLIENT_SECRET` | GitHub OAuth App | Сервер подключает аккаунт GitHub |
| `MCP_CLIENT_ID` / `MCP_CLIENT_SECRET` | Вы задаёте сами | ChatGPT и Claude получают токены MCP-сервера |

Client ID OAuth App не является числовым GitHub user ID. Пользователь входит в
GitHub через браузер; сервер определяет ID через `/user`. GitHub password, PAT
или GitHub client secret не вводятся в форме ChatGPT как MCP credentials.

GitHub OAuth App:

- Homepage URL: `https://mcp-msk.v02.ru`
- Authorization callback: `https://mcp-msk.v02.ru/oauth/github/callback`

## Окружение

Скопируйте `.env.example` в локальный `.env`, заполните и установите `chmod 600 .env`.
`.env` исключён из git. Сам сервер dotenv не читает: экспортируйте env в панели
хостинга, shell или используйте `sh scripts/start.sh ./.env`. Скрипт загружает
доверенный shell-файл через `.`; не используйте файлы неизвестного происхождения.

| Переменная | Значение / назначение |
|---|---|
| `MCP_ADDR` | `127.0.0.1:8181` за Apache |
| `MCP_PUBLIC_URL` | `https://mcp-msk.v02.ru`, HTTPS origin без пути |
| `GITHUB_CLIENT_ID` | Client ID GitHub OAuth App |
| `GITHUB_CLIENT_SECRET` | Client secret GitHub OAuth App |
| `MCP_CLIENT_ID` | По умолчанию `ghf-chatgpt` |
| `MCP_CLIENT_SECRET` | Случайный секрет ≥32 символов: `openssl rand -hex 32` |
| `MCP_REDIRECT_URI` | Точный ChatGPT callback; default `https://chatgpt.com/connector_platform_oauth_redirect` |
| `MCP_STATE_DIR` | Default `./data`; лучше постоянный абсолютный путь |
| `GITHUB_ALLOWED_USER_ID` | Необязательный numeric user ID для личной установки |
| `MCP_ALLOWED_USERS` | Необязательные логины через запятую; вместе с ID действуют оба ограничения |
| `MCP_ALLOW_DEFAULT_BRANCH_WRITES` | `false` по умолчанию |
| `MCP_GOMAXPROCS` | `2`; ограничивает параллельное исполнение Go-кода, не число goroutines/OS threads |
| `MCP_PUBLIC_HOST` | Дополнительный допустимый Host; обычно не нужен |

Без обоих GitHub credentials доступны только public read-only tools. Частично
заданные credentials — ошибка запуска. OAuth требует HTTPS origin и MCP secret.
User ID можно получить через `get_profile` после подключения или GitHub `/users/<login>`.

## Форма ChatGPT

Endpoint: `https://mcp-msk.v02.ru/mcp`, authentication OAuth.

| Поле | Значение |
|---|---|
| MCP Client ID | `MCP_CLIENT_ID` |
| MCP Client Secret | `MCP_CLIENT_SECRET` |
| Token endpoint auth method | `client_secret_post` либо `client_secret_basic` |
| Scope по умолчанию | `github` |
| Базовый scope | Можно пустым: единственный scope MCP-сервера — `github` |
| Authorization URL | `https://mcp-msk.v02.ru/oauth/authorize` |
| Token URL | `https://mcp-msk.v02.ru/oauth/token` |
| Authorization server / issuer | `https://mcp-msk.v02.ru` |
| Resource | `https://mcp-msk.v02.ru/mcp` |
| Registration URL | Отсутствует; статический клиент, DCR не реализован |
| OIDC | Выключен; discovery/userinfo/OIDC scopes не нужны |

Discovery публикует URLs и scope. Если форма пытается динамически регистрировать
клиента, выберите режим с заданным Client ID/Secret. Не подставляйте GitHub OAuth
endpoints в эту форму: они относятся к внутреннему шагу подключения GitHub.

## Подключение в Claude

Тот же сервер и та же пара `MCP_CLIENT_ID` / `MCP_CLIENT_SECRET` работают в Claude
(claude.ai, Desktop, мобильные приложения). Отдельной настройки сервера не нужно:
callback'и Claude `https://claude.ai/api/mcp/auth_callback` и
`https://claude.com/api/mcp/auth_callback` разрешены вместе с ChatGPT callback
из `MCP_REDIRECT_URI`. Код авторизации привязан к callback'у своего запроса.

1. Settings → Connectors → Add custom connector.
2. URL: `https://mcp-msk.v02.ru/mcp` (обязательно с `/mcp`).
3. Откройте «Advanced settings» (или «Use your own OAuth client») и введите:
   - OAuth Client ID: значение `MCP_CLIENT_ID`;
   - OAuth Client Secret: значение `MCP_CLIENT_SECRET`.
4. Connect → вход в GitHub → возврат в Claude.

Dynamic Client Registration (DCR) и Client ID Metadata Documents (CIMD) не
поддерживаются: без своих Client ID/Secret Claude не подключится.

При старте сервер печатает в лог `client_id` и список разрешённых callback'ов.
Каждый отказ OAuth возвращается с `error_description` (какой параметр или шаг
не прошёл) и пишется в лог сервера строкой `github-mcp: OAuth ... rejected`.

Claude кеширует список инструментов и HTML карточки. После обновления сервера,
меняющего карточку, удалите коннектор в Claude и добавьте его заново.

## Apache

Ваш reverse proxy сохраняется:

```apache
RewriteEngine On
RewriteRule ^(.*)$ http://127.0.0.1:8181/$1 [P,L]
```

Проксируйте `/mcp`, `/oauth/*`, `/.well-known/*`, `/healthz`; передавайте Authorization,
query string и полный JSON body, не кэшируйте OAuth-ответы. Для длинных операций
timeout прокси должен быть минимум 95 секунд.

`-public-host` не обязателен: `127.0.0.1:8181` допускается автоматически, внешний
домен берётся из `MCP_PUBLIC_URL`. X-Forwarded-Host не является источником доверия.
При anonymous mode и внешнем Host при необходимости задайте `-public-host`.

## Проверка после развёртывания

1. `/healthz` возвращает `ok`.
2. `/.well-known/oauth-protected-resource`: правильный resource `/mcp`.
3. `/.well-known/oauth-authorization-server`: правильные issuer/URLs.
4. Без токена `/mcp` возвращает 401 и `WWW-Authenticate`.
5. Переподключите ChatGPT, войдите нужным GitHub-аккаунтом и обновите tools.
6. Проверьте `get_profile`, `list_repositories`: все доступные public/private/org repos.
7. Откройте `open_repository_picker`, выберите repo/ветку; в новом чате вызовите
   `get_selection`. Другой GitHub-аккаунт должен иметь отдельный выбор.
8. Создайте тестовую ветку, получите `get_branch`, прочитайте файл, примените
   `commit_files`, проверьте diff и создайте draft PR.

### UI

Карточка использует MCP Apps JSON-RPC bridge, встроенный HTML, без CDN/скриптов извне.
Есть поиск, пагинация, private/read/write/protected, клавиатурная навигация, состояния
загрузки и ошибок. Неудачное сохранение оставляет старый выбор. Если сохранение
прошло, но контекст ChatGPT не обновился, карточка предлагает указать repo в сообщении.

Composer Mentions доступен клиентам с desktop extension. Поиск возвращает ссылки
`ghf://repository/owner/repo?branch=...`; разрешение выбранной ссылки проверяет
доступ и сохраняет preference. Сам поиск preference не меняет. Сервер не получает
отдельный callback клика по штатной кнопке плагина. Web-клиент использует карточку.

### Состояние и ошибки

Один экземпляр на каталог состояния. Хранится до 200 repo/branch preferences на
пользователя; старые вытесняются. Повреждённый JSON останавливает запуск вместо
молчаливого сброса. OAuth-секреты в preferences не записываются.

OAuth-сессии находятся в памяти: после перезапуска нужно переподключиться.
MCP token живёт до часа, refresh-пара вращается; GitHub grant ограничен 24 часами
либо более ранним сроком upstream token. Сохранённый выбор остаётся после рестарта.

403: проверьте permissions, OAuth policy организации и rate limit. 401: переподключите
OAuth. SHA conflict: прочитайте текущую ветку/файл. После timeout не повторяйте write
вслепую — GitHub мог уже создать коммит.

## Документация

- [MCP Go SDK](https://github.com/modelcontextprotocol/go-sdk)
- [MCP Apps](https://github.com/modelcontextprotocol/ext-apps)
- [ChatGPT UI](https://developers.openai.com/plugins/build/chatgpt-ui)
- [ChatGPT extensions](https://developers.openai.com/plugins/build/extensions)
- [OAuth](https://developers.openai.com/plugins/build/auth)
- [GitHub REST API](https://docs.github.com/en/rest)
