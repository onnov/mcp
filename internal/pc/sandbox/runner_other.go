//go:build !linux

package sandbox

import (
	"context"
	"errors"
	"io"
)

func checkPlatform() error {
	return errors.New("command execution requires Linux bubblewrap; file tools still work on this OS")
}
func (e *Engine) run(context.Context, Spec, io.Writer, io.Writer) error { return checkPlatform() }

func (e *Engine) checkPlatform() error { return checkPlatform() }
