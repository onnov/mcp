package egress

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPublicIPPolicy(t *testing.T) {
	for _, raw := range []string{"127.0.0.1", "::1", "10.0.0.1", "172.16.3.4", "192.168.1.1", "169.254.169.254", "100.64.0.1", "198.18.0.1", "192.0.2.1", "224.0.0.1", "::ffff:127.0.0.1", "2001:db8::1", "64:ff9b::7f00:1", "2002:7f00:1::"} {
		if PublicIP(netip.MustParseAddr(raw)) {
			t.Fatal("nonpublic address allowed", raw)
		}
	}
	for _, raw := range []string{"1.1.1.1", "8.8.8.8", "2606:4700:4700::1111"} {
		if !PublicIP(netip.MustParseAddr(raw)) {
			t.Fatal("public address blocked", raw)
		}
	}
}

func TestLocalEgressRequiresOperatorOptIn(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "development-service") }))
	defer upstream.Close()
	for _, allow := range []bool{false, true} {
		p, err := New(context.Background(), t.TempDir(), allow, "")
		if err != nil {
			t.Fatal(err)
		}
		transport := &http.Transport{Proxy: func(*http.Request) (*url.URL, error) { return url.Parse("http://proxy") }, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", filepath.Join(p.Directory, "net.sock"))
		}}
		client := &http.Client{Transport: transport, Timeout: time.Second}
		resp, err := client.Get(upstream.URL)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		transport.CloseIdleConnections()
		p.Close()
		if allow && (resp.StatusCode != 200 || string(body) != "development-service") {
			t.Fatal(resp.StatusCode, string(body))
		}
		if !allow && resp.StatusCode < 400 {
			t.Fatal("localhost escape accepted")
		}
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestGitHubCredentialInjectionNeverReachesJob(t *testing.T) {
	const token = "actual-host-only-github-token"
	p, err := New(context.Background(), t.TempDir(), false, token)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	ca, err := os.ReadFile(filepath.Join(p.Directory, "ca.pem"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(ca), token) {
		t.Fatal("token in job-readable CA bundle")
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(ca) {
		t.Fatal("CA unavailable")
	}
	// A fake trusted upstream inspects requests without making network calls.
	// The real transport is separately tested for private-address refusal.
	observed := make(chan *http.Request, 1)
	p.roundTrip = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		observed <- r.Clone(context.Background())
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("ok")), ContentLength: 2, Request: r}, nil
	})
	tr := &http.Transport{Proxy: func(*http.Request) (*url.URL, error) { return url.Parse("http://proxy") }, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", filepath.Join(p.Directory, "net.sock"))
	}, TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}}
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: tr, Timeout: 2 * time.Second}
	req, _ := http.NewRequest("GET", "https://api.github.com/user", nil)
	req.Header.Set("Authorization", "Bearer repository-controlled-value")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if strings.Contains(string(body), token) {
		t.Fatal("token exposed to job")
	}
	r := <-observed
	if r.Header.Get("Authorization") != "Bearer "+token || r.URL.Host != "api.github.com" || r.URL.Scheme != "https" {
		t.Fatal("credential was not bound to GitHub")
	}
}

func TestMalformedDestinations(t *testing.T) {
	for _, raw := range []string{"https://user:password@example.com/", "file:///etc/passwd", "http://example.com/#fragment", "http://"} {
		if _, err := target(raw, false); err == nil {
			t.Fatal("invalid target accepted", raw)
		}
	}
}
