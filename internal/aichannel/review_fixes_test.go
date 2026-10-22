package aichannel

import (
	"context"
	"strings"
	"testing"
)

// A generic agent without UseStdin used to lose the input context entirely.
func TestGenericAdapter_BuildCommand_EmbedsContextWithoutStdin(t *testing.T) {
	a := &GenericAdapter{agentType: AgentCustom}
	cfg := Config{Command: "agent", NonInteractiveFlag: "-p"}
	cmd, err := a.BuildCommand(context.Background(), cfg, "summarize", "build failed: x", "")
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(cmd.Args, "\x00")
	if !strings.Contains(joined, "build failed: x") {
		t.Fatalf("context dropped from argv: %q", cmd.Args)
	}

	// With UseStdin the context travels on stdin, not in argv.
	cfg.UseStdin = true
	cmd, err = a.BuildCommand(context.Background(), cfg, "summarize", "build failed: x", "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(cmd.Args, "\x00"), "build failed: x") {
		t.Fatalf("context duplicated into argv with UseStdin: %q", cmd.Args)
	}
}

// One stream-json line over bufio's default 64KB token cap must still parse.
func TestParseStreamJSON_LongLine(t *testing.T) {
	big := strings.Repeat("a", 200*1024)
	line := `{"type":"result","subtype":"success","result":"` + big + `"}`
	resp, err := ParseResponse(line+"\n", OutputFormatStreamJSON)
	if err != nil {
		t.Fatalf("long line failed to parse: %v", err)
	}
	if len(resp.Result) != len(big) {
		t.Fatalf("result truncated: %d", len(resp.Result))
	}
}
