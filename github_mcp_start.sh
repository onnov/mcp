#!/bin/sh
# github_mcp_start.sh — ручной запуск github_mcp.
# Снимает ручную остановку (github_mcp_stop.sh), после чего cron
# (github_mcp_check.sh) снова следит за сервером. Если сервер уже работает,
# ничего не делает.
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
TAG=start
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

if ! lock 30; then
    echo "ошибка: другой скрипт github_mcp_* держит $LOCK_DIR дольше 30 с" >&2
    exit 1
fi
rm -f "$STOP_FILE"

pids=$(server_pids)
if [ -n "$pids" ]; then
    echo "$pids" | head -n 1 >"$PID_FILE"
    echo "$NAME уже работает, PID $(cat "$PID_FILE")"
    exit 0
fi

if ! start_server; then
    tail -n 20 "$LOG_FILE" >&2
    exit 1
fi
