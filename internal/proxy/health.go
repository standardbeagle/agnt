package proxy

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/standardbeagle/agnt/internal/debug"
	"github.com/standardbeagle/agnt/internal/proxy/scripts"
)

// runHealthCheck periodically probes the backend target via TCP connect.
// It tracks consecutive failures and emits a diagnostic event when the
// backend transitions from healthy to unhealthy, and logs recovery.
func (ps *ProxyServer) runHealthCheck(ctx context.Context) {
	if ps.healthCheckInterval <= 0 {
		return
	}

	ticker := time.NewTicker(ps.healthCheckInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if !ps.running.Load() {
				return
			}
			ps.probeBackend()
		}
	}
}

// probeBackend attempts a TCP connect to the backend target.
// On consecutive failures exceeding the threshold, it marks the backend
// unhealthy and emits a diagnostic event. Recovery resets the failure count.
func (ps *ProxyServer) probeBackend() {
	host := ps.TargetURL.Host
	err := probeTCP(host, ps.healthProbeTimeout)
	now := time.Now()
	ps.lastHealthCheck.Store(now)

	if err == nil {
		// Backend responded — reset failure count
		failCount := ps.healthFailCount.Swap(0)
		wasUnhealthy := !ps.backendHealthy.Swap(true)
		if wasUnhealthy && failCount > 0 {
			debug.Log("proxy", "backend %s recovered after %d failures for proxy %s", host, failCount, ps.ID)
			ps.logger.LogDiagnostic(ProxyDiagnostic{
				Timestamp: now,
				Level:     DiagnosticInfo,
				Category:  "health",
				Event:     "backend_recovered",
				Message:   fmt.Sprintf("Backend %s recovered", host),
				Target:    ps.TargetURL.String(),
			})
		}
		return
	}

	// Backend unreachable
	failCount := ps.healthFailCount.Add(1)
	debug.Log("proxy", "health check failed for %s (consecutive: %d/%d): %v",
		host, failCount, ps.healthFailThreshold, err)

	if failCount == ps.healthFailThreshold {
		ps.backendHealthy.Store(false)
		ps.logger.LogDiagnostic(ProxyDiagnostic{
			Timestamp: now,
			Level:     DiagnosticError,
			Category:  "health",
			Event:     "backend_unhealthy",
			Message:   fmt.Sprintf("Backend %s unreachable after %d consecutive failures: %v", host, failCount, err),
			Target:    ps.TargetURL.String(),
			Data: map[string]any{
				"consecutive_failures": failCount,
				"threshold":            ps.healthFailThreshold,
				"last_error":           err.Error(),
			},
		})
	}
}

// probeTCP attempts a TCP connect to the given host:port with a timeout.
func probeTCP(host string, timeout time.Duration) error {
	conn, err := net.DialTimeout("tcp", host, timeout)
	if err != nil {
		return err
	}
	conn.Close()
	return nil
}

// isAddressInUse checks if the error is due to address already in use.
func isAddressInUse(err error) bool {
	if err == nil {
		return false
	}
	// Check for "bind: address already in use" error
	return strings.Contains(err.Error(), "address already in use") ||
		strings.Contains(err.Error(), "bind") && strings.Contains(err.Error(), "in use")
}

// isTransientConnectionError checks if an error is a transient connection error
// that commonly occurs during development (server restarts, connection resets, etc.)
func isTransientConnectionError(errStr string) bool {
	transientPatterns := []string{
		// Windows-specific connection reset
		"wsarecv: An existing connection was forcibly closed",
		"wsasend: An existing connection was forcibly closed",
		// Unix connection reset
		"connection reset by peer",
		"read: connection reset",
		"write: connection reset",
		// Connection closed
		"EOF",
		"broken pipe",
		"use of closed network connection",
		// Timeout errors
		"i/o timeout",
		"connection timed out",
	}

	errLower := strings.ToLower(errStr)
	for _, pattern := range transientPatterns {
		if strings.Contains(errLower, strings.ToLower(pattern)) {
			return true
		}
	}
	return false
}

// handleAxeCore serves the bundled axe-core library for offline accessibility auditing.
func handleAxeCore(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/javascript")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.Write([]byte(scripts.GetAxeCore()))
}
