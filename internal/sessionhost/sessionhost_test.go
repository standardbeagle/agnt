package sessionhost

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"go.uber.org/goleak"
)

func mustCreate(t *testing.T, cfg CreateConfig) *Session {
	t.Helper()
	if cfg.Command == "" {
		cfg.Command = "sh"
		cfg.Args = []string{"-c", "cat"}
	}
	s, err := Create(cfg)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	t.Cleanup(func() {
		_ = s.Close()
		select {
		case <-s.Done():
		case <-time.After(2 * time.Second):
		}
	})
	return s
}

func decodeStdout(t *testing.T, raw []byte) string {
	t.Helper()
	var f Frame
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatalf("unmarshal frame: %v", err)
	}
	if f.Type != "stdout" {
		return ""
	}
	var b64 string
	if err := json.Unmarshal(f.Data, &b64); err != nil {
		t.Fatalf("unmarshal stdout data: %v", err)
	}
	dec, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		t.Fatalf("base64 decode: %v", err)
	}
	return string(dec)
}

func TestCreate_SpawnsAndCapturesOutput(t *testing.T) {
	s := mustCreate(t, CreateConfig{Command: "sh", Args: []string{"-c", "echo hello-sessionhost"}})

	if s.SessionPGID <= 0 {
		t.Fatalf("expected positive SessionPGID, got %d", s.SessionPGID)
	}

	deadline := time.After(3 * time.Second)
	for {
		snap, _ := s.scrollback.Snapshot()
		if strings.Contains(string(snap), "hello-sessionhost") {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for output; got %q", string(snap))
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func TestAttach_ReplaysScrollbackThenLive(t *testing.T) {
	s := mustCreate(t, CreateConfig{Command: "sh", Args: []string{"-c", "printf preexisting; sleep 5"}})

	// Give the child a moment to print before attaching, so we exercise the
	// "replay what already happened" path (spec §1.4 invariant 6).
	time.Sleep(200 * time.Millisecond)

	ch, attachID, isPrimary := s.Attach(16)
	if !isPrimary {
		t.Fatalf("first attach should be primary")
	}
	if attachID == "" {
		t.Fatalf("expected non-empty attach id")
	}

	// First frame must be replay-marker.
	first := <-ch
	var f Frame
	if err := json.Unmarshal(first, &f); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if f.Type != "replay-marker" {
		t.Fatalf("expected replay-marker first, got %s", f.Type)
	}

	// Next frame(s) must replay the pre-existing scrollback.
	var replayed strings.Builder
	deadline := time.After(2 * time.Second)
	found := false
	for !found {
		select {
		case raw := <-ch:
			replayed.WriteString(decodeStdout(t, raw))
			if strings.Contains(replayed.String(), "preexisting") {
				found = true
			}
		case <-deadline:
			t.Fatalf("timed out waiting for replay; got %q", replayed.String())
		}
	}

	s.Detach(attachID)
}

func TestAttach_SecondAttachIsNotPrimary(t *testing.T) {
	s := mustCreate(t, CreateConfig{})

	_, id1, primary1 := s.Attach(8)
	_, id2, primary2 := s.Attach(8)

	if !primary1 {
		t.Fatalf("first attach must be primary")
	}
	if primary2 {
		t.Fatalf("second attach must not be primary")
	}
	if !s.IsPrimary(id1) {
		t.Fatalf("id1 should hold primary slot")
	}
	if s.IsPrimary(id2) {
		t.Fatalf("id2 should not hold primary slot")
	}
}

func TestDetach_DoesNotKillPTY(t *testing.T) {
	s := mustCreate(t, CreateConfig{})

	_, id, _ := s.Attach(8)
	s.Detach(id)

	select {
	case <-s.Done():
		t.Fatalf("session should still be running after detach")
	case <-time.After(200 * time.Millisecond):
	}
	if s.Status() != StatusRunning {
		t.Fatalf("expected StatusRunning after detach, got %s", s.Status())
	}
}

func TestDetach_PrimarySlotReclaimable(t *testing.T) {
	s := mustCreate(t, CreateConfig{})

	_, id1, primary1 := s.Attach(8)
	if !primary1 {
		t.Fatalf("id1 should be primary")
	}
	s.Detach(id1)

	_, id2, primary2 := s.Attach(8)
	if !primary2 {
		t.Fatalf("id2 should claim primary slot after id1 detached")
	}
	if !s.IsPrimary(id2) {
		t.Fatalf("id2 should hold primary slot")
	}
}

func TestClose_MarksExitedAndClosesDone(t *testing.T) {
	s := mustCreate(t, CreateConfig{})

	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	select {
	case <-s.Done():
	case <-time.After(3 * time.Second):
		t.Fatalf("expected Done() to close after PTY close")
	}
	if s.Status() != StatusExited {
		t.Fatalf("expected StatusExited, got %s", s.Status())
	}
}

func TestRegistry_AddGetListRemove(t *testing.T) {
	r := NewRegistry()
	s1 := mustCreate(t, CreateConfig{})
	s1.ProjectPath = "/proj/a"
	s2 := mustCreate(t, CreateConfig{})
	s2.ProjectPath = "/proj/b"

	r.Add(s1)
	r.Add(s2)

	if got, ok := r.Get(s1.ID); !ok || got != s1 {
		t.Fatalf("expected to get back s1")
	}

	all := r.List("", true)
	if len(all) != 2 {
		t.Fatalf("expected 2 sessions global, got %d", len(all))
	}
	scopedA := r.List("/proj/a", false)
	if len(scopedA) != 1 || scopedA[0] != s1 {
		t.Fatalf("expected only s1 scoped to /proj/a, got %v", scopedA)
	}

	r.Remove(s1.ID)
	if _, ok := r.Get(s1.ID); ok {
		t.Fatalf("expected s1 removed")
	}
	if r.Count() != 1 {
		t.Fatalf("expected 1 remaining, got %d", r.Count())
	}
}

// TestAttachDetachChurn_Race exercises concurrent attach/detach cycles
// against a single live session under `-race`, mirroring the acceptance
// criterion for this task.
func TestAttachDetachChurn_Race(t *testing.T) {
	s := mustCreate(t, CreateConfig{Command: "sh", Args: []string{"-c", "while true; do echo tick; sleep 0.01; done"}})

	// Enforce the acceptance criterion: no goroutine leaks from the
	// attach/detach fan-out. IgnoreCurrent baselines the session's own
	// long-lived readLoop/waitLoop (alive here because mustCreate ran first);
	// the deferred VerifyNone then fails only if an Attach/Detach cycle leaks
	// a goroutine. Runs before mustCreate's t.Cleanup tears the session down.
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 25; j++ {
				ch, id, _ := s.Attach(4)
				// Drain a frame or two without blocking forever.
				select {
				case <-ch:
				case <-time.After(50 * time.Millisecond):
				}
				s.Detach(id)
			}
		}()
	}
	wg.Wait()
}

func TestResize_NoErrorWhileRunning(t *testing.T) {
	s := mustCreate(t, CreateConfig{})
	if err := s.Resize(100, 40); err != nil {
		t.Fatalf("Resize: %v", err)
	}
}
