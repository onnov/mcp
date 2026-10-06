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
- Compiled binary stdio check: 16 tools, explicit selection, revision-bearing
  file read/write, persistence, traversal refusal, embedded UI/CSP, sandbox
  setup refusal and SIGTERM shutdown with exit code 0.
- Path/symlink confinement, stale file revisions, stale/dirty branches,
  non-Git projects, current-branch file work without command sandbox,
  nested-project checkout leases and single-instance state locking.
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
