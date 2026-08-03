package tools

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateStringLen(t *testing.T) {
	assert.NoError(t, validateStringLen("field", "", 100))
	assert.NoError(t, validateStringLen("field", "short", 100))
	assert.NoError(t, validateStringLen("field", strings.Repeat("a", 100), 100))
	assert.Error(t, validateStringLen("field", strings.Repeat("a", 101), 100))

	err := validateStringLen("my_field", strings.Repeat("x", 200), 100)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "my_field")
	assert.Contains(t, err.Error(), "200 bytes")
	assert.Contains(t, err.Error(), "max 100")
}

func TestValidateArrayLen(t *testing.T) {
	assert.NoError(t, validateArrayLen("items", []string{}, 10))
	assert.NoError(t, validateArrayLen("items", []string{"a", "b"}, 10))
	assert.NoError(t, validateArrayLen("items", make([]string, 10), 10))
	assert.Error(t, validateArrayLen("items", make([]string, 11), 10))

	err := validateArrayLen("args", make([]int, 50), 20)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "args")
	assert.Contains(t, err.Error(), "50")
	assert.Contains(t, err.Error(), "max 20")
}

func TestValidatePort(t *testing.T) {
	assert.NoError(t, validatePort("port", 0))
	assert.NoError(t, validatePort("port", 80))
	assert.NoError(t, validatePort("port", 65535))
	assert.Error(t, validatePort("port", -1))
	assert.Error(t, validatePort("port", 65536))
}

func TestValidatePositiveLimit(t *testing.T) {
	assert.NoError(t, validatePositiveLimit("limit", 0, 100))
	assert.NoError(t, validatePositiveLimit("limit", 50, 100))
	assert.NoError(t, validatePositiveLimit("limit", 100, 100))
	assert.Error(t, validatePositiveLimit("limit", -1, 100))
	assert.Error(t, validatePositiveLimit("limit", 101, 100))
}

func TestValidateRunInput(t *testing.T) {
	// Normal input passes
	assert.NoError(t, validateRunInput(RunInput{
		Path:       "/home/user/project",
		ScriptName: "test",
		Command:    "go",
		Args:       []string{"test", "./..."},
		ID:         "test-run",
		Mode:       RunModeBackground,
	}))

	// Oversized path
	assert.Error(t, validateRunInput(RunInput{
		Path: strings.Repeat("/a", maxPathLength),
	}))

	// Too many args
	assert.Error(t, validateRunInput(RunInput{
		Args: make([]string, maxArrayElements+1),
	}))

	// Oversized individual arg
	assert.Error(t, validateRunInput(RunInput{
		Args: []string{strings.Repeat("x", maxPathLength+1)},
	}))
}

func TestValidateProcInput(t *testing.T) {
	// Normal input passes
	assert.NoError(t, validateProcInput(ProcInput{
		Action:    "status",
		ProcessID: "dev-server",
		Tail:      100,
		Port:      3000,
	}))

	// Oversized grep pattern
	assert.Error(t, validateProcInput(ProcInput{
		Grep: strings.Repeat("x", maxGrepPattern+1),
	}))

	// Invalid port
	assert.Error(t, validateProcInput(ProcInput{
		Port: 70000,
	}))

	// Negative tail
	assert.Error(t, validateProcInput(ProcInput{
		Tail: -1,
	}))
}

func TestValidateProxyInput(t *testing.T) {
	// Normal input passes
	assert.NoError(t, validateProxyInput(ProxyInput{
		Action:    "start",
		ID:        "dev",
		TargetURL: "http://localhost:3000",
		Port:      12345,
	}))

	// Oversized JS code
	assert.Error(t, validateProxyInput(ProxyInput{
		Code: strings.Repeat("x", maxCodeLength+1),
	}))

	// Too many chaos rules
	assert.Error(t, validateProxyInput(ProxyInput{
		ChaosRules: make([]ChaosRuleInput, maxChaosRules+1),
	}))

	// Invalid chaos rule within input
	assert.Error(t, validateProxyInput(ProxyInput{
		ChaosRule: &ChaosRuleInput{
			ID: strings.Repeat("x", maxIDLength+1),
		},
	}))

	// Oversized toast message
	assert.Error(t, validateProxyInput(ProxyInput{
		ToastMessage: strings.Repeat("x", maxMessageLength+1),
	}))
}

