package incident

import (
	"crypto/sha256"
	"encoding/hex"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/standardbeagle/agnt/internal/debug"
)

// Source identifies where the incident originated.
type Source string

const (
	SourceBrowserJS     Source = "browser_js"
	SourceHTTP5xx       Source = "http_5xx"
	SourceHTTP4xx       Source = "http_4xx"
	SourceTransportErr  Source = "transport_err"
	SourceProxyDiag     Source = "proxy_diag"
	SourceProcessAlert  Source = "process_alert"
	SourceProcessOutput Source = "process_output"
	SourceProcessCrash  Source = "process_crash"
	SourceBuildFail     Source = "build_fail"
	SourcePortConflict  Source = "port_conflict"
	SourceShutdown      Source = "shutdown"
	SourceHookStopFail  Source = "hook_stop_failure"
	SourceChaosSwallow  Source = "chaos_swallowed_error"
)

// Severity orders incidents by urgency.
type Severity string

const (
	SeverityCritical Severity = "critical"
	SeverityError    Severity = "error"
	SeverityWarning  Severity = "warning"
	SeverityInfo     Severity = "info"
)

// Context carries identifiers for the resources involved in the incident.
type Context struct {
	ProcessID   string
	ProxyID     string
	SessionID   string
	ProjectPath string
	URL         string
	PID         int
	PGID        int
	Port        int
}

// Remediation hints which MCP tool and skill should address this incident.
// Populated by the L7 routing table; zero value means "no hint".
type Remediation struct {
	PrimaryTool  string         `json:"primary_tool,omitempty"`
	PrimaryArgs  map[string]any `json:"primary_args,omitempty"`
	FallbackTool string         `json:"fallback_tool,omitempty"`
	FallbackArgs map[string]any `json:"fallback_args,omitempty"`
	SkillHint    string         `json:"skill_hint,omitempty"`
}

// BlobRef points to a payload stored in a BlobStore.
type BlobRef struct {
	Hash string // sha256 hex of raw payload
	Size int
	MIME string // "text/plain", "application/json", "text/html"
}

// IncidentEvent is the canonical envelope for all 11 signal sources.
// ID is a time-ordered UUID v7 string — monotonic, sortable, cursor-friendly.
// Summary is capped at 200 bytes; oversized payloads live in BlobStore via PayloadRef.
type IncidentEvent struct {
	ID          string
	Fingerprint string // sha256(source|category|canonical_msg|location)[:16]
	Type        MessageType
	ReceivedAt  time.Time
	Source      Source
	Severity    Severity
	Category    string // finer-grained, e.g. "TypeError", "ECONNREFUSED"
	Summary     string // ≤200 bytes, single line
	PayloadRef  *BlobRef
	Ctx         Context
	Remediation Remediation
	// payload carries an oversized production message until the owning session
	// pipeline spills it into its bounded BlobStore. It is never serialized.
	payload []byte
}

const maxSummaryBytes = 200

// NewIncidentEvent builds an IncidentEvent from the raw message and optional
// full payload. If payload is non-nil and longer than 1KB it is stored in
// store (if non-nil) and referenced via PayloadRef; otherwise it is
// truncated into Summary.
func NewIncidentEvent(src Source, sev Severity, category, msg string, ctx Context, store *BlobStore) IncidentEvent {
	canonical := Canonicalize(msg)
	fp := computeFingerprint(string(src), category, canonical, ctx.URL)

	summary := truncateToBytes(msg, maxSummaryBytes)

	ev := IncidentEvent{
		ID:          newID(),
		Fingerprint: fp,
		Type:        MessageError,
		ReceivedAt:  time.Now(),
		Source:      src,
		Severity:    sev,
		Category:    category,
		Summary:     summary,
		Ctx:         ctx,
	}

	if store != nil && len(msg) > 1024 {
		ref, err := store.Write([]byte(msg), "text/plain")
		if err == nil {
			ev.PayloadRef = &ref
		} else {
			// Contract #6: blob store is best-effort. On a write failure the
			// envelope simply carries no PayloadRef (callers handle nil refs);
			// log so a failing store is diagnosable.
			debug.Log("incident-blob", "payload write failed, event carries no blob ref: %v", err)
		}
	} else if store == nil && len(msg) > 1024 {
		// Production adapters do not own session stores. Preserve the full bytes
		// privately so MPSCBus can spill them into the destination session's store.
		ev.payload = []byte(msg)
	}

	return ev
}

// truncateToBytes returns s clipped to at most maxBytes bytes without splitting
// a multi-byte UTF-8 rune. If the byte at the cap falls mid-rune, it walks back
// to the preceding rune boundary so the result stays valid UTF-8.
func truncateToBytes(s string, maxBytes int) string {
	if maxBytes <= 0 || len(s) <= maxBytes {
		return s
	}
	b := s[:maxBytes]
	for len(b) > 0 && !utf8.ValidString(b) {
		b = b[:len(b)-1]
	}
	return b
}

func newID() string {
	id, err := uuid.NewV7()
	if err != nil {
		// Fall back to random UUID on entropy exhaustion (should not happen).
		return uuid.NewString()
	}
	return id.String()
}

func computeFingerprint(source, category, canonMsg, location string) string {
	h := sha256.Sum256([]byte(source + "|" + category + "|" + canonMsg + "|" + location))
	return hex.EncodeToString(h[:])[:16]
}

// computeStormFingerprint produces a URL-independent fingerprint so that a flood
// of same-class errors from one proxy (e.g. a down dependency returning 5xx on
// many endpoints) collapses into a single inbox entry. proxyID is folded in so
// two proxies' floods stay distinct.
func computeStormFingerprint(source, statusClass, proxyID string) string {
	h := sha256.Sum256([]byte("storm|" + source + "|" + statusClass + "|" + proxyID))
	return hex.EncodeToString(h[:])[:16]
}
