# PC MCP: полноценная локальная разработка

Сервер рассчитан на одного доверенного владельца. Разрешённые возможности:
читать, писать и удалять файлы проекта, выполнять build/test/smoke, запускать
приложения и скрипты, работать с Git/gh и отправлять изменения в GitHub.
`purpose` описывает задачу и не ограничивает возможности команды.
Обычный локальный запуск имеет RW-доступ ко всему checkout. Для выбранного
подкаталога Git область команд — корень checkout; UI показывает её отдельно.

## Установка на Linux

Нужны Go >=1.27, bubblewrap >=0.12 и util-linux (`prlimit`). По умолчанию
`--resource-mode auto`: делегированный cgroup используется при доступности,
иначе команды выполняются с предупреждением без RAM/PID ceiling. Namespace
изоляция, сроки выполнения, Stop/Stop all, ограничение журналов и disk guard
сохраняются. Все свободные CPU доступны; искусственной CPU quota нет.
Строгий ресурсный режим и рекомендации: [PC_SECURITY.md](PC_SECURITY.md).

Для существующего launcher с настройками и секретами в рабочем каталоге:

```bash
# В текущем AI/mcp, ветка mcp/pc-ssh-proxy.
gofmt -w internal/pc
go build -o pc-mcp ./cmd/pc-mcp
./start_pc_mcp.sh
```

Новый бинарник запускается обычным launcher без миграции настроек. Перед заменой
работающего сервера остановите его обычным способом. Длительные процессы
плагина завершаются при остановке сервера.

Lifecycle одноразового подтверждения, regression harness и проверка версии после
restart: [PC_UI_LIFECYCLE.md](PC_UI_LIFECYCLE.md).

Для подключения единственного владельца без ввода пароля можно явно задать
`PC_MCP_OWNER_AUTH=client-secret` или `--owner-auth client-secret` при запуске.
Настройку Client ID/Client Secret в ChatGPT сохраните: именно уникальный секрет
определяет доступ. Client ID сам по себе, user ID и заголовки OpenAI не являются
аутентификацией. Не публикуйте и не делитесь этим клиентом. Любой обладатель
секрета получает права владельца. По умолчанию остаётся `password`; для него
нужен существующий bcrypt hash. PKCE S256, HTTPS callback, resource binding,
одноразовый code, bearer tokens, rotation и отзыв сохраняются в обоих режимах.

Секреты разрешено хранить в workspace: известные конфигурационные файлы дают
диагностическое предупреждение, а не ошибку запуска. Авторизованные файловые
инструменты и команды имеют доступ к этим файлам. Git ignore предотвращает
обычное добавление в Git, но не чтение. Полного рекурсивного сканирования при
старте нет. `AuditWorkspaceSecrets` остаётся отдельным ограниченным аудитом.
Перенос настроек и смена секретов необязательны; варианты усиления защиты
сохранены в [PC_SECURITY.md](PC_SECURITY.md).

Для первой установки: `make deps-pc check-pc install-pc`. Бинарник устанавливается
в `$HOME/.local/bin/pc-mcp`. Пример настроек: `.pc-mcp.ssh.env.example`.
Сам бинарник принимает environment/flags и не читает env-файлы. SDK вне
`/usr`, `/bin`, `/lib` добавляйте через `--toolchains /path/to/sdk`.

Для resource limits без квоты CPU можно запускать сам бинарник в отдельном scope:

```bash
systemd-run --user --scope --quiet --property=Delegate=yes ./pc-mcp
```

Environment должен быть уже экспортирован. Не запускайте внутри scope shell,
оставляющий родительский процесс рядом с бинарником: нужен отдельный scope
самого сервера. `scripts/run-pc-secure.sh` предоставляет строгий вариант с
env-файлом. Для stdio protocol stdout должен оставаться чистым.

## Подключение

- SSH + HTTPS + owner OAuth: [PC_SSH_SETUP.md](PC_SSH_SETUP.md).
- HTTP: `PC_MCP_TRANSPORT=http`, loopback `PC_MCP_HTTP_ADDR`, public HTTPS proxy,
  `PC_MCP_PUBLIC_URL`, owner password hash и OAuth client secret.
- OpenAI Tunnel: `PC_MCP_TRANSPORT=tunnel`, `CONTROL_PLANE_TUNNEL_ID`,
  `CONTROL_PLANE_API_KEY`, опционально organization ID. В ChatGPT выбрать Tunnel.
- Локальный MCP-клиент: `PC_MCP_TRANSPORT=stdio`.

Не предоставляйте этот single-owner endpoint нескольким независимым людям:
все допущенные transport-ом клиенты получают одну capability ко всему root.
Отдельные владельцы требуют отдельных root/state/process/transport instances.
SSH key/known_hosts должны находиться вне workspace, cache и toolchains;
host key проверяется, SSH agent и home не монтируются в job.
`PC_MCP_SOCKS5_PROXY` управляет исходящими SSH/OpenAI Tunnel соединениями.

