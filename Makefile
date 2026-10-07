.PHONY: build build-pc test check fmt test-pc-sandbox

build:
	CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o github_public_mcp ./cmd/github-mcp

test:
	go test -race ./...
	node --test internal/ui/picker_test.cjs internal/pc/ui/cards_test.cjs internal/pc/ui/lifecycle_test.cjs

check: test
	go vet ./...
	@test -z "$$(gofmt -l cmd internal)"

fmt:
	gofmt -w cmd internal

build-pc:
	CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o pc-mcp ./cmd/pc-mcp

test-pc-sandbox:
	PC_MCP_REQUIRE_SANDBOX=1 go test -race ./internal/pc/sandbox -run 'TestLinuxSandboxIsolation|TestEscapedSessionDescendantsStop|TestStopAllCommands' -v


# Populate module cache once with network, then run reproducible offline jobs.
deps-pc:
	go mod download

install-pc: build-pc
	install -d -m 700 "$(HOME)/.local/bin"
	install -m 700 pc-mcp "$(HOME)/.local/bin/pc-mcp"

check-pc:
	go test -race ./internal/pc/...
	node --test internal/pc/ui/cards_test.cjs internal/pc/ui/lifecycle_test.cjs
	go vet ./internal/pc/...
	@test -z "$$(gofmt -l internal/pc)"

test-pc-network:
	@task_tmp=$$(mktemp -d); trap 'rm -rf "$$task_tmp"' EXIT; \
	CGO_ENABLED=0 go build -o "$$task_tmp/proxy" ./cmd/pc-job-proxy && \
	PC_MCP_REQUIRE_SANDBOX=1 PC_MCP_TEST_PROXY_HELPER="$$task_tmp/proxy" go test -race ./internal/pc/sandbox -run TestNetworkBridgeKeepsPrivateNamespace -v

test-pc-resources:
	@task_tmp=$$(mktemp -d); trap 'rm -rf "$$task_tmp"' EXIT; \
	go test -race -c -o "$$task_tmp/sandbox.test" ./internal/pc/sandbox && \
	systemd-run --user --scope --quiet --property=Delegate=yes \
	  env PC_MCP_REQUIRE_RESOURCES=1 "$$task_tmp/sandbox.test" \
	  -test.run='TestRealCgroupLimits|TestRealResourceEnforcement' -test.v

# Check the actual embedded HTML and real tools/call replies of the built server.
.PHONY: check-pc-ui-binary
check-pc-ui-binary: build-pc
	PC_MCP_UI_TEST_BINARY="$(CURDIR)/pc-mcp" node --test internal/pc/ui/binary_lifecycle_test.cjs
