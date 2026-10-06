package netproxy

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	tunnelclient "github.com/openai/tunnel-client"
	"github.com/openai/tunnel-client/testsupport/mocktunnelservice"
)

func TestParse(t *testing.T) {
	for _, raw := range []string{"", " ", "127.0.0.1:1084", "socks5://127.0.0.1:1084", "socks5h://user:password@127.0.0.1:1084", "socks5h://[::1]:1084"} {
		if _, err := Parse(raw); err != nil {
			t.Errorf("valid proxy refused: %v", err)
		}
	}
	for _, raw := range []string{"http://127.0.0.1:1084", "proxy.example:1084", "127.0.0.1", "127.0.0.1:0", "127.0.0.1:65536", "socks5h://127.0.0.1:1084/path", "socks5h://127.0.0.1:1084?", "socks5h://127.0.0.1:1084#fragment", "socks5h://user:secret@bad:1084", "socks5h://user:%zz@127.0.0.1:1084"} {
		if _, err := Parse(raw); err == nil {
			t.Error("invalid proxy accepted")
		} else if strings.Contains(err.Error(), "secret") {
			t.Error("proxy credentials leaked in error")
		}
	}
}

func TestUnsetPreservesDefault(t *testing.T) {
	before := http.DefaultTransport
	restore, err := Install(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer restore()
	if http.DefaultTransport != before {
		t.Fatal("unset proxy changed system transport")
	}
}

func TestSOCKSRemoteDNSAndTLS(t *testing.T) {
	// .invalid cannot resolve locally. A poisoned system proxy and NO_PROXY
	// must not override the explicit SOCKS setting, even for loopback targets.
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")
	t.Setenv("NO_PROXY", "*")
	backend := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != "destination.invalid" {
			t.Errorf("unexpected HTTP host: %s", r.Host)
		}
		io.WriteString(w, "through SOCKS with TLS")
	}))
	defer backend.Close()
	backendURL, _ := url.Parse(backend.URL)
	for _, auth := range []bool{false, true} {
		t.Run(map[bool]string{false: "anonymous", true: "authenticated"}[auth], func(t *testing.T) {
			proxy, targets := socksFixture(t, backendURL.Host, auth)
			previous := http.DefaultTransport
			trusted := backend.Client().Transport.(*http.Transport).Clone()
			trusted.TLSClientConfig = trusted.TLSClientConfig.Clone()
			trusted.TLSClientConfig.ServerName = backendURL.Hostname()
			http.DefaultTransport = trusted
			defer func() { http.DefaultTransport = previous }()
			restore, err := Install(proxy)
			if err != nil {
				t.Fatal(err)
			}
			defer restore()
			client := &http.Client{Transport: http.DefaultTransport, Timeout: 2 * time.Second}
			defer client.CloseIdleConnections()
			response, err := client.Get("https://destination.invalid/")
			if err != nil {
				t.Fatal(err)
			}
			data, err := io.ReadAll(response.Body)
			response.Body.Close()
			if err != nil || string(data) != "through SOCKS with TLS" {
				t.Fatal(string(data), err)
			}
			select {
			case target := <-targets:
				if target != "destination.invalid:443" {
					t.Fatal("SOCKS received wrong target", target)
				}
			default:
				t.Fatal("request did not use SOCKS")
			}
		})
	}
}

func TestProxyFailureNeverFallsBack(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("direct fallback occurred") }))
	defer backend.Close()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	proxy, _ := Parse(listener.Addr().String())
	listener.Close()
	restore, err := Install(proxy)
	if err != nil {
		t.Fatal(err)
	}
	defer restore()
	client := &http.Client{Transport: http.DefaultTransport, Timeout: time.Second}
	if response, err := client.Get(backend.URL); err == nil {
		response.Body.Close()
		t.Fatal("unreachable proxy did not fail")
	}
}

func TestProxyRejectsTunnelSocketBypass(t *testing.T) {
	t.Setenv("TUNNEL_INTEGRATION_TUNNEL_SERVICE_SOCKET_PATH", "/tmp/tunnel.sock")
	proxy, _ := Parse("127.0.0.1:1084")
	if restore, err := Install(proxy); err == nil {
		restore()
		t.Fatal("SDK Unix socket bypass accepted")
	}
}

func TestSOCKSDialContextSendsSSHHostname(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, "through proxy") }))
	defer backend.Close()
	u, _ := url.Parse(backend.URL)
	proxy, targets := socksFixture(t, u.Host, false)
	dial, err := DialContext(proxy)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	conn, err := dial(ctx, "tcp", "ssh-server.invalid:22")
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
	select {
	case target := <-targets:
		if target != "ssh-server.invalid:22" {
			t.Fatal("SSH destination was resolved locally", target)
		}
	default:
		t.Fatal("SSH dial bypassed SOCKS5")
	}
}

