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
	"unicode/utf8"

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
	monitorCmd.Flags().StringVar(&monitorTypes, "types", "", "Comma-separated event types (error,http,panel_message,design_chat,sketch,interaction,mutation,diagnostic,process,hook)")
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
		Global:    true,
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

// mutationCoalesceWindow / mutationCoalesceCap bound how many DOM mutations
// collapse into one compact summary line. A single page load emits per-node
// mutations by the hundred; streaming them one-per-line swamps the compact
// output and the downstream consumer's rate limit. Coalescing keeps the signal
// (something is mutating, how much) without the firehose.
const (
	mutationCoalesceWindow = 500 * time.Millisecond
	mutationCoalesceCap    = 50
)

// mutationCoalescer accumulates a burst of same-target mutations into a rolling
// summary. It is driven entirely by incoming events (no timer/goroutine, since
// the stream callback runs sequentially): a different target, a non-mutation
// event, the time window elapsing, or the cap forces a flush.
type mutationCoalescer struct {
	count   int
	nodes   int
	target  string
	firstAt time.Time
}

// add folds one mutation into the running total, returning a summary line to
// print first when the incoming mutation belongs to a new bucket.
func (c *mutationCoalescer) add(m *proxy.MutationEvent) (flush string) {
	target := describeSelector(m.Target.Selector)
	nodes := len(m.Added) + len(m.Removed)
	if c.count > 0 && (c.target != target ||
		m.Timestamp.Sub(c.firstAt) > mutationCoalesceWindow ||
		c.count >= mutationCoalesceCap) {
		flush = c.flush()
	}
	if c.count == 0 {
		c.target = target
		c.firstAt = m.Timestamp
	}
	c.count++
	c.nodes += nodes
	return flush
}

// flush emits the buffered summary (if any) and resets the accumulator.
func (c *mutationCoalescer) flush() string {
	if c.count == 0 {
		return ""
	}
	line := fmt.Sprintf("[mutation] %d change%s (%d node%s) on %s",
		c.count, plural(c.count), c.nodes, plural(c.nodes), c.target)
	c.count, c.nodes, c.target = 0, 0, ""
	return line
}

// streamOnce connects to the daemon and streams events until an error or cancellation.
func streamOnce(ctx context.Context, client *daemon.Client, filter protocol.StreamEventFilter) error {
	if err := client.Connect(); err != nil {
		return fmt.Errorf("connect failed: %w", err)
	}
	defer client.Close()

	fmt.Fprintf(os.Stderr, "monitor: connected to %s\n", client.SocketPath())

	mc := &mutationCoalescer{}
	printLine := func(line string) {
		if line != "" {
			fmt.Println(line)
		}
	}

	err := client.StreamEvents(ctx, filter, func(entry proxy.LogEntry) error {
		// JSON output stays 1:1 (machine-readable); coalesce only the compact
		// stream, which is what a human/agent consumer reads.
		if monitorFormat == "compact" {
			if entry.Type == proxy.LogTypeMutation && entry.Mutation != nil {
				printLine(mc.add(entry.Mutation))
				return nil
			}
			// A non-mutation event flushes any pending summary first so the
			// coalesced line keeps its place in the timeline.
			printLine(mc.flush())
		}

		if monitorFormat == "json" {
			printLine(formatJSON(entry))
		} else {
			printLine(formatCompact(entry))
		}
		return nil
	})

	// Flush a mutation burst still buffered when the stream ends (page went
	// quiet or the connection dropped mid-burst).
	printLine(mc.flush())
	return err
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
		if msg == "" {
			msg = httpBodyPreview(h, 80)
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

	case proxy.LogTypeProcessOutput:
		if entry.ProcessOutput == nil {
			return ""
		}
		po := entry.ProcessOutput
		return fmt.Sprintf("[process:%s] %s", po.ProcessID, po.Line)

	case proxy.LogTypeHook:
		if entry.Hook == nil {
			return ""
		}
		h := entry.Hook
		// Compact form mirrors the other categories: tag the line with
		// the type plus event name, then the cheapest provenance hint
		// (session ID if present, otherwise agent). Payload bytes are
		// deliberately omitted from the compact view — they're free-
		// form JSON and would blow the line budget. Use --format json
		// to get the full payload.
		hint := h.SessionID
		if hint == "" {
			hint = h.Agent
		}
		if hint != "" {
			return fmt.Sprintf("[hook:%s] %s", h.Event, hint)
		}
		return fmt.Sprintf("[hook:%s]", h.Event)

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
			msg = httpBodyPreview(entry.HTTP, 120)
		}
		out.Message = fmt.Sprintf("%s %s → %d", entry.HTTP.Method, entry.HTTP.URL, entry.HTTP.StatusCode)
		if msg != "" {
			out.Message += ": " + msg
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

	case proxy.LogTypeProcessOutput:
		if entry.ProcessOutput == nil {
			return ""
		}
		out.Message = entry.ProcessOutput.Line
		out.Location = entry.ProcessOutput.ProcessID
		out.Timestamp = entry.ProcessOutput.Timestamp.Format(time.RFC3339)

	case proxy.LogTypeHook:
		if entry.Hook == nil {
			return ""
		}
		h := entry.Hook
		out.Message = h.Event
		// Location carries the cheapest provenance hint so JSON
		// consumers (jq pipelines, dashboards) can correlate events
		// to a session/agent without reaching into the payload.
		switch {
		case h.SessionID != "":
			out.Location = h.SessionID
		case h.Agent != "":
			out.Location = h.Agent
		case h.ProjectPath != "":
			out.Location = h.ProjectPath
		}
		out.Timestamp = h.ReceivedAt.Format(time.RFC3339)

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

// httpBodyPreview returns a short, log-safe preview of an HTTP response body.
// The proxy's traffic recorder captures the on-the-wire bytes, which stay
// compressed for any response the proxy did not decompress for HTML injection
// (JSON APIs, JS/CSS assets, …). Dumping those raw prints gzip/br/zstd garbage
// into the monitor stream, so a compressed or otherwise non-text body is
// rendered as a byte-count placeholder instead. Returns "" for an empty body.
func httpBodyPreview(h *proxy.HTTPLogEntry, maxLen int) string {
	body := h.ResponseBody
	if body == "" {
		return ""
	}
	if enc := strings.ToLower(strings.TrimSpace(h.ResponseHeaders["Content-Encoding"])); enc != "" && enc != "identity" {
		return fmt.Sprintf("<%s-encoded, %d bytes>", enc, len(body))
	}
	if !isPrintableText(body) {
		return fmt.Sprintf("<binary, %d bytes>", len(body))
	}
	return truncate(body, maxLen)
}

// isPrintableText reports whether s is valid UTF-8 with no control bytes (other
// than the usual whitespace) in its leading sample — a cheap "is this safe to
// print in a log line" check that rejects compressed/binary payloads.
func isPrintableText(s string) bool {
	if !utf8.ValidString(s) {
		return false
	}
	n := len(s)
	if n > 512 {
		n = 512
	}
	for i := 0; i < n; i++ {
		c := s[i]
		if c == '\n' || c == '\r' || c == '\t' {
			continue
		}
		if c < 0x20 || c == 0x7f {
			return false
		}
	}
	return true
}

// plural returns "s" if n != 1, else "".
func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
