//go:build linux

package sandbox

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// ResourceLimits reserves capacity for the desktop/server and gives a job the
// remaining capacity. CPU has no quota; the scheduler can use every idle core.
type resourceGroup struct {
	directory string
	fd        *os.File
}

func (g *resourceGroup) kill() error {
	return os.WriteFile(filepath.Join(g.directory, "cgroup.kill"), []byte("1"), 0600)
}
func (g *resourceGroup) close() {
	g.kill()
	g.fd.Close()
	// cgroup removal needs all killed tasks to be reaped by the kernel.
	deadline := time.Now().Add(2 * time.Second)
	for {
		if os.Remove(g.directory) == nil || time.Now().After(deadline) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func currentCgroup() (string, error) {
	b, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(b), "\n") {
		if p, ok := strings.CutPrefix(line, "0::"); ok {
			path := filepath.Join("/sys/fs/cgroup", filepath.Clean("/"+p))
			if path == "/sys/fs/cgroup" {
				return "", errors.New("a delegated cgroup v2 scope is required")
			}
			return path, nil
		}
	}
	return "", errors.New("cgroup v2 is required")
}

func (e *Engine) configureResources() {
	if !e.RequireResources {
		return
	}
	defer e.resourceFallback()
	root := e.CgroupRoot
	if root == "" {
		var err error
		root, err = currentCgroup()
		if err != nil {
			e.ResourceError = err.Error()
			return
		}
	}
	root, err := filepath.EvalSymlinks(root)
	if err != nil || !strings.HasPrefix(root, "/sys/fs/cgroup/") {
		e.ResourceError = "cgroup-root must be a delegated directory inside /sys/fs/cgroup"
		return
	}
	control := filepath.Join(root, "pc-mcp-control")
	// A terminal scope contains its shell and unrelated tasks. Do not move the
	// server before discovering that domain controllers cannot be enabled there.
	procs, err := os.ReadFile(filepath.Join(root, "cgroup.procs"))
	if err != nil {
		e.ResourceError = err.Error()
		return
	}
	if !exclusiveProcesses(string(procs), os.Getpid()) {
		e.ResourceError = "cgroup scope is shared; use scripts/run-pc-secure.sh for delegated resource limits"
		return
	}
	if err = os.Mkdir(control, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		e.ResourceError = err.Error()
		return
	}
	// Move the server out of the parent before enabling domain controllers.
	// This is only allowed inside an explicitly delegated systemd scope.
	if err = os.WriteFile(filepath.Join(control, "cgroup.procs"), []byte(strconv.Itoa(os.Getpid())), 0600); err != nil {
		e.ResourceError = "start through scripts/run-pc-secure.sh (systemd delegation): " + err.Error()
		return
	}
	if err = os.WriteFile(filepath.Join(root, "cgroup.subtree_control"), []byte("+memory +pids"), 0600); err != nil {
		e.ResourceError = err.Error()
		return
	}
	available, err := availableMemory()
	if err != nil {
		e.ResourceError = err.Error()
		return
	}
	budget, err := memoryBudget(available, e.MemoryReserve, e.MemoryMax)
	if err != nil {
		e.ResourceError = err.Error()
		return
	}
	jobs := filepath.Join(root, "pc-mcp-jobs")
	if err = os.Mkdir(jobs, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		e.ResourceError = err.Error()
		return
	}
	pids := e.MaxProcesses
	if pids == 0 {
		pids = 4096
	}
	for key, value := range map[string]string{"memory.max": strconv.FormatInt(budget, 10), "memory.swap.max": "0", "pids.max": strconv.Itoa(pids), "cgroup.subtree_control": "+memory +pids"} {
		if err = os.WriteFile(filepath.Join(jobs, key), []byte(value), 0600); err != nil {
			e.ResourceError = err.Error()
			return
		}
	}
	e.CgroupRoot = jobs
	// Fail before the first job if cgroup.kill or memory accounting is unavailable.
	probe, err := e.newResourceGroup()
	if err != nil {
		e.ResourceError = err.Error()
		return
	}
	probe.close()
}

func exclusiveProcesses(procs string, pid int) bool {
	for _, p := range strings.Fields(procs) {
		if p != strconv.Itoa(pid) {
			return false
		}
	}
	return true
}

func availableMemory() (int64, error) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0, err
	}
	defer f.Close()
	scan := bufio.NewScanner(f)
	for scan.Scan() {
		fields := strings.Fields(scan.Text())
		if len(fields) >= 2 && fields[0] == "MemAvailable:" {
			n, err := strconv.ParseInt(fields[1], 10, 64)
			return n * 1024, err
		}
	}
	return 0, errors.New("MemAvailable is unavailable")
}
func memoryBudget(available, reserve, ceiling int64) (int64, error) {
	if reserve == 0 {
		reserve = max(512<<20, available/10)
	}
	budget := available - reserve
	if ceiling > 0 {
		budget = min(budget, ceiling)
	}
	if budget < 128<<20 {
		return 0, errors.New("not enough memory after host reserve")
	}
	return budget, nil
}

