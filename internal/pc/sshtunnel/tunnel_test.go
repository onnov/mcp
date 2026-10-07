package sshtunnel

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

type sshFixture struct {
	listener net.Listener
	mu       sync.Mutex
	clients  map[*ssh.ServerConn]bool
	wg       sync.WaitGroup
	cfg      Config
}

func newSSHFixture(t *testing.T) *sshFixture {
	t.Helper()
	_, hostKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	host, err := ssh.NewSignerFromKey(hostKey)
	if err != nil {
		t.Fatal(err)
	}
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	clientSigner, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	keyFile := filepath.Join(dir, "id_ed25519")
	hosts := filepath.Join(dir, "known_hosts")
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hosts, []byte(knownhosts.Line([]string{listener.Addr().String()}, host.PublicKey())+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	port, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	remote := port.Addr().String()
	port.Close()
	f := &sshFixture{listener: listener, clients: map[*ssh.ServerConn]bool{}, cfg: Config{Addr: listener.Addr().String(), User: "owner", KeyFile: keyFile, KnownHosts: hosts, RemoteAddr: remote}}
	sc := &ssh.ServerConfig{PublicKeyCallback: func(meta ssh.ConnMetadata, pub ssh.PublicKey) (*ssh.Permissions, error) {
		if meta.User() == "owner" && string(pub.Marshal()) == string(clientSigner.PublicKey().Marshal()) {
			return nil, nil
		}
		return nil, os.ErrPermission
	}}
	sc.AddHostKey(host)
	f.wg.Add(1)
	go func() {
		defer f.wg.Done()
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			f.wg.Add(1)
			go func() { defer f.wg.Done(); f.serve(conn, sc) }()
		}
	}()
	t.Cleanup(func() { listener.Close(); f.drop(); f.wg.Wait() })
	return f
}

func (f *sshFixture) drop() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for c := range f.clients {
		c.Close()
	}
}

func (f *sshFixture) serve(conn net.Conn, cfg *ssh.ServerConfig) {
	defer conn.Close()
	sc, channels, requests, err := ssh.NewServerConn(conn, cfg)
	if err != nil {
		return
	}
	f.mu.Lock()
	f.clients[sc] = true
	f.mu.Unlock()
	defer func() { sc.Close(); f.mu.Lock(); delete(f.clients, sc); f.mu.Unlock() }()
	go func() {
		for c := range channels {
			c.Reject(ssh.UnknownChannelType, "no direct channels")
		}
	}()
	var remote net.Listener
	var workers sync.WaitGroup
	defer func() {
		if remote != nil {
			remote.Close()
		}
		sc.Close()
		workers.Wait()
	}()
	for req := range requests {
		switch req.Type {
		case "tcpip-forward":
			var v struct {
				Addr string
				Port uint32
			}
			if ssh.Unmarshal(req.Payload, &v) != nil || v.Addr != "127.0.0.1" || remote != nil {
				req.Reply(false, nil)
				continue
			}
			remote, err = net.Listen("tcp", net.JoinHostPort(v.Addr, strconv.Itoa(int(v.Port))))
			if err != nil {
				req.Reply(false, nil)
				continue
			}
			req.Reply(true, nil)
			workers.Add(1)
			go func(l net.Listener) {
				defer workers.Done()
				for {
					incoming, err := l.Accept()
					if err != nil {
						return
					}
					workers.Add(1)
					go func() {
						defer workers.Done()
						defer incoming.Close()
						origin := incoming.RemoteAddr().(*net.TCPAddr)
						payload := ssh.Marshal(struct {
							ConnectedAddr string
							ConnectedPort uint32
							OriginAddr    string
							OriginPort    uint32
						}{v.Addr, v.Port, origin.IP.String(), uint32(origin.Port)})
						ch, r, err := sc.OpenChannel("forwarded-tcpip", payload)
						if err != nil {
							return
						}
						defer ch.Close()
						go ssh.DiscardRequests(r)
						done := make(chan struct{})
						go func() { io.Copy(ch, incoming); ch.CloseWrite(); close(done) }()
						io.Copy(incoming, ch)
						incoming.Close()
						ch.Close()
						<-done
					}()
				}
			}(remote)
		case "cancel-tcpip-forward":
			if remote != nil {
				remote.Close()
			}
			req.Reply(true, nil)
		case "keepalive@openssh.com":
			req.Reply(true, nil)
		default:
			req.Reply(false, nil)
		}
	}
}

func waitHTTP(t *testing.T, addr string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client := &http.Client{Timeout: 200 * time.Millisecond}
	defer client.CloseIdleConnections()
	for ctx.Err() == nil {
		resp, err := client.Get("http://" + addr + "/hello")
		if err == nil {
			body, err := io.ReadAll(resp.Body)
			resp.Body.Close()
			if err == nil && string(body) == "from local PC" {
				return
			}
		}
		timer := time.NewTimer(20 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
		case <-timer.C:
		}
	}
	t.Fatal("HTTP did not traverse reverse SSH forward")
}

func TestReverseSSHForwardReconnectAndShutdown(t *testing.T) {
	f := newSSHFixture(t)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, "from local PC") }))
	defer backend.Close()
	tunnel, err := New(f.cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	tunnel.retry = 10 * time.Millisecond
	tunnel.keepInterval = 20 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- tunnel.Run(ctx, strings.TrimPrefix(backend.URL, "http://")) }()
	waitHTTP(t, f.cfg.RemoteAddr)
	f.drop()
	waitHTTP(t, f.cfg.RemoteAddr)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("SSH did not stop with the app")
	}
	conn, err := net.DialTimeout("tcp", f.cfg.RemoteAddr, 100*time.Millisecond)
	if err == nil {
		conn.Close()
		t.Fatal("remote listening port remained open")
	}
}

func TestUnknownHostKeyIsRefused(t *testing.T) {
	f := newSSHFixture(t)
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.cfg.KnownHosts, []byte(knownhosts.Line([]string{f.cfg.Addr}, signer.PublicKey())+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	tunnel, err := New(f.cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := tunnel.session(ctx, "127.0.0.1:8182"); err == nil || !strings.Contains(err.Error(), "knownhosts") {
		t.Fatal("host-key mismatch was not refused", err)
	}
}

func TestUnsafePrivateKeyPermissionsAreRefused(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows private key access is controlled by ACLs, not Unix permission bits")
	}
	f := newSSHFixture(t)
	if err := os.Chmod(f.cfg.KeyFile, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := New(f.cfg, nil); err == nil {
		t.Fatal("world-readable private key accepted")
	}
}
