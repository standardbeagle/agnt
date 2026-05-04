//go:build windows

package main

import (
	"io"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"syscall"
)

// BrowserHelper wraps an io.Writer and detects URLs in the output,
// automatically opening them in the default browser. This helps with
// OAuth flows where the child process tries to open a browser but the
// ConPTY environment may prevent it from working correctly.
//
// Windows-only — the Unix run loop relies on the agent process being
// able to invoke `open`/`xdg-open`/`gio open` itself.
type BrowserHelper struct {
	dest       io.Writer
	buffer     []byte
	urlPattern *regexp.Regexp
	opened     map[string]bool // Track URLs we've already opened
	mu         sync.Mutex
}

// NewBrowserHelper creates a new BrowserHelper that wraps the given writer.
func NewBrowserHelper(dest io.Writer) *BrowserHelper {
	return &BrowserHelper{
		dest:       dest,
		urlPattern: regexp.MustCompile(`https?://[^\s\x00-\x1f"'<>|\x7f]+`),
		opened:     make(map[string]bool),
	}
}

// Write implements io.Writer, scanning for URLs and opening them.
func (b *BrowserHelper) Write(p []byte) (n int, err error) {
	n, err = b.dest.Write(p)

	b.mu.Lock()
	defer b.mu.Unlock()

	// Append to buffer for URL detection (keep last 4KB).
	b.buffer = append(b.buffer, p...)
	if len(b.buffer) > 4096 {
		b.buffer = b.buffer[len(b.buffer)-4096:]
	}

	matches := b.urlPattern.FindAll(b.buffer, -1)
	for _, match := range matches {
		url := string(match)
		url = strings.TrimRight(url, ".,;:!?)]}>\"'")
		if !b.opened[url] && isAuthURL(url) {
			b.opened[url] = true
			go openBrowser(url)
		}
	}

	return n, err
}

// isAuthURL checks if a URL looks like an authentication/OAuth URL.
func isAuthURL(url string) bool {
	authPatterns := []string{
		"oauth",
		"auth",
		"login",
		"signin",
		"sign-in",
		"callback",
		"authorize",
		"console.anthropic.com",
		"accounts.google.com",
		"github.com/login",
		"login.microsoftonline.com",
	}
	urlLower := strings.ToLower(url)
	for _, pattern := range authPatterns {
		if strings.Contains(urlLower, pattern) {
			return true
		}
	}
	return false
}

// openBrowser opens a URL in the default browser on Windows.
func openBrowser(url string) {
	cmd := exec.Command("cmd", "/c", "start", "", url)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow: true,
	}
	_ = cmd.Run()
}
