package proxy

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestTrafficLogger_LogHTTP(t *testing.T) {
	logger := NewTrafficLogger(10)

	entry := HTTPLogEntry{
		ID:         "req-1",
		Timestamp:  time.Now(),
		Method:     "GET",
		URL:        "/api/test",
		StatusCode: 200,
		Duration:   50 * time.Millisecond,
	}

	logger.LogHTTP(entry)

	stats := logger.Stats()
	if stats.TotalEntries != 1 {
		t.Errorf("Expected 1 entry, got %d", stats.TotalEntries)
	}
	if stats.AvailableEntries != 1 {
		t.Errorf("Expected 1 available entry, got %d", stats.AvailableEntries)
	}
}

func TestTrafficLogger_CircularBuffer(t *testing.T) {
	maxSize := 5
	logger := NewTrafficLogger(maxSize)

	// Log more entries than max size
	for i := 0; i < 10; i++ {
		logger.LogHTTP(HTTPLogEntry{
			ID:         "req-" + string(rune('0'+i)),
			Timestamp:  time.Now(),
			Method:     "GET",
			URL:        "/test",
			StatusCode: 200,
		})
	}

	stats := logger.Stats()
	if stats.TotalEntries != 10 {
		t.Errorf("Expected 10 total entries, got %d", stats.TotalEntries)
	}
	if stats.AvailableEntries != int64(maxSize) {
		t.Errorf("Expected %d available entries, got %d", maxSize, stats.AvailableEntries)
	}
	if stats.Dropped != 5 {
		t.Errorf("Expected 5 dropped entries, got %d", stats.Dropped)
	}
}

func TestTrafficLogger_QueryByType(t *testing.T) {
	logger := NewTrafficLogger(100)

	logger.LogHTTP(HTTPLogEntry{
		ID:         "req-1",
		Timestamp:  time.Now(),
		Method:     "GET",
		URL:        "/test",
		StatusCode: 200,
	})

	logger.LogError(FrontendError{
		ID:        "err-1",
		Timestamp: time.Now(),
		Message:   "Test error",
		URL:       "/page",
	})

	// Query HTTP only
	filter := LogFilter{
		Types: []LogEntryType{LogTypeHTTP},
	}
	results := logger.Query(filter)

	if len(results) != 1 {
		t.Errorf("Expected 1 HTTP entry, got %d", len(results))
	}
	if results[0].Type != LogTypeHTTP {
		t.Errorf("Expected LogTypeHTTP, got %s", results[0].Type)
	}

	// Query errors only
	filter = LogFilter{
		Types: []LogEntryType{LogTypeError},
	}
	results = logger.Query(filter)

	if len(results) != 1 {
		t.Errorf("Expected 1 error entry, got %d", len(results))
	}
	if results[0].Type != LogTypeError {
		t.Errorf("Expected LogTypeError, got %s", results[0].Type)
	}
}

func TestTrafficLogger_QueryByMethod(t *testing.T) {
	logger := NewTrafficLogger(100)

	logger.LogHTTP(HTTPLogEntry{
		ID:         "req-1",
		Timestamp:  time.Now(),
		Method:     "GET",
		URL:        "/test",
		StatusCode: 200,
	})

	logger.LogHTTP(HTTPLogEntry{
		ID:         "req-2",
		Timestamp:  time.Now(),
		Method:     "POST",
		URL:        "/api",
		StatusCode: 201,
	})

	filter := LogFilter{
		Methods: []string{"POST"},
	}
	results := logger.Query(filter)

	if len(results) != 1 {
		t.Errorf("Expected 1 POST entry, got %d", len(results))
	}
	if results[0].HTTP.Method != "POST" {
		t.Errorf("Expected POST method, got %s", results[0].HTTP.Method)
	}
}

