package tools

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/standardbeagle/agnt/internal/config"
	"github.com/standardbeagle/agnt/internal/proxy"
)

const (
	// ChannelNotificationMethod is the MCP notification method for channel events.
	ChannelNotificationMethod = "notifications/claude/channel"

	// channelDedupeRingSize is the maximum number of dedupe entries.
	channelDedupeRingSize = 256
)

// NotifyFunc sends a notification with the given method and params.
// In production this wraps ServerSession.Notify; in tests it captures calls.
type NotifyFunc func(ctx context.Context, method string, params any) error

// ChannelSink bridges daemon StreamSink events to MCP channel notifications.
// It extracts message/severity/location from LogEntry union types, sanitizes
// meta keys, deduplicates within a configurable window, and filters by severity.
type ChannelSink struct {
	cfg     *config.ChannelConfig
	notify  NotifyFunc
	nowFunc func() time.Time // injectable clock for tests

	dedupeRing []dedupeEntry
	dedupeMu   sync.Mutex
}

// dedupeEntry tracks a previously emitted event for deduplication.
type dedupeEntry struct {
	key       string
	timestamp time.Time
}

// channelNotification is the params payload sent via Notify.
type channelNotification struct {
	Content string            `json:"content"`
	Meta    map[string]string `json:"meta,omitempty"`
}

// NewChannelSink creates a channel sink with the given config and notify function.
func NewChannelSink(cfg *config.ChannelConfig, notify NotifyFunc) *ChannelSink {
	return &ChannelSink{
		cfg:        cfg,
		notify:     notify,
		nowFunc:    time.Now,
		dedupeRing: make([]dedupeEntry, 0, channelDedupeRingSize),
	}
}

// SetNowFunc overrides the clock function (for tests).
func (s *ChannelSink) SetNowFunc(fn func() time.Time) {
	s.nowFunc = fn
}

// HandleEntry processes a single LogEntry and emits a channel notification
// if it passes severity filtering and deduplication.
func (s *ChannelSink) HandleEntry(ctx context.Context, entry proxy.LogEntry) {
	content, meta := extractChannelFields(entry)
	if content == "" {
		return
	}

	// Apply event type filter.
	if events := s.cfg.GetEvents(); len(events) > 0 {
		entryType := string(entry.Type)
		matched := false
		for _, ev := range events {
			if ev == entryType {
				matched = true
				break
			}
		}
		if !matched {
			return
		}
	}

	// Apply severity filter.
	if !passesSeverityFilter(meta, s.cfg.GetSeverity()) {
		return
	}

	// Deduplicate. DedupeWindow == 0 means disabled.
	// DefaultAgntConfig sets it to 2000, so 0 is always an explicit choice.
	if s.cfg.DedupeWindow > 0 {
		w := time.Duration(s.cfg.DedupeWindow) * time.Millisecond
		if s.isDuplicate(content, meta, w) {
			return
		}
	}

	// Sanitize meta keys.
	sanitized := make(map[string]string, len(meta))
	for k, v := range meta {
		clean := sanitizeMetaKey(k)
		if clean != "" {
			sanitized[clean] = v
		}
	}

	params := channelNotification{
		Content: content,
		Meta:    sanitized,
	}
	_ = s.notify(ctx, ChannelNotificationMethod, params)
}

// isDuplicate checks if an identical event was emitted within the dedupe window.
func (s *ChannelSink) isDuplicate(content string, meta map[string]string, window time.Duration) bool {
	key := dedupeKey(string(meta["type"]), meta["severity"], content, meta["location"])
	now := s.nowFunc()

	s.dedupeMu.Lock()
	defer s.dedupeMu.Unlock()

	// Check existing entries (newest first for cache locality).
	for i := len(s.dedupeRing) - 1; i >= 0; i-- {
		if s.dedupeRing[i].key == key && now.Sub(s.dedupeRing[i].timestamp) < window {
			return true
		}
	}

	// Add new entry.
	s.dedupeRing = append(s.dedupeRing, dedupeEntry{key: key, timestamp: now})

	// Evict oldest if over capacity.
	if len(s.dedupeRing) > channelDedupeRingSize {
		s.dedupeRing = s.dedupeRing[len(s.dedupeRing)-channelDedupeRingSize:]
	}

	return false
}

