package daemon

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/standardbeagle/agnt/internal/proxy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type recordingAgentNoticeSink struct {
	notices []AgentNotice
	err     error
}

func (s *recordingAgentNoticeSink) DeliverAgentNotice(notice AgentNotice) error {
	s.notices = append(s.notices, notice)
	return s.err
}

func TestEventHub_BroadcastAgentNotice_DispatchesThroughRegisteredSink(t *testing.T) {
	hub := NewEventHub()
	good := &recordingAgentNoticeSink{}
	bad := &recordingAgentNoticeSink{err: errors.New("closed PTY")}
	hub.AddAgentNoticeSink(good)
	hub.AddAgentNoticeSink(bad)

	notice := AgentNotice{SessionName: "agent", ProjectPath: "/work", Message: "[agnt] file arrived: .agnt-inbox/mock.png (24KB)"}
	delivered, errs := hub.BroadcastAgentNotice(notice)

	require.Equal(t, 1, delivered)
	require.Len(t, errs, 1)
	require.Equal(t, []AgentNotice{notice}, good.notices)
}

func TestEventHub_BroadcastAgentNotice_NoSinksIsExplicitDrop(t *testing.T) {
	delivered, errs := NewEventHub().BroadcastAgentNotice(AgentNotice{Message: "notice"})
	require.Zero(t, delivered)
	require.Error(t, errors.Join(errs...))
}

func TestStreamFilter_MatchesTypeFilter(t *testing.T) {
	t.Parallel()
	sf := streamFilter{
		types: map[proxy.LogEntryType]bool{
			proxy.LogTypeError: true,
		},
	}

	assert.True(t, sf.matches(proxy.LogEntry{Type: proxy.LogTypeError}, "proxy1", ""))
	assert.False(t, sf.matches(proxy.LogEntry{Type: proxy.LogTypeHTTP}, "proxy1", ""))
}

func TestStreamFilter_MatchesProxyIDFilter(t *testing.T) {
	t.Parallel()
	sf := streamFilter{
		proxyID: "target-proxy",
	}

	assert.True(t, sf.matches(proxy.LogEntry{Type: proxy.LogTypeError}, "target-proxy", ""))
	assert.False(t, sf.matches(proxy.LogEntry{Type: proxy.LogTypeError}, "other-proxy", ""))
}

func TestStreamFilter_MatchesSeverityFilter(t *testing.T) {
	t.Parallel()
	sf := streamFilter{
		severity: "error",
	}

	// Error entry matches error severity
	assert.True(t, sf.matches(proxy.LogEntry{
		Type:  proxy.LogTypeError,
		Error: &proxy.FrontendError{Message: "test"},
	}, "proxy1", ""))

	// HTTP 500 matches error severity
	assert.True(t, sf.matches(proxy.LogEntry{
		Type: proxy.LogTypeHTTP,
		HTTP: &proxy.HTTPLogEntry{StatusCode: 500},
	}, "proxy1", ""))

	// HTTP 200 does not match error severity
	assert.False(t, sf.matches(proxy.LogEntry{
		Type: proxy.LogTypeHTTP,
		HTTP: &proxy.HTTPLogEntry{StatusCode: 200},
	}, "proxy1", ""))

	// Custom log with error level
	assert.True(t, sf.matches(proxy.LogEntry{
		Type:   proxy.LogTypeCustom,
		Custom: &proxy.CustomLog{Level: "error"},
	}, "proxy1", ""))

	// Custom log with info level does not match error severity
	assert.False(t, sf.matches(proxy.LogEntry{
		Type:   proxy.LogTypeCustom,
		Custom: &proxy.CustomLog{Level: "info"},
	}, "proxy1", ""))
}

func TestStreamFilter_MatchesCombinedFilters(t *testing.T) {
	t.Parallel()
	sf := streamFilter{
		types:   map[proxy.LogEntryType]bool{proxy.LogTypeError: true},
		proxyID: "target-proxy",
	}

	assert.True(t, sf.matches(proxy.LogEntry{Type: proxy.LogTypeError}, "target-proxy", ""))
	assert.False(t, sf.matches(proxy.LogEntry{Type: proxy.LogTypeError}, "other-proxy", ""))
	assert.False(t, sf.matches(proxy.LogEntry{Type: proxy.LogTypeHTTP}, "target-proxy", ""))
}

