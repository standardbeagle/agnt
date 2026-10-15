package scripts

import (
	"os"
	"strings"
	"testing"
)

// The tests in this file are the always-on Go source guards for behaviour that
// is PROVEN in the vitest + jsdom tier (internal/proxy/scripts/jstest, run by
// `make test-js`). They need no node install, so `make test` keeps the contract
// even where the JS tier is skipped. Each names the JS test that proves it.

func mustContain(t *testing.T, name, src string, wants ...string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(src, want) {
			t.Errorf("%s: missing %q", name, want)
		}
	}
}

func mustNotContain(t *testing.T, name, src string, bans ...string) {
	t.Helper()
	for _, ban := range bans {
		if strings.Contains(src, ban) {
			t.Errorf("%s: must not contain %q", name, ban)
		}
	}
}

// TestInteraction_KeystrokesAreMasked — behaviour: jstest/interaction.test.js
// ("keystroke capture"). A keydown record must be built by keyInfoFor, which
// withholds the key for password fields and masks typed characters in any
// editable target; the raw event.key must not be copied anywhere else.
func TestInteraction_KeystrokesAreMasked(t *testing.T) {
	mustContain(t, "interaction.js", interactionJS,
		"function keyTargetKind(el)",
		"function keyInfoFor(event, target)",
		"interaction.key = keyInfoFor(event, target);",
		"if (type === 'password') return 'password';",
		"return { key: '', code: '', masked: true };",
	)
	if got := strings.Count(interactionJS, "event.key || ''"); got != 1 {
		t.Errorf("interaction.js reads event.key at %d sites, want exactly 1 (inside keyInfoFor)", got)
	}
	if got := strings.Count(interactionJS, "event.code || ''"); got != 1 {
		t.Errorf("interaction.js reads event.code at %d sites, want exactly 1 (inside keyInfoFor)", got)
	}
}

// TestInteraction_TargetsTextAndQueueAreBounded — behaviour:
// jstest/interaction.test.js ("click targets", "text capture", "pending send
// queue").
func TestInteraction_TargetsTextAndQueueAreBounded(t *testing.T) {
	// innerText forces layout and serialises the subtree; scroll and mousemove
	// used to read it for the document root many times a second.
	mustNotContain(t, "interaction.js", interactionJS, ".innerText")
	mustContain(t, "interaction.js", interactionJS,
		"function boundedText(el, max)",
		"function resolveTarget(el)",
		"}, { skipText: true });",
		"getTargetInfo(target, { skipText: true })",
		"var MAX_PENDING_BATCH = 300;",
		"queueBatchRecord(interaction);",
	)
	mustNotContain(t, "interaction.js", interactionJS, "pendingBatch.push(interaction)")
}

// TestMutation_GatedOnFrameRole — mutation.js must apply the same
// content-frame gate interaction.js does, so the full bundle does not run
// observers and timers in passive foreign frames.
func TestMutation_GatedOnFrameRole(t *testing.T) {
	mustContain(t, "mutation.js", mutationJS,
		"function isTelemetryFrame()",
		"if (observer || disposed || !isTelemetryFrame()) return;",
		"if (spinnerObserver || disposed || !isTelemetryFrame()) return;",
		"if (disposed || rateCheckInterval || !isTelemetryFrame()) return;",
	)
}

// TestAPI_CarriesForwardEarlierRegistrations — behaviour:
// jstest/bundle-namespace.test.js. api.js replaces window.__devtool and must
// carry forward what frames.js and the audit modules registered on it.
func TestAPI_CarriesForwardEarlierRegistrations(t *testing.T) {
	mustContain(t, "api.js", apiJS,
		"var priorDevtool = window.__devtool;",
		"window.__devtool[priorKey] = priorDevtool[priorKey];",
	)
	prior := strings.Index(apiJS, "var priorDevtool = window.__devtool;")
	assign := strings.Index(apiJS, "window.__devtool = {")
	if prior < 0 || assign < 0 || prior > assign {
		t.Error("api.js must capture the prior window.__devtool BEFORE replacing it")
	}
	// The modules whose registrations depend on it.
	mustContain(t, "audit-animations.js", auditAnimationsJS, "window.__devtool.audit.auditAnimations = auditAnimations;")
	mustContain(t, "audit-api.js", auditApiJS, "window.__devtool.audit.auditAPIEfficiency = auditAPIEfficiency;")
	mustContain(t, "responsive.js", responsiveJS, "window.__devtool.audit.highlight = highlight;")
	if moduleOrder[len(moduleOrder)-1].name != "api" {
		t.Error("api must stay the last module: it is the one that replaces window.__devtool")
	}
}

// TestAPI_DiagnosticsFallbackTestsARealMethod — behaviour:
// jstest/core-transport.test.js ("transport diagnostics global"). The chrome
// bundle has no diagnostics.js; __devtool.diagnostics must then be the "not
// loaded" stubs, which requires testing for a method, not truthiness.
func TestAPI_DiagnosticsFallbackTestsARealMethod(t *testing.T) {
	mustContain(t, "api.js", apiJS,
		"typeof diagnostics.outlineAll !== 'function'",
		"var transportDiag = window.__devtool_transport_diag;",
		"transport: transportDiag || {",
	)
}

