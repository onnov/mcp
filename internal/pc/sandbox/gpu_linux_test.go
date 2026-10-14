//go:build linux

package sandbox

import (
	"bytes"
	"context"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func roBindExisting(paths ...string) []string {
	var a []string
	for _, p := range paths {
		if _, err := os.Stat(p); err == nil {
			a = append(a, "--ro-bind", p, p)
		}
	}
	return a
}

func setenvArgs(vars map[string]string) []string {
	var a []string
	for _, k := range slices.Sorted(maps.Keys(vars)) {
		a = append(a, "--setenv", k, vars[k])
	}
	return a
}

var testPrlimit = []string{"/usr/bin/prlimit", "--nofile=4096:4096", "--fsize=1024:1024", "--"}

// legacyArgs is the bwrap argument list of the sandbox before GPU support.
// It is written out by hand so that any change to the default sandbox fails here.
func legacyArgs(network bool) []string {
	a := []string{"--die-with-parent", "--new-session", "--unshare-all", "--cap-drop", "ALL", "--clearenv"}
	a = append(a, roBindExisting("/usr", "/bin", "/sbin", "/lib", "/lib64")...)
	a = append(a, "--proc", "/proc", "--dev", "/dev", "--tmpfs", "/tmp", "--dir", "/home", "--dir", "/home/pc", "--dir", "/etc", "--dir", "/run")
	a = append(a, roBindExisting("/etc/ssl/certs", "/etc/ssl/openssl.cnf", "/etc/alternatives", "/etc/ld.so.cache", "/etc/localtime")...)
	if network {
		a = append(a, roBindExisting("/etc/resolv.conf", "/etc/hosts", "/etc/nsswitch.conf")...)
	}
	a = append(a, "--dir", "/workspace", "--dir", "/cache", "--bind", "/proc/self/fd/4", "/cache", "--bind", "/proc/self/fd/3", "/workspace")
	vars := map[string]string{"PATH": "/usr/local/go/bin:/usr/local/bin:/usr/bin:/bin", "HOME": "/home/pc", "TMPDIR": "/tmp", "LANG": "C.UTF-8", "GOCACHE": "/cache/go-build", "GOMODCACHE": "/cache/go-mod", "GOPATH": "/cache/go", "GOTOOLCHAIN": "local", "GOTELEMETRY": "off", "XDG_CACHE_HOME": "/cache", "PIP_CACHE_DIR": "/cache/pip", "npm_config_cache": "/cache/npm", "GIT_CONFIG_NOSYSTEM": "1", "GIT_CONFIG_GLOBAL": "/dev/null", "GIT_TERMINAL_PROMPT": "0", "GH_CONFIG_DIR": "/home/pc/.config/gh", "GH_PROMPT_DISABLED": "1"}
	wrapper := `exec 3<&-; exec 4<&-; exec "$@"`
	if network {
		a = append(a, "--ro-bind", "/state/proxy", "/run/pc-mcp", "--ro-bind", "/opt/pc-mcp-helper", "/pc-mcp-helper")
		for _, key := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy"} {
			vars[key] = "http://127.0.0.1:3128"
		}
		vars["NO_PROXY"] = ""
		vars["no_proxy"] = ""
		wrapper = `exec 3<&-; exec 4<&-; /pc-mcp-helper --job-proxy /run/pc-mcp/net.sock & n=0; while ! test -f /tmp/pc-mcp-proxy-ready; do n=$((n+1)); if test "$n" -ge 100; then echo 'network helper did not start' >&2; exit 125; fi; sleep 0.05; done; exec "$@"`
	}
	a = append(a, setenvArgs(vars)...)
	a = append(a, "--chdir", "/workspace/sub", "--", "/bin/sh", "-c", wrapper, "pc-mcp")
	a = append(a, testPrlimit...)
	return append(a, "go", "test", "./...")
}

