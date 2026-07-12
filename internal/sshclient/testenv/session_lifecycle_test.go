package testenv_test

import (
	"errors"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/standardbeagle/agnt/internal/sessionhost"
	"github.com/stretchr/testify/require"
)

func TestSessionLifecycleCreateDetachStealKill(t *testing.T) {
	s, err := sessionhost.Create(sessionhost.CreateConfig{
		Name: "lifecycle", ProjectPath: t.TempDir(), Command: "/bin/sh", Args: []string{"-c", "cat"},
	})
	require.NoError(t, err)
	t.Cleanup(func() { killSession(t, s) })

	registry := sessionhost.NewRegistry()
	registry.Add(s)
	require.Equal(t, 1, registry.Count())
	require.Equal(t, sessionhost.StatusRunning, s.Status())
	require.Greater(t, s.SessionPGID, 0)
	require.Equal(t, -1, s.ExitCode())
	require.Empty(t, s.LastAttached())

	first, firstID, firstPrimary := s.Attach(8)
	require.True(t, firstPrimary)
	require.True(t, s.IsPrimary(firstID))
	require.Equal(t, 1, s.AttachedCount())
	require.NotEmpty(t, <-first)
	require.False(t, s.LastAttached().IsZero())

	_, observerID, observerPrimary := s.Attach(8)
	require.False(t, observerPrimary)
	require.False(t, s.IsPrimary(observerID))
	require.Equal(t, 2, s.AttachedCount())

	s.Detach(firstID)
	require.Equal(t, 1, s.AttachedCount())
	require.False(t, s.IsPrimary(firstID))
	_, replacementID, replacementPrimary := s.Attach(8)
	require.True(t, replacementPrimary, "new attach steals the vacant primary slot")
	require.True(t, s.IsPrimary(replacementID))
	require.False(t, s.IsPrimary(observerID))

	require.NoError(t, syscall.Kill(-s.SessionPGID, syscall.SIGKILL))
	require.Eventually(t, func() bool { return s.Status() == sessionhost.StatusExited }, 3*time.Second, 10*time.Millisecond)
	select {
	case <-s.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("killed session did not close Done")
	}
	require.NotEqual(t, 0, s.ExitCode())
	registry.Remove(s.ID)
	require.Equal(t, 0, registry.Count())
	_, found := registry.Get(s.ID)
	require.False(t, found)
}

func killSession(t *testing.T, s *sessionhost.Session) {
	t.Helper()
	if s.Status() == sessionhost.StatusRunning && s.SessionPGID > 0 {
		err := syscall.Kill(-s.SessionPGID, syscall.SIGKILL)
		if err != nil && !errors.Is(err, os.ErrProcessDone) && !errors.Is(err, syscall.ESRCH) {
			t.Errorf("kill session pgid %d: %v", s.SessionPGID, err)
		}
	}
	select {
	case <-s.Done():
	case <-time.After(3 * time.Second):
		t.Errorf("session %s did not exit", s.ID)
	}
}