// dedupeKey produces a deduplication key from event fields.
func dedupeKey(entryType, severity, content, location string) string {
	h := sha256.Sum256([]byte(content))
	return fmt.Sprintf("%s|%s|%x|%s", entryType, severity, h[:8], location)
}

// extractChannelFields extracts content and meta from a LogEntry.
// Returns empty content if the entry has no message to forward.
func extractChannelFields(entry proxy.LogEntry) (string, map[string]string) {
	meta := make(map[string]string)
	meta["type"] = string(entry.Type)

	switch entry.Type {
	case proxy.LogTypeError:
		if entry.Error == nil {
			return "", nil
		}
		msg := entry.Error.Message
		if msg == "" {
			return "", nil
		}
		meta["severity"] = "error"
		if entry.Error.Source != "" && entry.Error.LineNo > 0 {
			meta["location"] = fmt.Sprintf("%s:%d:%d", entry.Error.Source, entry.Error.LineNo, entry.Error.ColNo)
		}
		if entry.Error.URL != "" {
			meta["page"] = entry.Error.URL
		}
		return msg, meta

	case proxy.LogTypeHTTP:
		if entry.HTTP == nil {
			return "", nil
		}
		if entry.HTTP.StatusCode < 400 && entry.HTTP.Error == "" {
			return "", nil
		}
		severity := "warning"
		if entry.HTTP.StatusCode >= 500 || entry.HTTP.Error != "" {
			severity = "error"
		}
		meta["severity"] = severity
		var msg string
		if entry.HTTP.Error != "" {
			msg = fmt.Sprintf("%s %s: %s", entry.HTTP.Method, entry.HTTP.URL, entry.HTTP.Error)
		} else {
			msg = fmt.Sprintf("%s %s → %d", entry.HTTP.Method, entry.HTTP.URL, entry.HTTP.StatusCode)
		}
		return msg, meta

	case proxy.LogTypeDiagnostic:
		if entry.Diagnostic == nil {
			return "", nil
		}
		if entry.Diagnostic.Message == "" {
			return "", nil
		}
		meta["severity"] = string(entry.Diagnostic.Level)
		if entry.Diagnostic.Category != "" {
			meta["category"] = entry.Diagnostic.Category
		}
		return entry.Diagnostic.Message, meta

	case proxy.LogTypeCustom:
		if entry.Custom == nil {
			return "", nil
		}
		if entry.Custom.Message == "" {
			return "", nil
		}
		meta["severity"] = entry.Custom.Level
		return entry.Custom.Message, meta

	case proxy.LogTypePanelMessage:
		if entry.PanelMessage == nil {
			return "", nil
		}
		if entry.PanelMessage.Message == "" {
			return "", nil
		}
		meta["severity"] = "info"
		if entry.PanelMessage.URL != "" {
			meta["page"] = entry.PanelMessage.URL
		}
		return entry.PanelMessage.Message, meta

	case proxy.LogTypeInteraction:
		if entry.Interaction == nil {
			return "", nil
		}
		meta["severity"] = "info"
		return fmt.Sprintf("%s on %s", entry.Interaction.EventType, entry.Interaction.Target.Selector), meta

	default:
		return "", nil
	}
}

// severityOrder defines the ordering of severity levels (lower index = lower severity).
var severityOrder = []string{"trace", "debug", "info", "warning", "error"}

// passesSeverityFilter returns true if the entry's severity meets the threshold.
func passesSeverityFilter(meta map[string]string, threshold string) bool {
	entrySev, ok := meta["severity"]
	if !ok {
		return true
	}
	return severityRank(entrySev) >= severityRank(threshold)
}

// severityRank returns the numeric rank of a severity level.
func severityRank(s string) int {
	for i, v := range severityOrder {
		if v == s {
			return i
		}
	}
	return 0
}

// sanitizeMetaKey cleans a meta key to match [a-zA-Z0-9_]+.
// Hyphens become underscores; other invalid characters are dropped.
// The result is lowercased.
func sanitizeMetaKey(key string) string {
	var b strings.Builder
	b.Grow(len(key))
	for _, r := range key {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' {
			b.WriteRune(r)
		} else if r == '-' {
			b.WriteByte('_')
		}
	}
	result := b.String()
	result = strings.ToLower(result)
	return result
}
