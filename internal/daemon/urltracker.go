package daemon

import (
	"context"
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

	// scannedBytes tracks how much output we've scanned per process
	// We only look at the first 8KB of output (startup phase)
	scannedBytes map[string]int

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
		pm:           pm,
		urls:         make(map[string][]string),
		seenURLs:     make(map[string]map[string]bool),
		scannedBytes: make(map[string]int),
		urlMatchers:  make(map[string][]string),
		scanInterval: config.ScanInterval,
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
	delete(t.scannedBytes, processID)
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

// scanProcess scans a single process for dev server URLs.
func (t *URLTracker) scanProcess(p *process.ManagedProcess) {
	t.mu.Lock()

	// Check if we've already scanned enough of this process
	if t.scannedBytes[p.ID] >= maxScanBytes {
		t.mu.Unlock()
		return
	}

	// Check if we already have enough URLs
	if len(t.urls[p.ID]) >= maxURLsPerProcess {
		t.mu.Unlock()
		return
	}

	// Call first-seen callback on first scan (for loading config)
	// This must happen BEFORE we scan, so matchers are available
	isFirstScan := t.scannedBytes[p.ID] == 0

	t.mu.Unlock()

	// Load matchers before scanning on first scan
	if isFirstScan && t.onProcessFirstSeen != nil {
		t.onProcessFirstSeen(p.ID)
	}

	// Get combined output
	output, _ := p.CombinedOutput()
	if len(output) == 0 {
		return
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	// Only scan up to maxScanBytes
	scanEnd := len(output)
	if scanEnd > maxScanBytes {
		scanEnd = maxScanBytes
	}

	// Only scan new bytes since last time
	scanStart := t.scannedBytes[p.ID]
	if scanStart >= scanEnd {
		return
	}

	// Update scanned position
	t.scannedBytes[p.ID] = scanEnd

	// Get URL matchers for this process (if any)
	matchers := t.urlMatchers[p.ID]

	// Parse dev server URLs from the new portion using matchers
	urls := parseDevServerURLsWithMatchers(output[scanStart:scanEnd], matchers)
	if len(urls) == 0 {
		return
	}

	// Initialize tracking maps if needed
	if t.seenURLs[p.ID] == nil {
		t.seenURLs[p.ID] = make(map[string]bool)
	}

	// Add new URLs and track which ones are new for callback notification
	var newURLs []string
	processID := p.ID // Save processID for callback after unlock

	for _, url := range urls {
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

	// Lock will be released by defer, then we notify about new URLs
	// We defer the callback to avoid holding the lock during notification
	if len(newURLs) > 0 && t.onURLDetected != nil {
		// Note: defer executes in LIFO order, so this runs AFTER the mutex unlock
		defer func() {
			for _, url := range newURLs {
				t.onURLDetected(processID, url)
			}
		}()
	}
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
			delete(t.scannedBytes, id)
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
		// If no matchers specified, scan entire line for URLs
		if len(matchers) == 0 {
			lineMatches := devServerURLRegex.FindAllString(line, -1)
			for _, match := range lineMatches {
				match = strings.TrimRight(match, ".,;:)")
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
					match = strings.TrimRight(match, ".,;:)")
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