func testGPUMount(card bool) *gpuMount {
	g := &gpuMount{
		Nodes: []gpuNode{{Path: "/dev/dri/renderD128", Kind: "render", CharLink: "/sys/dev/char/226:128", CharTarget: "../../devices/pci0000:00/0000:00:02.0/drm/renderD128"}},
		PCI:   "0000:00:02.0", ID: "8086:46a6", Driver: "i915", Device: "/sys/devices/pci0000:00/0000:00:02.0",
		BusLink: "/sys/bus/pci/devices/0000:00:02.0", BusTarget: "../../../devices/pci0000:00/0000:00:02.0",
		Configs: []string{"/etc/OpenCL/vendors", "/etc/vulkan"},
	}
	if card {
		g.Nodes = append(g.Nodes, gpuNode{Path: "/dev/dri/card1", Kind: "card", CharLink: "/sys/dev/char/226:1", CharTarget: "../../devices/pci0000:00/0000:00:02.0/drm/card1"})
	}
	return g
}

func TestBwrapArgsWithoutGPU(t *testing.T) {
	// GPUDevice/GPUCard alone must not enable anything: only AllowGPU does.
	e := &Engine{HelperPath: "/opt/pc-mcp-helper", GPUDevice: "/dev/dri/renderD128", GPUCard: true}
	gpu, err := e.gpu()
	if err != nil || gpu != nil {
		t.Fatalf("GPU mount without AllowGPU: %v %v", gpu, err)
	}
	for _, network := range []bool{false, true} {
		proxyDir := ""
		if network {
			proxyDir = "/state/proxy"
		}
		got, err := e.bwrapArgs(Spec{CWD: "sub", Args: []string{"go", "test", "./..."}, Network: network}, proxyDir, false, gpu, testPrlimit)
		if err != nil {
			t.Fatal(err)
		}
		if want := legacyArgs(network); !slices.Equal(got, want) {
			t.Fatalf("network=%v: default sandbox arguments changed\n got: %q\nwant: %q", network, got, want)
		}
		for _, arg := range got {
			if strings.Contains(arg, "/dev/dri") || strings.HasPrefix(arg, "/sys") || strings.HasPrefix(arg, "/etc/OpenCL") || strings.HasPrefix(arg, "/etc/vulkan") {
				t.Fatalf("GPU path %q in default sandbox", arg)
			}
		}
	}
}

func TestBwrapArgsWithGPU(t *testing.T) {
	e := &Engine{}
	for _, card := range []bool{false, true} {
		g := testGPUMount(card)
		got, err := e.bwrapArgs(Spec{CWD: "sub", Args: []string{"go", "test", "./..."}}, "", false, g, testPrlimit)
		if err != nil {
			t.Fatal(err)
		}
		segment := []string{"--dev-bind", "/dev/dri/renderD128", "/dev/dri/renderD128", "--symlink", "../renderD128", "/dev/dri/by-path/pci-0000:00:02.0-render"}
		if card {
			segment = append(segment, "--dev-bind", "/dev/dri/card1", "/dev/dri/card1", "--symlink", "../card1", "/dev/dri/by-path/pci-0000:00:02.0-card")
		}
		segment = append(segment,
			"--ro-bind", "/sys/devices/pci0000:00/0000:00:02.0", "/sys/devices/pci0000:00/0000:00:02.0",
			"--symlink", "../../devices/pci0000:00/0000:00:02.0/drm/renderD128", "/sys/dev/char/226:128",
			"--symlink", "../../devices/pci0000:00/0000:00:02.0/drm/renderD128", "/sys/class/drm/renderD128",
		)
		if card {
			segment = append(segment,
				"--symlink", "../../devices/pci0000:00/0000:00:02.0/drm/card1", "/sys/dev/char/226:1",
				"--symlink", "../../devices/pci0000:00/0000:00:02.0/drm/card1", "/sys/class/drm/card1",
			)
		}
		segment = append(segment,
			"--symlink", "../../../devices/pci0000:00/0000:00:02.0", "/sys/bus/pci/devices/0000:00:02.0",
			"--ro-bind", "/etc/OpenCL/vendors", "/etc/OpenCL/vendors",
			"--ro-bind", "/etc/vulkan", "/etc/vulkan",
		)
		// Exactly the default list plus the GPU segment just before /workspace.
		legacy := legacyArgs(false)
		at := slices.Index(legacy, "/workspace") - 1
		want := slices.Concat(legacy[:at], segment, legacy[at:])
		if !slices.Equal(got, want) {
			t.Fatalf("card=%v: GPU sandbox arguments\n got: %q\nwant: %q", card, got, want)
		}
		if !slices.Equal(got[:6], []string{"--die-with-parent", "--new-session", "--unshare-all", "--cap-drop", "ALL", "--clearenv"}) {
			t.Fatal("isolation flags weakened")
		}
		if slices.Index(got, "--dev-bind") < slices.Index(got, "/dev") {
			t.Fatal("render node must be bound after --dev /dev")
		}
		for i, arg := range got {
			if arg == "/sys" || arg == "/sys/class" || arg == "/sys/devices" || arg == "/dev/dri" || (!card && strings.HasPrefix(arg, "/dev/dri/card")) {
				t.Fatalf("unexpected GPU exposure %q", arg)
			}
			if arg == "--bind" && strings.HasPrefix(got[i+1], "/sys") {
				t.Fatalf("writable sysfs bind %q", got[i+1])
			}
		}
	}
}