func (e *Engine) newResourceGroup() (*resourceGroup, error) {
	e.resourceMu.Lock()
	defer e.resourceMu.Unlock()
	// Re-evaluate the aggregate pool before launch, including RAM already held
	// by plugin jobs. Freed host RAM becomes available without restarting.
	usedData, err := os.ReadFile(filepath.Join(e.CgroupRoot, "memory.current"))
	if err != nil {
		return nil, err
	}
	used, err := strconv.ParseInt(strings.TrimSpace(string(usedData)), 10, 64)
	if err != nil {
		return nil, err
	}
	free, err := availableMemory()
	if err != nil {
		return nil, err
	}
	pool, err := memoryBudget(free+used, e.MemoryReserve, e.MemoryMax)
	if err != nil {
		return nil, err
	}
	// Do not evict existing work merely because another host process grew.
	pool = max(pool, used)
	if err = os.WriteFile(filepath.Join(e.CgroupRoot, "memory.max"), []byte(strconv.FormatInt(pool, 10)), 0600); err != nil {
		return nil, err
	}
	available, err := availableMemory()
	if err != nil {
		return nil, err
	}
	budget, err := memoryBudget(available, e.MemoryReserve, e.MemoryMax)
	if err != nil {
		return nil, err
	}
	// Respect tighter ancestors (containers/systemd services), not only host RAM.
	for parent := e.CgroupRoot; strings.HasPrefix(parent, "/sys/fs/cgroup"); parent = filepath.Dir(parent) {
		maxData, readErr := os.ReadFile(filepath.Join(parent, "memory.max"))
		if readErr == nil {
			ceiling, parseErr := strconv.ParseInt(strings.TrimSpace(string(maxData)), 10, 64)
			usedData, _ := os.ReadFile(filepath.Join(parent, "memory.current"))
			used, _ := strconv.ParseInt(strings.TrimSpace(string(usedData)), 10, 64)
			if parseErr == nil {
				budget = min(budget, ceiling-used)
			}
		}
		if parent == "/sys/fs/cgroup" {
			break
		}
	}
	if budget < 128<<20 {
		return nil, errors.New("insufficient cgroup memory headroom")
	}
	dir, err := os.MkdirTemp(e.CgroupRoot, "pc-job-")
	if err != nil {
		return nil, fmt.Errorf("delegated cgroup unavailable: %w", err)
	}
	fail := func(err error) (*resourceGroup, error) { os.Remove(dir); return nil, err }
	pids := e.MaxProcesses
	if pids == 0 {
		pids = 4096
	}
	for key, value := range map[string]string{"memory.max": strconv.FormatInt(budget, 10), "memory.swap.max": "0", "memory.oom.group": "1", "pids.max": strconv.Itoa(pids)} {
		if err = os.WriteFile(filepath.Join(dir, key), []byte(value), 0600); err != nil {
			return fail(fmt.Errorf("set %s: %w", key, err))
		}
	}
	if _, err = os.Stat(filepath.Join(dir, "cgroup.kill")); err != nil {
		return fail(errors.New("Linux cgroup.kill is required (kernel >=5.14)"))
	}
	fd, err := os.Open(dir)
	if err != nil {
		return fail(err)
	}
	return &resourceGroup{dir, fd}, nil
}

func diskAvailable(path string) (int64, uint64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, 0, err
	}
	return int64(st.Bavail) * int64(st.Bsize), st.Ffree, nil
}
func (e *Engine) diskReserve() int64 {
	if e.DiskReserve > 0 {
		return e.DiskReserve
	}
	return 1 << 30
}
func (e *Engine) guardDisk(ctx context.Context, cancel context.CancelCauseFunc, paths ...string) {
	check := func() bool {
		for _, path := range paths {
			free, inodes, err := diskAvailable(path)
			if err != nil || free < e.diskReserve() || inodes < 1024 {
				cancel(errors.New("job stopped to preserve host disk/inode reserve"))
				return false
			}
		}
		return true
	}
	if !check() {
		return
	}
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if !check() {
				return
			}
		}
	}
}
