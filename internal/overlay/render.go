package overlay

import (
	"bytes"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// proxyListenConflict returns the proxy's listen port (as a string) when the
// ports inventory classifies that port as a conflict (held by an unmanaged
// process), otherwise "". Ties the ports panel's classification to the proxy UI.
func proxyListenConflict(proxy *ProxyInfo, ports []PortInfo) string {
	idx := strings.LastIndex(proxy.ListenAddr, ":")
	if idx == -1 {
		return ""
	}
	portStr := proxy.ListenAddr[idx+1:]
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return ""
	}
	for _, pi := range ports {
		if pi.Port == port && pi.Status == "conflict" {
			return portStr
		}
	}
	return ""
}

// ANSI escape sequences.
const (
	// Cursor control
	CursorHide     = "\x1b[?25l"
	CursorShow     = "\x1b[?25h"
	CursorSave     = "\x1b[s"
	CursorRestore  = "\x1b[u"
	CursorHome     = "\x1b[H"
	CursorToFormat = "\x1b[%d;%dH" // row;col (1-indexed)

	// Screen control
	ClearScreen    = "\x1b[2J"
	ClearLine      = "\x1b[2K"
	ClearToEOL     = "\x1b[K"
	ScrollRegion   = "\x1b[%d;%dr" // top;bottom
	ResetScroll    = "\x1b[r"
	EnterAltScreen = "\x1b[?1049h"
	ExitAltScreen  = "\x1b[?1049l"

	// Text attributes
	Reset     = "\x1b[0m"
	Bold      = "\x1b[1m"
	Dim       = "\x1b[2m"
	Italic    = "\x1b[3m"
	Underline = "\x1b[4m"
	Blink     = "\x1b[5m"
	Reverse   = "\x1b[7m"

	// Foreground colors (basic)
	FgBlack   = "\x1b[30m"
	FgRed     = "\x1b[31m"
	FgGreen   = "\x1b[32m"
	FgYellow  = "\x1b[33m"
	FgBlue    = "\x1b[34m"
	FgMagenta = "\x1b[35m"
	FgCyan    = "\x1b[36m"
	FgWhite   = "\x1b[37m"
	FgDefault = "\x1b[39m"

	// Bright foreground colors
	FgBrightBlack   = "\x1b[90m"
	FgBrightRed     = "\x1b[91m"
	FgBrightGreen   = "\x1b[92m"
	FgBrightYellow  = "\x1b[93m"
	FgBrightBlue    = "\x1b[94m"
	FgBrightMagenta = "\x1b[95m"
	FgBrightCyan    = "\x1b[96m"
	FgBrightWhite   = "\x1b[97m"

	// Background colors (basic)
	BgBlack   = "\x1b[40m"
	BgRed     = "\x1b[41m"
	BgGreen   = "\x1b[42m"
	BgYellow  = "\x1b[43m"
	BgBlue    = "\x1b[44m"
	BgMagenta = "\x1b[45m"
	BgCyan    = "\x1b[46m"
	BgWhite   = "\x1b[47m"
	BgDefault = "\x1b[49m"

	// Bright background colors
	BgBrightBlack = "\x1b[100m"
)

// Box drawing characters (Unicode).
const (
	BoxHorizontal       = "─"
	BoxVertical         = "│"
	BoxTopLeft          = "┌"
	BoxTopRight         = "┐"
	BoxBottomLeft       = "└"
	BoxBottomRight      = "┘"
	BoxVerticalRight    = "├"
	BoxVerticalLeft     = "┤"
	BoxHorizontalDown   = "┬"
	BoxHorizontalUp     = "┴"
	BoxCross            = "┼"
	BoxDoubleHorizontal = "═"
	BoxDoubleVertical   = "║"

	// Rounded box drawing characters (niri-style).
	RoundTopLeft     = "╭"
	RoundTopRight    = "╮"
	RoundBottomLeft  = "╰"
	RoundBottomRight = "╯"
)

// Status icons.
const (
	IconConnected    = "●"
	IconDisconnected = "○"
	IconError        = "✗"
	IconWarning      = "⚠"
	IconProcess      = "⚙"
	IconProxy        = "⇄"
	IconOK           = "✓"
)

// Overlay region names for tracking.
const (
	RegionMenu  = "menu"
	RegionInput = "input"
)

// StateColorCode returns the ANSI color code for a process state.
func StateColorCode(state string) string {
	switch state {
	case "running":
		return FgGreen
	case "failed":
		return FgRed
	case "stopped":
		return FgYellow
	default:
		return FgBrightBlack
	}
}

// scriptEmojiRules maps script name/command substrings to emoji labels.
// Checked in order; first match wins. Both the script name and command are searched.
var scriptEmojiRules = []struct {
	substring string
	emoji     string
}{
	{"test", "🧪"},
	{"lint", "🔍"},
	{"check", "🔍"},
	{"build", "🔨"},
	{"compile", "🔨"},
	{"watch", "👁"},
	{"dev", "🚀"},
	{"start", "🚀"},
	{"serve", "🌐"},
	{"server", "🌐"},
	{"web", "🌐"},
	{"api", "📡"},
	{"gate", "📡"},
	{"db", "🗄"},
	{"database", "🗄"},
	{"migrate", "🗄"},
	{"docker", "🐳"},
	{"compose", "🐳"},
	{"redis", "📮"},
	{"queue", "📮"},
	{"worker", "⛏"},
	{"job", "⛏"},
	{"cron", "⏰"},
	{"schedule", "⏰"},
	{"log", "📋"},
	{"monitor", "📋"},
	{"dotnet", "🟣"},
	{"aspnet", "🟣"},
	{"next", "▲"},
	{"vite", "⚡"},
	{"webpack", "📦"},
	{"lib", "📚"},
	{"docs", "📖"},
	{"storybook", "📖"},
	{"format", "✨"},
	{"deploy", "🚢"},
	{"publish", "🚢"},
}

// processEmoji returns a contextual emoji for a process based on its name or command.
func processEmoji(name, command string) string {
	lower := strings.ToLower(name + " " + command)
	for _, rule := range scriptEmojiRules {
		if strings.Contains(lower, rule.substring) {
			return rule.emoji
		}
	}
	return "⚙"
}

// stripProjectPrefix removes the "project-hash:" prefix from a process/proxy ID.
// IDs use the format "project-hash:name" (e.g., "bifrost-9cf0:dev"). Since the
// overlay is scoped to the current project, the prefix is redundant for display.
func stripProjectPrefix(id string) string {
	if idx := strings.Index(id, ":"); idx >= 0 {
		return id[idx+1:]
	}
	return id
}

// startingAnimFrames are cycled through when a process is in "starting" state.
// The animation provides a subtle visual pulse using different circle glyphs.
var startingAnimFrames = []string{"\u25cc", "\u25ce", "\u25c9", "\u25ce"}

// restartingAnimFrames are cycled through when a process is in "restarting" state.
var restartingAnimFrames = []string{"\u25cc", "\u25ce", "\u25c9", "\u25ce"}

// processStateIcon returns a distinct shape and color for a process state.
// Uses different shapes for accessibility (not just color).
// When hasAlerts is true and state is "running", shows a warning indicator.
// The frame parameter drives animation for starting/restarting states.
func processStateIcon(state string, hasAlerts bool, frame int) (icon, color string) {
	switch state {
	case "running":
		if hasAlerts {
			return "\u25a0", FgYellow // Filled square = running with errors
		}
		return "\u25cf", FgGreen // Filled circle = running
	case "failed":
		return "\u2717", FgRed // X = crashed/exited with error
	case "stopped":
		return "\u2717", FgYellow // X = stopped/exited
	case "starting":
		idx := frame % len(startingAnimFrames)
		return startingAnimFrames[idx], FgCyan // Animated circle = starting
	case "restarting":
		idx := frame % len(restartingAnimFrames)
		return restartingAnimFrames[idx], FgYellow // Animated circle = restarting
	case "idle":
		return "\u25cb", FgBrightBlack // Empty circle = idle/never started
	default:
		return "\u25cb", FgBrightBlack // Empty circle = unknown/pending
	}
}