func TestParseDRMLink(t *testing.T) {
	for node, target := range map[string]string{
		"/dev/dri/renderD128": "../../devices/pci0000:00/0000:00:02.0/drm/renderD128",
		"/dev/dri/card1":      "../../devices/pci0000:00/0000:00:02.0/drm/card1",
	} {
		device, pci, err := parseDRMLink("/sys/dev/char/226:128", target, node)
		if err != nil || device != "/sys/devices/pci0000:00/0000:00:02.0" || pci != "0000:00:02.0" {
			t.Fatal(node, device, pci, err)
		}
	}
	for _, target := range []string{
		"../../devices/pci0000:00/0000:00:02.0/drm/renderD129",          // another node
		"../../devices/platform/vgem/drm/renderD128",                    // not PCI
		"../../../../etc/drm/renderD128",                                // outside /sys/devices
		"../../devices/pci0000:00/0000:00:02.0/renderD128",              // not under drm
		"../../devices/pci0000:00/0000:00:02.0/../../../drm/renderD128", // escapes
		"../../devices/pci0000:00/drm/renderD128",                       // root port, not a device
	} {
		if _, _, err := parseDRMLink("/sys/dev/char/226:128", target, "/dev/dri/renderD128"); err == nil {
			t.Fatalf("accepted %s", target)
		}
	}
	if _, _, err := parseDRMLink("/sys/dev/char/226:128", "../../devices/pci0000:00/0000:00:02.0/drm/controlD64", "/dev/dri/controlD64"); err == nil {
		t.Fatal("accepted a non render/card node")
	}
}

func TestGPUEnabledButUnavailableIsReported(t *testing.T) {
	e := &Engine{AllowGPU: true, GPUDevice: "/dev/dri/renderD999", MaxSeconds: 10}
	if _, err := e.gpu(); err == nil || !strings.Contains(err.Error(), "PC_MCP_ALLOW_GPU") {
		t.Fatalf("missing render node not reported: %v", err)
	}
	m := e.Capabilities()
	if m["gpu_allowed"] != true || m["gpu_error"] == nil || m["execution_ready"] != false {
		t.Fatalf("capabilities hide GPU failure: %v", m)
	}
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err := e.run(context.Background(), Spec{Root: root, CWD: ".", Args: []string{"true"}, Seconds: 5}, &bytes.Buffer{}, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "GPU") {
		t.Fatalf("job started without the enabled GPU: %v", err)
	}
	if m := (&Engine{}).Capabilities(); m["gpu_allowed"] != false || m["gpu_error"] != nil {
		t.Fatalf("disabled GPU reported as error: %v", m)
	}
}

func TestNoGPUByDefault(t *testing.T) {
	root, engine := sandboxForTest(t)
	defer root.Close()
	var out, stderr bytes.Buffer
	err := engine.Run(context.Background(), Spec{Root: root, CWD: ".", Args: []string{"sh", "-c", `! test -e /dev/dri && ! test -e /sys && ! test -e /etc/OpenCL && ! test -e /etc/vulkan && echo no-gpu`}, Seconds: 10}, &out, &stderr)
	if err != nil || !strings.Contains(out.String(), "no-gpu") {
		t.Fatalf("default sandbox exposes GPU paths: %v %s %s", err, out.String(), stderr.String())
	}
}

