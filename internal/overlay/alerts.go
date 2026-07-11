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
	// AlertSourceUser is content originating from an explicit user action
	// (browser panel message, sketch, design-mode interaction). Such matches
	// set Protected so the overload throttle and dedup never drop them.
	AlertSourceUser AlertSource = "user"
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
	// Protected marks content from an explicit user action (panel message,
	// sketch, design interaction). Protected matches MUST NEVER be dropped:
	// they bypass dedup (a repeated user action is intentional) and are never
	// evicted by the overload throttle. They still honor activity-deferral so
	// they are not injected mid-response. See the messaging-queue skill.
	Protected bool
}

// AlertBatch is a collection of alert matches to be delivered together.
type AlertBatch struct {
	Matches  []*AlertMatch
	ScriptID string
	// Suppressed is the number of alerts dropped by the overload throttle
	// since the last flush (queue exceeded MaxPending while the agent was
	// busy). When > 0, Format appends a one-line summary so the agent knows
	// the stream was throttled rather than silently lossy.
	Suppressed int
}

// ProtectedOnly returns a batch holding only the protected (explicit user
// action) matches, or nil when there are none. Used by delivery callbacks
// that gate auto-generated alerts (forwarding pause) but must never drop
// user content. Suppressed is not carried over — the throttle note belongs
// to the auto-alert stream the caller is gating.
func (b *AlertBatch) ProtectedOnly() *AlertBatch {
	var matches []*AlertMatch
	for _, m := range b.Matches {
		if m.Protected {
			matches = append(matches, m)
		}
	}
	if len(matches) == 0 {
		return nil
	}
	return &AlertBatch{Matches: matches, ScriptID: b.ScriptID}
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
		b.appendSuppressedNote(&sb)
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

	b.appendSuppressedNote(&sb)
	return sb.String()
}

// appendSuppressedNote appends the overload-throttle summary line when the
// batch carries dropped alerts. Kept to a single line so a throttled burst
// stays token-cheap while still telling the agent the stream was capped.
func (b *AlertBatch) appendSuppressedNote(sb *strings.Builder) {
	if b.Suppressed <= 0 {
		return
	}
	fmt.Fprintf(sb, "[agnt] %d more alert(s) suppressed (queue full while agent busy)\n", b.Suppressed)
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

	// MaxPending caps the depth of the pending batch — the overload throttle.
	// While the agent is busy, flush is deferred and distinct (non-duplicate)
	// alerts accumulate; without a cap a burst would flood the agent's input
	// the moment it goes idle. When pending exceeds MaxPending the scanner
	// evicts the oldest lowest-severity entry (errors are preserved longest)
	// and counts it as suppressed. Zero means use the default (50).
	MaxPending int
}

// protectedBatchWindow is the coalesce window used when the pending batch is
// started by a protected (explicit user action) entry. Kept short so an
// interactive panel message / sketch is delivered promptly when the agent is
// idle, rather than waiting the full error-oriented batch window. Protected
// entries still honor activity-deferral, so they never land mid-response.
const protectedBatchWindow = 150 * time.Millisecond

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
	maxPending    int // overload-throttle high-water mark for pending
	suppressed    int // alerts dropped by the throttle since last flush

	// deliverCh serializes alert delivery. flush() enqueues batches here and a
	// single deliveryLoop goroutine calls onAlert one batch at a time. onAlert
	// injects Enter keystrokes into the PTY and can block for seconds
	// (sendEntersUntilActivity); without this single-consumer serialization two
	// overlapping flush() calls (a rescheduled retry timer racing a fresh batch
	// timer) could call onAlert concurrently and interleave PTY injection.
	deliverCh   chan *AlertBatch
	deliverDone chan struct{}
	// deliveryStarted guards lazy creation of the deliveryLoop goroutine so a
	// scanner that never emits an alert (the common case for short-lived daemon
	// instances in tests) never spins up a goroutine to leak. Guarded by mu.
	deliveryStarted bool

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

	maxPending := cfg.MaxPending
	if maxPending == 0 {
		maxPending = 50
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
		maxPending:    maxPending,
		clockNow:      time.Now,
		afterFunc:     time.AfterFunc,
		deliverCh:     make(chan *AlertBatch, 128),
		deliverDone:   make(chan struct{}),
	}
	s.enabled.Store(true)

	// deliveryLoop starts lazily on the first enqueue — see ensureDeliveryLoop.

	return s
}

// ensureDeliveryLoop starts the single delivery goroutine on demand, exactly
// once, and never after Stop. Returns whether the loop is running so callers
// know a send to deliverCh will be consumed.
func (s *AlertScanner) ensureDeliveryLoop() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.deliveryStarted {
		return true
	}
	if s.stopped.Load() {
		return false
	}
	s.deliveryStarted = true
	go s.deliveryLoop()
	return true
}

