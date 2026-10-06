package app

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/onnov/mcp/internal/pc/jobs"
	"github.com/onnov/mcp/internal/pc/sandbox"
	"github.com/onnov/mcp/internal/pc/tools"
	"github.com/onnov/mcp/internal/pc/workspace"
	tunnelclient "github.com/openai/tunnel-client"
	"github.com/openai/tunnel-client/testsupport/mocktunnelservice"
)

func TestPCFileCallThroughOfficialTunnel(t *testing.T) {
	const tid = "tunnel_pcaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const key = "test-tunnel-key"
	dir := t.TempDir()
	root := filepath.Join(dir, "root")
	state := filepath.Join(dir, "state")
	os.Mkdir(root, 0700)
	os.Mkdir(state, 0700)
	os.WriteFile(filepath.Join(root, "hello.txt"), []byte("hello through tunnel"), 0644)
	engine := &sandbox.Engine{MaxSeconds: 10}
	ws, e := workspace.New(root, state, engine)
	if e != nil {
		t.Fatal(e)
	}
	defer ws.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	jm := jobs.New(ctx, ws, engine, 10)
	defer jm.Close()
	server := tools.New(ws, jm)
	command := mocktunnelservice.CommandResponse{Command: mocktunnelservice.NewCommand("pc-file-call", json.RawMessage(`{"jsonrpc":"2.0","id":"pc-file","method":"tools/call","params":{"name":"pc_read_file","arguments":{"directory":".","branch":"","path":"hello.txt"}}}`), nil), ExpectedResponses: []mocktunnelservice.ExpectedResponse{{RequestID: "pc-file-call", Assert: func(tb testing.TB, r mocktunnelservice.ReceivedResponse) {
		var p struct {
			Result struct {
				IsError           bool `json:"isError"`
				StructuredContent struct {
					Text string `json:"text"`
				} `json:"structuredContent"`
			} `json:"result"`
		}
		if e := json.Unmarshal(r.JSONResponse, &p); e != nil || p.Result.IsError || p.Result.StructuredContent.Text != "hello through tunnel" {
			tb.Errorf("tunnel file response failed: %s (%v)", r.JSONResponse, e)
		}
	}}}}
	approval := mocktunnelservice.CommandResponse{Command: mocktunnelservice.NewCommand("pc-approval-call", json.RawMessage(`{"jsonrpc":"2.0","id":"pc-approval","method":"tools/call","params":{"name":"pc_request_run","arguments":{"directory":".","branch":"","args":["echo","smoke"],"purpose":"smoke","seconds":1}}}`), nil), ExpectedResponses: []mocktunnelservice.ExpectedResponse{{RequestID: "pc-approval-call", Assert: func(tb testing.TB, r mocktunnelservice.ReceivedResponse) {
		var p struct {
			Result struct {
				Meta       map[string]any `json:"_meta"`
				Structured struct {
					Status string `json:"status"`
				} `json:"structuredContent"`
			} `json:"result"`
		}
		if e := json.Unmarshal(r.JSONResponse, &p); e != nil || p.Result.Structured.Status != "awaiting_approval" {
			tb.Error("approval result not forwarded", e)
		}
		if token, ok := p.Result.Meta["approval_nonce"].(string); !ok || token == "" {
			tb.Error("private approval nonce lost in tunnel transport")
		}
	}}}}
	cp := mocktunnelservice.NewMockTunnelService(mocktunnelservice.WithAPIKey(key), mocktunnelservice.WithTunnelID(tid), mocktunnelservice.WithInitializationPhaseCommandsWithoutSessionHeaders(), mocktunnelservice.WithCommandResponses(command, approval))
	cp.Start(t)
	st, ct := mcp.NewInMemoryTransports()
	go server.Run(ctx, st)
	client, e := tunnelclient.New(tunnelclient.Config{TunnelID: tid, APIKey: key, ControlPlaneBaseURL: cp.BaseURL().String(), PollTimeout: 10 * time.Millisecond, LogWriter: io.Discard}, ct)
	if e != nil {
		t.Fatal(e)
	}
	if e = client.Start(ctx); e != nil {
		t.Fatal(e)
	}
	defer func() {
		c, stop := context.WithTimeout(context.Background(), 3*time.Second)
		defer stop()
		if e := client.Stop(c); e != nil {
			t.Error(e)
		}
	}()
	c, stop := context.WithTimeout(ctx, 5*time.Second)
	defer stop()
	if e = client.WaitUntilReady(c); e != nil {
		t.Fatal(e)
	}
	if e = cp.WaitUntilIdle(c); e != nil {
		t.Fatal(e)
	}
	found := false
	for _, r := range cp.ReceivedResponses(mocktunnelservice.ResponseMatchMatched) {
		if r.RequestID == "pc-file-call" {
			found = true
		}
	}
	if !found {
		t.Fatal("PC tool was not forwarded through the tunnel")
	}
}
