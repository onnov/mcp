# PC MCP validation — 2026-10-06

Validated with Go 1.27.0, MCP Go SDK v1.8.0 and the pinned official
OpenAI tunnel-client v0.0.15.

## Passed

- `go test -race ./...` for both servers, including existing GitHub tests.
- `go vet ./...`, `gofmt` and `git diff --check`.
- All 10 dependency-free UI tests: existing GitHub picker plus PC picker,
  branch creation, restored non-Git context, consent and private nonce handling.
- Native static Linux amd64 builds of both commands.
- Cross compilation of `cmd/pc-mcp` for Linux arm64, macOS arm64 and Windows amd64.
  Non-Linux builds provide file tools; command execution fails closed.
- Official tunnel SDK + local mock control plane: initialization, a real PC file
  tool call/response, and preservation of hidden `_meta.approval_nonce` on a
  pending smoke command. No live OpenAI credentials used.
- SOCKS5 fixture: unresolved destination names sent to the proxy, verified TLS,
  anonymous/password authentication, explicit proxy overriding environment
  bypass settings, no direct fallback when unavailable, invalid configuration
  refusal, and official tunnel SDK initialization through the proxy.
- Embedded Go SSH server fixture: authenticated reverse TCP forwarding of an
  HTTP request, automatic reconnection, cancellation/port closure, mismatched
  host-key refusal and private-key permission checks. SSH destination DNS sent
  through SOCKS5 in the outbound dialer test.
- Single-owner OAuth without GitHub: consent/cookie/origin checks, PKCE, exact
  resource and callback, code replay/concurrent consumption refusal, access
  expiration, atomic refresh rotation with family revocation on replay,
  revocation, client_secret_basic/post and bounded password attempts.
- Real HTTP MCP SDK client: unauthenticated access denied, owner password login,
  16 OAuth-tagged tools, file edit in the confined workspace, revocation and
  public Host/Origin checks with reverse-proxy-compatible SDK settings.
- OAuth login regression: the page overrides the outer `no-referrer` policy
  with `same-origin` so HTML form submissions retain their Origin; its CSP
  allows the configured ChatGPT callback. Missing, opaque (`null`) and foreign
  origins remain refused. The new regression assertions failed before the fix;
  affected-package race tests, vet and the PC binary build passed after it.
  These are HTTP/header tests, not a real browser or live ChatGPT login.
- HTTP/SSH env validation: no public listener, no anonymous HTTP mode, SSH key
  outside the workspace, sandbox cache (including symlink aliases) and SDK
  mounts. OpenAI Tunnel and stdio remain supported.
- Compiled binary: interactive-helper stdin hashing, env-only authenticated
  HTTP startup, unauthenticated MCP challenge, no OpenAI runtime in HTTP mode,
  and SIGTERM exit 0. Cross builds include the SSH/OAuth additions.
- Compiled binary stdio check: 16 tools, explicit selection, revision-bearing
  file read/write, persistence, traversal refusal, embedded UI/CSP, sandbox
  setup refusal and SIGTERM shutdown with exit code 0.
- Path/symlink confinement, stale file revisions, stale/dirty branches,
  non-Git projects, current-branch file work without command sandbox,
  nested-project checkout leases and single-instance state locking.
- File paths from a non-Git parent/outer checkout cannot bypass the branch
  boundary of a nested Git repository; symlink directory aliases cannot bypass it.
- Asynchronous cancellation, one-use consent nonce, network consent policy,
  concurrent stdout/stderr, first/tail/cursor behavior, byte budgets, very long
  output without newline, chunk-boundary handling and credential redaction.
- A model-writable `bwrap` executable is refused as the isolation boundary;
  old bubblewrap versions are refused before project command execution.

## Requires verification on the owner's Ubuntu PC

`TestLinuxSandboxIsolation` was **skipped** here: installed bubblewrap is 0.9.0,
which the server refuses, and this execution environment lacks the host `/proc`
needed for namespace setup. Thus actual bubblewrap mounts, namespace isolation
and child process termination have not been exercised here with bubblewrap 0.12.

Run this on the target Ubuntu machine after installing bubblewrap >=0.12.0 and
util-linux, with working unprivileged user namespaces:

```bash
make test-pc-sandbox
```

That target sets `PC_MCP_REQUIRE_SANDBOX=1`: missing or broken sandbox becomes a
failure, not a skip. It checks project writes, outside/symlink access refusal,
absence of host credential environment and the inherited host directory FD,
and cancellation of a command with a child process.

The ChatGPT UI was tested through a simulated MCP Apps host, not a deployed
ChatGPT iframe. Live tunnel availability, organization/workspace associations,
permissions and private widget metadata delivery must be checked in the user's
workspace using [PC_SETUP.md](PC_SETUP.md). No real PC was connected or exposed
by this development session.

SSH forwarding was tested against a local Go SSH server, not the owner's SSH
hosting or Apache. That host must allow remote forwarding and enforce loopback
binds (`GatewayPorts no`). The new password OAuth flow has not been linked in
the owner's ChatGPT workspace; use [PC_SSH_SETUP.md](PC_SSH_SETUP.md).
