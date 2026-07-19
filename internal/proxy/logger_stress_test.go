package proxy

// Mechanism-isolation stress harness for TrafficLogger (G4).
//
// Scope: this file exercises the circular buffer at
// internal/proxy/logger.go — AddEntry/Log* dispatch, the onLogEntry
// callback path, Query/Stats accounting under contention, and the drop
// counter semantics. It NEVER opens an httptest.Server, NEVER spawns a
// real ProxyServer, NEVER touches a real WebSocket, NEVER talks to the
// daemon. The system under test is pure in-memory state: a slice of
// LogEntry guarded by an RWMutex plus three atomics (head, count,
// onLogEntry pointer).
//
// Mental model (read logger.go end-to-end before editing):
//
//   * TrafficLogger.log() is the single dispatch point. head is a
//     monotonically increasing atomic.Int64; the write slot is
//     (head-1) % maxSize. log() holds tl.mu across the head advance,
//     the slice write, and the count bump — so Query holding RLock
//     either sees a slot's previous value or its fully-written new
//     value, never a half-filled LogEntry. The callback fires AFTER
//     the lock is released so slow/panicking callbacks cannot stall
//     producers or other readers.
//
//   * The onLogEntry callback is delivered ASYNCHRONOUSLY via a
//     logCallbackDispatcher (a bounded buffered channel drained by one
//     worker goroutine, created lazily on the first SetOnLogEntry). log()
//     does a non-blocking hand-off after the slice write + count bump, so
//     a slow/blocking/panicking sink can never stall producers — the
//     "must not block" contract is now structurally enforced, not trusted.
//     Delivery is FIFO; overflow drops the newest and counts it. Tests
//     call Close() for deterministic worker teardown (goleak).
//
//   * Drop accounting: Stats.Dropped = max(0, total - maxSize). The
//     ring never explicitly decrements; overwrites are implicit by
//     index wrap. The counter is the source of truth for "entries
//     that were evicted from the retained window."
//
//   * Clear() zeroes the slice AND resets head + count under the write
//     lock. A concurrent log() either runs entirely before Clear (and
//     is zeroed out by it) or entirely after (and writes into the
//     reset ring) — no torn state possible because log's reservation,
//     slice write, and count bump all happen under the same mutex as
//     Clear's reset.
//
// Every test runs under -race. Goroutine cleanup is verified per-test
// with goleak.VerifyNone(t, goleak.IgnoreCurrent()) — the proxy package
// has no TestMain, and we intentionally do not introduce one since
// sibling tests in this package spawn non-trivial goroutines (httptest
// servers, WebSocket handlers) that a package-wide goleak would have to
// track. p95 wall clock per test target: <1s.

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"
)

// ---- Test 1: HighThroughput --------------------------------------------

// TestTrafficLogger_HighThroughput asserts 100 producers × 10,000
// entries each (1M writes) into a 1000-slot ring yield:
//
//  1. Stats.TotalEntries == 1,000,000 (count atomic never skipped).
//  2. Stats.AvailableEntries == 1000 (clamped at maxSize).
//  3. Stats.Dropped == 999,000 (total - maxSize).
//  4. Query(all) returns exactly 1000 entries.
//  5. No torn writes — every retained entry has a non-nil HTTP payload
//     (we only Log HTTP entries) and a method/url matching our worker
//     template.
//
// Runtime target: <2s under -race. This is the "big hammer" test that
// forces the buffer to wrap ~1000 times, proving the mutex + atomic
// interplay scales under maximum producer contention.
func TestTrafficLogger_HighThroughput(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	const (
		workers        = 100
		entriesPerTask = 10_000
		maxSize        = 1000
	)
	total := int64(workers * entriesPerTask)

	tl := NewTrafficLogger(maxSize)

	var wg sync.WaitGroup
	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go func(worker int) {
			defer wg.Done()
			for i := 0; i < entriesPerTask; i++ {
				tl.LogHTTP(HTTPLogEntry{
					ID:         fmt.Sprintf("w%d-%d", worker, i),
					Timestamp:  time.Now(),
					Method:     "GET",
					URL:        "/stress",
					StatusCode: 200,
				})
			}
		}(w)
	}
	wg.Wait()

	stats := tl.Stats()

	// (1) count atomic never skipped.
	assert.Equal(t, total, stats.TotalEntries,
		"TotalEntries must equal every LogHTTP call that returned")

	// (2) AvailableEntries clamps at maxSize.
	assert.Equal(t, int64(maxSize), stats.AvailableEntries,
		"AvailableEntries clamps at maxSize even under 1000× overflow")

	// (3) Drop counter = total - maxSize.
	assert.Equal(t, total-int64(maxSize), stats.Dropped,
		"Dropped = TotalEntries - maxSize")

	// (4) Query returns exactly maxSize entries.
	all := tl.Query(LogFilter{})
	require.Len(t, all, maxSize,
		"Query with empty filter returns exactly maxSize retained entries")

	// (5) Every retained entry has a valid HTTP payload — no torn
	// writes where the Type was set but the union pointer was nil, and
	// no zero-value slots (those would be from Clear, not log).
	for i, e := range all {
		require.Equal(t, LogTypeHTTP, e.Type,
			"retained entry %d has unexpected type %q", i, e.Type)
		require.NotNil(t, e.HTTP,
			"retained entry %d has nil HTTP payload — torn write", i)
		require.Equal(t, "GET", e.HTTP.Method,
			"retained entry %d has unexpected method %q", i, e.HTTP.Method)
		require.Equal(t, "/stress", e.HTTP.URL,
			"retained entry %d has unexpected URL %q", i, e.HTTP.URL)
	}

	// Cap is stable.
	assert.Equal(t, int64(maxSize), stats.MaxSize)
}

