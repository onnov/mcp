package sandbox

import (
	"fmt"
	"os"
)

// Namespace isolation, deadlines, cancellation, disk and output guards stay on.
// Only cgroup accounting may fall back in the explicitly selected auto mode.
func (e *Engine) resourceFallback() {
	if e.ResourceError != "" && e.BestEffortResources {
		e.RequireResources = false
		fmt.Fprintln(os.Stderr, "pc-mcp: cgroup limits unavailable; continuing in auto mode:", e.ResourceError)
	}
}
