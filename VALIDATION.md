# Проверка версии 3.0.0

Проверено 2026-10-06, Go 1.26.2 / Node 24.19.0.

| Проверка | Результат |
|---|---|
| `go test -race -p 1 ./...` | Все пакеты проходят, race detector включён |
| `go vet ./...` | Проходит |
| `gofmt -l cmd internal main.go` | Пустой список |
| `node --test internal/ui/picker_test.cjs` | 6/6 сценариев проходят |
| `sh -n scripts/start.sh` | Проходит |
| `git diff --check` | Проходит |
| `CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o github_public_mcp ./cmd/github-mcp` | Собран статический бинарник |
| Локальный запуск бинарника, `/healthz`, MCP `tools/list` | `ok`, 26 read-only tools без OAuth |
| SDK discovery в OAuth-режиме | 57 инструментов; схемы и metadata проверены |

Покрыты: PKCE и повторное использование code/refresh; resource и browser binding;
неизменяемый user ID и ограничение аккаунта; передача identity через stateless HTTP
и MCP; приватные/org repo и продолжение поиска; запрещённые default/protected branch;
SHA conflict до записи и конкурентный push без force/retry; сохранение executable mode;
rename без перезаписи назначения; пагинация файлов коммита и ограничение патчей;
сохранение предпочтений после перезапуска, изоляция пользователей, параллельная запись
и disk failure; UI restore/save/error, сериализация кликов, stale search, безопасный
вывод имён и клавиатура.

GitHub API в тестах моделируется через HTTP transport. Работающий production endpoint
не заменялся, реальные PR/reviews/releases/Actions в рамках проверки не запускались.
Полный OAuth flow и отображение MCP Apps/Composer Mentions в настоящем клиенте
ChatGPT требуют проверки после развёртывания по SETUP.md. UI-тесты используют
модель DOM/host bridge и не являются браузерными визуальными тестами.