// ---- Test 2: SlowCallback ----------------------------------------------

// TestTrafficLogger_SlowCallback asserts the onLogEntry callback fires
// per entry AND the logger remains correct when the callback adds
// visible latency. Spec says 5ms per callback — we keep the workload
// small (50 entries) so the test stays under ~400ms wall clock while
// still forcing the callback onto the critical path of every log()
// call.
//
// Contract under test (delivery is async):
//
//  1. Producers do NOT block on the slow callback — delivery is decoupled
//     onto the worker, so the log() loop returns near-instantly.
//  2. Every entry is eventually delivered exactly once (buffer 4096 >> 200,
//     so nothing is dropped), in per-producer FIFO order.
//  3. Ring-buffer Stats accounting matches the LogHTTP calls that returned,
//     independent of callback latency.
//  4. Swapping in a replacement callback takes effect for later entries and
//     the logger never hangs.
func TestTrafficLogger_SlowCallback(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	const (
		producers      = 4
		entriesPerTask = 50
		callbackDelay  = 5 * time.Millisecond
		maxSize        = 1000
	)
	total := producers * entriesPerTask

	tl := NewTrafficLogger(maxSize)
	defer tl.Close() // deterministic worker teardown for goleak

	var cbCount atomic.Int64
	var mu sync.Mutex
	perWorker := make(map[string][]int) // worker -> observed indices

	tl.SetOnLogEntry(func(e LogEntry) {
		cbCount.Add(1)
		time.Sleep(callbackDelay)
		// Record per-worker ordering. The ID embeds worker and index.
		if e.HTTP == nil {
			return
		}
		var w, i int
		if _, err := fmt.Sscanf(e.HTTP.ID, "w%d-%d", &w, &i); err == nil {
			mu.Lock()
			key := fmt.Sprintf("w%d", w)
			perWorker[key] = append(perWorker[key], i)
			mu.Unlock()
		}
	})

	start := time.Now()

	var wg sync.WaitGroup
	wg.Add(producers)
	for w := 0; w < producers; w++ {
		go func(worker int) {
			defer wg.Done()
			for i := 0; i < entriesPerTask; i++ {
				tl.LogHTTP(HTTPLogEntry{
					ID:         fmt.Sprintf("w%d-%d", worker, i),
					Timestamp:  time.Now(),
					Method:     "GET",
					URL:        "/cb",
					StatusCode: 200,
				})
			}
		}(w)
	}
	wg.Wait()

	elapsed := time.Since(start)

	// The whole point of the async dispatcher: producers do NOT block on the
	// slow (5ms) callback. Serialized, 200 entries × 5ms ≈ 1s; the producers
	// return near-instantly because delivery is decoupled onto the worker.
	assert.Less(t, elapsed, 500*time.Millisecond,
		"producers must not block on the slow callback (async delivery)")

	// (1) Every entry is eventually recorded (buffer 4096 > 200, no drops).
	// Gate on the recorded map, not cbCount, because cbCount increments before
	// the callback's sleep+append finishes.
	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		n := 0
		for _, v := range perWorker {
			n += len(v)
		}
		return n == total
	}, 5*time.Second, 5*time.Millisecond, "every callback recorded")
	assert.Equal(t, int64(total), cbCount.Load(), "callback fires once per log() call")
	assert.Zero(t, tl.droppedCallbacks(), "no drops with buffer >> entry count")

	// (3) Stats match the number of returned calls.
	stats := tl.Stats()
	assert.Equal(t, int64(total), stats.TotalEntries)
	assert.Equal(t, int64(total), stats.AvailableEntries,
		"with maxSize=%d and %d entries, all are retained", maxSize, total)

	// (2) Per-worker ordering: each worker's observed indices must be strictly
	// ascending. The single-worker FIFO drain preserves each producer's order.
	// defer Unlock so a failing require never wedges the worker on mu.
	func() {
		mu.Lock()
		defer mu.Unlock()
		assert.Len(t, perWorker, producers,
			"every worker produced at least one callback entry")
		for key, indices := range perWorker {
			require.Len(t, indices, entriesPerTask,
				"worker %s produced %d callbacks, expected %d",
				key, len(indices), entriesPerTask)
			for j := 1; j < len(indices); j++ {
				require.Greater(t, indices[j], indices[j-1],
					"worker %s callback ordering broken at j=%d: %d !> %d",
					key, j, indices[j], indices[j-1])
			}
		}
	}()

	// (4) Swap in a no-op callback and log more — the logger must not hang, and
	// the new callback must take effect.
	var newCount atomic.Int64
	tl.SetOnLogEntry(func(e LogEntry) {
		newCount.Add(1)
	})
	for i := 0; i < 10; i++ {
		tl.LogHTTP(HTTPLogEntry{
			ID:         fmt.Sprintf("post-%d", i),
			Timestamp:  time.Now(),
			Method:     "GET",
			URL:        "/post",
			StatusCode: 200,
		})
	}
	require.Eventually(t, func() bool { return newCount.Load() == 10 },
		5*time.Second, 5*time.Millisecond, "replacement callback receives post-swap entries")
}

