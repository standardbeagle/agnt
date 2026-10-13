//go:build unix

package main

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestCanonicalAlertText(t *testing.T) {
	cases := map[string]string{
		"Error: Foo Bar":             "error: foo bar",
		"Error:    Foo   Bar":        "error: foo bar",
		"TypeError at app.js:12:3":   "typeerror at app.js",
		"TypeError at app.js:88:1":   "typeerror at app.js",
		"ReferenceError (main.ts:5)": "referenceerror (main.ts",
	}
	for in, want := range cases {
		if got := canonicalAlertText(in); got != want {
			t.Errorf("canonicalAlertText(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestGateDedupWithinWindow(t *testing.T) {
	g := newACPAlertGate(nil)
	if !g.add("Error: boom") {
		t.Fatal("first add should be accepted")
	}
	if g.add("error:   boom") { // canonical-identical
		t.Fatal("canonical-duplicate should be suppressed")
	}
	if g.add("Error: boom at app.js:12:3") {
		// canonical "error: boom at app.js" — distinct from "error: boom"
		// (different message), so this one is NEW.
	}
	// Two distinct alerts pending (the dup was dropped).
	if got, ok := g.Pull(); !ok || strings.Count(got, "\n") != 1 {
		t.Fatalf("expected 2 distinct pending, got ok=%v %q", ok, got)
	}
}

func TestGateLineColDedup(t *testing.T) {
	g := newACPAlertGate(nil)
	g.add("TypeError at app.js:12:3")
	if g.add("TypeError at app.js:88:9") {
		t.Fatal("same error at a different line/col should dedupe")
	}
	got, ok := g.Pull()
	if !ok || strings.Contains(got, "\n") {
		t.Fatalf("expected a single coalesced alert, got %q", got)
	}
}

func TestGatePullClearsAndJoins(t *testing.T) {
	g := newACPAlertGate(nil)
	g.add("one")
	g.add("two")
	got, ok := g.Pull()
	if !ok || got != "one\ntwo" {
		t.Fatalf("Pull = %q, %v; want \"one\\ntwo\"", got, ok)
	}
	if _, ok := g.Pull(); ok {
		t.Fatal("second Pull should be empty after clear")
	}
}

func TestGateMaxBatchTruncation(t *testing.T) {
	g := newACPAlertGate(nil)
	g.maxBatch = 3
	for _, m := range []string{"a", "b", "c", "d", "e"} {
		g.add(m)
	}
	got, ok := g.Pull()
	if !ok {
		t.Fatal("expected pending")
	}
	if !strings.Contains(got, "2 more alert(s) dropped") {
		t.Fatalf("expected truncation notice, got %q", got)
	}
	if strings.Count(got, "\n") != 3 { // 3 items + notice line = 3 newlines
		t.Fatalf("expected 3 kept + notice, got %q", got)
	}
}

func TestGateDedupWindowExpiry(t *testing.T) {
	g := newACPAlertGate(nil)
	g.dedupeWin = 20 * time.Millisecond
	if !g.add("recurring") {
		t.Fatal("first add accepted")
	}
	if g.add("recurring") {
		t.Fatal("within window should suppress")
	}
	time.Sleep(40 * time.Millisecond)
	if !g.add("recurring") {
		t.Fatal("after window expiry the alert should re-fire")
	}
}

// TestGateRunBatchesAndDedups exercises the goroutine: a burst with a dup
// coalesces into one Ready signal carrying the deduped set. No arbitrary
// sleeps — it waits on the Ready signal.
func TestGateRunBatchesAndDedups(t *testing.T) {
	in := make(chan aiOverlayMessage, 8)
	g := newACPAlertGate(in)
	g.batchWin = 25 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go g.Run(ctx)

	in <- aiOverlayMessage{text: "alpha"}
	in <- aiOverlayMessage{text: "alpha"} // dup
	in <- aiOverlayMessage{text: "beta"}

	select {
	case <-g.Ready():
	case <-time.After(2 * time.Second):
		t.Fatal("gate never signaled Ready")
	}
	got, ok := g.Pull()
	if !ok || got != "alpha\nbeta" {
		t.Fatalf("Pull = %q, %v; want \"alpha\\nbeta\"", got, ok)
	}
}

// TestGateProtectedMessagesBypassDedupAndCaps pins the PTY path's rule on this
// path too: an explicit user action is never deduped and never dropped by the
// batch cap, while auto-generated alerts around it still are.
func TestGateProtectedMessagesBypassDedupAndCaps(t *testing.T) {
	g := newACPAlertGate(nil)
	g.maxBatch = 1
	g.addMessage(aiOverlayMessage{text: "yes", protected: true})
	g.addMessage(aiOverlayMessage{text: "yes", protected: true}) // same text, must survive
	g.add("alert one")
	g.add("alert two") // over the cap of 1
	g.addMessage(aiOverlayMessage{text: "panel message", protected: true})

	got, ok := g.Pull()
	if !ok {
		t.Fatal("expected pending")
	}
	if strings.Count(got, "yes") != 2 {
		t.Fatalf("protected duplicate was deduped: %q", got)
	}
	if !strings.Contains(got, "panel message") {
		t.Fatalf("protected message after the cap was dropped: %q", got)
	}
	if strings.Contains(got, "alert two") || !strings.Contains(got, "1 more alert(s) dropped") {
		t.Fatalf("unprotected alert past the cap should be dropped and counted: %q", got)
	}
}

// TestGateReportsOverflow: alerts lost upstream (full channel) or to the
// backlog bound are counted and announced in the next Pull instead of
// vanishing into the debug log.
func TestGateReportsOverflow(t *testing.T) {
	t.Run("upstream channel drops", func(t *testing.T) {
		g := newACPAlertGate(nil)
		upstream := 3
		g.overflow = func() int { n := upstream; upstream = 0; return n }
		g.add("kept")
		got, ok := g.Pull()
		if !ok || !strings.Contains(got, "kept") || !strings.Contains(got, "3 more alert(s) dropped") {
			t.Fatalf("Pull = %q, %v; want kept + drop notice", got, ok)
		}
		if _, ok := g.Pull(); ok {
			t.Fatal("drop count must reset after being reported")
		}
	})

	t.Run("backlog bound", func(t *testing.T) {
		g := newACPAlertGate(nil)
		g.maxPending = 2
		for _, m := range []string{"a", "b", "c", "d"} {
			g.add(m)
		}
		got, ok := g.Pull()
		if !ok || !strings.Contains(got, "2 more alert(s) dropped") {
			t.Fatalf("Pull = %q, %v; want a 2-dropped notice", got, ok)
		}
	})

	t.Run("notice alone still surfaces", func(t *testing.T) {
		g := newACPAlertGate(nil)
		g.overflow = func() int { return 1 }
		if got, ok := g.Pull(); !ok || !strings.Contains(got, "1 more alert(s) dropped") {
			t.Fatalf("Pull = %q, %v; want the notice with nothing else pending", got, ok)
		}
	})
}

// TestGateNilSafe verifies a nil gate is inert (the --no-session path).
func TestGateNilSafe(t *testing.T) {
	var g *acpAlertGate
	if g.Ready() != nil {
		t.Fatal("nil gate Ready() must be a nil channel")
	}
	if _, ok := g.Pull(); ok {
		t.Fatal("nil gate Pull() must report empty")
	}
}
