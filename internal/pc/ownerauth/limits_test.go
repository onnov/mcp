package ownerauth

import (
	"net/http/httptest"
	"net/netip"
	"testing"
	"time"
)

func TestIndependentPeerLimitsAndUntrustedHeaders(t *testing.T) {
	l := &Limiter{Max: 2, Period: time.Minute}
	if !l.Allow("attacker") || !l.Allow("attacker") || l.Allow("attacker") || !l.Allow("owner") {
		t.Fatal("peer limit becomes global lockout")
	}
	r := httptest.NewRequest("GET", "https://pc.example/mcp", nil)
	r.RemoteAddr = "192.0.2.20:1234"
	r.Header.Set("X-Forwarded-For", "203.0.113.9")
	if ClientIP(r, nil) != "192.0.2.20" {
		t.Fatal("untrusted forwarding header accepted")
	}
	trusted := []netip.Prefix{netip.MustParsePrefix("192.0.2.20/32")}
	if ClientIP(r, trusted) != "203.0.113.9" {
		t.Fatal("trusted proxy lost peer")
	}
	r.Header.Set("X-Forwarded-For", "203.0.113.9, 127.0.0.1")
	if ClientIP(r, trusted) != "192.0.2.20" {
		t.Fatal("ambiguous header chain accepted")
	}
}

func TestUnauthenticatedFlowsAllocateNoPendingState(t *testing.T) {
	s, h := testServer(t)
	for i := 0; i < 30; i++ {
		begin(t, s, h)
	}
	if len(s.consumed) != 0 || len(s.codes) != 0 || len(s.access) != 0 || len(s.refresh) != 0 {
		t.Fatal("anonymous flow allocated OAuth grant state")
	}
}

func TestAuthorizationEnvelopeTampering(t *testing.T) {
	s, h := testServer(t)
	nonce, _ := begin(t, s, h)
	b := []byte(nonce)
	b[len(b)/2] ^= 1
	if _, err := s.openEnvelope(string(b)); err == nil {
		t.Fatal("tampered authorization state accepted")
	}
	if _, err := s.openEnvelope(nonce + "\n"); err == nil {
		t.Fatal("alternate encoding could bypass consumed-flow key")
	}
}