func TestStreamFilter_EmptyFilterMatchesAll(t *testing.T) {
	t.Parallel()
	sf := streamFilter{}

	assert.True(t, sf.matches(proxy.LogEntry{Type: proxy.LogTypeError}, "any-proxy", ""))
	assert.True(t, sf.matches(proxy.LogEntry{Type: proxy.LogTypeHTTP}, "any-proxy", ""))
}

func TestStreamFilter_ProjectPathFilter(t *testing.T) {
	t.Parallel()
	sf := streamFilter{projectPath: "/project/a"}

	// Proxy belonging to /project/a passes
	assert.True(t, sf.matches(proxy.LogEntry{Type: proxy.LogTypeError}, "proxy-a", "/project/a"))
	// Proxy belonging to /project/b is dropped
	assert.False(t, sf.matches(proxy.LogEntry{Type: proxy.LogTypeError}, "proxy-b", "/project/b"))
	// Unregistered proxy (empty proxyPath) passes through to avoid silently dropping hook events
	assert.True(t, sf.matches(proxy.LogEntry{Type: proxy.LogTypeError}, "", ""))
}

func TestEventHub_RegisterProxyPath_ScopesStreamEvents(t *testing.T) {
	t.Parallel()
	hub := NewEventHub()

	hub.RegisterProxyPath("proxy-a", "/project/a")
	hub.RegisterProxyPath("proxy-b", "/project/b")

	// Subscribe to events only from /project/a
	sink := hub.AddStreamSink(streamFilter{projectPath: "/project/a"})
	defer hub.RemoveStreamSink(sink)

	entry := proxy.LogEntry{Type: proxy.LogTypeError, Error: &proxy.FrontendError{Message: "err"}}

	// Event from proxy-a should arrive
	hub.BroadcastLogEntry(entry, "proxy-a")
	// Event from proxy-b should be filtered out
	hub.BroadcastLogEntry(entry, "proxy-b")

	// Drain what arrived
	var received []string
	timeout := time.After(50 * time.Millisecond)
drain:
	for {
		select {
		case e, ok := <-sink.Ch:
			if !ok {
				break drain
			}
			_ = e
			received = append(received, "got")
		case <-timeout:
			break drain
		}
	}

	assert.Len(t, received, 1, "only proxy-a event should pass the project path filter")
}

func TestEventHub_AddRemoveStreamSink(t *testing.T) {
	t.Parallel()
	hub := NewEventHub()

	sink := hub.AddStreamSink(streamFilter{})
	require.NotNil(t, sink)
	require.NotNil(t, sink.Ch)

	// Remove should close channel
	hub.RemoveStreamSink(sink)

	_, ok := <-sink.Ch
	assert.False(t, ok, "channel should be closed after removal")
}

func TestEventHub_BroadcastLogEntry_DeliversToMatchingSink(t *testing.T) {
	t.Parallel()
	hub := NewEventHub()

	sink := hub.AddStreamSink(streamFilter{
		types: map[proxy.LogEntryType]bool{proxy.LogTypeError: true},
	})
	defer hub.RemoveStreamSink(sink)

	entry := proxy.LogEntry{
		Type:  proxy.LogTypeError,
		Error: &proxy.FrontendError{Message: "test error"},
	}

	hub.BroadcastLogEntry(entry, "proxy1")

	select {
	case received := <-sink.Ch:
		assert.Equal(t, proxy.LogTypeError, received.Type)
		assert.NotNil(t, received.Error)
		assert.Equal(t, "test error", received.Error.Message)
	case <-time.After(time.Second):
		t.Fatal("expected to receive event on sink channel")
	}
}

func TestEventHub_BroadcastLogEntry_FiltersNonMatching(t *testing.T) {
	t.Parallel()
	hub := NewEventHub()

	sink := hub.AddStreamSink(streamFilter{
		types: map[proxy.LogEntryType]bool{proxy.LogTypeError: true},
	})
	defer hub.RemoveStreamSink(sink)

	entry := proxy.LogEntry{
		Type: proxy.LogTypeHTTP,
		HTTP: &proxy.HTTPLogEntry{StatusCode: 200},
	}

	hub.BroadcastLogEntry(entry, "proxy1")

	select {
	case <-sink.Ch:
		t.Fatal("should not receive non-matching event")
	case <-time.After(50 * time.Millisecond):
		// Expected: no event received
	}
}

