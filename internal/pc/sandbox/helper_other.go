//go:build !linux

package sandbox

func (e *Engine) configureHelper() { e.HelperError = "job egress requires Linux" }
