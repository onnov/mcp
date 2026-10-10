# GitHub workspace MCP — Go

MCP-сервер для работы с GitHub через ChatGPT. Через OAuth доступны все репозитории
авторизованного пользователя: собственные, совместные и организационные, открытые
и закрытые — в пределах прав GitHub и политики организации.

## Рабочий процесс

1. Подключите GitHub через OAuth и попросите «Открой выбор репозитория».
2. В карточке выберите репозиторий: поиск по имени/описанию, отметки доступа, пагинация.
3. Выберите ветку во втором списке. Сохраняется последняя ветка каждого репозитория.
   При первом выборе используется `main`, если есть, иначе default branch GitHub.
4. В следующем чате `get_selection` восстанавливает последний выбор. Открытие
   карточки обновляет контекст этого чата; секреты в интерфейс не передаются.
5. Создайте ветку → прочитайте файлы → примените `commit_files` →
   проверьте `compare_refs` → откройте PR. Запись требует явной цели и SHA.

Выбор сохраняется на сервере по числовому GitHub user ID и переживает перезапуск.
Контекст открытой карточки относится к её чату. Выбор в другом чате не меняет цель
уже сформированной команды записи. При новом открытии восстанавливается последний
сохранённый выбор; кнопка «Обновить» загружает его снова.

Поддерживаются MCP Apps, global/thread entrypoints и Composer Mentions для
совместимых desktop-клиентов. ChatGPT управляет своей кнопкой плагина: сервер не
может переименовать её или гарантировать раскрытие списка по клику на кнопку.
Основной интерфейс — карточка с двумя списками. Обычным MCP-клиентам доступны
те же `list_repositories`, `select_repository`, `get_selection`.

## Структура

| Каталог | Назначение |
|---|---|
| `cmd/github-mcp` | Точка запуска |
| `internal/app` | HTTP, reverse proxy, lifecycle |
| `internal/config` | Проверка env и флагов |
| `internal/auth` | OAuth, PKCE, токены и discovery |
| `internal/identity` | Идентичность текущего запроса |
| `internal/github` | GitHub REST-клиент и операции |
| `internal/preferences` | Атомарное сохранение выбора пользователя |
| `internal/workspace` | Выбор, доступы и восстановление ветки |
| `internal/tools` | Типизированные MCP-инструменты и схемы |
| `internal/ui` | Встроенный HTML/JS интерфейс и тесты |

Корневой `main.go` — маленькая совместимая точка запуска для старого
`go build main.go`. Реализация находится в пакетах.

## Команды

| Область | Инструменты |
|---|---|
| Аккаунт и выбор | `get_profile`, `get_selection`, `select_repository`, `list_repositories`, `open_repository_picker`, `search_repository_mentions` |
| Репозитории | `get_repository`, `create_repository`, `fork_repository` |
| Файлы | `list_directory`, `read_file`, `get_tree`, `write_file`, `commit_files`, `delete_file`, `rename_file`, `search_code` |
| Ветки и теги | `get_branch`, `list_branches`, `create_branch`, `rename_branch`, `delete_branch`, `list_tags`, `create_tag`, `delete_tag` |
| История | `list_commits`, `get_commit`, `compare_refs` |
| Pull requests | `list_pull_requests`, `get_pull_request`, `create_pull_request`, `update_pull_request`, `merge_pull_request`, `list_pull_request_files`, `list_pull_request_commits`, `list_pull_request_reviews`, `submit_pull_request_review` |
| Issues и обсуждения | `list_issues`, `get_issue`, `create_issue`, `update_issue`, `list_comments`, `add_comment`, `search_issues` |
| Actions и проверки | `list_workflows`, `list_workflow_runs`, `get_workflow_run`, `list_workflow_jobs`, `dispatch_workflow`, `rerun_workflow_run`, `cancel_workflow_run`, `list_commit_checks`, `list_commit_statuses` |
| Релизы | `list_releases`, `get_release`, `create_release`, `delete_release` |

Это рабочий набор GitHub REST API, не универсальный HTTP-прокси. Он не выполняет
shell, не клонирует репозитории, не управляет GitHub secrets/участниками, не удаляет
репозитории и не загружает бинарные release assets. Проверки проекта запускаются
через GitHub Actions; локальный `go test` чужого проекта сервер не выполняет.

