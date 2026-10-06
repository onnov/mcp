# PC MCP: режим одного владельца и рекомендации по усилению защиты

Сервер предоставляет одному доверенному владельцу RW-доступ к проектам,
build/test/smoke, скрипты, приложения, Git/gh и отправку в репозиторий.
Предоставление доступа означает возможность исполнять код проекта. Метка
`purpose=test` не делает произвольную команду безопасной и не урезает её права.

## Повседневный запуск

Существующий launcher и секреты разрешено оставлять в рабочем каталоге.
Диагностическая проверка известных конфигурационных файлов не блокирует запуск
и не сканирует все проекты. SSH-конфигурация в проектах также допускается.
Настройки сервера принимаются из environment/flags; модель не меняет их через
аргументы отдельных jobs. Не требуется выполнять миграцию или менять секреты.
State/cache остаются отдельным private каталогом: туда сервер помещает свои
блокировки и служебные данные. Это не перенос пользовательской конфигурации.

`--resource-mode auto` / `PC_MCP_RESOURCE_MODE=auto` — default: cgroup limits
включаются, когда доступны. Если scope не делегирован или содержит shell и другие
процессы, `pc_capabilities` показывает `resource_warning`, и команды продолжают
работать без cgroup RAM/PID ceiling. Bubblewrap namespace isolation остаётся
обязательной. CPU quota нет: доступны все свободные cores. При cgroups RAM budget
пересчитывается из доступной памяти с host reserve (auto: 10%, минимум 512 MiB).
`--memory-max-mib 0` не задаёт искусственного фиксированного потолка.
Без cgroup память/количество процессов не ограничиваются этими настройками.

Stop завершает выбранный job, Stop all — процессы всех jobs плагина, включая
синхронные команды workspace. В cgroup режиме используется `cgroup.kill`;
без cgroup — отмена всей process group и PID namespace bubblewrap. Дочерние
процессы, создавшие новую сессию, также завершаются при уничтожении namespace.
Длительные jobs задают deadline до 86400 секунд и могут останавливаться раньше.
Несвязанные приложения владельца не включаются в список процессов плагина.

## Подключение без ввода пароля

Для личного, нераспространяемого predefined OAuth client:

```bash
export PC_MCP_OWNER_AUTH=client-secret
```

Либо флаг `--owner-auth client-secret`. Client ID и существующий уникальный
Client Secret по-прежнему указываются в настройках OAuth в ChatGPT. Дополнительная
страница пароля не показывается. Это Authorization Code + PKCE S256, не
`client_credentials` grant. Token endpoint проверяет client secret, точный
callback, resource, verifier и одноразовый code. Access token действует до часа,
refresh grant — до семи дней; rotation и отзыв сохранены. Токены находятся в
памяти: restart отзывает их и требует новой OAuth-авторизации.

**Каждый обладатель секрета этого клиента имеет права владельца.** Режим подходит
для личного клиента, который не публикуется, не используется несколькими
пользователями и не передаётся другим людям. Для общего клиента потребуется
проверка владельца или полноценная система пользовательской авторизации.

Authorize endpoint stateless: анонимные запросы не заполняют очередь кодов.
Зашифрованные codes действуют одну минуту; replay state создаётся только после
успешной аутентификации клиента и ограничена по объёму. Login envelope и code
имеют разные cryptographic domains. Неверные секреты не дают MCP-доступа.
OAuth scopes дают доступ ко всему настроенному дереву, без multi-user ACL.

Default `PC_MCP_OWNER_AUTH=password` требует bcrypt hash и пароль владельца.
Это рекомендуемый дополнительный барьер, если client secret может быть доступен
другим людям. Настройка password hash сохраняется при переключении режима.

Проверка user ID в произвольном заголовке недостаточна: заголовок можно подделать.
IP allowlist или подтверждённое происхождение от OpenAI определяет сервис,
но не конкретного владельца. Для HTTPS можно дополнительно проверить OpenAI
mTLS на публичном reverse proxy: validate chain/clientAuth/SAN
`mtls.prod.connectors.openai.com`, без привязки к меняющемуся leaf certificate.
OAuth всё равно нужен для права доступа. Secure MCP Tunnel — альтернативный
вариант приватного доступа, привязанный к настройкам OpenAI workspace.

Официальные источники:

