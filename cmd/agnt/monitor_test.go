package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/standardbeagle/agnt/internal/proxy"
	"github.com/stretchr/testify/assert"
)

func TestParseTypes(t *testing.T) {
	tests := []struct {
		input string
		want  []string
	}{
		{"", nil},
		{"error", []string{"error"}},
		{"error,http,panel_message", []string{"error", "http", "panel_message"}},
		{" error , http ", []string{"error", "http"}},
		{",,,", nil},
	}

	for _, tt := range tests {
		got := parseTypes(tt.input)
		assert.Equal(t, tt.want, got)
	}
}

func TestFormatCompactError(t *testing.T) {
	entry := proxy.LogEntry{
		Type: proxy.LogTypeError,
		Error: &proxy.FrontendError{
			Message: "Cannot read 'map' of undefined",
			Source:  "List.tsx",
			LineNo:  42,
			ColNo:   15,
		},
	}
	got := formatCompact(entry)
	assert.Equal(t, `[error] Cannot read 'map' of undefined → List.tsx:42:15`, got)
}

func TestFormatCompactErrorNoLocation(t *testing.T) {
	entry := proxy.LogEntry{
		Type:  proxy.LogTypeError,
		Error: &proxy.FrontendError{Message: "Something broke"},
	}
	got := formatCompact(entry)
	assert.Equal(t, `[error] Something broke`, got)
}

func TestFormatCompactHTTPError(t *testing.T) {
	entry := proxy.LogEntry{
		Type: proxy.LogTypeHTTP,
		HTTP: &proxy.HTTPLogEntry{
			Method:     "POST",
			URL:        "/api/users",
			StatusCode: 500,
			Error:      "database connection timeout",
		},
	}
	got := formatCompact(entry)
	assert.Equal(t, `[http:500] POST /api/users → database connection timeout`, got)
}

func TestFormatCompactHTTPWithBody(t *testing.T) {
	entry := proxy.LogEntry{
		Type: proxy.LogTypeHTTP,
		HTTP: &proxy.HTTPLogEntry{
			Method:       "GET",
			URL:          "/api/data",
			StatusCode:   200,
			ResponseBody: "ok",
		},
	}
	got := formatCompact(entry)
	assert.Equal(t, `[http:200] GET /api/data → ok`, got)
}

func TestFormatCompactHTTPMinimal(t *testing.T) {
	entry := proxy.LogEntry{
		Type: proxy.LogTypeHTTP,
		HTTP: &proxy.HTTPLogEntry{
			Method:     "GET",
			URL:        "/api/health",
			StatusCode: 200,
		},
	}
	got := formatCompact(entry)
	assert.Equal(t, `[http:200] GET /api/health`, got)
}

func TestFormatCompactPanelMessage(t *testing.T) {
	entry := proxy.LogEntry{
		Type: proxy.LogTypePanelMessage,
		PanelMessage: &proxy.PanelMessage{
			Message:     "Please fix the header alignment",
			Attachments: []proxy.PanelAttachment{{Type: "element"}, {Type: "screenshot_area"}},
		},
	}
	got := formatCompact(entry)
	assert.Equal(t, `[panel_message] "Please fix the header alignment" +2 attachments`, got)
}

func TestFormatCompactPanelMessageNoAttachments(t *testing.T) {
	entry := proxy.LogEntry{
		Type: proxy.LogTypePanelMessage,
		PanelMessage: &proxy.PanelMessage{
			Message: "Hello",
		},
	}
	got := formatCompact(entry)
	assert.Equal(t, `[panel_message] "Hello"`, got)
}

func TestFormatCompactInteraction(t *testing.T) {
	entry := proxy.LogEntry{
		Type: proxy.LogTypeInteraction,
		Interaction: &proxy.InteractionEvent{
			EventType: "click",
			Target: proxy.InteractionTarget{
				Tag:  "button",
				ID:   "submit",
				Text: "Save Changes",
			},
		},
	}
	got := formatCompact(entry)
	assert.Equal(t, `[interaction:click] button#submit "Save Changes"`, got)
}

func TestFormatCompactMutation(t *testing.T) {
	entry := proxy.LogEntry{
		Type: proxy.LogTypeMutation,
		Mutation: &proxy.MutationEvent{
			MutationType: "added",
			Target: proxy.MutationTarget{
				Tag:      "div",
				Selector: ".container .list",
			},
		},
	}
	got := formatCompact(entry)
	assert.Equal(t, `[mutation:added] div .container .list`, got)
}

