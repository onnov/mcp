package jobs

import (
	"fmt"
	"strings"
	"sync"
	"testing"
)

func TestOutputHeadTailAndCursor(t *testing.T) {
	o := &Output{}
	w := o.Writer("stdout")
	for i := 1; i <= 150; i++ {
		fmt.Fprintf(w, "line %d\n", i)
	}
	w.Close()
	v := o.View(0, false)
	if len(v.Head) != 100 || len(v.Tail) != 0 || v.Cursor != 100 || v.Message == "" {
		t.Fatalf("running view %+v", v)
	}
	v = o.View(v.Cursor, false)
	if len(v.Head) != 0 {
		t.Fatal("head replayed")
	}
	v = o.View(100, true)
	if len(v.Head) != 0 || len(v.Tail) != 10 || v.Tail[0].Text != "line 141" || v.Tail[9].Text != "line 150" || v.Omitted != 40 || v.Cursor != 150 {
		t.Fatalf("final view %+v", v)
	}
	if v = o.View(v.Cursor, true); len(v.Head)+len(v.Tail) != 0 {
		t.Fatal("final output replayed")
	}
}
func TestNoNewlineBoundAndConcurrentStreams(t *testing.T) {
	o := &Output{}
	var wg sync.WaitGroup
	for _, name := range []string{"stdout", "stderr"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w := o.Writer(name)
			w.Write([]byte(strings.Repeat("x", 2*1024*1024)))
			w.Close()
		}()
	}
	wg.Wait()
	v := o.View(0, true)
	if len(v.Head) != HeadBytes/LineBytes || len(v.Tail) != TailBytes/LineBytes || v.TotalRecords != 1024 || v.Bytes != 4*1024*1024 {
		t.Fatalf("unbounded or missing output: %+v", v)
	}
	for _, l := range append(v.Head, v.Tail...) {
		if len(l.Text) > LineBytes || !l.Truncated {
			t.Fatal("incorrect record bound")
		}
	}
}
func TestChunkBoundaryDoesNotAddEmptyRecord(t *testing.T) {
	o := &Output{}
	w := o.Writer("stdout")
	w.Write([]byte(strings.Repeat("x", LineBytes) + "\nnext\n"))
	w.Close()
	v := o.View(0, true)
	if v.TotalRecords != 2 || len(v.Head) != 2 || v.Head[1].Text != "next" {
		t.Fatalf("synthetic empty record %+v", v)
	}
}

func TestOutputViewRecordsPagination(t *testing.T) {
	o := &Output{}
	w := o.Writer("stdout")
	for i := 1; i <= 450; i++ {
		fmt.Fprintf(w, "line %d\n", i)
	}
	w.Close()

	v1 := o.View(0, true)
	if len(v1.Records) != 200 || v1.RecordsCursor != 200 || !v1.More || v1.Evicted != 0 {
		t.Fatalf("first records page %+v", v1)
	}
	v2 := o.View(v1.RecordsCursor, true)
	if len(v2.Records) != 200 || v2.Records[0].Sequence != 201 || v2.RecordsCursor != 400 || !v2.More {
		t.Fatalf("second records page %+v", v2)
	}
	v3 := o.View(v2.RecordsCursor, true)
	if len(v3.Records) != 50 || v3.Records[0].Sequence != 401 || v3.RecordsCursor != 450 || v3.More {
		t.Fatalf("final records page %+v", v3)
	}
}

func TestOutputViewReportsEviction(t *testing.T) {
	o := &Output{}
	w := o.Writer("stdout")
	for i := 1; i <= 10050; i++ {
		fmt.Fprintf(w, "line %d\n", i)
	}
	w.Close()
	v := o.View(0, true)
	if v.Evicted != 50 || len(v.Records) == 0 || v.Records[0].Sequence != 51 || !v.More {
		t.Fatalf("eviction contract %+v", v)
	}
}
