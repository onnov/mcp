//go:build !linux

package sandbox

import "errors"

func (e *Engine) gpuCapabilities(m map[string]any) error {
	m["gpu_allowed"] = e.AllowGPU
	if !e.AllowGPU {
		return nil
	}
	err := errors.New("GPU access is enabled (PC_MCP_ALLOW_GPU) but requires Linux")
	m["gpu_error"] = err.Error()
	return err
}