// aggregatedURL contains a URL with its extracted port.
type aggregatedURL struct {
	URL  string
	Port string
}

// aggregateProcessURLs collects unique URLs from running processes, deduplicating by port.
// Prefers localhost URLs over other addresses for the same port.
func aggregateProcessURLs(processes []ProcessInfo) []aggregatedURL {
	urlsByPort := make(map[string]string)
	for _, p := range processes {
		if p.State != "running" {
			continue
		}
		for _, u := range p.URLs {
			port := extractPort(u)
			if port == "" {
				continue
			}
			if existing, ok := urlsByPort[port]; !ok {
				urlsByPort[port] = u
			} else if isLocalhostURL(u) && !isLocalhostURL(existing) {
				urlsByPort[port] = u
			}
		}
	}
	result := make([]aggregatedURL, 0, len(urlsByPort))
	for port, url := range urlsByPort {
		result = append(result, aggregatedURL{URL: url, Port: port})
	}
	// Sort by port for stable ordering
	sort.Slice(result, func(i, j int) bool {
		return result[i].Port < result[j].Port
	})
	return result
}

// Renderer handles drawing to the terminal.
type Renderer struct {
	out          io.Writer
	width        int
	height       int
	version      string // Version string for dashboard title
	mu           sync.Mutex
	screenMgr    *ScreenManager
	overlayStack *OverlayStack

	// Track current overlay regions for proper clearing
	currentMenuRegion  *ScreenRegion
	currentInputRegion *ScreenRegion

	// Diff-based panel refresh state: cached visible lines from last render
	lastPanelLines []string
	lastPanelStart int // startRow of the cached content area
	lastPanelCol   int // column of the cached content area
	lastPanelWidth int // width of the cached content area
	lastPanelAvail int // available lines in the content area

	// Animation frame counter for starting-state indicator. Incremented
	// atomically on each DrawIndicator call; read by processStateIcon
	// to produce frame-based animation when a process is starting.
	animFrame atomic.Int32

	// buf accumulates ANSI output under lock so the terminal write
	// happens after the lock is released. Set by beginBuffer, flushed
	// by flushBuffer. Nil when not buffering.
	buf *bytes.Buffer
}

// NewRenderer creates a new Renderer.
func NewRenderer(out io.Writer, width, height int) *Renderer {
	sm := NewScreenManager(out, width, height)
	return &Renderer{
		out:          out,
		width:        width,
		height:       height,
		screenMgr:    sm,
		overlayStack: NewOverlayStack(sm),
	}
}

// SetVersion sets the version displayed in the dashboard title.
func (r *Renderer) SetVersion(version string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.version = version
}

// SetOutput changes the writer used for rendering. This is used to route
// output through a synchronized writer (e.g. OutputGate.WriteDirect) to
// prevent interleaving with PTY output on the same stdout.
func (r *Renderer) SetOutput(out io.Writer) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.out = out
	r.screenMgr.SetOutput(out)
}

// SetSize updates the terminal dimensions.
func (r *Renderer) SetSize(width, height int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.width = width
	r.height = height
	r.screenMgr.SetSize(width, height)
}

// write outputs a string. When a buffer is active (beginBuffer was called)
// the data goes to the in-memory buffer; otherwise it writes directly to r.out.
// Caller must hold r.mu.
func (r *Renderer) write(s string) {
	if r.buf != nil {
		r.buf.WriteString(s)
		return
	}
	io.WriteString(r.out, s)
}

// beginBuffer starts buffering all write calls into an in-memory buffer.
// Caller must hold r.mu. Pair with flushBuffer after releasing the lock.
func (r *Renderer) beginBuffer() {
	r.buf = &bytes.Buffer{}
}

// flushBuffer writes the accumulated buffer to r.out and clears the buffer.
// Must be called WITHOUT holding r.mu (that is the whole point — keep the
// slow terminal I/O outside the lock).
func (r *Renderer) flushBuffer(buf *bytes.Buffer) {
	if buf.Len() > 0 {
		r.out.Write(buf.Bytes())
	}
}

// moveTo moves cursor to row, col (1-indexed).
func (r *Renderer) moveTo(row, col int) {
	r.write(fmt.Sprintf(CursorToFormat, row, col))
}

// DrawIndicator draws the status indicator bar at the bottom of the screen.
func (r *Renderer) DrawIndicator(status Status) {
	r.mu.Lock()
	r.beginBuffer()

	// Advance animation frame on each draw cycle
	frame := int(r.animFrame.Add(1))

	// Save cursor, hide it, move to bottom
	r.write(CursorSave + CursorHide)
	r.moveTo(r.height, 1)
	r.write(ClearLine)

	// Build status bar content
	var parts []string

	// Daemon connection status: emoji + status icon
	switch status.DaemonConnected {
	case ConnectionConnected:
		parts = append(parts, fmt.Sprintf("🔗 %s%s%s", FgGreen, IconConnected, Reset))
	case ConnectionDisconnected:
		parts = append(parts, fmt.Sprintf("🔗 %s%s%s", FgYellow, IconDisconnected, Reset))
	case ConnectionError:
		parts = append(parts, fmt.Sprintf("🔗 %s%s%s", FgRed, IconError, Reset))
	default:
		parts = append(parts, fmt.Sprintf("🔗 %s%s%s", FgBrightBlack, IconDisconnected, Reset))
	}

	// Per-script state icons with contextual emoji (persistent across restarts)
	for _, s := range status.Scripts {
		icon, color := processStateIcon(s.State, s.HasAlerts, frame)
		label := processEmoji(s.Name, s.Command)
		parts = append(parts, fmt.Sprintf("%s %s%s%s", label, color, icon, Reset))
	}

	// Running proxies — collect URLs for display
	aggregatedURLs := aggregateProcessURLs(status.Processes)
	errorProxyCount := 0
	var proxyURL string
	var tunnelURL string
	for _, p := range status.Proxies {
		if p.HasErrors {
			errorProxyCount++
		}
		if proxyURL == "" && p.ListenAddr != "" {
			proxyURL = "http://" + NormalizeListenAddr(p.ListenAddr)
		}
		if tunnelURL == "" && p.TunnelURL != "" {
			tunnelURL = p.TunnelURL
		}
	}

	// Build URL display: proxy URL (or tunnel) + process URLs
	proxyCount := len(status.Proxies)
	var urlParts []string

	if proxyCount > 0 {
		displayURL := tunnelURL
		if displayURL == "" {
			displayURL = proxyURL
		}
		urlColor := FgBrightCyan
		if tunnelURL != "" {
			urlColor = FgBrightMagenta
		}
		if displayURL != "" {
			urlParts = append(urlParts, fmt.Sprintf("%s%s%s", urlColor+Underline, displayURL, Reset))
		}
	}

	for _, au := range aggregatedURLs {
		normalized := normalizeProcessURL(au.URL)
		if normalized != "" {
			urlParts = append(urlParts, fmt.Sprintf("%s%s%s", FgCyan+Underline, normalized, Reset))
		}
	}

	// Display URLs section
	if len(urlParts) > 0 {
		urlDisplay := strings.Join(urlParts, fmt.Sprintf(" %s·%s ", FgBrightBlack, Reset))
		if errorProxyCount > 0 {
			parts = append(parts, fmt.Sprintf("%s%s%s %s %s(%d err)%s",
				FgMagenta, IconProxy, Reset, urlDisplay, FgRed, errorProxyCount, Reset))
		} else {
			parts = append(parts, fmt.Sprintf("%s%s%s %s",
				FgMagenta, IconProxy, Reset, urlDisplay))
		}
	} else if proxyCount > 0 {
		// No URLs to show, just show count
		if errorProxyCount > 0 {
			parts = append(parts, fmt.Sprintf("%s%s %d proxy%s %s(%d err)%s",
				FgMagenta, IconProxy, proxyCount, Reset, FgRed, errorProxyCount, Reset))
		} else {
			parts = append(parts, fmt.Sprintf("%s%s %d proxy%s", FgMagenta, IconProxy, proxyCount, Reset))
		}
	}

	// Recent errors
	recentErrors := 0
	cutoff := time.Now().Add(-5 * time.Minute)
	for _, e := range status.RecentErrors {
		if e.Timestamp.After(cutoff) {
			recentErrors++
		}
	}
	if recentErrors > 0 {
		parts = append(parts, fmt.Sprintf("%s%s %d errors%s", FgRed, IconWarning, recentErrors, Reset))
	}

	// Join parts with separator
	statusText := strings.Join(parts, fmt.Sprintf(" %s│%s ", FgBrightBlack, Reset))

	// Navigation hint on the right (constant: "Ctrl+→" = 6 visible chars)
	const hotkeyStr = "Ctrl+→"
	const hotkeyLen = 6
	hotkeyHint := FgBrightBlack + hotkeyStr + Reset

	// Calculate available space: width minus leading space, hotkey, trailing space, and separator
	maxStatusVisible := r.width - hotkeyLen - 4 // 4 for " " prefix, " │ " separator, " " suffix

	// Truncate status text if it would cause the bar to wrap
	visibleLen := r.estimateVisibleLength(statusText)
	if maxStatusVisible < 4 {
		// Terminal too narrow for any content, just show hotkey
		statusText = ""
		visibleLen = 0
	} else if visibleLen > maxStatusVisible {
		statusText = r.truncateANSI(statusText, maxStatusVisible-1) + "…"
		visibleLen = maxStatusVisible
	}

	padding := r.width - visibleLen - hotkeyLen - 4
	if padding < 1 {
		padding = 1
	}

	// Draw the bar with background
	r.write(BgBrightBlack + FgWhite)
	r.write(" " + statusText)
	r.write(strings.Repeat(" ", padding))
	r.write(hotkeyHint)
	r.write(" " + Reset)

	// Restore cursor
	r.write(CursorRestore + CursorShow)

	buf := r.buf
	r.buf = nil
	r.mu.Unlock()
	r.flushBuffer(buf)
}

