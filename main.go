// Compatibility entry point for existing hosting build scripts.
// New installations should build ./cmd/github-mcp.
package main

import (
	"github.com/onnov/mcp/internal/app"
	"log"
	"os"
)

func main() {
	if err := app.Run(os.Args[1:]); err != nil {
		log.Fatal(err)
	}
}