// deliveryLoop is the single consumer of deliverCh. It calls onAlert one batch
// at a time so PTY injection is never concurrent. On Stop it drains any queued
// batches and exits, signaling deliverDone.
func (s *AlertScanner) deliveryLoop() {
	deliver := func(b *AlertBatch) {
		if s.onAlert != nil {
			s.onAlert(b)
		}
	}
	for {
		select {
		case b := <-s.deliverCh:
			deliver(b)
		case <-s.stopCh:
			for {
				select {
				case b := <-s.deliverCh:
					deliver(b)
				default:
					close(s.deliverDone)
					return
				}
			}
		}
	}
}

// enqueue hands a batch to the delivery goroutine, starting it on first use.
// It blocks (providing backpressure) if the buffer is full, but never after Stop.
func (s *AlertScanner) enqueue(b *AlertBatch) {
	if !s.ensureDeliveryLoop() {
		return // stopped before any delivery; nothing will consume the batch
	}
	select {
	case s.deliverCh <- b:
	case <-s.stopCh:
	}
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

	trimmed := strings.TrimSpace(line)

	// Structured parsers ([G3]): fold a recognized multi-line/noisy error block
	// (Prisma client error, raw DB-auth failure) into a single compact
	// {kind, message, file:line} line BEFORE the catch-all, so the agent gets
	// structure instead of a dump. Runs only on lines the pattern bank did not
	// classify. The cause line reaches back into the recent-line ring for the
	// preceding banner/call-site.
	if se, ok := runStructuredParsers(trimmed, s.recentSnapshot()); ok {
		sp := &AlertPattern{
			ID:          "structured-" + se.Kind,
			Severity:    AlertSeverity(se.Severity),
			Category:    se.Category,
			Description: "structured " + se.Kind + " error",
		}
		m := &AlertMatch{
			Pattern:   sp,
			Line:      se.Compact(),
			Timestamp: s.clockNow(),
			ScriptID:  scriptID,
		}
		s.recordMatch(m)
		s.addMatch(m)
		s.pushRecentLine(trimmed)
		return
	}

	// Structural prefix lines (Prisma block header/invocation banner) are folded
	// by the structured parser at the cause line — don't surface them as
	// standalone unparsed noise. They remain in the recent ring for that fold.
	if isStructuralPrefix(trimmed) {
		s.pushRecentLine(trimmed)
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

// recentSnapshot returns the buffered recent non-empty lines, oldest→newest,
// for structured parsers that need to reach back for a preceding banner or
// call-site line.
func (s *AlertScanner) recentSnapshot() []string {
	s.lineMu.Lock()
	defer s.lineMu.Unlock()
	if s.recentLineLen == 0 {
		return nil
	}
	out := make([]string, 0, s.recentLineLen)
	start := (s.recentLineHead - s.recentLineLen + recentLineBufSize) % recentLineBufSize
	for i := 0; i < s.recentLineLen; i++ {
		out = append(out, s.recentLines[(start+i)%recentLineBufSize])
	}
	return out
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

	// Deduplicate — but never for protected (explicit user action) content: a
	// user repeating an action means it intentionally, so each one is delivered.
	if !m.Protected {
		if lastSeen, ok := s.dedupe[fp]; ok {
			if s.clockNow().Sub(lastSeen) < s.dedupeWindow {
				return
			}
		}
		s.dedupe[fp] = s.clockNow()
	}

	s.pending = append(s.pending, m)

	// Overload throttle: keep the pending batch bounded. When the agent is
	// busy, flush is deferred and distinct alerts pile up here; without this
	// cap a burst would dump everything into the agent's input at once. Evict
	// the oldest lowest-severity droppable entry so genuine errors survive
	// longest, and count the drop so flush can append a "N suppressed" summary.
	// Protected entries are never evicted — if the queue is entirely protected
	// it is allowed to exceed the cap rather than drop user content.
	if s.maxPending > 0 && len(s.pending) > s.maxPending {
		if s.evictOneLocked() {
			s.suppressed++
		}
	}

	// Start batch timer if not already running. Protected user actions use a
	// short coalesce window so interactive messages are not delayed by the
	// full batch window when the agent is idle; they still defer while active.
	if s.batchTimer == nil {
		window := s.batchWindow
		if m.Protected && protectedBatchWindow < window {
			window = protectedBatchWindow
		}
		s.batchTimer = s.afterFunc(window, func() {
			s.flush()
		})
	}
}

// evictOneLocked removes a single droppable entry from pending to honor the
// overload cap. It drops the oldest entry of the lowest severity present (info
// before warning before error), so error-level alerts are retained the longest
// under sustained pressure. Protected (explicit user action) entries are never
// dropped. Returns true if an entry was evicted. Caller must hold s.mu.
func (s *AlertScanner) evictOneLocked() bool {
	for _, sev := range []AlertSeverity{AlertSeverityInfo, AlertSeverityWarning, AlertSeverityError} {
		for i, m := range s.pending {
			if m.Protected {
				continue
			}
			if m.Pattern.Severity == sev {
				s.pending = append(s.pending[:i], s.pending[i+1:]...)
				return true
			}
		}
	}
	// Everything pending is protected (or has no recognized severity): drop
	// nothing — user content is never sacrificed to the cap.
	return false
}

// QueueDepth is a point-in-time snapshot of the alert queue, surfaced to the
// overlay status bar and overview panel so the developer can see throttling.
type QueueDepth struct {
	Pending    int  // entries waiting in the current batch
	Suppressed int  // alerts dropped by the throttle since the last flush
	Deferred   bool // flush is currently held off because the agent is busy
}

// DepthSnapshot returns the current queue depth for display. Safe for
// concurrent use.
func (s *AlertScanner) DepthSnapshot() QueueDepth {
	s.mu.Lock()
	defer s.mu.Unlock()
	return QueueDepth{
		Pending:    len(s.pending),
		Suppressed: s.suppressed,
		Deferred:   s.flushRetries > 0,
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

	s.batchTimer = nil
	s.flushRetries = 0
	byScript, suppressed := s.drainPendingLocked()
	s.mu.Unlock()

	// Deliver batches via the single delivery goroutine so PTY injection is
	// serialized even if this flush overlaps another. Only the post-drain
	// deliverPending path may call onAlert directly.
	if s.onAlert != nil {
		deliverByScript(byScript, suppressed, s.enqueue)
	}

	// Prune old dedup entries periodically
	s.pruneDedup()
}

// drainPendingLocked takes the pending batch and the suppressed count,
// resetting both, and returns the matches grouped by script for delivery.
// Caller must hold s.mu.
func (s *AlertScanner) drainPendingLocked() (map[string][]*AlertMatch, int) {
	byScript := map[string][]*AlertMatch{}
	for _, m := range s.pending {
		byScript[m.ScriptID] = append(byScript[m.ScriptID], m)
	}
	s.pending = nil
	suppressed := s.suppressed
	s.suppressed = 0
	return byScript, suppressed
}

// deliverByScript dispatches grouped matches in deterministic script order,
// attaching the suppressed count to the first batch so the overload-throttle
// summary is delivered exactly once.
func deliverByScript(byScript map[string][]*AlertMatch, suppressed int, onAlert func(*AlertBatch)) {
	scriptIDs := make([]string, 0, len(byScript))
	for id := range byScript {
		scriptIDs = append(scriptIDs, id)
	}
	sort.Strings(scriptIDs)

	for i, sid := range scriptIDs {
		batch := &AlertBatch{Matches: byScript[sid], ScriptID: sid}
		if i == 0 {
			batch.Suppressed = suppressed
		}
		onAlert(batch)
	}
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
	started := s.deliveryStarted
	s.mu.Unlock()

	// Wait for the delivery goroutine to drain queued batches and exit, so the
	// final flush below cannot call onAlert concurrently with it. Skip the wait
	// when the loop was never started (no alert ever enqueued) — deliverDone
	// would never be closed.
	//
	// The wait is bounded: onAlert injects into the PTY and can block for
	// several seconds per batch (it waits on child activity that never comes
	// when the child has already exited). Blocking teardown on that would stall
	// session/daemon shutdown. If the goroutine doesn't drain within the grace
	// window, return without the final flush — skipping deliverPending() is
	// also required for correctness, since the goroutine is still live and a
	// concurrent deliverPending() would invoke onAlert twice over.
	if started {
		select {
		case <-s.deliverDone:
			// Drained and exited — safe to flush synchronously.
			s.deliverPending()
		case <-time.After(alertStopGrace):
			// A batch is stuck in a blocking onAlert; don't stall teardown.
		}
		return
	}

	// Never started: no delivery goroutine, so the flush is race-free.
	s.deliverPending()
}

// alertStopGrace bounds how long Stop() waits for the delivery goroutine to
// drain before abandoning the final flush and returning, so a blocked PTY
// injection cannot stall session/daemon teardown.
const alertStopGrace = 2 * time.Second

// deliverPending delivers any remaining pending alerts without deferral.
func (s *AlertScanner) deliverPending() {
	s.mu.Lock()
	if len(s.pending) == 0 {
		s.mu.Unlock()
		return
	}

	byScript, suppressed := s.drainPendingLocked()
	s.mu.Unlock()

	if s.onAlert != nil {
		deliverByScript(byScript, suppressed, s.onAlert)
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
