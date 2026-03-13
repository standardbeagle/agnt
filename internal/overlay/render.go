package overlay

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"
)

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

// proxyDisplayInfo contains formatted proxy information for display.
type proxyDisplayInfo struct {
	LocalURL     string
	TailscaleURL string
	TunnelURL    string
	HasErrors    bool
}

// formatProxyDisplay formats proxy URLs for display.
func formatProxyDisplay(proxy ProxyInfo) proxyDisplayInfo {
	return proxyDisplayInfo{
		LocalURL:     "http://" + NormalizeListenAddr(proxy.ListenAddr),
		TailscaleURL: proxy.TailscaleURL,
		TunnelURL:    proxy.TunnelURL,
		HasErrors:    proxy.HasErrors,
	}
}

// Renderer handles drawing to the terminal.
type Renderer struct {
	out          io.Writer
	width        int
	height       int
	hotkey       byte   // Hotkey for overlay toggle (for display)
	version      string // Version string for dashboard title
	mu           sync.Mutex
	screenMgr    *ScreenManager
	overlayStack *OverlayStack

	// Track current overlay regions for proper clearing
	currentMenuRegion  *ScreenRegion
	currentInputRegion *ScreenRegion
}

// NewRenderer creates a new Renderer.
func NewRenderer(out io.Writer, width, height int) *Renderer {
	sm := NewScreenManager(out, width, height)
	return &Renderer{
		out:          out,
		width:        width,
		height:       height,
		hotkey:       0x19, // Ctrl+Y default
		screenMgr:    sm,
		overlayStack: NewOverlayStack(sm),
	}
}

// SetHotkey sets the hotkey displayed in the indicator bar.
func (r *Renderer) SetHotkey(hotkey byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.hotkey = hotkey
}

// SetVersion sets the version displayed in the dashboard title.
func (r *Renderer) SetVersion(version string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.version = version
}

// formatHotkey returns a human-readable hotkey string like "Ctrl+Y".
func formatHotkey(b byte) string {
	if b >= 1 && b <= 26 {
		return fmt.Sprintf("Ctrl+%c", 'A'+b-1)
	}
	return fmt.Sprintf("0x%02X", b)
}

// SetSize updates the terminal dimensions.
func (r *Renderer) SetSize(width, height int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.width = width
	r.height = height
	r.screenMgr.SetSize(width, height)
}

// write outputs a string without locking (caller must hold lock).
func (r *Renderer) write(s string) {
	io.WriteString(r.out, s)
}

// moveTo moves cursor to row, col (1-indexed).
func (r *Renderer) moveTo(row, col int) {
	r.write(fmt.Sprintf(CursorToFormat, row, col))
}

// DrawIndicator draws the status indicator bar at the bottom of the screen.
func (r *Renderer) DrawIndicator(status Status) {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Save cursor, hide it, move to bottom
	r.write(CursorSave + CursorHide)
	r.moveTo(r.height, 1)
	r.write(ClearLine)

	// Build status bar content
	var parts []string

	// Daemon connection status
	switch status.DaemonConnected {
	case ConnectionConnected:
		pingStr := ""
		if status.DaemonPingMs > 0 {
			pingStr = fmt.Sprintf(" %dms", status.DaemonPingMs)
		}
		parts = append(parts, fmt.Sprintf("%s%s%s daemon%s%s", FgGreen, IconConnected, Reset, FgBrightBlack, pingStr+Reset))
	case ConnectionDisconnected:
		parts = append(parts, fmt.Sprintf("%s%s%s daemon", FgYellow, IconDisconnected, Reset))
	case ConnectionError:
		parts = append(parts, fmt.Sprintf("%s%s%s daemon", FgRed, IconError, Reset))
	default:
		parts = append(parts, fmt.Sprintf("%s%s%s daemon", FgBrightBlack, IconDisconnected, Reset))
	}

	// Running processes count and collect process URLs (deduplicated by port)
	runningCount := 0
	for _, p := range status.Processes {
		if p.State == "running" {
			runningCount++
		}
	}
	aggregatedURLs := aggregateProcessURLs(status.Processes)
	if runningCount > 0 {
		parts = append(parts, fmt.Sprintf("%s%s %d proc%s", FgCyan, IconProcess, runningCount, Reset))
	}

	// Running proxies with clickable URL
	proxyCount := len(status.Proxies)
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
	var urlParts []string

	if proxyCount > 0 {
		// Prefer tunnel URL over local URL in status bar (more useful for sharing)
		displayURL := tunnelURL
		if displayURL == "" {
			displayURL = proxyURL
		}
		urlColor := FgBrightCyan
		if tunnelURL != "" {
			urlColor = FgBrightMagenta // Different color for tunnel URLs
		}

		if displayURL != "" {
			urlParts = append(urlParts, fmt.Sprintf("%s%s%s", urlColor+Underline, displayURL, Reset))
		}
	}

	// Add process URLs (normalized to use localhost, underlined for clickability)
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

	// Add hotkey hint on the right
	hotkeyStr := formatHotkey(r.hotkey)
	hotkeyHint := fmt.Sprintf("%s%s%s", FgBrightBlack, hotkeyStr, Reset)

	// Calculate padding
	// Note: This is approximate due to ANSI codes; for accurate width we'd need to strip codes
	visibleLen := r.estimateVisibleLength(statusText)
	hotkeyLen := len(hotkeyStr)
	padding := r.width - visibleLen - hotkeyLen - 4 // 4 for " │ " separator and spaces

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

// estimateVisibleLength estimates the visible length of a string with ANSI codes.
func (r *Renderer) estimateVisibleLength(s string) int {
	// Strip ANSI escape codes for length calculation
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
		length++
	}
	return length
}

