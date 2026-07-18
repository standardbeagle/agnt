package daemon

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunDoctor_AllChecksRun(t *testing.T) {
	t.Parallel()
	report := runDoctorWithChecks(context.Background(), []doctorCheck{
		{name: "check_a", fn: func(ctx context.Context) CheckResult {
			return CheckResult{Name: "check_a", Status: StatusOK, Message: "all good"}
		}},
		{name: "check_b", fn: func(ctx context.Context) CheckResult {
			return CheckResult{Name: "check_b", Status: StatusWarning, Message: "minor issue"}
		}},
	})

	require.Len(t, report.Checks, 2)
	assert.Equal(t, StatusWarning, report.Status)
}

func TestRunDoctor_OverallStatus_WorstWins(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		statuses []string
		want     string
	}{
		{"all ok", []string{StatusOK, StatusOK}, StatusOK},
		{"warning wins over ok", []string{StatusOK, StatusWarning}, StatusWarning},
		{"error wins over warning", []string{StatusWarning, StatusError, StatusOK}, StatusError},
		{"single error", []string{StatusError}, StatusError},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var checks []doctorCheck
			for i, s := range tt.statuses {
				s := s
				checks = append(checks, doctorCheck{
					name: tt.statuses[i],
					fn: func(ctx context.Context) CheckResult {
						return CheckResult{Name: s, Status: s, Message: "msg"}
					},
				})
			}
			report := runDoctorWithChecks(context.Background(), checks)
			assert.Equal(t, tt.want, report.Status)
		})
	}
}

