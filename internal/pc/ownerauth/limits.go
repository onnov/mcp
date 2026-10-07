package ownerauth

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"
)

// Limiter bounds memory as well as attempts. Exhaustion only rejects new peers,
// preserving existing buckets. A reverse proxy must also limit public traffic.
type bucket struct {
	count int
	until time.Time
}
type Limiter struct {
	mu     sync.Mutex
	peers  map[string]bucket
	Max    int
	Period time.Duration
}

func (l *Limiter) Allow(peer string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	if l.peers == nil {
		l.peers = map[string]bucket{}
	}
	b, exists := l.peers[peer]
	if !exists || !now.Before(b.until) {
		for k, v := range l.peers {
			if !now.Before(v.until) {
				delete(l.peers, k)
			}
		}
		if len(l.peers) >= 4096 {
			return false
		}
		b = bucket{until: now.Add(l.Period)}
	}
	if b.count >= l.Max {
		return false
	}
	b.count++
	l.peers[peer] = b
	return true
}
func ClientIP(r *http.Request, trusted []netip.Prefix) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return "unknown"
	}
	ip = ip.Unmap()
	for _, prefix := range trusted {
		if prefix.Contains(ip) {
			// Accept one address, never a client-controlled chain. The trusted proxy
			// must overwrite X-Forwarded-For with its actual remote peer.
			forwarded, err := netip.ParseAddr(strings.TrimSpace(r.Header.Get("X-Forwarded-For")))
			if err == nil {
				return forwarded.Unmap().String()
			}
			break
		}
	}
	return ip.String()
}

func (s *Server) seal(p pending) (string, error) {
	b, err := json.Marshal(p)
	if err != nil {
		return "", err
	}
	nonce := randomBytes()[:s.envelope.NonceSize()]
	return base64.RawURLEncoding.EncodeToString(s.envelope.Seal(nonce, nonce, b, []byte(s.resource()))), nil
}
func (s *Server) openEnvelope(raw string) (pending, error) {
	var p pending
	if len(raw) > 3000 {
		return p, errors.New("oversize authorization request")
	}
	b, err := base64.RawURLEncoding.Strict().DecodeString(raw)
	if err != nil || base64.RawURLEncoding.EncodeToString(b) != raw || len(b) < s.envelope.NonceSize() {
		return p, errors.New("invalid authorization request")
	}
	nonce := b[:s.envelope.NonceSize()]
	b, err = s.envelope.Open(nil, nonce, b[s.envelope.NonceSize():], []byte(s.resource()))
	if err != nil {
		return p, err
	}
	if json.Unmarshal(b, &p) != nil || !time.Now().Before(p.Expires) {
		return p, errors.New("expired authorization request")
	}
	return p, nil
}
func newEnvelope() (cipher.AEAD, error) {
	block, err := aes.NewCipher(randomBytes())
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