func TestEventHub_BroadcastLogEntry_DropsOnFullChannel(t *testing.T) {
	t.Parallel()
	hub := NewEventHub()

	sink := hub.AddStreamSink(streamFilter{})
	defer hub.RemoveStreamSink(sink)

	// Fill the channel (buffer size is 64)
	for i := 0; i < 100; i++ {
		hub.BroadcastLogEntry(proxy.LogEntry{Type: proxy.LogTypeError}, "proxy1")
	}
	// No panic = events were dropped gracefully on full channel
}

func TestEventHub_BroadcastLogEntry_MultipleSinks(t *testing.T) {
	t.Parallel()
	hub := NewEventHub()

	sink1 := hub.AddStreamSink(streamFilter{
		types: map[proxy.LogEntryType]bool{proxy.LogTypeError: true},
	})
	defer hub.RemoveStreamSink(sink1)

	sink2 := hub.AddStreamSink(streamFilter{
		types: map[proxy.LogEntryType]bool{proxy.LogTypeHTTP: true},
	})
	defer hub.RemoveStreamSink(sink2)

	entry := proxy.LogEntry{Type: proxy.LogTypeError}
	hub.BroadcastLogEntry(entry, "proxy1")

	// sink1 should receive (matches error type)
	select {
	case <-sink1.Ch:
	case <-time.After(time.Second):
		t.Fatal("sink1 should receive error event")
	}

	// sink2 should not receive (only matches HTTP)
	select {
	case <-sink2.Ch:
		t.Fatal("sink2 should not receive error event")
	case <-time.After(50 * time.Millisecond):
	}
}

func TestEventHub_BroadcastLogEntry_ConcurrentSinks(t *testing.T) {
	t.Parallel()
	hub := NewEventHub()
	var wg sync.WaitGroup

	const numSinks = 10
	sinks := make([]*StreamSink, numSinks)

	for i := 0; i < numSinks; i++ {
		sinks[i] = hub.AddStreamSink(streamFilter{})
		defer hub.RemoveStreamSink(sinks[i])
	}

	// Broadcast from multiple goroutines
	const numBroadcasters = 5
	for b := 0; b < numBroadcasters; b++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				hub.BroadcastLogEntry(proxy.LogEntry{Type: proxy.LogTypeError}, "proxy1")
			}
		}()
	}

	wg.Wait()

	// All sinks should have received events
	for _, sink := range sinks {
		count := 0
		for {
			select {
			case <-sink.Ch:
				count++
			default:
				goto nextSink
			}
		}
	nextSink:
		assert.Greater(t, count, 0, "each sink should receive at least one event")
	}
}

func TestBuildStreamFilter(t *testing.T) {
	t.Parallel()
	f := protocolStreamEventFilter{
		Types:    []string{"error", "http"},
		ProxyID:  "test-proxy",
		Severity: "error",
	}

	// Import the protocol package type for testing
	sf := buildStreamFilterFromStrings(f.Types, f.ProxyID, f.Severity)

	assert.Equal(t, "test-proxy", sf.proxyID)
	assert.Equal(t, "error", sf.severity)
	assert.True(t, sf.types[proxy.LogTypeError])
	assert.True(t, sf.types[proxy.LogTypeHTTP])
	assert.False(t, sf.types[proxy.LogTypePanelMessage])
}

// Helper to avoid importing protocol in test (which would create import cycle)
type protocolStreamEventFilter struct {
	Types    []string
	ProxyID  string
	Severity string
}

func buildStreamFilterFromStrings(types []string, proxyID, severity string) streamFilter {
	sf := streamFilter{
		proxyID:  proxyID,
		severity: severity,
	}
	if len(types) > 0 {
		sf.types = make(map[proxy.LogEntryType]bool, len(types))
		for _, t := range types {
			sf.types[proxy.LogEntryType(t)] = true
		}
	}
	return sf
}

