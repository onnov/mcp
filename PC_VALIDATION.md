# PC MCP validation — 2026-10-06

## 2026-10-07: фактическая регрессия approval output

На реальной карточке 1.1.4 подтверждены `succeeded/exit_code=0`, но внутри UI:
`Не удалось загрузить вывод: Unknown tool`. Server output содержит 3 records/82 bytes.
Карточка публикует прежний context `via pc_job_output`, тогда как исходники и
встроенные ресурсы 1.1.4 уже содержат статусный records path. Это старый HTML
и прежний каталог подключения; детали и пределы доказательства:
[PC_OUTPUT_REGRESSION.md](PC_OUTPUT_REGRESSION.md).

Подготовлена версия 1.1.5/schema 5, approval-v8/workspace-v5.
Два новых диагностических теста падали до изменения URI/bridge и прошли после.
`make check-pc` и `git diff --check` прошли: Go race tests, 26 UI tests, vet, gofmt.
Успешный harness не является подтверждением исправления в реальной ChatGPT
карточке. Host restart, обновление metadata подключения и новая визуальная
проверка остаются обязательными перед push/PR.

## Проверка текущей ветки

Ветка `mcp/pc-ssh-proxy` проверена на текущем PC MCP после загрузки Go-зависимостей.

Подтверждено:
- `make check-pc`: все Go-тесты `internal/pc/...` под `-race`, UI-тесты и `go vet` проходят.
- `make test-pc-sandbox`: namespace/filesystem/FD isolation и остановка descendants проходят.
- `make test-pc-network`: сетевой bridge внутри sandbox проходит.
- `make build-pc`: бинарник `pc-mcp` собирается.
- Transport tests: HTTP OAuth, OpenAI Tunnel, reverse SSH с reconnect, host-key verification и проверкой прав ключа проходят.
- `stdio` smoke: собранный сервер отвечает на MCP `initialize` по newline-delimited JSON и согласует protocol version `2025-11-25`.
- Owner auth: `password` и `client-secret`/PKCE сценарии проходят.
- Resource modes: `strict` fail-closed и `auto` fallback/warning проходят unit/integration tests.
- UI подтверждения в harness: после разрешения запуска действия немедленно блокируются; затем отображаются running/terminal status, exit code, duration и вывод из `pc_job_status.output.records`. Реальная карточка со старым HTML показала `Unknown tool`; deploy gate описан выше.
- `.gitignore`: локальные env/secrets, SSH key/known_hosts, launcher, password/hash file, IDE metadata, binaries, logs, profiles и coverage output игнорируются; tracked/untracked secret-pattern scan не выявил реальных секретов.
- `git diff --check` проходит.

Ограничение среды: `make test-pc-resources` нельзя достоверно выполнить внутри текущего job namespace, потому что здесь нет доступного пользовательского systemd bus/delegated cgroup scope. Тест ожидаемо завершается ошибкой `Failed to connect to bus: No medium found`. Реальные cgroup RAM/PID ceilings и `cgroup.kill` необходимо проверять с хоста через отдельный delegated user scope:

```bash
make test-pc-resources
```

Это ограничение среды проверки, а не успешный результат и не скрытый skip.

## UI подтверждения и логов

Карточка подтверждения теперь использует отдельные состояния:
1. команда подготовлена и ещё не запущена;
2. подтверждение принято, запуск выполняется;
3. команда выполняется/в очереди;
4. terminal state: успешно, ошибка, отмена или timeout.

После клика «Разрешить запуск» кнопка подтверждения немедленно исчезает, а stop-кнопки появляются только после получения running/queued state. Повторный запуск из той же карточки невозможен.

Для консоли `pc_job_status` теперь самодостаточен: `output.records` постранично передаёт retained output, `records_cursor/more` продолжают чтение после terminal state, а `evicted` сообщает о потерянных записях. Карточка показывает sequence number, stdout/stderr, exit code, время выполнения и число записей/байт. `pc_job_output` остаётся необязательным отдельным API; отсутствие этого tool больше не ломает отображение результата. Для старых серверов сохраняется fallback на compact `head/tail`.

## Рекомендуемая host-side проверка перед deployment

```bash
make deps-pc
make check-pc
make test-pc-sandbox
make test-pc-network
make test-pc-resources
make build-pc
```

После обновления бинарника/перезапуска PC MCP нужно обновить подключение плагина, потому что URI карточки подтверждения изменён на новую версию и старый descriptor может быть закеширован клиентом.
