//go:build linux

package sandbox

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

func TestMemoryBudgetUsesAvailableCapacity(t *testing.T) {
	budget, err := memoryBudget(16<<30, 0, 0)
	if err != nil || budget < 14<<30 {
		t.Fatal(budget, err)
	}
	budget, err = memoryBudget(16<<30, 1<<30, 4<<30)
	if err != nil || budget != 4<<30 {
		t.Fatal(budget, err)
	}
	if _, err = memoryBudget(256<<20, 512<<20, 0); err == nil {
		t.Fatal("host reserve ignored")
	}
}

func TestDiskGuardCancelsBeforeLaunch(t *testing.T) {
	e := &Engine{DiskReserve: 1 << 62}
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	go e.guardDisk(ctx, cancel, t.TempDir())
	select {
	case <-ctx.Done():
		if errors.Is(context.Cause(ctx), context.Canceled) {
			t.Fatal("lost resource-limit reason")
		}
	case <-time.After(time.Second):
		t.Fatal("disk guard did not cancel")
	}
}

func TestProductionResourceModeFailsClosed(t *testing.T) {
	e := &Engine{RequireResources: true, CgroupRoot: t.TempDir()}
	e.configureResources()
	if e.ResourceError == "" {
		t.Fatal("ordinary directory accepted as cgroup")
	}
}

func TestAutoResourceModeReportsFallback(t *testing.T) {
	e := &Engine{RequireResources: true, BestEffortResources: true, CgroupRoot: t.TempDir()}
	e.configureResources()
	if e.ResourceError == "" || e.RequireResources {
		t.Fatal("auto mode blocked ordinary development or hid warning")
	}
}

func TestSharedScopeIsNotDelegated(t *testing.T) {
	if !exclusiveProcesses("123\n", 123) || exclusiveProcesses("123\n456\n", 123) || exclusiveProcesses("456\n", 123) {
		t.Fatal("shared terminal scope accepted")
	}
}

// Run from a delegated scope with PC_MCP_REQUIRE_RESOURCES=1 to require the
// actual memory/PID/kill boundary instead of reporting an integration skip.
func TestRealCgroupLimits(t *testing.T) {
	if os.Getenv("PC_MCP_REQUIRE_RESOURCES") != "1" {
		t.Skip("requires delegated cgroup v2")
	}
	e := &Engine{RequireResources: true, MaxProcesses: 128}
	e.configureResources()
	if e.ResourceError != "" {
		t.Fatal(e.ResourceError)
	}
	group, err := e.newResourceGroup()
	if err != nil {
		t.Fatal(err)
	}
	defer group.close()
	for _, key := range []string{"memory.max", "pids.max", "memory.oom.group", "cgroup.kill"} {
		if _, err = os.Stat(group.directory + "/" + key); err != nil {
			t.Fatal(err)
		}
	}
}
