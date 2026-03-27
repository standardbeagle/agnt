// Package config provides port detection for auto-started scripts.
package config

import (
	"context"
	"regexp"
	"strconv"
	"time"
)

// PortDetector detects listening ports from processes.
type PortDetector struct {
	// Patterns to match in stdout for port detection
	patterns []*regexp.Regexp
}

// NewPortDetector creates a new port detector with common patterns.
func NewPortDetector() *PortDetector {
	return &PortDetector{
		patterns: []*regexp.Regexp{
			// Next.js: "ready started server on 0.0.0.0:3000"
			// Also: "- Local: http://localhost:3000"
			regexp.MustCompile(`(?:localhost|127\.0\.0\.1|0\.0\.0\.0):(\d+)`),
			// Vite: "Local: http://localhost:5173/"
			regexp.MustCompile(`Local:\s*https?://[^:]+:(\d+)`),
			// Generic: "listening on port 3000"
			regexp.MustCompile(`(?i)listening\s+(?:on\s+)?port\s+(\d+)`),
			// Generic: "server running at http://..."
			regexp.MustCompile(`(?i)(?:server|app)\s+(?:is\s+)?running\s+(?:at|on)\s+https?://[^:]+:(\d+)`),
			// Generic: "started on port 3000"
			regexp.MustCompile(`(?i)started\s+(?:on\s+)?port\s+(\d+)`),
			// Generic: "port: 3000" or "PORT: 3000"
			regexp.MustCompile(`(?i)port[:\s]+(\d+)`),
		},
	}
}

// DetectFromOutput scans output text for port patterns.
// Returns the first port found, or 0 if none detected.
func (pd *PortDetector) DetectFromOutput(output string) int {
	for _, pattern := range pd.patterns {
		if matches := pattern.FindStringSubmatch(output); len(matches) > 1 {
			if port, err := strconv.Atoi(matches[1]); err == nil && port > 0 && port < 65536 {
				return port
			}
		}
	}
	return 0
}

// DetectFromPID finds listening TCP ports for a process.
// Uses platform-native APIs: /proc/net/tcp on Linux, lsof on macOS,
// netstat on Windows. Returns all detected ports.
func (pd *PortDetector) DetectFromPID(ctx context.Context, pid int) []int {
	return detectPortsForPID(ctx, pid)
}

// WaitForPort waits for a port to be detected, either from output or PID.
// Returns the detected port or 0 if timeout expires.
func (pd *PortDetector) WaitForPort(ctx context.Context, pid int, getOutput func() string, timeout time.Duration) int {
	deadline := time.Now().Add(timeout)
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return 0
		case <-ticker.C:
			if time.Now().After(deadline) {
				return 0
			}

			// Try output first (faster)
			if output := getOutput(); output != "" {
				if port := pd.DetectFromOutput(output); port > 0 {
					return port
				}
			}

			// Try PID-based detection
			if pid > 0 {
				if ports := pd.DetectFromPID(ctx, pid); len(ports) > 0 {
					return ports[0]
				}
			}
		}
	}
}
