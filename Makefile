.PHONY: build test check fmt

build:
	CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o github_public_mcp ./cmd/github-mcp

test:
	go test -race ./...
	node --test internal/ui/picker_test.cjs

check: test
	go vet ./...
	@test -z "$$(gofmt -l cmd internal main.go)"

fmt:
	gofmt -w cmd internal main.go
