//go:build linux

package sandbox

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"golang.org/x/sys/unix"
)

// drmMajor is the Linux DRM character-device major number.
const drmMajor = 226

var (
	renderNodePattern = regexp.MustCompile(`^/dev/dri/renderD[0-9]+$`)
	drmNamePattern    = regexp.MustCompile(`^(renderD|card)[0-9]+$`)
	cardNamePattern   = regexp.MustCompile(`^card[0-9]+$`)
	pciAddressPattern = regexp.MustCompile(`^[0-9a-f]{4}:[0-9a-f]{2}:[0-9a-f]{2}\.[0-7]$`)
)

// gpuConfigPaths are host GPU runtime configurations bound read-only when
// present: OpenCL ICDs, Vulkan ICDs/layers, GLVND EGL vendors, Mesa driconf
// and libva. Drivers and the ICD files shipped in /usr are already visible.
var gpuConfigPaths = []string{"/etc/OpenCL/vendors", "/etc/vulkan", "/etc/glvnd", "/etc/drirc", "/etc/libva.conf"}

// gpuNode is one DRM device node of the GPU.
type gpuNode struct {
	Path       string // /dev/dri/renderD128
	Kind       string // render or card, as in /dev/dri/by-path/pci-<pci>-<kind>
	CharLink   string // /sys/dev/char/226:128
	CharTarget string // ../../devices/pci0000:00/0000:00:02.0/drm/renderD128
}

// gpuMount describes the host view a job needs to use the GPU like a normal
// desktop process: the device nodes, the PCI device's sysfs directory and the
// links that libdrm, Mesa, NEO, Level Zero and VA-API use to find it.
type gpuMount struct {
	Nodes     []gpuNode // render node first, then the card node when enabled
	PCI       string    // 0000:00:02.0
	ID        string    // 8086:46a6
	Driver    string    // i915
	Device    string    // /sys/devices/pci0000:00/0000:00:02.0
	BusLink   string    // /sys/bus/pci/devices/0000:00:02.0
	BusTarget string    // ../../../devices/pci0000:00/0000:00:02.0
	Configs   []string  // existing gpuConfigPaths
}

// args returns bwrap operations. They must follow --dev /dev and --dir /etc.
// The PCI device directory is bound read-only as a whole (vendor, device,
// revision, uevent, drm/*, gt/engine/frequency attributes); the rest of /sys,
// including other devices, /sys/kernel and /sys/module, stays absent.
func (g *gpuMount) args() []string {
	var a []string
	for _, n := range g.Nodes {
		a = append(a, "--dev-bind", n.Path, n.Path, "--symlink", "../"+filepath.Base(n.Path), "/dev/dri/by-path/pci-"+g.PCI+"-"+n.Kind)
	}
	a = append(a, "--ro-bind", g.Device, g.Device)
	for _, n := range g.Nodes {
		// /sys/class/drm and /sys/dev/char have the same depth, so the host's
		// relative link target is valid for both.
		a = append(a, "--symlink", n.CharTarget, n.CharLink, "--symlink", n.CharTarget, "/sys/class/drm/"+filepath.Base(n.Path))
	}
	a = append(a, "--symlink", g.BusTarget, g.BusLink)
	for _, p := range g.Configs {
		a = append(a, "--ro-bind", p, p)
	}
	return a
}

// discoverGPU validates the configured (or the only) render node on every call,
// so a removed node, a driver reload or lost group membership is reported
// instead of silently starting jobs without the GPU. With card it also
// requires the primary node of the same PCI device.
func discoverGPU(device string, card bool) (*gpuMount, error) {
	node := device
	if node == "" {
		nodes, _ := filepath.Glob("/dev/dri/renderD*")
		switch len(nodes) {
		case 0:
			return nil, errors.New("no /dev/dri/renderD* node on the host; is the i915/xe/amdgpu driver loaded?")
		case 1:
			node = nodes[0]
		default:
			return nil, fmt.Errorf("several render nodes (%s); choose the integrated GPU with PC_MCP_GPU_DEVICE", describeRenderNodes(nodes))
		}
	}
	if !renderNodePattern.MatchString(node) {
		return nil, fmt.Errorf("%s is not a /dev/dri/renderD* node", node)
	}
	render, err := probeDRMNode(node, "render", "add the server user to group render and start pc-mcp from a new login session")
	if err != nil {
		return nil, err
	}
	g := &gpuMount{Nodes: []gpuNode{render}}
	if g.Device, g.PCI, err = parseDRMLink(render.CharLink, render.CharTarget, node); err != nil {
		return nil, err
	}
	g.BusLink = "/sys/bus/pci/devices/" + g.PCI
	if g.BusTarget, err = os.Readlink(g.BusLink); err != nil {
		return nil, fmt.Errorf("sysfs PCI link for %s: %w", g.PCI, err)
	}
	if filepath.Join(filepath.Dir(g.BusLink), g.BusTarget) != g.Device {
		return nil, fmt.Errorf("sysfs PCI link %s does not point to %s", g.BusLink, g.Device)
	}
	if driver, err := os.Readlink(filepath.Join(g.Device, "driver")); err == nil {
		g.Driver = filepath.Base(driver)
	}
	vendor, _ := os.ReadFile(filepath.Join(g.Device, "vendor"))
	product, _ := os.ReadFile(filepath.Join(g.Device, "device"))
	if v, p := strings.TrimPrefix(strings.TrimSpace(string(vendor)), "0x"), strings.TrimPrefix(strings.TrimSpace(string(product)), "0x"); v != "" && p != "" {
		g.ID = v + ":" + p
	}
	if card {
		entries, err := os.ReadDir(filepath.Join(g.Device, "drm"))
		if err != nil {
			return nil, fmt.Errorf("sysfs DRM directory of %s: %w", g.PCI, err)
		}
		name := ""
		for _, entry := range entries {
			if cardNamePattern.MatchString(entry.Name()) {
				name = entry.Name()
				break
			}
		}
		if name == "" {
			return nil, fmt.Errorf("GPU %s has no card* node; disable PC_MCP_GPU_CARD", g.PCI)
		}
		c, err := probeDRMNode("/dev/dri/"+name, "card", "add the server user to group video and start pc-mcp from a new login session")
		if err != nil {
			return nil, err
		}
		device, _, err := parseDRMLink(c.CharLink, c.CharTarget, c.Path)
		if err != nil {
			return nil, err
		}
		if device != g.Device {
			return nil, fmt.Errorf("%s belongs to %s, not to %s", c.Path, device, g.Device)
		}
		g.Nodes = append(g.Nodes, c)
	}
	for _, p := range gpuConfigPaths {
		if _, err := os.Stat(p); err == nil {
			g.Configs = append(g.Configs, p)
		}
	}
	return g, nil
}

