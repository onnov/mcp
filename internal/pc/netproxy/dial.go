package netproxy

import (
	"context"
	"net"
	"net/url"
	"time"

	"golang.org/x/net/proxy"
)

// DialContext is also used for outbound SSH. SOCKS5 receives the original
// destination hostname; the local resolver is never called for that target.
func DialContext(u *url.URL) (func(context.Context, string, string) (net.Conn, error), error) {
	base := &net.Dialer{Timeout: 20 * time.Second, KeepAlive: 30 * time.Second}
	if u == nil {
		return base.DialContext, nil
	}
	var auth *proxy.Auth
	if u.User != nil {
		password, _ := u.User.Password()
		auth = &proxy.Auth{User: u.User.Username(), Password: password}
	}
	d, err := proxy.SOCKS5("tcp", u.Host, auth, base)
	if err != nil {
		return nil, err
	}
	return d.(proxy.ContextDialer).DialContext, nil
}