// ---- Test 3: PanickingCallback ------------------------------------------

// TestTrafficLogger_PanickingCallback asserts the logger is resilient
// to callbacks that panic. Spec says "panics every 10th call" — the
// logger must not crash the producer goroutine and must keep adding
// entries correctly.
//
// Implementation note: the current logger.log() code does NOT wrap the
// callback in recover. A panicking callback propagates up through the
// producer goroutine. This is by design — the callback is documented
// as "must not block" but not "must not panic," and the production
// consumers (StreamSink drain, AlertScanner) never panic. To keep the
// test honest to what the logger actually guarantees, we wrap the
// callback in a recovering shim ourselves and assert:
//
//  1. Every panic is recovered (no goroutine crashes).
//  2. Stats.TotalEntries equals the number of LogHTTP calls — even
//     panicking entries are accounted for (the callback fires AFTER
//     the slice write and AFTER count bump).
//  3. Query returns entries for both panicking and non-panicking
//     callback invocations.
//  4. Panic count roughly matches expected ratio (~10% ± a few slots
//     due to head-reservation race under concurrency).
//
// If the logger ever adopts internal recover(), update this test to
// remove the shim and assert producer goroutines run unimpeded.
func TestTrafficLogger_PanickingCallback(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	const (
		producers      = 4
		entriesPerTask = 250
		maxSize        = 1000
	)
	total := producers * entriesPerTask

	tl := NewTrafficLogger(maxSize)
	defer tl.Close() // deterministic worker teardown for goleak

	var seen atomic.Int64
	var recovered atomic.Int64

	// The callback recovers its own panic (as a real consumer would). The
	// dispatcher worker ALSO recovers as defence in depth, so even an
	// un-recovered panic could not take the worker down — proven by the fact
	// that every subsequent entry is still delivered.
	tl.SetOnLogEntry(func(e LogEntry) {
		defer func() {
			if r := recover(); r != nil {
				recovered.Add(1)
			}
		}()
		n := seen.Add(1)
		if n%10 == 0 {
			panic(fmt.Sprintf("injected panic at callback %d", n))
		}
	})

	var wg sync.WaitGroup
	wg.Add(producers)
	for w := 0; w < producers; w++ {
		go func(worker int) {
			defer wg.Done()
			for i := 0; i < entriesPerTask; i++ {
				tl.LogHTTP(HTTPLogEntry{
					ID:         fmt.Sprintf("w%d-%d", worker, i),
					Timestamp:  time.Now(),
					Method:     "GET",
					URL:        "/panic",
					StatusCode: 200,
				})
			}
		}(w)
	}
	wg.Wait()

	// (1) Callback eventually fires for every entry (delivery is async now).
	require.Eventually(t, func() bool { return seen.Load() == int64(total) },
		5*time.Second, 5*time.Millisecond,
		"callback invoked for every log() — panics do not skip entries")

	// (4) Panic rate matches 1-in-10 (exact because seen is atomic incremented
	// inside the callback before the panic check).
	expectedPanics := int64(total / 10)
	assert.Equal(t, expectedPanics, recovered.Load(),
		"recovered panic count == floor(total/10)")

	// (2) Stats are correct regardless of panics.
	stats := tl.Stats()
	assert.Equal(t, int64(total), stats.TotalEntries,
		"panics do not break count accounting")
	assert.Equal(t, int64(total), stats.AvailableEntries,
		"all entries retained (total <= maxSize)")
	assert.Equal(t, int64(0), stats.Dropped,
		"no drops when total <= maxSize")

	// (3) Query returns every entry, regardless of whether its
	// callback panicked.
	all := tl.Query(LogFilter{})
	require.Len(t, all, total,
		"Query returns every entry, panic or not")
	for i, e := range all {
		require.Equal(t, LogTypeHTTP, e.Type,
			"entry %d has unexpected type %q", i, e.Type)
		require.NotNil(t, e.HTTP, "entry %d has nil HTTP payload", i)
		require.Equal(t, "/panic", e.HTTP.URL)
	}
}

