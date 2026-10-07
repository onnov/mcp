#!/bin/sh
set -eu
# One-time migration; run on the PC from this repository's root.
private=${XDG_CONFIG_HOME:-$HOME/.config}/pc-mcp
umask 077
mkdir -p "$private"
if [ -f "$private/server.env" ]; then echo "Private server.env already exists; merge manually without copying secrets back to the project." >&2; exit 1; fi
if [ ! -f start_pc_mcp.sh ]; then echo "No start_pc_mcp.sh to migrate." >&2; exit 1; fi
# Copy only environment assignments, never execute the old script.
# Existing config uses single-line export assignments; refuse continuation syntax.
if sed -n '/^export /p' start_pc_mcp.sh | grep '\\$' >/dev/null; then echo "Multiline configuration must be migrated manually." >&2; exit 1; fi
sed -n '/^export /p' start_pc_mcp.sh > "$private/server.env"
chmod 600 "$private/server.env"
cat > start_pc_mcp.sh <<'EOF'
#!/bin/sh
set -eu
exec sh "$(dirname "$0")/scripts/run-pc-secure.sh" "$@"
EOF
chmod 700 start_pc_mcp.sh
# This reviewed file contains a plaintext owner password; the bcrypt hash was
# copied from the launch config, so keeping the plaintext serves no purpose.
if [ -f paswd_hash.md ]; then rm -- paswd_hash.md; fi
echo "Configuration moved. Rotate owner password/client secret in the private server.env, then start with ./start_pc_mcp.sh."
