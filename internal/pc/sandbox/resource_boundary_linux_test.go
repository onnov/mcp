//go:build linux

package sandbox

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The allocation fixture may only execute inside a verified bounded job cgroup.
func TestResourceMemoryFixture(t *testing.T) {
	if os.Getenv("PC_MCP_TEST_RESOURCE_CHILD") != "1" {
		return
	}
	path, err := currentCgroup()
	if err != nil || !strings.Contains(path, "/pc-job-") {
		t.Fatal("fixture needs job cgroup")
	}
	limit, err := os.ReadFile(filepath.Join(path, "memory.max"))
	if err != nil {
		t.Fatal(err)
	}
	n, err := strconv.ParseInt(strings.TrimSpace(string(limit)), 10, 64)
	if err != nil || n > 256<<20 {
		t.Fatal("fixture needs <=256 MiB hard limit")
	}
	blocks := [][]byte{}
	for i := 0; i < 32; i++ {
		b := make([]byte, 16<<20)
		for j := 0; j < len(b); j += 4096 {
			b[j] = 1
		}
		blocks = append(blocks, b)
	}
	runtime.KeepAlive(blocks)
	t.Fatal("allocation escaped memory boundary")
}

func TestRealResourceEnforcement(t *testing.T) {
	if os.Getenv("PC_MCP_REQUIRE_RESOURCES") != "1" {
		t.Skip("requires delegated cgroup v2")
	}
	e := &Engine{RequireResources: true, MaxProcesses: 64, MemoryMax: 128 << 20}
	e.configureResources()
	if e.ResourceError != "" {
		t.Fatal(e.ResourceError)
	}
	run := func(t *testing.T, args []string, env []string, observe func(*resourceGroup)) {
		t.Helper()
		group, err := e.newResourceGroup()
		if err != nil {
			t.Fatal(err)
		}
		defer group.close()
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, args[0], args[1:]...)
		cmd.Env = append(os.Environ(), env...)
		cmd.SysProcAttr = &syscall.SysProcAttr{UseCgroupFD: true, CgroupFD: int(group.fd.Fd())}
		cmd.Cancel = group.kill
		cmd.WaitDelay = 2 * time.Second
		cmd.Stdout = io.Discard
		cmd.Stderr = io.Discard
		if err = cmd.Start(); err != nil {
			t.Fatal(err)
		}
		defer func() { group.kill(); cmd.Wait() }()
		observe(group)
	}
	event := func(group *resourceGroup, name, key string) bool {
		data, _ := os.ReadFile(filepath.Join(group.directory, name))
		for _, line := range strings.Split(string(data), "\n") {
			f := strings.Fields(line)
			if len(f) == 2 && f[0] == key {
				n, _ := strconv.Atoi(f[1])
				return n > 0
			}
		}
		return false
	}
	await := func(t *testing.T, predicate func() bool) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for !predicate() {
			if time.Now().After(deadline) {
				t.Fatal("kernel resource boundary was not enforced")
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	t.Run("memory", func(t *testing.T) {
		binary, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		run(t, []string{binary, "-test.run=^TestResourceMemoryFixture$"}, []string{"PC_MCP_TEST_RESOURCE_CHILD=1"}, func(g *resourceGroup) { await(t, func() bool { return event(g, "memory.events", "oom_kill") }) })
	})
	t.Run("processes", func(t *testing.T) {
		run(t, []string{"sh", "-c", "while :; do sleep 30 & done"}, nil, func(g *resourceGroup) { await(t, func() bool { return event(g, "pids.events", "max") }) })
	})
	t.Run("escaped-session-kill", func(t *testing.T) {
		heartbeat := filepath.Join(t.TempDir(), "heartbeat")
		run(t, []string{"sh", "-c", `setsid sh -c 'while :; do printf x >> "$1"; sleep 0.03; done' sh "$1" & wait`, "sh", heartbeat}, nil, func(g *resourceGroup) {
			await(t, func() bool { info, err := os.Stat(heartbeat); return err == nil && info.Size() > 1 })
			if err := g.kill(); err != nil {
				t.Fatal(err)
			}
			await(t, func() bool {
				data, _ := os.ReadFile(filepath.Join(g.directory, "cgroup.procs"))
				return strings.TrimSpace(string(data)) == ""
			})
			before, _ := os.ReadFile(heartbeat)
			time.Sleep(100 * time.Millisecond)
			after, _ := os.ReadFile(heartbeat)
			if string(before) != string(after) {
				t.Fatal("setsid descendant survived cgroup.kill")
			}
		})
	})
}
