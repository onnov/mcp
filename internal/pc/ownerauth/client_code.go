package ownerauth

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"time"
)

// Anonymous authorization requests allocate no server state. Only a successful
// exchange by the configured confidential client consumes bounded replay state.
// Domain separation prevents a password-login envelope from becoming a code.
func (s *Server) sealClientCode(c code) (string, error) {
	b, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	nonce := randomBytes()[:s.envelope.NonceSize()]
	return base64.RawURLEncoding.EncodeToString(s.envelope.Seal(nonce, nonce, b, []byte(s.resource()+"\x00client-code"))), nil
}

func (s *Server) openClientCode(raw string) (code, error) {
	var c code
	if len(raw) > 512 {
		return c, errors.New("oversize authorization code")
	}
	b, err := base64.RawURLEncoding.Strict().DecodeString(raw)
	if err != nil || base64.RawURLEncoding.EncodeToString(b) != raw || len(b) < s.envelope.NonceSize() {
		return c, errors.New("invalid authorization code")
	}
	n := s.envelope.NonceSize()
	b, err = s.envelope.Open(nil, b[:n], b[n:], []byte(s.resource()+"\x00client-code"))
	if err != nil {
		return c, err
	}
	if json.Unmarshal(b, &c) != nil || !validChallenge(c.Challenge) || !time.Now().Before(c.Expires) {
		return c, errors.New("expired or invalid authorization code")
	}
	return c, nil
}
