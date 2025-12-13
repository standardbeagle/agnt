package overlay

import (
	"fmt"
	"io"
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

// Renderer handles drawing to the terminal.
type Renderer struct {
	out          io.Writer
	width        int
	height       int
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
		screenMgr:    sm,
		overlayStack: NewOverlayStack(sm),
	}
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

	// Running processes count
	runningCount := 0
	for _, p := range status.Processes {
		if p.State == "running" {
			runningCount++
		}
	}
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
			proxyURL = "http://" + normalizeListenAddr(p.ListenAddr)
		}
		if tunnelURL == "" && p.TunnelURL != "" {
			tunnelURL = p.TunnelURL
		}
	}
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
			// Show clickable URL (terminals support OSC 8 hyperlinks or just show URL for ctrl+click)
			if errorProxyCount > 0 {
				parts = append(parts, fmt.Sprintf("%s%s%s %s%s %s(%d err)%s",
					FgMagenta, IconProxy, Reset, urlColor+Underline, displayURL, FgRed, errorProxyCount, Reset))
			} else {
				parts = append(parts, fmt.Sprintf("%s%s%s %s%s%s",
					FgMagenta, IconProxy, Reset, urlColor+Underline, displayURL, Reset))
			}
		} else if errorProxyCount > 0 {
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
	hotkeyHint := fmt.Sprintf("%sCtrl+P%s", FgBrightBlack, Reset)

	// Calculate padding
	// Note: This is approximate due to ANSI codes; for accurate width we'd need to strip codes
	visibleLen := r.estimateVisibleLength(statusText)
	hotkeyLen := 6                                  // "Ctrl+P"
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

// normalizeListenAddr converts wildcard addresses to localhost for clickable URLs.
// This is the most reliable option since LAN IPs can be unreliable in virtual environments
// (WSL2, Docker, etc.). Users who need LAN access can check the detailed proxy output.
func normalizeListenAddr(addr string) string {
	var port string

	// Extract port from wildcard addresses
	if strings.HasPrefix(addr, "[::]:") {
		port = addr[4:] // Get :port part
	} else if strings.HasPrefix(addr, "0.0.0.0:") {
		port = addr[7:] // Get :port part
	} else if addr == "[::]" || addr == "0.0.0.0" {
		port = ""
	} else {
		// Not a wildcard address, return as-is
		return addr
	}

	return "localhost" + port
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

// DrawDashboard draws a comprehensive dashboard with status, proxies, processes, and menu.
func (r *Renderer) DrawDashboard(menu Menu, selectedIndex int, status Status) {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Calculate dashboard dimensions - use more screen real estate
	dashWidth := min(r.width-4, 80)

	// Calculate content rows needed
	browserCount := min(len(status.BrowserSessions), 4)
	processCount := min(len(status.Processes), 6)
	menuItemCount := len(menu.Items)

	// Count standalone proxies (not linked to a process), tunnels, and tailscale URLs
	standaloneProxyCount := 0
	tunnelCount := 0
	tailscaleCount := 0
	for _, p := range status.Proxies {
		if p.LinkedProcessID == "" {
			standaloneProxyCount++
			if p.TailscaleURL != "" {
				tailscaleCount++
			}
			if p.TunnelURL != "" {
				tunnelCount++
			}
		}
	}

	// Count processes with output (for extra lines)
	outputLineCount := 0
	for i, p := range status.Processes {
		if i >= 6 {
			break
		}
		if p.LastOutput != "" {
			outputLineCount++
		}
	}

	// Dashboard sections:
	// 1. Header (title + connection status)
	// 2. Dev servers section (processes with linked proxies + output)
	// 3. Standalone proxies section (if any)
	// 4. Browser sessions section (if any)
	// 5. Menu section
	// 6. Footer
	dashHeight := 4 + menuItemCount // header + menu + footer
	if processCount > 0 {
		// Each process takes 1 line, plus 1 line for output if present
		dashHeight += processCount + outputLineCount + 2
	}
	if standaloneProxyCount > 0 {
		dashHeight += standaloneProxyCount + tunnelCount + tailscaleCount + 2
	}
	if browserCount > 0 {
		dashHeight += browserCount + 2
	}
	dashHeight = min(dashHeight, r.height-2)

	// Center the dashboard
	startRow := max((r.height-dashHeight)/2, 1)
	startCol := max((r.width-dashWidth)/2, 1)

	// Track the region for later clearing
	if r.currentMenuRegion == nil {
		r.currentMenuRegion = &ScreenRegion{
			Row:    startRow,
			Col:    startCol,
			Width:  dashWidth,
			Height: dashHeight,
		}
		r.overlayStack.Push(RegionMenu, *r.currentMenuRegion)
	}

	r.write(CursorSave + CursorHide)

	// Draw outer box
	r.drawBox(startRow, startCol, dashWidth, dashHeight, "agnt Dashboard")

	currentRow := startRow + 2

	// === CONNECTION STATUS ===
	r.moveTo(currentRow, startCol+2)
	connStatus := fmt.Sprintf("%s%s%s Connected", FgGreen, IconConnected, Reset)
	if status.DaemonConnected != ConnectionConnected {
		connStatus = fmt.Sprintf("%s%s%s Disconnected", FgYellow, IconDisconnected, Reset)
	}
	r.write(connStatus)

	// Show error count on same line if any
	recentErrors := 0
	cutoff := time.Now().Add(-5 * time.Minute)
	for _, e := range status.RecentErrors {
		if e.Timestamp.After(cutoff) {
			recentErrors++
		}
	}
	if recentErrors > 0 {
		r.write(fmt.Sprintf("  %s%s %d errors%s", FgRed, IconWarning, recentErrors, Reset))
	}
	currentRow++

	// Build proxy lookup map for quick access
	proxyByID := make(map[string]ProxyInfo)
	for _, p := range status.Proxies {
		proxyByID[p.ID] = p
	}

	// === DEV SERVERS SECTION (Processes with linked proxies) ===
	if processCount > 0 {
		currentRow++ // spacing
		r.moveTo(currentRow, startCol+2)
		r.write(FgCyan + Bold + "DEV SERVERS" + Reset + FgBrightBlack + " (1-9 to view output)" + Reset)
		currentRow++

		for i, proc := range status.Processes {
			if i >= 6 {
				r.moveTo(currentRow, startCol+3)
				r.write(FgBrightBlack + fmt.Sprintf("  ... and %d more", len(status.Processes)-6) + Reset)
				currentRow++
				break
			}
			r.moveTo(currentRow, startCol+3)

			// State-based styling
			stateIcon := FgBrightBlack + "○" + Reset
			stateColor := FgBrightBlack
			switch proc.State {
			case "running":
				stateIcon = FgGreen + IconConnected + Reset
				stateColor = FgGreen
			case "failed":
				stateIcon = FgRed + IconError + Reset
				stateColor = FgRed
			case "stopped":
				stateIcon = FgYellow + "■" + Reset
				stateColor = FgYellow
			}

			// Runtime formatting
			runtime := ""
			if proc.Runtime > 0 {
				if proc.Runtime < time.Minute {
					runtime = fmt.Sprintf("%ds", int(proc.Runtime.Seconds()))
				} else {
					runtime = fmt.Sprintf("%dm", int(proc.Runtime.Minutes()))
				}
			}

			// Build process line
			line := fmt.Sprintf("[%s%d%s] %s %s%s%s %s%s%s",
				FgCyan, i+1, Reset,
				stateIcon,
				FgWhite+Bold, proc.ID, Reset,
				stateColor, proc.State, Reset)

			// Add linked proxy URL if present
			if proc.LinkedProxyID != "" {
				if proxy, ok := proxyByID[proc.LinkedProxyID]; ok {
					proxyURL := "http://" + normalizeListenAddr(proxy.ListenAddr)
					line += fmt.Sprintf(" %s→%s %s%s%s",
						FgBrightBlack, Reset,
						FgBrightCyan+Underline, proxyURL, Reset)
					// Add Tailscale URL if available
					if proxy.TailscaleURL != "" {
						line += fmt.Sprintf(" %s|%s %s%s%s",
							FgBrightBlack, Reset,
							FgMagenta+Underline, proxy.TailscaleURL, Reset)
					}
					if proxy.HasErrors {
						line += fmt.Sprintf(" %s(%d err)%s", FgRed, proxy.ErrorCount, Reset)
					}
				}
			}

			// Add runtime at the end
			if runtime != "" {
				line += fmt.Sprintf(" %s%s%s", FgBrightBlack, runtime, Reset)
			}

			r.write(line)
			currentRow++

			// Show last output line on next row
			if proc.LastOutput != "" {
				r.moveTo(currentRow, startCol+5)
				// Truncate output to fit dashboard width
				maxOutputLen := dashWidth - 8
				output := proc.LastOutput
				if len(output) > maxOutputLen {
					output = output[:maxOutputLen-3] + "..."
				}
				r.write(FgBrightBlack + "└ " + output + Reset)
				currentRow++
			}
		}
	}

	// === STANDALONE PROXIES SECTION (proxies not linked to processes) ===
	if standaloneProxyCount > 0 {
		currentRow++ // spacing
		r.moveTo(currentRow, startCol+2)
		r.write(FgCyan + Bold + "PROXIES" + Reset + FgBrightBlack + " (ctrl+click URL to open)" + Reset)
		currentRow++

		for _, proxy := range status.Proxies {
			// Skip proxies linked to processes (already shown above)
			if proxy.LinkedProcessID != "" {
				continue
			}

			r.moveTo(currentRow, startCol+3)

			// Status icon
			statusIcon := FgGreen + IconOK + Reset
			if proxy.HasErrors {
				statusIcon = FgRed + IconWarning + Reset
			}

			// Build proxy line with clickable URL
			proxyURL := "http://" + normalizeListenAddr(proxy.ListenAddr)
			line := fmt.Sprintf("%s %s%s%s → %s%s%s",
				statusIcon,
				FgWhite+Bold, proxy.ID, Reset,
				FgBrightCyan+Underline, proxyURL, Reset)

			if proxy.HasErrors {
				line += fmt.Sprintf(" %s(%d errors)%s", FgRed, proxy.ErrorCount, Reset)
			}

			r.write(line)
			currentRow++

			// Show Tailscale URL on a separate line if available
			if proxy.TailscaleURL != "" {
				r.moveTo(currentRow, startCol+5)
				tailscaleIcon := FgMagenta + "⟷" + Reset // Mesh network icon
				tailscaleLine := fmt.Sprintf("%s %s%s%s %s(tailnet)%s",
					tailscaleIcon,
					FgMagenta+Underline, proxy.TailscaleURL, Reset,
					FgBrightBlack, Reset)
				r.write(tailscaleLine)
				currentRow++
			}

			// Show tunnel URL on a separate line if available
			if proxy.TunnelURL != "" {
				r.moveTo(currentRow, startCol+5)
				tunnelIcon := FgGreen + "⇡" + Reset
				if !proxy.TunnelRunning {
					tunnelIcon = FgYellow + "⇡" + Reset
				}
				tunnelLine := fmt.Sprintf("%s %s%s%s",
					tunnelIcon,
					FgBrightMagenta+Underline, proxy.TunnelURL, Reset)
				r.write(tunnelLine)
				currentRow++
			}
		}
	}

	// === BROWSER SESSIONS SECTION ===
	if browserCount > 0 {
		currentRow++ // spacing
		r.moveTo(currentRow, startCol+2)
		r.write(FgCyan + Bold + "BROWSERS" + Reset + FgBrightBlack + " (connected sessions)" + Reset)
		currentRow++

		for i, session := range status.BrowserSessions {
			if i >= 4 {
				r.moveTo(currentRow, startCol+3)
				r.write(FgBrightBlack + fmt.Sprintf("  ... and %d more", len(status.BrowserSessions)-4) + Reset)
				currentRow++
				break
			}
			r.moveTo(currentRow, startCol+3)

			// Truncate URL for display
			displayURL := session.URL
			maxURLLen := dashWidth - 30
			if len(displayURL) > maxURLLen {
				displayURL = displayURL[:maxURLLen-3] + "..."
			}

			// Activity indicator
			activityIcon := FgGreen + IconConnected + Reset
			if time.Since(session.LastActivity) > 30*time.Second {
				activityIcon = FgBrightBlack + IconDisconnected + Reset
			}

			line := fmt.Sprintf("%s %s%s%s",
				activityIcon,
				FgWhite, displayURL, Reset)

			// Show interactions/mutations if any
			if session.Interactions > 0 || session.Mutations > 0 {
				line += fmt.Sprintf(" %s(%d clicks, %d mutations)%s",
					FgBrightBlack, session.Interactions, session.Mutations, Reset)
			}

			r.write(line)
			currentRow++
		}
	}

	// === MENU SECTION ===
	currentRow++ // spacing
	r.moveTo(currentRow, startCol+2)
	r.write(FgCyan + Bold + "ACTIONS" + Reset)
	currentRow++

	for i, item := range menu.Items {
		r.moveTo(currentRow, startCol+3)

		if i == selectedIndex {
			r.write(BgBlue + FgWhite + Bold)
		}

		shortcut := " "
		if item.Shortcut != 0 {
			shortcut = string(item.Shortcut)
		}

		label := fmt.Sprintf("[%s] %s", shortcut, item.Label)
		label = r.padRight(label, dashWidth-6)
		r.write(label)

		if i == selectedIndex {
			r.write(Reset)
		}
		currentRow++
	}

	// === FOOTER ===
	footerRow := startRow + dashHeight - 1
	r.moveTo(footerRow, startCol+1)
	r.write(FgBrightBlack)
	hint := " ↑↓ Navigate │ Enter Select │ 1-9 View Process │ Esc Close "
	hint = r.padCenter(hint, dashWidth-2)
	r.write(hint)
	r.write(Reset)

	r.write(CursorRestore + CursorShow)
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

			stateColor := FgBrightBlack
			if proc.State == "running" {
				stateColor = FgGreen
			} else if proc.State == "failed" {
				stateColor = FgRed
			}

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

// DrawProcessOutput draws the process output viewer on the alt screen.
func (r *Renderer) DrawProcessOutput(processID, command, state, output string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Clear screen and draw header
	r.write(ClearScreen + CursorHome + CursorHide)

	// Draw header bar (row 1)
	r.moveTo(1, 1)
	r.write(ClearLine)
	r.write(BgBrightBlack + FgWhite + Bold)
	header := fmt.Sprintf(" Process: %s | Command: %s | State: %s ", processID, command, state)
	header = r.padRight(header, r.width)
	r.write(header)
	r.write(Reset)

	// Draw separator (row 2)
	r.moveTo(2, 1)
	r.write(ClearLine)
	r.write(FgBrightBlack + strings.Repeat("─", r.width) + Reset)

	// Draw output lines (leave room for header, separator, and footer)
	lines := strings.Split(output, "\n")
	maxLines := r.height - 4 // header + separator + footer + blank

	// Wrap long lines and collect all display lines
	var displayLines []string
	for _, line := range lines {
		if len(line) == 0 {
			displayLines = append(displayLines, "")
			continue
		}
		// Wrap long lines instead of truncating
		for len(line) > 0 {
			if len(line) <= r.width {
				displayLines = append(displayLines, line)
				break
			}
			displayLines = append(displayLines, line[:r.width])
			line = line[r.width:]
		}
	}

	// Show last N lines if output is longer
	startLine := 0
	if len(displayLines) > maxLines {
		startLine = len(displayLines) - maxLines
	}

	// Draw each line at explicit row positions
	currentRow := 3
	for i := startLine; i < len(displayLines) && currentRow < r.height-1; i++ {
		r.moveTo(currentRow, 1)
		r.write(ClearLine)
		r.write(displayLines[i])
		currentRow++
	}

	// Clear any remaining lines
	for currentRow < r.height-1 {
		r.moveTo(currentRow, 1)
		r.write(ClearLine)
		currentRow++
	}

	// Move to bottom and draw footer
	r.moveTo(r.height, 1)
	r.write(ClearLine)
	r.write(BgBrightBlack + FgWhite)
	footer := " Press any key to close "
	footer = r.padCenter(footer, r.width)
	r.write(footer)
	r.write(Reset)
}