func TestTrafficLogger_QueryByURLPattern(t *testing.T) {
	logger := NewTrafficLogger(100)

	logger.LogHTTP(HTTPLogEntry{
		ID:         "req-1",
		Timestamp:  time.Now(),
		Method:     "GET",
		URL:        "/api/users",
		StatusCode: 200,
	})

	logger.LogHTTP(HTTPLogEntry{
		ID:         "req-2",
		Timestamp:  time.Now(),
		Method:     "GET",
		URL:        "/api/posts",
		StatusCode: 200,
	})

	logger.LogHTTP(HTTPLogEntry{
		ID:         "req-3",
		Timestamp:  time.Now(),
		Method:     "GET",
		URL:        "/static/image.png",
		StatusCode: 200,
	})

	filter := LogFilter{
		URLPattern: "/api",
	}
	results := logger.Query(filter)

	if len(results) != 2 {
		t.Errorf("Expected 2 API entries, got %d", len(results))
	}

	for _, result := range results {
		if !contains(result.HTTP.URL, "/api") {
			t.Errorf("Expected URL to contain /api, got %s", result.HTTP.URL)
		}
	}
}

func TestTrafficLogger_QueryByStatusCode(t *testing.T) {
	logger := NewTrafficLogger(100)

	logger.LogHTTP(HTTPLogEntry{
		ID:         "req-1",
		Timestamp:  time.Now(),
		Method:     "GET",
		URL:        "/test",
		StatusCode: 200,
	})

	logger.LogHTTP(HTTPLogEntry{
		ID:         "req-2",
		Timestamp:  time.Now(),
		Method:     "GET",
		URL:        "/error",
		StatusCode: 500,
	})

	filter := LogFilter{
		StatusCodes: []int{500},
	}
	results := logger.Query(filter)

	if len(results) != 1 {
		t.Errorf("Expected 1 error entry, got %d", len(results))
	}
	if results[0].HTTP.StatusCode != 500 {
		t.Errorf("Expected status 500, got %d", results[0].HTTP.StatusCode)
	}
}

func TestTrafficLogger_QueryByTimeRange(t *testing.T) {
	logger := NewTrafficLogger(100)

	now := time.Now()
	past := now.Add(-1 * time.Hour)

	logger.LogHTTP(HTTPLogEntry{
		ID:         "req-1",
		Timestamp:  past,
		Method:     "GET",
		URL:        "/old",
		StatusCode: 200,
	})

	logger.LogHTTP(HTTPLogEntry{
		ID:         "req-2",
		Timestamp:  now,
		Method:     "GET",
		URL:        "/current",
		StatusCode: 200,
	})

	// Query since 30 minutes ago
	since := now.Add(-30 * time.Minute)
	filter := LogFilter{
		Since: &since,
	}
	results := logger.Query(filter)

	if len(results) != 1 {
		t.Errorf("Expected 1 recent entry, got %d", len(results))
	}
	if results[0].HTTP.URL != "/current" {
		t.Errorf("Expected /current, got %s", results[0].HTTP.URL)
	}

	// Query until 30 minutes ago
	until := now.Add(-30 * time.Minute)
	filter = LogFilter{
		Until: &until,
	}
	results = logger.Query(filter)

	if len(results) != 1 {
		t.Errorf("Expected 1 old entry, got %d", len(results))
	}
	if results[0].HTTP.URL != "/old" {
		t.Errorf("Expected /old, got %s", results[0].HTTP.URL)
	}
}

func TestTrafficLogger_Clear(t *testing.T) {
	logger := NewTrafficLogger(10)

	for i := 0; i < 5; i++ {
		logger.LogHTTP(HTTPLogEntry{
			ID:         "req-" + string(rune('0'+i)),
			Timestamp:  time.Now(),
			Method:     "GET",
			URL:        "/test",
			StatusCode: 200,
		})
	}

	stats := logger.Stats()
	if stats.TotalEntries != 5 {
		t.Errorf("Expected 5 entries before clear, got %d", stats.TotalEntries)
	}

	logger.Clear()

	stats = logger.Stats()
	if stats.TotalEntries != 0 {
		t.Errorf("Expected 0 entries after clear, got %d", stats.TotalEntries)
	}
	if stats.AvailableEntries != 0 {
		t.Errorf("Expected 0 available entries after clear, got %d", stats.AvailableEntries)
	}
}