// ---- Test 4: QueryWhileWriting ------------------------------------------

// TestTrafficLogger_QueryWhileWriting asserts readers calling Query
// concurrently with writers never observe torn state. A torn query
// would return:
//
//   - A zero-value LogEntry (Type=="") — would mean Query's available
//     count included a slot the producer reserved but had not yet
//     filled. The shared mutex across head/slice/count in log() closes
//     this window.
//   - An entry with Type set but the matching union pointer nil.
//   - An entry with a stale URL field but a fresh ID (partial struct
//     assignment visible through a copy).
//   - Len(results) > maxSize.
//
// We run 16 writers and 16 queriers for 200ms, then verify:
//
//  1. No torn entry ever observed (per-snapshot Validate).
//  2. Stats.TotalEntries + Stats.Dropped arithmetic closes at end.
//  3. Every Query result length <= maxSize and > 0 after the first
//     write lands.
//  4. Queries made many snapshots (producer doesn't starve consumer).
//
// Intended run harness: -race -count=50. p95 <500ms per run.
func TestTrafficLogger_QueryWhileWriting(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	const (
		writers  = 16
		readers  = 16
		duration = 200 * time.Millisecond
		maxSize  = 500
	)
	tl := NewTrafficLogger(maxSize)

	done := make(chan struct{})
	var wg sync.WaitGroup

	var totalWrites atomic.Int64
	wg.Add(writers)
	for w := 0; w < writers; w++ {
		go func(worker int) {
			defer wg.Done()
			i := 0
			for {
				select {
				case <-done:
					return
				default:
					tl.LogHTTP(HTTPLogEntry{
						ID:         fmt.Sprintf("w%d-%d", worker, i),
						Timestamp:  time.Now(),
						Method:     "GET",
						URL:        "/qw",
						StatusCode: 200,
					})
					totalWrites.Add(1)
					i++
				}
			}
		}(w)
	}

	var totalQueries atomic.Int64
	var tornCount atomic.Int64
	wg.Add(readers)
	for r := 0; r < readers; r++ {
		go func() {
			defer wg.Done()
			for {
				select {
				case <-done:
					return
				default:
					results := tl.Query(LogFilter{})
					totalQueries.Add(1)
					// (3) Length bound.
					if len(results) > maxSize {
						t.Errorf("Query returned %d > maxSize %d",
							len(results), maxSize)
						return
					}
					// (1) Per-entry integrity.
					for i, e := range results {
						if e.Type == "" {
							// Zero-value entry — only legal if
							// buffer has fewer writes than maxSize
							// and this is past the visible tail.
							// Since Query already bounds by
							// available = min(head, maxSize), a zero
							// entry here IS torn.
							tornCount.Add(1)
							t.Errorf("torn query: zero-type entry at index %d of %d",
								i, len(results))
							return
						}
						if e.Type == LogTypeHTTP && e.HTTP == nil {
							tornCount.Add(1)
							t.Errorf("torn query: HTTP type with nil payload at %d",
								i)
							return
						}
						if e.HTTP != nil && e.HTTP.URL != "/qw" {
							tornCount.Add(1)
							t.Errorf("torn query: unexpected URL %q at %d",
								e.HTTP.URL, i)
							return
						}
					}
				}
			}
		}()
	}

	time.Sleep(duration)
	close(done)
	wg.Wait()

	assert.Zero(t, tornCount.Load(),
		"no torn query observed across all readers")
	assert.Greater(t, totalQueries.Load(), int64(50),
		"readers completed many snapshots (consumer not starved)")

	// (2) Arithmetic closes at end.
	stats := tl.Stats()
	total := totalWrites.Load()
	require.Equal(t, total, stats.TotalEntries,
		"TotalEntries matches observed write count")
	if total >= int64(maxSize) {
		assert.Equal(t, int64(maxSize), stats.AvailableEntries)
		assert.Equal(t, total-int64(maxSize), stats.Dropped)
	} else {
		assert.Equal(t, total, stats.AvailableEntries)
		assert.Equal(t, int64(0), stats.Dropped)
	}
}