// NormalizeListenAddr converts wildcard and loopback addresses to localhost for clickable URLs.
// This is the most reliable option since LAN IPs can be unreliable in virtual environments
// (WSL2, Docker, etc.). Users who need LAN access can check the detailed proxy output.
func NormalizeListenAddr(addr string) string {
	var port string

	// Extract port from wildcard/loopback addresses (port includes the colon)
	if strings.HasPrefix(addr, "[::]:") {
		port = addr[4:] // Get :port part
	} else if strings.HasPrefix(addr, "0.0.0.0:") {
		port = addr[7:] // Get :port part
	} else if strings.HasPrefix(addr, "127.0.0.1:") {
		port = addr[9:] // Get :port part
	} else if addr == "[::]" || addr == "0.0.0.0" || addr == "127.0.0.1" {
		port = ""
	} else {
		// Not a wildcard/loopback address, return as-is
		return addr
	}

	return "localhost" + port
}

// normalizeProcessURL normalizes a URL from process output.
// It converts IP addresses to localhost and returns a clickable URL.
// E.g., "http://127.0.0.1:3847" → "http://localhost:3847"
func normalizeProcessURL(urlStr string) string {
	// Extract protocol and address
	protocol := "http://"
	addr := urlStr

	if strings.HasPrefix(addr, "http://") {
		addr = addr[7:]
	} else if strings.HasPrefix(addr, "https://") {
		protocol = "https://"
		addr = addr[8:]
	}

	// Normalize localhost variants
	addr = NormalizeListenAddr(addr)

	return protocol + addr
}

// extractPort extracts the port from a URL string.
// Returns empty string if no port found.
func extractPort(urlStr string) string {
	// Strip protocol
	addr := urlStr
	if strings.HasPrefix(addr, "http://") {
		addr = addr[7:]
	} else if strings.HasPrefix(addr, "https://") {
		addr = addr[8:]
	}

	// Find the last colon (port separator)
	lastColon := strings.LastIndex(addr, ":")
	if lastColon == -1 {
		return ""
	}

	// Extract port (everything after the colon, before any path)
	port := addr[lastColon+1:]
	if slashIdx := strings.Index(port, "/"); slashIdx != -1 {
		port = port[:slashIdx]
	}

	return port
}

// isLocalhostURL checks if a URL uses localhost or loopback address.
func isLocalhostURL(urlStr string) bool {
	return strings.Contains(urlStr, "localhost") ||
		strings.Contains(urlStr, "127.0.0.1") ||
		strings.Contains(urlStr, "[::1]")
}

// estimateVisibleLength estimates the visible cell width of a string with ANSI codes.
// Emoji and other wide characters count as 2 cells (matching terminal rendering).
func (r *Renderer) estimateVisibleLength(s string) int {
	inEscape := false
	length := 0
	for _, ch := range s {
		if ch == '\x1b' {
			inEscape = true
			continue
		}
		if inEscape {
			if (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') {
				inEscape = false
			}
			continue
		}
		if isWideChar(ch) {
			length += 2
		} else {
			length++
		}
	}
	return length
}

// isWideChar returns true for characters that occupy 2 cells in a terminal.
// Based on Unicode East Asian Width: only W (Wide) and F (Fullwidth) characters.
// BMP symbols like ⚙ (U+2699), ⚠ (U+26A0), ✗, ● are narrow (1 cell).
func isWideChar(ch rune) bool {
	// Supplementary emoji (U+1F300–U+1FBFF): all wide
	if ch >= 0x1F300 && ch <= 0x1FBFF {
		return true
	}
	// CJK Unified Ideographs (U+4E00–U+9FFF)
	if ch >= 0x4E00 && ch <= 0x9FFF {
		return true
	}
	// CJK Extension A (U+3400–U+4DBF)
	if ch >= 0x3400 && ch <= 0x4DBF {
		return true
	}
	// CJK Compatibility Ideographs (U+F900–U+FAFF)
	if ch >= 0xF900 && ch <= 0xFAFF {
		return true
	}
	// Fullwidth forms (U+FF01–U+FF60)
	if ch >= 0xFF01 && ch <= 0xFF60 {
		return true
	}
	// Hangul Syllables (U+AC00–U+D7AF)
	if ch >= 0xAC00 && ch <= 0xD7AF {
		return true
	}
	// CJK Extension B+ (U+20000–U+2FA1F)
	if ch >= 0x20000 && ch <= 0x2FA1F {
		return true
	}
	return false
}

// truncateANSI truncates a string containing ANSI escape codes to maxVisible
// visible characters. ANSI sequences are preserved up to the cut point, and a
// Reset is appended to close any open attributes.
func (r *Renderer) truncateANSI(s string, maxVisible int) string {
	var buf strings.Builder
	inEscape := false
	visible := 0
	for _, ch := range s {
		if visible >= maxVisible && !inEscape {
			break
		}
		buf.WriteRune(ch)
		if ch == '\x1b' {
			inEscape = true
			continue
		}
		if inEscape {
			if (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') {
				inEscape = false
			}
			continue
		}
		if isWideChar(ch) {
			visible += 2
		} else {
			visible++
		}
	}
	buf.WriteString(Reset)
	return buf.String()
}

// ClearIndicator clears the indicator bar.
func (r *Renderer) ClearIndicator() {
	r.mu.Lock()
	r.beginBuffer()

	r.write(CursorSave + CursorHide)
	r.moveTo(r.height, 1)
	r.write(ClearLine)
	r.write(CursorRestore + CursorShow)

	buf := r.buf
	r.buf = nil
	r.mu.Unlock()
	r.flushBuffer(buf)
}