func TestTrafficLogger_QueryAfterWrap_ChronologicalOrder(t *testing.T) {
	maxSize := 5
	logger := NewTrafficLogger(maxSize)

	// Write 8 entries into a buffer of size 5.
	// After 8 writes: head=8, indices are:
	//   idx 0: write 5, idx 1: write 6, idx 2: write 7
	//   idx 3: write 3, idx 4: write 4
	// Chronological order should be: write 3, 4, 5, 6, 7
	baseTime := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 8; i++ {
		logger.LogHTTP(HTTPLogEntry{
			ID:         "req-" + string(rune('A'+i)),
			Timestamp:  baseTime.Add(time.Duration(i) * time.Second),
			Method:     "GET",
			URL:        "/test",
			StatusCode: 200,
		})
	}

	results := logger.Query(LogFilter{})
	if len(results) != maxSize {
		t.Fatalf("Expected %d entries, got %d", maxSize, len(results))
	}

	// Verify chronological order: entries 3, 4, 5, 6, 7
	for i, r := range results {
		expectedID := string(rune('A' + 3 + i))
		if r.HTTP.ID != "req-"+expectedID {
			t.Errorf("Entry %d: expected ID req-%s, got %s", i, expectedID, r.HTTP.ID)
		}
	}

	// Verify timestamps are strictly increasing
	for i := 1; i < len(results); i++ {
		if !results[i].HTTP.Timestamp.After(results[i-1].HTTP.Timestamp) {
			t.Errorf("Entry %d timestamp %v not after entry %d timestamp %v",
				i, results[i].HTTP.Timestamp, i-1, results[i-1].HTTP.Timestamp)
		}
	}
}

func TestTrafficLogger_QueryAfterWrap_NoStaleEntries(t *testing.T) {
	maxSize := 5
	logger := NewTrafficLogger(maxSize)

	// Write exactly 3 entries (no wrap), then query
	for i := 0; i < 3; i++ {
		logger.LogHTTP(HTTPLogEntry{
			ID:        "req-" + string(rune('A'+i)),
			Timestamp: time.Now(),
			Method:    "GET",
			URL:       "/test",
		})
	}

	results := logger.Query(LogFilter{})
	if len(results) != 3 {
		t.Fatalf("Expected 3 entries before wrap, got %d", len(results))
	}
	for _, r := range results {
		if r.Type != LogTypeHTTP {
			t.Errorf("Expected type %s, got %s", LogTypeHTTP, r.Type)
		}
	}

	// Now write enough to wrap
	for i := 3; i < 12; i++ {
		logger.LogHTTP(HTTPLogEntry{
			ID:        "req-" + string(rune('A'+i)),
			Timestamp: time.Now(),
			Method:    "POST",
			URL:       "/wrapped",
		})
	}

	results = logger.Query(LogFilter{})
	if len(results) != maxSize {
		t.Fatalf("Expected %d entries after wrap, got %d", maxSize, len(results))
	}

	// All entries must be POST (from the recent writes), not stale GET entries
	for i, r := range results {
		if r.Type == "" {
			t.Errorf("Entry %d has empty type (stale entry leaked)", i)
		}
		if r.HTTP == nil {
			t.Errorf("Entry %d has nil HTTP data (stale entry leaked)", i)
		} else if r.HTTP.Method != "POST" {
			t.Errorf("Entry %d: expected POST (recent), got %s (stale)", i, r.HTTP.Method)
		}
	}
}

func TestTrafficLogger_QueryAfterExactWrap(t *testing.T) {
	maxSize := 5
	logger := NewTrafficLogger(maxSize)

	// Write exactly maxSize entries (buffer full, no wrap yet)
	baseTime := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < maxSize; i++ {
		logger.LogHTTP(HTTPLogEntry{
			ID:        "req-" + string(rune('A'+i)),
			Timestamp: baseTime.Add(time.Duration(i) * time.Second),
			Method:    "GET",
			URL:       "/exact",
		})
	}

	results := logger.Query(LogFilter{})
	if len(results) != maxSize {
		t.Fatalf("Expected %d entries, got %d", maxSize, len(results))
	}

	// Should be in order A, B, C, D, E
	for i, r := range results {
		expectedID := string(rune('A' + i))
		if r.HTTP.ID != "req-"+expectedID {
			t.Errorf("Entry %d: expected req-%s, got %s", i, expectedID, r.HTTP.ID)
		}
	}

	// Write one more to trigger first wrap
	logger.LogHTTP(HTTPLogEntry{
		ID:        "req-F",
		Timestamp: baseTime.Add(5 * time.Second),
		Method:    "GET",
		URL:       "/exact",
	})

	results = logger.Query(LogFilter{})
	if len(results) != maxSize {
		t.Fatalf("Expected %d entries after first wrap, got %d", maxSize, len(results))
	}

	// Should be B, C, D, E, F (oldest A dropped)
	expected := []string{"req-B", "req-C", "req-D", "req-E", "req-F"}
	for i, r := range results {
		if r.HTTP.ID != expected[i] {
			t.Errorf("Entry %d: expected %s, got %s", i, expected[i], r.HTTP.ID)
		}
	}
}