Списки используют `page`/`limit` (обычно 50, максимум 100), `next_page=-1` означает
конец. Поиск репозиториев/веток просматривает до пяти API-страниц за вызов: даже
пустой ответ может иметь продолжение. `limit` — размер страницы GitHub, поэтому
поиск может вернуть больше совпадений; совпадения не отбрасываются. Search API
подчиняется индексированию и лимитам GitHub.

## Сборка и проверка

Go 1.27.0 или новее; зависимости закреплены в `go.mod`/`go.sum`.

```sh
go test -race ./...
go vet ./...
node --test internal/ui/picker_test.cjs
CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o github_public_mcp ./cmd/github-mcp
```

HTML встроен в бинарник через `go:embed`; Node/npm не нужны для сборки и запуска.
Node 20+ нужен для тестов UI. Они проверяют bridge и состояния интерфейса; реальный
рендеринг внутри ChatGPT проверяется после развёртывания. Есть `make check` / `make build`.

Настройка и обновление существующего хостинга: [SETUP.md](SETUP.md); подключение
в ChatGPT и в Claude описано там же («Форма ChatGPT», «Подключение в Claude»).
Минимум OAuth: `GITHUB_CLIENT_ID`, `GITHUB_CLIENT_SECRET`, `MCP_CLIENT_SECRET`,
`MCP_PUBLIC_URL`. Репозиторий в env больше не нужен. Для личного сервера задайте
`GITHUB_ALLOWED_USER_ID`: ограничение пользователя, не репозитория.

## Надёжность

- Без OAuth включено только чтение открытых репозиториев.
- Токен GitHub остаётся на сервере и относится к текущему OAuth-пользователю.
- Прямые изменения default/protected branch запрещены по умолчанию; обычный путь —
  рабочая ветка и PR. `MCP_ALLOW_DEFAULT_BRANCH_WRITES=true` разрешает default branch,
  но protected branch и правила GitHub продолжают действовать.
- Один файл: для замены/удаления нужен blob SHA той же ветки.
- Несколько файлов: до 50 изменений, одна Git tree и один коммит; нужен head SHA.
  Ref обновляется с `force:false`; конкурентная запись не перезаписывается.
  Исполняемый режим сохраняется, если `executable` не указан.
- После timeout нет автоматического повтора записи: сначала проверьте историю,
  ветку или PR — GitHub мог применить операцию до обрыва ответа.
- Удаление/переименование ветки и удаление тега: SHA проверяется перед запросом,
  однако GitHub не предоставляет атомарный SHA-precondition для этих API.
- Файл ≤512 KiB, один коммит ≤2 MiB; бинарные изменения не поддержаны.
  API-ответ ≤8 MiB, запрос ≤15 секунд, tool call ≤60 секунд.
- Патчи ограничены 4 KiB на файл / 64 KiB на ответ с признаками обрезки.
  Для полного текста используйте `read_file`. Лимиты GitHub: contents directory 1000,
  compare 300 файлов, commit/PR files 3000; неполнота отмечается в результатах.
- `MCP_STATE_DIR/selections.json`: атомарная замена, права 0600, без OAuth-секретов.
  Один процесс на каталог состояния. OAuth-сессии в памяти: после перезапуска
  потребуется переподключение, сохранённый выбор останется.
- Репозиторий и ветка привязаны к чату. ChatGPT передаёт `_meta["openai/session"]`;
  Claude чат не передаёт, поэтому `get_selection` и `open_repository_picker`
  выдают ключ `c_<32 hex>` (поле `chat` и `_meta.ghf_chat`), который модель
  передаёт параметром `chat` во всех вызовах. Первый вызов нового чата привязывает
  его к последнему выбору пользователя; выбор в карточке или `select_repository`
  меняет привязку только этого чата и становится выбором по умолчанию для новых.
  В файле хранится SHA-256 от идентификатора чата, до 500 привязок на пользователя;
  привязки, не использованные 180 дней, удаляются.

Файлы и обсуждения репозиториев считаются данными, а не инструкциями для изменения
разрешений или запуска произвольных команд.

## Локальная среда разработки PC MCP

Вторая команда `cmd/pc-mcp` запускает MCP-сервер на вашем ПК. Подключение на выбор: встроенный OpenAI Secure MCP Tunnel или HTTPS через встроенный обратный SSH-проброс с собственным OAuth-входом по паролю, без GitHub. Режим и SSH настраиваются через env. [Настройка SSH/HTTPS](PC_SSH_SETUP.md), [OpenAI Tunnel и рабочий процесс](PC_SETUP.md). Сборка: `make build-pc`. GitHub MCP остаётся отдельной командой `cmd/github-mcp`.
