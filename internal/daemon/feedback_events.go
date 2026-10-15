package daemon

import (
	"time"

	"github.com/standardbeagle/agnt/internal/proxy"
)

// feedback_events.go is the project-scoped fan-out for public-plane feedback
// ARRIVAL events (P9). When an anonymous viewer POSTs feedback on the public
// plane, the daemon-side sink (feedbackArrivalSink) resolves the owning project
// from the share and emits a COUNTS-ONLY event to that project's dev/agent
// subscribers — and ONLY that project's.
//
// # Why a dedicated hub and not the incident bus
//
// incident.MPSCBus.deliver fans every event to EVERY session pipeline
// regardless of project; project isolation there is a query/scope concern, not a
// delivery one. The spec forbids cross-project bleed of feedback (INV-1/§2a): a
// subscriber on project B must NEVER receive project A's feedback event. A
// purpose-built per-project notifier makes that isolation structural and
// trivially testable, rather than relying on a downstream filter to redact a
// leak after fan-out.
//
// # What the event carries (and never carries)
//
// FeedbackArrival is counts + a remediation hint ONLY. It carries NO share
// token (INV-9), NO feedback body, and NO cross-project identifier — a viewer's
// payload never rides the event, so there is no payload bleed surface.

// FeedbackArrival is the counts-only signal that new feedback landed for a share
// owned by a single project. It is deliberately free of the token and the
// feedback body: an agent learns THAT feedback arrived and how much, then reads
// the rows through the owner-scoped `publish feedback` MCP surface.
type FeedbackArrival struct {
	// ShareID is the viewer-safe share id (never the token — INV-9).
	ShareID string `json:"share_id"`
	// RevisionID is the immutable published-revision digest the feedback is
	// keyed to (spec §10).
	RevisionID string `json:"revision_id"`
	// Total is the share's stored feedback row count after this arrival.
	Total int `json:"total"`
	// Dropped is the cumulative rate-limit-shed count (spec §5 observability).
	Dropped int64 `json:"dropped"`
	// Remediation is a static, token-free hint pointing the agent at the read
	// surface. It never embeds project- or token-identifying data.
	Remediation string `json:"remediation"`
}

// feedbackRemediationHint is the fixed remediation string on every arrival
// event. Static by design: it must not embed the token, the project path, or any
// per-share secret (INV-9), only the tool the agent should call.
const feedbackRemediationHint = "run `publish feedback` with this share id to read the new rows"

// FeedbackHub emits FeedbackArrival events onto the owning project's
// STREAM-EVENTS stream. Safe for concurrent use: it holds no state of its own.
// The project stamp on each entry IS the isolation boundary.
type FeedbackHub struct {
	eventHub *EventHub
}

// NewFeedbackHub returns a hub that emits onto eventHub.
func NewFeedbackHub(eventHub *EventHub) *FeedbackHub {
	return &FeedbackHub{eventHub: eventHub}
}

// Emit delivers ev to the event stream of exactly projectPath — and no other
// project. An empty projectPath is dropped: without a resolvable owner the event
// has no scope, and delivering it anywhere would bleed across projects. Delivery
// is non-blocking (stream sinks drop on a full channel) so the public POST path
// never stalls on a slow agent.
func (h *FeedbackHub) Emit(projectPath string, ev FeedbackArrival) {
	if h == nil || projectPath == "" || h.eventHub == nil {
		return
	}
	// STREAM-EVENTS is the agent/developer transport. Stamp the owning project
	// explicitly so another project's subscriber cannot receive this proxy-less
	// event. Stream sink removal owns channel cleanup; this synchronous edge adds
	// no goroutine or subscription lifecycle of its own.
	h.eventHub.BroadcastLogEntryForProject(proxy.LogEntry{
		Type: proxy.LogTypeCustom,
		Custom: &proxy.CustomLog{
			ID:        "feedback_arrival",
			Timestamp: time.Now(),
			Level:     "info",
			Message:   ev.Remediation,
			Data: map[string]interface{}{
				"share_id":    ev.ShareID,
				"revision_id": ev.RevisionID,
				"total":       ev.Total,
				"dropped":     ev.Dropped,
			},
		},
	}, projectPath)
}
