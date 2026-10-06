#!/bin/sh
set -eu
if [ "$#" -gt 1 ]; then
    echo "Usage: sh scripts/start.sh [trusted-shell-env-file]" >&2
    exit 2
fi
if [ "$#" -eq 1 ]; then
    case "$1" in
        /*|./*|../*) mcp_env_path="$1" ;;
        *) mcp_env_path="./$1" ;;
    esac
    set -a
    . "$mcp_env_path"
    set +a
fi
exec ./github_public_mcp