// ClearScreen clears the entire screen and resets cursor to home.
func (r *Renderer) ClearScreen() {
	r.mu.Lock()
	r.beginBuffer()

	// Clear entire screen, move cursor home, reset scroll region
	r.write(ClearScreen + CursorHome + ResetScroll)

	buf := r.buf
	r.buf = nil
	r.mu.Unlock()
	r.flushBuffer(buf)
}

// ClearVisible clears the visible screen and moves cursor home.
// Unlike ClearScreen, this preserves the scroll region.
func (r *Renderer) ClearVisible() {
	r.mu.Lock()
	r.beginBuffer()

	r.write(ClearScreen + CursorHome)

	buf := r.buf
	r.buf = nil
	r.mu.Unlock()
	r.flushBuffer(buf)
}

// EnterAltScreen switches to the alternate screen buffer.
// The main screen content is preserved and restored when ExitAltScreen is called.
func (r *Renderer) EnterAltScreen() {
	r.mu.Lock()
	r.beginBuffer()
	r.write(EnterAltScreen + CursorHome)
	buf := r.buf
	r.buf = nil
	r.mu.Unlock()
	r.flushBuffer(buf)
}

// ExitAltScreen switches back to the main screen buffer.
// The main screen content that was preserved when EnterAltScreen was called is restored.
func (r *Renderer) ExitAltScreen() {
	r.mu.Lock()
	r.beginBuffer()
	r.write(ExitAltScreen)
	buf := r.buf
	r.buf = nil
	r.mu.Unlock()
	r.flushBuffer(buf)
}

// DrawPanelView draws a full-screen panel view with a niri-style tab bar at top.
// Panels are arranged horizontally like niri columns: Ctrl+Left/Right to navigate.
func (r *Renderer) DrawPanelView(panels []PanelItem, activeIndex int, status Status, overviewSelectedIdx int, commandInput bool, commandBuffer string, commandSelectedIdx int, showAllPorts bool, actions OverviewActions) {
	r.mu.Lock()
	r.beginBuffer()

	r.write(ClearScreen + CursorHome + CursorHide)
	// Full redraw invalidates diff cache; drawScrollableContent will repopulate it
	r.lastPanelLines = nil

	if activeIndex >= len(panels) {
		activeIndex = len(panels) - 1
	}
	if activeIndex < 0 {
		activeIndex = 0
	}

	active := panels[activeIndex]

	// === TAB BAR (row 1-2) ===
	r.drawPanelTabBar(panels, activeIndex)

	// === CONTENT PANEL ===
	const contentStartRow = 3
	const margin = 1
	panelWidth := r.width - margin*2
	panelHeight := r.height - contentStartRow - 1 // Leave room for footer
	panelCol := margin + 1

	// Choose gradient based on panel type
	grad := gradientMenu
	switch active.Type {
	case "process":
		grad = gradientProcess
	case "proxy":
		grad = gradientProxy
	}

	// Draw the content panel
	title := active.Label
	switch active.Type {
	case "overview", "summary":
		// title is the label ("overview" / "summary")
	case "log":
		title = "session log"
	default:
		title = active.Type + ": " + active.ID
	}
	r.drawNiriPanel(contentStartRow, panelCol, panelWidth, panelHeight, title, grad)

	// Fill content based on panel type
	contentRow := contentStartRow + 1
	contentWidth := panelWidth - 4

	switch active.Type {
	case "overview":
		r.drawOverviewContent(contentRow, panelCol+2, contentWidth, panelHeight-2, status, overviewSelectedIdx, commandInput, commandBuffer, commandSelectedIdx, showAllPorts, actions)
	case "process":
		r.drawProcessPanelContent(contentRow, panelCol+2, contentWidth, panelHeight-2, active, status)
	case "proxy":
		r.drawProxyPanelContent(contentRow, panelCol+2, contentWidth, panelHeight-2, active, status)
	case "log", "summary":
		r.drawScrollableContent(contentRow, panelCol+2, contentWidth, panelHeight-2, active)
	}

	// === FOOTER ===
	// The keybind hint sits one row above the bottom, leaving the protected
	// bottom row (r.height) for the global status bar drawn by DrawIndicator.
	footerRow := r.height - 1
	r.moveTo(footerRow, 1)
	r.write(BgBrightBlack + FgWhite)
	var hint string
	if active.Type == "overview" {
		// Build the overview hint contextually: script actions only when a
		// script is selected, summarize only when configured, reconnect only
		// when disconnected.
		parts := []string{}
		if len(status.Scripts) > 0 {
			parts = append(parts, "↑↓ Select", "Enter Open", "a/s/r Script", ": Cmd")
		}
		if actions.SummarizeEnabled {
			parts = append(parts, "m Summarize")
		}
		if status.DaemonConnected != ConnectionConnected {
			parts = append(parts, "c Reconnect")
		}
		parts = append(parts, "Ctrl+→ Panels", "Esc Exit")
		hint = fmt.Sprintf(" %s  (%d/%d) ", strings.Join(parts, "  "), activeIndex+1, len(panels))
	} else {
		hint = fmt.Sprintf(" Tab Navigate  ↑↓ Scroll  1-9 Jump  x Close stopped  Esc Exit  (%d/%d) ", activeIndex+1, len(panels))
	}
	hint = r.padRight(hint, r.width)
	r.write(hint)
	r.write(Reset)

	r.write(CursorShow)

	buf := r.buf
	r.buf = nil
	r.mu.Unlock()
	r.flushBuffer(buf)
}

// drawPanelTabBar draws the horizontal tab bar showing all panels.
func (r *Renderer) drawPanelTabBar(panels []PanelItem, activeIndex int) {
	// Row 1: Tab bar with panel labels
	r.moveTo(1, 1)
	r.write(ClearLine)

	// Build tab bar fitting within screen width
	var tabBar strings.Builder
	tabBar.WriteString(" ")

	for i, panel := range panels {
		label := panel.Label

		// Choose tab style based on type
		var tabColor string
		switch panel.Type {
		case "process":
			tabColor = gradientProcess.fromFg
		case "proxy":
			tabColor = gradientProxy.fromFg
		case "overview":
			tabColor = gradientMenu.fromFg
		default:
			tabColor = FgBrightBlack
		}

		if i == activeIndex {
			// Active tab: bold with underline, brighter
			tabBar.WriteString(tabColor + Bold + Underline)
			tabBar.WriteString(" " + label + " ")
			tabBar.WriteString(Reset)
		} else {
			// Inactive tab: dimmed
			tabBar.WriteString(FgBrightBlack)
			tabBar.WriteString(" " + label + " ")
			tabBar.WriteString(Reset)
		}

		// Tab separator
		if i < len(panels)-1 {
			tabBar.WriteString(FgBrightBlack + "│" + Reset)
		}
	}

	r.write(tabBar.String())

	// Row 2: Thin separator line with gradient
	r.moveTo(2, 1)
	r.write(ClearLine)

	// Draw a gradient separator spanning the width
	half := r.width / 2
	for i := 0; i < r.width; i++ {
		if i < half {
			r.write(gradientProcess.fromFg + "─" + Reset)
		} else {
			r.write(gradientProxy.fromFg + "─" + Reset)
		}
	}
}

// overviewSpinner returns the braille spinner frame for the given tick index.
func overviewSpinner(frame int) string {
	frames := []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}
	if frame < 0 {
		frame = -frame
	}
	return frames[frame%len(frames)]
}

// noticeTruncate clamps s to max display columns with an ellipsis.
func noticeTruncate(s string, max int) string {
	if max < 1 {
		return ""
	}
	if len(s) <= max {
		return s
	}
	if max < 2 {
		return s[:max]
	}
	return s[:max-1] + "…"
}

