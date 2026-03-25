package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunDoctor_AllChecksRun(t *testing.T) {
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
	report := runDoctorWithChecks(context.Background(), nil)
	assert.Equal(t, StatusOK, report.Status)
	assert.Empty(t, report.Checks)
}

func TestCheckSessionHealth_NoSessions(t *testing.T) {
	reg := NewSessionRegistry(60 * time.Second)
	result := checkSessionHealth(context.Background(), reg)
	assert.Equal(t, StatusOK, result.Status)
}

func TestCheckSessionHealth_StaleSession(t *testing.T) {
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
	store := NewStartupLogStore(100)
	result := checkStartupErrors(context.Background(), store)
	assert.Equal(t, StatusOK, result.Status)
}

func TestCheckStartupErrors_RecentErrors(t *testing.T) {
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
	result := checkConfigHealth(context.Background(), "/nonexistent/path/that/does/not/exist")
	assert.Equal(t, StatusOK, result.Status)
}

func TestCheckDaemonHealth(t *testing.T) {
	result := checkDaemonHealthStatic("0.12.20", 5*time.Minute, 3)
	assert.Equal(t, StatusOK, result.Status)
	assert.Contains(t, result.Message, "0.12.20")
}

func TestWorstStatus(t *testing.T) {
	assert.Equal(t, StatusOK, worstStatus(StatusOK, StatusOK))
	assert.Equal(t, StatusWarning, worstStatus(StatusOK, StatusWarning))
	assert.Equal(t, StatusError, worstStatus(StatusWarning, StatusError))
	assert.Equal(t, StatusError, worstStatus(StatusError, StatusOK))
}
