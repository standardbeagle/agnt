//go:build unix

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestRunExitsAfterCtrlCEndsTheAgent is the end-to-end regression for `agnt
// run` staying up after the user pressed Ctrl+C and the agent quit. The
// terminal is in raw mode, so Ctrl+C reaches agnt as the byte 0x03; agnt
// forwards it to the child's PTY, whose line discipline turns it into SIGINT
// for the agent. Once the agent is gone, agnt must exit by itself.
//
// The agent here also leaves a job behind that ignores SIGINT and SIGHUP, as
// a coding agent's MCP servers and `npm run dev &` jobs do.
//
// Spawns the real binary under a real PTY, so no t.Parallel().
func TestRunExitsAfterCtrlCEndsTheAgent(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns the agnt binary")
	}
	bin := findAgntBinary(t)

	for _, tc := range []struct {
		name string
		args []string
	}{
		{"overlay", nil},
		{"no-overlay", []string{"--no-overlay"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			projectDir := t.TempDir()
			if err := os.WriteFile(filepath.Join(projectDir, ".agnt.kdl"), []byte("// test project\n"), 0o644); err != nil {
				t.Fatalf("write config: %v", err)
			}
			master, slave := openRunTestPTY(t)

			const ready = "agnt-ctrlc-agent-ready"
			// The job inherits the ignores from before the fork (a trap set
			// inside it races the agent's exit); the agent then re-arms INT.
			script := `trap "" INT HUP; sleep 600 & echo "job=$!"; trap 'exit 130' INT; echo ` + ready + `; while :; do sleep 0.2; done`
			args := append([]string{"run"}, tc.args...)
			args = append(args, "--no-autostart", "--session", "ctrlc-"+tc.name, "sh", "-c", script)
			cmd := exec.Command(bin, args...)
			cmd.Dir = projectDir
			cmd.Env = append(os.Environ(), "XDG_STATE_HOME="+t.TempDir(), "TERM=xterm")
			cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
			if err := cmd.Start(); err != nil {
				t.Fatalf("start agnt run: %v", err)
			}
			_ = slave.Close()

			var mu sync.Mutex
			var output strings.Builder
			sawReady := make(chan struct{})
			var once sync.Once
			go func() {
				buf := make([]byte, 4096)
				for {
					n, err := master.Read(buf)
					if n > 0 {
						mu.Lock()
						output.Write(buf[:n])
						if strings.Contains(output.String(), ready) {
							once.Do(func() { close(sawReady) })
						}
						mu.Unlock()
					}
					if err != nil {
						return
					}
				}
			}()
			outputSoFar := func() string {
				mu.Lock()
				defer mu.Unlock()
				return output.String()
			}

			exited := make(chan error, 1)
			go func() { exited <- cmd.Wait() }()
			t.Cleanup(func() {
				if cmd.ProcessState == nil {
					_ = cmd.Process.Kill()
				}
			})

			select {
			case <-sawReady:
			case err := <-exited:
				t.Fatalf("agnt run exited before the agent was ready: %v\noutput:\n%s", err, outputSoFar())
			case <-time.After(60 * time.Second):
				t.Fatalf("agent never printed %q\noutput:\n%s", ready, outputSoFar())
			}

			if _, err := master.Write([]byte{0x03}); err != nil {
				t.Fatalf("write Ctrl+C: %v", err)
			}
			pressed := time.Now()

			select {
			case <-exited:
				t.Logf("agnt run exited %s after Ctrl+C", time.Since(pressed).Round(time.Millisecond))
			case <-time.After(30 * time.Second):
				tree, _ := exec.Command("ps", "-eo", "pid,ppid,pgid,sid,stat,wchan:24,args").CombinedOutput()
				// SIGQUIT makes the Go runtime print every goroutine's stack to
				// stderr (the PTY, so it lands in the output) and exit.
				_ = syscall.Kill(cmd.Process.Pid, syscall.SIGQUIT)
				select {
				case <-exited:
				case <-time.After(5 * time.Second):
					_ = cmd.Process.Kill()
					<-exited
				}
				time.Sleep(200 * time.Millisecond) // let the reader drain the dump
				t.Fatalf("agnt run did not exit within 30s of Ctrl+C ending the agent\nagnt pid %d\nprocesses:\n%s\noutput:\n%s",
					cmd.Process.Pid, tree, outputSoFar())
			}

			job := jobPIDFromOutput(t, outputSoFar())
			require.Eventually(t, func() bool { return !pidAlive(job) }, 10*time.Second, 50*time.Millisecond,
				"the agent's leftover job %d outlived the session", job)
		})
	}
}

// jobPIDFromOutput reads the "job=<pid>" line the test agent prints.
func jobPIDFromOutput(t *testing.T, output string) int {
	t.Helper()
	m := regexp.MustCompile(`job=(\d+)`).FindStringSubmatch(output)
	require.NotNil(t, m, "agent output has no job pid:\n%s", output)
	pid, err := strconv.Atoi(m[1])
	require.NoError(t, err)
	return pid
}

// pidAlive reports whether pid exists and is not a zombie.
func pidAlive(pid int) bool {
	stat, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return syscall.Kill(pid, 0) == nil
	}
	// The state field follows the parenthesised command name.
	s := string(stat)
	i := strings.LastIndexByte(s, ')')
	return i < 0 || i+2 >= len(s) || s[i+2] != 'Z'
}
