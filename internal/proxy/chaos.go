package proxy

import (
	"math/rand"
	"net/http"
	"regexp"
	"sync"
	"sync/atomic"
	"time"
)

// ChaosType defines the type of chaos to inject
type ChaosType string

const (
	// Network chaos
	ChaosLatency    ChaosType = "latency"     // Add delays to responses
	ChaosBandwidth  ChaosType = "bandwidth"   // Limit data transfer rate
	ChaosPacketLoss ChaosType = "packet_loss" // Drop random requests
	ChaosDisconnect ChaosType = "disconnect"  // Drop connection mid-response
	ChaosSlowClose  ChaosType = "slow_close"  // Delay TCP close

	// Response timing
	ChaosSlowDrip   ChaosType = "slow_drip"    // Trickle bytes slowly
	ChaosTimeout    ChaosType = "timeout"      // Never respond (simulate timeout)
	ChaosStale      ChaosType = "stale"        // Very long delays (hours)
	ChaosOutOfOrder ChaosType = "out_of_order" // Reorder responses

	// HTTP errors
	ChaosHTTPError ChaosType = "http_error" // Inject HTTP error codes
	ChaosRateLimit ChaosType = "rate_limit" // Simulate rate limiting (429)

	// Data corruption
	ChaosBitFlip     ChaosType = "bit_flip"      // Random byte changes
	ChaosTruncate    ChaosType = "truncate"      // Cut off response body
	ChaosCorruptJSON ChaosType = "corrupt_json"  // Malform JSON responses

	// Protocol edge cases
	ChaosChunkedAbort ChaosType = "chunked_abort" // No terminal chunk
	ChaosPartialBody  ChaosType = "partial_body"  // Incomplete body
	ChaosHeaderBomb   ChaosType = "header_bomb"   // Many headers
)

// LoggingMode defines how chaos events are logged
type LoggingMode int32

const (
	LoggingModeSilent      LoggingMode = 0 // No chaos-specific logging
	LoggingModeTesting     LoggingMode = 1 // Log session start/stop + stats
	LoggingModeCoordinated LoggingMode = 2 // Minimal logging
)

// ChaosRule defines a single chaos behavior
type ChaosRule struct {
	ID      string    `json:"id"`
	Name    string    `json:"name"`
	Type    ChaosType `json:"type"`
	Enabled bool      `json:"enabled"`

	// Matching criteria
	URLPattern  string   `json:"url_pattern,omitempty"`  // Regex pattern for URL
	Methods     []string `json:"methods,omitempty"`      // HTTP methods (empty = all)
	Probability float64  `json:"probability,omitempty"`  // 0.0-1.0, default 1.0

	// Latency config
	MinLatencyMs int `json:"min_latency_ms,omitempty"`
	MaxLatencyMs int `json:"max_latency_ms,omitempty"`
	JitterMs     int `json:"jitter_ms,omitempty"`

	// Slow-drip config
	BytesPerMs int `json:"bytes_per_ms,omitempty"` // Bytes to write per millisecond
	ChunkSize  int `json:"chunk_size,omitempty"`   // Size of each write chunk

	// Connection drop config
	DropAfterPercent float64 `json:"drop_after_percent,omitempty"` // Drop after % of body
	DropAfterBytes   int64   `json:"drop_after_bytes,omitempty"`   // Drop after N bytes

	// Error injection config
	ErrorCodes   []int  `json:"error_codes,omitempty"` // HTTP status codes
	ErrorMessage string `json:"error_message,omitempty"`

	// Truncation config
	TruncatePercent float64 `json:"truncate_percent,omitempty"` // Keep this % of response

	// Out-of-order config
	ReorderMinRequests int `json:"reorder_min_requests,omitempty"` // Min concurrent to reorder
	ReorderMaxWaitMs   int `json:"reorder_max_wait_ms,omitempty"`  // Max wait for batch

	// Stale config
	StaleDelayMs int64 `json:"stale_delay_ms,omitempty"` // Delay in milliseconds

	// Compiled regex (internal)
	urlRegex *regexp.Regexp
}

// ChaosConfig defines chaos rules for a proxy
type ChaosConfig struct {
	Enabled     bool         `json:"enabled"`
	Rules       []*ChaosRule `json:"rules"`
	GlobalOdds  float64      `json:"global_odds,omitempty"` // 0.0-1.0, applies to all
	Seed        int64        `json:"seed,omitempty"`        // For reproducible chaos
	LoggingMode LoggingMode  `json:"logging_mode,omitempty"`
}

