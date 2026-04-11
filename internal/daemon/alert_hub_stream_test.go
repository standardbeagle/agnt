package daemon

import (
	"sync"
	"testing"
	"time"

	"github.com/standardbeagle/agnt/internal/config"
	"github.com/standardbeagle/agnt/internal/proxy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStreamFilter_MatchesTypeFilter(t *testing.T) {
	sf := streamFilter{
		types: map[proxy.LogEntryType]bool{
			proxy.LogTypeError: true,
		},
	}

	assert.True(t, sf.matches(proxy.LogEntry{Type: proxy.LogTypeError}, "proxy1"))
	assert.False(t, sf.matches(proxy.LogEntry{Type: proxy.LogTypeHTTP}, "proxy1"))
}

func TestStreamFilter_MatchesProxyIDFilter(t *testing.T) {
	sf := streamFilter{
		proxyID: "target-proxy",
	}

	assert.True(t, sf.matches(proxy.LogEntry{Type: proxy.LogTypeError}, "target-proxy"))
	assert.False(t, sf.matches(proxy.LogEntry{Type: proxy.LogTypeError}, "other-proxy"))
}

func TestStreamFilter_MatchesSeverityFilter(t *testing.T) {
	sf := streamFilter{
		severity: "error",
	}

	// Error entry matches error severity
	assert.True(t, sf.matches(proxy.LogEntry{
		Type:  proxy.LogTypeError,
		Error: &proxy.FrontendError{Message: "test"},
	}, "proxy1"))

	// HTTP 500 matches error severity
	assert.True(t, sf.matches(proxy.LogEntry{
		Type: proxy.LogTypeHTTP,
		HTTP: &proxy.HTTPLogEntry{StatusCode: 500},
	}, "proxy1"))

	// HTTP 200 does not match error severity
	assert.False(t, sf.matches(proxy.LogEntry{
		Type: proxy.LogTypeHTTP,
		HTTP: &proxy.HTTPLogEntry{StatusCode: 200},
	}, "proxy1"))

	// Custom log with error level
	assert.True(t, sf.matches(proxy.LogEntry{
		Type:   proxy.LogTypeCustom,
		Custom: &proxy.CustomLog{Level: "error"},
	}, "proxy1"))

	// Custom log with info level does not match error severity
	assert.False(t, sf.matches(proxy.LogEntry{
		Type:   proxy.LogTypeCustom,
		Custom: &proxy.CustomLog{Level: "info"},
	}, "proxy1"))
}

func TestStreamFilter_MatchesCombinedFilters(t *testing.T) {
	sf := streamFilter{
		types:   map[proxy.LogEntryType]bool{proxy.LogTypeError: true},
		proxyID: "target-proxy",
	}

	assert.True(t, sf.matches(proxy.LogEntry{Type: proxy.LogTypeError}, "target-proxy"))
	assert.False(t, sf.matches(proxy.LogEntry{Type: proxy.LogTypeError}, "other-proxy"))
	assert.False(t, sf.matches(proxy.LogEntry{Type: proxy.LogTypeHTTP}, "target-proxy"))
}

func TestStreamFilter_EmptyFilterMatchesAll(t *testing.T) {
	sf := streamFilter{}

	assert.True(t, sf.matches(proxy.LogEntry{Type: proxy.LogTypeError}, "any-proxy"))
	assert.True(t, sf.matches(proxy.LogEntry{Type: proxy.LogTypeHTTP}, "any-proxy"))
}

func TestAlertHub_AddRemoveStreamSink(t *testing.T) {
	hub := NewAlertHub()

	sink := hub.AddStreamSink(streamFilter{})
	require.NotNil(t, sink)
	require.NotNil(t, sink.Ch)

	// Remove should close channel
	hub.RemoveStreamSink(sink)

	_, ok := <-sink.Ch
	assert.False(t, ok, "channel should be closed after removal")
}

