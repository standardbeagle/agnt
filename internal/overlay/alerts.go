package overlay

import (
	"crypto/sha256"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// AlertSeverity indicates the severity of an alert match.
type AlertSeverity string

const (
	AlertSeverityError   AlertSeverity = "error"
	AlertSeverityWarning AlertSeverity = "warning"
	AlertSeverityInfo    AlertSeverity = "info"
)

// AlertPattern defines a regex pattern to match against process output.
type AlertPattern struct {
	ID          string
	Pattern     *regexp.Regexp
	Severity    AlertSeverity
	Category    string // e.g. "dotnet", "webpack", "go", "generic"
	Description string
}

// AlertSource tags where a match originated. Default zero value
// (AlertSourceProcess) covers regex-matched process output. Non-default
// sources route through the same dedup/batch/activity-defer queue but
// carry pre-rendered text in RenderedText so OnAlert can frame them
// without the process-alert wrapper.
type AlertSource string

const (
	// AlertSourceProcess is the default — match came from process stdout/stderr.
	AlertSourceProcess AlertSource = ""
	// AlertSourceBrowser is a browser-JS error injected from a proxy.
	AlertSourceBrowser AlertSource = "browser"
	// AlertSourceHTTP is an HTTP 4xx/5xx response injected from a proxy.
	AlertSourceHTTP AlertSource = "http"
)

// AlertMatch represents a single matched alert from process output.
type AlertMatch struct {
	Pattern   *AlertPattern
	Line      string
	Timestamp time.Time
	ScriptID  string
	// Source tags origin for OnAlert dispatch. Empty = process-output.
	Source AlertSource
	// RenderedText, when set, is the pre-formatted PTY-ready text for
	// this match. Used by non-process sources whose framing differs from
	// the canonical "[agnt process alert] Script %q detected issues"
	// wrapper. AlertBatch.Format honors RenderedText when every match in
	// a batch carries it, otherwise falls back to the default wrapper.
	RenderedText string
}

// AlertBatch is a collection of alert matches to be delivered together.
type AlertBatch struct {
	Matches  []*AlertMatch
	ScriptID string
}

// MaxSeverity returns the highest severity in the batch.
func (b *AlertBatch) MaxSeverity() AlertSeverity {
	hasSeverity := map[AlertSeverity]bool{}
	for _, m := range b.Matches {
		hasSeverity[m.Pattern.Severity] = true
	}
	if hasSeverity[AlertSeverityError] {
		return AlertSeverityError
	}
	if hasSeverity[AlertSeverityWarning] {
		return AlertSeverityWarning
	}
	return AlertSeverityInfo
}

// Format renders the batch as a human-readable message for the AI agent.
func (b *AlertBatch) Format() string {
	if len(b.Matches) == 0 {
		return ""
	}

	// Pre-rendered fast path: when every match in the batch carries
	// RenderedText, emit those joined directly. Used by non-process
	// sources (browser-JS errors, HTTP errors) whose framing is
	// determined upstream and would be wrong under the
	// "Script %q detected issues" wrapper.
	allRendered := true
	for _, m := range b.Matches {
		if m.RenderedText == "" {
			allRendered = false
			break
		}
	}
	if allRendered {
		var sb strings.Builder
		for _, m := range b.Matches {
			sb.WriteString(m.RenderedText)
			if !strings.HasSuffix(m.RenderedText, "\n") {
				sb.WriteByte('\n')
			}
		}
		return sb.String()
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("[agnt process alert] Script %q detected issues:\n", b.ScriptID))

	// Group by severity
	bySeverity := map[AlertSeverity][]*AlertMatch{}
	for _, m := range b.Matches {
		bySeverity[m.Pattern.Severity] = append(bySeverity[m.Pattern.Severity], m)
	}

	// Output in severity order: error, warning, info
	for _, sev := range []AlertSeverity{AlertSeverityError, AlertSeverityWarning, AlertSeverityInfo} {
		matches := bySeverity[sev]
		if len(matches) == 0 {
			continue
		}

		sb.WriteString(fmt.Sprintf("\n%ss (%d):\n", capitalize(string(sev)), len(matches)))
		for _, m := range matches {
			line := m.Line
			if len(line) > 120 {
				line = line[:117] + "..."
			}
			sb.WriteString(fmt.Sprintf("  - %s\n", line))
		}
	}

	if bySeverity[AlertSeverityError] != nil {
		sb.WriteString("\nConsider restarting the dev server.\n")
	}

	return sb.String()
}

// AlertScannerConfig configures the AlertScanner.
type AlertScannerConfig struct {
	// Patterns are additional custom patterns to register.
	Patterns []*AlertPattern

	// DisabledIDs is a set of pattern IDs to disable.
	DisabledIDs []string

	// BatchWindow is how long to collect alerts before flushing.
	// Default: 3 seconds.
	BatchWindow time.Duration

	// DedupeWindow is how long to suppress duplicate alerts.
	// Default: 60 seconds.
	DedupeWindow time.Duration

	// ActivityState returns the current activity state of the AI agent.
	// If non-nil and returns ActivityActive, flush is deferred.
	ActivityState func() ActivityState

	// OnAlert is called when a batch of alerts is ready for delivery.
	OnAlert func(*AlertBatch)

	// RetryInterval is how often the scanner retries delivering deferred alerts.
	// Zero means use the default (2s).
	RetryInterval time.Duration
}

// matchBufSize is the fixed capacity of the ring buffer for recent matches.
const matchBufSize = 200

// recentLineBufSize is how many recent non-empty lines we retain per scanner
// to attach preceding context ("cause line") to an unparsed catch-all match.
const recentLineBufSize = 3

// unparsedPatternID is the synthetic pattern ID stamped on catch-all matches
// for error-looking lines that no real pattern classified. Surfacing them
// (rather than dropping them) guarantees no genuine error is silently lost;
// downstream consumers key on Category=="unparsed" to render them distinctly.
const unparsedPatternID = "unparsed"

// unparsedErrorRe is a deliberately conservative whole-word heuristic for
// "this stderr line looks like an error" used only as a last resort after the
// real pattern bank declines a line. Word boundaries keep it from firing on
// substrings ("errorless", "refusenik") and the term set is limited to high-
// signal failure vocabulary so ordinary log chatter is not promoted to an
// alert. Existing dedup + batch caps bound any residual flooding.
var unparsedErrorRe = regexp.MustCompile(`(?i)\b(error|errors|failed|failure|exception|fatal|panic|denied|refused|not valid|invalid|unauthorized)\b`)

// unparsedPattern is the synthetic AlertPattern carried by catch-all matches.
var unparsedPattern = &AlertPattern{
	ID:          unparsedPatternID,
	Pattern:     unparsedErrorRe,
	Severity:    AlertSeverityError,
	Category:    "unparsed",
	Description: "Unclassified error-looking output (no specific pattern matched)",
}

// AlertScanner matches process output lines against known error/warning patterns,
// deduplicates, batches, and delivers alerts through a callback.
type AlertScanner struct {
	patterns    []*AlertPattern
	disabledIDs map[string]bool
	patternMu   sync.RWMutex // protects patterns and disabledIDs
	onAlert     func(*AlertBatch)
	actState    func() ActivityState

	batchWindow  time.Duration
	dedupeWindow time.Duration

	mu            sync.Mutex
	pending       []*AlertMatch
	batchTimer    *time.Timer
	dedupe        map[string]time.Time // fingerprint -> last seen
	enabled       atomic.Bool
	stopped       atomic.Bool
	stopCh        chan struct{}
	flushRetries  int
	maxRetries    int
	retryInterval time.Duration

	// clockNow returns the current time. Defaults to time.Now.
	// Tests may substitute a stub to control dedup window checks.
	clockNow func() time.Time

	// afterFunc schedules f to run after d. Defaults to time.AfterFunc.
	// Tests may substitute a stub to control batch/retry timer firing.
	afterFunc func(d time.Duration, f func()) *time.Timer

	// Ring buffer for recent matches (pre-dedup, all matches retained).
	matchBuf     [matchBufSize]*AlertMatch
	matchBufHead int // next write position
	matchBufLen  int // number of entries (max matchBufSize)
	matchBufMu   sync.RWMutex

	// recentLines is a small per-scanner ring of the most recent non-empty
	// lines seen, used to attach a preceding context line to an unparsed
	// catch-all match (e.g. the Prisma invocation anchor that precedes the
	// auth-failure signal). Guarded by lineMu.
	recentLines    [recentLineBufSize]string
	recentLineHead int
	recentLineLen  int
	lineMu         sync.Mutex
}

// NewAlertScanner creates and starts a new AlertScanner with the given config.
func NewAlertScanner(cfg AlertScannerConfig) *AlertScanner {
	batchWindow := cfg.BatchWindow
	if batchWindow == 0 {
		batchWindow = 3 * time.Second
	}
	dedupeWindow := cfg.DedupeWindow
	if dedupeWindow == 0 {
		dedupeWindow = 60 * time.Second
	}

	disabledIDs := make(map[string]bool, len(cfg.DisabledIDs))
	for _, id := range cfg.DisabledIDs {
		disabledIDs[id] = true
	}

	retryInterval := cfg.RetryInterval
	if retryInterval == 0 {
		retryInterval = 2 * time.Second
	}

	s := &AlertScanner{
		patterns:      append(DefaultAlertPatterns(), cfg.Patterns...),
		disabledIDs:   disabledIDs,
		onAlert:       cfg.OnAlert,
		actState:      cfg.ActivityState,
		batchWindow:   batchWindow,
		dedupeWindow:  dedupeWindow,
		dedupe:        make(map[string]time.Time),
		stopCh:        make(chan struct{}),
		maxRetries:    5,
		retryInterval: retryInterval,
		clockNow:      time.Now,
		afterFunc:     time.AfterFunc,
	}
	s.enabled.Store(true)

	return s
}

// ProcessLine checks a single line of output against all enabled patterns.
func (s *AlertScanner) ProcessLine(line string, scriptID string) {
	if !s.enabled.Load() || s.stopped.Load() {
		return
	}
	if strings.TrimSpace(line) == "" {
		return
	}

	var matched *AlertMatch

	s.patternMu.RLock()
	for _, p := range s.patterns {
		if s.disabledIDs[p.ID] {
			continue
		}
		if p.Pattern.MatchString(line) {
			matched = &AlertMatch{
				Pattern:   p,
				Line:      strings.TrimSpace(line),
				Timestamp: s.clockNow(),
				ScriptID:  scriptID,
			}
			break // One pattern match per line is sufficient
		}
	}
	s.patternMu.RUnlock()

	if matched != nil {
		s.recordMatch(matched)
		s.addMatch(matched)
		s.pushRecentLine(strings.TrimSpace(line))
		return
	}

	// Catch-all: a line that no real pattern classified but that looks like
	// an error must still surface, marked unparsed, so nothing is silently
	// dropped. Keep the heuristic conservative; rely on dedup + batch caps to
	// bound flooding. Operators who want the safety net off can disable it
	// like any other pattern via DisablePattern(unparsedPatternID).
	s.patternMu.RLock()
	catchAllDisabled := s.disabledIDs[unparsedPatternID]
	s.patternMu.RUnlock()

	trimmed := strings.TrimSpace(line)
	if !catchAllDisabled && unparsedErrorRe.MatchString(trimmed) {
		um := &AlertMatch{
			Pattern:   unparsedPattern,
			Line:      s.withRecentContext(trimmed),
			Timestamp: s.clockNow(),
			ScriptID:  scriptID,
		}
		s.recordMatch(um)
		s.addMatch(um)
	}

	s.pushRecentLine(trimmed)
}

// pushRecentLine records a non-empty line in the per-scanner recent-line ring.
func (s *AlertScanner) pushRecentLine(line string) {
	if line == "" {
		return
	}
	s.lineMu.Lock()
	s.recentLines[s.recentLineHead] = line
	s.recentLineHead = (s.recentLineHead + 1) % recentLineBufSize
	if s.recentLineLen < recentLineBufSize {
		s.recentLineLen++
	}
	s.lineMu.Unlock()
}

// withRecentContext prefixes the signal line with the single most recent
// non-blank, non-duplicate preceding line as a compact "cause" hint. Kept to
// one line to stay token-efficient — no raw multi-line dumps.
func (s *AlertScanner) withRecentContext(signal string) string {
	s.lineMu.Lock()
	defer s.lineMu.Unlock()
	if s.recentLineLen == 0 {
		return signal
	}
	// Most recent line is one slot behind head.
	prev := s.recentLines[(s.recentLineHead-1+recentLineBufSize)%recentLineBufSize]
	if prev == "" || prev == signal {
		return signal
	}
	return prev + " | " + signal
}

// Inject adds a pre-classified match to the scanner without regex
// matching, sharing the same dedup / batch / activity-defer pipeline as
// regex-matched alerts. Used by non-process sources (browser-JS errors,
// HTTP errors) so every PTY-bound alert flows through one queue. The
// canonical-queue invariant (see .claude/skills/messaging-queue) requires
// callers to use this entry point rather than writing directly to the
// PTY.
//
// Callers that classify by canonical message (rather than regex pattern)
// must still set Pattern.ID + Pattern.Severity so dedup keying and
// severity ordering work. Source distinguishes presentation; RenderedText
// is the pre-formatted text for the non-process render path.
func (s *AlertScanner) Inject(m *AlertMatch) {
	if m == nil || !s.enabled.Load() || s.stopped.Load() {
		return
	}
	if m.Pattern == nil {
		return
	}
	if m.Timestamp.IsZero() {
		m.Timestamp = s.clockNow()
	}
	s.recordMatch(m)
	s.addMatch(m)
}

// addMatch adds a match to the pending batch, applying deduplication.
func (s *AlertScanner) addMatch(m *AlertMatch) {
	fp := fingerprint(m.Pattern.ID, m.Line)

	s.mu.Lock()
	defer s.mu.Unlock()

	// Deduplicate
	if lastSeen, ok := s.dedupe[fp]; ok {
		if s.clockNow().Sub(lastSeen) < s.dedupeWindow {
			return
		}
	}
	s.dedupe[fp] = s.clockNow()

	s.pending = append(s.pending, m)

	// Start batch timer if not already running
	if s.batchTimer == nil {
		s.batchTimer = s.afterFunc(s.batchWindow, func() {
			s.flush()
		})
	}
}

// flush delivers the current batch of alerts.
func (s *AlertScanner) flush() {
	if s.stopped.Load() {
		return
	}

	s.mu.Lock()

	// If AI is active, defer the flush (up to maxRetries)
	if s.actState != nil && s.actState() == ActivityActive && s.flushRetries < s.maxRetries {
		s.flushRetries++
		s.batchTimer = s.afterFunc(s.retryInterval, func() {
			s.flush()
		})
		s.mu.Unlock()
		return
	}

	if len(s.pending) == 0 {
		s.batchTimer = nil
		s.flushRetries = 0
		s.mu.Unlock()
		return
	}

	// Group by scriptID
	byScript := map[string][]*AlertMatch{}
	for _, m := range s.pending {
		byScript[m.ScriptID] = append(byScript[m.ScriptID], m)
	}

	s.pending = nil
	s.batchTimer = nil
	s.flushRetries = 0
	s.mu.Unlock()

	// Deliver batches
	if s.onAlert != nil {
		// Sort script IDs for deterministic ordering
		scriptIDs := make([]string, 0, len(byScript))
		for id := range byScript {
			scriptIDs = append(scriptIDs, id)
		}
		sort.Strings(scriptIDs)

		for _, sid := range scriptIDs {
			s.onAlert(&AlertBatch{
				Matches:  byScript[sid],
				ScriptID: sid,
			})
		}
	}

	// Prune old dedup entries periodically
	s.pruneDedup()
}

// pruneDedup removes expired dedup entries.
func (s *AlertScanner) pruneDedup() {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.clockNow()
	for fp, ts := range s.dedupe {
		if now.Sub(ts) > s.dedupeWindow {
			delete(s.dedupe, fp)
		}
	}
}

// AddPattern registers an additional alert pattern.
func (s *AlertScanner) AddPattern(p *AlertPattern) {
	s.patternMu.Lock()
	defer s.patternMu.Unlock()
	s.patterns = append(s.patterns, p)
}

// DisablePattern disables a pattern by ID.
func (s *AlertScanner) DisablePattern(id string) {
	s.patternMu.Lock()
	defer s.patternMu.Unlock()
	s.disabledIDs[id] = true
}

// SetEnabled enables or disables the scanner.
func (s *AlertScanner) SetEnabled(enabled bool) {
	s.enabled.Store(enabled)
}

// Stop stops the scanner and flushes any pending alerts.
func (s *AlertScanner) Stop() {
	if !s.stopped.CompareAndSwap(false, true) {
		return
	}
	close(s.stopCh)

	s.mu.Lock()
	if s.batchTimer != nil {
		s.batchTimer.Stop()
		s.batchTimer = nil
	}
	s.mu.Unlock()

	// Final flush
	s.deliverPending()
}

// deliverPending delivers any remaining pending alerts without deferral.
func (s *AlertScanner) deliverPending() {
	s.mu.Lock()
	if len(s.pending) == 0 {
		s.mu.Unlock()
		return
	}

	byScript := map[string][]*AlertMatch{}
	for _, m := range s.pending {
		byScript[m.ScriptID] = append(byScript[m.ScriptID], m)
	}
	s.pending = nil
	s.mu.Unlock()

	if s.onAlert != nil {
		scriptIDs := make([]string, 0, len(byScript))
		for id := range byScript {
			scriptIDs = append(scriptIDs, id)
		}
		sort.Strings(scriptIDs)

		for _, sid := range scriptIDs {
			s.onAlert(&AlertBatch{
				Matches:  byScript[sid],
				ScriptID: sid,
			})
		}
	}
}

// recordMatch stores a match in the ring buffer. Called from ProcessLine
// before addMatch, so it captures every match regardless of dedup.
func (s *AlertScanner) recordMatch(m *AlertMatch) {
	s.matchBufMu.Lock()
	s.matchBuf[s.matchBufHead] = m
	s.matchBufHead = (s.matchBufHead + 1) % matchBufSize
	if s.matchBufLen < matchBufSize {
		s.matchBufLen++
	}
	s.matchBufMu.Unlock()
}

// RecentMatches returns matches from the ring buffer with timestamp >= since.
// If since is the zero time, all buffered matches are returned.
// Results are ordered oldest-to-newest.
func (s *AlertScanner) RecentMatches(since time.Time) []*AlertMatch {
	s.matchBufMu.RLock()
	defer s.matchBufMu.RUnlock()

	if s.matchBufLen == 0 {
		return nil
	}

	// Start index is the oldest entry in the ring buffer.
	start := (s.matchBufHead - s.matchBufLen + matchBufSize) % matchBufSize
	isZero := since.IsZero()

	result := make([]*AlertMatch, 0, s.matchBufLen)
	for i := 0; i < s.matchBufLen; i++ {
		idx := (start + i) % matchBufSize
		m := s.matchBuf[idx]
		if isZero || !m.Timestamp.Before(since) {
			result = append(result, m)
		}
	}
	return result
}

// capitalize uppercases the first letter of a string.
func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// fingerprint creates a dedup key from pattern ID and the matched line.
func fingerprint(patternID, line string) string {
	h := sha256.Sum256([]byte(strings.TrimSpace(line)))
	return patternID + ":" + fmt.Sprintf("%x", h[:8])
}
