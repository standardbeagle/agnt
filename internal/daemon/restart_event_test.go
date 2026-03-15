package daemon

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRestartEvent_AddAndRetrieve(t *testing.T) {
	state := &processRestartState{
		config: DefaultAutoRestartConfig(),
	}

	event := RestartEvent{
		Timestamp:  time.Now(),
		ExitCode:   1,
		Runtime:    30 * time.Second,
		LastOutput: "error: module not found\nsegfault",
	}
	state.addRestartEvent(event)

	events := state.getRestartEvents()
	require.Len(t, events, 1)
	assert.Equal(t, 1, events[0].ExitCode)
	assert.Equal(t, 30*time.Second, events[0].Runtime)
	assert.Contains(t, events[0].LastOutput, "module not found")
}

func TestRestartEvent_MaxEventsRetained(t *testing.T) {
	state := &processRestartState{
		config: DefaultAutoRestartConfig(),
	}

	for i := 0; i < maxRestartEvents+5; i++ {
		state.addRestartEvent(RestartEvent{
			Timestamp: time.Now(),
			ExitCode:  i,
		})
	}

	events := state.getRestartEvents()
	assert.Len(t, events, maxRestartEvents)
	// Oldest events should be trimmed; first retained event has exit code 5
	assert.Equal(t, 5, events[0].ExitCode)
}

func TestRestartEvent_EmptyReturnsNil(t *testing.T) {
	state := &processRestartState{
		config: DefaultAutoRestartConfig(),
	}

	events := state.getRestartEvents()
	assert.Nil(t, events)
}

func TestRestartEvent_ReturnsCopy(t *testing.T) {
	state := &processRestartState{
		config: DefaultAutoRestartConfig(),
	}
	state.addRestartEvent(RestartEvent{ExitCode: 1})

	events1 := state.getRestartEvents()
	events1[0].ExitCode = 99

	events2 := state.getRestartEvents()
	assert.Equal(t, 1, events2[0].ExitCode, "modifying returned slice should not affect stored events")
}

func TestFormatRestartDelimiter_Empty(t *testing.T) {
	assert.Equal(t, "", FormatRestartDelimiter(nil))
	assert.Equal(t, "", FormatRestartDelimiter([]RestartEvent{}))
}

func TestFormatRestartDelimiter_SingleEvent(t *testing.T) {
	ts := time.Date(2026, 3, 14, 14, 23, 5, 0, time.UTC)
	events := []RestartEvent{{
		Timestamp:  ts,
		ExitCode:   1,
		Runtime:    2*time.Minute + 15*time.Second,
		LastOutput: "error: Cannot find module 'express'\nProcess exited",
	}}

	result := FormatRestartDelimiter(events)

	assert.Contains(t, result, "PROCESS RESTARTED")
	assert.Contains(t, result, "exit code: 1")
	assert.Contains(t, result, "14:23:05")
	assert.Contains(t, result, "2m 15s")
	assert.Contains(t, result, "Cannot find module 'express'")
	assert.Contains(t, result, "Process exited")
	assert.Contains(t, result, "═══")
	assert.Contains(t, result, "───")
}

func TestFormatRestartDelimiter_MultipleEvents(t *testing.T) {
	events := []RestartEvent{
		{Timestamp: time.Now(), ExitCode: 1, Runtime: 5 * time.Second, LastOutput: "crash one"},
		{Timestamp: time.Now(), ExitCode: 137, Runtime: 10 * time.Second, LastOutput: "crash two"},
	}

	result := FormatRestartDelimiter(events)

	assert.Equal(t, 2, strings.Count(result, "PROCESS RESTARTED"))
	assert.Contains(t, result, "exit code: 1")
	assert.Contains(t, result, "exit code: 137")
	assert.Contains(t, result, "crash one")
	assert.Contains(t, result, "crash two")
}

func TestFormatRestartDelimiter_NoLastOutput(t *testing.T) {
	events := []RestartEvent{{
		Timestamp: time.Now(),
		ExitCode:  0,
		Runtime:   500 * time.Millisecond,
	}}

	result := FormatRestartDelimiter(events)

	assert.Contains(t, result, "PROCESS RESTARTED")
	assert.Contains(t, result, "exit code: 0")
	assert.Contains(t, result, "500ms")
	assert.NotContains(t, result, "Last output")
}

func TestFormatRestartRuntime(t *testing.T) {
	tests := []struct {
		d    time.Duration
		want string
	}{
		{500 * time.Millisecond, "500ms"},
		{5 * time.Second, "5s"},
		{90 * time.Second, "1m 30s"},
		{2 * time.Minute, "2m"},
		{3*time.Minute + 45*time.Second, "3m 45s"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			assert.Equal(t, tt.want, formatRestartRuntime(tt.d))
		})
	}
}

func TestProcessAutoRestarter_GetRestartEvents(t *testing.T) {
	// Test that GetRestartEvents returns nil for unregistered process
	r := &ProcessAutoRestarter{
		processes: make(map[string]*processRestartState),
	}

	events := r.GetRestartEvents("nonexistent")
	assert.Nil(t, events)

	// Register a process and add events
	r.processes["test"] = &processRestartState{
		config: DefaultAutoRestartConfig(),
	}
	r.processes["test"].addRestartEvent(RestartEvent{ExitCode: 42})

	events = r.GetRestartEvents("test")
	require.Len(t, events, 1)
	assert.Equal(t, 42, events[0].ExitCode)
}
