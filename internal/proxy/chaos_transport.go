package proxy

import (
	"context"
	"net/http"
	"sync"
	"time"

	"math/rand"
)

// ChaosTransport wraps http.RoundTripper to inject chaos at the transport level.
// This enables latency injection, request reordering, and full request/response control.
type ChaosTransport struct {
	underlying http.RoundTripper
	engine     *ChaosEngine
}

// NewChaosTransport creates a new chaos transport wrapping the given transport
func NewChaosTransport(underlying http.RoundTripper, engine *ChaosEngine) *ChaosTransport {
	if underlying == nil {
		underlying = http.DefaultTransport
	}
	return &ChaosTransport{
		underlying: underlying,
		engine:     engine,
	}
}

// RoundTrip implements http.RoundTripper with chaos injection
func (ct *ChaosTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Skip chaos for devtool endpoints
	if isDevtoolPath(req.URL.Path) {
		return ct.underlying.RoundTrip(req)
	}

	// Skip chaos for WebSocket upgrades
	if isWebSocketUpgrade(req) {
		return ct.underlying.RoundTrip(req)
	}

	// Get matching chaos rules
	rules := ct.engine.MatchingRules(req)
	if len(rules) == 0 {
		return ct.underlying.RoundTrip(req)
	}

	// Check for packet loss (drop request entirely)
	if ct.engine.ShouldDrop(rules) && ct.engine.HasRuleType(rules, ChaosPacketLoss) {
		// Return a connection refused error
		return nil, &chaosError{message: "chaos: connection dropped (packet loss)"}
	}

	// Check for stale delay (very long delays)
	if staleDelay := ct.engine.GetStaleDelay(rules); staleDelay > 0 {
		select {
		case <-req.Context().Done():
			return nil, req.Context().Err()
		case <-time.After(staleDelay):
			// Continue after stale delay
		}
	}

	// Apply latency injection
	if delay := ct.engine.GetLatencyDelay(rules); delay > 0 {
		select {
		case <-req.Context().Done():
			return nil, req.Context().Err()
		case <-time.After(delay):
			// Continue after delay
		}
	}

	// Check for out-of-order responses
	if ct.engine.ShouldReorder(rules) {
		return ct.engine.reorderQueue.Submit(req, ct.underlying, rules)
	}

	// Execute request normally
	return ct.underlying.RoundTrip(req)
}

// isDevtoolPath checks if the path is a devtool reserved endpoint
func isDevtoolPath(path string) bool {
	return len(path) >= 10 && path[:10] == "/__devtool"
}

// isWebSocketUpgrade checks if the request is a WebSocket upgrade
func isWebSocketUpgrade(req *http.Request) bool {
	return req.Header.Get("Upgrade") == "websocket" ||
		req.Header.Get("Connection") == "Upgrade"
}

// chaosError represents a chaos-injected error
type chaosError struct {
	message string
}

func (e *chaosError) Error() string {
	return e.message
}

// pendingRequest represents a request waiting to be reordered
type pendingRequest struct {
	req        *http.Request
	transport  http.RoundTripper
	rules      []*ChaosRule
	submitTime time.Time
	respChan   chan *http.Response
	errChan    chan error
}

// ReorderQueue holds requests and releases them in shuffled order
// to simulate out-of-order response delivery
type ReorderQueue struct {
	mu          sync.Mutex
	pending     []*pendingRequest
	minHold     int           // Minimum requests to hold before releasing
	maxWait     time.Duration // Maximum time to hold before force release
	lastRelease time.Time
	engine      *ChaosEngine

	// Shutdown coordination
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewReorderQueue creates a new reorder queue
func NewReorderQueue(engine *ChaosEngine) *ReorderQueue {
	ctx, cancel := context.WithCancel(context.Background())
	rq := &ReorderQueue{
		minHold:     2,
		maxWait:     500 * time.Millisecond,
		lastRelease: time.Now(),
		engine:      engine,
		ctx:         ctx,
		cancel:      cancel,
	}

	// Start background goroutine to force-release old requests
	rq.wg.Add(1)
	go rq.releaseLoop()

	return rq
}

// Submit adds a request to the reorder queue and waits for its response
func (rq *ReorderQueue) Submit(req *http.Request, transport http.RoundTripper, rules []*ChaosRule) (*http.Response, error) {
	pr := &pendingRequest{
		req:        req,
		transport:  transport,
		rules:      rules,
		submitTime: time.Now(),
		respChan:   make(chan *http.Response, 1),
		errChan:    make(chan error, 1),
	}

	rq.mu.Lock()
	rq.pending = append(rq.pending, pr)
	count := len(rq.pending)

	// Get config from first out-of-order rule
	minReq, maxWait := rq.engine.GetReorderConfig(rules)
	rq.minHold = minReq
	rq.maxWait = maxWait

	shouldRelease := count >= rq.minHold
	rq.mu.Unlock()

	if shouldRelease {
		rq.release()
	}

	// Wait for response or context cancellation
	select {
	case <-req.Context().Done():
		// Remove from pending if still there
		rq.removePending(pr)
		return nil, req.Context().Err()
	case resp := <-pr.respChan:
		return resp, nil
	case err := <-pr.errChan:
		return nil, err
	}
}

// release shuffles and executes all pending requests
func (rq *ReorderQueue) release() {
	rq.mu.Lock()
	if len(rq.pending) == 0 {
		rq.mu.Unlock()
		return
	}

	// Take all pending requests
	pending := rq.pending
	rq.pending = nil
	rq.lastRelease = time.Now()
	rq.mu.Unlock()

	// Shuffle the pending requests
	rand.Shuffle(len(pending), func(i, j int) {
		pending[i], pending[j] = pending[j], pending[i]
	})

	// Execute in shuffled order (parallel execution)
	for _, pr := range pending {
		rq.engine.IncrementReordered()
		go rq.execute(pr)
	}
}

// execute performs the actual HTTP request and sends the response
func (rq *ReorderQueue) execute(pr *pendingRequest) {
	resp, err := pr.transport.RoundTrip(pr.req)
	if err != nil {
		select {
		case pr.errChan <- err:
		default:
		}
		return
	}

	select {
	case pr.respChan <- resp:
	default:
	}
}

// removePending removes a pending request (e.g., on context cancellation)
func (rq *ReorderQueue) removePending(target *pendingRequest) {
	rq.mu.Lock()
	defer rq.mu.Unlock()

	for i, pr := range rq.pending {
		if pr == target {
			rq.pending = append(rq.pending[:i], rq.pending[i+1:]...)
			return
		}
	}
}

// releaseLoop periodically checks for stale pending requests
func (rq *ReorderQueue) releaseLoop() {
	defer rq.wg.Done()

	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-rq.ctx.Done():
			// Release any remaining on shutdown
			rq.release()
			return
		case <-ticker.C:
			rq.mu.Lock()
			shouldRelease := len(rq.pending) > 0 &&
				time.Since(rq.lastRelease) > rq.maxWait
			rq.mu.Unlock()

			if shouldRelease {
				rq.release()
			}
		}
	}
}

// Stop stops the reorder queue and releases any pending requests
func (rq *ReorderQueue) Stop() {
	rq.cancel()
	rq.wg.Wait()
}

// PendingCount returns the number of pending requests
func (rq *ReorderQueue) PendingCount() int {
	rq.mu.Lock()
	defer rq.mu.Unlock()
	return len(rq.pending)
}