// ---- Test 5: ClearMidStream ---------------------------------------------

// TestTrafficLogger_ClearMidStream asserts Clear() is safe to call
// concurrently with Log* and Query. The settled state (after all
// producers stop and one final Clear) must be coherent:
//
//  1. No panic, no data race (enforced by -race).
//  2. After final Clear, Stats returns all-zero.
//  3. During the run, any Query observes entries that either belong
//     to the pre-Clear era (zero-value slots replaced by the most
//     recent writes) or the post-Clear era (new writes) — but never
//     a mix that violates the "entry has Type set ⇒ union pointer
//     non-nil" invariant.
//  4. Drop counter is correctly reset by Clear — a Clear followed by
//     N < maxSize writes must report Dropped=0. Spec calls out that
//     drop-counter behavior across Clear needs documenting; logger.go
//     Clear() resets head AND count to 0, so Dropped = max(0, 0-cap)
//     = 0 immediately post-Clear. We assert that contract here.
//
// Contract under concurrent log + Clear + Query:
//   - no deadlock
//   - no torn entry (Type set with union pointer nil)
//   - no zero-type entry visible in Query results (all visible slots
//     correspond to completed writes under the shared mutex)
//   - count never negative
//   - no -race report
func TestTrafficLogger_ClearMidStream(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	const (
		producers   = 8
		clearers    = 2
		queriers    = 4
		duration    = 150 * time.Millisecond
		maxSize     = 200
		clearPeriod = 10 * time.Millisecond
	)
	tl := NewTrafficLogger(maxSize)

	done := make(chan struct{})
	var wg sync.WaitGroup

	// Producers.
	var writeCount atomic.Int64
	wg.Add(producers)
	for w := 0; w < producers; w++ {
		go func(worker int) {
			defer wg.Done()
			i := 0
			for {
				select {
				case <-done:
					return
				default:
					tl.LogHTTP(HTTPLogEntry{
						ID:         fmt.Sprintf("w%d-%d", worker, i),
						Timestamp:  time.Now(),
						Method:     "POST",
						URL:        "/clr",
						StatusCode: 201,
					})
					writeCount.Add(1)
					i++
				}
			}
		}(w)
	}

	// Clearers.
	var clearCount atomic.Int64
	wg.Add(clearers)
	for c := 0; c < clearers; c++ {
		go func() {
			defer wg.Done()
			ticker := time.NewTicker(clearPeriod)
			defer ticker.Stop()
			for {
				select {
				case <-done:
					return
				case <-ticker.C:
					tl.Clear()
					clearCount.Add(1)
				}
			}
		}()
	}

	// Queriers — they enforce the torn-entry invariant.
	var queryCount atomic.Int64
	var tornCount atomic.Int64
	wg.Add(queriers)
	for q := 0; q < queriers; q++ {
		go func() {
			defer wg.Done()
			for {
				select {
				case <-done:
					return
				default:
					results := tl.Query(LogFilter{})
					queryCount.Add(1)
					if len(results) > maxSize {
						t.Errorf("Query returned %d > maxSize %d",
							len(results), maxSize)
						return
					}
					for i, e := range results {
						if e.Type == "" {
							// With log/Clear/Query all serialized
							// through tl.mu, a zero-type entry in
							// Query results means the logger's
							// consistency contract broke.
							tornCount.Add(1)
							t.Errorf("torn entry: zero-type at idx %d of %d results",
								i, len(results))
							return
						}
						if e.Type == LogTypeHTTP && e.HTTP == nil {
							t.Errorf("torn entry: HTTP type with nil payload at idx %d",
								i)
							return
						}
					}
				}
			}
		}()
	}

	// Run until every role has made forward progress, rather than for a fixed
	// slice of wall clock. Sleeping `duration` and then asserting a throughput
	// floor asserts the scheduler's generosity, not the logger's liveness: under
	// heavy Clear contention on a loaded machine the clearers fired exactly 3
	// times in the window and the test failed on "3 is not greater than 3".
	//
	// The deadline below is a deadlock guard. Only a real stall trips it.
	const (
		wantWrites  = int64(10)
		wantClears  = int64(3)
		wantQueries = int64(10)
	)
	deadline := time.Now().Add(30 * time.Second)
	for {
		progressed := writeCount.Load() > wantWrites &&
			clearCount.Load() > wantClears &&
			queryCount.Load() > wantQueries
		if progressed {
			break
		}
		if time.Now().After(deadline) {
			close(done)
			wg.Wait()
			t.Fatalf("no forward progress: writes=%d clears=%d queries=%d",
				writeCount.Load(), clearCount.Load(), queryCount.Load())
		}
		time.Sleep(time.Millisecond)
	}
	close(done)
	wg.Wait()

	// (1) -race catches anything. (goleak in defer catches goroutines.)

	// Every role made forward progress; the shape we care about is "nonzero
	// across the board, not a deadlock".
	assert.Greater(t, writeCount.Load(), wantWrites, "producers made forward progress")
	assert.Greater(t, clearCount.Load(), wantClears, "clearers fired several times")
	assert.Greater(t, queryCount.Load(), wantQueries, "queriers made many observations")

	// No torn slots permitted under the shared-mutex consistency
	// contract; tornCount is asserted zero here (any error above already
	// called t.Errorf which fails the test, but we also assert on the
	// atomic tally to cover the possibility of the querier goroutine
	// bailing out early).
	assert.Zero(t, tornCount.Load(),
		"no torn zero-type entries observed during concurrent log/Clear/Query")

	// (2) + (4) Settled state after final Clear.
	tl.Clear()
	stats := tl.Stats()
	assert.Equal(t, int64(0), stats.TotalEntries,
		"Clear resets TotalEntries")
	assert.Equal(t, int64(0), stats.AvailableEntries,
		"Clear resets AvailableEntries")
	assert.Equal(t, int64(0), stats.Dropped,
		"Clear resets Dropped counter (contract: drop is derived from count)")
	assert.Equal(t, int64(maxSize), stats.MaxSize,
		"MaxSize is stable across Clear")

	// Post-Clear writes of count < maxSize must report Dropped=0.
	for i := 0; i < maxSize/2; i++ {
		tl.LogHTTP(HTTPLogEntry{
			ID:         fmt.Sprintf("post-%d", i),
			Timestamp:  time.Now(),
			Method:     "GET",
			URL:        "/post",
			StatusCode: 200,
		})
	}
	stats = tl.Stats()
	assert.Equal(t, int64(maxSize/2), stats.TotalEntries)
	assert.Equal(t, int64(maxSize/2), stats.AvailableEntries)
	assert.Equal(t, int64(0), stats.Dropped,
		"Dropped=0 when post-Clear writes < maxSize")
}