// ChaosStats tracks chaos injection statistics
type ChaosStats struct {
	TotalRequests   int64            `json:"total_requests"`
	AffectedCount   int64            `json:"affected_count"`
	LatencyInjected int64            `json:"latency_injected_ms"`
	ErrorsInjected  int64            `json:"errors_injected"`
	DropsInjected   int64            `json:"drops_injected"`
	TruncatedCount  int64            `json:"truncated_count"`
	ReorderedCount  int64            `json:"reordered_count"`
	RuleStats       map[string]int64 `json:"rule_stats"` // Rule ID -> times applied
}

// ChaosEngine manages chaos rules and injection
type ChaosEngine struct {
	enabled     atomic.Bool
	loggingMode atomic.Int32

	mu     sync.RWMutex
	config *ChaosConfig
	rules  []*chaosRuleState
	rng    *rand.Rand

	// Reorder queue for out-of-order responses
	reorderQueue *ReorderQueue

	// Statistics (lock-free)
	stats chaosStatsAtomic

	// Optional logger for chaos testing mode
	logger *TrafficLogger
}

// chaosRuleState holds a rule with atomic enabled state
type chaosRuleState struct {
	rule    *ChaosRule
	enabled atomic.Bool
	applied atomic.Int64
}

// chaosStatsAtomic holds atomic counters for stats
type chaosStatsAtomic struct {
	totalRequests   atomic.Int64
	affectedCount   atomic.Int64
	latencyInjected atomic.Int64
	errorsInjected  atomic.Int64
	dropsInjected   atomic.Int64
	truncatedCount  atomic.Int64
	reorderedCount  atomic.Int64
}

// NewChaosEngine creates a new chaos engine
func NewChaosEngine(logger *TrafficLogger) *ChaosEngine {
	ce := &ChaosEngine{
		rng:    rand.New(rand.NewSource(time.Now().UnixNano())),
		logger: logger,
	}
	ce.reorderQueue = NewReorderQueue(ce)
	return ce
}

// Enable enables chaos injection
func (ce *ChaosEngine) Enable() {
	ce.enabled.Store(true)
}

// Disable disables chaos injection
func (ce *ChaosEngine) Disable() {
	ce.enabled.Store(false)
}

// IsEnabled returns whether chaos is enabled
func (ce *ChaosEngine) IsEnabled() bool {
	return ce.enabled.Load()
}

// SetLoggingMode sets the logging mode
func (ce *ChaosEngine) SetLoggingMode(mode LoggingMode) {
	ce.loggingMode.Store(int32(mode))
}

// GetLoggingMode returns the current logging mode
func (ce *ChaosEngine) GetLoggingMode() LoggingMode {
	return LoggingMode(ce.loggingMode.Load())
}

// SetConfig sets the chaos configuration
func (ce *ChaosEngine) SetConfig(config *ChaosConfig) error {
	ce.mu.Lock()
	defer ce.mu.Unlock()

	// Compile URL patterns
	rules := make([]*chaosRuleState, 0, len(config.Rules))
	for _, r := range config.Rules {
		if r.URLPattern != "" {
			regex, err := regexp.Compile(r.URLPattern)
			if err != nil {
				return err
			}
			r.urlRegex = regex
		}

		// Set defaults
		if r.Probability == 0 {
			r.Probability = 1.0
		}

		state := &chaosRuleState{rule: r}
		state.enabled.Store(r.Enabled)
		rules = append(rules, state)
	}

	ce.config = config
	ce.rules = rules
	ce.enabled.Store(config.Enabled)
	ce.loggingMode.Store(int32(config.LoggingMode))

	// Seed RNG if specified
	if config.Seed != 0 {
		ce.rng = rand.New(rand.NewSource(config.Seed))
	}

	return nil
}

// GetConfig returns the current configuration
func (ce *ChaosEngine) GetConfig() *ChaosConfig {
	ce.mu.RLock()
	defer ce.mu.RUnlock()
	return ce.config
}

// Clear clears all chaos rules
func (ce *ChaosEngine) Clear() {
	ce.mu.Lock()
	defer ce.mu.Unlock()

	ce.config = nil
	ce.rules = nil
	ce.enabled.Store(false)

	// Reset stats
	ce.stats = chaosStatsAtomic{}
}

// AddRule adds a single rule
func (ce *ChaosEngine) AddRule(rule *ChaosRule) error {
	ce.mu.Lock()
	defer ce.mu.Unlock()

	if rule.URLPattern != "" {
		regex, err := regexp.Compile(rule.URLPattern)
		if err != nil {
			return err
		}
		rule.urlRegex = regex
	}

	if rule.Probability == 0 {
		rule.Probability = 1.0
	}

	state := &chaosRuleState{rule: rule}
	state.enabled.Store(rule.Enabled)
	ce.rules = append(ce.rules, state)

	return nil
}