func TestFormatCompactDesignChat(t *testing.T) {
	entry := proxy.LogEntry{
		Type: proxy.LogTypeDesignChat,
		DesignChat: &proxy.DesignChat{
			Message:  "Make it blue",
			Selector: ".header",
		},
	}
	got := formatCompact(entry)
	assert.Equal(t, `[design_chat] "Make it blue" on .header`, got)
}

func TestFormatCompactSketch(t *testing.T) {
	entry := proxy.LogEntry{
		Type: proxy.LogTypeSketch,
		Sketch: &proxy.SketchEntry{
			Description:  "Login form wireframe",
			ElementCount: 7,
		},
	}
	got := formatCompact(entry)
	assert.Equal(t, `[sketch] Login form wireframe (7 elements)`, got)
}

func TestFormatCompactDiagnostic(t *testing.T) {
	entry := proxy.LogEntry{
		Type: proxy.LogTypeDiagnostic,
		Diagnostic: &proxy.ProxyDiagnostic{
			Level:   proxy.DiagnosticError,
			Message: "Connection refused",
		},
	}
	got := formatCompact(entry)
	assert.Equal(t, `[diagnostic:error] Connection refused`, got)
}

func TestFormatCompactCustom(t *testing.T) {
	entry := proxy.LogEntry{
		Type:   proxy.LogTypeCustom,
		Custom: &proxy.CustomLog{Level: "warn", Message: "Slow query detected"},
	}
	got := formatCompact(entry)
	assert.Equal(t, `[custom:warn] Slow query detected`, got)
}

func TestFormatCompactNilFields(t *testing.T) {
	types := []proxy.LogEntryType{
		proxy.LogTypeError, proxy.LogTypeHTTP, proxy.LogTypePanelMessage,
		proxy.LogTypeInteraction, proxy.LogTypeMutation, proxy.LogTypeDesignChat,
		proxy.LogTypeSketch, proxy.LogTypeDiagnostic, proxy.LogTypeCustom,
		proxy.LogTypeProcessOutput,
	}
	for _, typ := range types {
		entry := proxy.LogEntry{Type: typ}
		got := formatCompact(entry)
		assert.Equal(t, "", got, "empty entry of type %s should produce no output", typ)
	}
}

func TestFormatCompactUnknownType(t *testing.T) {
	entry := proxy.LogEntry{Type: proxy.LogTypePerformance}
	got := formatCompact(entry)
	assert.Equal(t, "", got)
}

func TestFormatJSONError(t *testing.T) {
	ts := time.Date(2026, 4, 10, 12, 0, 0, 0, time.UTC)
	entry := proxy.LogEntry{
		Type: proxy.LogTypeError,
		Error: &proxy.FrontendError{
			Message:   "Cannot read 'map' of undefined",
			Source:    "List.tsx",
			LineNo:    42,
			ColNo:     15,
			Timestamp: ts,
		},
	}
	got := formatJSON(entry)
	assert.NotEmpty(t, got)

	var result monitorJSONEntry
	assert.NoError(t, json.Unmarshal([]byte(got), &result))
	assert.Equal(t, "error", result.Type)
	assert.Equal(t, "Cannot read 'map' of undefined", result.Message)
	assert.Equal(t, "List.tsx:42:15", result.Location)
	assert.Equal(t, "error", result.Severity)
	assert.Equal(t, ts.Format(time.RFC3339), result.Timestamp)
}

func TestFormatJSONHTTP500(t *testing.T) {
	entry := proxy.LogEntry{
		Type: proxy.LogTypeHTTP,
		HTTP: &proxy.HTTPLogEntry{
			Method:     "POST",
			URL:        "/api/users",
			StatusCode: 500,
			Error:      "database connection timeout",
			Timestamp:  time.Now(),
		},
	}
	got := formatJSON(entry)
	assert.NotEmpty(t, got)

	var result monitorJSONEntry
	assert.NoError(t, json.Unmarshal([]byte(got), &result))
	assert.Equal(t, "http", result.Type)
	assert.Equal(t, "error", result.Severity)
	assert.Contains(t, result.Message, "500")
	assert.Contains(t, result.Message, "database connection timeout")
}

