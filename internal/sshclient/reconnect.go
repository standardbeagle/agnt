package sshclient

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/standardbeagle/agnt/internal/debug"
)

// This file implements the reconnect state machine (task 09c, spec
// docs/superpowers/specs/2026-07-03-remote-ssh-design.md §3.6):
//
//	CONNECTED --(Dead() fires / dial or attach error)--> RECONNECTING
//	RECONNECTING --(backoff)--> dial+attach attempt --(ok)--> CONNECTED
//	RECONNECTING --(attempt fails, not fatal)--> backoff again
//	RECONNECTING --(named session gone, no --create-if-missing)--> fatal, stop
//
// It deliberately does not reimplement liveness detection: callers drive
// the CONNECTED->RECONNECTING transition off the existing Client.Dead()
// channel (client.go, task 04b's bounded keepalive probe). This file only
// owns what happens *after* the transport is known dead: backoff, re-dial,
// re-attach, and the terminal-side bookkeeping (status line ordering,
// Ctrl-C during the backoff/attach window) that the CLI needs to drive it.

// ErrSessionMissing is returned by an AttachFunc when the named remote
// session-host session no longer exists and the caller has not opted in to
// creating a new one. The Reconnector treats this as fatal (invariant 24:
// reconnect never re-creates) — it does not retry past this error, and
// unwraps with errors.Is/errors.As through any %w wrapping the caller adds.
var ErrSessionMissing = errors.New("sshclient: remote session-host session not found; reconnect will not create a new one (pass --create-if-missing or --new)")

// BackoffConfig parameterizes the exponential-with-jitter backoff schedule
// (spec invariant 23: 1s, 2s, 4s, ... capped at 30s, ±20% jitter). BaseDelay
// and Jitter are both injectable specifically so tests can assert
// multiplicative growth and jitter bounds deterministically at
// microsecond scale, never via wall-clock sleeps (see
// .claude/rules — no load-sensitive wall-clock primary invariants).
type BackoffConfig struct {
	// BaseDelay is the delay before the first retry attempt. Defaults to
	// 1s if zero.
	BaseDelay time.Duration
	// MaxDelay caps the (pre-jitter) computed delay. Defaults to 30s if
	// zero.
	MaxDelay time.Duration
	// Jitter returns a value in [0,1); Delay maps it onto a ±20% factor
	// ([0.8,1.2)). A nil Jitter disables jitter (factor is always 1.0) —
	// production wiring should pass rand.Float64, tests pass a fixed or
	// sequenced stub for deterministic assertions.
	Jitter func() float64
}

// Delay returns the backoff delay before reconnect attempt N (1-indexed):
// min(BaseDelay*2^(N-1), MaxDelay), then scaled by a ±20% jitter factor
// derived from Jitter(). Pure function — no sleeping, no wall clock reads —
// so it is trivially unit-testable at any timescale.
func (c BackoffConfig) Delay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	base := c.BaseDelay
	if base <= 0 {
		base = time.Second
	}
	capDelay := c.MaxDelay
	if capDelay <= 0 {
		capDelay = 30 * time.Second
	}

	shift := attempt - 1
	if shift > 32 {
		shift = 32 // avoid overflowing the time.Duration multiply below
	}
	mult := time.Duration(1) << uint(shift)
	d := base * mult
	if d <= 0 || d > capDelay {
		d = capDelay
	}

	factor := 1.0
	if c.Jitter != nil {
		factor = 0.8 + 0.4*c.Jitter() // [0,1) -> [0.8,1.2)
	}
	return time.Duration(float64(d) * factor)
}

// DialFunc establishes a fresh transport to the same remote host. It should
// perform dial + host-key verification + auth (mirroring the initial
// sshclient.Dial call) but must NOT touch any session-host state.
type DialFunc func(ctx context.Context) (*Client, error)

// AttachFunc re-attaches to the same named session-host session on an
// already-dialed client. Implementations must return an error satisfying
// errors.Is(err, ErrSessionMissing) when the named session is gone and the
// caller has not opted in to creating a replacement — any other error is
// treated as retryable.
type AttachFunc func(ctx context.Context, client *Client) (*PTYSession, error)

// Reconnector drives the RECONNECTING state: backoff, dial, attach, repeat
// until success, a fatal ErrSessionMissing, ctx cancellation, or MaxAttempts
// is exhausted.
type Reconnector struct {
	Backoff     BackoffConfig
	MaxAttempts int // 0 = unlimited (interactive default, spec invariant 23)
	Dial        DialFunc
	Attach      AttachFunc
	// OnStatus, if non-nil, is called with a short human-readable status
	// line before each attempt and once more on success. Callers wire this
	// to a stderr writer so the line prints before scrollback replay
	// begins on stdout (criterion 3) — Run calls OnStatus and returns the
	// new session synchronously, so the caller's subsequent Relay() call
	// is always ordered after the final OnStatus call.
	OnStatus func(string)
}