// RemoveRule removes a rule by ID
func (ce *ChaosEngine) RemoveRule(ruleID string) bool {
	ce.mu.Lock()
	defer ce.mu.Unlock()

	for i, r := range ce.rules {
		if r.rule.ID == ruleID {
			ce.rules = append(ce.rules[:i], ce.rules[i+1:]...)
			return true
		}
	}
	return false
}

// EnableRule enables a specific rule
func (ce *ChaosEngine) EnableRule(ruleID string) bool {
	ce.mu.RLock()
	defer ce.mu.RUnlock()

	for _, r := range ce.rules {
		if r.rule.ID == ruleID {
			r.enabled.Store(true)
			return true
		}
	}
	return false
}

// DisableRule disables a specific rule
func (ce *ChaosEngine) DisableRule(ruleID string) bool {
	ce.mu.RLock()
	defer ce.mu.RUnlock()

	for _, r := range ce.rules {
		if r.rule.ID == ruleID {
			r.enabled.Store(false)
			return true
		}
	}
	return false
}

// GetStats returns current statistics
func (ce *ChaosEngine) GetStats() ChaosStats {
	ce.mu.RLock()
	ruleStats := make(map[string]int64)
	for _, r := range ce.rules {
		ruleStats[r.rule.ID] = r.applied.Load()
	}
	ce.mu.RUnlock()

	return ChaosStats{
		TotalRequests:   ce.stats.totalRequests.Load(),
		AffectedCount:   ce.stats.affectedCount.Load(),
		LatencyInjected: ce.stats.latencyInjected.Load(),
		ErrorsInjected:  ce.stats.errorsInjected.Load(),
		DropsInjected:   ce.stats.dropsInjected.Load(),
		TruncatedCount:  ce.stats.truncatedCount.Load(),
		ReorderedCount:  ce.stats.reorderedCount.Load(),
		RuleStats:       ruleStats,
	}
}

// ResetStats resets all statistics
func (ce *ChaosEngine) ResetStats() {
	ce.stats = chaosStatsAtomic{}

	ce.mu.RLock()
	for _, r := range ce.rules {
		r.applied.Store(0)
	}
	ce.mu.RUnlock()
}

// MatchingRules returns all rules that match the request
func (ce *ChaosEngine) MatchingRules(req *http.Request) []*ChaosRule {
	if !ce.enabled.Load() {
		return nil
	}

	ce.stats.totalRequests.Add(1)

	ce.mu.RLock()
	defer ce.mu.RUnlock()

	// Check global odds
	if ce.config != nil && ce.config.GlobalOdds > 0 && ce.config.GlobalOdds < 1.0 {
		if ce.rng.Float64() > ce.config.GlobalOdds {
			return nil
		}
	}

	var matches []*ChaosRule
	for _, state := range ce.rules {
		if !state.enabled.Load() {
			continue
		}

		rule := state.rule
		if ce.ruleMatches(rule, req) {
			// Check probability
			if rule.Probability < 1.0 && ce.rng.Float64() > rule.Probability {
				continue
			}

			matches = append(matches, rule)
			state.applied.Add(1)
		}
	}

	if len(matches) > 0 {
		ce.stats.affectedCount.Add(1)
	}

	return matches
}

// ruleMatches checks if a rule matches the request
func (ce *ChaosEngine) ruleMatches(rule *ChaosRule, req *http.Request) bool {
	// Check method
	if len(rule.Methods) > 0 {
		methodMatch := false
		for _, m := range rule.Methods {
			if m == req.Method {
				methodMatch = true
				break
			}
		}
		if !methodMatch {
			return false
		}
	}

	// Check URL pattern
	if rule.urlRegex != nil {
		if !rule.urlRegex.MatchString(req.URL.String()) {
			return false
		}
	}

	return true
}

// GetLatencyDelay calculates latency delay for matching rules
func (ce *ChaosEngine) GetLatencyDelay(rules []*ChaosRule) time.Duration {
	var totalDelay time.Duration

	for _, rule := range rules {
		if rule.Type != ChaosLatency {
			continue
		}

		minMs := rule.MinLatencyMs
		maxMs := rule.MaxLatencyMs
		if maxMs <= minMs {
			maxMs = minMs + 1
		}

		delay := minMs + ce.rng.Intn(maxMs-minMs)

		// Add jitter
		if rule.JitterMs > 0 {
			jitter := ce.rng.Intn(rule.JitterMs*2) - rule.JitterMs
			delay += jitter
			if delay < 0 {
				delay = 0
			}
		}

		totalDelay += time.Duration(delay) * time.Millisecond
	}

	if totalDelay > 0 {
		ce.stats.latencyInjected.Add(int64(totalDelay / time.Millisecond))
	}

	return totalDelay
}

