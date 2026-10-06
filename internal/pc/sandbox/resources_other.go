//go:build !linux

package sandbox

func (e *Engine) configureResources() {
	defer e.resourceFallback()
	if e.RequireResources {
		e.ResourceError = "resource-isolated execution requires Linux cgroup v2"
	}
}
