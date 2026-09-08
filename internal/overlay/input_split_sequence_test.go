package overlay

import (
	"bytes"
	"io"
	"testing"
	"time"
)

// A terminal does not promise to deliver an escape sequence in one read. Over
// SSH, on a loaded machine, or from a terminal that writes a report in two
// parts, the bytes of one sequence arrive spread over time. Every layer that
// decides what a sequence means therefore has to survive that split, and the
// failure is never quiet: the overlay acts on a fragment and the rest of the
// sequence is typed into the wrapped agent as text.
//
// These tests drive the whole input path -- byte source, win32 scanner, router
// loop, child PTY -- and deliver each sequence one byte at a time with a gap
// wider than the scanner's hold budget. The budget is shrunk to keep the tests
// quick; the ordering they exercise is the same one a slow link produces.
const (
	testPendingFlush = 5 * time.Millisecond
	testSplitGap     = 4 * testPendingFlush
)

// splitRouter builds a router whose input arrives through a pipe and whose
// child writes land in a recorder, with the scanner's hold budget shrunk.
func splitRouter(t *testing.T, state State) (*InputRouter, *Overlay, *io.PipeWriter, *writeRecorder) {
	t.Helper()
	rec := &writeRecorder{}
	cfg := DefaultConfig()
	cfg.ShowIndicator = true
	ov := New(rec, 80, 24, cfg)
	ov.renderer = NewRenderer(&bytes.Buffer{}, 80, 24)
	ov.state.Store(int32(state))

	pr, pw := io.Pipe()
	router := NewInputRouter(rec, ov)
	router.input = pr
	router.pendingFlush = testPendingFlush

	go func() { _ = router.Run() }()
	t.Cleanup(func() {
		router.Stop()
		_ = pw.Close()
	})
	return router, ov, pw, rec
}

// typeSplitAt delivers seq in two reads, cut at index i, with a gap between
// them wider than the budget for holding a bare Escape. This is how a real
// terminal splits a sequence: at a read boundary, not per byte.
func typeSplitAt(t *testing.T, pw *io.PipeWriter, seq string, i int) {
	t.Helper()
	for _, part := range []string{seq[:i], seq[i:]} {
		if _, err := pw.Write([]byte(part)); err != nil {
			t.Fatalf("writing %q: %v", part, err)
		}
		time.Sleep(testSplitGap)
	}
	time.Sleep(10 * testSplitGap)
}

// typeByteAtATime is the harshest delivery: every byte in its own read, spaced
// wider than any hold budget.
func typeByteAtATime(t *testing.T, pw *io.PipeWriter, seq string) {
	t.Helper()
	for i := 0; i < len(seq); i++ {
		if _, err := pw.Write([]byte{seq[i]}); err != nil {
			t.Fatalf("writing byte %d of %q: %v", i, seq, err)
		}
		time.Sleep(testSplitGap)
	}
	time.Sleep(10 * testSplitGap)
}

func childBytes(rec *writeRecorder) []byte {
	var out []byte
	for _, w := range rec.getWrites() {
		out = append(out, w...)
	}
	return out
}

// Ctrl+Right opens the panel browser from the indicator state. A terminal may
// deliver that sequence in two reads, and the second may arrive long after the
// first. The binding must survive the split: a sequence released in fragments
// matches nothing, so the key is typed into the agent instead of acted on.
//
// Every cut of the sequence after its introducer is exercised, because the
// read boundary lands wherever the link happens to put it.
func TestSplitCtrlArrowStillReachesItsBinding(t *testing.T) {
	const seq = "\x1b[1;5C"
	for cut := 2; cut < len(seq); cut++ {
		_, ov, pw, rec := splitRouter(t, StateIndicator)

		typeSplitAt(t, pw, seq, cut)

		if got := childBytes(rec); len(got) != 0 {
			t.Errorf("cut at %d: Ctrl+Right was typed into the child as %q; it binds to the panel browser", cut, got)
		}
		if ov.State() != StateMenu {
			t.Errorf("cut at %d: overlay state = %v, want StateMenu (the panel browser did not open)", cut, ov.State())
		}
	}
}

// The same for the forwarding hotkeys, which are matched on the same path.
func TestSplitCtrlUpStillPausesForwarding(t *testing.T) {
	router, _, pw, rec := splitRouter(t, StateIndicator)
	paused := make(chan bool, 4)
	router.SetForwardingToggle(func(p bool) { paused <- p })

	typeSplitAt(t, pw, "\x1b[1;5A", 4)

	select {
	case got := <-paused:
		if !got {
			t.Error("Ctrl+Up resumed forwarding; it pauses it")
		}
	default:
		t.Errorf("a split Ctrl+Up did not pause forwarding; the child was sent %q instead", childBytes(rec))
	}
}

// With a panel open the overlay owns input, and a mouse report must be
// swallowed whole. Split delivery must not turn it into an Escape that closes
// the panel and hands the rest of the report to the child.
func TestSplitMouseReportLeavesThePanelOpen(t *testing.T) {
	_, ov, pw, rec := splitRouter(t, StateMenu)
	ov.panelMode = true
	ov.panelItems = []PanelItem{{Type: "overview", Label: "overview"}}

	typeByteAtATime(t, pw, "\x1b[<35;80;24M")

	if got := childBytes(rec); len(got) != 0 {
		t.Errorf("a split mouse report reached the child as %q", got)
	}
	if ov.State() != StateMenu {
		t.Errorf("overlay state = %v after a split mouse report, want StateMenu (the panel closed)", ov.State())
	}
	if !ov.panelMode {
		t.Error("a split mouse report left panel mode")
	}
}

// On the passthrough path a sequence the overlay does not bind belongs to the
// child, and it must arrive whole and unchanged. Losing a byte to a hotkey
// matcher, or dropping one, corrupts what the child parses.
func TestUnboundSequenceReachesTheChildIntact(t *testing.T) {
	const report = "\x1b[<35;80;24M"
	_, _, pw, rec := splitRouter(t, StateHidden)

	typeSplitAt(t, pw, report, 5)

	if got := string(childBytes(rec)); got != report {
		t.Errorf("child received %q, want %q", got, report)
	}
}

// The limit of what reassembly can do, recorded so it is not mistaken for a
// regression. A lone Escape keypress and the first byte of a sequence are the
// same byte, so an ESC that arrives with nothing behind it has to be released
// on a short budget or Escape would never reach the child at all. A sequence
// whose introducer is separated from the rest by more than that budget is
// therefore released as a bare Escape, and the remainder is passed through.
//
// The degradation is graceful and must stay that way: the child receives every
// byte, in order, and the overlay does not act on the fragment.
func TestIntroducerSeparatedFromItsSequenceDegradesToPassthrough(t *testing.T) {
	const seq = "\x1b[1;5C"
	_, ov, pw, rec := splitRouter(t, StateIndicator)

	typeByteAtATime(t, pw, seq)

	if got := string(childBytes(rec)); got != seq {
		t.Errorf("child received %q, want the whole sequence %q in order", got, seq)
	}
	if ov.State() != StateIndicator {
		t.Errorf("overlay state = %v, want StateIndicator (a fragment was acted on)", ov.State())
	}
}
