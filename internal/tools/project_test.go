package tools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/standardbeagle/go-sdk/mcp"
)

// TestHandleDetect_NextJS covers the three explicit acceptance criteria:
//
//  1. detect on a Next.js project returns a proc_run field per script
//  2. compact output shows ready-to-use proc commands without raw:true
//  3. likely_signals = url+ready for `next dev`, error+warning for `next build`
func TestHandleDetect_NextJS(t *testing.T) {
	dir := t.TempDir()
	pkg := `{
  "name": "my-next-app",
  "scripts": {
    "dev": "next dev",
    "build": "next build",
    "start": "next start",
    "lint": "next lint"
  },
  "dependencies": {
    "next": "^14.0.0"
  }
}`
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(pkg), 0644); err != nil {
		t.Fatalf("write package.json: %v", err)
	}

	result, output, err := detectAndFormat(dir, false)
	if err != nil {
		t.Fatalf("handleDetect returned error: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil compact CallToolResult, got nil")
	}
	if result.IsError {
		t.Fatalf("unexpected error result: %+v", result)
	}

	// AC1: every script gets a proc_run field.
	if len(output.Scripts) == 0 {
		t.Fatal("expected at least one script in output")
	}
	for _, s := range output.Scripts {
		if s.ProcRun == "" {
			t.Errorf("script %q missing proc_run", s.Name)
		}
		if !strings.Contains(s.ProcRun, `action:"run"`) {
			t.Errorf("script %q proc_run missing action:run, got %q", s.Name, s.ProcRun)
		}
		if !strings.Contains(s.ProcRun, `id:"`+s.Name+`"`) {
			t.Errorf("script %q proc_run missing id field, got %q", s.Name, s.ProcRun)
		}
		if s.ProcWait == "" {
			t.Errorf("script %q missing proc_wait", s.Name)
		}
	}

	// AC3: likely_signals heuristics for next dev / next build.
	dev := findScript(output.Scripts, "dev")
	if dev == nil {
		t.Fatal("expected 'dev' script in output")
	}
	if !signalsEqual(dev.LikelySignals, []string{"url", "ready", "port"}) {
		t.Errorf("dev likely_signals = %v, want [url ready port]", dev.LikelySignals)
	}
	build := findScript(output.Scripts, "build")
	if build == nil {
		t.Fatal("expected 'build' script in output")
	}
	if !signalsEqual(build.LikelySignals, []string{"error", "warning"}) {
		t.Errorf("build likely_signals = %v, want [error warning]", build.LikelySignals)
	}

	// AC2: compact output present without raw:true.
	if output.Summary == "" {
		t.Fatal("expected non-empty Summary in compact mode")
	}
	if !strings.Contains(output.Summary, "Detected:") {
		t.Errorf("Summary missing 'Detected:' header, got:\n%s", output.Summary)
	}
	if !strings.Contains(output.Summary, "Node.js") {
		t.Errorf("Summary missing 'Node.js' label, got:\n%s", output.Summary)
	}
	// Each detected script's proc_run should appear in the compact block.
	for _, s := range output.Scripts {
		if !strings.Contains(output.Summary, s.ProcRun) {
			t.Errorf("Summary missing proc_run for %q:\nproc_run: %s\nsummary:\n%s",
				s.Name, s.ProcRun, output.Summary)
		}
	}

	// CallToolResult.Content[0] must echo the compact summary so MCP clients
	// without structured-output rendering still see something useful.
	if len(result.Content) == 0 {
		t.Fatal("expected at least one Content item in CallToolResult")
	}
	tc, ok := result.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("Content[0] is not TextContent, got %T", result.Content[0])
	}
	if tc.Text != output.Summary {
		t.Errorf("CallToolResult text != Summary\nresult: %q\nsummary: %q", tc.Text, output.Summary)
	}
}

// TestHandleDetect_RawMode confirms raw:true skips compact rendering.
func TestHandleDetect_RawMode(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "package.json"),
		[]byte(`{"name":"x","scripts":{"dev":"vite"}}`), 0644); err != nil {
		t.Fatalf("write package.json: %v", err)
	}

	result, output, err := detectAndFormat(dir, true)
	if err != nil {
		t.Fatalf("handleDetect returned error: %v", err)
	}
	if result != nil {
		t.Errorf("expected nil CallToolResult in raw mode (structured-only), got %+v", result)
	}
	if output.Summary != "" {
		t.Errorf("expected empty Summary in raw mode, got %q", output.Summary)
	}
	if len(output.Scripts) == 0 {
		t.Fatal("raw mode should still populate Scripts")
	}
}

