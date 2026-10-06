package sandbox

import (
	"bytes"
	"io"
)

// redactWriter handles tokens split across arbitrary stdout/stderr writes.
// It limits accidental plaintext credential logging; it is not a DLP boundary.
type redactWriter struct {
	dst     io.Writer
	secret  []byte
	pending []byte
}

func newRedactor(dst io.Writer, secret string) *redactWriter {
	return &redactWriter{dst: dst, secret: []byte(secret)}
}
func (w *redactWriter) Write(p []byte) (int, error) {
	if len(w.secret) == 0 {
		return w.dst.Write(p)
	}
	n := len(p)
	w.pending = append(w.pending, p...)
	for {
		index := bytes.Index(w.pending, w.secret)
		if index >= 0 {
			if _, e := w.dst.Write(w.pending[:index]); e != nil {
				return 0, e
			}
			if _, e := w.dst.Write([]byte("[REDACTED]")); e != nil {
				return 0, e
			}
			w.pending = w.pending[index+len(w.secret):]
			continue
		}
		safe := len(w.pending) - len(w.secret) + 1
		if safe > 0 {
			if _, e := w.dst.Write(w.pending[:safe]); e != nil {
				return 0, e
			}
			w.pending = w.pending[safe:]
		}
		break
	}
	return n, nil
}
func (w *redactWriter) Close() { _, _ = w.dst.Write(w.pending); w.pending = nil }
