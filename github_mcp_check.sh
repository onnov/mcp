#!/bin/sh
# github_mcp_check.sh — запуск из cron раз в минуту:
#   * * * * * /полный/путь/github_mcp_check.sh
# Запускает github_mcp, только если он не работает и не остановлен вручную
# через github_mcp_stop.sh. Ничего не пишет в stdout (cron не шлёт письма),
# события пишет в github_mcp.log.
set -u

script=$0
if resolved=$(readlink -f -- "$script" 2>/dev/null) && [ -n "$resolved" ]; then
    script=$resolved
fi
DIR=$(cd -- "$(dirname -- "$script")" && pwd -P) || exit 1

NAME=github_mcp
BIN=$DIR/$NAME
ENV_FILE=$DIR/.env
PID_FILE=$DIR/$NAME.pid
STOP_FILE=$DIR/$NAME.stopped
LOG_FILE=$DIR/$NAME.log
LOCK_DIR=$DIR/$NAME.lock
LOG_MAX_BYTES=10485760
TAG=check
PATH=/usr/local/bin:/usr/bin:/bin:${PATH:-}
export PATH

log() { printf '%s [%s] %s\n' "$(date '+%Y-%m-%d %H:%M:%S')" "$TAG" "$*" >>"$LOG_FILE"; }
say() { log "$*"; }

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

rotate_log() {
    if [ -f "$LOG_FILE" ] && [ "$(wc -c <"$LOG_FILE")" -gt "$LOG_MAX_BYTES" ]; then
        mv -f "$LOG_FILE" "$LOG_FILE.1"
    fi
}

start_server() {
    if [ ! -x "$BIN" ]; then
        say "ошибка: нет исполняемого файла $BIN"
        return 1
    fi
    rotate_log
    (
        cd "$DIR" || exit 1
        if [ -f "$ENV_FILE" ]; then
            set -a
            . "$ENV_FILE"
            set +a
        fi
        if command -v setsid >/dev/null 2>&1; then
            exec setsid nohup "$BIN" </dev/null >>"$LOG_FILE" 2>&1
        fi
        exec nohup "$BIN" </dev/null >>"$LOG_FILE" 2>&1
    ) &
    sleep 2
    pids=$(server_pids)
    if [ -z "$pids" ]; then
        say "ошибка: $NAME завершился сразу после запуска, причина выше в $LOG_FILE"
        rm -f "$PID_FILE"
        return 1
    fi
    echo "$pids" | head -n 1 >"$PID_FILE"
    say "$NAME запущен, PID $(cat "$PID_FILE")"
}

[ -e "$STOP_FILE" ] && exit 0
lock 0 || exit 0
[ -e "$STOP_FILE" ] && exit 0

pids=$(server_pids)
if [ -n "$pids" ]; then
    echo "$pids" | head -n 1 >"$PID_FILE"
    exit 0
fi

say "$NAME не работает, запускаю"
start_server