func TestTrafficLogger_QueryWithFilterAfterWrap(t *testing.T) {
	maxSize := 5
	logger := NewTrafficLogger(maxSize)

	// Write mixed types that wrap
	for i := 0; i < 8; i++ {
		if i%2 == 0 {
			logger.LogHTTP(HTTPLogEntry{
				ID:        "req-" + string(rune('A'+i)),
				Timestamp: time.Now(),
				Method:    "GET",
				URL:       "/test",
			})
		} else {
			logger.LogError(FrontendError{
				ID:        "err-" + string(rune('A'+i)),
				Timestamp: time.Now(),
				Message:   "test error",
			})
		}
	}

	// Query only HTTP entries after wrap
	results := logger.Query(LogFilter{Types: []LogEntryType{LogTypeHTTP}})
	for _, r := range results {
		if r.Type != LogTypeHTTP {
			t.Errorf("Expected HTTP type, got %s", r.Type)
		}
		if r.HTTP == nil {
			t.Error("HTTP data is nil for HTTP type entry")
		}
	}

	// Query only error entries after wrap
	results = logger.Query(LogFilter{Types: []LogEntryType{LogTypeError}})
	for _, r := range results {
		if r.Type != LogTypeError {
			t.Errorf("Expected Error type, got %s", r.Type)
		}
		if r.Error == nil {
			t.Error("Error data is nil for Error type entry")
		}
	}
}

func TestTrafficLogger_ConcurrentWrites(t *testing.T) {
	logger := NewTrafficLogger(1000)

	done := make(chan bool)
	numGoroutines := 10
	entriesPerGoroutine := 100

	for i := 0; i < numGoroutines; i++ {
		go func(id int) {
			for j := 0; j < entriesPerGoroutine; j++ {
				logger.LogHTTP(HTTPLogEntry{
					ID:         "req",
					Timestamp:  time.Now(),
					Method:     "GET",
					URL:        "/test",
					StatusCode: 200,
				})
			}
			done <- true
		}(i)
	}

	for i := 0; i < numGoroutines; i++ {
		<-done
	}

	stats := logger.Stats()
	expectedTotal := int64(numGoroutines * entriesPerGoroutine)
	if stats.TotalEntries != expectedTotal {
		t.Errorf("Expected %d total entries, got %d", expectedTotal, stats.TotalEntries)
	}
}

func TestTrafficLogger_OnLogEntry(t *testing.T) {
	logger := NewTrafficLogger(10)
	t.Cleanup(logger.Close)

	// Delivery is asynchronous (decoupled onto a worker), so guard the shared
	// slice and drain with Eventually rather than reading immediately.
	var mu sync.Mutex
	var received []LogEntry
	logger.SetOnLogEntry(func(entry LogEntry) {
		mu.Lock()
		received = append(received, entry)
		mu.Unlock()
	})

	logger.LogHTTP(HTTPLogEntry{ID: "req-1", Method: "GET", URL: "/test", StatusCode: 200})
	logger.LogError(FrontendError{Message: "oops"})

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(received) == 2
	}, time.Second, 2*time.Millisecond, "expected 2 async callback invocations")

	mu.Lock()
	defer mu.Unlock()
	// FIFO order is preserved by the single-worker channel drain.
	if received[0].Type != LogTypeHTTP {
		t.Errorf("First callback: expected HTTP type, got %s", received[0].Type)
	}
	if received[1].Type != LogTypeError {
		t.Errorf("Second callback: expected Error type, got %s", received[1].Type)
	}
}

func TestTrafficLogger_OnLogEntry_Nil(t *testing.T) {
	logger := NewTrafficLogger(10)

	// No callback set — should not panic
	logger.LogHTTP(HTTPLogEntry{ID: "req-1", Method: "GET", URL: "/test", StatusCode: 200})
}