// drawNoticeBanner renders the dismissable silent-failure notices at the top of
// the overview. Returns the next free row. Renders nothing (and consumes no
// rows) when there are no notices. The [n] index matches the :dismiss <n>
// argument, which operates on this same visible order.
func (r *Renderer) drawNoticeBanner(row, col, width, rowLimit int, notices []NoticeInfo) int {
	if len(notices) == 0 || row >= rowLimit {
		return row
	}
	const maxVisible = 3

	word := "issue"
	if len(notices) != 1 {
		word = "issues"
	}
	r.moveTo(row, col)
	r.write(fmt.Sprintf("%s%s %d %s%s %s· :dismiss <n> · :dismiss-all%s",
		FgRed, IconWarning, len(notices), word, Reset, FgBrightBlack, Reset))
	row++

	shown := 0
	for i, n := range notices {
		if row >= rowLimit || shown >= maxVisible {
			break
		}
		sevColor := FgRed
		if n.Severity == "warning" {
			sevColor = FgYellow
		}
		r.moveTo(row, col)
		r.write(fmt.Sprintf(" %s[%d]%s %s%s%s",
			FgBrightBlack, i+1, Reset, sevColor, noticeTruncate(n.Summary, width-6), Reset))
		row++

		hint := n.Remediation
		if hint == "" {
			hint = n.Detail
		}
		if hint != "" && row < rowLimit {
			r.moveTo(row, col)
			r.write(fmt.Sprintf("     %s↳ %s%s", FgBrightBlack, noticeTruncate(hint, width-8), Reset))
			row++
		}
		shown++
	}
	if remaining := len(notices) - shown; remaining > 0 && row < rowLimit {
		r.moveTo(row, col)
		r.write(fmt.Sprintf("     %s+%d more · :dismiss-all%s", FgBrightBlack, remaining, Reset))
		row++
	}
	row++ // blank separator before connection status
	return row
}

// drawOverviewContent draws the overview panel content (system summary).
// selectedIdx is the highlighted script row index (0-based, -1 for no selection).
func (r *Renderer) drawOverviewContent(startRow, col, width, maxRows int, status Status, selectedIdx int, commandInput bool, commandBuffer string, commandSelectedIdx int, showAllPorts bool, actions OverviewActions) {
	row := startRow
	spinner := overviewSpinner(actions.SpinnerFrame)

	// Silent-failure notice banner (dismissable) — most prominent, at the top.
	row = r.drawNoticeBanner(row, col, width, startRow+maxRows, status.Notices)

	// Connection status (with contextual reconnect affordance / state)
	r.moveTo(row, col)
	switch {
	case status.DaemonConnected == ConnectionConnected:
		pingStr := ""
		if status.DaemonPingMs > 0 {
			pingStr = fmt.Sprintf(" %s(%dms)%s", FgBrightBlack, status.DaemonPingMs, Reset)
		}
		r.write(fmt.Sprintf("%s%s%s connected%s", FgGreen, IconConnected, Reset, pingStr))
		row++
	case actions.Connecting:
		r.write(fmt.Sprintf("%s%s%s connecting…", FgYellow, spinner, Reset))
		row++
	default:
		r.write(fmt.Sprintf("%s%s%s disconnected %s· press %sc%s%s to reconnect%s",
			FgYellow, IconDisconnected, Reset,
			FgBrightBlack, FgCyan+Bold, Reset, FgBrightBlack, Reset))
		row++
		if actions.ConnectErr != "" && row < startRow+maxRows {
			r.moveTo(row, col)
			msg := actions.ConnectErr
			if len(msg) > width-3 {
				msg = msg[:width-4] + "…"
			}
			r.write(fmt.Sprintf("%s%s %s%s", FgRed, IconWarning, msg, Reset))
			row++
		}
	}
	row++ // blank separator

	// Scripts summary (persistent state from ScriptRegistry)
	if len(status.Scripts) > 0 && row < startRow+maxRows {
		r.moveTo(row, col)
		r.write(Bold + "scripts" + Reset)
		row++

		for i, script := range status.Scripts {
			if row >= startRow+maxRows || i >= 8 {
				break
			}
			r.moveTo(row, col+1)

			selected := i == selectedIdx
			if selected {
				r.write(Reverse)
			}

			icon, iconColor := processStateIcon(script.State, script.HasAlerts, 0)

			nameStr := script.Name
			if len(nameStr) > width-20 {
				nameStr = nameStr[:width-23] + "…"
			}

			r.write(fmt.Sprintf("%s%s%s %s", iconColor, icon, Reset, nameStr))
			if selected {
				// Re-apply reverse after Reset from icon color
				r.write(Reverse)
			}

			if script.FailCount > 0 {
				r.write(fmt.Sprintf(" %sfails:%d%s", FgRed, script.FailCount, Reset))
				if selected {
					r.write(Reverse)
				}
			}

			if script.LastError != "" && script.State == "failed" {
				errMsg := script.LastError
				maxErr := width - len(nameStr) - 20
				if maxErr > 0 && len(errMsg) > maxErr {
					errMsg = errMsg[:maxErr-1] + "…"
				}
				if maxErr > 0 {
					r.write(fmt.Sprintf(" %s%s%s", FgBrightBlack, errMsg, Reset))
					if selected {
						r.write(Reverse)
					}
				}
			}

			// Pad to fill the row for a clean highlight bar
			if selected {
				r.write(Reset)
			}
			row++
		}
		row++
	}

	// Proxies summary
	if len(status.Proxies) > 0 && row < startRow+maxRows {
		r.moveTo(row, col)
		r.write(Bold + "proxies" + Reset)
		row++

		for _, proxy := range status.Proxies {
			if row >= startRow+maxRows {
				break
			}
			r.moveTo(row, col+1)
			proxyURL := "http://" + NormalizeListenAddr(proxy.ListenAddr)
			errStr := ""
			if proxy.HasErrors {
				errStr = fmt.Sprintf(" %s%s%d%s", FgRed, IconWarning, proxy.ErrorCount, Reset)
			}
			r.write(fmt.Sprintf("%s%s%s %s→%s %s%s%s%s",
				FgWhite+Bold, stripProjectPrefix(proxy.ID), Reset,
				FgBrightBlack, Reset,
				FgBrightCyan+Underline, proxyURL, Reset,
				errStr))
			row++
		}
		row++
	}

	// Ports inventory (port-whisperer style: dev ports shown, system hidden).
	visiblePorts := make([]PortInfo, 0, len(status.Ports))
	hiddenPorts := 0
	for _, p := range status.Ports {
		if p.System && !showAllPorts {
			hiddenPorts++
			continue
		}
		visiblePorts = append(visiblePorts, p)
	}
	if (len(visiblePorts) > 0 || hiddenPorts > 0) && row < startRow+maxRows {
		r.moveTo(row, col)
		hdr := Bold + "ports" + Reset
		if hiddenPorts > 0 {
			hdr += fmt.Sprintf("  %s%d system hidden · :toggle-ports%s", FgBrightBlack, hiddenPorts, Reset)
		}
		r.write(hdr)
		row++
		for i, p := range visiblePorts {
			if row >= startRow+maxRows || i >= 8 {
				break
			}
			r.moveTo(row, col+1)

			var icon, iconColor, tag, tagColor string
			switch p.Status {
			case "managed":
				icon, iconColor = "●", FgGreen
				tag, tagColor = "managed", FgGreen
			case "conflict":
				icon, iconColor = "!", FgRed
				tag, tagColor = "CONFLICT", FgRed+Bold
			default:
				icon, iconColor = "●", FgYellow
				tag, tagColor = "unmanaged", FgBrightBlack
			}

			owner := p.Name
			if owner == "" {
				if p.PID > 0 {
					owner = fmt.Sprintf("pid %d", p.PID)
				} else {
					owner = "unknown"
				}
			}
			if p.Windows {
				owner += " (win)"
			}
			if len(owner) > 18 {
				owner = owner[:17] + "…"
			}

			r.write(fmt.Sprintf("%s%s%s %s%-6d%s %s%-18s%s %s%s%s",
				iconColor, icon, Reset,
				FgWhite+Bold, p.Port, Reset,
				FgWhite, owner, Reset,
				tagColor, tag, Reset))
			row++
		}
		if len(visiblePorts) > 8 && row < startRow+maxRows {
			r.moveTo(row, col+1)
			r.write(fmt.Sprintf("%s… %d more%s", FgBrightBlack, len(visiblePorts)-8, Reset))
			row++
		}
		row++
	}

	// Orphaned process groups (leader dead, members alive)
	if len(status.Orphans) > 0 && row < startRow+maxRows {
		r.moveTo(row, col)
		r.write(fmt.Sprintf("%sorphans%s  %spress %s:%s%s kill-orphans%s",
			Bold, Reset, FgBrightBlack, FgCyan+Bold, Reset, FgBrightBlack, Reset))
		row++
		for i, o := range status.Orphans {
			if row >= startRow+maxRows || i >= 5 {
				break
			}
			r.moveTo(row, col+1)
			r.write(fmt.Sprintf("%s⚠%s pgid %s%d%s  %s%d procs (leader dead)%s",
				FgYellow, Reset,
				FgWhite+Bold, o.PGID, Reset,
				FgBrightBlack, o.Count, Reset))
			row++
		}
		row++
	}

	// Recent errors
	recentErrors := 0
	cutoff := time.Now().Add(-5 * time.Minute)
	for _, e := range status.RecentErrors {
		if e.Timestamp.After(cutoff) {
			recentErrors++
		}
	}
	if recentErrors > 0 && row < startRow+maxRows {
		r.moveTo(row, col)
		r.write(fmt.Sprintf("%s%s %d recent errors%s", FgRed+Bold, IconWarning, recentErrors, Reset))
		row++
		for _, e := range status.RecentErrors {
			if !e.Timestamp.After(cutoff) || row >= startRow+maxRows {
				break
			}
			r.moveTo(row, col+1)
			msg := e.Message
			if len(msg) > width-10 {
				msg = msg[:width-13] + "…"
			}
			r.write(fmt.Sprintf("%s%s%s %s %s%s%s",
				FgRed, IconError, Reset, msg,
				FgBrightBlack, formatShortTimeAgo(e.Timestamp), Reset))
			row++
		}
	}

	// Global actions line (summarize state + command affordance)
	if row < startRow+maxRows-1 {
		row++
		r.moveTo(row, col)
		r.write(Bold + "actions" + Reset + "  ")
		switch {
		case actions.Summarizing:
			r.write(fmt.Sprintf("%s%s%s summarizing…", FgCyan, spinner, Reset))
		case actions.SummaryErr != "":
			msg := actions.SummaryErr
			if len(msg) > width-22 {
				msg = msg[:width-23] + "…"
			}
			r.write(fmt.Sprintf("%s%s summarize failed: %s%s", FgRed, IconWarning, msg, Reset))
		default:
			if actions.SummarizeEnabled {
				r.write(fmt.Sprintf("%sm%s summarize", FgCyan+Bold, Reset))
			}
		}
		row++
	}

	// Command palette (bottom of overview)
	if commandInput {
		if row < startRow+maxRows-1 {
			row++
			r.moveTo(row, col)
			r.write(Bold + "run command" + Reset + FgBrightBlack + "  (type to filter, ↑↓ select, ⏎ run, esc cancel)" + Reset)
			row++
			r.moveTo(row, col)
			r.write(fmt.Sprintf("%s>%s %s%s█%s", FgCyan+Bold, Reset, commandBuffer, FgBrightBlack, Reset))
			row++

			matches, _, _ := filterPaletteCommands(commandBuffer)
			if len(matches) == 0 && row < startRow+maxRows {
				r.moveTo(row, col+1)
				r.write(FgBrightBlack + "no matching command" + Reset)
				row++
			}
			for i, c := range matches {
				if row >= startRow+maxRows {
					break
				}
				r.moveTo(row, col+1)
				selected := i == commandSelectedIdx
				marker := "  "
				if selected {
					marker = FgCyan + Bold + "▸ " + Reset
				}
				name := c.Name
				if c.Arg != "" {
					name += " " + c.Arg
				}
				nameCol := FgWhite
				if selected {
					nameCol = FgCyan + Bold
				}
				line := fmt.Sprintf("%s%s%-22s%s%s%s", marker, nameCol, name, Reset, FgBrightBlack, c.Desc)
				if len(line) > 0 {
					r.write(line + Reset)
				}
				row++
			}
		}
	} else if row < startRow+maxRows-1 {
		row++
		r.moveTo(row, col)
		r.write(FgBrightBlack + "Press " + Reset + FgCyan + Bold + ":" + Reset + FgBrightBlack + " to run a command" + Reset)
	}
}