- [OpenAI: MCP authentication](https://developers.openai.com/plugins/build/auth)
- [OpenAI: Secure MCP Tunnels](https://developers.openai.com/api/docs/guides/secure-mcp-tunnels)

## Строгие настройки на будущее

| Защита | Как включить / ограничение |
| --- | --- |
| Пароль владельца | `PC_MCP_OWNER_AUTH=password`, bcrypt cost 10..14, двухпоточная проверка и rate limit |
| Секреты вне проекта | Private env/SSH files вне workspace/cache/SDK, каталоги 0700, файлы 0600; не добавлять в Git. `scripts/migrate-pc-config.sh` — отдельная необязательная миграция, изменяет launcher и удаляет `paswd_hash.md`; изучить до использования |
| Отдельный executable | Установить через `make install-pc` вне RW workspace; bwrap executable всегда вне workspace/cache |
| Обязательные RAM/PID limits | `--resource-mode strict` и отдельный `systemd-run --user --scope --property=Delegate=yes` для самого бинарника; Linux cgroup v2, kernel >=5.14. Проверить `make test-pc-resources` |
| Резерв RAM | Увеличить `--memory-reserve-mib` для тяжёлых параллельных host workloads; optional `--memory-max-mib`; swap jobs выключен, OOM уничтожает группу |
| Fork/resource limits | `--max-processes` (default 4096), `--max-jobs` (default 4, 1..16), ограниченный history; PID ceiling требует cgroups |
| Disk exhaustion | Disk/inode guard каждые 200 ms, reserve default 1 GiB, RLIMIT_FSIZE; строгая гарантия требует отдельной filesystem/project quota, polling может пропустить быстрый burst |
| FD/tmp exhaustion | FD limit min(4096, inherited hard limit); `--tmp-max-mib`, default strict 1 GiB / auto kernel tmpfs default; cgroup accounting учитывает tmpfs RAM |
| Подтверждение команд | `--confirm-commands`; network/credential jobs уже требуют отдельного подтверждения |
| Ограничение исходящей сети | Default network disabled. `--allow-network` разрешает подтверждённые jobs через public HTTP(S) proxy, DNS/IP validation, 80/443, запрет private/loopback/link-local/reserved destinations |
| LAN/VPN/localhost | `--allow-private-network` расширяет proxy; `--allow-host-network` + job `host_network:true` даёт прямой TCP/UDP. Включать по необходимости; host-network jobs не получают credential capability |
| GitHub credentials | Fine-grained `PC_MCP_GH_TOKEN` только для нужных репозиториев; host-only injection в проверенный HTTPS GitHub host, job env содержит placeholder; hooks/fsmonitor отключены для credential jobs |
| HTTP perimeter | HTTPS, loopback listen, bounded sockets/request slots, Host/Origin/header/body/time limits; exact trusted proxy CIDRs, overwritten single X-Forwarded-For |
| DDoS на внешнем канале | Hosting/WAF provider protection, per-IP requests/connections и timeouts на публичном proxy; пример `scripts/pc-proxy-nginx.conf.example`, Apache требует эквивалентных правил |
| SSH tunnel | Проверенный known_hosts, private key 0600, remote bind localhost, GatewayPorts no, optional отдельный пользователь с PermitListen/remote-forwarding-only |
| Независимая проверка | `make check-pc`, required sandbox/network integration, strict cgroup integration и явный AuditWorkspaceSecrets |

## Защита, которая остаётся в обычном режиме

Файлы ограничены rooted workspace, traversal и `.git` API access запрещены,
операции требуют точную ветку и revision. Create публикуется atomic no-replace;
update имеет optimistic revision checks, но не atomic CAS с внешним IDE.
Live editing допускается во время jobs. Branch transition ждёт освобождения
checkout; пересекающиеся области разных проектов не выполняются одновременно.

Публичные запросы, OAuth attempts/grants, pending approvals, job concurrency,
журналы и HTTP connections ограничены по объёму. Анонимные запросы не занимают
MCP slots авторизованного владельца. Request body/header/time bounds и
single-use approvals сохраняются. Logout/restart/revoke не выдаёт доступ без
токена. Доступ команды к секрету, добровольно оставленному внутри workspace,
является следствием предоставленного RW-доступа.

Application limits уменьшают нагрузку и ограничивают память/процессы сервера,
но не могут закрыть все виды атак или гарантировать отсутствие volumetric DDoS:
атака может заполнить канал ещё до достижения приложения. Защиту внешнего канала
настраивает hosting provider/WAF. Частное владение каталогом не скрывает
публичный HTTPS endpoint от чужих запросов.
