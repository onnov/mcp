.PHONY: build build-pc test check fmt test-pc-sandbox

build:
	CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o github_public_mcp ./cmd/github-mcp

test:
	go test -race ./...
	node --test internal/ui/picker_test.cjs internal/pc/ui/cards_test.cjs

check: test
	go vet ./...
	@test -z "$$(gofmt -l cmd internal)"

fmt:
	gofmt -w cmd internal

build-pc:
	CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o pc-mcp ./cmd/pc-mcp

test-pc-sandbox:
	PC_MCP_REQUIRE_SANDBOX=1 go test -race ./internal/pc/sandbox -run TestLinuxSandboxIsolation -v
