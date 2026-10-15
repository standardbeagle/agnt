//go:build unix

package main

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/creack/pty"
)

// TestRunNoOverlayExitsWithoutKeypress is the end-to-end regression for `agnt
// run --no-overlay` hanging after the agent exits. Teardown used to join the
// stdin copy goroutine, which was parked in os.Stdin.Read and only returned
// when the user pressed a key. The child here exits immediately and the test
// never writes to the terminal, so the only way agnt can exit is on its own.
//
// Spawns the real binary under a real PTY, so no t.Parallel().
func TestRunNoOverlayExitsWithoutKeypress(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns the agnt binary")
	}
	bin := findAgntBinary(t)

	// A config in the project dir skips the first-run setup gate, so the child
	// is launched exactly once as the coding phase.
	projectDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(projectDir, ".agnt.kdl"), []byte("// test project\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	master, slave := openRunTestPTY(t)

	const marker = "agnt-no-overlay-child-ran"
	cmd := exec.Command(bin, "run", "--no-overlay", "--no-autostart", "--session", "nooverlay-test",
		"sh", "-c", "echo "+marker)
	cmd.Dir = projectDir
	cmd.Env = append(os.Environ(),
		"XDG_STATE_HOME="+t.TempDir(), // first-run markers stay out of the real state dir
		"TERM=xterm",
	)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
	if err := cmd.Start(); err != nil {
		t.Fatalf("start agnt run: %v", err)
	}
	_ = slave.Close()

	// Drain the terminal so agnt never blocks on output; keep what it wrote.
	var mu sync.Mutex
	var output strings.Builder
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		buf := make([]byte, 4096)
		for {
			n, err := master.Read(buf)
			if n > 0 {
				mu.Lock()
				output.Write(buf[:n])
				mu.Unlock()
			}
			if err != nil {
				return
			}
		}
	}()

	started := time.Now()
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()

	select {
	case <-exited:
		t.Logf("agnt run exited %s after launch with no input", time.Since(started).Round(time.Millisecond))
	case <-time.After(90 * time.Second):
		_ = cmd.Process.Kill()
		<-exited
		mu.Lock()
		got := output.String()
		mu.Unlock()
		t.Fatalf("agnt run --no-overlay did not exit after its child exited (no keypress was sent)\noutput:\n%s", got)
	}

	_ = master.Close()
	<-drained
	mu.Lock()
	got := output.String()
	mu.Unlock()
	if !strings.Contains(got, marker) {
		t.Fatalf("child output %q not relayed; got:\n%s", marker, got)
	}
}

// TestRunExitsWithChildStatus is the end-to-end regression for `agnt run`
// reporting success whatever its child did: the wrapped command's exit status
// was dropped and agnt always exited 0.
//
// Spawns the real binary under a real PTY, so no t.Parallel().
func TestRunExitsWithChildStatus(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns the agnt binary")
	}
	bin := findAgntBinary(t)

	projectDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(projectDir, ".agnt.kdl"), []byte("// test project\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	master, slave := openRunTestPTY(t)

	cmd := exec.Command(bin, "run", "--no-overlay", "--no-autostart", "--session", "exitcode-test",
		"sh", "-c", "exit 7")
	cmd.Dir = projectDir
	cmd.Env = append(os.Environ(), "XDG_STATE_HOME="+t.TempDir(), "TERM=xterm")
	cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
	if err := cmd.Start(); err != nil {
		t.Fatalf("start agnt run: %v", err)
	}
	_ = slave.Close()
	go func() { _, _ = io.Copy(io.Discard, master) }()

	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	var err error
	select {
	case err = <-exited:
	case <-time.After(90 * time.Second):
		_ = cmd.Process.Kill()
		<-exited
		t.Fatal("agnt run did not exit after its child exited")
	}
	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.ExitCode() != 7 {
		t.Fatalf("agnt run exit = %v, want status 7 (the child's)", err)
	}
}

// openRunTestPTY opens a PTY pair closed at test cleanup.
func openRunTestPTY(t *testing.T) (master, slave *os.File) {
	t.Helper()
	master, slave, err := pty.Open()
	if err != nil {
		t.Skipf("cannot open a PTY: %v", err)
	}
	t.Cleanup(func() {
		_ = master.Close()
		_ = slave.Close()
	})
	return master, slave
}
