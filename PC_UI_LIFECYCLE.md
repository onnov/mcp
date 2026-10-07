# PC MCP: lifecycle карточки подтверждения

Версия исправления: server `1.1.5`, tool schema `5`,
approval `ui://pc-mcp/approve-v8.html`, picker `ui://pc-mcp/workspace-v5.html`.
Picker тоже получил новый URI, поскольку обе карточки встраивают один bridge.
Прежние обслуживаемые URI сохранены как aliases на новый HTML.

Регрессия вывода и отдельная проверка кэша описаны в
[PC_OUTPUT_REGRESSION.md](PC_OUTPUT_REGRESSION.md). В реальной старой карточке
подтверждён `Unknown tool`; проверка нового бинарника в ChatGPT остаётся deploy gate.

## Причина и пределы доказательства

В исходной реализации `receive()` считал входящие snapshots текущим состоянием
job. Источниками были и MCP notification, и повторное чтение OpenAI globals.
Они не являются подпиской на состояние процесса. Между ними нет общей версии
или порядка с ответами внутренних RPC. `hydrate()` перечитывал первоначальный
result при любом `openai:set_globals`, даже при изменении темы или widget state.

В `2ad8918` локальный `approvalConsumed` уже блокировал повторный
`awaiting_approval` в том же iframe. Поэтому один такой replay после клика
не объясняет повторно активную кнопку в этой версии. Однако closure исчезает
при пересоздании iframe: старая карточка `pc_request_run` снова содержит
`awaiting_approval` и nonce, и старая реализация активировала кнопку, не проверяя
сервер. Кроме того, replay `running` мог перезаписать уже terminal state и
включить Stop. Сам `bridge` не смешивал коррелированные ответы RPC с
notifications; смешение происходило при обработке состояния в карточке.

Новый harness исполняет реальные `bridge.js` и HTML в отдельных Window/VM.
На неизменённом исходном UI воспроизведены три падения:

1. Повторное открытие завершённого job со старым snapshot активирует approval.
2. Поздний unsolicited `running` заменяет terminal state и включает Stop.
3. Отклонённый approval становится доступным после пересоздания iframe.

Это доказанные ошибки кода. Трасса конкретного проблемного экземпляра ChatGPT
не получена; утверждать, что именно он был пересоздан, нельзя. Harness покрывает
этот lifecycle и replay событий, но окончательная проверка в ChatGPT проводится
после restart и создания новой карточки.

## Каналы событий

| Источник | Что он означает | Роль после исправления |
| --- | --- | --- |
| `ui/initialize` → ответ → `ui/notifications/initialized` | Handshake готовности UI | Разрешает обращения к серверу; не задаёт job state |
| `ui/notifications/tool-result` | Envelope результата tool | Один initial job id + request + приватный nonce |
| `openai:set_globals` | Частичное обновление host globals | Hydrate только при обновлении tool data |
| `window.openai.toolResponseMetadata` | Widget-only metadata, включая полный MCP envelope | Совместимый initial источник `mcp_tool_result` / `call_tool_result` |
| `window.openai.toolOutput` | Structured snapshot для карточки | Initial fallback, без права перезаписывать job state |
| `PC.onResult` | Notification/hydrate stream, с replay последнего результата | Карточка принимает identity один раз, последующие события игнорирует |
| `tools/call` → ответ с matching id | Ответ конкретного внутреннего вызова | Promise `PC.tool`; approval/cancel вызывают свежую сверку status |
| `ui/update-model-context` | Исходящее резюме для следующих ходов модели | Не изменяет initial result; ACK не блокирует polling |
| `ui/resource-teardown` | Host закрывает/пересоздаёт iframe | Остановить polling и ответить host; job продолжает жить на сервере |

Спецификация не даёт глобального порядка между snapshots, globals и внутренними
ответами. Код больше не зависит от повторной доставки первоначального result
или echoes результатов внутренних вызовов.

## State machine

| Состояние | Approval | Stop |
| --- | --- | --- |
| Initial получен; сервер ещё не проверен | Disabled | Disabled |
| `pc_job_status = awaiting_approval`, nonce есть, approval не consumed | Можно подтвердить | Disabled |
| Первый click | Синхронно consumed, nonce очищен, навсегда disabled | Disabled до running/queued от сервера |
| `pc_job_status = running/queued` | Disabled | Активна |
| `succeeded/failed/cancelled/timed_out/expired` | Disabled | Disabled |
| Ошибка запуска или недоступный job | Disabled; approval не повторяется | По последнему подтверждённому состоянию job |

Факт consumed сохраняется как `{id, consumed:true}` в приватной части
`window.openai.widgetState`, без nonce. Это latch взаимодействия, а не копия
состояния job. При каждом новом mount `pc_job_status` обязателен перед активацией
approval. Даже host без widget persistence не предложит запуск уже выполняемого,
завершённого или отсутствующего на сервере job. Сохранение отказа/незавершённого
RPC между mount зависит от optional widget persistence; сервер остаётся
окончательной защитой от повторного успешного запуска.

После binding только `pc_job_status` пишет job state. Единственный polling
promise исключает одновременные readers; после action старый read завершается
перед новым. Model context обновляется отдельно и только при изменении резюме.
Terminal state применяется сразу вместе с очередной страницей stdout/stderr,
поэтому Stop отключается немедленно. Основной console contract теперь находится
в `pc_job_status.output.records`: `records_cursor` и `more` позволяют дочитать
retained output даже после terminal state, а `evicted` сообщает о реально
потерянных записях. UI не зависит от `pc_job_output`; старые `head/tail` остаются
fallback для совместимости.

## Проверки и restart

```bash
make check-pc
make build-pc
git diff --check
```

`check-pc` включает race tests Go, обе UI suites с настоящим bridge, vet и gofmt.
В lifecycle suite есть последовательность initial → click → approve/running →
status/running → stale initial → status/succeeded → stale globals/hydrate,
повторные onclick, несколько jobs, remount, pending RPC, teardown и retained output.

В текущем `AI/mcp` остановить запущенный сервер обычным launcher-способом,
затем запустить уже собранный бинарник существующим launcher:

```bash
./start_pc_mcp.sh
```

HTML/JS встроены в бинарник через `go:embed`; один restart старого бинарника
не обновляет UI. Если используемый launcher запускает установленный экземпляр
из `$HOME/.local/bin`, сначала выполнить `make install-pc`.

После restart сначала вызвать `pc_capabilities`: ожидаются `server_version=1.1.5`
и `tool_schema_version=5`. Только затем создавать новую тестовую approval-card.
Обязательно обновить metadata/инструменты подключения ChatGPT: на проверенном
подключении каталог инструментов и HTML карточки устарели. До пользовательской проверки в ChatGPT push и PR запрещены.

## Официальные источники

- [MCP Apps protocol/lifecycle, 2026-01-26](https://github.com/modelcontextprotocol/ext-apps/blob/main/specification/2026-01-26/apps.mdx).
- [OpenAI UI: серверное состояние, instance state и widget persistence](https://developers.openai.com/plugins/build/chatgpt-ui).
- [OpenAI reference: globals, MCP envelopes и setWidgetState](https://developers.openai.com/plugins/reference).
