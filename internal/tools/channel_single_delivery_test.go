package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/standardbeagle/agnt/internal/config"
	"github.com/standardbeagle/agnt/internal/incident"
	"github.com/standardbeagle/agnt/internal/proxy"
)

// transports is the agent-facing surface of one `agnt mcp` process: the channel
// notifications and the MCP log notifications it sent.
type transports struct {
	channel []channelNotification
	logs    []string
}

func (tr *transports) notify(_ context.Context, _ string, params any) error {
	if n, ok := params.(channelNotification); ok {
		tr.channel = append(tr.channel, n)
	}
	return nil
}

func (tr *transports) log(_ context.Context, _ string, message string) error {
	tr.logs = append(tr.logs, message)
	return nil
}

func (tr *transports) total() int { return len(tr.channel) + len(tr.logs) }

// digestEntry is the incident_digest stream entry the daemon broadcasts, after
// the JSON round trip the daemon socket puts it through.
func digestEntry(t *testing.T, payload incident.PingPayload) proxy.LogEntry {
	t.Helper()
	entry := proxy.LogEntry{
		Type: proxy.LogTypeIncidentDigest,
		Custom: &proxy.CustomLog{
			Level:   "error",
			Message: incident.CompactDigestText(payload),
			Data:    map[string]interface{}{incident.DigestPayloadKey: payload},
		},
	}
	wire, err := json.Marshal(entry)
	if err != nil {
		t.Fatal(err)
	}
	var out proxy.LogEntry
	if err := json.Unmarshal(wire, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func browserErrorEntry() proxy.LogEntry {
	return proxy.LogEntry{
		Type: proxy.LogTypeError,
		Error: &proxy.FrontendError{
			Message: "TypeError: Cannot read property 'map' of undefined",
			Stack:   "    at ProductList (src/components/List.tsx:42:15)",
			Source:  "src/components/List.tsx", LineNo: 42, ColNo: 15,
			URL: "http://localhost:3000/list",
		},
	}
}

func onePingPayload() incident.PingPayload {
	return incident.PingPayload{
		Type: "agnt.incident_ping", Version: 1, Session: "s",
		Summary: incident.PingStats{Error: 1, New: 1},
		Top: []incident.TopEntry{{
			Fingerprint: "41bd07f0aa11bb22", Category: "TypeError", Count: 1, Severity: incident.SeverityError,
			Summary: "TypeError: Cannot read property 'map' of undefined\n    at ProductList (src/components/List.tsx:42:15)",
		}},
	}
}

// One browser error reaches a channel-mode agent exactly once across both
// transports. It used to arrive twice: raw from the channel sink (bypassing
// every pipeline gate) and again as the incident digest.
func TestChannelMode_OneErrorOneNotification(t *testing.T) {
	var tr transports
	cfg := enabledChannelConfig()
	cfg.DedupeWindow = 2000
	sink := NewChannelSink(cfg, tr.notify)
	ctx := context.Background()

	// The two stream entries the daemon emits for one browser error: the raw
	// log entry (to every STREAM-EVENTS subscriber) and, through the incident
	// pipeline, one digest.
	sink.HandleEntry(ctx, browserErrorEntry())
	if tr.total() != 0 {
		t.Fatalf("the channel sink forwarded a pipeline-owned error raw: %+v", tr)
	}
	if !ForwardIncidentDigest(ctx, digestEntry(t, onePingPayload()), true, tr.log, tr.notify) {
		t.Fatal("digest was not delivered")
	}

	if tr.total() != 1 || len(tr.channel) != 1 {
		t.Fatalf("want exactly 1 notification, on the channel; got channel=%d log=%d", len(tr.channel), len(tr.logs))
	}
	got := tr.channel[0]
	if got.Meta["type"] != "incident_digest" || got.Meta["severity"] != "error" {
		t.Errorf("digest meta = %v", got.Meta)
	}
	// Channel mode's promise is error text in the conversation: the digest
	// carries what broke, not only a count, and stays one line per incident.
	for _, want := range []string{"err=1", "get_incidents", "[error:TypeError]", "Cannot read property 'map' of undefined", "41bd07f0aa11bb22"} {
		if !strings.Contains(got.Content, want) {
			t.Errorf("digest content missing %q:\n%s", want, got.Content)
		}
	}
	if strings.Count(got.Content, "\n") != 1 {
		t.Errorf("want the summary line plus one line per incident:\n%s", got.Content)
	}
}

// Every error-class entry the incident pipeline ingests is dropped by the
// channel sink; everything without an incident adapter still flows.
func TestChannelSink_ForwardsOnlyWhatThePipelineDoesNotCarry(t *testing.T) {
	cfg := enabledChannelConfig()
	cfg.Severity = "info"

	owned := map[string]proxy.LogEntry{
		"browser js error": browserErrorEntry(),
		"http 500":         {Type: proxy.LogTypeHTTP, HTTP: &proxy.HTTPLogEntry{Method: "GET", URL: "/a", StatusCode: 500}},
		"http 404":         {Type: proxy.LogTypeHTTP, HTTP: &proxy.HTTPLogEntry{Method: "GET", URL: "/a", StatusCode: 404}},
		"http 502 with transport error": {Type: proxy.LogTypeHTTP,
			HTTP: &proxy.HTTPLogEntry{Method: "GET", URL: "/a", StatusCode: 502, Error: "bad gateway"}},
		"error diagnostic": {Type: proxy.LogTypeDiagnostic,
			Diagnostic: &proxy.ProxyDiagnostic{Level: proxy.DiagnosticError, Message: "connection refused"}},
		"warning diagnostic": {Type: proxy.LogTypeDiagnostic,
			Diagnostic: &proxy.ProxyDiagnostic{Level: proxy.DiagnosticWarning, Message: "slow response"}},
	}
	for name, entry := range owned {
		var tr transports
		NewChannelSink(cfg, tr.notify).HandleEntry(context.Background(), entry)
		if tr.total() != 0 {
			t.Errorf("%s: pipeline-owned entry was forwarded raw: %+v", name, tr.channel)
		}
		if !pipelineOwned(entry) {
			t.Errorf("%s: pipelineOwned = false", name)
		}
	}

	kept := map[string]proxy.LogEntry{
		"panel message": {Type: proxy.LogTypePanelMessage, PanelMessage: &proxy.PanelMessage{Message: "make this button blue"}},
		"interaction":   {Type: proxy.LogTypeInteraction, Interaction: &proxy.InteractionEvent{EventType: "click"}},
		"responsive":    {Type: proxy.LogTypeResponsiveRequest, ResponsiveRequest: &proxy.ResponsiveRequest{Width: 390}},
		"custom log":    {Type: proxy.LogTypeCustom, Custom: &proxy.CustomLog{Level: "info", Message: "hello"}},
		"info diagnostic": {Type: proxy.LogTypeDiagnostic,
			Diagnostic: &proxy.ProxyDiagnostic{Level: proxy.DiagnosticInfo, Message: "connection established"}},
		"statusless transport failure": {Type: proxy.LogTypeHTTP,
			HTTP: &proxy.HTTPLogEntry{Method: "GET", URL: "/a", Error: "dial tcp: refused"}},
	}
	for name, entry := range kept {
		var tr transports
		NewChannelSink(cfg, tr.notify).HandleEntry(context.Background(), entry)
		if len(tr.channel) != 1 {
			t.Errorf("%s: want 1 raw channel event, got %d", name, len(tr.channel))
		}
	}
}

// A panel message is the browser→agent conversation: it arrives raw, on the
// HandleEntry call itself, and repeating it is not deduplicated away — while a
// machine-generated custom log inside the same window still is.
func TestChannelSink_UserActionsAreImmediateAndNeverDeduped(t *testing.T) {
	var tr transports
	cfg := enabledChannelConfig()
	cfg.Severity = "info"
	cfg.DedupeWindow = 2000
	sink := NewChannelSink(cfg, tr.notify)
	now := time.Unix(1_700_000_000, 0)
	sink.SetNowFunc(func() time.Time { return now })
	ctx := context.Background()

	panel := proxy.LogEntry{Type: proxy.LogTypePanelMessage,
		PanelMessage: &proxy.PanelMessage{Message: "make this button blue", URL: "http://localhost:3000/"}}
	sink.HandleEntry(ctx, panel)
	if len(tr.channel) != 1 {
		t.Fatalf("panel message must be delivered by the HandleEntry call itself, got %d", len(tr.channel))
	}
	if tr.channel[0].Content != "make this button blue" || tr.channel[0].Meta["type"] != "panel_message" {
		t.Errorf("panel message must arrive raw: %+v", tr.channel[0])
	}
	sink.HandleEntry(ctx, panel)
	sink.HandleEntry(ctx, panel)
	if len(tr.channel) != 3 {
		t.Fatalf("a repeated user action is deliberate; want 3 deliveries, got %d", len(tr.channel))
	}

	log := proxy.LogEntry{Type: proxy.LogTypeCustom, Custom: &proxy.CustomLog{Level: "info", Message: "render"}}
	sink.HandleEntry(ctx, log)
	sink.HandleEntry(ctx, log)
	if len(tr.channel) != 4 {
		t.Fatalf("a repeated machine-generated log inside the window is deduplicated; want 4 total, got %d", len(tr.channel))
	}
	now = now.Add(3 * time.Second)
	sink.HandleEntry(ctx, log)
	if len(tr.channel) != 5 {
		t.Fatalf("after the window the log is delivered again; want 5 total, got %d", len(tr.channel))
	}
}

// On the DEFAULT channel config (severity "warning") user actions are still
// delivered: they bypass the severity filter, which used to drop every panel
// message and interaction because they are stamped "info". Severity keeps
// applying to machine-generated events, and `events` still selects user actions.
func TestChannelSink_DefaultConfigDeliversUserActionsNotInfoLogs(t *testing.T) {
	cfg := *config.DefaultAgntConfig().Channel
	enabled := true
	cfg.Enabled = &enabled // the only non-default: turn channel mode on
	if cfg.GetSeverity() != "warning" {
		t.Fatalf("setup: default channel severity is %q, this test assumes warning", cfg.GetSeverity())
	}
	ctx := context.Background()

	panel := proxy.LogEntry{Type: proxy.LogTypePanelMessage, PanelMessage: &proxy.PanelMessage{Message: "make this button blue"}}
	userActions := map[string]proxy.LogEntry{
		"panel message": panel,
		"interaction":   {Type: proxy.LogTypeInteraction, Interaction: &proxy.InteractionEvent{EventType: "click"}},
		"responsive":    {Type: proxy.LogTypeResponsiveRequest, ResponsiveRequest: &proxy.ResponsiveRequest{Width: 390}},
	}
	for name, entry := range userActions {
		var tr transports
		NewChannelSink(&cfg, tr.notify).HandleEntry(ctx, entry)
		if len(tr.channel) != 1 {
			t.Errorf("%s: default config must deliver a user action, got %d", name, len(tr.channel))
		}
	}

	// Machine-generated events below the threshold are still filtered...
	belowThreshold := map[string]proxy.LogEntry{
		"info custom log": {Type: proxy.LogTypeCustom, Custom: &proxy.CustomLog{Level: "info", Message: "render pass"}},
		"info diagnostic": {Type: proxy.LogTypeDiagnostic,
			Diagnostic: &proxy.ProxyDiagnostic{Level: proxy.DiagnosticInfo, Message: "connection established"}},
	}
	for name, entry := range belowThreshold {
		var tr transports
		NewChannelSink(&cfg, tr.notify).HandleEntry(ctx, entry)
		if len(tr.channel) != 0 {
			t.Errorf("%s: below the default severity threshold, must not be delivered", name)
		}
	}
	// ...and one at or above it is delivered.
	var tr transports
	NewChannelSink(&cfg, tr.notify).HandleEntry(ctx,
		proxy.LogEntry{Type: proxy.LogTypeCustom, Custom: &proxy.CustomLog{Level: "error", Message: "cart invariant broken"}})
	if len(tr.channel) != 1 {
		t.Errorf("error-level custom log must pass the default threshold, got %d", len(tr.channel))
	}

	// `events` remains the selector for user actions.
	cfg.Events = []string{"custom"}
	tr = transports{}
	NewChannelSink(&cfg, tr.notify).HandleEntry(ctx, panel)
	if len(tr.channel) != 0 {
		t.Errorf("events allowlist without panel_message must still exclude it, got %d", len(tr.channel))
	}
	cfg.Events = []string{"panel_message"}
	NewChannelSink(&cfg, tr.notify).HandleEntry(ctx, panel)
	if len(tr.channel) != 1 {
		t.Errorf("events allowlist naming panel_message must deliver it, got %d", len(tr.channel))
	}
}

// With channel mode off nothing changes: the digest is an MCP log notification
// carrying the one-line text, and no channel event is ever sent.
func TestChannelModeOff_DigestIsALogNotification(t *testing.T) {
	var tr transports
	payload := onePingPayload()
	if !ForwardIncidentDigest(context.Background(), digestEntry(t, payload), false, tr.log, tr.notify) {
		t.Fatal("digest was not delivered")
	}
	if len(tr.channel) != 0 || len(tr.logs) != 1 {
		t.Fatalf("want 1 log and 0 channel events, got channel=%d log=%d", len(tr.channel), len(tr.logs))
	}
	if tr.logs[0] != incident.CompactDigestText(payload) {
		t.Errorf("log text changed: %q", tr.logs[0])
	}

	// Non-digest entries are not this function's to deliver, on either transport.
	for _, channelOn := range []bool{true, false} {
		if ForwardIncidentDigest(context.Background(), browserErrorEntry(), channelOn, tr.log, tr.notify) {
			t.Errorf("channelOn=%v: a non-digest entry was delivered", channelOn)
		}
	}
	if tr.total() != 1 {
		t.Errorf("non-digest entries produced notifications: %+v", tr)
	}
}

// A digest from a daemon that attaches no payload still delivers its summary.
func TestForwardIncidentDigest_WithoutPayloadFallsBackToSummaryLine(t *testing.T) {
	var tr transports
	entry := proxy.LogEntry{Type: proxy.LogTypeIncidentDigest,
		Custom: &proxy.CustomLog{Level: "warning", Message: "[agnt] incidents: crit=0 err=0 warn=1 info=0 (new=1). pull: get_incidents"}}
	ForwardIncidentDigest(context.Background(), entry, true, tr.log, tr.notify)
	if len(tr.channel) != 1 || tr.channel[0].Content != entry.Custom.Message {
		t.Fatalf("want the summary line alone, got %+v", tr.channel)
	}
}
