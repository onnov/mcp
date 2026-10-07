//go:build linux

package sandbox

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFailClosedOldBubblewrap(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "bwrap"), []byte("#!/bin/sh\necho 'bubblewrap 0.9.0'\n"), 0700)
	t.Setenv("PATH", dir)
	if e := checkPlatform(); e == nil {
		t.Fatal("unsafe bubblewrap version accepted")
	}
}
func TestLinuxSandboxIsolation(t *testing.T) {
	if e := checkPlatform(); e != nil {
		if os.Getenv("PC_MCP_REQUIRE_SANDBOX") == "1" {
			t.Fatal(e)
		}
		t.Skip("integration requires working bubblewrap >=0.12: " + e.Error())
	}
	base := t.TempDir()
	rootPath := filepath.Join(base, "work")
	os.Mkdir(rootPath, 0700)
	secret := filepath.Join(base, "outside-secret")
	os.WriteFile(secret, []byte("secret"), 0600)
	os.Symlink(secret, filepath.Join(rootPath, "escape"))
	root, e := os.OpenRoot(rootPath)
	if e != nil {
		t.Fatal(e)
	}
	defer root.Close()
	engine := &Engine{Cache: filepath.Join(base, "cache"), MaxSeconds: 10}
	engine.Configure(rootPath)
	var out, er bytes.Buffer
	e = engine.Run(context.Background(), Spec{Root: root, CWD: ".", Args: []string{"sh", "-c", `test -z "$CONTROL_PLANE_API_KEY" && test -z "$GH_TOKEN" && ! test -e /etc/ssl/private && ! test -e /proc/self/fd/3 && ! test -e /proc/self/fd/4 && ! cat /workspace/escape && ! test -e "$1" && touch /workspace/inside && echo isolated`, "sh", secret}, Seconds: 5}, &out, &er)
	if e != nil {
		if os.Getenv("PC_MCP_REQUIRE_SANDBOX") == "1" {
			t.Fatalf("sandbox integration failed: %v %s", e, er.String())
		}
		t.Skipf("host namespaces unavailable: %v %s", e, er.String())
	}
	if !strings.Contains(out.String(), "isolated") {
		t.Fatal(out.String())
	}
	if _, e = os.Stat(filepath.Join(rootPath, "inside")); e != nil {
		t.Fatal(e)
	}
	out.Reset()
	er.Reset()
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	start := time.Now()
	e = engine.Run(ctx, Spec{Root: root, CWD: ".", Args: []string{"sh", "-c", "sleep 100 & wait"}, Seconds: 5}, &out, &er)
	if !errors.Is(e, context.DeadlineExceeded) || time.Since(start) > 3*time.Second {
		t.Fatalf("descendant cancellation failed %v", e)
	}
}
func TestSandboxExecutableCannotBeProjectControlled(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "bwrap"), []byte("#!/bin/sh\necho 'bubblewrap 0.12.0'\n"), 0700)
	t.Setenv("PATH", root)
	e := &Engine{Cache: filepath.Join(t.TempDir(), "cache"), MaxSeconds: 10}
	e.Configure(root)
	if e.BwrapPath != "" || e.BwrapError == "" {
		t.Fatal("project executable became sandbox boundary")
	}
}

func TestEscapedSessionDescendantsStop(t *testing.T) {
	if err := checkPlatform(); err != nil {
		if os.Getenv("PC_MCP_REQUIRE_SANDBOX") == "1" {
			t.Fatal(err)
		}
		t.Skip(err)
	}
	base := t.TempDir()
	rootPath := filepath.Join(base, "root")
	os.Mkdir(rootPath, 0700)
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	engine := &Engine{Cache: filepath.Join(base, "cache"), MaxSeconds: 10}
	engine.Configure(rootPath)
	var out, stderr bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	err = engine.Run(ctx, Spec{Root: root, CWD: ".", Args: []string{"sh", "-c", `setsid sh -c 'while :; do echo alive >> /workspace/heartbeat; sleep 0.02; done' & wait`}, Seconds: 5}, &out, &stderr)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cancel did not stop session: %v %s", err, stderr.String())
	}
	before, err := os.ReadFile(filepath.Join(rootPath, "heartbeat"))
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond)
	after, err := os.ReadFile(filepath.Join(rootPath, "heartbeat"))
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatal("escaped-session descendant survived job cancellation")
	}
}

func TestStopAllCommands(t *testing.T) {
	if err := checkPlatform(); err != nil {
		if os.Getenv("PC_MCP_REQUIRE_SANDBOX") == "1" {
			t.Fatal(err)
		}
		t.Skip(err)
	}
	base := t.TempDir()
	rootPath := filepath.Join(base, "root")
	os.Mkdir(rootPath, 0700)
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	engine := &Engine{Cache: filepath.Join(base, "cache"), MaxSeconds: 30}
	engine.Configure(rootPath)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() {
			results <- engine.Run(ctx, Spec{Root: root, CWD: ".", Args: []string{"sh", "-c", "sleep 30 & wait"}, Seconds: 30}, io.Discard, io.Discard)
		}()
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		engine.activeMu.Lock()
		active := len(engine.active)
		engine.activeMu.Unlock()
		if active == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("commands did not register")
		}
		time.Sleep(time.Millisecond)
	}
	engine.StopAll()
	for i := 0; i < 2; i++ {
		select {
		case err := <-results:
			if !errors.Is(err, context.Canceled) {
				t.Fatal("stop-all lost cancellation", err)
			}
		case <-ctx.Done():
			t.Fatal("stop-all did not complete")
		}
	}
	engine.activeMu.Lock()
	remaining := len(engine.active)
	engine.activeMu.Unlock()
	if remaining != 0 {
		t.Fatal("terminated commands retained in registry")
	}
}
