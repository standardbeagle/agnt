package daemon

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newSubVerbTestDaemon builds a daemon whose hub has every agnt command
// registered, without starting the production socket listener.
func newSubVerbTestDaemon(t *testing.T) *Daemon {
	t.Helper()
	return NewForTest(t, DaemonConfig{SocketPath: filepath.Join(t.TempDir(), "d.sock")})
}

// TestRouterActionsAreRegisteredSubVerbs pins the invariant that makes
// AUTOSTART RECONCILE and OVERLAY FORWARDING impossible to break again: every
// action a router dispatches on must also be registered as a sub-verb of its
// verb.
//
// The parser only lifts a registered token into cmd.SubVerb. An unregistered
// action leaves the token in Args, so the router sees SubVerb == "" and either
// answers "unknown action" or — worse, when the verb has a "" default alias —
// silently runs a different action than the caller asked for.
func TestRouterActionsAreRegisteredSubVerbs(t *testing.T) {
	d := newSubVerbTestDaemon(t)

	routers := map[string]map[string]handlerFn{
		"PROC":         d.procActions(),
		"PROXY":        d.proxyActions(),
		"PROXYLOG":     d.proxyLogActions(),
		"CURRENTPAGE":  d.currentPageActions(),
		"OVERLAY":      d.overlayActions(),
		"TUNNEL":       d.tunnelActions(),
		"BROWSER":      d.browserActions(),
		"AUTOMATION":   d.automationActions(),
		"CHAOS":        d.chaosActions(),
		"SESSION":      d.sessionActions(),
		"SESSION-HOST": d.sessionHostActions(),
		"STORE":        d.storeActions(),
		"AUTOMATE":     d.automateActions(),
		"ALERTS":       d.alertsActions(),
		"INCIDENTS":    d.incidentsActions(),
		"PORTS":        d.portsActions(),
		"SCRIPT":       d.scriptActions(),
		"AUTOSTART":    d.autostartActions(),
	}

	for verb, actions := range routers {
		registered := d.hub.ValidSubVerbs(verb)
		require.NotEmpty(t, registered, "verb %s registered no sub-verbs", verb)
		for _, action := range routerSubVerbs(actions) {
			assert.Contains(t, registered, action,
				"%s %s is routable but not a registered sub-verb: the parser will leave it in Args and the router will dispatch the wrong action", verb, action)
		}
	}
}

// TestAutostartReconcileIsRoutable is the regression for the specific bug: the
// live `.agnt.kdl` reconcile verb dispatched to "unknown action" because
// RECONCILE was missing from AUTOSTART's registered sub-verbs.
func TestAutostartReconcileIsRoutable(t *testing.T) {
	d := newSubVerbTestDaemon(t)
	assert.Contains(t, d.hub.ValidSubVerbs("AUTOSTART"), "RECONCILE")
}

// TestOverlayForwardingIsRoutable is the regression for the sibling bug:
// OVERLAY FORWARDING was unregistered, so it fell through to OVERLAY's ""
// default alias (GET) and reported an overlay endpoint instead of pausing
// agent-inbound push.
func TestOverlayForwardingIsRoutable(t *testing.T) {
	d := newSubVerbTestDaemon(t)
	assert.Contains(t, d.hub.ValidSubVerbs("OVERLAY"), "FORWARDING")
}

// TestRouterSubVerbsSkipsDefaultAlias guards the helper that both registration
// and the "unknown action" error message read from: the "" default alias is an
// internal routing entry, not a sub-verb a caller may name.
func TestRouterSubVerbsSkipsDefaultAlias(t *testing.T) {
	var noop handlerFn
	got := routerSubVerbs(map[string]handlerFn{"": noop, "QUERY": noop, "CLEAR": noop})
	assert.Equal(t, []string{"CLEAR", "QUERY"}, got, "sorted, without the \"\" alias")
	assert.Empty(t, routerSubVerbs(map[string]handlerFn{"": noop}))
	assert.Empty(t, routerSubVerbs(nil))
}
