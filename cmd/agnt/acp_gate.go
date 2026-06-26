//go:build unix

package main

import (
	"context"
	"regexp"
	"strings"
	"sync"
	"time"
)

// acpAlertGate is the single ordered queue between the AIOverlay event channel
// and the ACP prompt loop. It applies the messaging-queue layers that the raw
// AIOverlay channel skips:
//
//  1. Fingerprint + dedup window — identical alerts within dedupeWindow collapse
//     to one (canonicalized text key; mirrors the canonical(message) contract in
//     .claude/skills/messaging-queue).
//  2. Batch window — a burst coalesces into one pending set before it is offered.
//  3. Activity deferral — the gate never injects on its own. The REPL pulls only
//     while idle (between turns), so alerts arriving during a Prompt turn batch
//     until the turn ends. Unlike the PTY path, ACP turn boundaries make this
//     deterministic (no output heuristic).
//
// Throttle falls out for free: the minimum spacing between injected batches is
// batchWindow plus the duration of the turn each batch triggers.
type acpAlertGate struct {
	in        <-chan string
	ready     chan struct{} // buffered(1): a batch is available to pull
	dedupeWin time.Duration
	batchWin  time.Duration
	maxBatch  int

	mu      sync.Mutex
	pending []string
	seen    map[string]time.Time
}

func newACPAlertGate(in <-chan string) *acpAlertGate {
	return &acpAlertGate{
		in:        in,
		ready:     make(chan struct{}, 1),
		dedupeWin: 10 * time.Second,
		batchWin:  750 * time.Millisecond,
		maxBatch:  20,
		seen:      make(map[string]time.Time),
	}
}

// Ready fires (once per batch) when a coalesced set of alerts is available.
// Returns nil for a nil gate so a select case blocks forever.
func (g *acpAlertGate) Ready() <-chan struct{} {
	if g == nil {
		return nil
	}
	return g.ready
}

// Pull returns the coalesced pending alerts as one block and clears them,
// or ("", false) when nothing is pending. Called by the REPL only while idle.
func (g *acpAlertGate) Pull() (string, bool) {
	if g == nil {
		return "", false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.pending) == 0 {
		return "", false
	}
	items := g.pending
	dropped := 0
	if len(items) > g.maxBatch {
		dropped = len(items) - g.maxBatch
		items = items[:g.maxBatch]
	}
	g.pending = nil
	out := strings.Join(items, "\n")
	if dropped > 0 {
		out += "\n… (" + itoa(dropped) + " more alert(s) coalesced)"
	}
	return out, true
}

// Run consumes the input channel until ctx is done or it closes, deduping and
// batching. It signals Ready after batchWin once a fresh alert lands. Run in a
// goroutine.
func (g *acpAlertGate) Run(ctx context.Context) {
	if g == nil {
		return
	}
	timer := time.NewTimer(time.Hour)
	if !timer.Stop() {
		<-timer.C
	}
	armed := false
	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-g.in:
			if !ok {
				return
			}
			if g.add(msg) && !armed {
				armed = true
				timer.Reset(g.batchWin)
			}
		case <-timer.C:
			armed = false
			select {
			case g.ready <- struct{}{}:
			default:
			}
		}
	}
}

// add appends msg to pending unless an identical (canonicalized) alert is
// within the dedupe window. Returns true if it was newly added.
func (g *acpAlertGate) add(msg string) bool {
	msg = strings.TrimRight(msg, "\n")
	if strings.TrimSpace(msg) == "" {
		return false
	}
	key := canonicalAlertText(msg)
	now := time.Now()
	g.mu.Lock()
	defer g.mu.Unlock()
	g.pruneLocked(now)
	if last, ok := g.seen[key]; ok && now.Sub(last) < g.dedupeWin {
		g.seen[key] = now // refresh so a steady stream stays suppressed
		return false
	}
	g.seen[key] = now
	g.pending = append(g.pending, msg)
	return true
}

func (g *acpAlertGate) pruneLocked(now time.Time) {
	for k, t := range g.seen {
		if now.Sub(t) >= g.dedupeWin {
			delete(g.seen, k)
		}
	}
}

var trailingLineColRe = regexp.MustCompile(`[:(]\s*\d+([:,]\s*\d+)?\)?\s*$`)

// canonicalAlertText normalizes an alert string for fingerprinting: lowercase,
// whitespace-collapsed, with a trailing line/column reference stripped so the
// "same error at a shifting position" still dedupes.
func canonicalAlertText(s string) string {
	s = strings.ToLower(strings.Join(strings.Fields(s), " "))
	s = trailingLineColRe.ReplaceAllString(s, "")
	return strings.TrimSpace(s)
}

// itoa avoids importing strconv for one call.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