func TestTrafficLogger_SetOnLogEntry_Replaces(t *testing.T) {
	logger := NewTrafficLogger(10)
	t.Cleanup(logger.Close)

	var count1, count2 atomic.Int64

	logger.SetOnLogEntry(func(entry LogEntry) { count1.Add(1) })
	logger.LogHTTP(HTTPLogEntry{ID: "req-1", Method: "GET", URL: "/test", StatusCode: 200})
	// Drain the first entry before swapping so the replacement is guaranteed to
	// be the callback the second entry sees (the swap itself races the worker).
	require.Eventually(t, func() bool { return count1.Load() == 1 }, time.Second, 2*time.Millisecond,
		"first callback should receive the pre-swap entry")

	logger.SetOnLogEntry(func(entry LogEntry) { count2.Add(1) })
	logger.LogError(FrontendError{Message: "oops"})

	require.Eventually(t, func() bool { return count2.Load() == 1 }, time.Second, 2*time.Millisecond,
		"replacement callback should receive the post-swap entry")
	if got := count1.Load(); got != 1 {
		t.Errorf("First callback: expected 1 call, got %d", got)
	}
}

func TestTrafficLogger_LogDesignEdit(t *testing.T) {
	logger := NewTrafficLogger(10)

	ts := time.Date(2026, 6, 7, 9, 0, 0, 0, time.UTC)
	entry := DesignEdit{
		ID:             "edit-1",
		Timestamp:      ts,
		Selector:       ".hero",
		XPath:          "//*[@id=\"hero\"]",
		OID:            "oid-1",
		Deltas:         map[string]string{"width": "320px"},
		ComputedBefore: map[string]string{"width": "300px"},
		ComputedAfter:  map[string]string{"width": "320px"},
		Metadata:       DesignElementMetadata{Tag: "section", ID: "hero"},
		URL:            "http://localhost:3000/",
	}

	logger.LogDesignEdit(entry)

	// Retrievable by type.
	results := logger.Query(LogFilter{Types: []LogEntryType{LogTypeDesignEdit}})
	if len(results) != 1 {
		t.Fatalf("expected 1 design_edit entry, got %d", len(results))
	}
	got := results[0]
	if got.Type != LogTypeDesignEdit {
		t.Errorf("expected type %q, got %q", LogTypeDesignEdit, got.Type)
	}
	if got.DesignEdit == nil {
		t.Fatal("DesignEdit payload is nil")
	}
	if got.DesignEdit.Selector != ".hero" {
		t.Errorf("expected selector .hero, got %q", got.DesignEdit.Selector)
	}
	if got.DesignEdit.Deltas["width"] != "320px" {
		t.Errorf("expected delta width 320px, got %q", got.DesignEdit.Deltas["width"])
	}

	// Timestamp extraction routes through the design_edit case (Since filter
	// must include the entry when the window opens before its timestamp).
	since := ts.Add(-time.Minute)
	if n := len(logger.Query(LogFilter{Types: []LogEntryType{LogTypeDesignEdit}, Since: &since})); n != 1 {
		t.Errorf("expected design_edit within Since window, got %d", n)
	}
	after := ts.Add(time.Minute)
	if n := len(logger.Query(LogFilter{Types: []LogEntryType{LogTypeDesignEdit}, Since: &after})); n != 0 {
		t.Errorf("expected design_edit excluded by later Since, got %d", n)
	}
}

func TestTrafficLogger_LogWalkthrough(t *testing.T) {
	logger := NewTrafficLogger(10)

	logger.LogWalkthrough(WalkthroughEntry{
		ID:        "wt-1",
		Timestamp: time.Now(),
		Event:     "step",
		ScriptID:  "demo",
		StepIndex: 2,
		Total:     5,
		StepTitle: "Confirm",
		How:       "click",
		Mode:      "auto",
	})

	results := logger.Query(LogFilter{Types: []LogEntryType{LogTypeWalkthrough}})
	if len(results) != 1 {
		t.Fatalf("expected 1 walkthrough entry, got %d", len(results))
	}
	got := results[0]
	if got.Type != LogTypeWalkthrough {
		t.Errorf("type = %s, want %s", got.Type, LogTypeWalkthrough)
	}
	if got.Walkthrough == nil {
		t.Fatal("Walkthrough payload nil")
	}
	if got.Walkthrough.ScriptID != "demo" || got.Walkthrough.Event != "step" || got.Walkthrough.StepIndex != 2 {
		t.Errorf("unexpected payload: %+v", got.Walkthrough)
	}
}
