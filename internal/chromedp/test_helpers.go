package chromedp

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// UseFakeLauncherForTest makes m start sessions without launching Chrome (see
// fakeStart), so other packages' tests can hold real AutomationSessions in the
// default suite. The *testing.T parameter keeps it out of production use.
func UseFakeLauncherForTest(t *testing.T, m *SessionManager) {
	t.Helper()
	m.startSession = fakeStart
}

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
