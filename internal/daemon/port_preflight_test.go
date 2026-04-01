package daemon

import (
	"context"
	"net"
	"os"
	"testing"

	"github.com/standardbeagle/agnt/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDetectPortConflicts_NoConflicts(t *testing.T) {
	scripts := map[string]*config.ScriptConfig{
		"api": {Ports: []int{19876}, Autostart: true},
	}
	conflicts := detectPortConflicts(context.Background(), scripts, nil)
	assert.Empty(t, conflicts)
}

func TestDetectPortConflicts_WithBlocker(t *testing.T) {
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
	scripts := map[string]*config.ScriptConfig{
		"lib": {Autostart: true},
	}
	conflicts := detectPortConflicts(context.Background(), scripts, nil)
	assert.Empty(t, conflicts)
}

func TestDetectPortConflicts_MultiplePortsMultipleScripts(t *testing.T) {
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
