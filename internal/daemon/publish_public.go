package daemon

import (
	"fmt"
	"net"
	"net/http"

	"github.com/standardbeagle/agnt/internal/config"
	"github.com/standardbeagle/agnt/internal/debug"
	"github.com/standardbeagle/agnt/internal/proxy"
	"github.com/standardbeagle/agnt/internal/publish"
)

// publish_public.go owns the P9 deferred wiring: it constructs the durable
// feedback store, builds the anonymous-viewer public plane (P7 NewPublicHandler)
// over the publish store's token verifier + a feedback sink that BOTH persists
// and emits a project-scoped arrival event, and stands up the dedicated public
// listener. The dev control surface is never registered on this handler, so it
// is structurally unreachable on the public plane (INV-1/INV-2).

// feedbackArrivalSink adapts the durable publish.FeedbackStore to the
// proxy.FeedbackSink the public handler wires in, and — on a SUCCESSFUL append —
// emits a counts-only, project-scoped arrival event. It is the single point
// where the scope-free public POST path (INV-1: a token maps to a revision, not
// a project) is re-associated with the owning project, using the publish store
// as the authoritative shareID→project map. No token or body ever leaves here on
// the event (INV-9/INV-7).
type feedbackArrivalSink struct {
	store *publish.FeedbackStore
	hub   *FeedbackHub
	// ownerOf resolves a viewer-safe share id to its owning project path (and
	// whether the share still exists). Backed by the publish store.
	ownerOf func(shareID string) (projectPath string, ok bool)
}

// Accept persists the feedback (P8 semantics: rate-limit, validate, append) and,
// only on success, emits the scoped arrival event. A rate-limited/invalid write
// returns the store error unchanged (the public handler maps it to 429/413/422)
// and emits NOTHING — but the drop is still counted in store.Dropped(), so the
// NEXT successful arrival's event carries the updated dropped total.
func (s *feedbackArrivalSink) Accept(shareID, revisionID, remoteAddr string, body []byte) error {
	if err := s.store.Accept(shareID, revisionID, remoteAddr, body); err != nil {
		return err
	}
	// Success: re-scope to the owning project and fan out a counts-only event.
	// A share that vanished between verify and here yields no owner → no event
	// (never a fallback broadcast, which would bleed across projects).
	projectPath, ok := s.ownerOf(shareID)
	if !ok {
		return nil
	}
	s.hub.Emit(projectPath, FeedbackArrival{
		ShareID:     shareID,
		RevisionID:  revisionID,
		Total:       s.store.Count(shareID),
		Dropped:     s.store.Dropped(),
		Remediation: feedbackRemediationHint,
	})
	return nil
}

// buildPublicPlane constructs the feedback store and the public handler. It is
// called from bootstrap after the publish store is opened. A feedback-store load
// failure is surfaced loud (Silent Failure Prohibition) and leaves feedbackStore
// nil — the public handler then runs P7's safe accept-and-drop stub and the
// control read returns empty, rather than silently serving a half-loaded store.
func (d *Daemon) buildPublicPlane() {
	feedbackDir := d.config.FeedbackDir
	if feedbackDir == "" {
		feedbackDir = DefaultFeedbackDir()
	}
	limits := feedbackLimitsFromConfig(d.config.FeedbackLimits)

	if store, err := publish.NewFeedbackStore(feedbackDir, limits, nil); err != nil {
		d.daemonStartupLog("error", "feedback_store_load_failed",
			fmt.Sprintf("feedback store failed to load (public feedback drops until resolved): %v", err))
	} else {
		d.feedbackStore = store
		d.daemonStartupLog("info", "feedback_store_loaded", "feedback store loaded")
	}

	// The token verifier is always the publish store (nil until it loads). The
	// public handler tolerates a nil sink (safe accept-and-drop); it does NOT
	// tolerate a nil verifier, so skip the handler when the publish store failed
	// to load — there is nothing to verify tokens against.
	if d.publishStore == nil {
		d.daemonStartupLog("warning", "public_plane_skipped",
			"public plane not built: publish store unavailable")
		return
	}

	var sink proxy.FeedbackSink
	if d.feedbackStore != nil {
		sink = &feedbackArrivalSink{
			store:   d.feedbackStore,
			hub:     d.feedbackHub,
			ownerOf: d.projectOfShare,
		}
	}
	// Use the NORMALIZED body cap (same source as the store limiter above) so an
	// unset field falls back to the spec §5 default rather than 0, and an operator
	// value flows through unchanged. Keeps the handler cap and the store cap from
	// diverging.
	d.publicHandler = proxy.NewPublicHandler(d.publishStore, sink, limits.MaxBodyBytes)
}

// projectOfShare resolves a viewer-safe share id to its owning project path via
// the authoritative publish store. ok is false for an unknown share.
func (d *Daemon) projectOfShare(shareID string) (string, bool) {
	if d.publishStore == nil {
		return "", false
	}
	info, err := d.publishStore.Status(shareID)
	if err != nil {
		return "", false
	}
	return info.ProjectPath, true
}

// PublicHandler returns the anonymous-viewer public plane handler (P7). It is
// the security boundary: only the §2b token-gated routes exist on it, and the
// dev control surface is structurally absent. Returns nil before bootstrap or if
// the publish store failed to load. Exposed so a listener (production or a
// httptest harness) can serve it.
func (d *Daemon) PublicHandler() http.Handler {
	if d.publicHandler == nil {
		return nil
	}
	return d.publicHandler
}

// PublicPlaneAddr returns the bound address of the dedicated public listener, or
// "" if no public listener is running. Resolved after Listen, so a :0 ephemeral
// port is observable by tests.
func (d *Daemon) PublicPlaneAddr() string {
	if p := d.publicAddr.Load(); p != nil {
		return *p
	}
	return ""
}

// startPublicListener stands up the dedicated public-plane HTTP listener when
// config.PublicListenAddr is set and a handler was built. A bind failure is
// surfaced loud but is non-fatal to daemon startup — the control plane and dev
// proxy stay up; only the public plane is unavailable. Called from bootstrap so
// both Start() and the test harness bring the plane up identically.
func (d *Daemon) startPublicListener() {
	if d.config.PublicListenAddr == "" || d.publicHandler == nil {
		return
	}
	ln, err := net.Listen("tcp", d.config.PublicListenAddr)
	if err != nil {
		d.daemonStartupLog("error", "public_listener_bind_failed",
			fmt.Sprintf("public plane listener failed to bind %s: %v", d.config.PublicListenAddr, err))
		return
	}
	addr := ln.Addr().String()
	d.publicAddr.Store(&addr)
	srv := &http.Server{Handler: d.publicHandler}
	d.publicServer = srv
	d.goTracked(func() {
		if serveErr := srv.Serve(ln); serveErr != nil && serveErr != http.ErrServerClosed {
			debug.Warn("daemon", "public plane server exited: %v", serveErr)
		}
	})
	d.daemonStartupLog("info", "public_listener_started",
		fmt.Sprintf("public plane listening on %s", addr))
}

// feedbackLimitsFromConfig converts the normalized config.FeedbackConfig into
// the package-publish FeedbackLimits (keeps package publish free of a config
// import). Normalize fills any zero field with its spec default.
func feedbackLimitsFromConfig(c config.FeedbackConfig) publish.FeedbackLimits {
	n := c.Normalize()
	return publish.FeedbackLimits{
		RatePerMinute:   n.RatePerMinute,
		Burst:           n.Burst,
		MaxBodyBytes:    n.MaxBodyBytes,
		MaxRowsPerShare: n.MaxRowsPerShare,
		RetentionDays:   n.RetentionDays,
	}
}
