package proxy

import (
	"testing"
	"time"
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
