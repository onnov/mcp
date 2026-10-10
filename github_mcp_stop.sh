#!/bin/sh
# github_mcp_stop.sh — ручная остановка github_mcp.
# Ставит отметку github_mcp.stopped, поэтому cron (github_mcp_check.sh) больше
# не запускает сервер, пока его не запустят вручную через github_mcp_start.sh.
set -u

script=$0
if resolved=$(readlink -f -- "$script" 2>/dev/null) && [ -n "$resolved" ]; then
    script=$resolved
fi
DIR=$(cd -- "$(dirname -- "$script")" && pwd -P) || exit 1

NAME=github_mcp
BIN=$DIR/$NAME
PID_FILE=$DIR/$NAME.pid
STOP_FILE=$DIR/$NAME.stopped
LOG_FILE=$DIR/$NAME.log
LOCK_DIR=$DIR/$NAME.lock
TAG=stop
PATH=/usr/local/bin:/usr/bin:/bin:${PATH:-}
export PATH

log() { printf '%s [%s] %s\n' "$(date '+%Y-%m-%d %H:%M:%S')" "$TAG" "$*" >>"$LOG_FILE"; }
say() { echo "$*"; log "$*"; }

# PID процессов github_mcp из этого каталога.
server_pids() {
    if command -v ps >/dev/null 2>&1; then
        ps -eo pid=,args= 2>/dev/null | while read -r pid cmd _; do
            case $cmd in
                "$BIN") echo "$pid" ;;
                */"$NAME" | "$NAME")
                    [ "$(readlink "/proc/$pid/exe" 2>/dev/null)" = "$BIN" ] && echo "$pid" ;;
            esac
        done
    elif [ -s "$PID_FILE" ]; then
        pid=$(cat "$PID_FILE")
        case $pid in
            '' | *[!0-9]*) ;;
            *) kill -0 "$pid" 2>/dev/null && echo "$pid" ;;
        esac
    fi
}

# Блокировка через mkdir: работает без flock и не наследуется сервером.
# $1 — сколько секунд ждать.
lock() {
    waited=0
    while ! mkdir "$LOCK_DIR" 2>/dev/null; do
        holder=$(cat "$LOCK_DIR/pid" 2>/dev/null)
        if [ -n "$holder" ] && ! kill -0 "$holder" 2>/dev/null; then
            rm -rf "$LOCK_DIR"
            continue
        fi
        if [ -z "$holder" ] && [ -n "$(find "$LOCK_DIR" -maxdepth 0 -mmin +1 2>/dev/null)" ]; then
            rm -rf "$LOCK_DIR"
            continue
        fi
        [ "$waited" -ge "$1" ] && return 1
        sleep 1
        waited=$((waited + 1))
    done
    echo $$ >"$LOCK_DIR/pid"
    trap 'rm -rf "$LOCK_DIR"' EXIT
    trap 'exit 1' HUP INT TERM
}

# Отметка ставится до блокировки: cron, запущенный в эту же секунду, уже не стартует.
touch "$STOP_FILE" || exit 1

if ! lock 30; then
    echo "ошибка: другой скрипт github_mcp_* держит $LOCK_DIR дольше 30 с" >&2
    exit 1
fi

pids=$(server_pids)
if [ -z "$pids" ]; then
    rm -f "$PID_FILE"
    say "$NAME не запущен; автозапуск из cron отключён"
    exit 0
fi

say "останавливаю $NAME, PID $(echo $pids)"
kill -TERM $pids 2>/dev/null
i=0
while [ "$i" -lt 15 ]; do
    sleep 1
    i=$((i + 1))
    pids=$(server_pids)
    [ -z "$pids" ] && break
done
if [ -n "$pids" ]; then
    say "$NAME не завершился за 15 с, отправляю SIGKILL"
    kill -KILL $pids 2>/dev/null
    sleep 1
    pids=$(server_pids)
fi
if [ -n "$pids" ]; then
    say "ошибка: не удалось остановить $NAME, PID $(echo $pids)"
    exit 1
fi
rm -f "$PID_FILE"
say "$NAME остановлен; автозапуск из cron отключён до github_mcp_start.sh"