// ClearIndicator clears the indicator bar.
func (r *Renderer) ClearIndicator() {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.write(CursorSave + CursorHide)
	r.moveTo(r.height, 1)
	r.write(ClearLine)
	r.write(CursorRestore + CursorShow)
}

// ClearScreen clears the entire screen and resets cursor to home.
func (r *Renderer) ClearScreen() {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Clear entire screen, move cursor home, reset scroll region
	r.write(ClearScreen + CursorHome + ResetScroll)
}

// ClearVisible clears the visible screen and moves cursor home.
// Unlike ClearScreen, this preserves the scroll region.
func (r *Renderer) ClearVisible() {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.write(ClearScreen + CursorHome)
}

// EnterAltScreen switches to the alternate screen buffer.
// The main screen content is preserved and restored when ExitAltScreen is called.
func (r *Renderer) EnterAltScreen() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.write(EnterAltScreen + CursorHome)
}

// ExitAltScreen switches back to the main screen buffer.
// The main screen content that was preserved when EnterAltScreen was called is restored.
func (r *Renderer) ExitAltScreen() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.write(ExitAltScreen)
}

// DrawMenu draws a popup menu in the center of the screen.
func (r *Renderer) DrawMenu(menu Menu, selectedIndex int) {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Calculate menu dimensions
	menuWidth := len(menu.Title) + 4
	for _, item := range menu.Items {
		itemWidth := len(item.Label) + 6 // "[x] " prefix + padding
		if itemWidth > menuWidth {
			menuWidth = itemWidth
		}
	}
	menuWidth = min(menuWidth+4, r.width-4) // Add padding, cap at screen width

	menuHeight := len(menu.Items) + 4 // Title + separator + items + bottom

	// Calculate position (centered, but above indicator bar)
	startRow := (r.height-menuHeight)/2 - 1
	if startRow < 1 {
		startRow = 1
	}
	startCol := (r.width - menuWidth) / 2
	if startCol < 1 {
		startCol = 1
	}

	// Track the region for later clearing (only on first draw, not updates)
	if r.currentMenuRegion == nil {
		r.currentMenuRegion = &ScreenRegion{
			Row:    startRow,
			Col:    startCol,
			Width:  menuWidth,
			Height: menuHeight,
		}
		r.overlayStack.Push(RegionMenu, *r.currentMenuRegion)
	}

	r.write(CursorSave + CursorHide)

	// Draw box
	r.drawBox(startRow, startCol, menuWidth, menuHeight, menu.Title)

	// Draw menu items
	for i, item := range menu.Items {
		row := startRow + 2 + i
		r.moveTo(row, startCol+1)

		if i == selectedIndex {
			r.write(BgBlue + FgWhite + Bold)
		}

		// Format: " [x] Label     "
		shortcut := " "
		if item.Shortcut != 0 {
			shortcut = string(item.Shortcut)
		}

		label := fmt.Sprintf(" [%s] %s", shortcut, item.Label)
		label = r.padRight(label, menuWidth-2)
		r.write(label)

		if i == selectedIndex {
			r.write(Reset)
		}
	}

	// Draw footer hint
	footerRow := startRow + menuHeight - 1
	r.moveTo(footerRow, startCol+1)
	r.write(FgBrightBlack)
	hint := " ↑↓ Navigate  Enter Select  Esc Close "
	hint = r.padCenter(hint, menuWidth-2)
	r.write(hint)
	r.write(Reset)

	r.write(CursorRestore + CursorShow)
}

