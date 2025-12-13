package proxy

// ChaosPresets contains built-in chaos configurations for common testing scenarios
var ChaosPresets = map[string]*ChaosConfig{
	// mobile-3g simulates a 3G mobile network with high latency and packet loss
	"mobile-3g": {
		Enabled: true,
		Rules: []*ChaosRule{
			{
				ID:           "mobile-3g-latency",
				Name:         "3G Network Latency",
				Type:         ChaosLatency,
				Enabled:      true,
				Probability:  1.0,
				MinLatencyMs: 200,
				MaxLatencyMs: 2000,
				JitterMs:     500,
			},
			{
				ID:          "mobile-3g-packet-loss",
				Name:        "3G Packet Loss",
				Type:        ChaosPacketLoss,
				Enabled:     true,
				Probability: 0.02, // 2% packet loss
			},
		},
		LoggingMode: LoggingModeTesting,
	},

	// mobile-4g simulates a 4G/LTE mobile network
	"mobile-4g": {
		Enabled: true,
		Rules: []*ChaosRule{
			{
				ID:           "mobile-4g-latency",
				Name:         "4G Network Latency",
				Type:         ChaosLatency,
				Enabled:      true,
				Probability:  1.0,
				MinLatencyMs: 50,
				MaxLatencyMs: 500,
				JitterMs:     100,
			},
			{
				ID:          "mobile-4g-packet-loss",
				Name:        "4G Packet Loss",
				Type:        ChaosPacketLoss,
				Enabled:     true,
				Probability: 0.005, // 0.5% packet loss
			},
		},
		LoggingMode: LoggingModeTesting,
	},

	// flaky-api simulates an unreliable API with random errors and timeouts
	"flaky-api": {
		Enabled: true,
		Rules: []*ChaosRule{
			{
				ID:          "flaky-http-errors",
				Name:        "Random HTTP Errors",
				Type:        ChaosHTTPError,
				Enabled:     true,
				Probability: 0.05, // 5% error rate
				ErrorCodes:  []int{500, 502, 503, 504},
			},
			{
				ID:           "flaky-latency",
				Name:         "Variable Latency",
				Type:         ChaosLatency,
				Enabled:      true,
				Probability:  0.3, // 30% of requests get extra latency
				MinLatencyMs: 100,
				MaxLatencyMs: 5000,
				JitterMs:     1000,
			},
			{
				ID:          "flaky-timeout",
				Name:        "Occasional Timeouts",
				Type:        ChaosTimeout,
				Enabled:     true,
				Probability: 0.02, // 2% timeout rate
			},
		},
		LoggingMode: LoggingModeTesting,
	},

	// race-condition simulates conditions that expose frontend race conditions
	"race-condition": {
		Enabled: true,
		Rules: []*ChaosRule{
			{
				ID:                 "reorder-responses",
				Name:               "Out-of-Order Responses",
				Type:               ChaosOutOfOrder,
				Enabled:            true,
				Probability:        0.5, // 50% of requests get reordered
				ReorderMinRequests: 2,
				ReorderMaxWaitMs:   500,
			},
			{
				ID:           "variable-latency",
				Name:         "High Variance Latency",
				Type:         ChaosLatency,
				Enabled:      true,
				Probability:  0.7, // 70% get variable delays
				MinLatencyMs: 0,
				MaxLatencyMs: 3000,
				JitterMs:     1000,
			},
		},
		LoggingMode: LoggingModeTesting,
	},

	// stale-tab simulates a browser tab that's been open for hours
	"stale-tab": {
		Enabled: true,
		Rules: []*ChaosRule{
			{
				ID:           "stale-delay",
				Name:         "Stale Tab Delay",
				Type:         ChaosStale,
				Enabled:      true,
				Probability:  1.0,
				StaleDelayMs: 10800000, // 3 hours in milliseconds
			},
		},
		LoggingMode: LoggingModeTesting,
	},

	// slow-connection simulates a very slow internet connection
	"slow-connection": {
		Enabled: true,
		Rules: []*ChaosRule{
			{
				ID:         "slow-drip",
				Name:       "Slow Data Transfer",
				Type:       ChaosSlowDrip,
				Enabled:    true,
				BytesPerMs: 5,  // 5 bytes per ms = 5KB/s
				ChunkSize:  10, // 10 byte chunks
			},
			{
				ID:           "connection-latency",
				Name:         "Connection Latency",
				Type:         ChaosLatency,
				Enabled:      true,
				Probability:  1.0,
				MinLatencyMs: 500,
				MaxLatencyMs: 1000,
			},
		},
		LoggingMode: LoggingModeTesting,
	},

	// connection-drops simulates unstable connections that drop mid-transfer
	"connection-drops": {
		Enabled: true,
		Rules: []*ChaosRule{
			{
				ID:               "drop-mid-response",
				Name:             "Drop Connection Mid-Response",
				Type:             ChaosDisconnect,
				Enabled:          true,
				Probability:      0.1, // 10% drop rate
				DropAfterPercent: 0.5, // Drop after 50% of response
			},
		},
		LoggingMode: LoggingModeTesting,
	},

	// data-corruption simulates data integrity issues
	"data-corruption": {
		Enabled: true,
		Rules: []*ChaosRule{
			{
				ID:              "truncate-response",
				Name:            "Truncate Responses",
				Type:            ChaosTruncate,
				Enabled:         true,
				Probability:     0.05, // 5% truncation rate
				TruncatePercent: 0.8,  // Keep 80% of response
			},
		},
		LoggingMode: LoggingModeTesting,
	},

	// rate-limited simulates hitting API rate limits
	"rate-limited": {
		Enabled: true,
		Rules: []*ChaosRule{
			{
				ID:           "rate-limit-429",
				Name:         "Rate Limit Errors",
				Type:         ChaosHTTPError,
				Enabled:      true,
				Probability:  0.2, // 20% get rate limited
				ErrorCodes:   []int{429},
				ErrorMessage: `{"error": "Too Many Requests", "retry_after": 60}`,
			},
		},
		LoggingMode: LoggingModeTesting,
	},

	// auth-failures simulates authentication/authorization failures
	"auth-failures": {
		Enabled: true,
		Rules: []*ChaosRule{
			{
				ID:          "auth-error",
				Name:        "Authentication Failures",
				Type:        ChaosHTTPError,
				Enabled:     true,
				Probability: 0.1, // 10% auth failure rate
				ErrorCodes:  []int{401, 403},
			},
		},
		LoggingMode: LoggingModeTesting,
	},

	// service-degradation simulates gradual service degradation
	"service-degradation": {
		Enabled:    true,
		GlobalOdds: 0.3, // 30% of requests affected
		Rules: []*ChaosRule{
			{
				ID:           "degraded-latency",
				Name:         "Degraded Response Times",
				Type:         ChaosLatency,
				Enabled:      true,
				Probability:  0.5,
				MinLatencyMs: 1000,
				MaxLatencyMs: 10000,
			},
			{
				ID:          "degraded-errors",
				Name:        "Intermittent Errors",
				Type:        ChaosHTTPError,
				Enabled:     true,
				Probability: 0.1,
				ErrorCodes:  []int{503},
			},
			{
				ID:              "degraded-truncation",
				Name:            "Partial Responses",
				Type:            ChaosTruncate,
				Enabled:         true,
				Probability:     0.05,
				TruncatePercent: 0.5,
			},
		},
		LoggingMode: LoggingModeTesting,
	},

	// pressure-test applies multiple chaos types for stress testing
	"pressure-test": {
		Enabled:    true,
		GlobalOdds: 0.5, // 50% of requests affected
		Rules: []*ChaosRule{
			{
				ID:           "pressure-latency",
				Name:         "Random Latency",
				Type:         ChaosLatency,
				Enabled:      true,
				Probability:  0.4,
				MinLatencyMs: 100,
				MaxLatencyMs: 2000,
				JitterMs:     500,
			},
			{
				ID:          "pressure-errors",
				Name:        "Random Errors",
				Type:        ChaosHTTPError,
				Enabled:     true,
				Probability: 0.1,
				ErrorCodes:  []int{500, 502, 503, 504, 429},
			},
			{
				ID:                 "pressure-reorder",
				Name:               "Response Reordering",
				Type:               ChaosOutOfOrder,
				Enabled:            true,
				Probability:        0.3,
				ReorderMinRequests: 3,
				ReorderMaxWaitMs:   300,
			},
			{
				ID:          "pressure-drops",
				Name:        "Connection Drops",
				Type:        ChaosDisconnect,
				Enabled:     true,
				Probability: 0.02,
			},
		},
		LoggingMode: LoggingModeCoordinated,
	},
}