// Run attempts to reconnect, blocking (subject to ctx and MaxAttempts)
// across the full backoff schedule. Returns the new Client and PTYSession
// on success. Returns ctx.Err() on cancellation (callers that derive ctx
// specifically to represent a local Ctrl-C, e.g. via InputPump's interrupt
// callback, can treat that as a clean user-requested stop rather than a
// failure — Run itself has no opinion on why ctx was cancelled), or an
// error wrapping ErrSessionMissing if the named session is confirmed gone
// and Attach did not opt into creating one.
func (r *Reconnector) Run(ctx context.Context) (*Client, *PTYSession, error) {
	attempt := 0
	for {
		attempt++
		if r.MaxAttempts > 0 && attempt > r.MaxAttempts {
			return nil, nil, fmt.Errorf("sshclient: reconnect: exceeded max attempts (%d)", r.MaxAttempts)
		}

		delay := r.Backoff.Delay(attempt)
		r.status(fmt.Sprintf("agnt ssh: reconnecting (attempt %d, retrying in %s)...", attempt, delay.Round(time.Millisecond)))

		select {
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		case <-time.After(delay):
		}

		client, err := r.Dial(ctx)
		if err != nil {
			r.status(fmt.Sprintf("agnt ssh: reconnect attempt %d: dial failed: %v", attempt, err))
			continue
		}

		session, err := r.Attach(ctx, client)
		if err != nil {
			client.Close()
			if errors.Is(err, ErrSessionMissing) {
				return nil, nil, err
			}
			r.status(fmt.Sprintf("agnt ssh: reconnect attempt %d: attach failed: %v", attempt, err))
			continue
		}

		r.status("agnt ssh: reconnected")
		return client, session, nil
	}
}

func (r *Reconnector) status(msg string) {
	if r.OnStatus != nil {
		r.OnStatus(msg)
	}
}

// InputPump owns a single real input source (os.Stdin in production) for
// the entire process lifetime, so that repeated reconnects never leave more
// than one goroutine blocked reading it (the generalized lesson in
// .claude/rules/lessons-liveness-probes.md applies equally to fan-out
// readers: an indefinite-blocking Read used to feed a channel that gets
// re-created every reconnect must have exactly one owner, or concurrent
// stale readers race for the same bytes). It forwards bytes to whatever
// target is currently set; while no target is set (the RECONNECTING
// window), it instead watches for a literal Ctrl-C byte (0x03) and invokes
// the configured interrupt callback: a raw-mode terminal (term.MakeRaw
// disables ISIG) never delivers a real SIGINT for Ctrl-C — the byte arrives
// as ordinary stdin data — and during RECONNECTING there is no live Relay()
// loop forwarding stdin to a remote that doesn't exist yet, so this is the
// only way Ctrl-C during backoff/reattach is observable.
type InputPump struct {
	mu             sync.Mutex
	target         io.Writer
	interrupt      func()
	writeErrLogged atomic.Bool
}

// NewInputPump constructs a pump; interrupt is called (from the pump's own
// goroutine) whenever a Ctrl-C byte arrives while no target is set. interrupt
// may be nil.
func NewInputPump(interrupt func()) *InputPump {
	return &InputPump{interrupt: interrupt}
}

// Start begins reading src in a single background goroutine. Call at most
// once per InputPump. The goroutine runs until src.Read returns an error
// (EOF or otherwise) — for a real os.Stdin that is effectively "until the
// process exits," matching the existing fire-and-forget stdin-copy pattern
// in PTYSession.Relay; tests should pass a closable io.Reader (e.g. an
// io.Pipe) so the goroutine can be observed to exit for goleak checks.
func (p *InputPump) Start(src io.Reader) {
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := src.Read(buf)
			if n > 0 {
				p.handle(buf[:n])
			}
			if err != nil {
				return
			}
		}
	}()
}

func (p *InputPump) handle(data []byte) {
	p.mu.Lock()
	target := p.target
	p.mu.Unlock()

	if target == nil {
		if p.interrupt != nil && bytes.IndexByte(data, 0x03) >= 0 {
			p.interrupt()
		}
		return
	}
	if _, err := target.Write(data); err != nil {
		// Dropped keystrokes are otherwise invisible and make reconnect
		// debugging miserable; log once per failure streak.
		if p.writeErrLogged.CompareAndSwap(false, true) {
			debug.Log("sshclient", "input pump: dropping input, write failed: %v", err)
		}
		return
	}
	p.writeErrLogged.Store(false)
}

// SetTarget switches the forwarding destination. Pass nil to enter the
// "no live session" state, which arms the Ctrl-C watch instead of
// forwarding bytes (used for the RECONNECTING window).
func (p *InputPump) SetTarget(w io.Writer) {
	p.mu.Lock()
	p.target = w
	p.mu.Unlock()
}
