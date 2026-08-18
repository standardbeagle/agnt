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
	// Location is the source position the incident points at, as
	// file:line:col. Adapters that can identify one (the browser adapter reads
	// the first app stack frame) set it; it is carried as a first-class field
	// rather than left to survive inside Summary, which truncates at 200 bytes
	// and routinely cuts the app frame off behind framework frames.
	Location string
	// FrameID attributes a browser-sourced incident to the content frame that
	// raised it. Under the always-wrap model each content frame is a distinct
	// failing surface (docs/responsive-canonical-target.md §5.2/§6.2), so it is
	// also folded into the fingerprint: the same error in two frames is two
	// failures, not one.
	FrameID string
	PID     int
	PGID    int
	Port    int
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
// Summary is capped at maxSummaryBytes; any message longer than that has its
// full bytes preserved in BlobStore via PayloadRef, so a truncated Summary
// tail is always retrievable via detail:"full".
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
// full payload. Whenever the message is longer than the Summary can hold
// (maxSummaryBytes) its full bytes are preserved — stored in store (if
// non-nil) and referenced via PayloadRef, or kept privately for MPSCBus to
// spill into the destination session's store. This upholds the invariant that
// truncation never discards bytes that no blob holds: without it, messages in
// the 200<len<=1024 "dead band" lost their tail with nothing to hydrate from.
func NewIncidentEvent(src Source, sev Severity, category, msg string, ctx Context, store *BlobStore) IncidentEvent {
	canonical := Canonicalize(msg)
	fp := computeFingerprint(string(src), category, canonical, fingerprintLocation(ctx))

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

	// The blob threshold is the Summary cap, not an arbitrary 1KB: the moment
	// the message is longer than Summary can hold, the truncated tail must be
	// recoverable, or bytes are lost with no blob to hydrate from (the dead-band
	// regression). Messages that fit entirely in Summary need no blob.
	if store != nil && len(msg) > maxSummaryBytes {
		ref, err := store.Write([]byte(msg), "text/plain")
		if err == nil {
			ev.PayloadRef = &ref
		} else {
			// Contract #6: blob store is best-effort. On a write failure the
			// envelope simply carries no PayloadRef (callers handle nil refs);
			// log so a failing store is diagnosable.
			debug.Log("incident-blob", "payload write failed, event carries no blob ref: %v", err)
		}
	} else if store == nil && len(msg) > maxSummaryBytes {
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

// fingerprintLocation is the location component of the fingerprint: the page
// URL, qualified by the emitting content frame when one is known.
//
// The frame is appended rather than added as a separate hash component so that
// an incident with no frame identity fingerprints exactly as before — only
// frame-attributed sources move. Folding it in at all is the point: without it
// the same error raised in two distinct content frames collapses to one entry
// upstream of the inbox, and the second failing surface no longer exists to be
// labelled by any downstream field.
func fingerprintLocation(ctx Context) string {
	if ctx.FrameID == "" {
		return ctx.URL
	}
	return ctx.URL + "#" + ctx.FrameID
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
