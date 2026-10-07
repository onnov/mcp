#!/bin/sh
set -eu
# Optional strict profile: private config and delegated resource limits.
# Ordinary owner launchers may keep configuration inside the workspace.
config=${PC_MCP_CONFIG:-${XDG_CONFIG_HOME:-$HOME/.config}/pc-mcp/server.env}
if [ ! -f "$config" ]; then
 echo "Create private config $config from .pc-mcp.ssh.env.example (chmod 600)." >&2
 exit 1
fi
set -a
. "$config"
set +a
binary=${PC_MCP_BINARY:-${HOME}/.local/bin/pc-mcp}
if [ ! -x "$binary" ]; then echo "Install pc-mcp at $binary first (make install-pc)." >&2; exit 1; fi
# Delegation provides per-job memory/PID accounting and reliable descendant kill.
# No CPU quota is applied. systemd transfers environment to this private scope.
exec systemd-run --user --scope --quiet --property=Delegate=yes "$binary" --resource-mode strict "$@"