func TestEntryHasSeverity(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		entry    proxy.LogEntry
		severity string
		want     bool
	}{
		{
			name:     "error type matches error",
			entry:    proxy.LogEntry{Type: proxy.LogTypeError},
			severity: "error",
			want:     true,
		},
		{
			name:     "error type does not match warning",
			entry:    proxy.LogEntry{Type: proxy.LogTypeError},
			severity: "warning",
			want:     false,
		},
		{
			name:     "HTTP 500 matches error",
			entry:    proxy.LogEntry{Type: proxy.LogTypeHTTP, HTTP: &proxy.HTTPLogEntry{StatusCode: 500}},
			severity: "error",
			want:     true,
		},
		{
			name:     "HTTP 404 matches warning",
			entry:    proxy.LogEntry{Type: proxy.LogTypeHTTP, HTTP: &proxy.HTTPLogEntry{StatusCode: 404}},
			severity: "warning",
			want:     true,
		},
		{
			name:     "HTTP 200 matches nothing",
			entry:    proxy.LogEntry{Type: proxy.LogTypeHTTP, HTTP: &proxy.HTTPLogEntry{StatusCode: 200}},
			severity: "error",
			want:     false,
		},
		{
			name:     "custom error level",
			entry:    proxy.LogEntry{Type: proxy.LogTypeCustom, Custom: &proxy.CustomLog{Level: "error"}},
			severity: "error",
			want:     true,
		},
		{
			name:     "custom info does not match error",
			entry:    proxy.LogEntry{Type: proxy.LogTypeCustom, Custom: &proxy.CustomLog{Level: "info"}},
			severity: "error",
			want:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, entryHasSeverity(tt.entry, tt.severity))
		})
	}
}

func TestStreamFilter_MatchesProcessIDFilter(t *testing.T) {
	t.Parallel()
	sf := streamFilter{
		processID: "dev-server",
	}

	// Matching process output
	assert.True(t, sf.matches(proxy.LogEntry{
		Type: proxy.LogTypeProcessOutput,
		ProcessOutput: &proxy.ProcessOutputEvent{
			ProcessID: "dev-server",
			Line:      "listening on :3000",
		},
	}, "", ""))

	// Non-matching process ID
	assert.False(t, sf.matches(proxy.LogEntry{
		Type: proxy.LogTypeProcessOutput,
		ProcessOutput: &proxy.ProcessOutputEvent{
			ProcessID: "test-runner",
			Line:      "test passed",
		},
	}, "", ""))

	// Non-process entries pass through when processID filter is set
	// (processID filter only applies to LogTypeProcessOutput)
	assert.True(t, sf.matches(proxy.LogEntry{Type: proxy.LogTypeError}, "", ""))
}

func TestStreamFilter_MatchesGrepFilter(t *testing.T) {
	t.Parallel()
	sf := streamFilter{
		grep: "error",
	}

	// Line contains grep string
	assert.True(t, sf.matches(proxy.LogEntry{
		Type: proxy.LogTypeProcessOutput,
		ProcessOutput: &proxy.ProcessOutputEvent{
			ProcessID: "dev",
			Line:      "error: connection refused",
		},
	}, "", ""))

	// Line does not contain grep string
	assert.False(t, sf.matches(proxy.LogEntry{
		Type: proxy.LogTypeProcessOutput,
		ProcessOutput: &proxy.ProcessOutputEvent{
			ProcessID: "dev",
			Line:      "listening on :3000",
		},
	}, "", ""))

	// Non-process entries are not affected by grep filter
	assert.True(t, sf.matches(proxy.LogEntry{Type: proxy.LogTypeError}, "", ""))
}

func TestStreamFilter_MatchesGrepStreamFilter(t *testing.T) {
	t.Parallel()
	sf := streamFilter{
		grepStream: "stdout",
	}

	// Matching stream
	assert.True(t, sf.matches(proxy.LogEntry{
		Type: proxy.LogTypeProcessOutput,
		ProcessOutput: &proxy.ProcessOutputEvent{
			ProcessID: "dev",
			Stream:    "stdout",
			Line:      "hello",
		},
	}, "", ""))

	// Non-matching stream
	assert.False(t, sf.matches(proxy.LogEntry{
		Type: proxy.LogTypeProcessOutput,
		ProcessOutput: &proxy.ProcessOutputEvent{
			ProcessID: "dev",
			Stream:    "stderr",
			Line:      "hello",
		},
	}, "", ""))
}

