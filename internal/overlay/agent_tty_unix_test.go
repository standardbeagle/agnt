//go:build unix

package overlay

import (
	"bytes"
	"errors"
	"io"
	"os"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/term"
)

// pollablePTY opens a PTY pair whose master the Go runtime polls, as
// startPollablePTY in cmd/agnt does for the real agent. Only a polled file
// honours write deadlines, which is how the stuck-deadline bug got live.
func pollablePTY(t testing.TB) (master, slave *os.File) {
	t.Helper()
	m, s, err := pty.Open()
	require.NoError(t, err)
	// Raw, as the agent runs it: a line-mode slave hands over nothing
	// before a newline and drops input once its line buffer is full.
	_, err = term.MakeRaw(int(s.Fd()))
	require.NoError(t, err)
	fd, err := syscall.Dup(int(m.Fd()))
	require.NoError(t, err)
	require.NoError(t, syscall.SetNonblock(fd, true))
	_ = m.Close()
	master = os.NewFile(uintptr(fd), "ptmx")
	t.Cleanup(func() { _ = master.Close(); _ = s.Close() })
	return master, s
}

func readAtLeast(t *testing.T, f *os.File, n int) string {
	t.Helper()
	buf := make([]byte, 0, n)
	tmp := make([]byte, 256)
	_ = f.SetReadDeadline(time.Now().Add(2 * time.Second))
	for len(buf) < n {
		k, err := f.Read(tmp)
		buf = append(buf, tmp[:k]...)
		if err != nil {
			break
		}
	}
	return string(buf)
}

// The bug: an injected message set a write deadline on the agent's terminal
// and never cleared it. Once it passed, every typed key failed with "i/o
// timeout" and reached nothing, while each new injection, setting a fresh
// deadline, still arrived. Here a typed key long after an injection arrives.
func TestTypedKeysStillArriveLongAfterAnInjection(t *testing.T) {
	master, slave := pollablePTY(t)
	tty := NewAgentTTY(master)
	tty.SetWriteTimeoutForTest(20 * time.Millisecond)
	var reported []string
	tty.SetWriteErrorHandler(func(source string, err error) { reported = append(reported, source+": "+err.Error()) })

	router := NewInputRouter(tty, New(tty, 80, 24, DefaultConfig()))

	_, err := tty.WriteFrom("type", []byte("a"))
	require.NoError(t, err)
	time.Sleep(60 * time.Millisecond) // three deadlines' worth
	router.passthrough([]byte("b"))
	_, err = tty.WriteFrom("keyboard", []byte("c"))
	require.NoError(t, err)

	assert.Equal(t, "abc", readAtLeast(t, slave, 3), "the injection and both typed keys reached the agent")
	assert.Empty(t, reported, "no write failed")

	events := tty.Recorder().Events()
	require.Len(t, events, 3)
	assert.Equal(t, []string{"type", "keyboard", "keyboard"}, []string{events[0].Source, events[1].Source, events[2].Source})
}

// An agent that stops reading still makes a write fail, after the bound and
// not forever, and the failure is reported once, with what was not sent.
func TestAStalledAgentFailsTheWriteVisibly(t *testing.T) {
	master, _ := pollablePTY(t) // the slave is never read
	tty := NewAgentTTY(master)
	tty.SetWriteTimeoutForTest(30 * time.Millisecond)
	var mu sync.Mutex
	var reports []string
	tty.SetWriteErrorHandler(func(source string, err error) {
		mu.Lock()
		reports = append(reports, source+": "+err.Error())
		mu.Unlock()
	})

	big := bytes.Repeat([]byte("x"), 256<<10) // far past the PTY buffer
	start := time.Now()
	n, err := tty.WriteFrom("type", big)
	elapsed := time.Since(start)
	require.Error(t, err)
	assert.ErrorIs(t, err, os.ErrDeadlineExceeded)
	assert.Contains(t, err.Error(), "agent did not read its input")
	assert.Less(t, n, len(big))
	assert.Less(t, elapsed, 2*time.Second, "bounded, not blocked")

	_, err = tty.WriteFrom("keyboard", big)
	require.Error(t, err, "still stalled")
	mu.Lock()
	assert.Len(t, reports, 1, "one report per streak of failures")
	assert.True(t, strings.HasPrefix(reports[0], "type: "))
	mu.Unlock()

	events := tty.Recorder().Events()
	require.NotEmpty(t, events)
	last := events[len(events)-1]
	assert.Equal(t, "keyboard!failed", last.Source, "the bytes that were not sent are recorded as such")
	assert.LessOrEqual(t, len(last.Data), ttyRecordBytes, "the recording stays within its budget")
}

