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
//
// Despite the name the gate is shared: `agnt ai claude` runs its AIOverlay
// channel through the same instance type, so neither REPL turns a raw event
// stream into one paid model turn per event.
//
// Protected messages (explicit user actions: panel messages, sketches,
// `agnt session send`) follow the PTY path's rule — never deduped, never
// dropped by a cap. Only auto-generated alerts are.
type acpAlertGate struct {
	in        <-chan aiOverlayMessage
	ready     chan struct{} // buffered(1): a batch is available to pull
	dedupeWin time.Duration
	batchWin  time.Duration
	maxBatch  int
	// maxPending bounds the unprotected backlog while the REPL is busy in a
	// long turn; alerts past it are counted in dropped, not queued.
	maxPending int
	// overflow, when set, returns (and resets) the number of messages the
	// upstream channel dropped because it was full, so Pull can report them.
	overflow func() int

	mu      sync.Mutex
	pending []aiOverlayMessage
	seen    map[string]time.Time
	dropped int
}

func newACPAlertGate(in <-chan aiOverlayMessage) *acpAlertGate {
	return &acpAlertGate{
		in:         in,
		ready:      make(chan struct{}, 1),
		dedupeWin:  10 * time.Second,
		batchWin:   750 * time.Millisecond,
		maxBatch:   20,
		maxPending: 200,
		seen:       make(map[string]time.Time),
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
	upstream := 0
	if g.overflow != nil {
		upstream = g.overflow()
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	dropped := g.dropped + upstream
	g.dropped = 0
	if len(g.pending) == 0 && dropped == 0 {
		return "", false
	}
	// Keep every protected message; cap only the auto-generated alerts.
	items := make([]string, 0, len(g.pending))
	alerts := 0
	for _, m := range g.pending {
		if !m.protected {
			if alerts >= g.maxBatch {
				dropped++
				continue
			}
			alerts++
		}
		items = append(items, m.text)
	}
	g.pending = nil
	out := strings.Join(items, "\n")
	if dropped > 0 {
		// Say so in the text that reaches both the user and the agent: a
		// silently shortened batch reads as "that was everything".
		if out != "" {
			out += "\n"
		}
		out += "… (" + itoa(dropped) + " more alert(s) dropped — queue full; use get_incidents for the complete list)"
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
			if g.addMessage(msg) && !armed {
				armed = true
				timer.Reset(g.batchWin)
			}
		case <-timer.C:
			armed = false
			// A Pull between the add and this tick may already have taken
			// the batch; signalling then would wake the REPL for nothing.
			g.mu.Lock()
			waiting := len(g.pending) > 0 || g.dropped > 0
			g.mu.Unlock()
			if !waiting {
				continue
			}
			select {
			case g.ready <- struct{}{}:
			default:
			}
		}
	}
}

// add queues msg as an auto-generated (unprotected) alert. Returns true if it
// was newly added.
func (g *acpAlertGate) add(msg string) bool {
	return g.addMessage(aiOverlayMessage{text: msg})
}

// addMessage appends m to pending. An unprotected alert is skipped when an
// identical (canonicalized) one is within the dedupe window, and counted as
// dropped once the backlog is full; a protected message always queues.
// Returns true if something new is waiting (so the batch timer should arm).
func (g *acpAlertGate) addMessage(m aiOverlayMessage) bool {
	m.text = strings.TrimRight(m.text, "\n")
	if strings.TrimSpace(m.text) == "" {
		return false
	}
	now := time.Now()
	g.mu.Lock()
	defer g.mu.Unlock()
	if m.protected {
		g.pending = append(g.pending, m)
		return true
	}
	key := canonicalAlertText(m.text)
	g.pruneLocked(now)
	if last, ok := g.seen[key]; ok && now.Sub(last) < g.dedupeWin {
		g.seen[key] = now // refresh so a steady stream stays suppressed
		return false
	}
	g.seen[key] = now
	if g.maxPending > 0 && len(g.pending) >= g.maxPending {
		g.dropped++
		// Still report true: the drop notice itself must reach the REPL.
		return true
	}
	g.pending = append(g.pending, m)
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
