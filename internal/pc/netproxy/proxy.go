// Package netproxy configures outbound HTTP before the embedded tunnel starts.
package netproxy

import (
	"errors"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// Parse accepts an IP:port or SOCKS5 URL. Requiring the proxy's IP also avoids
// a local DNS lookup for the proxy itself. Target names are sent over SOCKS5.
func Parse(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	if !strings.Contains(raw, "://") {
		raw = "socks5h://" + raw
	}
	u, err := url.Parse(raw)
	// Do not include raw input/errors: a URL may contain proxy credentials.
	if err != nil || (u.Scheme != "socks5" && u.Scheme != "socks5h") || u.Opaque != "" || u.Path != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return nil, errors.New("PC_MCP_SOCKS5_PROXY must be IP:port or socks5h://[user:password@]IP:port")
	}
	host, port, err := net.SplitHostPort(u.Host)
	n, portErr := strconv.Atoi(port)
	if err != nil || net.ParseIP(host) == nil || portErr != nil || n < 1 || n > 65535 {
		return nil, errors.New("PC_MCP_SOCKS5_PROXY requires a numeric IP address and port 1..65535 (IPv6 in brackets)")
	}
	if u.User != nil {
		password, _ := u.User.Password()
		if len(u.User.Username()) < 1 || len(u.User.Username()) > 255 || len(password) > 255 {
			return nil, errors.New("PC_MCP_SOCKS5_PROXY credentials exceed SOCKS5 limits")
		}
	}
	u.Scheme = "socks5h"
	return u, nil
}

// Install changes the process default transport for the lifetime of one server.
// Call only at startup, before HTTP users/goroutines start; restore after they
// stop. tunnel-client v0.0.15 clones http.DefaultTransport and has no public
// transport injection option. Never mutate the existing transport in place.
func Install(proxy *url.URL) (restore func(), err error) {
	if proxy == nil {
		return func() {}, nil // Preserve system/environment behavior exactly.
	}
	if os.Getenv("TUNNEL_INTEGRATION_TUNNEL_SERVICE_SOCKET_PATH") != "" {
		return nil, errors.New("SOCKS5 proxy cannot be combined with TUNNEL_INTEGRATION_TUNNEL_SERVICE_SOCKET_PATH")
	}
	previous := http.DefaultTransport
	base, ok := previous.(*http.Transport)
	if !ok {
		return nil, errors.New("SOCKS5 proxy requires the standard Go HTTP transport")
	}
	transport := base.Clone()
	transport.Proxy = http.ProxyURL(proxy) // Fixed for all hosts; ignores NO_PROXY.
	transport.DialContext = (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	transport.Dial = nil
	transport.DialTLS = nil
	transport.DialTLSContext = nil
	http.DefaultTransport = transport
	return func() {
		http.DefaultTransport = previous
		transport.CloseIdleConnections()
	}, nil
}