// The deadline is set and cleared inside each write, under the lock, so no
// writer ever inherits another's.
func TestEveryWriteSetsAndClearsItsOwnDeadline(t *testing.T) {
	fb := &deadlineLog{}
	tty := NewAgentTTY(fb)
	var wg sync.WaitGroup
	for w := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 50 {
				_, _ = tty.WriteFrom("w", []byte{byte('a' + w)})
			}
		}()
	}
	wg.Wait()
	fb.mu.Lock()
	defer fb.mu.Unlock()
	require.Len(t, fb.ops, 3*8*50)
	for i := 0; i < len(fb.ops); i += 3 {
		assert.Equal(t, []string{"set", "write", "clear"}, fb.ops[i:i+3], "op %d", i)
	}
}

type deadlineLog struct {
	mu  sync.Mutex
	ops []string
}

func (d *deadlineLog) Read([]byte) (int, error) { return 0, io.EOF }
func (d *deadlineLog) Write(p []byte) (int, error) {
	d.mu.Lock()
	d.ops = append(d.ops, "write")
	d.mu.Unlock()
	return len(p), nil
}
func (d *deadlineLog) SetWriteDeadline(t time.Time) error {
	d.mu.Lock()
	if t.IsZero() {
		d.ops = append(d.ops, "clear")
	} else {
		d.ops = append(d.ops, "set")
	}
	d.mu.Unlock()
	return nil
}

// A streak ends on a successful write; the next failure is reported again.
func TestWriteFailureIsReportedAgainAfterRecovery(t *testing.T) {
	fw := &flakyWriter{}
	tty := NewAgentTTY(fw)
	var n int
	tty.SetWriteErrorHandler(func(string, error) { n++ })
	fw.fail = true
	_, _ = tty.WriteFrom("k", []byte("a"))
	_, _ = tty.WriteFrom("k", []byte("a"))
	fw.fail = false
	_, _ = tty.WriteFrom("k", []byte("a"))
	fw.fail = true
	_, _ = tty.WriteFrom("k", []byte("a"))
	assert.Equal(t, 2, n)
}

type flakyWriter struct{ fail bool }

func (f *flakyWriter) Read([]byte) (int, error) { return 0, io.EOF }
func (f *flakyWriter) Write(p []byte) (int, error) {
	if f.fail {
		return 0, errors.New("boom")
	}
	return len(p), nil
}

// The recorder keeps each direction to its budget, newest kept, merges the
// two in time order, and quotes bytes so escape sequences show.
func TestTTYRecorderBudgetOrderAndDump(t *testing.T) {
	r := NewTTYRecorder(10)
	r.Record(TTYIn, "keyboard", []byte("12345"))
	r.Record(TTYOut, "agent", []byte("\x1b[?2004h"))
	r.Record(TTYIn, "type", []byte("6789"))
	r.Record(TTYIn, "keyboard", []byte("ab")) // pushes "12345" out
	r.Record(TTYIn, "huge", []byte("0123456789XYZ"))

	ev := r.Events()
	var in []string
	for _, e := range ev {
		if e.Dir == TTYIn {
			in = append(in, string(e.Data))
		}
	}
	assert.Equal(t, []string{"3456789XYZ"}, in, "an oversized event keeps its newest budget bytes and evicts the rest")
	for i := 1; i < len(ev); i++ {
		assert.False(t, ev[i].At.Before(ev[i-1].At), "time order")
	}
	dump := r.Dump()
	assert.Contains(t, dump, `"\x1b[?2004h"`)
	assert.Contains(t, dump, "< agent")
	assert.Contains(t, dump, "> huge")
}
