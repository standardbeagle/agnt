package incident

import (
	"sync/atomic"
	"testing"
	"time"
)

func TestActivityDetector_IsActiveAfterHook(t *testing.T) {
	t.Parallel()
	ad := NewActivityDetector(50*time.Millisecond, 200*time.Millisecond, nil)
	defer ad.Stop()

	if ad.IsActive() {
		t.Error("should be inactive before any hook")
	}
	ad.RecordHook()
	if !ad.IsActive() {
		t.Error("should be active immediately after hook")
	}
	time.Sleep(70 * time.Millisecond) // past activeWindow
	if ad.IsActive() {
		t.Error("should be inactive after activeWindow elapsed")
	}
}

func TestActivityDetector_OnIdleFires(t *testing.T) {
	t.Parallel()
	var fired atomic.Bool
	ad := NewActivityDetector(20*time.Millisecond, 40*time.Millisecond, func() {
		fired.Store(true)
	})
	defer ad.Stop()

	ad.RecordHook()
	time.Sleep(60 * time.Millisecond) // idleWindow = 40ms
	if !fired.Load() {
		t.Error("onIdle should have fired after idleWindow")
	}
}

func TestActivityDetector_HookBurstResetsIdleTimer(t *testing.T) {
	t.Parallel()
	var count atomic.Int32
	ad := NewActivityDetector(20*time.Millisecond, 60*time.Millisecond, func() {
		count.Add(1)
	})
	defer ad.Stop()

	// Rapid hooks every 30ms for 180ms: idle timer keeps resetting.
	for i := 0; i < 6; i++ {
		ad.RecordHook()
		time.Sleep(30 * time.Millisecond)
	}
	// onIdle should have fired at most once (after last hook + idleWindow).
	time.Sleep(80 * time.Millisecond)

	n := count.Load()
	if n == 0 {
		t.Error("onIdle never fired")
	}
	// Each RecordHook stops+restarts the timer, so only the final idle should fire.
	if n > 2 {
		t.Errorf("onIdle fired %d times, want ≤2 (burst should reset timer)", n)
	}
}

func TestActivityDetector_StopPreventsIdleFire(t *testing.T) {
	t.Parallel()
	var fired atomic.Bool
	ad := NewActivityDetector(10*time.Millisecond, 30*time.Millisecond, func() {
		fired.Store(true)
	})
	ad.RecordHook()
	ad.Stop() // cancel before idle fires
	time.Sleep(50 * time.Millisecond)
	if fired.Load() {
		t.Error("Stop should prevent onIdle from firing")
	}
}

func TestActivityDetector_NilOnIdle_NoHook(t *testing.T) {
	t.Parallel()
	ad := NewActivityDetector(20*time.Millisecond, 50*time.Millisecond, nil)
	defer ad.Stop()
	ad.RecordHook() // must not panic with nil onIdle
	time.Sleep(30 * time.Millisecond)
}