func TestFormatJSONHTTP200(t *testing.T) {
	entry := proxy.LogEntry{
		Type: proxy.LogTypeHTTP,
		HTTP: &proxy.HTTPLogEntry{
			Method:     "GET",
			URL:        "/api/data",
			StatusCode: 200,
			Timestamp:  time.Now(),
		},
	}
	got := formatJSON(entry)
	assert.NotEmpty(t, got)

	var result monitorJSONEntry
	assert.NoError(t, json.Unmarshal([]byte(got), &result))
	assert.Equal(t, "http", result.Type)
	assert.Empty(t, result.Severity)
}

func TestFormatJSONPanelMessage(t *testing.T) {
	entry := proxy.LogEntry{
		Type: proxy.LogTypePanelMessage,
		PanelMessage: &proxy.PanelMessage{
			Message:   "Fix the header",
			Timestamp: time.Now(),
		},
	}
	got := formatJSON(entry)
	assert.NotEmpty(t, got)

	var result monitorJSONEntry
	assert.NoError(t, json.Unmarshal([]byte(got), &result))
	assert.Equal(t, "panel_message", result.Type)
	assert.Equal(t, "Fix the header", result.Message)
}

func TestFormatJSONNilFields(t *testing.T) {
	for _, typ := range []proxy.LogEntryType{
		proxy.LogTypeError, proxy.LogTypeHTTP, proxy.LogTypePanelMessage,
	} {
		entry := proxy.LogEntry{Type: typ}
		got := formatJSON(entry)
		assert.Equal(t, "", got, "nil %s should produce empty json", typ)
	}
}

func TestTruncate(t *testing.T) {
	assert.Equal(t, "short", truncate("short", 10))
	assert.Equal(t, "this is a very lo...", truncate("this is a very long string indeed", 20))
}

func TestLocation(t *testing.T) {
	assert.Equal(t, "app.tsx:10:5", location("app.tsx", 10, 5))
	assert.Equal(t, "app.tsx:10", location("app.tsx", 10, 0))
	assert.Equal(t, "", location("", 10, 5))
}

func TestPlural(t *testing.T) {
	assert.Equal(t, "", plural(1))
	assert.Equal(t, "s", plural(0))
	assert.Equal(t, "s", plural(2))
}

func TestFormatCompactLongMessage(t *testing.T) {
	longBody := strings.Repeat("x", 200)
	entry := proxy.LogEntry{
		Type: proxy.LogTypeHTTP,
		HTTP: &proxy.HTTPLogEntry{
			Method:       "GET",
			URL:          "/api/data",
			StatusCode:   200,
			ResponseBody: longBody,
		},
	}
	got := formatCompact(entry)
	assert.True(t, len(got) < 150, "compact output should be short, got %d chars: %s", len(got), got)
	assert.Contains(t, got, "...")
}

func TestFormatCompactProcessOutput(t *testing.T) {
	entry := proxy.LogEntry{
		Type: proxy.LogTypeProcessOutput,
		ProcessOutput: &proxy.ProcessOutputEvent{
			ProcessID: "dev-server",
			Stream:    "combined",
			Line:      "Listening on http://localhost:3000",
		},
	}
	got := formatCompact(entry)
	assert.Equal(t, `[process:dev-server] Listening on http://localhost:3000`, got)
}

func TestFormatCompactProcessOutputNil(t *testing.T) {
	entry := proxy.LogEntry{Type: proxy.LogTypeProcessOutput}
	got := formatCompact(entry)
	assert.Equal(t, "", got)
}

func TestFormatJSONProcessOutput(t *testing.T) {
	ts := time.Date(2026, 4, 11, 0, 0, 0, 0, time.UTC)
	entry := proxy.LogEntry{
		Type: proxy.LogTypeProcessOutput,
		ProcessOutput: &proxy.ProcessOutputEvent{
			ProcessID: "dev",
			Stream:    "combined",
			Line:      "Server started",
			Timestamp: ts,
		},
	}
	got := formatJSON(entry)
	assert.NotEmpty(t, got)

	var result monitorJSONEntry
	assert.NoError(t, json.Unmarshal([]byte(got), &result))
	assert.Equal(t, "process", result.Type)
	assert.Equal(t, "Server started", result.Message)
	assert.Equal(t, "dev", result.Location)
	assert.Equal(t, ts.Format(time.RFC3339), result.Timestamp)
}