func TestStreamFilter_MatchesProcessCombinedFilter(t *testing.T) {
	t.Parallel()
	sf := streamFilter{
		processID: "dev",
		grep:      "error",
		types:     map[proxy.LogEntryType]bool{proxy.LogTypeProcessOutput: true},
	}

	// All filters match
	assert.True(t, sf.matches(proxy.LogEntry{
		Type: proxy.LogTypeProcessOutput,
		ProcessOutput: &proxy.ProcessOutputEvent{
			ProcessID: "dev",
			Line:      "error: something broke",
		},
	}, "", ""))

	// Wrong process ID
	assert.False(t, sf.matches(proxy.LogEntry{
		Type: proxy.LogTypeProcessOutput,
		ProcessOutput: &proxy.ProcessOutputEvent{
			ProcessID: "test",
			Line:      "error: something broke",
		},
	}, "", ""))

	// Grep not matching
	assert.False(t, sf.matches(proxy.LogEntry{
		Type: proxy.LogTypeProcessOutput,
		ProcessOutput: &proxy.ProcessOutputEvent{
			ProcessID: "dev",
			Line:      "all good",
		},
	}, "", ""))

	// Wrong type
	assert.False(t, sf.matches(proxy.LogEntry{Type: proxy.LogTypeError}, "", ""))
}

func TestEventHub_BroadcastProcessOutput_DeliversToMatchingSink(t *testing.T) {
	t.Parallel()
	hub := NewEventHub()

	sink := hub.AddStreamSink(streamFilter{
		processID: "dev-server",
	})
	defer hub.RemoveStreamSink(sink)

	entry := proxy.LogEntry{
		Type: proxy.LogTypeProcessOutput,
		ProcessOutput: &proxy.ProcessOutputEvent{
			ProcessID: "dev-server",
			Stream:    "combined",
			Line:      "Server started on :3000",
			Timestamp: time.Now(),
		},
	}

	hub.BroadcastProcessOutput(entry)

	select {
	case received := <-sink.Ch:
		assert.Equal(t, proxy.LogTypeProcessOutput, received.Type)
		require.NotNil(t, received.ProcessOutput)
		assert.Equal(t, "dev-server", received.ProcessOutput.ProcessID)
		assert.Equal(t, "Server started on :3000", received.ProcessOutput.Line)
	case <-time.After(time.Second):
		t.Fatal("expected to receive process output event on sink channel")
	}
}

func TestEventHub_BroadcastProcessOutput_FiltersNonMatching(t *testing.T) {
	t.Parallel()
	hub := NewEventHub()

	sink := hub.AddStreamSink(streamFilter{
		processID: "dev-server",
	})
	defer hub.RemoveStreamSink(sink)

	entry := proxy.LogEntry{
		Type: proxy.LogTypeProcessOutput,
		ProcessOutput: &proxy.ProcessOutputEvent{
			ProcessID: "test-runner",
			Line:      "test passed",
		},
	}

	hub.BroadcastProcessOutput(entry)

	select {
	case <-sink.Ch:
		t.Fatal("should not receive event for wrong process ID")
	case <-time.After(50 * time.Millisecond):
	}
}

func TestEventHub_BroadcastProcessOutput_WithGrepFilter(t *testing.T) {
	t.Parallel()
	hub := NewEventHub()

	sink := hub.AddStreamSink(streamFilter{
		grep: "ERROR",
	})
	defer hub.RemoveStreamSink(sink)

	// Matching grep
	hub.BroadcastProcessOutput(proxy.LogEntry{
		Type: proxy.LogTypeProcessOutput,
		ProcessOutput: &proxy.ProcessOutputEvent{
			ProcessID: "dev",
			Line:      "ERROR: connection refused",
		},
	})

	select {
	case received := <-sink.Ch:
		assert.Equal(t, "ERROR: connection refused", received.ProcessOutput.Line)
	case <-time.After(time.Second):
		t.Fatal("expected to receive event matching grep filter")
	}

	// Non-matching grep
	hub.BroadcastProcessOutput(proxy.LogEntry{
		Type: proxy.LogTypeProcessOutput,
		ProcessOutput: &proxy.ProcessOutputEvent{
			ProcessID: "dev",
			Line:      "Listening on :3000",
		},
	})

	select {
	case <-sink.Ch:
		t.Fatal("should not receive event not matching grep filter")
	case <-time.After(50 * time.Millisecond):
	}
}
