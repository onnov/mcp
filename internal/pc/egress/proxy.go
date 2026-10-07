// Package egress exposes a per-job Unix socket. Jobs never share the host
// network namespace or receive the real GitHub token.
package egress

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type Proxy struct {
	Directory    string
	AllowPrivate bool
	AllowGitSSH  bool
	Token        string
	server       *http.Server
	listener     net.Listener
	transport    *http.Transport
	roundTrip    http.RoundTripper
	slots        chan struct{}
	mu           sync.Mutex
	connections  map[net.Conn]bool
	certificates map[string]tls.Certificate
	cancel       context.CancelFunc
	ctx          context.Context
}

func New(ctx context.Context, state string, allowPrivate bool, token string, gitSSH ...bool) (*Proxy, error) {
	allowGitSSH := len(gitSSH) > 0 && gitSSH[0]
	dir, err := os.MkdirTemp(state, "egress-")
	if err != nil {
		return nil, err
	}
	p := &Proxy{Directory: dir, AllowPrivate: allowPrivate, AllowGitSSH: allowGitSSH, Token: token, slots: make(chan struct{}, 32), connections: map[net.Conn]bool{}, certificates: map[string]tls.Certificate{}}
	p.ctx, p.cancel = context.WithCancel(ctx)
	fail := func(err error) (*Proxy, error) { p.Close(); return nil, err }
	if token != "" {
		if err = p.makeCertificates(); err != nil {
			return fail(err)
		}
	}
	p.transport = &http.Transport{Proxy: nil, DialContext: p.dial, TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 30 * time.Second, MaxResponseHeaderBytes: 64 << 10, MaxIdleConns: 32, MaxIdleConnsPerHost: 8, IdleConnTimeout: 30 * time.Second}
	p.roundTrip = p.transport
	p.listener, err = net.Listen("unix", filepath.Join(dir, "net.sock"))
	if err != nil {
		return fail(err)
	}
	if err = os.Chmod(filepath.Join(dir, "net.sock"), 0600); err != nil {
		return fail(err)
	}
	p.listener = &boundedListener{Listener: p.listener, slots: make(chan struct{}, 64)}
	p.server = &http.Server{Handler: p, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10, BaseContext: func(net.Listener) context.Context { return p.ctx }}
	go p.server.Serve(p.listener)
	return p, nil
}

func (p *Proxy) Close() {
	if p.cancel != nil {
		p.cancel()
	}
	if p.server != nil {
		p.server.Close()
	}
	if p.listener != nil {
		p.listener.Close()
	}
	if p.transport != nil {
		p.transport.CloseIdleConnections()
	}
	p.mu.Lock()
	for c := range p.connections {
		c.Close()
	}
	p.mu.Unlock()
	if p.Directory != "" {
		os.RemoveAll(p.Directory)
	}
}

// PublicIP rejects local, private, multicast, documentation and special-use
// ranges. The resolved address is dialled directly: no second DNS lookup.
func PublicIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	if ip.Is6() && !netip.MustParsePrefix("2000::/3").Contains(ip) {
		return false
	}
	if !ip.IsValid() || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return false
	}
	for _, raw := range []string{"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "192.88.99.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4", "2001:db8::/32", "2001:2::/48", "2001:10::/28", "2001:20::/28", "2001::/32", "2002::/16", "64:ff9b::/96", "64:ff9b:1::/48"} {
		if netip.MustParsePrefix(raw).Contains(ip) {
			return false
		}
	}
	return true
}

func (p *Proxy) dial(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	github := strings.EqualFold(host, "github.com")
	private := p.AllowPrivate && !(p.Token != "" && (github || strings.EqualFold(host, "api.github.com") || strings.EqualFold(host, "uploads.github.com")))
	gitSSH := p.AllowGitSSH && github && port == "22"
	if !private && !gitSSH && port != "80" && port != "443" {
		return nil, errors.New("public egress destination or port is not permitted")
	}
	resolveCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	ips, err := net.DefaultResolver.LookupNetIP(resolveCtx, "ip", host)
	if err != nil {
		return nil, err
	}
	// Reject mixed public/private answers as well as wholly private names.
	for _, ip := range ips {
		if !private && !PublicIP(ip) {
			return nil, errors.New("local/private/special-use destination blocked")
		}
	}
	d := net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	for _, ip := range ips {
		c, e := d.DialContext(resolveCtx, "tcp", net.JoinHostPort(ip.String(), port))
		if e == nil {
			return c, nil
		}
		err = e
	}
	if err == nil {
		err = errors.New("destination has no addresses")
	}
	return nil, err
}

