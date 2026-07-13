package sessionhost

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
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

	// Terminal status is published only after readLoop has drained the PTY.
	// The timeout is a deadlock ceiling, not a latency requirement.
	require.Eventually(t, func() bool {
		return s.Status() == StatusExited
	}, 30*time.Second, 20*time.Millisecond)
	snap, _ := s.scrollback.Snapshot()
	require.Contains(t, string(snap), "hello-sessionhost")
}

func TestCreate_LeaderExitIsTerminalWhenDescendantRetainsPTY(t *testing.T) {
	s := mustCreate(t, CreateConfig{
		Command: os.Args[0],
		Args:    []string{"-test.run=TestSessionhostLeaderHelper"},
		Env:     map[string]string{"AGNT_SESSIONHOST_LEADER_HELPER": "1"},
	})

	// The descendant inherits the slave and would keep readLoop blocked long
	// after the shell exits. The drain grace must preserve the shell's final
	// bytes, then bound terminal publication rather than waiting for sleep.
	require.Eventually(t, func() bool {
		return s.Status() == StatusExited
	}, 5*time.Second, 20*time.Millisecond)
	snap, _ := s.scrollback.Snapshot()
	require.Contains(t, string(snap), "leader-final")
}

func TestSessionhostLeaderHelper(t *testing.T) {
	if os.Getenv("AGNT_SESSIONHOST_LEADER_HELPER") != "1" {
		return
	}
	slavePath, err := filepath.EvalSymlinks("/dev/stdout")
	if err != nil {
		os.Exit(2)
	}
	cmd := exec.Command("setsid", "sh", "-c", `exec 3<>"$1"; trap '' HUP; sleep 3`, "sh", slavePath)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		os.Exit(2)
	}
	time.Sleep(50 * time.Millisecond)
	_, _ = os.Stdout.WriteString("leader-final")
	os.Exit(0)
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

func TestDetachReattach_ReplaysBoundaryThenDeliversLiveAndExit(t *testing.T) {
	s := mustCreate(t, CreateConfig{
		Command: "sh",
		Args:    []string{"-c", `printf 'before-detach\n'; read first; printf 'while-detached:%s\n' "$first"; read second; printf 'after-attach:%s\n' "$second"`},
	})

	first, firstID, primary := s.Attach(16)
	if !primary {
		t.Fatal("first attach should be primary")
	}
	waitForStdoutContains(t, first, "before-detach", 15*time.Second)
	s.Detach(firstID)

	if s.IsPrimary(firstID) {
		t.Fatal("detached client must not retain the primary write slot")
	}
	if got := s.AttachedCount(); got != 0 {
		t.Fatalf("attached count after detach = %d, want zero", got)
	}
	if err := s.WriteStdin([]byte("detached-period\n")); err != nil {
		t.Fatalf("WriteStdin(detached period): %v", err)
	}
	waitForScrollbackContains(t, s, "while-detached:detached-period", 15*time.Second)

	second, secondID, primary := s.Attach(16)
	if !primary {
		t.Fatal("reattach should reclaim the primary slot")
	}
	defer s.Detach(secondID)

	// The first frame and snapshot prove output produced with no subscribers is
	// replayed before any output generated after reattachment.
	replayed := readReplayThrough(t, second, "while-detached:detached-period", 15*time.Second)
	if err := s.WriteStdin([]byte("live-period\n")); err != nil {
		t.Fatalf("WriteStdin(second): %v", err)
	}

	var live strings.Builder
	exitSeen := false
	deadline := time.After(15 * time.Second)
	for !exitSeen {
		select {
		case raw, ok := <-second:
			if !ok {
				t.Fatalf("attach stream closed without exit frame; stdout=%q", live.String())
			}
			var f Frame
			if err := json.Unmarshal(raw, &f); err != nil {
				t.Fatalf("unmarshal frame: %v", err)
			}
			if f.Type == "stdout" {
				live.WriteString(decodeStdout(t, raw))
			}
			if f.Type == "exit" {
				exitSeen = true
			}
		case <-deadline:
			t.Fatalf("timed out waiting for live output and exit; stdout=%q", live.String())
		}
	}
	if got := live.String(); !strings.Contains(got, "after-attach:live-period") {
		t.Fatalf("missing live output before exit: %q", got)
	}
	all := replayed + live.String()
	if got := strings.Count(all, "while-detached:detached-period"); got != 1 {
		t.Fatalf("detached-period output count = %d, want exactly once; stream=%q", got, all)
	}
	if got := strings.Count(all, "after-attach:live-period"); got != 1 {
		t.Fatalf("live-period output count = %d, want exactly once; stream=%q", got, all)
	}
	if strings.Index(all, "while-detached:detached-period") > strings.Index(all, "after-attach:live-period") {
		t.Fatalf("detached replay followed live output; stream=%q", all)
	}
}

func waitForScrollbackContains(t *testing.T, s *Session, want string, timeout time.Duration) {
	t.Helper()
	deadline := time.After(timeout)
	for {
		snapshot, _ := s.scrollback.Snapshot()
		if strings.Contains(string(snapshot), want) {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for detached output %q; scrollback=%q", want, snapshot)
		case <-time.After(20 * time.Millisecond):
		}
	}
}

func readReplayThrough(t *testing.T, ch <-chan []byte, want string, timeout time.Duration) string {
	t.Helper()
	deadline := time.After(timeout)
	var stdout strings.Builder
	first := true
	for {
		select {
		case raw, ok := <-ch:
			if !ok {
				t.Fatalf("attach stream closed during replay; stdout=%q", stdout.String())
			}
			var f Frame
			if err := json.Unmarshal(raw, &f); err != nil {
				t.Fatalf("unmarshal replay frame: %v", err)
			}
			if first && f.Type != "replay-marker" {
				t.Fatalf("first reattach frame = %q, want replay-marker", f.Type)
			}
			first = false
			if f.Type == "stdout" {
				stdout.WriteString(decodeStdout(t, raw))
			}
			if strings.Contains(stdout.String(), want) {
				return stdout.String()
			}
		case <-deadline:
			t.Fatalf("timed out waiting for replay %q; stdout=%q", want, stdout.String())
		}
	}
}

func waitForStdoutContains(t *testing.T, ch <-chan []byte, want string, timeout time.Duration) {
	t.Helper()
	var got strings.Builder
	deadline := time.After(timeout)
	for {
		select {
		case raw, ok := <-ch:
			if !ok {
				t.Fatalf("attach stream closed waiting for %q; stdout=%q", want, got.String())
			}
			got.WriteString(decodeStdout(t, raw))
			if strings.Contains(got.String(), want) {
				return
			}
		case <-deadline:
			t.Fatalf("timed out waiting for %q; stdout=%q", want, got.String())
		}
	}
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

	// Closing the PTY unblocks readLoop, the child sees EOF and exits, and
	// waitLoop reaps it. The deadline is a deadlock guard, not a budget: three
	// seconds is well inside the time a loaded machine needs to schedule that
	// chain, which is what made this flake.
	select {
	case <-s.Done():
	case <-time.After(30 * time.Second):
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