// DrawDashboard draws a niri-style dashboard with centered panels.
// Panels are stacked vertically in the center with rounded corners,
// gradient borders, and gaps between them (inspired by niri window manager).
func (r *Renderer) DrawDashboard(menu Menu, selectedIndex int, status Status) {
	r.mu.Lock()
	defer r.mu.Unlock()

	const panelGap = 1    // Gap between panels (niri-style spacing)
	const panelMargin = 1 // Margin from screen edges

	// Panel width: centered, capped at 60 cols
	panelWidth := min(r.width-panelMargin*2, 60)
	if panelWidth < 30 {
		panelWidth = min(30, r.width-2)
	}
	panelCol := (r.width - panelWidth) / 2
	if panelCol < 1 {
		panelCol = 1
	}

	// Calculate available vertical space
	availHeight := r.height - 2 // Leave room for status bar

	// Determine which panels to show and their sizes
	processCount := min(len(status.Processes), 6)
	browserCount := min(len(status.BrowserSessions), 4)
	proxyCount := len(status.Proxies)
	menuItemCount := len(menu.Items)

	recentErrors := 0
	cutoff := time.Now().Add(-5 * time.Minute)
	for _, e := range status.RecentErrors {
		if e.Timestamp.After(cutoff) {
			recentErrors++
		}
	}

	// Calculate panel heights
	// Process panel: header + per-process lines (ID + last output + URLs)
	processLines := 0
	for i, p := range status.Processes {
		if i >= 6 {
			processLines++ // "... and N more"
			break
		}
		processLines++ // process line
		if p.LastOutput != "" {
			processLines++ // last output
		}
		processLines += len(p.URLs)
	}
	processPanelH := 0
	if processCount > 0 {
		processPanelH = processLines + 2 // border top/bottom
	}

	// Error panel
	errorPanelH := 0
	if recentErrors > 0 {
		errorPanelH = min(recentErrors, 5) + 2
	}

	// Proxy panel
	proxyLines := 0
	for _, p := range status.Proxies {
		proxyLines++ // proxy ID line
		proxyLines++ // listen addr
		if p.TailscaleURL != "" {
			proxyLines++
		}
		if p.TunnelURL != "" {
			proxyLines++
		}
	}
	proxyPanelH := 0
	if proxyCount > 0 {
		proxyPanelH = proxyLines + 2
	}

	// Browser panel
	browserPanelH := 0
	if browserCount > 0 {
		browserPanelH = browserCount + 2
	}

	// Menu panel: always shown
	menuPanelH := menuItemCount + 3 // items + title line + border top/bottom

	// Calculate total height needed
	totalPanels := 0
	totalHeight := 0
	panelHeights := []int{}
	if processPanelH > 0 {
		totalPanels++
		panelHeights = append(panelHeights, processPanelH)
		totalHeight += processPanelH
	}
	if errorPanelH > 0 {
		totalPanels++
		panelHeights = append(panelHeights, errorPanelH)
		totalHeight += errorPanelH
	}
	if proxyPanelH > 0 {
		totalPanels++
		panelHeights = append(panelHeights, proxyPanelH)
		totalHeight += proxyPanelH
	}
	if browserPanelH > 0 {
		totalPanels++
		panelHeights = append(panelHeights, browserPanelH)
		totalHeight += browserPanelH
	}
	totalPanels++
	panelHeights = append(panelHeights, menuPanelH)
	totalHeight += menuPanelH

	// Add gaps
	totalHeight += (totalPanels - 1) * panelGap

	// Shrink panels proportionally if needed
	if totalHeight > availHeight && len(panelHeights) > 0 {
		excess := totalHeight - availHeight
		// Shrink largest panels first
		for excess > 0 {
			maxIdx := 0
			for i, h := range panelHeights {
				if h > panelHeights[maxIdx] {
					maxIdx = i
				}
			}
			if panelHeights[maxIdx] <= 3 {
				break // Can't shrink further
			}
			panelHeights[maxIdx]--
			excess--
		}
	}

	// Track the full region for clearing
	regionHeight := min(totalHeight, availHeight)
	// Vertically center the panels
	startRow := max((availHeight-regionHeight)/2+1, panelMargin)
	if r.currentMenuRegion == nil {
		r.currentMenuRegion = &ScreenRegion{
			Row:    startRow,
			Col:    panelCol,
			Width:  panelWidth,
			Height: regionHeight,
		}
		r.overlayStack.Push(RegionMenu, *r.currentMenuRegion)
	}

	r.write(CursorSave + CursorHide)

	// Draw panels top-to-bottom, centered
	currentRow := startRow
	panelIdx := 0

	// === PROCESSES PANEL ===
	if processCount > 0 && panelIdx < len(panelHeights) {
		h := panelHeights[panelIdx]
		panelIdx++

		title := fmt.Sprintf("%s processes", IconProcess)
		r.drawNiriPanel(currentRow, panelCol, panelWidth, h, title, gradientProcess)

		row := currentRow + 1
		contentWidth := panelWidth - 4
		for i, proc := range status.Processes {
			if i >= 6 || row >= currentRow+h-1 {
				if len(status.Processes) > 6 && row < currentRow+h-1 {
					r.moveTo(row, panelCol+2)
					r.write(FgBrightBlack + fmt.Sprintf("… %d more", len(status.Processes)-6) + Reset)
				}
				break
			}

			r.moveTo(row, panelCol+2)
			stateColor := StateColorCode(proc.State)
			stateIcon := IconConnected
			if proc.State != "running" {
				stateIcon = IconDisconnected
			}

			// Process line: icon ID (state) runtime
			runtimeStr := ""
			if proc.Runtime > 0 {
				runtimeStr = formatShortDuration(proc.Runtime)
			}

			idStr := proc.ID
			if len(idStr) > contentWidth-15 {
				idStr = idStr[:contentWidth-15] + "…"
			}

			line := fmt.Sprintf("%s%s%s %s%s%s",
				stateColor, stateIcon, Reset,
				FgWhite, idStr, Reset)
			if runtimeStr != "" {
				line += fmt.Sprintf(" %s%s%s", FgBrightBlack, runtimeStr, Reset)
			}
			r.write(line)
			row++

			// Show last output line (dimmed)
			if proc.LastOutput != "" && row < currentRow+h-1 {
				r.moveTo(row, panelCol+4)
				out := proc.LastOutput
				if len(out) > contentWidth-2 {
					out = out[:contentWidth-5] + "…"
				}
				r.write(FgBrightBlack + out + Reset)
				row++
			}

			// Show URLs
			for _, urlStr := range proc.URLs {
				if row >= currentRow+h-1 {
					break
				}
				r.moveTo(row, panelCol+4)
				displayURL := urlStr
				if len(displayURL) > contentWidth-2 {
					displayURL = displayURL[:contentWidth-5] + "…"
				}
				r.write(FgBrightCyan + Underline + displayURL + Reset)
				row++
			}
		}

		currentRow += h + panelGap
	}

	// === ERRORS PANEL ===
	if recentErrors > 0 && panelIdx < len(panelHeights) {
		h := panelHeights[panelIdx]
		panelIdx++

		title := fmt.Sprintf("%s %d errors", IconWarning, recentErrors)
		r.drawNiriPanel(currentRow, panelCol, panelWidth, h, title, gradientError)

		row := currentRow + 1
		contentWidth := panelWidth - 4
		shown := 0
		for _, e := range status.RecentErrors {
			if !e.Timestamp.After(cutoff) || row >= currentRow+h-1 || shown >= 5 {
				break
			}

			r.moveTo(row, panelCol+2)
			msg := e.Message
			if len(msg) > contentWidth {
				msg = msg[:contentWidth-3] + "…"
			}
			ago := formatShortTimeAgo(e.Timestamp)
			srcColor := FgRed
			r.write(fmt.Sprintf("%s%s%s %s%s%s %s%s%s",
				srcColor, IconError, Reset,
				FgWhite, msg, Reset,
				FgBrightBlack, ago, Reset))
			row++
			shown++
		}

		currentRow += h + panelGap
	}

	// === PROXIES PANEL ===
	if proxyCount > 0 && panelIdx < len(panelHeights) {
		h := panelHeights[panelIdx]
		panelIdx++

		title := fmt.Sprintf("%s proxies", IconProxy)
		r.drawNiriPanel(currentRow, panelCol, panelWidth, h, title, gradientProxy)

		row := currentRow + 1
		contentWidth := panelWidth - 4
		for _, proxy := range status.Proxies {
			if row >= currentRow+h-1 {
				break
			}

			// Proxy ID and target
			r.moveTo(row, panelCol+2)
			errIndicator := ""
			if proxy.HasErrors {
				errIndicator = fmt.Sprintf(" %s%s%d%s", FgRed, IconWarning, proxy.ErrorCount, Reset)
			}
			target := proxy.TargetURL
			if len(target) > contentWidth-len(proxy.ID)-5 {
				target = target[:contentWidth-len(proxy.ID)-8] + "…"
			}
			r.write(fmt.Sprintf("%s%s%s %s→%s %s%s",
				FgWhite+Bold, proxy.ID, Reset,
				FgBrightBlack, Reset,
				target, errIndicator))
			row++

			// Listen address
			if row < currentRow+h-1 {
				proxyURL := "http://" + NormalizeListenAddr(proxy.ListenAddr)
				r.moveTo(row, panelCol+4)
				if len(proxyURL) > contentWidth-2 {
					proxyURL = proxyURL[:contentWidth-5] + "…"
				}
				r.write(FgBrightCyan + Underline + proxyURL + Reset)
				row++
			}

			// Tailscale URL
			if proxy.TailscaleURL != "" && row < currentRow+h-1 {
				r.moveTo(row, panelCol+4)
				tsURL := proxy.TailscaleURL
				if len(tsURL) > contentWidth-2 {
					tsURL = tsURL[:contentWidth-5] + "…"
				}
				r.write(FgMagenta + Underline + tsURL + Reset)
				row++
			}

			// Tunnel URL
			if proxy.TunnelURL != "" && row < currentRow+h-1 {
				r.moveTo(row, panelCol+4)
				tunURL := proxy.TunnelURL
				if len(tunURL) > contentWidth-2 {
					tunURL = tunURL[:contentWidth-5] + "…"
				}
				r.write(FgBrightMagenta + Underline + tunURL + Reset)
				row++
			}
		}

		currentRow += h + panelGap
	}

	// === BROWSER SESSIONS PANEL ===
	if browserCount > 0 && panelIdx < len(panelHeights) {
		h := panelHeights[panelIdx]
		panelIdx++

		title := "browsers"
		r.drawNiriPanel(currentRow, panelCol, panelWidth, h, title, gradientBrowser)

		row := currentRow + 1
		contentWidth := panelWidth - 4
		for i, session := range status.BrowserSessions {
			if i >= 4 || row >= currentRow+h-1 {
				break
			}
			r.moveTo(row, panelCol+2)

			displayURL := session.URL
			maxURLLen := contentWidth - 6
			if len(displayURL) > maxURLLen {
				displayURL = displayURL[:maxURLLen-3] + "…"
			}

			activityIcon := FgGreen + IconConnected + Reset
			if time.Since(session.LastActivity) > 30*time.Second {
				activityIcon = FgBrightBlack + IconDisconnected + Reset
			}

			line := fmt.Sprintf("%s %s%s%s", activityIcon, FgWhite, displayURL, Reset)
			r.write(line)
			row++
		}

		currentRow += h + panelGap
	}

	// === ACTIONS PANEL (always shown) ===
	{
		h := panelHeights[panelIdx]

		versionTitle := "agnt"
		if r.version != "" {
			versionTitle = fmt.Sprintf("agnt v%s", r.version)
		}
		r.drawNiriPanel(currentRow, panelCol, panelWidth, h, versionTitle, gradientMenu)

		// Connection status on first content line
		row := currentRow + 1
		r.moveTo(row, panelCol+2)
		if status.DaemonConnected == ConnectionConnected {
			r.write(fmt.Sprintf("%s%s%s connected", FgGreen, IconConnected, Reset))
		} else {
			r.write(fmt.Sprintf("%s%s%s disconnected", FgYellow, IconDisconnected, Reset))
		}
		row++

		// Menu items
		for i, item := range menu.Items {
			if row >= currentRow+h-1 {
				break
			}
			r.moveTo(row, panelCol+2)

			if i == selectedIndex {
				r.write(BgBlue + FgWhite + Bold)
			}

			shortcut := " "
			if item.Shortcut != 0 {
				shortcut = string(item.Shortcut)
			}

			label := fmt.Sprintf("[%s] %s", shortcut, item.Label)
			label = r.padRight(label, panelWidth-4)
			r.write(label)

			if i == selectedIndex {
				r.write(Reset)
			}
			row++
		}
	}

	// Draw navigation hint at the very bottom of the last panel
	lastPanelBottom := currentRow + panelHeights[panelIdx] - 1
	r.moveTo(lastPanelBottom, panelCol+1)
	r.write(FgBrightBlack)
	hint := "↑↓ Nav  Enter Sel  1-9 Proc  Esc Close"
	if len(hint) > panelWidth-2 {
		hint = hint[:panelWidth-2]
	}
	r.write(r.padCenter(hint, panelWidth-2))
	r.write(Reset)

	r.write(CursorRestore + CursorShow)
}

