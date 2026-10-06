//go:build linux

package sandbox

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
)

func (e *Engine) configureHelper() {
	// /proc/self/exe pins the running image, including when the repository binary
	// was replaced. Never re-execute a model-writable project binary by pathname.
	f, err := os.Open("/proc/self/exe")
	if err != nil {
		e.HelperError = err.Error()
		return
	}
	defer f.Close()
	dir := filepath.Join(e.State, "helpers")
	if err = os.MkdirAll(dir, 0700); err != nil {
		e.HelperError = err.Error()
		return
	}
	temp, err := os.CreateTemp(dir, "helper-")
	if err != nil {
		e.HelperError = err.Error()
		return
	}
	name := temp.Name()
	defer os.Remove(name)
	h := sha256.New()
	_, err = io.Copy(io.MultiWriter(temp, h), f)
	if err == nil {
		err = temp.Chmod(0500)
	}
	if err == nil {
		err = temp.Sync()
	}
	closeErr := temp.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		e.HelperError = err.Error()
		return
	}
	final := filepath.Join(dir, hex.EncodeToString(h.Sum(nil)))
	if err = os.Rename(name, final); err != nil {
		e.HelperError = err.Error()
		return
	}
	e.HelperPath = final
}
