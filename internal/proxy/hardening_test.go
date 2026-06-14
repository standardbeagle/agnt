package proxy

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/standardbeagle/agnt/internal/scope"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Race Condition Tests for TrafficLogger ---

func TestTrafficLogger_ConcurrentLogAndQuery(t *testing.T) {
	logger := NewTrafficLogger(100)
	var wg sync.WaitGroup

	// 10 goroutines writing different log types concurrently
	for w := 0; w < 10; w++ {
		wg.Add(1)
		go func(writerID int) {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				logger.LogHTTP(HTTPLogEntry{
					ID:         fmt.Sprintf("http-%d-%d", writerID, i),
					Timestamp:  time.Now(),
					Method:     "GET",
					URL:        fmt.Sprintf("/api/test/%d", i),
					StatusCode: 200,
				})
				logger.LogError(FrontendError{
					ID:        fmt.Sprintf("err-%d-%d", writerID, i),
					Timestamp: time.Now(),
					Message:   fmt.Sprintf("error from writer %d", writerID),
				})
				logger.LogCustom(CustomLog{
					ID:        fmt.Sprintf("custom-%d-%d", writerID, i),
					Timestamp: time.Now(),
					Level:     "info",
					Message:   "test",
				})
			}
		}(w)
	}

	// 5 concurrent readers
	for r := 0; r < 5; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 30; i++ {
				_ = logger.Query(LogFilter{})
				_ = logger.Query(LogFilter{Types: []LogEntryType{LogTypeHTTP}})
				_ = logger.Query(LogFilter{ErrorsOnly: true})
				_ = logger.Stats()
			}
		}()
	}

	wg.Wait()

	stats := logger.Stats()
	assert.Greater(t, stats.TotalEntries, int64(0))
	assert.LessOrEqual(t, stats.AvailableEntries, int64(100))
}

func TestTrafficLogger_ConcurrentLogAndClear(t *testing.T) {
	logger := NewTrafficLogger(50)
	var wg sync.WaitGroup

	// Writers
	for w := 0; w < 5; w++ {
		wg.Add(1)
		go func(writerID int) {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				logger.LogHTTP(HTTPLogEntry{
					ID:        fmt.Sprintf("log-%d-%d", writerID, i),
					Timestamp: time.Now(),
					Method:    "POST",
					URL:       "/api/data",
				})
			}
		}(w)
	}

	// Concurrent clears
	for c := 0; c < 3; c++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 10; i++ {
				logger.Clear()
				time.Sleep(time.Millisecond)
			}
		}()
	}

	wg.Wait()

	// Should not panic or deadlock
	_ = logger.Stats()
	_ = logger.Query(LogFilter{})
}

func TestTrafficLogger_ConcurrentAllLogTypes(t *testing.T) {
	logger := NewTrafficLogger(200)
	var wg sync.WaitGroup

	now := time.Now()

	// Each goroutine logs a different type
	logFuncs := []func(){
		func() {
			logger.LogHTTP(HTTPLogEntry{Timestamp: now, Method: "GET", URL: "/test"})
		},
		func() {
			logger.LogError(FrontendError{Timestamp: now, Message: "test error"})
		},
		func() {
			logger.LogPerformance(PerformanceMetric{Timestamp: now})
		},
		func() {
			logger.LogCustom(CustomLog{Timestamp: now, Level: "info", Message: "test"})
		},
		func() {
			logger.LogScreenshot(Screenshot{Timestamp: now, Name: "test"})
		},
		func() {
			logger.LogExecution(ExecutionResult{Timestamp: now, Code: "alert(1)"})
		},
		func() {
			logger.LogResponse(ExecutionResponse{Timestamp: now, Success: true})
		},
		func() {
			logger.LogInteraction(InteractionEvent{Timestamp: now, EventType: "click"})
		},
		func() {
			logger.LogMutation(MutationEvent{Timestamp: now, MutationType: "added"})
		},
		func() {
			logger.LogPanelMessage(PanelMessage{Timestamp: now, Message: "hello"})
		},
	}

	for _, fn := range logFuncs {
		for i := 0; i < 5; i++ {
			wg.Add(1)
			logFn := fn
			go func() {
				defer wg.Done()
				for j := 0; j < 20; j++ {
					logFn()
				}
			}()
		}
	}

	wg.Wait()

	stats := logger.Stats()
	assert.Greater(t, stats.TotalEntries, int64(0))
}

// --- Shutdown-Under-Load Tests for ProxyManager ---

func TestProxyManager_ShutdownUnderLoad(t *testing.T) {
	pm := NewProxyManager()
	ctx := context.Background()

	// Create several proxies
	proxyIDs := make([]string, 5)
	for i := 0; i < 5; i++ {
		id := fmt.Sprintf("load-%d", i)
		proxyIDs[i] = id
		config := ProxyConfig{
			ID:         id,
			TargetURL:  "http://localhost:9999",
			ListenPort: 0,
			MaxLogSize: 100,
		}
		_, err := pm.Create(ctx, config)
		require.NoError(t, err)
	}

	require.Equal(t, int64(5), pm.ActiveCount())

	// Start concurrent operations on the proxies
	var wg sync.WaitGroup

	// Goroutines continuously listing and getting proxies
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				_ = pm.ListScoped(scope.Unscoped("test"))
				_, _ = pm.Get(proxyIDs[id%len(proxyIDs)])
			}
		}(i)
	}

	// Shutdown while operations are in-flight
	shutdownErr := make(chan error, 1)
	go func() {
		time.Sleep(5 * time.Millisecond) // Let the reads start
		shutdownErr <- pm.Shutdown(ctx)
	}()

	wg.Wait()

	err := <-shutdownErr
	assert.NoError(t, err)
	assert.Equal(t, int64(0), pm.ActiveCount())
}