После обновления бинарника обновите подключение плагина в ChatGPT. Версия
сервера 1.1.5 и resource URI обновлены; старые UI URI продолжают обслуживаться.
Сервер не может принудительно сбросить descriptor cache клиента. Если новые
поля/инструменты отсутствуют, обновление подключения обязательно. Перезапуск
отзывает in-memory OAuth grants и требует повторного входа.

## Рабочий процесс

Выберите directory/branch в интерактивной карточке. Перед изменениями читайте
ревизии; не переключайте dirty checkout автоматически. У каждой операции
явные `directory` и `branch`; сохранённый выбор — только default для нового чата.

`pc_start_job` запускает разрешённую пользователем локальную разработку,
включая smoke, приложение, скрипт и local Git, без дополнительной карточки.
По желанию operator может включить `--confirm-commands`: тогда карточка нужна
для каждого arbitrary command независимо от `purpose`. `pc_request_run` также
можно использовать для добровольного подтверждения любой локальной команды.
Сеть и GitHub credentials всегда требуют карточку с точным argv и capabilities.
Nonce приватный, одноразовый, истекает через 10 минут и не передаётся модели.

```json
{"directory":"project","branch":"feature","args":["go","test","-race","./..."],"purpose":"test","seconds":1800}
```

Зависимости отсутствуют в project cache? Первый запуск выполняйте с
`network:true` через карточку. В private server.env должно быть
`PC_MCP_ALLOW_NETWORK=true` (или launcher `--allow-network`). Затем тот же
project cache повторно используется без сети. Альтернатива: host `go mod vendor`,
после чего зависимости доступны в checkout. Host Go module cache автоматически
не монтируется: тесты не получают скрытый доступ к home или host credentials.
`make deps-pc` готовит host cache для сборки самого сервера, а не job cache.

Запуск возвращает job ID. Основной контракт polling — `pc_job_status` с
`after=output.records_cursor`: `output.records` возвращает следующую ограниченную
порцию retained console output, `more` сообщает о продолжении, `evicted` — о
потерянных до cursor записях. Это самодостаточный контракт для UI; отдельный
`pc_job_output` остаётся необязательным API для клиентов, которым удобнее читать
логи отдельно от status. Head/tail summary и `output.cursor` сохранены для обратной
совместимости. Retained logs: последние 1 MiB / 10000 records на job. Оба потока
всегда дренируются. History ограничена 50 jobs; restart её очищает.

`pc_cancel_job` останавливает job и его descendants. `pc_cancel_all_jobs`
останавливает все plugin jobs и отменяет pending approvals. Poll до terminal
status: ответ cancel — запрос остановки, не утверждение, что процессы уже умерли.
Есть кнопки остановки в карточке запуска и в workspace picker. Несвязанные
процессы владельца не затрагиваются. Exit/shutdown уничтожает все plugin jobs.

До 4 jobs могут выполняться одновременно (`--max-jobs 1..16`), включая приложение
и smoke/test в одном checkout. Команды с одинаковой областью делят execution lease;
разные пересекающиеся parent/child области блокируются. Live read/write/delete
доступны во время работы приложения для hot reload. Branch transitions ждут
завершения всех jobs checkout. При параллельных build/test учитывайте обычные
конфликты generated files; `purpose` не предоставляет read-only гарантий.

## Ресурсы и производительность

Все доступные CPU cores разрешены; `GOMAXPROCS=2` и CPU quota удалены.
Когда cgroup доступен, память всех jobs ограничена общим cgroup pool: доступная память
минус host reserve. Перед запуском очередной задачи pool пересчитывается с учётом
RAM уже запущенных jobs; освободившаяся память доступна без перезапуска сервера. Отдельный job получает доступную на старте capacity с учётом
ancestor/pool headroom. Default RAM reserve: 10%, минимум 512 MiB.
`--memory-reserve-mib` задаёт резерв; `--memory-max-mib` optional ceiling, 0 — auto.
Host workloads, запущенные позже, также потребляют память; reserve следует
увеличить на ПК с тяжёлыми параллельными задачами. Нулевой запас хосту не обещается.

В cgroup режиме `--max-processes` ограничивает процессы/threads каждого job и всего pool (default 4096).
`memory.oom.group=1`, `memory.swap.max=0`, `pids.max`, `cgroup.kill` не зависят от
имён команд или `purpose`. Новая сессия/setsid/double-fork остаются в cgroup.
`--max-seconds` default/max 86400; default job duration 300. Для долгого приложения
задайте нужный deadline и завершите его вручную, когда работа закончена.

FD limit не выше 4096 и inherited hard limit. `/tmp` в auto использует kernel
tmpfs default; в strict default 1 GiB. `--tmp-max-mib` задаёт другой предел.
Tmpfs учитывается в RAM cgroup при его доступности. Cache writable только для выбранного checkout, по SHA256 root path;
другие project caches и control-plane state в sandbox отсутствуют.