func TestOfficialTunnelUsesSOCKSForControlPlane(t *testing.T) {
	t.Setenv("NO_PROXY", "*")
	cp := mocktunnelservice.NewMockTunnelService(
		mocktunnelservice.WithAPIKey("test-key"),
		mocktunnelservice.WithTunnelID("tunnel_pcaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"),
		mocktunnelservice.WithInitializationPhaseCommandsWithoutSessionHeaders(),
	)
	cp.Start(t)
	proxy, targets := socksFixture(t, cp.BaseURL().Host, false)
	restore, err := Install(proxy)
	if err != nil {
		t.Fatal(err)
	}
	defer restore()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	st, ct := mcp.NewInMemoryTransports()
	server := mcp.NewServer(&mcp.Implementation{Name: "proxy-test", Version: "1"}, nil)
	done := make(chan error, 1)
	go func() { done <- server.Run(ctx, st) }()
	defer func() { cancel(); <-done }()
	client, err := tunnelclient.New(tunnelclient.Config{
		TunnelID: "tunnel_pcaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", APIKey: "test-key",
		ControlPlaneBaseURL: "http://control-plane.invalid", PollTimeout: 10 * time.Millisecond, LogWriter: io.Discard,
	}, ct)
	if err != nil {
		t.Fatal(err)
	}
	if err = client.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() {
		stopCtx, stop := context.WithTimeout(context.Background(), 3*time.Second)
		defer stop()
		if err := client.Stop(stopCtx); err != nil {
			t.Error(err)
		}
	}()
	if err = client.WaitUntilReady(ctx); err != nil {
		t.Fatal(err)
	}
	if err = cp.WaitUntilIdle(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case target := <-targets:
		if target != "control-plane.invalid:80" {
			t.Fatal("control-plane DNS was not sent to proxy", target)
		}
	default:
		t.Fatal("official SDK bypassed SOCKS")
	}
}

// socksFixture maps every requested destination to a local test backend. It
// records the wire destination before forwarding, without resolving its name.
func socksFixture(t *testing.T, backend string, auth bool) (*url.URL, <-chan string) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	targets := make(chan string, 64)
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer conn.Close()
				stop := context.AfterFunc(ctx, func() { conn.Close() })
				defer stop()
				conn.SetDeadline(time.Now().Add(10 * time.Second))
				header := make([]byte, 2)
				if _, err := io.ReadFull(conn, header); err != nil || header[0] != 5 {
					return
				}
				methods := make([]byte, int(header[1]))
				if _, err := io.ReadFull(conn, methods); err != nil {
					return
				}
				method := byte(0)
				if auth {
					method = 2
				}
				conn.Write([]byte{5, method})
				if auth {
					if _, err := io.ReadFull(conn, header); err != nil || header[0] != 1 {
						return
					}
					user := make([]byte, int(header[1]))
					if _, err := io.ReadFull(conn, user); err != nil {
						return
					}
					length := make([]byte, 1)
					if _, err := io.ReadFull(conn, length); err != nil {
						return
					}
					password := make([]byte, int(length[0]))
					if _, err := io.ReadFull(conn, password); err != nil || string(user) != "user" || string(password) != "password" {
						t.Error("SOCKS authentication did not match")
						return
					}
					conn.Write([]byte{1, 0})
				}
				request := make([]byte, 4)
				if _, err := io.ReadFull(conn, request); err != nil || request[0] != 5 || request[1] != 1 || request[3] != 3 {
					t.Error("expected SOCKS5 CONNECT with unresolved domain name")
					return
				}
				length := make([]byte, 1)
				if _, err := io.ReadFull(conn, length); err != nil {
					return
				}
				name := make([]byte, int(length[0]))
				if _, err := io.ReadFull(conn, name); err != nil {
					return
				}
				port := make([]byte, 2)
				if _, err := io.ReadFull(conn, port); err != nil {
					return
				}
				select {
				case targets <- net.JoinHostPort(string(name), strconv.Itoa(int(binary.BigEndian.Uint16(port)))):
				default:
				}
				upstream, err := (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "tcp", backend)
				if err != nil {
					return
				}
				defer upstream.Close()
				conn.Write([]byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 0})
				copied := make(chan struct{})
				go func() { io.Copy(upstream, conn); upstream.Close(); close(copied) }()
				io.Copy(conn, upstream)
				conn.Close()
				<-copied
			}()
		}
	}()
	t.Cleanup(func() { cancel(); listener.Close(); wg.Wait() })
	u, _ := Parse(listener.Addr().String())
	if auth {
		u.User = url.UserPassword("user", "password")
	}
	return u, targets
}