func target(raw string, connect bool) (*url.URL, error) {
	if connect {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Hostname() == "" || u.Fragment != "" || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, errors.New("invalid proxy destination")
	}
	if connect && (u.Path != "" || u.RawQuery != "") {
		return nil, errors.New("invalid CONNECT target")
	}
	return u, nil
}

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	select {
	case p.slots <- struct{}{}:
		defer func() { <-p.slots }()
	default:
		http.Error(w, "egress busy", 429)
		return
	}
	if r.Method == http.MethodConnect {
		p.connect(w, r)
		return
	}
	u, err := target(r.URL.String(), false)
	if err != nil || u.Scheme != "http" {
		http.Error(w, "absolute HTTP URL required", 400)
		return
	}
	// Credentials may only go over verified HTTPS, never a plaintext proxy URL.
	p.forward(w, r, u, false)
}

func strip(h http.Header) {
	for _, v := range h.Values("Connection") {
		for _, key := range strings.Split(v, ",") {
			h.Del(strings.TrimSpace(key))
		}
	}
	for _, key := range []string{"Connection", "Proxy-Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization", "TE", "Trailer", "Transfer-Encoding", "Upgrade"} {
		h.Del(key)
	}
}

func (p *Proxy) upstream(r *http.Request, u *url.URL, credential bool) (*http.Response, error) {
	req := r.Clone(r.Context())
	req.URL = u
	req.Host = u.Host
	req.RequestURI = ""
	req.Header = r.Header.Clone()
	strip(req.Header)
	if credential {
		if u.Hostname() == "api.github.com" || u.Hostname() == "uploads.github.com" {
			req.Header.Set("Authorization", "Bearer "+p.Token)
		} else {
			req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("x-access-token:"+p.Token)))
		}
	}
	return p.roundTrip.RoundTrip(req) // RoundTrip never follows redirects with credentials.
}
func (p *Proxy) forward(w http.ResponseWriter, r *http.Request, u *url.URL, credential bool) {
	resp, err := p.upstream(r, u, credential)
	if err != nil {
		http.Error(w, "egress request failed", 502)
		return
	}
	defer resp.Body.Close()
	strip(resp.Header)
	for key, values := range resp.Header {
		for _, v := range values {
			w.Header().Add(key, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

func (p *Proxy) track(c net.Conn) func() {
	p.mu.Lock()
	if p.ctx.Err() != nil {
		c.Close()
	} else {
		p.connections[c] = true
	}
	p.mu.Unlock()
	return func() { c.Close(); p.mu.Lock(); delete(p.connections, c); p.mu.Unlock() }
}

type bufferedConn struct {
	net.Conn
	reader *bufio.Reader
}

func (c bufferedConn) Read(b []byte) (int, error) { return c.reader.Read(b) }

func (p *Proxy) connect(w http.ResponseWriter, r *http.Request) {
	u, err := target(r.Host, true)
	if err != nil {
		http.Error(w, "bad CONNECT target", 400)
		return
	}
	host := strings.ToLower(u.Hostname())
	port := u.Port()
	if port == "" {
		port = "443"
	}
	gitSSH := p.AllowGitSSH && host == "github.com" && port == "22"
	credential := p.Token != "" && !gitSSH && port == "443" && (host == "github.com" || host == "api.github.com" || host == "uploads.github.com")
	// Authenticate only canonical GitHub HTTPS, with public addresses even when
	// the operator has opted into private development services.
	if p.Token != "" && !gitSSH && (host == "github.com" || host == "api.github.com" || host == "uploads.github.com") && port != "443" {
		http.Error(w, "GitHub HTTPS required", 403)
		return
	}
	addr := u.Host
	if u.Port() == "" {
		addr = net.JoinHostPort(u.Hostname(), "443")
	}
	var remote net.Conn
	if !credential {
		remote, err = p.dial(p.ctx, "tcp", addr)
		if err != nil {
			http.Error(w, "destination blocked or unavailable", 403)
			return
		}
	}
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		if remote != nil {
			remote.Close()
		}
		http.Error(w, "CONNECT unavailable", 500)
		return
	}
	client, rw, err := hijacker.Hijack()
	if err != nil {
		if remote != nil {
			remote.Close()
		}
		return
	}
	defer p.track(client)()
	client.SetDeadline(time.Now().Add(30 * time.Minute))
	if _, err = rw.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		return
	}
	if rw.Flush() != nil {
		return
	}
	buffered := bufferedConn{client, rw.Reader}
	if credential {
		p.github(buffered, host)
		return
	}
	defer p.track(remote)()
	remote.SetDeadline(time.Now().Add(30 * time.Minute))
	done := make(chan struct{}, 1)
	go func() { io.Copy(remote, buffered); done <- struct{}{} }()
	io.Copy(client, remote)
	remote.Close()
	client.Close()
	<-done
}

type singleListener struct {
	conn      net.Conn
	delivered bool
	done      chan struct{}
}

func (l *singleListener) Accept() (net.Conn, error) {
	if !l.delivered {
		l.delivered = true
		return l.conn, nil
	}
	<-l.done
	return nil, net.ErrClosed
}
func (l *singleListener) Close() error   { return l.conn.Close() }
func (l *singleListener) Addr() net.Addr { return l.conn.LocalAddr() }

type closingConn struct {
	net.Conn
	done chan struct{}
	once sync.Once
}

func (c *closingConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(func() { close(c.done) })
	return err
}
func (p *Proxy) github(client net.Conn, host string) {
	c := tls.Server(client, &tls.Config{Certificates: []tls.Certificate{p.certificates[host]}, MinVersion: tls.VersionTLS12, NextProtos: []string{"http/1.1"}})
	client.SetDeadline(time.Now().Add(10 * time.Second))
	if c.HandshakeContext(p.ctx) != nil {
		return
	}
	client.SetDeadline(time.Time{})
	done := make(chan struct{})
	conn := &closingConn{Conn: c, done: done}
	listener := &singleListener{conn: conn, done: done}
	defer listener.Close()
	server := &http.Server{ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 5 * time.Minute, WriteTimeout: 5 * time.Minute, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10, BaseContext: func(net.Listener) context.Context { return p.ctx }, Handler: http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method == http.MethodConnect || req.URL.IsAbs() || req.Host != host && req.Host != host+":443" {
			http.Error(w, "GitHub origin required", 403)
			return
		}
		u := *req.URL
		u.Scheme = "https"
		u.Host = host
		p.forward(w, req, &u, true)
	})}
	server.Serve(listener)
	server.Close()
}

