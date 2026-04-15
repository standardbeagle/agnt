package main

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestDispatchNotifyHook_DaemonDownReturnsZero asserts that
// dispatchNotifyHook returns exit code 0 when the daemon is not
// reachable. This is the contract for the notify alias: hook scripts
// must never fail loudly, even when the daemon is wedged or stopped.
func TestDispatchNotifyHook_DaemonDownReturnsZero(t *testing.T) {
	// Point at a socket path that cannot exist. Connect() will fail
	// immediately, and the function must swallow the error and return 0.
	bogusSocket := filepath.Join(t.TempDir(), "nonexistent.sock")

	rc := dispatchNotifyHook(bogusSocket, "info", "Title", "Message")
	assert.Equal(t, 0, rc, "daemon-down case must return exit 0")
}

// TestDispatchNotifyHook_HappyExitZero asserts the signature and the
// default return path: with any input, the function never returns a
// non-zero exit code. The exit-1 path lives in runNotify (arg
// validation) and is exercised separately.
//
// We use an obviously-broken socket path here too, because the
// dispatched return value is the same regardless of whether the
// daemon answered or not — the contract is "fire and forget, exit 0".
func TestDispatchNotifyHook_AlwaysReturnsZero(t *testing.T) {
	rc := dispatchNotifyHook(filepath.Join(t.TempDir(), "missing.sock"), "warning", "", "hello")
	assert.Equal(t, 0, rc)
}

// TestDispatchNotifyHook_EmptyKindStillExitsZero asserts that the
// empty kind / empty title combinations are silently accepted. The
// daemon-side broadcastNotificationToast defaults `type=info` when
// the field is empty, so this is the agreed contract.
func TestDispatchNotifyHook_EmptyKindStillExitsZero(t *testing.T) {
	rc := dispatchNotifyHook(filepath.Join(t.TempDir(), "missing.sock"), "", "", "fallback")
	assert.Equal(t, 0, rc)
}
