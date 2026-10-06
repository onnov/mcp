//go:build linux

package sandbox

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNetworkBridgeKeepsPrivateNamespace(t *testing.T) {
	helper := os.Getenv("PC_MCP_TEST_PROXY_HELPER")
	if helper == "" {
		t.Skip("build cmd/pc-job-proxy and set PC_MCP_TEST_PROXY_HELPER")
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "via-proxy") }))
	defer upstream.Close()
	base := t.TempDir()
	rootPath := filepath.Join(base, "root")
	os.Mkdir(rootPath, 0700)
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	engine := &Engine{State: base, Cache: filepath.Join(base, "cache"), MaxSeconds: 20, AllowNetwork: true, AllowPrivateNetwork: true}
	engine.Configure(rootPath)
	engine.HelperPath = helper
	engine.HelperError = ""
	var out, stderr bytes.Buffer
	script := `test -z "$GH_TOKEN"; if curl --noproxy '*' --connect-timeout 1 -fsS "$1" >/dev/null 2>&1; then echo 'direct loopback escape'; exit 1; fi; curl --connect-timeout 5 -fsS "$1"`
	err = engine.Run(context.Background(), Spec{Root: root, CWD: ".", Args: []string{"sh", "-c", script, "sh", upstream.URL}, Network: true, Seconds: 15}, &out, &stderr)
	if err != nil {
		t.Fatalf("network bridge failed: %v %s", err, stderr.String())
	}
	if strings.TrimSpace(out.String()) != "via-proxy" {
		t.Fatal(out.String())
	}
}