// GetPreset returns a copy of the named preset configuration
func GetPreset(name string) *ChaosConfig {
	preset, ok := ChaosPresets[name]
	if !ok {
		return nil
	}

	// Return a deep copy to prevent modification of the original
	return copyConfig(preset)
}

// ListPresets returns the names of all available presets
func ListPresets() []string {
	names := make([]string, 0, len(ChaosPresets))
	for name := range ChaosPresets {
		names = append(names, name)
	}
	return names
}

// copyConfig creates a deep copy of a ChaosConfig
func copyConfig(src *ChaosConfig) *ChaosConfig {
	if src == nil {
		return nil
	}

	dst := &ChaosConfig{
		Enabled:     src.Enabled,
		GlobalOdds:  src.GlobalOdds,
		Seed:        src.Seed,
		LoggingMode: src.LoggingMode,
	}

	if len(src.Rules) > 0 {
		dst.Rules = make([]*ChaosRule, len(src.Rules))
		for i, rule := range src.Rules {
			dst.Rules[i] = copyRule(rule)
		}
	}

	return dst
}

// copyRule creates a deep copy of a ChaosRule
func copyRule(src *ChaosRule) *ChaosRule {
	if src == nil {
		return nil
	}

	dst := &ChaosRule{
		ID:                 src.ID,
		Name:               src.Name,
		Type:               src.Type,
		Enabled:            src.Enabled,
		URLPattern:         src.URLPattern,
		Probability:        src.Probability,
		MinLatencyMs:       src.MinLatencyMs,
		MaxLatencyMs:       src.MaxLatencyMs,
		JitterMs:           src.JitterMs,
		BytesPerMs:         src.BytesPerMs,
		ChunkSize:          src.ChunkSize,
		DropAfterPercent:   src.DropAfterPercent,
		DropAfterBytes:     src.DropAfterBytes,
		ErrorMessage:       src.ErrorMessage,
		TruncatePercent:    src.TruncatePercent,
		ReorderMinRequests: src.ReorderMinRequests,
		ReorderMaxWaitMs:   src.ReorderMaxWaitMs,
		StaleDelayMs:       src.StaleDelayMs,
	}

	if len(src.Methods) > 0 {
		dst.Methods = make([]string, len(src.Methods))
		copy(dst.Methods, src.Methods)
	}

	if len(src.ErrorCodes) > 0 {
		dst.ErrorCodes = make([]int, len(src.ErrorCodes))
		copy(dst.ErrorCodes, src.ErrorCodes)
	}

	return dst
}
