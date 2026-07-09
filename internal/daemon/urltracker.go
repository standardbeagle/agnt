package daemon

import (
	"context"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/standardbeagle/go-cli-server/process"
)

// URLTracker monitors process output and extracts dev server URLs.
// Focused on capturing localhost URLs from dev server startup (e.g., pnpm dev).
// URLs are stored persistently per process ID so they survive buffer overflow.
type URLTracker struct {
	pm *process.ProcessManager
	mu sync.RWMutex

	// urls stores detected URLs per process ID (max 5 per process)
	urls map[string][]string

	// seenURLs tracks which URLs we've already recorded per process
	seenURLs map[string]map[string]bool

	// scannedStdout / scannedStderr track how much of each output stream we
	// have scanned per process. The two streams MUST be tracked separately:
	// a single offset into CombinedOutput() (= stdout ++ stderr) is incoherent
	// because the concatenation is not append-only — when stdout grows, its new
	// bytes are inserted before the stderr region, at offsets below a combined
	// cursor that stderr had already advanced, so a URL printed to stdout (e.g.
	// Vite's "Local: http://localhost:5173/" while deprecation warnings stream
	// to stderr) would sit below the cursor and never be scanned. See
	// scanStreamForURLs. We only look at the first 8KB of each stream (startup
	// phase).
	scannedStdout map[string]int
	scannedStderr map[string]int

	// urlMatchers stores URL matcher patterns per process ID
	// e.g., ["Local:\\s*{url}", "Network:\\s*{url}"]
	urlMatchers map[string][]string

	// scanInterval is how often to scan for new URLs
	scanInterval time.Duration

	// onURLDetected is called when a new URL is detected
	onURLDetected func(processID, url string)

	// onProcessStopped is called when a process is removed/stopped
	onProcessStopped func(processID string)

	// onProcessFirstSeen is called when a process is first scanned (for loading config)
	onProcessFirstSeen func(processID string)
}

// URLTrackerConfig configures the URL tracker.
type URLTrackerConfig struct {
	// ScanInterval is how often to scan process output for URLs.
	// Default: 500ms (fast for quick startup detection)
	ScanInterval time.Duration
}

// DefaultURLTrackerConfig returns sensible defaults.
func DefaultURLTrackerConfig() URLTrackerConfig {
	return URLTrackerConfig{
		ScanInterval: 500 * time.Millisecond,
	}
}

// NewURLTracker creates a new URL tracker.
func NewURLTracker(pm *process.ProcessManager, config URLTrackerConfig) *URLTracker {
	if config.ScanInterval == 0 {
		config.ScanInterval = 500 * time.Millisecond
	}

	return &URLTracker{
		pm:            pm,
		urls:          make(map[string][]string),
		seenURLs:      make(map[string]map[string]bool),
		scannedStdout: make(map[string]int),
		scannedStderr: make(map[string]int),
		urlMatchers:   make(map[string][]string),
		scanInterval:  config.ScanInterval,
	}
}

// SetURLMatchers sets URL matcher patterns for a specific process.
// Matchers support patterns like "Local:\\s*{url}" or "(Local|Network):\\s*{url}".
func (t *URLTracker) SetURLMatchers(processID string, matchers []string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if len(matchers) > 0 {
		t.urlMatchers[processID] = matchers
	} else {
		delete(t.urlMatchers, processID)
	}
}

// Start begins periodic URL scanning.
func (t *URLTracker) Start(ctx context.Context) {
	go t.scanLoop(ctx)
}

// GetURLs returns the detected URLs for a process.
func (t *URLTracker) GetURLs(processID string) []string {
	t.mu.RLock()
	defer t.mu.RUnlock()

	if urls, ok := t.urls[processID]; ok {
		// Return a copy
		result := make([]string, len(urls))
		copy(result, urls)
		return result
	}
	return nil
}

// ClearProcess removes URL tracking for a process.
func (t *URLTracker) ClearProcess(processID string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	delete(t.urls, processID)
	delete(t.seenURLs, processID)
	delete(t.scannedStdout, processID)
	delete(t.scannedStderr, processID)
}

// scanLoop periodically scans process output for URLs.
func (t *URLTracker) scanLoop(ctx context.Context) {
	ticker := time.NewTicker(t.scanInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			t.scanAllProcesses()
		}
	}
}

// Constants for URL detection
const (
	maxScanBytes      = 8 * 1024 // Only scan first 8KB of output (startup phase)
	maxURLsPerProcess = 5        // Max URLs to store per process
)

// scanAllProcesses scans all running processes for URLs.
func (t *URLTracker) scanAllProcesses() {
	procs := t.pm.List()

	for _, p := range procs {
		if p.State() == process.StateRunning {
			t.scanProcess(p)
		}
	}

	// Clean up tracking for removed processes
	t.cleanupRemovedProcesses(procs)
}

