package tools

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestBuildWalkthroughExec_Validation(t *testing.T) {
	cases := []struct {
		name    string
		input   WalkthroughInput
		wantErr string // substring; "" = no error
	}{
		{"unknown action", WalkthroughInput{Action: "frobnicate"}, "unknown action"},
		{"load without script", WalkthroughInput{Action: "load"}, "script required"},
		{"start without script or id", WalkthroughInput{Action: "start"}, "requires script or script_id"},
		{"start invalid mode", WalkthroughInput{Action: "start", ScriptID: "demo", Mode: "turbo"}, "invalid mode"},
		{"stop ok", WalkthroughInput{Action: "stop"}, ""},
		{"next ok", WalkthroughInput{Action: "next"}, ""},
		{"prev ok", WalkthroughInput{Action: "prev"}, ""},
		{"play ok", WalkthroughInput{Action: "play"}, ""},
		{"pause ok", WalkthroughInput{Action: "pause"}, ""},
		{"status ok", WalkthroughInput{Action: "status"}, ""},
		{"list ok", WalkthroughInput{Action: "list"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, err := buildWalkthroughExec(tc.input)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if !strings.Contains(code, "window.__devtool") || !strings.Contains(code, "walkthrough") {
					t.Fatalf("code missing walkthrough guard: %q", code)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q, got code %q", tc.wantErr, code)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error %q does not contain %q", err.Error(), tc.wantErr)
			}
		})
	}
}

func TestBuildWalkthroughExec_StartEmbedsScriptAndMode(t *testing.T) {
	script := json.RawMessage(`{"id":"demo","title":"Demo","steps":[{"title":"a","advance":{"type":"auto"}}]}`)
	code, err := buildWalkthroughExec(WalkthroughInput{Action: "start", Script: script})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(code, `w.start(`) {
		t.Errorf("missing start call: %q", code)
	}
	if !strings.Contains(code, `"id":"demo"`) {
		t.Errorf("script not embedded: %q", code)
	}
	if !strings.Contains(code, `mode:"auto"`) {
		t.Errorf("default mode not auto: %q", code)
	}
}

func TestBuildWalkthroughExec_StartByIDManualMode(t *testing.T) {
	code, err := buildWalkthroughExec(WalkthroughInput{Action: "start", ScriptID: "checkout-demo", Mode: "manual"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(code, `"checkout-demo"`) {
		t.Errorf("script_id not embedded: %q", code)
	}
	if !strings.Contains(code, `mode:"manual"`) {
		t.Errorf("manual mode not set: %q", code)
	}
}

func TestBuildWalkthroughExec_LoadEmbedsScript(t *testing.T) {
	script := json.RawMessage(`{"id":"x","steps":[]}`)
	code, err := buildWalkthroughExec(WalkthroughInput{Action: "load", Script: script})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(code, `w.load(`) || !strings.Contains(code, `"id":"x"`) {
		t.Errorf("load did not embed script: %q", code)
	}
}
