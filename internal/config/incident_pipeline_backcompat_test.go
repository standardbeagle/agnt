package config

import "testing"

// The incident-pipeline flag was removed as a functional gate (the pipeline is
// now unconditional), but existing .agnt.kdl files may still carry it. Parsing
// must tolerate the key rather than failing the whole config load.
func TestParseAgntConfig_DeprecatedIncidentPipelineKeyTolerated(t *testing.T) {
	cfg, err := ParseAgntConfig("alerts {\n  incident-pipeline true\n}\n")
	if err != nil {
		t.Fatalf("deprecated incident-pipeline key must still parse, got: %v", err)
	}
	if cfg.Alerts == nil || !cfg.Alerts.DeprecatedIncidentPipeline {
		t.Fatal("expected the deprecated flag to be parsed onto the back-compat field")
	}
}