// DrawPanelView draws a full-screen panel view with a niri-style tab bar at top.
// Panels are arranged horizontally like niri columns: Ctrl+Left/Right to navigate.
func (r *Renderer) DrawPanelView(panels []PanelItem, activeIndex int, status Status) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.write(ClearScreen + CursorHome + CursorHide)

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
	if active.Type != "overview" {
		title = active.Type + ": " + active.ID
	}
	r.drawNiriPanel(contentStartRow, panelCol, panelWidth, panelHeight, title, grad)

	// Fill content based on panel type
	contentRow := contentStartRow + 1
	contentWidth := panelWidth - 4

	switch active.Type {
	case "overview":
		r.drawOverviewContent(contentRow, panelCol+2, contentWidth, panelHeight-2, status)
	case "process":
		r.drawProcessPanelContent(contentRow, panelCol+2, contentWidth, panelHeight-2, active, status)
	case "proxy":
		r.drawProxyPanelContent(contentRow, panelCol+2, contentWidth, panelHeight-2, active, status)
	}

	// === FOOTER ===
	footerRow := r.height
	r.moveTo(footerRow, 1)
	r.write(BgBrightBlack + FgWhite)
	hint := fmt.Sprintf(" Ctrl+← → Navigate panels (%d/%d)  Esc Back ", activeIndex+1, len(panels))
	hint = r.padRight(hint, r.width)
	r.write(hint)
	r.write(Reset)

	r.write(CursorShow)
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

