package sandbox

import (
	"bytes"
	"strings"
	"testing"
)

func TestCredentialRedactionAcrossChunks(t *testing.T) {
	var dst bytes.Buffer
	w := newRedactor(&dst, "secret-token")
	for _, p := range []string{"before se", "cret-", "token after secret-token!"} {
		w.Write([]byte(p))
	}
	w.Close()
	if dst.String() != "before [REDACTED] after [REDACTED]!" || strings.Contains(dst.String(), "secret-token") {
		t.Fatal(dst.String())
	}
}