Disk/inode guard проверяет checkout/cache каждые 200 ms и останавливает job при
достижении reserve (`--disk-reserve-mib`, default 1024). Single-file writes имеют
RLIMIT_FSIZE. Это защита раннего прекращения, **не filesystem quota**: быстрый burst
может обогнать polling. Для строгой защиты disk exhaustion нужен выделенный
filesystem/project quota для workspace/cache; её настройка требует host admin
и зависит от файловой системы. Root namespace сам по себе quota не обеспечивает.

## Сеть и GitHub

Default network job сохраняет private network namespace. HTTP(S)_PROXY указывает
на loopback bridge, который передаёт запросы через отдельный Unix socket серверу.
Сервер проверяет DNS answers и dial-ит проверенный IP; private/loopback/link-local,
reserved/multicast и порты кроме 80/443 блокируются. Connection count, header sizes
и timeouts ограничены. Системные SDK используют стандартные proxy env variables.
Custom clients должны уметь HTTP proxy; raw TCP/UDP не получает прямой маршрут.

Для DB/LAN development services есть operator `--allow-private-network`;
маршрут по-прежнему через proxy. Если нужен обычный TCP/UDP networking, operator
может включить `--allow-host-network`, а job запросить `network:true,host_network:true`.
Карточка явно показывает доступ к Интернету/localhost/LAN/VPN. Этот opt-in
предоставляет более широкую capability и не имеет public-egress filtering.
Credential jobs всегда используют изолированный proxy, `host_network:false`.
Для smoke, обращающегося к приложению из другого job, нужен этот явный host-network
opt-in у обоих jobs. При обычной изоляции их localhost различается; другой вариант —
запустить приложение и smoke одной командой в общем sandbox.

`PC_MCP_GH_TOKEN` остаётся в сервере. В `git/gh` environment только placeholder;
real Authorization добавляется host proxy для canonical github.com/api.github.com/uploads.github.com
HTTPS с проверкой TLS. Неавторизованные hosts и redirects не получают token.
Per-job CA bundle публичный; private CA key/token в sandbox не монтируются.
Даже repo-controlled child process не может прочитать raw token из environment.
Credential approval разрешает job обращаться к GitHub в пределах token scopes;
используйте fine-grained token с доступом только к нужным репозиториям.

```json
{"directory":"project","branch":"feature","args":["git","push","origin","feature"],"purpose":"git","network":true,"credential":true,"seconds":1800}
```

Hooks/fsmonitor отключены для credential jobs. Обычные local jobs могут
использовать project scripts/hooks/filters — это часть предоставленной RW
development capability. Модель не должна считать label `test` безопасной командой.

## HTTP/OAuth и защита от перегрузки

OAuth authorize flow зашифрован/подписан и stateless до успешной аутентификации:
anonymous traffic не может заполнить глобальную pending-map. Attempts per-peer,
bounded rate-limit state, два одновременных bcrypt checks, ограниченные grant maps,
128 HTTP sockets, независимые public/MCP request slots, header/body/time limits.
Unauthenticated запросы не занимают authenticated MCP slots. Refresh rotation,
PKCE, exact callback/resource, Origin и Host проверки сохранены.

За HTTPS proxy передавайте single overwritten `X-Forwarded-For`, а на ПК задайте
`PC_MCP_TRUSTED_PROXIES` с точными CIDR доверенного proxy peer. Без этого forwarding
headers не считаются доверенными; при SSH forwarding все запросы могут выглядеть
как один loopback peer. Не доверяйте произвольной цепочке клиентских заголовков.

Public reverse proxy также должен иметь per-IP request/connection limits и
провайдерскую защиту от volumetric DDoS. Application code не может предотвратить
заполнение внешнего канала до достижения сервера. Пример nginx:
[scripts/pc-proxy-nginx.conf.example](scripts/pc-proxy-nginx.conf.example).
Для Apache используйте эквивалентные лимиты хостинга/WAF; одного RewriteRule мало.

## Файлы и проверка

File tools поддерживают regular files <=512 KiB. `os.Root` ограничивает traversal;
`.git` блокируется без учёта регистра. Create через `expected_revision:new` имеет
atomic no-replace publication. Update существующего файла — optimistic revision
check с повторной проверкой перед Rename; **не atomic CAS с внешним IDE**.
File API сериализует собственные mutations; команды и IDE могут менять файлы
параллельно и в этой блокировке не участвуют.
Удаление одного файла требует его revision; recursive deletion доступно командой
в рамках предоставленной project RW capability.

```bash
make check-pc
make test-pc-sandbox test-pc-network
make test-pc-resources
```

`test-pc-resources` компилирует test binary и запускает его напрямую в отдельном
делегированном scope: проверяет memory OOM, PID ceiling и kill после setsid.
Запуск `go test` внутри scope оставляет compiler parent в cgroup и не подходит
для включения domain controllers.

Текущее подтверждённое состояние проверок: [PC_VALIDATION.md](PC_VALIDATION.md).
