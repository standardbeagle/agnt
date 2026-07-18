package config

import "testing"

// TestParseAgntConfig_Retention pins Config Authority for the alerts
// retention block: parsed keys must change getter results, and every trigger
// defaults to enabled when the block (or whole alerts config) is absent.
func TestParseAgntConfig_Retention(t *testing.T) {
	// Absent config: everything defaults on, nil-safe at every level.
	var nilAlerts *AlertsConfig
	r := nilAlerts.GetRetention()
	if !r.ClearOnBuildSuccess() || !r.ClearOnProcStop() || !r.ClearOnSessionEnd() {
		t.Fatal("nil config must default every retention trigger to enabled")
	}

	cfg, err := ParseAgntConfig(`alerts {
  retention {
    on-build-success false
    on-proc-stop false
  }
}`)
	if err != nil {
		t.Fatalf("ParseAgntConfig: %v", err)
	}
	r = cfg.Alerts.GetRetention()
	if r == nil {
		t.Fatal("retention block parsed to nil")
	}
	if r.ClearOnBuildSuccess() {
		t.Error("on-build-success false must disable the build-success clear")
	}
	if r.ClearOnProcStop() {
		t.Error("on-proc-stop false must disable the proc-stop clear")
	}
	if !r.ClearOnSessionEnd() {
		t.Error("unset on-session-end must stay enabled")
	}

	cfg, err = ParseAgntConfig("alerts {\n  retention {\n    on-session-end true\n  }\n}")
	if err != nil {
		t.Fatalf("ParseAgntConfig: %v", err)
	}
	r = cfg.Alerts.GetRetention()
	if !r.ClearOnBuildSuccess() || !r.ClearOnSessionEnd() {
		t.Error("explicit true and unset keys must both read enabled")
	}
}
