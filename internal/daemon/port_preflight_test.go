//go:build unix

package daemon

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"

	"github.com/standardbeagle/agnt/internal/config"
	goprocess "github.com/standardbeagle/go-cli-server/process"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDetectPortConflicts_NoConflicts(t *testing.T) {
	t.Parallel()
	scripts := map[string]*config.ScriptConfig{
		"api": {Ports: []int{19876}, Autostart: true},
	}
	conflicts := detectPortConflicts(context.Background(), scripts, nil)
	assert.Empty(t, conflicts)
}

func TestDetectPortConflicts_WithBlocker(t *testing.T) {
	t.Parallel()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	scripts := map[string]*config.ScriptConfig{
		"api": {Ports: []int{port}, Autostart: true},
	}
	conflicts := detectPortConflicts(context.Background(), scripts, nil)
	require.Len(t, conflicts, 1)
	assert.Equal(t, "api", conflicts[0].ScriptName)
	assert.Equal(t, port, conflicts[0].Port)
	assert.Contains(t, conflicts[0].PIDs, os.Getpid())
}

func TestDetectPortConflicts_ManagedPIDSkipped(t *testing.T) {
	t.Parallel()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	managedPIDs := map[int]bool{os.Getpid(): true}
	scripts := map[string]*config.ScriptConfig{
		"api": {Ports: []int{port}, Autostart: true},
	}
	conflicts := detectPortConflicts(context.Background(), scripts, managedPIDs)
	assert.Empty(t, conflicts, "should skip managed PIDs")
}

func TestDetectPortConflicts_NoPorts(t *testing.T) {
	t.Parallel()
	scripts := map[string]*config.ScriptConfig{
		"lib": {Autostart: true},
	}
	conflicts := detectPortConflicts(context.Background(), scripts, nil)
	assert.Empty(t, conflicts)
}

func TestDetectPortConflicts_MultiplePortsMultipleScripts(t *testing.T) {
	t.Parallel()
	ln1, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln1.Close()
	ln2, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer ln2.Close()

	scripts := map[string]*config.ScriptConfig{
		"api":      {Ports: []int{ln1.Addr().(*net.TCPAddr).Port}, Autostart: true},
		"frontend": {Ports: []int{ln2.Addr().(*net.TCPAddr).Port}, Autostart: true},
	}
	conflicts := detectPortConflicts(context.Background(), scripts, nil)
	assert.Len(t, conflicts, 2)
}

func TestKillPortBlockers_FreesPort(t *testing.T) {
	t.Parallel()
	if os.Getuid() == 0 {
		t.Skip("don't run as root")
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()

	// Start a subprocess that listens on the port
	cmd := exec.Command("python3", "-c",
		fmt.Sprintf(`import socket,time; s=socket.socket(); s.setsockopt(socket.SOL_SOCKET,socket.SO_REUSEADDR,1); s.bind(('127.0.0.1',%d)); s.listen(1); time.sleep(60)`, port))
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	require.NoError(t, cmd.Start())
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()

	// Wait for port to be bound
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 100*time.Millisecond); err == nil {
			conn.Close()
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	pm := goprocess.NewProcessManager(goprocess.DefaultManagerConfig())
	defer pm.Shutdown(context.Background())

	conflicts := []PortConflict{{
		ScriptName: "test", Port: port, PIDs: []int{cmd.Process.Pid},
	}}

	results := killPortBlockers(context.Background(), pm, conflicts)
	require.Len(t, results, 1)
	assert.True(t, results[0].Killed)
	assert.Empty(t, results[0].Error)

	// Verify port is free
	ln2, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	assert.NoError(t, err, "port should be free after kill")
	if ln2 != nil {
		ln2.Close()
	}
}

func TestKillPortBlockers_NonExistentPID(t *testing.T) {
	t.Parallel()
	pm := goprocess.NewProcessManager(goprocess.DefaultManagerConfig())
	defer pm.Shutdown(context.Background())

	conflicts := []PortConflict{{
		ScriptName: "test", Port: ephemeralPort(t), PIDs: []int{1 << 29}, // non-existent PID
	}}
	results := killPortBlockers(context.Background(), pm, conflicts)
	require.Len(t, results, 1)
	assert.True(t, results[0].Killed)
}