// drawOverviewContent draws the overview panel content (system summary).
func (r *Renderer) drawOverviewContent(startRow, col, width, maxRows int, status Status) {
	row := startRow

	// Connection status
	r.moveTo(row, col)
	if status.DaemonConnected == ConnectionConnected {
		pingStr := ""
		if status.DaemonPingMs > 0 {
			pingStr = fmt.Sprintf(" %s(%dms)%s", FgBrightBlack, status.DaemonPingMs, Reset)
		}
		r.write(fmt.Sprintf("%s%s%s connected%s", FgGreen, IconConnected, Reset, pingStr))
	} else {
		r.write(fmt.Sprintf("%s%s%s disconnected", FgYellow, IconDisconnected, Reset))
	}
	row += 2

	// Processes summary
	if len(status.Processes) > 0 && row < startRow+maxRows {
		r.moveTo(row, col)
		r.write(Bold + "processes" + Reset)
		row++

		for i, proc := range status.Processes {
			if row >= startRow+maxRows || i >= 8 {
				break
			}
			r.moveTo(row, col+1)
			stateColor := StateColorCode(proc.State)
			stateIcon := IconConnected
			if proc.State != "running" {
				stateIcon = IconDisconnected
			}

			idStr := proc.ID
			if len(idStr) > width-20 {
				idStr = idStr[:width-23] + "…"
			}

			r.write(fmt.Sprintf("%s%s%s %s", stateColor, stateIcon, Reset, idStr))

			if proc.Runtime > 0 {
				r.write(fmt.Sprintf(" %s%s%s", FgBrightBlack, formatShortDuration(proc.Runtime), Reset))
			}

			// Show URLs inline
			for _, u := range proc.URLs {
				display := u
				if len(display) > width-10 {
					display = display[:width-13] + "…"
				}
				r.write(fmt.Sprintf(" %s%s%s", FgBrightCyan+Underline, display, Reset))
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
				FgWhite+Bold, proxy.ID, Reset,
				FgBrightBlack, Reset,
				FgBrightCyan+Underline, proxyURL, Reset,
				errStr))
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
}

// drawProcessPanelContent draws the content for a process panel.
func (r *Renderer) drawProcessPanelContent(startRow, col, width, maxRows int, panel PanelItem, status Status) {
	// Find the process
	var proc *ProcessInfo
	for i := range status.Processes {
		if status.Processes[i].ID == panel.ID {
			proc = &status.Processes[i]
			break
		}
	}

	row := startRow
	if proc == nil {
		r.moveTo(row, col)
		r.write(FgBrightBlack + "process not found" + Reset)
		return
	}

	// Process header info
	r.moveTo(row, col)
	stateColor := StateColorCode(proc.State)
	r.write(fmt.Sprintf("%s %s%s%s%s",
		proc.Command,
		stateColor+Bold, proc.State, Reset,
		func() string {
			if proc.Runtime > 0 {
				return fmt.Sprintf(" %s%s%s", FgBrightBlack, formatShortDuration(proc.Runtime), Reset)
			}
			return ""
		}()))
	row++

	// URLs
	for _, u := range proc.URLs {
		if row >= startRow+maxRows {
			break
		}
		r.moveTo(row, col)
		r.write(FgBrightCyan + Underline + u + Reset)
		row++
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

	// Output content
	if panel.Content != "" {
		lines := strings.Split(panel.Content, "\n")

		// Show last lines that fit
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
		r.write(FgBrightBlack + "use Ctrl+← → to navigate, content loads on focus" + Reset)
	}
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
		r.write(FgBrightBlack + "use Ctrl+← → to navigate, content loads on focus" + Reset)
	}
}

// formatShortDuration formats a duration compactly for panel display.
func formatShortDuration(d time.Duration) string {
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	if d < time.Minute {
		return fmt.Sprintf("%.0fs", d.Seconds())
	}
	if d < time.Hour {
		return fmt.Sprintf("%.0fm", d.Minutes())
	}
	return fmt.Sprintf("%.1fh", d.Hours())
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

// DrawMenuWithProcesses draws a popup menu with a process list below it.
// Deprecated: Use DrawDashboard for a more comprehensive view.
func (r *Renderer) DrawMenuWithProcesses(menu Menu, selectedIndex int, processes []ProcessInfo) {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Calculate menu dimensions
	menuWidth := len(menu.Title) + 4
	for _, item := range menu.Items {
		itemWidth := len(item.Label) + 6 // "[x] " prefix + padding
		if itemWidth > menuWidth {
			menuWidth = itemWidth
		}
	}

	// Also consider process list width
	for i, proc := range processes {
		if i >= 9 {
			break
		}
		procLabel := fmt.Sprintf(" [%d] %s (%s)", i+1, proc.ID, proc.State)
		if len(procLabel)+2 > menuWidth {
			menuWidth = len(procLabel) + 2
		}
	}

	menuWidth = min(menuWidth+4, r.width-4) // Add padding, cap at screen width

	// Calculate height: menu items + process list + separators
	processCount := min(len(processes), 9)
	menuHeight := len(menu.Items) + 4 // Title + separator + items + bottom
	if processCount > 0 {
		menuHeight += processCount + 2 // separator + "Processes:" + items
	}

	// Calculate position (centered, but above indicator bar)
	startRow := (r.height-menuHeight)/2 - 1
	if startRow < 1 {
		startRow = 1
	}
	startCol := (r.width - menuWidth) / 2
	if startCol < 1 {
		startCol = 1
	}

	// Track the region for later clearing (only on first draw, not updates)
	if r.currentMenuRegion == nil {
		r.currentMenuRegion = &ScreenRegion{
			Row:    startRow,
			Col:    startCol,
			Width:  menuWidth,
			Height: menuHeight,
		}
		r.overlayStack.Push(RegionMenu, *r.currentMenuRegion)
	}

	r.write(CursorSave + CursorHide)

	// Draw box
	r.drawBox(startRow, startCol, menuWidth, menuHeight, menu.Title)

	// Draw menu items
	currentRow := startRow + 2
	for i, item := range menu.Items {
		r.moveTo(currentRow, startCol+1)

		if i == selectedIndex {
			r.write(BgBlue + FgWhite + Bold)
		}

		// Format: " [x] Label     "
		shortcut := " "
		if item.Shortcut != 0 {
			shortcut = string(item.Shortcut)
		}

		label := fmt.Sprintf(" [%s] %s", shortcut, item.Label)
		label = r.padRight(label, menuWidth-2)
		r.write(label)

		if i == selectedIndex {
			r.write(Reset)
		}
		currentRow++
	}

	// Draw process list if there are any
	if processCount > 0 {
		// Draw separator
		r.moveTo(currentRow, startCol+1)
		r.write(FgBrightBlack)
		r.write(r.padCenter("─ Processes (1-9 to view) ─", menuWidth-2))
		r.write(Reset)
		currentRow++

		// Draw processes
		for i, proc := range processes {
			if i >= 9 {
				break
			}
			r.moveTo(currentRow, startCol+1)

			stateColor := StateColorCode(proc.State)

			label := fmt.Sprintf(" [%s%d%s] %s %s(%s)%s",
				FgCyan, i+1, Reset,
				proc.ID,
				stateColor, proc.State, Reset)
			r.write(label)
			// Pad the rest
			visLen := r.estimateVisibleLength(label)
			if visLen < menuWidth-2 {
				r.write(strings.Repeat(" ", menuWidth-2-visLen))
			}
			currentRow++
		}
	}

	// Draw footer hint
	footerRow := startRow + menuHeight - 1
	r.moveTo(footerRow, startCol+1)
	r.write(FgBrightBlack)
	hint := " ↑↓ Navigate  Enter Select  Esc Close "
	hint = r.padCenter(hint, menuWidth-2)
	r.write(hint)
	r.write(Reset)

	r.write(CursorRestore + CursorShow)
}

// DrawInput draws a text input dialog.
func (r *Renderer) DrawInput(prompt, value string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	inputWidth := max(len(prompt)+4, 40)
	inputWidth = min(inputWidth, r.width-4)
	inputHeight := 5

	startRow := (r.height-inputHeight)/2 - 1
	if startRow < 1 {
		startRow = 1
	}
	startCol := (r.width - inputWidth) / 2
	if startCol < 1 {
		startCol = 1
	}

	// Track the region for later clearing (only on first draw, not updates)
	if r.currentInputRegion == nil {
		r.currentInputRegion = &ScreenRegion{
			Row:    startRow,
			Col:    startCol,
			Width:  inputWidth,
			Height: inputHeight,
		}
		r.overlayStack.Push(RegionInput, *r.currentInputRegion)
	}

	r.write(CursorSave + CursorHide)

	// Draw box
	r.drawBox(startRow, startCol, inputWidth, inputHeight, prompt)

	// Draw input field
	inputRow := startRow + 2
	r.moveTo(inputRow, startCol+2)
	r.write(FgCyan + "> " + Reset)

	// Draw value with cursor
	displayValue := value
	maxValueLen := inputWidth - 6 // Account for "> " and padding
	if len(displayValue) > maxValueLen {
		displayValue = displayValue[len(displayValue)-maxValueLen:]
	}
	r.write(displayValue)
	r.write(BgWhite + " " + Reset) // Cursor
	r.write(strings.Repeat(" ", maxValueLen-len(displayValue)))

	// Draw footer hint
	footerRow := startRow + inputHeight - 1
	r.moveTo(footerRow, startCol+1)
	r.write(FgBrightBlack)
	hint := " Enter Submit  Esc Cancel "
	hint = r.padCenter(hint, inputWidth-2)
	r.write(hint)
	r.write(Reset)

	r.write(CursorRestore + CursorShow)
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

// ClearCurrentMenu clears the current menu from screen and resets the region.
// Use this before transitioning to a different menu type (e.g., Dashboard to submenu).
func (r *Renderer) ClearCurrentMenu() {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Pop and clear the current menu from the overlay stack
	r.overlayStack.Pop()

	// Reset the tracked region so the next draw creates a fresh one
	r.currentMenuRegion = nil
}

// drawBox draws a box with a title.
func (r *Renderer) drawBox(row, col, width, height int, title string) {
	// Top border
	r.moveTo(row, col)
	r.write(FgCyan)
	r.write(BoxTopLeft)
	if title != "" {
		titlePart := " " + title + " "
		remaining := width - 2 - len(titlePart)
		leftPad := remaining / 2
		rightPad := remaining - leftPad
		r.write(strings.Repeat(BoxHorizontal, leftPad))
		r.write(Reset + Bold + title + Reset + FgCyan)
		r.write(strings.Repeat(BoxHorizontal, rightPad+2)) // +2 for spaces around title
	} else {
		r.write(strings.Repeat(BoxHorizontal, width-2))
	}
	r.write(BoxTopRight)

	// Side borders
	for i := 1; i < height-1; i++ {
		r.moveTo(row+i, col)
		r.write(BoxVertical)
		r.write(Reset + strings.Repeat(" ", width-2) + FgCyan)
		r.write(BoxVertical)
	}

	// Bottom border
	r.moveTo(row+height-1, col)
	r.write(BoxBottomLeft)
	r.write(strings.Repeat(BoxHorizontal, width-2))
	r.write(BoxBottomRight)
	r.write(Reset)
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
		fromFg: "\x1b[38;5;75m",  // Steel blue
		toFg:   "\x1b[38;5;69m",  // Medium blue
	}
	gradientError = niriGradient{
		fromFg: "\x1b[38;5;209m", // Salmon
		toFg:   "\x1b[38;5;167m", // Indian red
	}
	gradientProxy = niriGradient{
		fromFg: "\x1b[38;5;114m", // Pale green
		toFg:   "\x1b[38;5;72m",  // Cadet blue
	}
	gradientBrowser = niriGradient{
		fromFg: "\x1b[38;5;183m", // Plum
		toFg:   "\x1b[38;5;141m", // Medium purple
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
	defer r.mu.Unlock()

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
}

// DrawStatusBarMessage draws a message on the status bar at the bottom of the screen.
// Use this for transient status updates like spinners.
func (r *Renderer) DrawStatusBarMessage(message string) {
	r.mu.Lock()
	defer r.mu.Unlock()

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
}

// ClearStatusBarMessage clears any message on the status bar.
// After clearing, the regular indicator should be redrawn.
func (r *Renderer) ClearStatusBarMessage() {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.write(CursorSave + CursorHide)
	r.moveTo(r.height, 1)
	r.write(ClearLine)
	r.write(CursorRestore + CursorShow)
}