func (p *Proxy) makeCertificates() error {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return err
	}
	ca := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "PC MCP per-job GitHub proxy"}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(25 * time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, ca, ca, &key.PublicKey, key)
	if err != nil {
		return err
	}
	bundle, _ := os.ReadFile("/etc/ssl/certs/ca-certificates.crt")
	bundle = append(bundle, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})...)
	if err = os.WriteFile(filepath.Join(p.Directory, "ca.pem"), bundle, 0600); err != nil {
		return err
	}
	for _, host := range []string{"github.com", "api.github.com", "uploads.github.com"} {
		serial, err = rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
		if err != nil {
			return err
		}
		leaf := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: host}, DNSNames: []string{host}, NotBefore: ca.NotBefore, NotAfter: ca.NotAfter, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
		cert, err := x509.CreateCertificate(rand.Reader, leaf, ca, &key.PublicKey, key)
		if err != nil {
			return err
		}
		p.certificates[host] = tls.Certificate{Certificate: [][]byte{cert, der}, PrivateKey: key}
	}
	return nil
}

// Bridge runs inside the job's private network namespace. Only this fixed
// loopback listener is reachable; traffic is forwarded over the mounted socket.
func Bridge(ctx context.Context, socket string) error {
	listener, err := net.Listen("tcp", "127.0.0.1:3128")
	if err != nil {
		return err
	}
	defer listener.Close()
	if err = os.WriteFile("/tmp/pc-mcp-proxy-ready", []byte("ready"), 0600); err != nil {
		return err
	}
	go func() { <-ctx.Done(); listener.Close() }()
	slots := make(chan struct{}, 32)
	for {
		c, err := listener.Accept()
		if err != nil {
			return err
		}
		select {
		case slots <- struct{}{}:
		default:
			c.Close()
			continue
		}
		go func(c net.Conn) {
			defer func() { <-slots }()
			defer c.Close()
			c.SetDeadline(time.Now().Add(30 * time.Minute))
			d := net.Dialer{Timeout: 5 * time.Second}
			remote, err := d.DialContext(ctx, "unix", socket)
			if err != nil {
				return
			}
			defer remote.Close()
			done := make(chan struct{}, 1)
			go func() { io.Copy(remote, c); done <- struct{}{} }()
			io.Copy(c, remote)
			c.Close()
			remote.Close()
			<-done
		}(c)
	}
}
