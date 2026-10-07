package jobs

import (
	"strings"
	"sync"
	"unicode/utf8"
)

const HeadLines = 100
const TailLines = 10
const LineBytes = 4096
const HeadBytes = 32 * 1024
const TailBytes = 8 * 1024

// Each stream is segmented at 4 KiB even without a newline. Memory stays bounded
// for megabytes of one-line output, and both streams drain continuously.
type Line struct {
	Sequence  int    `json:"sequence"`
	Stream    string `json:"stream"`
	Text      string `json:"text"`
	Truncated bool   `json:"truncated,omitempty"`
}
type Output struct {
	mu                   sync.Mutex
	head, tail           []Line
	ring                 []Line
	ringBytes            int
	total                int
	headBytes, tailBytes int
	headClosed           bool
	bytes                int64
}
type streamWriter struct {
	o        *Output
	name     string
	partial  []byte
	fragment bool
}

func (o *Output) Writer(name string) *streamWriter { return &streamWriter{o: o, name: name} }
func (w *streamWriter) Write(b []byte) (int, error) {
	n := len(b)
	w.o.mu.Lock()
	w.o.bytes += int64(n)
	w.o.mu.Unlock()
	for _, v := range b {
		if v == '\n' {
			if len(w.partial) > 0 || !w.fragment {
				w.flush(false)
			}
			w.fragment = false
			continue
		}
		w.partial = append(w.partial, v)
		if len(w.partial) >= LineBytes {
			w.flush(true)
			w.fragment = true
		}
	}
	return n, nil
}
func (w *streamWriter) Close() {
	if len(w.partial) > 0 {
		w.flush(false)
	}
}
func (w *streamWriter) flush(truncated bool) {
	text := strings.ToValidUTF8(string(w.partial), "�")
	text = strings.Map(func(r rune) rune {
		if r == '\t' || r >= 32 && r != 127 && utf8.ValidRune(r) {
			return r
		}
		return -1
	}, text)
	w.partial = w.partial[:0]
	o := w.o
	o.mu.Lock()
	defer o.mu.Unlock()
	o.total++
	l := Line{o.total, w.name, text, truncated}
	for len(o.ring) > 0 && (o.ringBytes+len(text) > 1<<20 || len(o.ring) >= 10000) {
		o.ringBytes -= len(o.ring[0].Text)
		o.ring[0] = Line{}
		o.ring = o.ring[1:]
	}
	o.ring = append(o.ring, l)
	o.ringBytes += len(text)
	if !o.headClosed && len(o.head) < HeadLines && o.headBytes+len(text) <= HeadBytes {
		o.head = append(o.head, l)
		o.headBytes += len(text)
	} else {
		o.headClosed = true
		for len(o.tail) > 0 && (len(o.tail) >= TailLines || o.tailBytes+len(text) > TailBytes) {
			o.tailBytes -= len(o.tail[0].Text)
			copy(o.tail, o.tail[1:])
			o.tail = o.tail[:len(o.tail)-1]
		}
		o.tail = append(o.tail, l)
		o.tailBytes += len(text)
	}
}

type OutputView struct {
	Head         []Line `json:"head"`
	Tail         []Line `json:"tail"`
	TotalRecords int    `json:"total_records"`
	Bytes        int64  `json:"bytes"`
	Omitted      int    `json:"omitted"`
	Cursor       int    `json:"cursor"`
	Message      string `json:"message,omitempty"`
}

func (o *Output) View(after int, done bool) OutputView {
	o.mu.Lock()
	defer o.mu.Unlock()
	v := OutputView{Head: []Line{}, Tail: []Line{}, TotalRecords: o.total, Bytes: o.bytes, Cursor: after}
	for _, l := range o.head {
		if l.Sequence > after {
			v.Head = append(v.Head, l)
			v.Cursor = l.Sequence
		}
	}
	if done {
		for _, l := range o.tail {
			if l.Sequence > after {
				v.Tail = append(v.Tail, l)
			}
		}
		v.Cursor = max(v.Cursor, o.total)
	}
	v.Omitted = max(0, o.total-len(o.head)-len(o.tail))
	if !done && o.headClosed {
		v.Message = "Output exceeded 100 records or 32 KiB. Both streams are still drained; poll after completion for up to the last 10 records (8 KiB)."
	}
	return v
}

type OutputPage struct {
	Records      []Line `json:"records"`
	Cursor       int    `json:"cursor"`
	Evicted      int    `json:"evicted"`
	TotalRecords int    `json:"total_records"`
}

func (o *Output) Page(after, limit int) OutputPage {
	o.mu.Lock()
	defer o.mu.Unlock()
	if limit < 1 || limit > 200 {
		limit = 100
	}
	v := OutputPage{Records: []Line{}, Cursor: after, TotalRecords: o.total}
	if len(o.ring) > 0 {
		v.Evicted = max(0, o.ring[0].Sequence-1-after)
	}
	bytes := 0
	for _, l := range o.ring {
		if l.Sequence > after {
			if len(v.Records) >= limit || bytes+len(l.Text) > 128<<10 {
				break
			}
			v.Records = append(v.Records, l)
			v.Cursor = l.Sequence
			bytes += len(l.Text)
		}
	}
	return v
}
