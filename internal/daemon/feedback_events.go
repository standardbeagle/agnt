package daemon

import "sync"

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

// FeedbackHub fans FeedbackArrival events to per-project subscribers. Safe for
// concurrent use. A subscriber only ever sees events for the exact project path
// it subscribed with — the map key IS the isolation boundary.
type FeedbackHub struct {
	mu   sync.Mutex
	subs map[string]map[*FeedbackSub]struct{} // projectPath -> set of subscribers
}

// FeedbackSub is one subscription. C delivers arrival events for the subscribed
// project; it is buffered and drop-newest so a slow subscriber never blocks the
// public-plane POST path.
type FeedbackSub struct {
	projectPath string
	C           chan FeedbackArrival
}

// NewFeedbackHub returns an empty hub.
func NewFeedbackHub() *FeedbackHub {
	return &FeedbackHub{subs: make(map[string]map[*FeedbackSub]struct{})}
}

// Subscribe registers a subscriber for exactly one project path. An empty
// projectPath is rejected (returns nil): an unscoped subscription would be the
// cross-project leak this hub exists to prevent.
func (h *FeedbackHub) Subscribe(projectPath string) *FeedbackSub {
	if h == nil || projectPath == "" {
		return nil
	}
	sub := &FeedbackSub{projectPath: projectPath, C: make(chan FeedbackArrival, 16)}
	h.mu.Lock()
	defer h.mu.Unlock()
	set := h.subs[projectPath]
	if set == nil {
		set = make(map[*FeedbackSub]struct{})
		h.subs[projectPath] = set
	}
	set[sub] = struct{}{}
	return sub
}

// Unsubscribe removes a subscriber. Idempotent and nil-safe.
func (h *FeedbackHub) Unsubscribe(sub *FeedbackSub) {
	if h == nil || sub == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	set := h.subs[sub.projectPath]
	if set == nil {
		return
	}
	delete(set, sub)
	if len(set) == 0 {
		delete(h.subs, sub.projectPath)
	}
}

// Emit delivers ev to every subscriber of exactly projectPath — and no other
// project. An empty projectPath is dropped: without a resolvable owner the event
// has no scope, and delivering it anywhere would bleed across projects. Delivery
// is non-blocking (drop-newest) so the public POST path never stalls on a slow
// agent.
func (h *FeedbackHub) Emit(projectPath string, ev FeedbackArrival) {
	if h == nil || projectPath == "" {
		return
	}
	h.mu.Lock()
	subs := make([]*FeedbackSub, 0, len(h.subs[projectPath]))
	for sub := range h.subs[projectPath] {
		subs = append(subs, sub)
	}
	h.mu.Unlock()
	for _, sub := range subs {
		select {
		case sub.C <- ev:
		default:
			// Drop-newest: a saturated subscriber loses this event rather than
			// blocking ingest. The counts on the next arrival are cumulative, so
			// a dropped notice self-heals.
		}
	}
}