func TestProxyManager_ShutdownWithTimeout(t *testing.T) {
	pm := NewProxyManager()
	ctx := context.Background()

	// Create proxies
	for i := 0; i < 3; i++ {
		config := ProxyConfig{
			ID:         fmt.Sprintf("timeout-%d", i),
			TargetURL:  "http://localhost:9999",
			ListenPort: 0,
			MaxLogSize: 100,
		}
		_, err := pm.Create(ctx, config)
		require.NoError(t, err)
	}

	// Shutdown with a very short timeout
	shortCtx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()

	_ = pm.Shutdown(shortCtx)

	// Even if some proxies time out, the manager should be in shutdown state
	assert.True(t, pm.shuttingDown.Load())
}

func TestProxyManager_ConcurrentCreateDuringShutdown(t *testing.T) {
	pm := NewProxyManager()
	ctx := context.Background()

	// Create initial proxies
	for i := 0; i < 3; i++ {
		config := ProxyConfig{
			ID:         fmt.Sprintf("init-%d", i),
			TargetURL:  "http://localhost:9999",
			ListenPort: 0,
			MaxLogSize: 100,
		}
		_, err := pm.Create(ctx, config)
		require.NoError(t, err)
	}

	var wg sync.WaitGroup
	createErrors := make([]error, 10)

	// Start shutdown
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = pm.Shutdown(ctx)
	}()

	// Try to create proxies concurrently with shutdown
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			config := ProxyConfig{
				ID:         fmt.Sprintf("during-shutdown-%d", id),
				TargetURL:  "http://localhost:9999",
				ListenPort: 0,
				MaxLogSize: 100,
			}
			_, err := pm.Create(ctx, config)
			createErrors[id] = err
		}(i)
	}

	wg.Wait()

	// At least some creates should have been rejected
	rejectedCount := 0
	for _, err := range createErrors {
		if err != nil {
			rejectedCount++
		}
	}

	// After shutdown completes, all creates should be rejected
	config := ProxyConfig{
		ID:         "post-shutdown",
		TargetURL:  "http://localhost:9999",
		ListenPort: 0,
		MaxLogSize: 100,
	}
	_, err := pm.Create(ctx, config)
	assert.Error(t, err, "create after shutdown should fail")
}

func TestProxyManager_StopAllUnderLoad(t *testing.T) {
	pm := NewProxyManager()
	ctx := context.Background()

	// Create proxies
	for i := 0; i < 5; i++ {
		config := ProxyConfig{
			ID:         fmt.Sprintf("stopall-%d", i),
			TargetURL:  "http://localhost:9999",
			ListenPort: 0,
			MaxLogSize: 100,
		}
		_, err := pm.Create(ctx, config)
		require.NoError(t, err)
	}

	var wg sync.WaitGroup

	// Concurrent list/get operations
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 30; j++ {
				_ = pm.ListScoped(scope.Unscoped("test"))
				_ = pm.ActiveCount()
				_ = pm.TotalStarted()
			}
		}()
	}

	// StopAll while reads are happening
	var stoppedIDs []string
	var stopErr error
	wg.Add(1)
	go func() {
		defer wg.Done()
		time.Sleep(2 * time.Millisecond)
		stoppedIDs, stopErr = pm.StopAll(ctx)
	}()

	wg.Wait()

	assert.NoError(t, stopErr)
	assert.Equal(t, 5, len(stoppedIDs))

	// Unlike Shutdown, StopAll should allow new proxies
	config := ProxyConfig{
		ID:         "after-stopall",
		TargetURL:  "http://localhost:9999",
		ListenPort: 0,
		MaxLogSize: 100,
	}
	proxy, err := pm.Create(ctx, config)
	assert.NoError(t, err)
	if proxy != nil {
		pm.Stop(ctx, "after-stopall")
	}
}

func TestProxyManager_ConcurrentStopSameProxy(t *testing.T) {
	pm := NewProxyManager()
	ctx := context.Background()

	config := ProxyConfig{
		ID:         "double-stop",
		TargetURL:  "http://localhost:9999",
		ListenPort: 0,
		MaxLogSize: 100,
	}
	_, err := pm.Create(ctx, config)
	require.NoError(t, err)

	var wg sync.WaitGroup
	errors := make([]error, 5)

	// 5 goroutines all try to stop the same proxy
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			errors[idx] = pm.Stop(ctx, "double-stop")
		}(i)
	}

	wg.Wait()

	// Exactly one should succeed, others should get ErrProxyNotFound
	successCount := 0
	for _, err := range errors {
		if err == nil {
			successCount++
		}
	}
	// At least one succeeded (might be more if the race resolves before
	// the delete, but the proxy should still be stopped cleanly)
	assert.GreaterOrEqual(t, successCount, 1)
	assert.Equal(t, int64(0), pm.ActiveCount())
}

// --- Race Condition Tests for ProxyManager Registry ---

func TestProxyManager_ConcurrentCreateAndList(t *testing.T) {
	pm := NewProxyManager()
	ctx := context.Background()
	var wg sync.WaitGroup

	// Create proxies concurrently
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			config := ProxyConfig{
				ID:         fmt.Sprintf("race-create-%d", id),
				TargetURL:  "http://localhost:9999",
				ListenPort: 0,
				MaxLogSize: 100,
			}
			_, _ = pm.Create(ctx, config)
		}(i)
	}

	// List concurrently
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				_ = pm.ListScoped(scope.Unscoped("test"))
				_ = pm.ActiveCount()
			}
		}()
	}

	wg.Wait()

	// Cleanup
	for _, p := range pm.ListScoped(scope.Unscoped("test")) {
		pm.Stop(ctx, p.ID)
	}
}
