package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/standardbeagle/agnt/internal/config"
)

func proxyEvent(t *testing.T, typ string, data any) ProxyEvent {
	t.Helper()
	raw, err := json.Marshal(data)
	if err != nil {
		t.Fatalf("marshal event data: %v", err)
	}
	return ProxyEvent{Type: typ, ProxyID: "dev", Data: raw}
}

func boolPtr(b bool) *bool { return &b }

// newTestAIOverlay builds an AIOverlay with no listener: processProxyEvent and
// the queue need none.
func newTestAIOverlay(policy autoForwardPolicy, queue int) *AIOverlay {
	return &AIOverlay{messages: make(chan aiOverlayMessage, queue), policy: policy}
}

func drainAI(a *AIOverlay) []aiOverlayMessage {
	var out []aiOverlayMessage
	for {
		select {
		case m := <-a.messages:
			out = append(out, m)
		default:
			return out
		}
	}
}

// TestAutoForwardPolicyAdmit covers each link of the shared filter chain.
func TestAutoForwardPolicyAdmit(t *testing.T) {
	jsErr := proxyEvent(t, "browser_error", map[string]any{"message": "TypeError: x is undefined"})
	http500 := proxyEvent(t, "http_error", map[string]any{"method": "GET", "url": "/api/x", "status_code": 500})
	http404 := proxyEvent(t, "http_error", map[string]any{"method": "GET", "url": "/api/x", "status_code": 404})
	hmr := proxyEvent(t, "browser_error", map[string]any{"message": "[vite] server connection lost"})

	cases := []struct {
		name   string
		policy autoForwardPolicy
		event  ProxyEvent
		want   bool
	}{
		{"disabled drops everything", autoForwardPolicy{enabled: false}, jsErr, false},
		{"enabled with defaults admits a JS error", autoForwardPolicy{enabled: true}, jsErr, true},
		{"source filter excludes http", autoForwardPolicy{enabled: true, cfg: &config.AutoForwardConfig{Sources: []string{"browser"}}}, http500, false},
		{"source filter keeps browser", autoForwardPolicy{enabled: true, cfg: &config.AutoForwardConfig{Sources: []string{"browser"}}}, jsErr, true},
		{"default severity floor drops a 4xx", autoForwardPolicy{enabled: true, cfg: &config.AutoForwardConfig{}}, http404, false},
		{"default severity floor keeps a 5xx", autoForwardPolicy{enabled: true, cfg: &config.AutoForwardConfig{}}, http500, true},
		{"warning severity keeps a 4xx", autoForwardPolicy{enabled: true, cfg: &config.AutoForwardConfig{Severity: "warning"}}, http404, true},
		{"cascade pattern drops transport noise", autoForwardPolicy{enabled: true, cascade: []string{"server connection lost"}}, hmr, false},
		{"cascade match is case-insensitive", autoForwardPolicy{enabled: true, cascade: []string{"typeerror"}}, jsErr, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, got := tc.policy.admit(tc.event); got != tc.want {
				t.Fatalf("admit = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestAlertPolicyFromConfig pins the config → policy mapping the AI REPLs
// use, including the two "off" switches the path used to ignore.
func TestAlertPolicyFromConfig(t *testing.T) {
	if p := alertPolicyFromConfig(config.DefaultAgntConfig()); !p.enabled || len(p.cascade) == 0 {
		t.Fatalf("default config: enabled=%v cascade=%d, want enabled with default cascade patterns", p.enabled, len(p.cascade))
	}

	off := config.DefaultAgntConfig()
	off.Alerts = &config.AlertsConfig{AutoForward: &config.AutoForwardConfig{Enabled: boolPtr(false)}}
	if p := alertPolicyFromConfig(off); p.enabled {
		t.Fatal("alerts.auto-forward.enabled=false must disable forwarding")
	}

	allOff := config.DefaultAgntConfig()
	allOff.Alerts = &config.AlertsConfig{Enabled: boolPtr(false)}
	if p := alertPolicyFromConfig(allOff); p.enabled {
		t.Fatal("alerts.enabled=false must disable forwarding")
	}

	if p := alertPolicyFromConfig(nil); !p.enabled {
		t.Fatal("nil config must behave like the defaults")
	}
}

// TestAIOverlayAppliesAlertPolicy is the regression for `agnt ai claude` /
// `agnt acp` ignoring the alerts config: an auto-generated error is filtered,
// an explicit user action is always delivered and marked protected.
func TestAIOverlayAppliesAlertPolicy(t *testing.T) {
	jsErr := proxyEvent(t, "browser_error", map[string]any{"message": "TypeError: x is undefined"})
	sketch := proxyEvent(t, "sketch", map[string]any{"file_path": "/tmp/s.png", "description": "move this"})

	t.Run("auto-forward disabled drops errors, keeps user actions", func(t *testing.T) {
		a := newTestAIOverlay(autoForwardPolicy{enabled: false}, 8)
		a.processProxyEvent(jsErr)
		a.processProxyEvent(sketch)
		got := drainAI(a)
		if len(got) != 1 || !strings.Contains(got[0].text, "move this") {
			t.Fatalf("got %+v, want exactly the sketch", got)
		}
		for _, m := range got {
			if strings.Contains(m.text, "TypeError") {
				t.Fatalf("filtered error was queued: %q", m.text)
			}
			if !m.protected {
				t.Fatalf("user action not marked protected: %q", m.text)
			}
		}
	})

	t.Run("enabled queues the error unprotected", func(t *testing.T) {
		a := newTestAIOverlay(autoForwardPolicy{enabled: true}, 8)
		a.processProxyEvent(jsErr)
		got := drainAI(a)
		if len(got) != 1 || got[0].protected || !strings.Contains(got[0].text, "TypeError") {
			t.Fatalf("got %+v, want one unprotected browser error", got)
		}
	})

	t.Run("forwarding pause holds errors", func(t *testing.T) {
		a := newTestAIOverlay(autoForwardPolicy{enabled: true}, 8)
		a.SetForwardingPaused(true)
		a.processProxyEvent(jsErr)
		if got := drainAI(a); len(got) != 0 {
			t.Fatalf("paused overlay queued %+v", got)
		}
		a.SetForwardingPaused(false)
		a.processProxyEvent(jsErr)
		if got := drainAI(a); len(got) != 1 {
			t.Fatalf("resumed overlay queued %d messages, want 1", len(got))
		}
	})

	t.Run("zero-value policy forwards no errors", func(t *testing.T) {
		a := newTestAIOverlay(autoForwardPolicy{}, 8)
		a.processProxyEvent(jsErr)
		if got := drainAI(a); len(got) != 0 {
			t.Fatalf("unconfigured overlay queued %+v", got)
		}
	})
}

// TestAIOverlayCountsQueueOverflow: a full queue drops the message but the
// loss is counted for the consumer to report, not left in the debug log.
func TestAIOverlayCountsQueueOverflow(t *testing.T) {
	a := newTestAIOverlay(autoForwardPolicy{}, 2)
	for i := 0; i < 5; i++ {
		a.enqueue(aiOverlayMessage{text: "m", protected: true})
	}
	if got := a.TakeDropped(); got != 3 {
		t.Fatalf("TakeDropped = %d, want 3", got)
	}
	if got := a.TakeDropped(); got != 0 {
		t.Fatalf("TakeDropped did not reset: %d", got)
	}
}