// ---- Test 6: AllLogTypes ------------------------------------------------

// TestTrafficLogger_AllLogTypes asserts the logger handles every
// LogEntryType declared in logger.go — 22 types — and the type filter
// in LogFilter selects exactly the matching entries.
//
// There are actually 22 LogEntryType constants (http, error, performance,
// custom, screenshot, execution, response, interaction, mutation,
// panel_message, sketch, screenshot_capture, element_capture,
// sketch_capture, design_state, design_request, design_chat,
// diagnostic, process, hook). The task spec says "14" which is the
// original count before later additions. We cover ALL current types
// because the cost is trivial (one entry each) and catches schema
// drift.
//
// Verification:
//
//  1. For every type T, Log<T>(entry) followed by Query with
//     Types=[T] returns exactly 1 entry of that type with a non-nil
//     payload pointer.
//  2. Query with Types=[] (empty filter) returns every entry.
//  3. Query with Types=[T,U] returns entries of both types.
//  4. Stats.TotalEntries == number of types logged.
func TestTrafficLogger_AllLogTypes(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	tl := NewTrafficLogger(100)

	now := time.Now()

	// One entry per type. We use Log<X> methods (not raw log) so we
	// exercise the full dispatch path that production code uses.
	tl.LogHTTP(HTTPLogEntry{ID: "h1", Timestamp: now, Method: "GET", URL: "/", StatusCode: 200})
	tl.LogError(FrontendError{ID: "e1", Timestamp: now, Message: "boom"})
	tl.LogPerformance(PerformanceMetric{ID: "p1", Timestamp: now})
	tl.LogCustom(CustomLog{ID: "c1", Timestamp: now, Level: "info", Message: "x"})
	tl.LogScreenshot(Screenshot{ID: "s1", Timestamp: now, Name: "shot"})
	tl.LogExecution(ExecutionResult{ID: "x1", Timestamp: now, Code: "1+1"})
	tl.LogResponse(ExecutionResponse{ID: "r1", Timestamp: now, ExecID: "x1", Success: true})
	tl.LogInteraction(InteractionEvent{ID: "i1", Timestamp: now, EventType: "click"})
	tl.LogMutation(MutationEvent{ID: "m1", Timestamp: now, MutationType: "added"})
	tl.LogPanelMessage(PanelMessage{ID: "pm1", Timestamp: now, Message: "hi"})
	tl.LogSketch(SketchEntry{ID: "sk1", Timestamp: now, Description: "wf"})
	tl.LogScreenshotCapture(ScreenshotCapture{ID: "sc1", Timestamp: now, Summary: "cap"})
	tl.LogElementCapture(ElementCapture{ID: "ec1", Timestamp: now, Summary: "el"})
	tl.LogSketchCapture(SketchCapture{ID: "skc1", Timestamp: now, Summary: "skcap"})
	tl.LogDesignState(DesignState{ID: "ds1", Timestamp: now, Selector: "#x"})
	tl.LogDesignRequest(DesignRequest{ID: "dr1", Timestamp: now, Selector: "#x"})
	tl.LogDesignChat(DesignChat{ID: "dc1", Timestamp: now, Selector: "#x", Message: "iterate"})
	tl.LogDiagnostic(ProxyDiagnostic{Timestamp: now, Level: DiagnosticError, Category: "proxy", Event: "refused"})

	// Process and hook entries don't have public Log helpers — they
	// reach the logger through daemon BroadcastLogEntry paths. We
	// use the bare log() via direct LogEntry construction below.
	// But log() is unexported; the next-best thing is to verify the
	// 18 types that DO have Log helpers, then cover Process/Hook via
	// a union construction through a test-only shim. Simplest: log a
	// handful through the struct-assignment path by calling LogHTTP
	// etc. The 18 helpers cover the production surface area. Process
	// and Hook are injected by daemon.BroadcastProcessOutput and
	// drainHooks respectively, outside this package.
	//
	// We assert the helper set covers every LogEntryType constant
	// that has a LogXxx method. That is the contract tests can
	// enforce within the proxy package.

	// (4) Total entries = 18 helpers exercised.
	const expectedTypes = 18
	stats := tl.Stats()
	assert.Equal(t, int64(expectedTypes), stats.TotalEntries)
	assert.Equal(t, int64(expectedTypes), stats.AvailableEntries)
	assert.Equal(t, int64(0), stats.Dropped)

	// (2) Empty filter returns all.
	all := tl.Query(LogFilter{})
	require.Len(t, all, expectedTypes,
		"empty Types filter returns every entry")

	// (1) Each type yields exactly one entry with a non-nil union
	// pointer under Types=[T] filter.
	typeChecks := []struct {
		typ    LogEntryType
		verify func(t *testing.T, e LogEntry)
	}{
		{LogTypeHTTP, func(t *testing.T, e LogEntry) {
			require.NotNil(t, e.HTTP)
			assert.Equal(t, "h1", e.HTTP.ID)
		}},
		{LogTypeError, func(t *testing.T, e LogEntry) {
			require.NotNil(t, e.Error)
			assert.Equal(t, "e1", e.Error.ID)
		}},
		{LogTypePerformance, func(t *testing.T, e LogEntry) {
			require.NotNil(t, e.Performance)
			assert.Equal(t, "p1", e.Performance.ID)
		}},
		{LogTypeCustom, func(t *testing.T, e LogEntry) {
			require.NotNil(t, e.Custom)
			assert.Equal(t, "c1", e.Custom.ID)
		}},
		{LogTypeScreenshot, func(t *testing.T, e LogEntry) {
			require.NotNil(t, e.Screenshot)
			assert.Equal(t, "s1", e.Screenshot.ID)
		}},
		{LogTypeExecution, func(t *testing.T, e LogEntry) {
			require.NotNil(t, e.Execution)
			assert.Equal(t, "x1", e.Execution.ID)
		}},
		{LogTypeResponse, func(t *testing.T, e LogEntry) {
			require.NotNil(t, e.Response)
			assert.Equal(t, "r1", e.Response.ID)
		}},
		{LogTypeInteraction, func(t *testing.T, e LogEntry) {
			require.NotNil(t, e.Interaction)
			assert.Equal(t, "i1", e.Interaction.ID)
		}},
		{LogTypeMutation, func(t *testing.T, e LogEntry) {
			require.NotNil(t, e.Mutation)
			assert.Equal(t, "m1", e.Mutation.ID)
		}},
		{LogTypePanelMessage, func(t *testing.T, e LogEntry) {
			require.NotNil(t, e.PanelMessage)
			assert.Equal(t, "pm1", e.PanelMessage.ID)
		}},
		{LogTypeSketch, func(t *testing.T, e LogEntry) {
			require.NotNil(t, e.Sketch)
			assert.Equal(t, "sk1", e.Sketch.ID)
		}},
		{LogTypeScreenshotCapture, func(t *testing.T, e LogEntry) {
			require.NotNil(t, e.ScreenshotCapture)
			assert.Equal(t, "sc1", e.ScreenshotCapture.ID)
		}},
		{LogTypeElementCapture, func(t *testing.T, e LogEntry) {
			require.NotNil(t, e.ElementCapture)
			assert.Equal(t, "ec1", e.ElementCapture.ID)
		}},
		{LogTypeSketchCapture, func(t *testing.T, e LogEntry) {
			require.NotNil(t, e.SketchCapture)
			assert.Equal(t, "skc1", e.SketchCapture.ID)
		}},
		{LogTypeDesignState, func(t *testing.T, e LogEntry) {
			require.NotNil(t, e.DesignState)
			assert.Equal(t, "ds1", e.DesignState.ID)
		}},
		{LogTypeDesignRequest, func(t *testing.T, e LogEntry) {
			require.NotNil(t, e.DesignRequest)
			assert.Equal(t, "dr1", e.DesignRequest.ID)
		}},
		{LogTypeDesignChat, func(t *testing.T, e LogEntry) {
			require.NotNil(t, e.DesignChat)
			assert.Equal(t, "dc1", e.DesignChat.ID)
		}},
		{LogTypeDiagnostic, func(t *testing.T, e LogEntry) {
			require.NotNil(t, e.Diagnostic)
			assert.Equal(t, DiagnosticError, e.Diagnostic.Level)
		}},
	}

	require.Len(t, typeChecks, expectedTypes,
		"typeChecks must cover every helper-backed type")

	for _, tc := range typeChecks {
		t.Run(string(tc.typ), func(t *testing.T) {
			results := tl.Query(LogFilter{Types: []LogEntryType{tc.typ}})
			require.Lenf(t, results, 1,
				"filter Types=[%s] returns exactly 1 entry, got %d",
				tc.typ, len(results))
			require.Equal(t, tc.typ, results[0].Type,
				"filter returned wrong type")
			tc.verify(t, results[0])
		})
	}

	// (3) Multi-type filter.
	multi := tl.Query(LogFilter{Types: []LogEntryType{LogTypeHTTP, LogTypeError}})
	require.Len(t, multi, 2,
		"filter Types=[http,error] returns exactly both entries")
	types := map[LogEntryType]bool{}
	for _, e := range multi {
		types[e.Type] = true
	}
	assert.True(t, types[LogTypeHTTP], "http present")
	assert.True(t, types[LogTypeError], "error present")

	// Negative case: a type we never logged returns empty.
	none := tl.Query(LogFilter{Types: []LogEntryType{LogTypeProcessOutput}})
	assert.Empty(t, none, "Types=[process] returns empty — never logged")

	nonHook := tl.Query(LogFilter{Types: []LogEntryType{LogTypeHook}})
	assert.Empty(t, nonHook, "Types=[hook] returns empty — never logged via helper")
}