// probeDRMNode checks that path is a DRM character device the server may open
// read-write (as jobs will) without opening it: opening a card node could
// briefly make the server DRM master.
func probeDRMNode(path, kind, hint string) (gpuNode, error) {
	var st unix.Stat_t
	if err := unix.Stat(path, &st); err != nil {
		return gpuNode{}, fmt.Errorf("DRM node %s: %w", path, err)
	}
	if st.Mode&unix.S_IFMT != unix.S_IFCHR || unix.Major(st.Rdev) != drmMajor {
		return gpuNode{}, fmt.Errorf("%s is not a DRM character device", path)
	}
	if err := unix.Access(path, unix.R_OK|unix.W_OK); err != nil {
		return gpuNode{}, fmt.Errorf("cannot open %s: %w; %s", path, err, hint)
	}
	n := gpuNode{Path: path, Kind: kind, CharLink: fmt.Sprintf("/sys/dev/char/%d:%d", drmMajor, unix.Minor(st.Rdev))}
	var err error
	if n.CharTarget, err = os.Readlink(n.CharLink); err != nil {
		return gpuNode{}, fmt.Errorf("sysfs link for %s: %w", path, err)
	}
	return n, nil
}

// describeRenderNodes lists render nodes with PCI address, ID and driver so the
// operator can pick the integrated GPU (Intel iGPUs are usually 0000:00:02.0).
func describeRenderNodes(nodes []string) string {
	var parts []string
	for _, node := range nodes {
		desc := node
		var st unix.Stat_t
		if unix.Stat(node, &st) == nil {
			link := fmt.Sprintf("/sys/dev/char/%d:%d", drmMajor, unix.Minor(st.Rdev))
			if target, err := os.Readlink(link); err == nil {
				if device, pci, err := parseDRMLink(link, target, node); err == nil {
					desc += " " + pci
					if driver, err := os.Readlink(filepath.Join(device, "driver")); err == nil {
						desc += " " + filepath.Base(driver)
					}
				}
			}
		}
		parts = append(parts, desc)
	}
	return strings.Join(parts, ", ")
}

// parseDRMLink checks that /sys/dev/char/226:N resolves to
// /sys/devices/.../<pci>/drm/<node name> and returns the PCI device directory.
func parseDRMLink(link, target, node string) (device, pci string, err error) {
	resolved := filepath.Join(filepath.Dir(link), target)
	device = filepath.Dir(filepath.Dir(resolved))
	pci = filepath.Base(device)
	name := filepath.Base(resolved)
	if !strings.HasPrefix(resolved, "/sys/devices/") || name != filepath.Base(node) || !drmNamePattern.MatchString(name) ||
		filepath.Base(filepath.Dir(resolved)) != "drm" || !pciAddressPattern.MatchString(pci) {
		return "", "", fmt.Errorf("%s -> %s is not a PCI DRM node; only PCI GPUs are supported", link, target)
	}
	return device, pci, nil
}

func (e *Engine) gpu() (*gpuMount, error) {
	if !e.AllowGPU {
		return nil, nil
	}
	g, err := discoverGPU(e.GPUDevice, e.GPUCard)
	if err != nil {
		return nil, fmt.Errorf("GPU access is enabled (PC_MCP_ALLOW_GPU) but unavailable: %w", err)
	}
	return g, nil
}

func (e *Engine) gpuCapabilities(m map[string]any) error {
	m["gpu_allowed"] = e.AllowGPU
	g, err := e.gpu()
	if err != nil {
		m["gpu_error"] = err.Error()
		return err
	}
	if g != nil {
		var nodes []string
		for _, n := range g.Nodes {
			nodes = append(nodes, n.Path)
		}
		m["gpu_device"] = g.Nodes[0].Path
		m["gpu_nodes"] = nodes
		m["gpu_pci"] = g.PCI
		m["gpu_pci_id"] = g.ID
		m["gpu_driver"] = g.Driver
		m["gpu_sysfs"] = g.Device
		m["gpu_configs"] = g.Configs
	}
	return nil
}