func TestValidateProxyLogInput(t *testing.T) {
	// Normal input passes
	assert.NoError(t, validateProxyLogInput(ProxyLogInput{
		ProxyID: "dev",
		Action:  "query",
		Types:   []string{"http", "error"},
		Limit:   100,
	}))

	// Oversized limit
	assert.Error(t, validateProxyLogInput(ProxyLogInput{
		Limit: maxLimit + 1,
	}))

	// Too many types
	assert.Error(t, validateProxyLogInput(ProxyLogInput{
		Types: make([]string, maxArrayElements+1),
	}))

	// Unknown log type is rejected loudly rather than silently matching nothing.
	err := validateProxyLogInput(ProxyLogInput{ProxyID: "dev", Types: []string{"http", "bogus"}})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "bogus")

	// Newer internal stream types are accepted.
	assert.NoError(t, validateProxyLogInput(ProxyLogInput{
		ProxyID: "dev",
		Types:   []string{"diagnostic", "walkthrough", "hook", "incident_digest"},
	}))

	// New filter fields validate their bounds.
	assert.Error(t, validateProxyLogInput(ProxyLogInput{
		ProxyID:        "dev",
		MessagePattern: strings.Repeat("x", maxStringField+1),
	}))
	assert.Error(t, validateProxyLogInput(ProxyLogInput{
		ProxyID:          "dev",
		InteractionTypes: make([]string, maxArrayElements+1),
	}))
}

func TestValidateGetIncidentsInput(t *testing.T) {
	// Normal input passes
	assert.NoError(t, validateGetIncidentsInput(GetIncidentsInput{
		ProcessID: "dev",
		ProxyID:   "proxy-dev",
		Since:     "5m",
		Detail:    "full",
		Limit:     25,
	}))

	// Oversized process_id
	assert.Error(t, validateGetIncidentsInput(GetIncidentsInput{
		ProcessID: strings.Repeat("x", maxIDLength+1),
	}))

	// Oversized retention fields — the pin target and its note are caller-supplied.
	assert.Error(t, validateGetIncidentsInput(GetIncidentsInput{
		Action:  "pin",
		ErrorID: strings.Repeat("x", maxIDLength+1),
	}))
	assert.Error(t, validateGetIncidentsInput(GetIncidentsInput{
		Action: "pin",
		Tag:    strings.Repeat("x", maxStringField+1),
	}))

	// Negative limit
	assert.Error(t, validateGetIncidentsInput(GetIncidentsInput{
		Limit: -1,
	}))
}

func TestValidateSnapshotInput(t *testing.T) {
	// Normal input passes
	assert.NoError(t, validateSnapshotInput(SnapshotInput{
		Action:   "baseline",
		Name:     "before-refactor",
		Baseline: "v1",
	}))

	// Oversized name
	assert.Error(t, validateSnapshotInput(SnapshotInput{
		Name: strings.Repeat("x", maxIDLength+1),
	}))
}

func TestValidateBrowserInput(t *testing.T) {
	// Normal input passes
	assert.NoError(t, validateBrowserInput(BrowserInput{
		Action:  "start",
		ID:      "chrome-1",
		URL:     "http://localhost:3000",
		ProxyID: "dev",
	}))

	// Oversized URL
	assert.Error(t, validateBrowserInput(BrowserInput{
		URL: strings.Repeat("x", maxURLLength+1),
	}))
}

func TestValidateAutomationInput(t *testing.T) {
	// Normal input passes
	assert.NoError(t, validateAutomationInput(AutomationInput{
		Action:    "evaluate",
		SessionID: "sess-1",
		Script:    "document.title",
	}))

	// Oversized script
	assert.Error(t, validateAutomationInput(AutomationInput{
		Script: strings.Repeat("x", maxCodeLength+1),
	}))
}