// GetHTTPError returns an HTTP error code to inject, or 0 if none
func (ce *ChaosEngine) GetHTTPError(rules []*ChaosRule) (int, string) {
	for _, rule := range rules {
		if rule.Type != ChaosHTTPError {
			continue
		}

		if len(rule.ErrorCodes) == 0 {
			continue
		}

		code := rule.ErrorCodes[ce.rng.Intn(len(rule.ErrorCodes))]
		ce.stats.errorsInjected.Add(1)
		return code, rule.ErrorMessage
	}
	return 0, ""
}

// ShouldDrop returns true if the connection should be dropped
func (ce *ChaosEngine) ShouldDrop(rules []*ChaosRule) bool {
	for _, rule := range rules {
		if rule.Type == ChaosPacketLoss || rule.Type == ChaosDisconnect {
			ce.stats.dropsInjected.Add(1)
			return true
		}
	}
	return false
}

// GetDropConfig returns connection drop configuration
func (ce *ChaosEngine) GetDropConfig(rules []*ChaosRule) (afterPercent float64, afterBytes int64) {
	for _, rule := range rules {
		if rule.Type != ChaosDisconnect {
			continue
		}

		if rule.DropAfterPercent > 0 {
			return rule.DropAfterPercent, 0
		}
		if rule.DropAfterBytes > 0 {
			return 0, rule.DropAfterBytes
		}
		// Default: drop after 50%
		return 0.5, 0
	}
	return 0, 0
}

// GetSlowDripConfig returns slow-drip configuration
func (ce *ChaosEngine) GetSlowDripConfig(rules []*ChaosRule) (bytesPerMs, chunkSize int) {
	for _, rule := range rules {
		if rule.Type != ChaosSlowDrip {
			continue
		}

		bpm := rule.BytesPerMs
		if bpm <= 0 {
			bpm = 10 // Default: 10 bytes per ms (10KB/s)
		}

		cs := rule.ChunkSize
		if cs <= 0 {
			cs = 1 // Default: 1 byte chunks
		}

		return bpm, cs
	}
	return 0, 0
}

// GetTruncateConfig returns truncation configuration
func (ce *ChaosEngine) GetTruncateConfig(rules []*ChaosRule) float64 {
	for _, rule := range rules {
		if rule.Type != ChaosTruncate {
			continue
		}

		percent := rule.TruncatePercent
		if percent <= 0 || percent > 1.0 {
			percent = 0.5 // Default: keep 50%
		}

		ce.stats.truncatedCount.Add(1)
		return percent
	}
	return 0
}

// ShouldReorder returns true if responses should be reordered
func (ce *ChaosEngine) ShouldReorder(rules []*ChaosRule) bool {
	for _, rule := range rules {
		if rule.Type == ChaosOutOfOrder {
			return true
		}
	}
	return false
}

// GetReorderConfig returns out-of-order configuration
func (ce *ChaosEngine) GetReorderConfig(rules []*ChaosRule) (minRequests int, maxWait time.Duration) {
	for _, rule := range rules {
		if rule.Type != ChaosOutOfOrder {
			continue
		}

		min := rule.ReorderMinRequests
		if min <= 0 {
			min = 2 // Default: wait for 2 requests
		}

		wait := time.Duration(rule.ReorderMaxWaitMs) * time.Millisecond
		if wait <= 0 {
			wait = 500 * time.Millisecond // Default: 500ms max wait
		}

		return min, wait
	}
	return 2, 500 * time.Millisecond
}

// GetStaleDelay returns stale response delay
func (ce *ChaosEngine) GetStaleDelay(rules []*ChaosRule) time.Duration {
	for _, rule := range rules {
		if rule.Type != ChaosStale {
			continue
		}

		delay := time.Duration(rule.StaleDelayMs) * time.Millisecond
		if delay <= 0 {
			delay = 3 * time.Hour // Default: 3 hours
		}

		return delay
	}
	return 0
}

// HasRuleType checks if any matching rule has the specified type
func (ce *ChaosEngine) HasRuleType(rules []*ChaosRule, ruleType ChaosType) bool {
	for _, rule := range rules {
		if rule.Type == ruleType {
			return true
		}
	}
	return false
}

// IncrementReordered increments the reordered counter
func (ce *ChaosEngine) IncrementReordered() {
	ce.stats.reorderedCount.Add(1)
}
