# PC MCP: регрессия вывода approval-card

Проверка 2026-10-07, ветка `mcp/pc-ssh-proxy`.
Подготовленное исправление: server `1.1.5`, schema `5`,
approval `ui://pc-mcp/approve-v8.html`, picker `ui://pc-mcp/workspace-v5.html`.

## Что установлено на реальном подключении

Контрольный job `97149998c174a3df4c923371326a8c99802b8c733024a8ec` завершился
`succeeded`, `exit_code=0`. `pc_job_status` вернул три retained records,
82 байта: два stdout-маркера и один stderr-маркер.

На пользовательском снимке карточки видны зелёный terminal status, заблокированные
кнопки и **«Не удалось загрузить вывод: Unknown tool»** внутри строки статуса.
Карточка прислала model context:

```text
Console output is available in the command card and via pc_job_output.
```

В текущем исходном UI и встроенном HTML бинарника 1.1.4 такой строки уже нет:
они публикуют `Console output is included in pc_job_status.`
Отдельный stdio probe бинарника подтвердил, что ресурсы `approve-v7` и
`approve-v1` содержат `records_cursor` и не вызывают `PC.tool('pc_job_output')`.
Embedded approval HTML совпадает с исходным файлом.

Таким образом, в показанной карточке исполняется прежний HTML/JS.
В каталоге инструментов этого подключения нет `pc_job_output` и
`pc_cancel_all_jobs`; описания `pc_request_run`/`pc_start_job`/`pc_job_status`
тоже остались прежними. Версия работающего сервера не обновляет каталог
инструментов и ресурсный кэш ChatGPT автоматически.

## Конкретное изменение в git

В `daf2681` и его предшественниках console строилась из
`pc_job_status.output.head/tail`. Не было дополнительного tools/call для вывода.

В `cff3e79` появилась функция `drainOutput()`, обязательный
`PC.tool('pc_job_output', ...)` и отдельный OutputPage API.
`poll()` перестал рендерить head/tail и стал ожидать `drainOutput()`.
Для подключения с прежним каталогом это новый неизвестный RPC.
В `11fc677` и `2ad8918` этот output path и decode bridge не изменились.

Это доказанный переход, объясняющий `Unknown tool`: раньше карточка использовала
уже известный status tool, после изменения ей обязательно понадобился новый
output tool. Последующий переход на status.records уже присутствовал в файлах
и бинарнике 1.1.4, но не попал в фактически исполняемую карточку.

Точная строка «Не удалось загрузить вывод» отсутствует в текущих файлах,
проверенной git-истории и бинарнике. На снимке она находится внутри approval UI,
значит её формирует прежний cached UI, а не текущий bridge/output.go.
Полный cached asset и raw JSON-RPC error envelope недоступны; вывод о конкретном
`pc_job_output` RPC основан на старом output path, live context и каталоге.
Не выдаём исторический fixture за байтовую копию iframe пользователя.

## Проверка contracts и polling

- `tools/call` возвращает обычный MCP CallToolResult.
  `PC.tool()` читает `structuredContent`, иначе JSON text content;
  correlated replies не попадают в `onResult`.
- Initial tool-result/globals связывают job один раз. Состояние дальше читается
  через `pc_job_status`.
- Primary cursor — `records_cursor`; legacy `cursor` относится к compact
  head/tail. Они не взаимозаменяемы.
- Terminal state не завершает polling, пока `output.more=true`.
  Existing test дренирует 4205 records, в том числе после succeeded.
- Manager закрывает stdout/stderr writers до установки terminal state;
  финальные неполные строки не появляются после последнего terminal snapshot.
- `ui.visibility=["model","app"]` разрешает app tools по текущему MCP Apps
  контракту. Добавление legacy widgetAccessible к каждому tool не исправляет
  устаревший каталог с отсутствующим именем.

## Исправление и red/green

Не добавлен новый output workaround. Сохранён уже существующий status.records
path. Выданы новые URI `approve-v8` и `workspace-v5`; прежние обслуживаемые URI
остаются aliases. Обновлены server/capabilities/bridge версии до 1.1.5.

Harness исполняет настоящий bridge и поддерживает ресурсный кэш по URI.
Fixture `fixtures/approve-2ad8918.html` — точная историческая HTML-реализация,
без копии/подмены PC API. Тест запускает awaiting_approval → approve → running
→ succeeded → stdout/stderr render с прежним каталогом инструментов.

До изменения URI cache-тест упал с:
`cached HTML attempted tools/call pc_job_output; the old output path prevents polling/render`
(actual=1, expected=0). Новый URI выбирает актуальный HTML и проходит полный
сценарий без pc_job_output. Это воспроизведение класса regression кэша, а не
полная трасса проблемного iframe.

Отдельный red/green тест показывает, что bridge раньше терял имя RPC и error code.
Теперь ошибки PC.tool содержат tools/call, имя tool, correlation ID и сохраняют
JSON-RPC code. Аргументы, nonce и полный result не публикуются.

Для проверки реально загруженного UI context содержит uiVersion, outputSource,
renderedRecords/stdout/stderr, cursor и more. Capabilities содержит approval_uri,
picker_uri и SHA256 встроенного approval HTML. Эти поля служат диагностике;
ни unit tests, ни context счётчики не заменяют визуальную проверку новой карточки.

## Проверки подготовленного бинарника

`make check-pc`, `make build-pc`, `make check-pc-ui-binary` и
`git diff --check` выполнены успешно.

`binary_lifecycle_test.cjs` запускает именно собранный сервер по stdio,
вызывает реальные initialize/tools/list/resources/read/tools/call,
получает HTML из его resources/read и исполняет этот embedded bridge в harness.
Status/approve handlers возвращают реальные MCP envelopes без mock output.
Реальная sandbox-команда печатает безопасные stdout/stderr маркеры.

Подтверждены 1.1.5/schema 5, approve-v8, совпадение SHA256 HTML с capabilities,
awaiting_approval → running → running → succeeded, exit code 0,
рендер всех трёх records и отсутствие pc_job_output.
SHA256 approval HTML:
`1a15e72aaf068247e9a7c69fc5c5d8526fc959284051bebbf319bdaca1f9b27a`.

Проверка бинарника воспроизводится:
```bash
make check-pc-ui-binary
```

Это integration test нового бинарника. Host ChatGPT и его кэш в этом тесте
не участвуют; финальную проверку настоящей ChatGPT карточки он не заменяет.

## Deploy gate

```bash
make check-pc
make build-pc
git diff --check
```

После сборки необходим обычный host restart через существующий launcher
`./start_pc_mcp.sh`, затем обновление metadata/инструментов подключения ChatGPT.
Обычные PC jobs выполняются в отдельном PID/network namespace и не управляют
host сервером, SSH credentials или его launcher session. Запуск второго
launcher внутри такого job не является restart сервера.

После restart `pc_capabilities` должен показать 1.1.5/schema 5,
`approval_uri=ui://pc-mcp/approve-v8.html`.
Новая карточка должна содержать оба потока и uiVersion=1.1.5 в context.
Пока нет этой проверки, deployed fix не считается подтверждённым.
Push/PR не выполняются до подтверждения пользователя.

## Первичные источники

- [MCP Apps: visibility, resources/read/cache, lifecycle, tools/call](https://github.com/modelcontextprotocol/ext-apps/blob/main/specification/2026-01-26/apps.mdx).
- [OpenAI: стандартный MCP Apps bridge и состояние UI](https://developers.openai.com/plugins/build/chatgpt-ui).
- [OpenAI: tool response metadata и structuredContent](https://developers.openai.com/plugins/reference).
