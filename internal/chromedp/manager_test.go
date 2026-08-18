package chromedp

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeStart drives an AutomationSession through its real state machine and
// context/Done() plumbing WITHOUT launching a Chrome process. It mirrors the
// tail of (*AutomationSession).Start (contexts, Running CAS, monitor goroutine
// that closes done on cancellation) so that manager.Stop → session.Stop →
// cleanup → done-close → the manager's slot-release goroutine all behave
// identically to production, letting the concurrency cap be asserted in the
// default test suite (no chromee2e tier, no real browser).
func fakeStart(ctx context.Context, s *AutomationSession) error {
	if !s.compareAndSwapState(StateIdle, StateStarting) {
		return fmt.Errorf("session already started")
	}
	s.allocCtx, s.allocCancel = context.WithCancel(ctx)
	s.taskCtx, s.taskCancel = context.WithCancel(s.allocCtx)

	if !s.compareAndSwapState(StateStarting, StateRunning) {
		s.cleanup()
		s.setState(StateStopped)
		close(s.done)
		return fmt.Errorf("session stopped during startup")
	}

	now := time.Now()
	s.startedAt.Store(&now)

	go func() {
		defer close(s.done)
		<-s.taskCtx.Done()
		if s.State() == StateRunning {
			s.setState(StateStopped)
		}
	}()
	return nil
}

// newFakeManager builds a manager whose sessions never spawn Chrome.
func newFakeManager(max int) *SessionManager {
	m := NewSessionManagerWithMax(max)
	m.startSession = fakeStart
	return m
}

func fillToCap(t *testing.T, m *SessionManager, cap int) {
	t.Helper()
	for i := 0; i < cap; i++ {
		if _, err := m.Start(context.Background(), fmt.Sprintf("sess-%d", i), SessionConfig{Headless: true}); err != nil {
			t.Fatalf("Start #%d within cap should succeed, got: %v", i, err)
		}
	}
	if got := m.ActiveCount(); got != cap {
		t.Fatalf("ActiveCount after filling to cap = %d, want %d", got, cap)
	}
}

// TestStart_RefusesAtCap is the load-bearing cap assertion: the (N+1)th Start
// is REFUSED with an actionable error (positive assertion, not merely that N
// succeed) and never mutates active past the ceiling. Removing the cap check in
// Start makes this fail (the (N+1)th create would succeed) — mutation guard.
func TestStart_RefusesAtCap(t *testing.T) {
	const cap = 3
	m := newFakeManager(cap)
	fillToCap(t, m, cap)

	sess, err := m.Start(context.Background(), "overflow", SessionConfig{Headless: true})
	if err == nil {
		t.Fatal("(N+1)th Start must be refused at the cap, but it succeeded")
	}
	if sess != nil {
		t.Fatalf("refused Start must return a nil session, got %v", sess)
	}
	if !errors.Is(err, ErrSessionCapReached) {
		t.Fatalf("refusal must wrap ErrSessionCapReached, got: %v", err)
	}
	// Actionable error: names the limit and how to free a slot.
	msg := err.Error()
	lowMsg := strings.ToLower(msg)
	for _, want := range []string{"3", "automation.max-sessions", "stop"} {
		if !strings.Contains(lowMsg, want) {
			t.Fatalf("cap error must mention %q (actionable), got: %q", want, msg)
		}
	}
	if got := m.ActiveCount(); got != cap {
		t.Fatalf("active must never exceed the cap: got %d, want %d", got, cap)
	}
	// The refused id must not be left registered.
	if _, err := m.Get("overflow"); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("refused session id must not linger in the registry, Get err = %v", err)
	}
}

// TestStart_ReleaseFreesSlot proves a stopped session frees a slot: the count
// decrements and a subsequent Start succeeds.
func TestStart_ReleaseFreesSlot(t *testing.T) {
	const cap = 2
	m := newFakeManager(cap)
	fillToCap(t, m, cap)

	// At cap: next Start refused.
	if _, err := m.Start(context.Background(), "blocked", SessionConfig{Headless: true}); !errors.Is(err, ErrSessionCapReached) {
		t.Fatalf("at cap Start should be refused, got: %v", err)
	}

	// Release one slot.
	if err := m.Stop(context.Background(), "sess-0"); err != nil {
		t.Fatalf("Stop failed: %v", err)
	}
	waitForActive(t, m, cap-1)

	// A subsequent Start now succeeds.
	if _, err := m.Start(context.Background(), "after-release", SessionConfig{Headless: true}); err != nil {
		t.Fatalf("Start after releasing a slot should succeed, got: %v", err)
	}
	if got := m.ActiveCount(); got != cap {
		t.Fatalf("ActiveCount after refill = %d, want %d", got, cap)
	}
}

// TestStart_ConcurrentNeverExceedsCap races many Start calls and asserts active
// never crosses the ceiling — the check+increment is a single atomic admission
// point, not a load-then-add TOCTOU window (publish-security-review-lessons §4).
// Run under -race.
func TestStart_ConcurrentNeverExceedsCap(t *testing.T) {
	const cap = 4
	const racers = 40
	m := newFakeManager(cap)

	var successes atomic.Int32
	var maxObserved atomic.Int32
	var wg sync.WaitGroup
	start := make(chan struct{})

	observe := func() {
		for {
			cur := m.active.Load()
			old := maxObserved.Load()
			if cur <= old || maxObserved.CompareAndSwap(old, cur) {
				break
			}
		}
	}

	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, err := m.Start(context.Background(), fmt.Sprintf("race-%d", i), SessionConfig{Headless: true})
			observe()
			if err == nil {
				successes.Add(1)
			} else if !errors.Is(err, ErrSessionCapReached) {
				t.Errorf("racing Start returned unexpected error: %v", err)
			}
		}(i)
	}
	close(start)
	wg.Wait()

	if got := successes.Load(); got != cap {
		t.Fatalf("exactly cap sessions should be admitted: got %d, want %d", got, cap)
	}
	if got := maxObserved.Load(); got > cap {
		t.Fatalf("active exceeded the cap during the race: observed %d, cap %d", got, cap)
	}
	if got := m.ActiveCount(); got != cap {
		t.Fatalf("final ActiveCount = %d, want %d", got, cap)
	}
}

// TestSetMaxSessions_DrivesRuntime proves a non-default ceiling actually changes
// admission behaviour (config §5: a parsed value must drive runtime, not just
// be stored).
func TestSetMaxSessions_DrivesRuntime(t *testing.T) {
	m := newFakeManager(DefaultMaxSessions)
	m.SetMaxSessions(1)
	if got := m.MaxSessions(); got != 1 {
		t.Fatalf("MaxSessions = %d, want 1", got)
	}

	if _, err := m.Start(context.Background(), "only", SessionConfig{Headless: true}); err != nil {
		t.Fatalf("first Start under cap=1 should succeed: %v", err)
	}
	if _, err := m.Start(context.Background(), "second", SessionConfig{Headless: true}); !errors.Is(err, ErrSessionCapReached) {
		t.Fatalf("second Start under cap=1 must be refused, got: %v", err)
	}

	// A non-positive value restores the default.
	m.SetMaxSessions(0)
	if got := m.MaxSessions(); got != DefaultMaxSessions {
		t.Fatalf("SetMaxSessions(0) should restore default %d, got %d", DefaultMaxSessions, got)
	}
}

func waitForActive(t *testing.T, m *SessionManager, want int) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		if m.ActiveCount() == want {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for ActiveCount == %d (got %d)", want, m.ActiveCount())
		default:
			time.Sleep(time.Millisecond)
		}
	}
}