// TestHandleDetect_ProcWaitTimeouts checks the timeout heuristic per category.
func TestHandleDetect_ProcWaitTimeouts(t *testing.T) {
	dir := t.TempDir()
	pkg := `{
  "name": "x",
  "scripts": {
    "dev": "next dev",
    "build": "next build",
    "test": "vitest run",
    "lint": "eslint ."
  }
}`
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(pkg), 0644); err != nil {
		t.Fatalf("write package.json: %v", err)
	}

	_, output, err := detectAndFormat(dir, true)
	if err != nil {
		t.Fatalf("handleDetect: %v", err)
	}

	// Per task notes:
	//   dev/serve  → 30s   (timeout:30000)
	//   build      → 60s   (timeout:60000)
	//   test       → 120s  (timeout:120000)
	//   lint       → 30s   (timeout:30000)
	wantTimeout := map[string]string{
		"dev":   "timeout:30000",
		"build": "timeout:60000",
		"test":  "timeout:120000",
		"lint":  "timeout:30000",
	}
	for name, want := range wantTimeout {
		s := findScript(output.Scripts, name)
		if s == nil {
			t.Errorf("missing %q script", name)
			continue
		}
		if !strings.Contains(s.ProcWait, want) {
			t.Errorf("script %q proc_wait should contain %q, got %q", name, want, s.ProcWait)
		}
	}
}

// TestHandleDetect_GoProject ensures proc_run is generated for non-Node projects too.
func TestHandleDetect_GoProject(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"),
		[]byte("module example.com/x\n\ngo 1.23\n"), 0644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}

	_, output, err := detectAndFormat(dir, false)
	if err != nil {
		t.Fatalf("handleDetect: %v", err)
	}
	if output.Type != "go" {
		t.Errorf("type = %q, want go", output.Type)
	}
	test := findScript(output.Scripts, "test")
	if test == nil {
		t.Fatal("expected test script for Go project")
	}
	if !strings.Contains(test.Command, "go test") {
		t.Errorf("test command should contain 'go test', got %q", test.Command)
	}
	if !signalsEqual(test.LikelySignals, []string{"error", "ready"}) {
		t.Errorf("test likely_signals = %v, want [error ready]", test.LikelySignals)
	}
	if !strings.Contains(test.ProcRun, `id:"test"`) {
		t.Errorf("proc_run missing id:\"test\", got %q", test.ProcRun)
	}
}

// TestHandleDetect_UnknownProject covers the empty-script path so we don't
// emit a malformed compact block for unknown directories.
func TestHandleDetect_UnknownProject(t *testing.T) {
	dir := t.TempDir() // empty
	_, output, err := detectAndFormat(dir, false)
	if err != nil {
		t.Fatalf("handleDetect: %v", err)
	}
	if output.Type != "unknown" {
		t.Errorf("type = %q, want unknown", output.Type)
	}
	if len(output.Scripts) != 0 {
		t.Errorf("expected no scripts for unknown project, got %d", len(output.Scripts))
	}
	if !strings.Contains(output.Summary, "no scripts detected") {
		t.Errorf("expected fallback message in summary, got:\n%s", output.Summary)
	}
}

// TestHandleDetect_BadPath covers validation errors.
func TestHandleDetect_BadPath(t *testing.T) {
	result, _, err := detectAndFormat("/this/path/does/not/exist/"+strings.Repeat("x", 10), false)
	if err != nil {
		t.Fatalf("handler should not return Go error, got %v", err)
	}
	if result == nil || !result.IsError {
		t.Errorf("expected error result for nonexistent path, got %+v", result)
	}
}

// TestDetectOutput_JSONShape pins the wire-format keys so callers don't break.
func TestDetectOutput_JSONShape(t *testing.T) {
	out := DetectOutput{
		Type: "node",
		Name: "x",
		Scripts: []DetectScript{
			{Name: "dev", Command: "npm run dev", ProcRun: "PR", ProcWait: "PW", LikelySignals: []string{"url"}},
		},
		ScriptNames: []string{"dev"},
	}
	b, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(b)
	for _, key := range []string{
		`"type"`, `"name"`, `"scripts"`, `"script_names"`,
		`"proc_run"`, `"proc_wait"`, `"likely_signals"`, `"command"`,
	} {
		if !strings.Contains(s, key) {
			t.Errorf("JSON missing key %s\nfull: %s", key, s)
		}
	}
}

// helpers

func findScript(scripts []DetectScript, name string) *DetectScript {
	for i := range scripts {
		if scripts[i].Name == name {
			return &scripts[i]
		}
	}
	return nil
}

func signalsEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