// drawProcessPanelContent draws the content for a process panel.
// Panel.ID is the script name; state comes from the ScriptInfo in status.Scripts.
func (r *Renderer) drawProcessPanelContent(startRow, col, width, maxRows int, panel PanelItem, status Status) {
	// Find the script by name
	var script *ScriptInfo
	for i := range status.Scripts {
		if status.Scripts[i].Name == panel.ID {
			script = &status.Scripts[i]
			break
		}
	}

	row := startRow
	if script == nil {
		// Script removed from registry — show header with last known state
		r.moveTo(row, col)
		state := panel.ProcessState
		if state == "" {
			state = "stopped"
		}
		stateColor := StateColorCode(state)
		r.write(fmt.Sprintf("%s %s%s%s", panel.ID, stateColor+Bold, state, Reset))
		if panel.IsDone() {
			r.write(fmt.Sprintf("  %sx%s close", FgBrightBlack+Bold, Reset))
		}
		row++
	} else {
		// Script header info
		r.moveTo(row, col)
		stateColor := StateColorCode(script.State)
		r.write(fmt.Sprintf("%s %s%s%s",
			script.Command,
			stateColor+Bold, script.State, Reset))
		if script.StartCount > 1 {
			r.write(fmt.Sprintf(" %sstarts:%d%s", FgBrightBlack, script.StartCount, Reset))
		}
		if panel.IsDone() {
			r.write(fmt.Sprintf("  %sx%s close", FgBrightBlack+Bold, Reset))
		}
		row++

		if script.LastError != "" && script.State == "failed" {
			r.moveTo(row, col)
			errMsg := script.LastError
			if len(errMsg) > width {
				errMsg = errMsg[:width-1] + "…"
			}
			r.write(fmt.Sprintf("%s%s%s", FgRed, errMsg, Reset))
			row++
		}
	}

	// Separator
	if row < startRow+maxRows {
		row++
		r.moveTo(row, col)
		sepColor := gradientProcess.borderColorAt(0.3)
		sepWidth := min(width, r.width-col-2)
		r.write(sepColor + strings.Repeat("╌", sepWidth) + Reset)
		row++
	}

	// Output content with scroll support
	r.drawScrollableContent(row, col, width, startRow+maxRows-row, panel)
}

// drawScrollableContent renders panel content with vertical scroll support.
// ScrollOffset 0 = pinned to bottom (showing latest output).
func (r *Renderer) drawScrollableContent(startRow, col, width, availLines int, panel PanelItem) {
	visible := visibleLines(panel, availLines, width)

	row := startRow
	for _, line := range visible {
		r.moveTo(row, col)
		r.write(line)
		row++
	}

	// Cache for diff-based refresh
	r.lastPanelLines = visible
	r.lastPanelStart = startRow
	r.lastPanelCol = col
	r.lastPanelWidth = width
	r.lastPanelAvail = availLines

	// Scroll indicators
	r.drawScrollIndicators(startRow, col, width, availLines, panel)
}

// visibleLines computes the truncated lines visible in a scrollable panel.
func visibleLines(panel PanelItem, availLines, width int) []string {
	if panel.Content == "" {
		return []string{FgBrightBlack + "no output" + Reset}
	}

	lines := strings.Split(panel.Content, "\n")

	endLine := len(lines) - panel.ScrollOffset
	if endLine <= 0 {
		endLine = 1
	}
	if endLine > len(lines) {
		endLine = len(lines)
	}
	fromLine := endLine - availLines
	if fromLine < 0 {
		fromLine = 0
	}

	count := endLine - fromLine
	if count > availLines {
		count = availLines
	}
	out := make([]string, 0, count)
	for i := fromLine; i < endLine && len(out) < availLines; i++ {
		line := lines[i]
		if len(line) > width {
			line = line[:width]
		}
		out = append(out, line)
	}
	return out
}

