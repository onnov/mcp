// pc-mcp runs a confined development workspace and an embedded Go tunnel.
package main

import (
	"errors"
	"flag"
	"log"
	"os"
	"runtime"

	"github.com/onnov/mcp/internal/pc/app"
)

func main() {
	runtime.GOMAXPROCS(2)
	if e := app.Run(os.Args[1:]); e != nil {
		if errors.Is(e, flag.ErrHelp) {
			return
		}
		log.Fatal(e)
	}
}