// scanStreamForURLs scans the unscanned tail of a single output stream for
// dev-server URLs. It returns any URLs found in buf[scanned:scanEnd] (scanEnd
// capped at maxScanBytes) and the new scanned offset for that stream.
//
// Tracking each stream against its own offset is the fix for a real bug: the
// previous implementation kept a single offset into CombinedOutput()
// (= stdout ++ stderr). That concatenation is not append-only — growth in
// stdout shifts new bytes in before the already-counted stderr region — so a
// URL on stdout could fall below the combined cursor and never be scanned
// (e.g. Vite 8: "Local: http://localhost:5173/" on stdout, deprecation
// warnings on stderr). Scanning each stream independently removes the coupling.
func scanStreamForURLs(buf []byte, scanned int, matchers []string) (urls []string, newScanned int) {
	scanEnd := len(buf)
	if scanEnd > maxScanBytes {
		scanEnd = maxScanBytes
	}
	if scanned >= scanEnd {
		return nil, scanned
	}
	return parseDevServerURLsWithMatchers(buf[scanned:scanEnd], matchers), scanEnd
}

// scanProcess scans a single process for dev server URLs.
func (t *URLTracker) scanProcess(p *process.ManagedProcess) {
	t.mu.Lock()

	// Check if we already have enough URLs
	if len(t.urls[p.ID]) >= maxURLsPerProcess {
		t.mu.Unlock()
		return
	}

	// Check if we've already scanned enough of both streams
	stdoutScanned := t.scannedStdout[p.ID]
	stderrScanned := t.scannedStderr[p.ID]
	if stdoutScanned >= maxScanBytes && stderrScanned >= maxScanBytes {
		t.mu.Unlock()
		return
	}

	// Call first-seen callback on first scan (for loading config).
	// This must happen BEFORE we scan, so matchers are available.
	isFirstScan := stdoutScanned == 0 && stderrScanned == 0

	t.mu.Unlock()

	// Load matchers before scanning on first scan
	if isFirstScan && t.onProcessFirstSeen != nil {
		t.onProcessFirstSeen(p.ID)
	}

	// Read stdout and stderr separately — never CombinedOutput(), whose
	// concatenation breaks a single byte cursor (see scanStreamForURLs).
	stdout, _ := p.Stdout()
	stderr, _ := p.Stderr()
	if len(stdout) == 0 && len(stderr) == 0 {
		return
	}

	// newURLs is collected under the lock and notified after it: onURLDetected
	// reaches the readiness signaler, the process manager, and the proxy-event
	// channel, and nothing about that work belongs inside the tracker's lock.
	var newURLs []string
	processID := p.ID
	defer func() {
		if t.onURLDetected == nil {
			return
		}
		for _, url := range newURLs {
			t.onURLDetected(processID, url)
		}
	}()

	t.mu.Lock()
	defer t.mu.Unlock()

	matchers := t.urlMatchers[p.ID]

	stdoutURLs, newStdout := scanStreamForURLs(stdout, t.scannedStdout[p.ID], matchers)
	stderrURLs, newStderr := scanStreamForURLs(stderr, t.scannedStderr[p.ID], matchers)
	t.scannedStdout[p.ID] = newStdout
	t.scannedStderr[p.ID] = newStderr

	if len(stdoutURLs) == 0 && len(stderrURLs) == 0 {
		return
	}

	// Initialize tracking maps if needed
	if t.seenURLs[p.ID] == nil {
		t.seenURLs[p.ID] = make(map[string]bool)
	}

	// Add new URLs and track which ones are new for callback notification.
	// seenURLs dedup naturally collapses a URL printed to both streams.
	for _, url := range append(stdoutURLs, stderrURLs...) {
		if t.seenURLs[p.ID][url] {
			continue // Already seen
		}

		// Check limit
		if len(t.urls[p.ID]) >= maxURLsPerProcess {
			break
		}

		// Add new URL
		t.urls[p.ID] = append(t.urls[p.ID], url)
		t.seenURLs[p.ID][url] = true
		newURLs = append(newURLs, url)
	}

	// The notify defer registered at the top of this function runs after the
	// unlock defer below it — defers are LIFO, so the earlier registration runs
	// last. Registering the callback here instead ran it *before* the unlock,
	// with the tracker's lock held: the opposite of what its comment claimed,
	// and a deadlock the first time a callback touches the URLTracker.
}

// cleanupRemovedProcesses removes tracking for processes that no longer exist.
func (t *URLTracker) cleanupRemovedProcesses(currentProcs []*process.ManagedProcess) {
	// Build set of current process IDs
	currentIDs := make(map[string]bool, len(currentProcs))
	for _, p := range currentProcs {
		currentIDs[p.ID] = true
	}

	t.mu.Lock()

	// Remove tracking for processes that don't exist and collect stopped IDs
	var stoppedProcesses []string
	for id := range t.urls {
		if !currentIDs[id] {
			delete(t.urls, id)
			delete(t.seenURLs, id)
			delete(t.scannedStdout, id)
			delete(t.scannedStderr, id)
			delete(t.urlMatchers, id)
			stoppedProcesses = append(stoppedProcesses, id)
		}
	}

	t.mu.Unlock()

	// Notify about stopped processes (after releasing lock)
	if t.onProcessStopped != nil {
		for _, id := range stoppedProcesses {
			t.onProcessStopped(id)
		}
	}
}