// drawScrollIndicators renders the up/down scroll position hints.
func (r *Renderer) drawScrollIndicators(startRow, col, width, availLines int, panel PanelItem) {
	if panel.Content == "" {
		return
	}
	lines := strings.Split(panel.Content, "\n")
	endLine := len(lines) - panel.ScrollOffset
	if endLine <= 0 {
		endLine = 1
	}
	if endLine > len(lines) {
		endLine = len(lines)
	}
	fromLine := endLine - availLines
	if fromLine < 0 {
		fromLine = 0
	}

	if panel.ScrollOffset > 0 {
		indicator := fmt.Sprintf("↓ %d", panel.ScrollOffset)
		r.moveTo(startRow+availLines-1, col+width-len(indicator)-1)
		r.write(FgBrightBlack + indicator + Reset)
	}
	if fromLine > 0 {
		indicator := fmt.Sprintf("↑ %d", fromLine)
		r.moveTo(startRow, col+width-len(indicator)-1)
		r.write(FgBrightBlack + indicator + Reset)
	}
}

// RefreshPanelContent performs a diff-based update of the scrollable content
// area within the current panel view. Only lines that differ from the last
// render are redrawn, reducing terminal flicker during live refresh.
// Returns false if no cached state exists (caller should do a full draw).
func (r *Renderer) RefreshPanelContent(panel PanelItem) bool {
	r.mu.Lock()

	if r.lastPanelLines == nil {
		r.mu.Unlock()
		return false
	}

	r.beginBuffer()

	startRow := r.lastPanelStart
	col := r.lastPanelCol
	width := r.lastPanelWidth
	availLines := r.lastPanelAvail

	newLines := visibleLines(panel, availLines, width)

	r.write(CursorHide)

	maxLen := len(newLines)
	if len(r.lastPanelLines) > maxLen {
		maxLen = len(r.lastPanelLines)
	}

	for i := 0; i < maxLen; i++ {
		var oldLine, newLine string
		if i < len(r.lastPanelLines) {
			oldLine = r.lastPanelLines[i]
		}
		if i < len(newLines) {
			newLine = newLines[i]
		}
		if oldLine == newLine {
			continue
		}
		r.moveTo(startRow+i, col)
		r.write(ClearToEOL)
		r.write(newLine)
	}

	r.lastPanelLines = newLines
	r.drawScrollIndicators(startRow, col, width, availLines, panel)
	r.write(CursorShow)

	buf := r.buf
	r.buf = nil
	r.mu.Unlock()
	r.flushBuffer(buf)
	return true
}

// drawProxyPanelContent draws the content for a proxy panel.
func (r *Renderer) drawProxyPanelContent(startRow, col, width, maxRows int, panel PanelItem, status Status) {
	// Find the proxy
	var proxy *ProxyInfo
	for i := range status.Proxies {
		if status.Proxies[i].ID == panel.ID {
			proxy = &status.Proxies[i]
			break
		}
	}

	row := startRow
	if proxy == nil {
		r.moveTo(row, col)
		r.write(FgBrightBlack + "proxy not found" + Reset)
		return
	}

	// Proxy header
	r.moveTo(row, col)
	r.write(fmt.Sprintf("%s→%s %s", FgBrightBlack, Reset, proxy.TargetURL))
	row++

	// URLs
	proxyURL := "http://" + NormalizeListenAddr(proxy.ListenAddr)
	r.moveTo(row, col)
	r.write(FgBrightCyan + Underline + proxyURL + Reset)
	row++

	if proxy.TailscaleURL != "" {
		r.moveTo(row, col)
		r.write(FgMagenta + Underline + proxy.TailscaleURL + Reset)
		row++
	}
	if proxy.TunnelURL != "" {
		r.moveTo(row, col)
		r.write(FgBrightMagenta + Underline + proxy.TunnelURL + Reset)
		row++
	}

	// Error indicator
	if proxy.HasErrors {
		r.moveTo(row, col)
		r.write(fmt.Sprintf("%s%s %d errors%s", FgRed, IconWarning, proxy.ErrorCount, Reset))
		row++
	}

	// Gate state: which dependencies the proxy is still waiting on
	if proxy.State == "waiting_for_dependencies" && len(proxy.WaitingOn) > 0 {
		r.moveTo(row, col)
		r.write(fmt.Sprintf("%s%s waiting on: %s%s", FgYellow, IconWarning, strings.Join(proxy.WaitingOn, ", "), Reset))
		row++
	}

	// Port conflict: the proxy's listen port is held by an unmanaged process
	if cport := proxyListenConflict(proxy, status.Ports); cport != "" {
		r.moveTo(row, col)
		r.write(fmt.Sprintf("%s%s port %s conflict — held by another process%s", FgRed+Bold, IconWarning, cport, Reset))
		row++
	}

	// Traffic stats: cumulative requests + uptime
	if proxy.TotalRequests > 0 || proxy.Uptime != "" {
		var parts []string
		if proxy.TotalRequests > 0 {
			parts = append(parts, fmt.Sprintf("%d reqs", proxy.TotalRequests))
		}
		if proxy.Uptime != "" {
			parts = append(parts, "up "+proxy.Uptime)
		}
		r.moveTo(row, col)
		r.write(FgBrightBlack + strings.Join(parts, " · ") + Reset)
		row++
	}

	// Separator
	if row < startRow+maxRows {
		row++
		r.moveTo(row, col)
		sepColor := gradientProxy.borderColorAt(0.3)
		sepWidth := min(width, r.width-col-2)
		r.write(sepColor + strings.Repeat("╌", sepWidth) + Reset)
		row++
	}

	// Log content
	if panel.Content != "" {
		lines := strings.Split(panel.Content, "\n")
		startLine := 0
		availLines := startRow + maxRows - row
		if len(lines) > availLines {
			startLine = len(lines) - availLines
		}
		for i := startLine; i < len(lines) && row < startRow+maxRows; i++ {
			r.moveTo(row, col)
			line := lines[i]
			if len(line) > width {
				line = line[:width]
			}
			r.write(line)
			row++
		}
	} else {
		r.moveTo(row, col)
		r.write(FgBrightBlack + "Tab/Shift+Tab to navigate panels" + Reset)
	}
}

// formatShortTimeAgo formats a timestamp as a short relative time string.
func formatShortTimeAgo(t time.Time) string {
	d := time.Since(t)
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	return fmt.Sprintf("%dh", int(d.Hours()))
}

// ClearMenu clears all overlay regions (menu and input dialogs).
// This restores the screen by clearing the tracked regions.
func (r *Renderer) ClearMenu() {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Pop all overlays from the stack - this clears each tracked region
	r.overlayStack.PopAll()

	// Reset tracked regions
	r.currentMenuRegion = nil
	r.currentInputRegion = nil
}

// ResetMenuRegions resets the tracked menu regions without clearing screen content.
// Use this when relying on SIGWINCH to trigger a full redraw by the child process.
func (r *Renderer) ResetMenuRegions() {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Clear the overlay stack without actually clearing screen regions
	// The child process will redraw everything via SIGWINCH
	r.overlayStack.Clear()

	// Reset tracked regions
	r.currentMenuRegion = nil
	r.currentInputRegion = nil
}

// niriGradient defines a two-color gradient for niri-style panel borders.
// Uses 256-color ANSI for broad terminal compatibility.
type niriGradient struct {
	fromFg string // Start color (top/left)
	toFg   string // End color (bottom/right)
}