func TestRunDoctor_TimeoutEnforced(t *testing.T) {
	t.Parallel()
	slow := doctorCheck{
		name: "slow_check",
		fn: func(ctx context.Context) CheckResult {
			select {
			case <-ctx.Done():
				return CheckResult{Name: "slow_check", Status: StatusError, Message: "timed out"}
			case <-time.After(30 * time.Second):
				return CheckResult{Name: "slow_check", Status: StatusOK, Message: "done"}
			}
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	report := runDoctorWithChecks(ctx, []doctorCheck{slow})
	require.Len(t, report.Checks, 1)
	assert.Equal(t, StatusError, report.Checks[0].Status)
	assert.Contains(t, report.Checks[0].Message, "timed out")
}

func TestRunDoctor_EmptyChecks(t *testing.T) {
	t.Parallel()
	report := runDoctorWithChecks(context.Background(), nil)
	assert.Equal(t, StatusOK, report.Status)
	assert.Empty(t, report.Checks)
}

func TestCheckSessionHealth_NoSessions(t *testing.T) {
	t.Parallel()
	reg := NewSessionRegistry(60 * time.Second)
	result := checkSessionHealth(context.Background(), reg)
	assert.Equal(t, StatusOK, result.Status)
}

func TestCheckSessionHealth_StaleSession(t *testing.T) {
	t.Parallel()
	reg := NewSessionRegistry(60 * time.Second)
	session := &Session{
		Code:      "test-1",
		StartedAt: time.Now().Add(-10 * time.Minute),
		LastSeen:  time.Now().Add(-2 * time.Minute),
		Status:    SessionStatusActive,
	}
	reg.sessions.Store("test-1", session)

	result := checkSessionHealth(context.Background(), reg)
	assert.Equal(t, StatusWarning, result.Status)
	assert.Contains(t, result.Message, "stale")
}

func TestCheckSessionHealth_ActiveSession(t *testing.T) {
	t.Parallel()
	reg := NewSessionRegistry(60 * time.Second)
	session := &Session{
		Code:      "test-2",
		StartedAt: time.Now().Add(-5 * time.Minute),
		LastSeen:  time.Now().Add(-5 * time.Second),
		Status:    SessionStatusActive,
	}
	reg.sessions.Store("test-2", session)

	result := checkSessionHealth(context.Background(), reg)
	assert.Equal(t, StatusOK, result.Status)
}

func TestCheckStartupErrors_NoErrors(t *testing.T) {
	t.Parallel()
	store := NewStartupLogStore(100)
	result := checkStartupErrors(context.Background(), store)
	assert.Equal(t, StatusOK, result.Status)
}

func TestCheckStartupErrors_RecentErrors(t *testing.T) {
	t.Parallel()
	store := NewStartupLogStore(100)
	store.Add(&StartupLogEntry{
		ProcessID: "proc-1",
		Level:     "error",
		EventType: "start_failed",
		Message:   "port in use",
		Timestamp: time.Now().Add(-5 * time.Minute),
	})
	store.Add(&StartupLogEntry{
		ProcessID: "proc-2",
		Level:     "info",
		EventType: "started",
		Message:   "started successfully",
		Timestamp: time.Now().Add(-3 * time.Minute),
	})

	result := checkStartupErrors(context.Background(), store)
	assert.Equal(t, StatusWarning, result.Status)
	assert.Contains(t, result.Message, "1 error")
}

func TestCheckConfigHealth_NoConfig(t *testing.T) {
	t.Parallel()
	result := checkConfigHealth(context.Background(), "/nonexistent/path/that/does/not/exist")
	assert.Equal(t, StatusOK, result.Status)
}

// TestCheckConfigHealth_ValidConfig guards the healthy path against
// regression: a well-formed .agnt.kdl with no missing script cwd dirs
// must still report StatusOK with an actionable "config valid" message.
func TestCheckConfigHealth_ValidConfig(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeAgntConfig(t, dir, `scripts {
    api {
        run "echo hi"
    }
}
`)

	result := checkConfigHealth(context.Background(), dir)
	assert.Equal(t, StatusOK, result.Status)
	assert.Equal(t, "config valid", result.Message)
}

// TestCheckConfigHealth_MalformedConfig pins the doctor contract that a
// malformed existing .agnt.kdl is surfaced as an actionable problem, never
// silently treated as "no config to check" (which is reserved for the
// genuinely-missing-file case covered by TestCheckConfigHealth_NoConfig).
// Production already returns StatusError with parse-error context here
// (internal/daemon/doctor.go:426-434) — this test is coverage-only,
// guarding that behavior against regression.
func TestCheckConfigHealth_MalformedConfig(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// Unterminated block — invalid KDL syntax.
	writeAgntConfig(t, dir, `scripts {
    api {
        run "echo hi"
`)

	result := checkConfigHealth(context.Background(), dir)
	require.Equal(t, StatusError, result.Status, "malformed config must never be reported as healthy")
	assert.NotEqual(t, "no config to check", result.Message)
	assert.NotEqual(t, "no .agnt.kdl found", result.Message)
	assert.Contains(t, result.Message, "config parse error")
	assert.NotEmpty(t, result.Fix, "malformed config must include actionable fix guidance")
}

// TestRunDoctor_MalformedConfig_PropagatesToOverallStatus pins that a
// malformed .agnt.kdl surfaces all the way up to a non-OK DoctorReport,
// not just a non-OK individual check result.
func TestRunDoctor_MalformedConfig_PropagatesToOverallStatus(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeAgntConfig(t, dir, `scripts {
    api {
        run "echo hi"
`)

	checks := []doctorCheck{
		{name: "config_health", fn: func(ctx context.Context) CheckResult {
			return checkConfigHealth(ctx, dir)
		}},
	}
	report := runDoctorWithChecks(context.Background(), checks)

	require.Len(t, report.Checks, 1)
	assert.Equal(t, StatusError, report.Status, "overall report must be non-OK when config is malformed")
	assert.Contains(t, report.Checks[0].Message, "config parse error")
}

// writeAgntConfig writes body to <dir>/.agnt.kdl, failing the test on error.
func writeAgntConfig(t *testing.T, dir, body string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".agnt.kdl"), []byte(body), 0o644))
}

func TestCheckDaemonHealth(t *testing.T) {
	t.Parallel()
	result := checkDaemonHealthStatic("0.12.20", 5*time.Minute, 3)
	assert.Equal(t, StatusOK, result.Status)
	assert.Contains(t, result.Message, "0.12.20")
}

func TestWorstStatus(t *testing.T) {
	t.Parallel()
	assert.Equal(t, StatusOK, worstStatus(StatusOK, StatusOK))
	assert.Equal(t, StatusWarning, worstStatus(StatusOK, StatusWarning))
	assert.Equal(t, StatusError, worstStatus(StatusWarning, StatusError))
	assert.Equal(t, StatusError, worstStatus(StatusError, StatusOK))
}