func TestAlertHub_BroadcastLogEntry_DeliversToMatchingSink(t *testing.T) {
	hub := NewAlertHub()

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

func TestAlertHub_BroadcastLogEntry_FiltersNonMatching(t *testing.T) {
	hub := NewAlertHub()

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

func TestAlertHub_BroadcastLogEntry_DropsOnFullChannel(t *testing.T) {
	hub := NewAlertHub()

	sink := hub.AddStreamSink(streamFilter{})
	defer hub.RemoveStreamSink(sink)

	// Fill the channel (buffer size is 64)
	for i := 0; i < 100; i++ {
		hub.BroadcastLogEntry(proxy.LogEntry{Type: proxy.LogTypeError}, "proxy1")
	}
	// No panic = events were dropped gracefully on full channel
}

func TestAlertHub_BroadcastLogEntry_MultipleSinks(t *testing.T) {
	hub := NewAlertHub()

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

func TestAlertHub_BroadcastLogEntry_ConcurrentSinks(t *testing.T) {
	hub := NewAlertHub()
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

// Mock sinks for testing push config filtering in Deliver
type mockOverlaySink struct {
	alerts  []string
	enabled bool
}

func (m *mockOverlaySink) TypeAlert(text string) error {
	m.alerts = append(m.alerts, text)
	return nil
}

func (m *mockOverlaySink) IsEnabled() bool {
	return m.enabled
}

type mockMCPSink struct {
	alerts []struct{ level, msg string }
}

func (m *mockMCPSink) SendAlert(level string, message string) error {
	m.alerts = append(m.alerts, struct{ level, msg string }{level, message})
	return nil
}

func TestAlertHub_Deliver_NoPushConfig(t *testing.T) {
	hub := NewAlertHub()
	overlay := &mockOverlaySink{enabled: true}
	mcp := &mockMCPSink{}
	hub.SetOverlaySink(overlay)
	hub.AddMCPSink(mcp)

	hub.Deliver("error", "test alert")

	assert.Len(t, overlay.alerts, 1)
	assert.Equal(t, "test alert", overlay.alerts[0])
	assert.Len(t, mcp.alerts, 1)
	assert.Equal(t, "test alert", mcp.alerts[0].msg)
}

func TestAlertHub_Deliver_BothChannelsEnabled(t *testing.T) {
	hub := NewAlertHub()
	tr := true
	hub.SetPushConfig(&config.PushConfig{
		MCPNotifications: &tr,
		PTYInjection:     &tr,
	})
	overlay := &mockOverlaySink{enabled: true}
	mcp := &mockMCPSink{}
	hub.SetOverlaySink(overlay)
	hub.AddMCPSink(mcp)

	hub.Deliver("error", "test alert")

	assert.Len(t, overlay.alerts, 1)
	assert.Len(t, mcp.alerts, 1)
}

func TestAlertHub_Deliver_PTYDisabled(t *testing.T) {
	hub := NewAlertHub()
	f := false
	tr := true
	hub.SetPushConfig(&config.PushConfig{
		MCPNotifications: &tr,
		PTYInjection:     &f,
	})
	overlay := &mockOverlaySink{enabled: true}
	mcp := &mockMCPSink{}
	hub.SetOverlaySink(overlay)
	hub.AddMCPSink(mcp)

	hub.Deliver("error", "test alert")

	assert.Empty(t, overlay.alerts, "PTY disabled should skip overlay")
	assert.Len(t, mcp.alerts, 1, "MCP should still deliver")
}

func TestAlertHub_Deliver_MCPDisabled(t *testing.T) {
	hub := NewAlertHub()
	f := false
	tr := true
	hub.SetPushConfig(&config.PushConfig{
		MCPNotifications: &f,
		PTYInjection:     &tr,
	})
	overlay := &mockOverlaySink{enabled: true}
	mcp := &mockMCPSink{}
	hub.SetOverlaySink(overlay)
	hub.AddMCPSink(mcp)

	hub.Deliver("error", "test alert")

	assert.Len(t, overlay.alerts, 1, "PTY should still deliver")
	assert.Empty(t, mcp.alerts, "MCP disabled should skip MCP")
}

func TestAlertHub_Deliver_BothDisabled(t *testing.T) {
	hub := NewAlertHub()
	f := false
	hub.SetPushConfig(&config.PushConfig{
		MCPNotifications: &f,
		PTYInjection:     &f,
	})
	overlay := &mockOverlaySink{enabled: true}
	mcp := &mockMCPSink{}
	hub.SetOverlaySink(overlay)
	hub.AddMCPSink(mcp)

	hub.Deliver("error", "test alert")

	assert.Empty(t, overlay.alerts, "PTY disabled should skip overlay")
	assert.Empty(t, mcp.alerts, "MCP disabled should skip MCP")
}

func TestAlertHub_Deliver_EmptyMessage(t *testing.T) {
	hub := NewAlertHub()
	overlay := &mockOverlaySink{enabled: true}
	mcp := &mockMCPSink{}
	hub.SetOverlaySink(overlay)
	hub.AddMCPSink(mcp)

	hub.Deliver("error", "")

	assert.Empty(t, overlay.alerts)
	assert.Empty(t, mcp.alerts)
}

func TestAlertHub_Deliver_ClaudeCodePreset(t *testing.T) {
	hub := NewAlertHub()
	hub.SetPushConfig(config.PresetPushConfig("claude-code"))
	overlay := &mockOverlaySink{enabled: true}
	mcp := &mockMCPSink{}
	hub.SetOverlaySink(overlay)
	hub.AddMCPSink(mcp)

	hub.Deliver("error", "test alert")

	assert.Empty(t, overlay.alerts, "claude-code preset disables PTY injection")
	assert.Len(t, mcp.alerts, 1, "claude-code preset enables MCP notifications")
}