// TestCore_ConsoleCaptureFormatsErrors — behaviour:
// jstest/core-transport.test.js ("console capture").
func TestCore_ConsoleCaptureFormatsErrors(t *testing.T) {
	mustContain(t, "core.js", coreJS,
		"function describeError(err)",
		"function formatConsoleArgs(args)",
		"var MAX_CONSOLE_ARG_CHARS = 2000;",
		"var MAX_CONSOLE_ARG_NODES = 500;",
	)
	if got := strings.Count(coreJS, "var formatted = formatConsoleArgs(arguments);"); got != 2 {
		t.Errorf("core.js formats console arguments at %d sites, want 2 (console.error and console.warn)", got)
	}
	// The old per-argument formatter: JSON.stringify(arg) of an Error is "{}".
	mustNotContain(t, "core.js", coreJS, "return JSON.stringify(arg);")
}

// TestAPITracker_NeverChangesRequestSemantics — behaviour:
// jstest/api-tracker.test.js.
func TestAPITracker_NeverChangesRequestSemantics(t *testing.T) {
	mustContain(t, "api-tracker.js", apiTrackerJS,
		"function fetchTarget(resource)",
		"new URL(String(url), window.location.href)",
		"if (typeof originalFetch === 'function') {",
		"var result = originalFetch.apply(this, arguments);",
		"return result;",
		"String(method).toUpperCase()",
	)
	mustNotContain(t, "api-tracker.js", apiTrackerJS,
		": resource.url;",
		"method.toUpperCase()",
		"window.location.origin)",
	)
}

// TestAPITracker_XHRListenerInstalledOncePerObject — behaviour:
// jstest/api-tracker.test.js ("records each request on a reused XHR exactly
// once"). send() installs the loadend listener only on an XHR's first send and
// the listener reads the current request's metadata, so a reused XHR is not
// recorded once per earlier send.
func TestAPITracker_XHRListenerInstalledOncePerObject(t *testing.T) {
	mustContain(t, "api-tracker.js", apiTrackerJS,
		"if (!xhr.__devtool_api_listening) {",
		"xhr.__devtool_api_listening = true;",
		"var meta = xhr.__devtool_api;",
	)
	if got := strings.Count(apiTrackerJS, "addEventListener('loadend'"); got != 1 {
		t.Errorf("api-tracker.js adds a loadend listener at %d sites, want 1 (guarded by __devtool_api_listening)", got)
	}
}

// TestPanels_EscapePageDerivedStrings — behaviour: jstest/html-sinks.test.js.
func TestPanels_EscapePageDerivedStrings(t *testing.T) {
	mustContain(t, "diagnostics.js", diagnosticsJS,
		"function escapeHtml(value)",
		"escapeHtml(style.fontFamily)",
		"escapeHtmlList(mItem.changes).join(', ')",
		"escapeHtmlList(item.path).join(' &gt; ')",
		"escapeHtml(item.color)",
	)
	mustNotContain(t, "diagnostics.js", diagnosticsJS,
		"' + style.fontFamily + '",
		"mItem.changes.join(",
		"item.path.join(",
	)
	mustContain(t, "indicator.js", indicatorJS,
		"escapeHtml(String(attachment.data.text).substring(0, 100))",
		"escapeHtml(attachment.data.selector || '')",
		"escapeHtml(c.original)",
		"escapeHtml(c.current)",
		"escapeHtml(attachment.summary || '')",
	)
	mustNotContain(t, "indicator.js", indicatorJS,
		"attachment.data.text.substring(0, 100) + '\"'",
		"c.property + ': ' + c.original",
	)
}

// TestVariantEngine_QuietReconcileAndBoundedUndos — behaviour:
// jstest/variant-engine.test.js.
func TestVariantEngine_QuietReconcileAndBoundedUndos(t *testing.T) {
	mustContain(t, "variant-engine.js", variantEngineJS,
		"function surfaceMissing(selectors, changed)",
		"surfaceMissing(missing, missingKey !== lastMissingKey);",
		"function pruneUndos()",
		"var UNDO_PRUNE_THRESHOLD = 256;",
	)
}

// TestFeedbackClient_EndpointValidatedByResolvedOrigin — behaviour:
// jstest/public-feedback.test.js ("rejects the cross-origin or relative
// endpoint"). publish-security-review-lessons.md §3: the validator must reject
// equivalent spellings, which only resolving the URL can do.
func TestFeedbackClient_EndpointValidatedByResolvedOrigin(t *testing.T) {
	mustContain(t, "feedback-client.js", feedbackClientJS,
		"var resolved = new URL(s, here.href);",
		"return resolved.origin === here.origin;",
		"code <= 0x20 || code === 0x7f || code === 0x5c",
	)
}

// TestRecorderModuleStaysDeleted — recorder.js was an unbundled, unreferenced
// module. It must not come back as a stray file that looks shipped.
func TestRecorderModuleStaysDeleted(t *testing.T) {
	if _, err := os.Stat("recorder.js"); err == nil {
		t.Error("recorder.js exists but is not in moduleOrder; bundle it or delete it")
	}
	for _, m := range moduleOrder {
		if _, ok := moduleScript[m.name]; !ok {
			t.Errorf("module %q is in moduleOrder but has no embedded script", m.name)
		}
	}
}
