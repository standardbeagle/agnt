package main

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"
)

// fakeDaemon scripts the seams of daemonLifecycle. aliveFor is how long the
// socket keeps accepting after SHUTDOWN, on a fake clock that only advances
// when the lifecycle sleeps — so a "never exits" daemon costs no wall time.
type fakeDaemon struct {
	connectable    bool
	shutdownErr    error
	aliveFor       time.Duration // < 0: never exits
	clock          time.Time
	shutdownAt     time.Time
	shutdownSent   bool
	startCalls     int
	runningAtStart bool
}

func (f *fakeDaemon) running() bool {
	if !f.connectable {
		return false
	}
	if !f.shutdownSent || f.aliveFor < 0 {
		return true
	}
	return f.clock.Sub(f.shutdownAt) < f.aliveFor
}

func (f *fakeDaemon) lifecycle() daemonLifecycle {
	f.clock = time.Unix(1_000_000, 0)
	return daemonLifecycle{
		shutdown: func(string) (bool, error) {
			if !f.connectable {
				return false, errors.New("socket not found")
			}
			f.shutdownSent = true
			f.shutdownAt = f.clock
			return true, f.shutdownErr
		},
		isRunning: func(string) bool { return f.running() },
		startDetached: func(string) error {
			f.startCalls++
			f.runningAtStart = f.running()
			return nil
		},
		exitTimeout:  5 * time.Second,
		pollInterval: 100 * time.Millisecond,
		now:          func() time.Time { return f.clock },
		sleep:        func(d time.Duration) { f.clock = f.clock.Add(d) },
	}
}

func TestDaemonStop_ExitsCleanly(t *testing.T) {
	f := &fakeDaemon{connectable: true, aliveFor: 300 * time.Millisecond}
	var out, errb bytes.Buffer
	if code := f.lifecycle().stop("sock", &out, &errb); code != 0 {
		t.Fatalf("stop exit = %d, want 0 (stderr %q)", code, errb.String())
	}
	if !strings.Contains(out.String(), "Daemon stopped") {
		t.Errorf("stdout = %q, want \"Daemon stopped\"", out.String())
	}
}

// A daemon that still accepts on its socket after the wait did not stop:
// stop must exit non-zero and must not claim "Daemon stopped".
func TestDaemonStop_StillRunningAfterWait_Fails(t *testing.T) {
	f := &fakeDaemon{connectable: true, aliveFor: -1}
	var out, errb bytes.Buffer
	code := f.lifecycle().stop("sock", &out, &errb)
	if code == 0 {
		t.Errorf("stop exit = 0, want non-zero when the daemon is still running")
	}
	if strings.Contains(out.String(), "Daemon stopped") {
		t.Errorf("stdout = %q; must not claim the daemon stopped", out.String())
	}
	if !strings.Contains(errb.String(), "still running") {
		t.Errorf("stderr = %q, want it to say the daemon is still running", errb.String())
	}
}

func TestDaemonStop_NotRunning_Fails(t *testing.T) {
	f := &fakeDaemon{connectable: false}
	var out, errb bytes.Buffer
	if code := f.lifecycle().stop("sock", &out, &errb); code == 0 {
		t.Errorf("stop exit = 0, want non-zero when no daemon is running")
	}
}

func TestDaemonRestart_WaitsForOldDaemonThenStarts(t *testing.T) {
	f := &fakeDaemon{connectable: true, aliveFor: 2 * time.Second}
	var out, errb bytes.Buffer
	if code := f.lifecycle().restart("sock", &out, &errb); code != 0 {
		t.Fatalf("restart exit = %d, want 0 (stderr %q)", code, errb.String())
	}
	if f.startCalls != 1 || f.runningAtStart {
		t.Errorf("startCalls = %d, oldRunningAtStart = %v; want one start after the old daemon is gone",
			f.startCalls, f.runningAtStart)
	}
	if !strings.Contains(out.String(), "Daemon restarted") {
		t.Errorf("stdout = %q, want \"Daemon restarted\"", out.String())
	}
}

// If the old daemon is still accepting after the wait, restart must not start
// (the auto-start client would just connect to the dying daemon) and must fail.
func TestDaemonRestart_OldDaemonStillRunning_FailsWithoutStarting(t *testing.T) {
	f := &fakeDaemon{connectable: true, aliveFor: -1}
	var out, errb bytes.Buffer
	code := f.lifecycle().restart("sock", &out, &errb)
	if code == 0 {
		t.Errorf("restart exit = 0, want non-zero when the old daemon is still running")
	}
	if f.startCalls != 0 {
		t.Errorf("startDetached called %d times; must not start/connect while the old daemon is alive", f.startCalls)
	}
	if strings.Contains(out.String(), "Daemon restarted") {
		t.Errorf("stdout = %q; must not claim the daemon restarted", out.String())
	}
}

func TestDaemonRestart_NoDaemon_Starts(t *testing.T) {
	f := &fakeDaemon{connectable: false}
	var out, errb bytes.Buffer
	if code := f.lifecycle().restart("sock", &out, &errb); code != 0 {
		t.Fatalf("restart exit = %d, want 0 (stderr %q)", code, errb.String())
	}
	if f.startCalls != 1 {
		t.Errorf("startCalls = %d, want 1", f.startCalls)
	}
}

// A socket that answers but refused our connect is not "gone": restart must not
// start onto it.
func TestDaemonRestart_UnreachableButAnswering_FailsWithoutStarting(t *testing.T) {
	f := &fakeDaemon{connectable: true, aliveFor: -1}
	l := f.lifecycle()
	l.shutdown = func(string) (bool, error) { return false, errors.New("connect timeout") }
	var out, errb bytes.Buffer
	if code := l.restart("sock", &out, &errb); code == 0 {
		t.Errorf("restart exit = 0, want non-zero")
	}
	if f.startCalls != 0 {
		t.Errorf("startDetached called %d times, want 0", f.startCalls)
	}
}