// Predefined gradients for different panel types (niri-inspired).
var (
	gradientProcess = niriGradient{
		fromFg: "\x1b[38;5;75m", // Steel blue
		toFg:   "\x1b[38;5;69m", // Medium blue
	}
	gradientError = niriGradient{
		fromFg: "\x1b[38;5;209m", // Salmon
		toFg:   "\x1b[38;5;167m", // Indian red
	}
	gradientProxy = niriGradient{
		fromFg: "\x1b[38;5;114m", // Pale green
		toFg:   "\x1b[38;5;72m",  // Cadet blue
	}
	gradientMenu = niriGradient{
		fromFg: "\x1b[38;5;252m", // Light grey
		toFg:   "\x1b[38;5;245m", // Grey
	}
)

// borderColorAt returns the interpolated border color for a given position
// along a gradient (0.0 = start, 1.0 = end).
func (g niriGradient) borderColorAt(t float64) string {
	if t <= 0.5 {
		return g.fromFg
	}
	return g.toFg
}

// drawNiriPanel draws a niri-style panel with rounded corners and gradient border.
// The panel has a 1-cell gap on all sides for the characteristic niri spacing.
func (r *Renderer) drawNiriPanel(row, col, width, height int, title string, grad niriGradient) {
	if height < 2 || width < 4 {
		return
	}

	// Top border with rounded corners
	r.moveTo(row, col)
	topColor := grad.borderColorAt(0)
	r.write(topColor)
	r.write(RoundTopLeft)
	if title != "" {
		remaining := width - 2
		titleDisplay := " " + title + " "
		titleLen := len(titleDisplay)
		if titleLen > remaining {
			titleLen = remaining
			titleDisplay = titleDisplay[:titleLen]
		}
		leftBar := 1
		rightBar := remaining - titleLen - leftBar
		if rightBar < 0 {
			rightBar = 0
		}
		r.write(strings.Repeat(BoxHorizontal, leftBar))
		r.write(Reset + Bold + topColor + titleDisplay + Reset + topColor)
		r.write(strings.Repeat(BoxHorizontal, rightBar))
	} else {
		r.write(strings.Repeat(BoxHorizontal, width-2))
	}
	r.write(RoundTopRight + Reset)

	// Side borders with gradient
	for i := 1; i < height-1; i++ {
		t := float64(i) / float64(height-1)
		sideColor := grad.borderColorAt(t)
		r.moveTo(row+i, col)
		r.write(sideColor + BoxVertical + Reset)
		r.write(strings.Repeat(" ", width-2))
		r.write(sideColor + BoxVertical + Reset)
	}

	// Bottom border with rounded corners
	r.moveTo(row+height-1, col)
	bottomColor := grad.borderColorAt(1)
	r.write(bottomColor)
	r.write(RoundBottomLeft)
	r.write(strings.Repeat(BoxHorizontal, width-2))
	r.write(RoundBottomRight + Reset)
}

// padRight pads a string to the right to reach the target width.
func (r *Renderer) padRight(s string, width int) string {
	visLen := r.estimateVisibleLength(s)
	if visLen >= width {
		return s
	}
	return s + strings.Repeat(" ", width-visLen)
}

// padCenter centers a string within the target width.
func (r *Renderer) padCenter(s string, width int) string {
	visLen := r.estimateVisibleLength(s)
	if visLen >= width {
		return s
	}
	leftPad := (width - visLen) / 2
	rightPad := width - visLen - leftPad
	return strings.Repeat(" ", leftPad) + s + strings.Repeat(" ", rightPad)
}

// DrawProcessOutput draws the process output viewer on the alt screen
// using niri-style panel with rounded corners and gradient border.
func (r *Renderer) DrawProcessOutput(processID, command, state, output string) {
	r.mu.Lock()
	r.beginBuffer()

	// Clear screen
	r.write(ClearScreen + CursorHome + CursorHide)

	// Panel dimensions: centered with niri-style margins
	const margin = 2
	panelWidth := r.width - margin*2
	panelHeight := r.height - margin*2
	if panelWidth < 20 {
		panelWidth = r.width
	}
	if panelHeight < 5 {
		panelHeight = r.height
	}
	panelCol := (r.width - panelWidth) / 2
	if panelCol < 1 {
		panelCol = 1
	}
	panelRow := margin

	// Choose gradient based on state
	grad := gradientProcess
	if state == "failed" {
		grad = gradientError
	}

	// Draw niri-style panel
	stateIcon := IconConnected
	if state != "running" {
		stateIcon = IconDisconnected
	}
	title := fmt.Sprintf("%s %s", stateIcon, processID)
	r.drawNiriPanel(panelRow, panelCol, panelWidth, panelHeight, title, grad)

	// Draw header info inside panel
	headerRow := panelRow + 1
	contentWidth := panelWidth - 4
	r.moveTo(headerRow, panelCol+2)

	stateColor := StateColorCode(state)
	cmdDisplay := command
	if len(cmdDisplay) > contentWidth-len(state)-5 {
		cmdDisplay = cmdDisplay[:contentWidth-len(state)-8] + "…"
	}
	r.write(fmt.Sprintf("%s%s%s %s%s%s",
		FgBrightBlack, cmdDisplay, Reset,
		stateColor+Bold, state, Reset))

	// Separator line
	sepRow := headerRow + 1
	r.moveTo(sepRow, panelCol+1)
	sepColor := grad.borderColorAt(0.1)
	r.write(sepColor + strings.Repeat("╌", panelWidth-2) + Reset)

	// Draw output lines
	lines := strings.Split(output, "\n")
	outputWidth := panelWidth - 4
	maxLines := panelHeight - 5 // header + separator + footer margins

	// Wrap long lines
	var displayLines []string
	for _, line := range lines {
		if len(line) == 0 {
			displayLines = append(displayLines, "")
			continue
		}
		for len(line) > 0 {
			if len(line) <= outputWidth {
				displayLines = append(displayLines, line)
				break
			}
			displayLines = append(displayLines, line[:outputWidth])
			line = line[outputWidth:]
		}
	}

	// Show last N lines if output is longer
	startLine := 0
	if len(displayLines) > maxLines {
		startLine = len(displayLines) - maxLines
	}

	currentRow := sepRow + 1
	for i := startLine; i < len(displayLines) && currentRow < panelRow+panelHeight-2; i++ {
		r.moveTo(currentRow, panelCol+2)
		r.write(displayLines[i])
		currentRow++
	}

	// Footer hint inside the panel bottom border area
	footerRow := panelRow + panelHeight - 1
	r.moveTo(footerRow, panelCol+1)
	footerColor := grad.borderColorAt(1)
	r.write(footerColor)
	hint := " press any key to close "
	r.write(r.padCenter(hint, panelWidth-2))
	r.write(Reset)

	buf := r.buf
	r.buf = nil
	r.mu.Unlock()
	r.flushBuffer(buf)
}

// DrawStatusBarMessage draws a message on the status bar at the bottom of the screen.
// Use this for transient status updates like spinners.
func (r *Renderer) DrawStatusBarMessage(message string) {
	r.mu.Lock()
	r.beginBuffer()

	r.write(CursorSave + CursorHide)
	r.moveTo(r.height, 1)
	r.write(ClearLine)

	// Draw the message with status bar styling
	r.write(BgBrightBlack + FgWhite)
	displayMsg := " " + message
	// Pad to fill the status bar width
	if len(displayMsg) < r.width {
		displayMsg += strings.Repeat(" ", r.width-len(displayMsg))
	}
	r.write(displayMsg)
	r.write(Reset)

	r.write(CursorRestore + CursorShow)

	buf := r.buf
	r.buf = nil
	r.mu.Unlock()
	r.flushBuffer(buf)
}

// ClearStatusBarMessage clears any message on the status bar.
// After clearing, the regular indicator should be redrawn.
func (r *Renderer) ClearStatusBarMessage() {
	r.mu.Lock()
	r.beginBuffer()

	r.write(CursorSave + CursorHide)
	r.moveTo(r.height, 1)
	r.write(ClearLine)
	r.write(CursorRestore + CursorShow)

	buf := r.buf
	r.buf = nil
	r.mu.Unlock()
	r.flushBuffer(buf)
}
