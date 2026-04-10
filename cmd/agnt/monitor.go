package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/standardbeagle/agnt/internal/daemon"
	"github.com/standardbeagle/agnt/internal/protocol"
	"github.com/standardbeagle/agnt/internal/proxy"
)

var monitorCmd = &cobra.Command{
	Use:   "monitor",
	Short: "Stream daemon events to stdout",
	Long: `Connect to the daemon event stream and write one line per event.

Supports filtering by event type, proxy, process, and severity.
Output can be compact (human-readable) or json (NDJSON).

Automatically reconnects if the daemon restarts. Exit with Ctrl+C.`,
	RunE: runMonitor,
}

var (
	monitorTypes    string
	monitorProxy    string
	monitorProcess  string
	monitorSeverity string
	monitorFormat   string
)

func init() {
	monitorCmd.Flags().StringVar(&monitorTypes, "types", "", "Comma-separated event types (error,http,panel_message,design_chat,sketch,interaction,mutation,diagnostic,process)")
	monitorCmd.Flags().StringVar(&monitorProxy, "proxy", "", "Filter to specific proxy ID")
	monitorCmd.Flags().StringVar(&monitorProcess, "process", "", "Filter to specific process ID")
	monitorCmd.Flags().StringVar(&monitorSeverity, "severity", "", "Minimum severity (info,warning,error)")
	monitorCmd.Flags().StringVar(&monitorFormat, "format", "compact", "Output format: compact or json")
}

func runMonitor(cmd *cobra.Command, args []string) error {
	if monitorFormat != "compact" && monitorFormat != "json" {
		return fmt.Errorf("invalid format %q: must be compact or json", monitorFormat)
	}

	filter := protocol.StreamEventFilter{
		Types:     parseTypes(monitorTypes),
		ProxyID:   monitorProxy,
		ProcessID: monitorProcess,
		Severity:  monitorSeverity,
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	socketPath := getSocketPath(cmd)
	client := daemon.NewClient(daemon.WithSocketPath(socketPath))

	reconnectDelay := 2 * time.Second
	for {
		err := streamOnce(ctx, client, filter)
		if ctx.Err() != nil {
			return nil
		}
		fmt.Fprintf(os.Stderr, "monitor: %v; reconnecting in %s\n", err, reconnectDelay)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(reconnectDelay):
		}
	}
}

// streamOnce connects to the daemon and streams events until an error or cancellation.
func streamOnce(ctx context.Context, client *daemon.Client, filter protocol.StreamEventFilter) error {
	if err := client.Connect(); err != nil {
		return fmt.Errorf("connect failed: %w", err)
	}
	defer client.Close()

	fmt.Fprintf(os.Stderr, "monitor: connected to %s\n", client.SocketPath())

	return client.StreamEvents(ctx, filter, func(entry proxy.LogEntry) error {
		var line string
		if monitorFormat == "json" {
			line = formatJSON(entry)
		} else {
			line = formatCompact(entry)
		}
		if line == "" {
			return nil
		}
		fmt.Println(line)
		return nil
	})
}