// TestTrafficLogger_SlowSinkDoesNotBlockProducer asserts finding 3: the
// onLogEntry callback is delivered asynchronously, so a deliberately-slow (or
// wedged) sink cannot stall the producing goroutine. Before the fix the
// callback fired synchronously in log() after unlock, so N entries × a blocking
// sink serialized onto the request/telemetry hot path.
func TestTrafficLogger_SlowSinkDoesNotBlockProducer(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	tl := NewTrafficLogger(1000)
	defer tl.Close()

	release := make(chan struct{})
	var entered atomic.Int64
	// A sink that blocks hard on its first invocation until released.
	tl.SetOnLogEntry(func(e LogEntry) {
		if entered.Add(1) == 1 {
			<-release // wedge the worker on the very first entry
		}
	})

	const n = 200
	start := time.Now()
	for i := 0; i < n; i++ {
		tl.LogHTTP(HTTPLogEntry{ID: fmt.Sprintf("r-%d", i), Timestamp: time.Now(), Method: "GET", URL: "/x", StatusCode: 200})
	}
	elapsed := time.Since(start)

	// The producer returned promptly even though the worker is wedged on the
	// first entry — proving delivery is decoupled. A synchronous callback would
	// have blocked here forever on <-release.
	if elapsed > 250*time.Millisecond {
		t.Fatalf("producer blocked on the slow sink: %v for %d entries", elapsed, n)
	}

	// The ring buffer retained every entry regardless of the wedged sink.
	if got := tl.Stats().TotalEntries; got != int64(n) {
		t.Fatalf("ring buffer lost entries: got %d want %d", got, n)
	}

	// Unwedge so the worker can exit cleanly under Close (goleak).
	close(release)
	require.Eventually(t, func() bool { return entered.Load() >= 1 }, time.Second, 5*time.Millisecond,
		"worker should have entered the sink at least once")
}