// TestGPURenderNodeInSandbox needs a host render node the test user can open.
// It is skipped without one unless PC_MCP_REQUIRE_GPU=1 (make test-pc-gpu).
// PC_MCP_GPU_CARD=true also checks the card node. Installed clinfo, vulkaninfo
// and vainfo are run inside the sandbox and must find the GPU.
func TestGPURenderNodeInSandbox(t *testing.T) {
	card := os.Getenv("PC_MCP_GPU_CARD") == "true"
	g, err := discoverGPU(os.Getenv("PC_MCP_GPU_DEVICE"), card)
	if err != nil {
		if os.Getenv("PC_MCP_REQUIRE_GPU") == "1" {
			t.Fatal(err)
		}
		t.Skip("no usable render node: " + err.Error())
	}
	root, engine := sandboxForTest(t)
	defer root.Close()
	engine.AllowGPU = true
	engine.GPUDevice = g.Nodes[0].Path
	engine.GPUCard = card
	if m := engine.Capabilities(); m["gpu_device"] != g.Nodes[0].Path || m["gpu_error"] != nil {
		t.Fatalf("capabilities: %v", m)
	}
	script := `set -e
pci=$1; dev=$2; shift 2
for n in "$@"; do
  test -c "$n"; exec 5<>"$n"; exec 5<&-
  base=${n##*/}
  test "$(readlink -f "/sys/class/drm/$base")" = "$dev/drm/$base"
  test "$(readlink -f "$dev/drm/$base/device")" = "$dev"
done
for n in /dev/dri/*; do
  test "$n" = /dev/dri/by-path && continue
  ok=; for m in "$@"; do test "$n" = "$m" && ok=1; done
  test -n "$ok" || { echo "unexpected $n"; exit 1; }
done
for l in /dev/dri/by-path/*; do test -c "$l"; done
! test -e /sys/kernel
! test -e /sys/module
! test -e /sys/class/net
test "$(readlink -f "/sys/bus/pci/devices/$pci")" = "$dev"
cat "$dev/vendor" "$dev/device" "$dev/revision" >/dev/null
grep -q "PCI_SLOT_NAME=$pci" "$dev/uevent"
test "$(basename "$(readlink "$dev/subsystem")")" = pci
if (: >"$dev/drm/new-file") 2>/dev/null; then echo "writable sysfs"; exit 1; fi
echo gpu-ok
if command -v clinfo >/dev/null; then clinfo -l; fi
if command -v vulkaninfo >/dev/null; then vulkaninfo --summary 2>&1 | grep -E 'deviceName|driverName' || echo 'vulkan: no device'; fi
if command -v vainfo >/dev/null; then vainfo --display drm --device "$1" 2>&1 | grep -E 'Driver version|VA-API version' || echo 'vaapi: no driver'; fi`
	args := []string{"sh", "-c", script, "gpu", g.PCI, g.Device}
	for _, n := range g.Nodes {
		args = append(args, n.Path)
	}
	var out, stderr bytes.Buffer
	if err := engine.Run(context.Background(), Spec{Root: root, CWD: ".", Args: args, Seconds: 120}, &out, &stderr); err != nil {
		t.Fatalf("GPU job failed: %v\n%s\n%s", err, out.String(), stderr.String())
	}
	t.Log(out.String())
	if !strings.Contains(out.String(), "gpu-ok") {
		t.Fatal(out.String())
	}
	if strings.Contains(out.String(), "Platform") && !strings.Contains(out.String(), "Device #") {
		t.Fatalf("OpenCL platform without device in sandbox:\n%s", out.String())
	}
	for _, failure := range []string{"vulkan: no device", "vaapi: no driver"} {
		if strings.Contains(out.String(), failure) {
			t.Fatalf("%s in sandbox:\n%s\n%s", failure, out.String(), stderr.String())
		}
	}
}

func sandboxForTest(t *testing.T) (*os.Root, *Engine) {
	t.Helper()
	if err := checkPlatform(); err != nil {
		if os.Getenv("PC_MCP_REQUIRE_SANDBOX") == "1" {
			t.Fatal(err)
		}
		t.Skip(err)
	}
	base := t.TempDir()
	rootPath := filepath.Join(base, "root")
	if err := os.Mkdir(rootPath, 0700); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	engine := &Engine{Cache: filepath.Join(base, "cache"), MaxSeconds: 60}
	engine.Configure(rootPath)
	return root, engine
}