// parseTypes splits a comma-separated string into a slice, trimming whitespace.
func parseTypes(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	result := make([]string, 0, len(parts))
	for _, p := range parts {
		t := strings.TrimSpace(p)
		if t != "" {
			result = append(result, t)
		}
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

// formatCompact renders a log entry as a single human-readable line.
func formatCompact(entry proxy.LogEntry) string {
	switch entry.Type {
	case proxy.LogTypeError:
		if entry.Error == nil {
			return ""
		}
		e := entry.Error
		loc := location(e.Source, e.LineNo, e.ColNo)
		if loc != "" {
			return fmt.Sprintf("[error] %s → %s", e.Message, loc)
		}
		return fmt.Sprintf("[error] %s", e.Message)

	case proxy.LogTypeHTTP:
		if entry.HTTP == nil {
			return ""
		}
		h := entry.HTTP
		status := ""
		if h.StatusCode > 0 {
			status = fmt.Sprintf(":%d", h.StatusCode)
		}
		msg := h.Error
		if msg == "" && h.ResponseBody != "" {
			msg = truncate(h.ResponseBody, 80)
		}
		if msg != "" {
			return fmt.Sprintf("[http%s] %s %s → %s", status, h.Method, h.URL, msg)
		}
		return fmt.Sprintf("[http%s] %s %s", status, h.Method, h.URL)

	case proxy.LogTypePanelMessage:
		if entry.PanelMessage == nil {
			return ""
		}
		pm := entry.PanelMessage
		attach := ""
		if n := len(pm.Attachments); n > 0 {
			attach = fmt.Sprintf(" +%d attachment%s", n, plural(n))
		}
		return fmt.Sprintf("[panel_message] %q%s", pm.Message, attach)

	case proxy.LogTypeInteraction:
		if entry.Interaction == nil {
			return ""
		}
		ie := entry.Interaction
		target := describeTarget(ie.Target)
		return fmt.Sprintf("[interaction:%s] %s", ie.EventType, target)

	case proxy.LogTypeMutation:
		if entry.Mutation == nil {
			return ""
		}
		m := entry.Mutation
		return fmt.Sprintf("[mutation:%s] %s %s", m.MutationType, m.Target.Tag, describeSelector(m.Target.Selector))

	case proxy.LogTypeDesignChat:
		if entry.DesignChat == nil {
			return ""
		}
		dc := entry.DesignChat
		return fmt.Sprintf("[design_chat] %q on %s", dc.Message, describeSelector(dc.Selector))

	case proxy.LogTypeSketch:
		if entry.Sketch == nil {
			return ""
		}
		sk := entry.Sketch
		return fmt.Sprintf("[sketch] %s (%d elements)", sk.Description, sk.ElementCount)

	case proxy.LogTypeDiagnostic:
		if entry.Diagnostic == nil {
			return ""
		}
		d := entry.Diagnostic
		return fmt.Sprintf("[diagnostic:%s] %s", d.Level, d.Message)

	case proxy.LogTypeCustom:
		if entry.Custom == nil {
			return ""
		}
		c := entry.Custom
		return fmt.Sprintf("[custom:%s] %s", c.Level, c.Message)

	default:
		return ""
	}
}

// monitorJSONEntry is the JSON output shape for a streamed event.
type monitorJSONEntry struct {
	Type      string `json:"type"`
	ProxyID   string `json:"proxy_id,omitempty"`
	Message   string `json:"message,omitempty"`
	Location  string `json:"location,omitempty"`
	Severity  string `json:"severity,omitempty"`
	Timestamp string `json:"timestamp"`
}

// formatJSON renders a log entry as a single JSON line (NDJSON).
func formatJSON(entry proxy.LogEntry) string {
	out := monitorJSONEntry{Type: string(entry.Type)}

	switch entry.Type {
	case proxy.LogTypeError:
		if entry.Error == nil {
			return ""
		}
		out.Message = entry.Error.Message
		out.Location = location(entry.Error.Source, entry.Error.LineNo, entry.Error.ColNo)
		out.Severity = "error"
		out.Timestamp = entry.Error.Timestamp.Format(time.RFC3339)

	case proxy.LogTypeHTTP:
		if entry.HTTP == nil {
			return ""
		}
		msg := entry.HTTP.Error
		if msg == "" {
			msg = entry.HTTP.ResponseBody
		}
		out.Message = fmt.Sprintf("%s %s → %d", entry.HTTP.Method, entry.HTTP.URL, entry.HTTP.StatusCode)
		if msg != "" {
			out.Message += ": " + truncate(msg, 120)
		}
		if entry.HTTP.StatusCode >= 500 {
			out.Severity = "error"
		} else if entry.HTTP.StatusCode >= 400 {
			out.Severity = "warning"
		}
		out.Timestamp = entry.HTTP.Timestamp.Format(time.RFC3339)

	case proxy.LogTypePanelMessage:
		if entry.PanelMessage == nil {
			return ""
		}
		out.Message = entry.PanelMessage.Message
		out.Timestamp = entry.PanelMessage.Timestamp.Format(time.RFC3339)

	case proxy.LogTypeInteraction:
		if entry.Interaction == nil {
			return ""
		}
		out.Message = fmt.Sprintf("%s %s", entry.Interaction.EventType, describeTarget(entry.Interaction.Target))
		out.Timestamp = entry.Interaction.Timestamp.Format(time.RFC3339)

	case proxy.LogTypeMutation:
		if entry.Mutation == nil {
			return ""
		}
		out.Message = fmt.Sprintf("%s %s %s", entry.Mutation.MutationType, entry.Mutation.Target.Tag, describeSelector(entry.Mutation.Target.Selector))
		out.Timestamp = entry.Mutation.Timestamp.Format(time.RFC3339)

	case proxy.LogTypeDesignChat:
		if entry.DesignChat == nil {
			return ""
		}
		out.Message = entry.DesignChat.Message
		out.Location = entry.DesignChat.Selector
		out.Timestamp = entry.DesignChat.Timestamp.Format(time.RFC3339)

	case proxy.LogTypeSketch:
		if entry.Sketch == nil {
			return ""
		}
		out.Message = fmt.Sprintf("%s (%d elements)", entry.Sketch.Description, entry.Sketch.ElementCount)
		out.Timestamp = entry.Sketch.Timestamp.Format(time.RFC3339)

	case proxy.LogTypeDiagnostic:
		if entry.Diagnostic == nil {
			return ""
		}
		out.Message = entry.Diagnostic.Message
		out.Severity = string(entry.Diagnostic.Level)
		out.Timestamp = entry.Diagnostic.Timestamp.Format(time.RFC3339)

	case proxy.LogTypeCustom:
		if entry.Custom == nil {
			return ""
		}
		out.Message = entry.Custom.Message
		out.Severity = entry.Custom.Level
		out.Timestamp = entry.Custom.Timestamp.Format(time.RFC3339)

	default:
		return ""
	}

	b, err := json.Marshal(out)
	if err != nil {
		return ""
	}
	return string(b)
}

// describeTarget renders an interaction target as a short selector string.
func describeTarget(t proxy.InteractionTarget) string {
	s := t.Tag
	if t.ID != "" {
		s += "#" + t.ID
	}
	text := strings.TrimSpace(t.Text)
	if text != "" {
		s += fmt.Sprintf(" %q", truncate(text, 40))
	} else if t.Selector != "" {
		s += " " + describeSelector(t.Selector)
	}
	return s
}

// describeSelector shortens a CSS selector for display.
func describeSelector(sel string) string {
	return truncate(sel, 60)
}

// location builds a source location string.
func location(source string, line, col int) string {
	if source == "" {
		return ""
	}
	if line > 0 && col > 0 {
		return fmt.Sprintf("%s:%d:%d", source, line, col)
	}
	if line > 0 {
		return fmt.Sprintf("%s:%d", source, line)
	}
	return source
}

// truncate shortens s to maxLen with ellipsis.
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}

// plural returns "s" if n != 1, else "".
func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
