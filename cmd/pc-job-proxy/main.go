// pc-job-proxy is the dependency-free bridge fixture for sandbox integration.
// Production uses a pinned copy of pc-mcp with the same --job-proxy entry point.
package main

import (
	"context"
	"log"
	"os"

	"github.com/onnov/mcp/internal/pc/egress"
)

func main() {
	if len(os.Args) != 3 || os.Args[1] != "--job-proxy" {
		log.Fatal("expected --job-proxy socket")
	}
	if err := egress.Bridge(context.Background(), os.Args[2]); err != nil {
		log.Fatal(err)
	}
}