func TestValidateTunnelInput(t *testing.T) {
	// Normal input passes
	assert.NoError(t, validateTunnelInput(TunnelInput{
		Action:    "start",
		ID:        "dev-tunnel",
		Provider:  "cloudflare",
		LocalPort: 3000,
	}))

	// Invalid port
	assert.Error(t, validateTunnelInput(TunnelInput{
		LocalPort: 70000,
	}))
}

func TestValidateDaemonInput(t *testing.T) {
	assert.NoError(t, validateDaemonInput(DaemonInput{Action: "status"}))
	assert.Error(t, validateDaemonInput(DaemonInput{
		Action: strings.Repeat("x", maxIDLength+1),
	}))
}

func TestValidateSessionInput(t *testing.T) {
	// Normal input passes
	assert.NoError(t, validateSessionInput(SessionInput{
		Action:  "send",
		Code:    "abc123",
		Message: "hello world",
	}))

	// Oversized message
	assert.Error(t, validateSessionInput(SessionInput{
		Message: strings.Repeat("x", maxMessageLength+1),
	}))
}

func TestValidateStoreInput(t *testing.T) {
	// Normal input passes
	assert.NoError(t, validateStoreInput(StoreInput{
		Action:   "set",
		Scope:    "global",
		Key:      "theme",
		Metadata: map[string]any{"tag": "v1"},
	}))

	// Oversized key
	assert.Error(t, validateStoreInput(StoreInput{
		Key: strings.Repeat("x", maxStringField+1),
	}))

	// Too many metadata entries
	bigMeta := make(map[string]any, maxArrayElements+1)
	for i := 0; i <= maxArrayElements; i++ {
		bigMeta[strings.Repeat("k", 5)+string(rune('a'+i%26))+string(rune('0'+i/26))] = i
	}
	assert.Error(t, validateStoreInput(StoreInput{
		Metadata: bigMeta,
	}))
}

func TestValidateResponsiveAuditInputSize(t *testing.T) {
	// Normal input passes
	assert.NoError(t, validateResponsiveAuditInputSize(ResponsiveAuditInput{
		ProxyID: "dev",
		Checks:  []string{"layout", "overflow"},
		Timeout: 10000,
	}))

	// Oversized timeout
	assert.Error(t, validateResponsiveAuditInputSize(ResponsiveAuditInput{
		Timeout: maxTimeoutMs + 1,
	}))

	// Too many viewports
	assert.Error(t, validateResponsiveAuditInputSize(ResponsiveAuditInput{
		Viewports: make([]ViewportInput, maxArrayElements+1),
	}))
}

func TestValidateCurrentPageInput(t *testing.T) {
	// Normal input passes
	assert.NoError(t, validateCurrentPageInput(CurrentPageInput{
		ProxyID:   "dev",
		Action:    "list",
		SessionID: "page-1",
		Limit:     5,
	}))

	// Oversized session_id
	assert.Error(t, validateCurrentPageInput(CurrentPageInput{
		SessionID: strings.Repeat("x", maxIDLength+1),
	}))

	// Invalid limit
	assert.Error(t, validateCurrentPageInput(CurrentPageInput{
		Limit: maxLimit + 1,
	}))
}

func TestValidationError(t *testing.T) {
	msg := validationError("run", assert.AnError)
	assert.Contains(t, msg, "invalid run input")
}

func TestValidateChaosRuleInput(t *testing.T) {
	// Normal rule passes
	assert.NoError(t, validateChaosRuleInput("rule", ChaosRuleInput{
		ID:         "latency-1",
		Name:       "Add latency",
		Type:       "latency",
		URLPattern: "/api/*",
		Methods:    []string{"GET", "POST"},
	}))

	// Oversized URL pattern
	assert.Error(t, validateChaosRuleInput("rule", ChaosRuleInput{
		URLPattern: strings.Repeat("x", maxURLLength+1),
	}))

	// Too many methods
	assert.Error(t, validateChaosRuleInput("rule", ChaosRuleInput{
		Methods: make([]string, maxArrayElements+1),
	}))
}
