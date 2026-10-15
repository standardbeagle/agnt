package overlay

import (
	"bytes"
	"sync"
	"testing"
	"time"

	"github.com/standardbeagle/vt10x"
)

// syncTerminal is the host terminal both the child's output and the overlay
// draws reach, in the order their Writes arrive.
type syncTerminal struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *syncTerminal) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

// stallingWriter holds the child's chunk at the point ProtectedWriter hands
// it to the terminal: the filter has already advanced its tracked cursor
// past the chunk, but the terminal has not received it.
type stallingWriter struct {
	out     *syncTerminal
	stall   bool
	entered chan struct{}
	release chan struct{}
}

func (w *stallingWriter) Write(p []byte) (int, error) {
	if w.stall {
		w.stall = false
		close(w.entered)
		<-w.release
	}
	return w.out.Write(p)
}

// An overlay draw that lands while the child's chunk is between "tracked"
// and "on the terminal" must not restore the cursor to where the chunk will
// leave it: the chunk is relative text, so it would then be painted from
// that restored position instead of from where the child left the cursor.
// The draw has to be ordered entirely before or entirely after the chunk.
func TestOverlayDrawDuringChildWriteRestoresWhereTheTerminalIs(t *testing.T) {
	term := &syncTerminal{}
	child := &stallingWriter{out: term, entered: make(chan struct{}), release: make(chan struct{})}
	pw := NewProtectedWriter(child, 40, 10, FilterConfig{ProtectBottomRows: 1})
	t.Cleanup(func() { SetChildCursorSource(nil) })
	SetChildCursorSource(pw.HoldChildCursor)

	// The child parks its cursor at row 7, col 12.
	if _, err := pw.Write([]byte("\x1b[7;12H")); err != nil {
		t.Fatalf("filter write: %v", err)
	}

	// Next chunk is plain text, painted wherever the cursor is.
	child.stall = true
	childDone := make(chan struct{})
	go func() {
		defer close(childDone)
		_, _ = pw.Write([]byte("hello"))
	}()
	<-child.entered

	r := NewRenderer(term, 40, 10)
	drawDone := make(chan struct{})
	go func() {
		defer close(drawDone)
		r.DrawIndicator(Status{})
	}()
	// A correct draw waits for the chunk; give a racing one time to land.
	select {
	case <-drawDone:
	case <-time.After(200 * time.Millisecond):
	}
	close(child.release)
	<-childDone
	<-drawDone

	vt := vt10x.New(vt10x.WithSize(40, 10))
	term.mu.Lock()
	_, _ = vt.Write(term.buf.Bytes())
	stream := term.buf.String()
	term.mu.Unlock()

	var got []rune
	for x := 11; x < 16; x++ {
		got = append(got, vt.Cell(x, 6).Char)
	}
	if string(got) != "hello" {
		var row []rune
		for x := 0; x < 40; x++ {
			ch := vt.Cell(x, 6).Char
			if ch == 0 {
				ch = ' '
			}
			row = append(row, ch)
		}
		t.Errorf("child text landed off its cursor: row 7 = %q, want \"hello\" at col 12.\nstream: %q", string(row), stream)
	}
}