// Regex to match localhost-like dev server URLs.
// Only matches true localhost addresses (localhost, 127.0.0.1, 0.0.0.0, [::1]).
// Network IP addresses (192.168.x.x, 10.x.x.x) are excluded to avoid duplicate proxies.
var devServerURLRegex = regexp.MustCompile(`https?://(?:localhost|127\.0\.0\.1|0\.0\.0\.0|\[::1\]):\d+[^\s\)\]\}'"<>]*`)

// parseDevServerURLs extracts dev server URLs from output.
// Only returns localhost-like URLs that look like dev servers.
func parseDevServerURLs(output []byte) []string {
	return parseDevServerURLsWithMatchers(output, nil)
}

// parseDevServerURLsWithMatchers extracts URLs matching specific patterns.
// If matchers is nil or empty, returns all detected URLs.
// Matchers support patterns like "Local:\s*{url}" or "(Local|Network):\s*{url}".
func parseDevServerURLsWithMatchers(output []byte, matchers []string) []string {
	lines := strings.Split(string(output), "\n")
	seen := make(map[string]bool)
	var urls []string

	for _, line := range lines {
		// Strip ANSI escape codes so patterns and URL regex match
		// even when output contains terminal color/formatting sequences
		// (e.g., Vite wraps URLs in \x1b[36m...\x1b[0m)
		line = ansiEscapeRegex.ReplaceAllString(line, "")

		// If no matchers specified, scan entire line for URLs
		if len(matchers) == 0 {
			lineMatches := devServerURLRegex.FindAllString(line, -1)
			for _, match := range lineMatches {
				match = normalizeURL(match)
				if !seen[match] && !shouldIgnoreURL(match) {
					seen[match] = true
					urls = append(urls, match)
				}
			}
			continue
		}

		// Check if line matches any of the patterns
		for _, matcher := range matchers {
			if matchesURLPattern(line, matcher) {
				// Extract URL from the line
				lineMatches := devServerURLRegex.FindAllString(line, -1)
				for _, match := range lineMatches {
					match = normalizeURL(match)
					if !seen[match] && !shouldIgnoreURL(match) {
						seen[match] = true
						urls = append(urls, match)
					}
				}
				break // Line matched, no need to check other patterns
			}
		}
	}

	return urls
}

// ansiEscapeRegex matches ANSI escape sequences (colors, cursor movement, etc.)
var ansiEscapeRegex = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)

// normalizeURL normalizes a URL for consistent comparison.
// Removes ANSI escape codes, trailing slashes and punctuation, lowercases the scheme and host.
func normalizeURL(u string) string {
	// Strip ANSI escape codes (terminal colors/formatting)
	u = ansiEscapeRegex.ReplaceAllString(u, "")

	// Trim common trailing chars first
	u = strings.TrimRight(u, ".,;:)/")

	// Parse and normalize
	parsed, err := url.Parse(u)
	if err != nil {
		return u // Return as-is if parsing fails
	}

	// Lowercase scheme and host
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	parsed.Host = strings.ToLower(parsed.Host)

	// Remove trailing slash from path (but keep root "/" as empty)
	parsed.Path = strings.TrimSuffix(parsed.Path, "/")

	return parsed.String()
}

// matchesURLPattern checks if a line matches a URL matcher pattern.
// Supports patterns like:
//   - "Local:\s*{url}" - matches lines containing "Local:" followed by a URL
//   - "(Local|Network):\s*{url}" - matches lines with "Local:" or "Network:"
//   - "{url}" - matches any line with a URL
func matchesURLPattern(line, pattern string) bool {
	// Replace {url} placeholder with a simple marker
	// We don't need to match the actual URL, just check if the prefix exists
	pattern = strings.ReplaceAll(pattern, "{url}", "")
	pattern = strings.TrimSpace(pattern)

	if pattern == "" {
		// Empty pattern matches any line with a URL
		return devServerURLRegex.MatchString(line)
	}

	// Handle regex-style patterns like "(Local|Network):"
	// Simple matching: check if the line contains the pattern (after removing {url})
	matched, _ := regexp.MatchString(pattern, line)
	return matched
}

// shouldIgnoreURL returns true if the URL should be ignored.
func shouldIgnoreURL(url string) bool {
	lower := strings.ToLower(url)

	// Ignore URLs with certain paths that suggest errors/APIs
	ignoredPaths := []string{
		"/api/",
		"/error",
		"/debug",
		"/.well-known/",
		"/favicon",
		"/static/",
		"/assets/",
		"/node_modules/",
	}
	for _, path := range ignoredPaths {
		if strings.Contains(lower, path) {
			return true
		}
	}

	// Ignore URLs with query strings (usually not the main dev server URL)
	if strings.Contains(url, "?") {
		return true
	}

	return false
}

// parseURLsFromBytes extracts unique URLs from output bytes.
// This is a broader parser used as fallback - prefer parseDevServerURLs.
func parseURLsFromBytes(output []byte) []string {
	// Use the dev server regex for consistency
	return parseDevServerURLs(output)
}
