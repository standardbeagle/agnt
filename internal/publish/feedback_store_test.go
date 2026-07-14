package publish

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeClock is a deterministic, injectable clock (no wall-clock sleeps).
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock() *fakeClock { return &fakeClock{t: time.Unix(1_700_000_000, 0).UTC()} }
func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}
func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func testLimits() FeedbackLimits {
	return FeedbackLimits{RatePerMinute: 10, Burst: 5, MaxBodyBytes: 4096, MaxRowsPerShare: 500, RetentionDays: 90}
}

func newTestStore(t *testing.T, limits FeedbackLimits, clk *fakeClock) (*FeedbackStore, string) {
	t.Helper()
	dir := t.TempDir()
	s, err := NewFeedbackStore(dir, limits, clk.now)
	if err != nil {
		t.Fatalf("NewFeedbackStore: %v", err)
	}
	return s, dir
}

// TestAcceptAppendsWithServerIDAndClock proves the row carries a server-assigned
// id + injected-clock timestamp, and that appends are ordered/additive.
func TestAcceptAppendsWithServerIDAndClock(t *testing.T) {
	clk := newFakeClock()
	s, _ := newTestStore(t, testLimits(), clk)

	if err := s.Accept("share-1", "rev-1", "1.2.3.4:5000", []byte(`{"message":"first"}`)); err != nil {
		t.Fatalf("accept 1: %v", err)
	}
	clk.advance(time.Second)
	if err := s.Accept("share-1", "rev-1", "1.2.3.4:5000", []byte(`{"message":"second"}`)); err != nil {
		t.Fatalf("accept 2: %v", err)
	}

	rows, _, err := s.ReadByShare("share-1", "", 0)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("want 2 rows, got %d", len(rows))
	}
	if rows[0].ID == "" || rows[1].ID == "" || rows[0].ID == rows[1].ID {
		t.Fatalf("server ids missing or non-unique: %q %q", rows[0].ID, rows[1].ID)
	}
	if !rows[0].CreatedAt.Equal(clk.now().Add(-time.Second)) {
		t.Fatalf("row 0 timestamp not from injected clock: %v", rows[0].CreatedAt)
	}
	if rows[0].Body != `{"message":"first"}` {
		t.Fatalf("body 0 mangled: %q", rows[0].Body)
	}
}

// TestValidationRejects covers malformed UTF-8, oversize, and bad schema.
func TestValidationRejects(t *testing.T) {
	clk := newFakeClock()
	limits := testLimits()
	limits.MaxBodyBytes = 64
	s, _ := newTestStore(t, limits, clk)

	cases := []struct {
		name string
		body []byte
		want error
	}{
		{"bad utf8", append([]byte(`{"message":"`), 0xff, 0xfe, '"', '}'), ErrFeedbackInvalid},
		{"oversize", []byte(`{"message":"` + strings.Repeat("x", 200) + `"}`), ErrFeedbackTooLarge},
		{"not json", []byte(`this is not json`), ErrFeedbackInvalid},
		{"not object", []byte(`["array"]`), ErrFeedbackInvalid},
		{"unknown field", []byte(`{"exec":"rm -rf"}`), ErrFeedbackInvalid},
		{"rating range", []byte(`{"rating":99}`), ErrFeedbackInvalid},
	}
	for i, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// Distinct source IP per case so the burst budget (rate-limit runs
			// first) never masks a validation reject under test.
			ip := fmt.Sprintf("9.9.9.%d", i)
			err := s.Accept("s", "r", ip, c.body)
			if !errors.Is(err, c.want) {
				t.Fatalf("got %v, want %v", err, c.want)
			}
		})
	}
	if s.Count("s") != 0 {
		t.Fatalf("rejected bodies must not persist; count=%d", s.Count("s"))
	}
}

// TestRateLimiterBurstThenThrottle asserts the token bucket allows burst then
// 429s, and that refill (via injected clock) restores budget.
func TestRateLimiterBurstThenThrottle(t *testing.T) {
	clk := newFakeClock()
	s, _ := newTestStore(t, testLimits(), clk) // burst 5, 10/min

	body := []byte(`{"message":"hi"}`)
	for i := 0; i < 5; i++ {
		if err := s.Accept("share-1", "rev", "5.5.5.5", body); err != nil {
			t.Fatalf("burst post %d rejected: %v", i, err)
		}
	}
	// 6th within the same instant → throttled.
	if err := s.Accept("share-1", "rev", "5.5.5.5", body); !errors.Is(err, ErrFeedbackRateLimited) {
		t.Fatalf("6th post: got %v, want rate-limited", err)
	}
	if s.Dropped() != 1 {
		t.Fatalf("dropped count = %d, want 1", s.Dropped())
	}
	// Advance 6s → 10/min = 1 token accrued → one more allowed, then throttled.
	clk.advance(6 * time.Second)
	if err := s.Accept("share-1", "rev", "5.5.5.5", body); err != nil {
		t.Fatalf("post-refill accept: %v", err)
	}
	if err := s.Accept("share-1", "rev", "5.5.5.5", body); !errors.Is(err, ErrFeedbackRateLimited) {
		t.Fatalf("post-refill 2nd: got %v, want rate-limited", err)
	}
}

// TestRateLimiterPerKeyIsolation asserts one IP's exhausted bucket does not
// throttle another IP, nor another share.
func TestRateLimiterPerKeyIsolation(t *testing.T) {
	clk := newFakeClock()
	s, _ := newTestStore(t, testLimits(), clk)
	body := []byte(`{"message":"hi"}`)

	for i := 0; i < 5; i++ {
		_ = s.Accept("share-1", "rev", "1.1.1.1", body)
	}
	// share-1/1.1.1.1 is now empty.
	if err := s.Accept("share-1", "rev", "1.1.1.1", body); !errors.Is(err, ErrFeedbackRateLimited) {
		t.Fatalf("exhausted key should throttle, got %v", err)
	}
	// Different IP, same share → fresh bucket.
	if err := s.Accept("share-1", "rev", "2.2.2.2", body); err != nil {
		t.Fatalf("different IP throttled: %v", err)
	}
	// Different share, same IP → fresh bucket.
	if err := s.Accept("share-2", "rev", "1.1.1.1", body); err != nil {
		t.Fatalf("different share throttled: %v", err)
	}
}

// TestXFFDoesNotEvadeLimiter proves the limiter keys on the real remoteAddr, so
// varying an X-Forwarded-For-style value (passed as body/other fields) cannot
// mint fresh buckets — the store never consults XFF; the same remoteAddr stays
// one bucket regardless of payload.
func TestXFFDoesNotEvadeLimiter(t *testing.T) {
	clk := newFakeClock()
	s, _ := newTestStore(t, testLimits(), clk)

	// Same real peer address; a client cannot pass a spoofed IP to Accept because
	// Accept takes only the real remoteAddr. Exhaust it, then confirm no payload
	// variation refills it.
	for i := 0; i < 5; i++ {
		if err := s.Accept("share-1", "rev", "10.0.0.1:1111", []byte(fmt.Sprintf(`{"message":"m%d"}`, i))); err != nil {
			t.Fatalf("burst %d: %v", i, err)
		}
	}
	if err := s.Accept("share-1", "rev", "10.0.0.1:2222", []byte(`{"message":"evade"}`)); !errors.Is(err, ErrFeedbackRateLimited) {
		t.Fatalf("same host different source port must share the bucket, got %v", err)
	}
}

// TestConcurrentAcceptRaceSafe drives many parallel posts to exercise the -race
// gate: no lost updates (dropped + persisted == total), no double count.
func TestConcurrentAcceptRaceSafe(t *testing.T) {
	clk := newFakeClock()
	limits := testLimits()
	limits.Burst = 50
	limits.RatePerMinute = 1 // no refill within the test instant beyond burst
	s, _ := newTestStore(t, limits, clk)

	const goroutines = 20
	const perG = 10
	var wg sync.WaitGroup
	body := []byte(`{"message":"concurrent"}`)
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perG; i++ {
				_ = s.Accept("share-1", "rev", "7.7.7.7:9", body)
			}
		}()
	}
	wg.Wait()

	total := goroutines * perG
	persisted := int64(s.Count("share-1"))
	dropped := s.Dropped()
	if persisted+dropped != int64(total) {
		t.Fatalf("lost updates: persisted %d + dropped %d != %d", persisted, dropped, total)
	}
	if persisted != int64(limits.Burst) {
		t.Fatalf("burst 50 should admit exactly 50, got %d persisted", persisted)
	}
}

// TestRestartSurvival writes feedback, reconstructs the store from the same dir,
// and asserts the rows survive.
func TestRestartSurvival(t *testing.T) {
	clk := newFakeClock()
	s, dir := newTestStore(t, testLimits(), clk)
	for i := 0; i < 3; i++ {
		clk.advance(time.Second) // distinct timestamps → deterministic read order
		if err := s.Accept("share-x", "rev-x", "8.8.8.8", []byte(fmt.Sprintf(`{"message":"m%d"}`, i))); err != nil {
			t.Fatalf("accept %d: %v", i, err)
		}
	}

	reopened, err := NewFeedbackStore(dir, testLimits(), clk.now)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	rows, _, _ := reopened.ReadByShare("share-x", "", 0)
	if len(rows) != 3 {
		t.Fatalf("after restart want 3 rows, got %d", len(rows))
	}
	if rows[0].Body != `{"message":"m0"}` {
		t.Fatalf("restart mangled row: %q", rows[0].Body)
	}
}

// TestCorruptionFailsLoud asserts a tampered ring file makes reopen fail loud
// rather than silently starting empty.
func TestCorruptionFailsLoud(t *testing.T) {
	clk := newFakeClock()
	s, dir := newTestStore(t, testLimits(), clk)
	if err := s.Accept("share-c", "rev-c", "8.8.8.8", []byte(`{"message":"m"}`)); err != nil {
		t.Fatalf("accept: %v", err)
	}
	// Corrupt the record body without fixing the checksum.
	path := filepath.Join(dir, feedbackFileName("share-c"))
	data, _ := os.ReadFile(path)
	// The body is stored as a JSON string, so its quotes are backslash-escaped;
	// tamper the unescaped word "message" instead, leaving the checksum stale.
	tampered := strings.Replace(string(data), "message", "HACKED_", 1)
	if tampered == string(data) {
		t.Fatal("test setup: expected to tamper the body")
	}
	if err := os.WriteFile(path, []byte(tampered), 0600); err != nil {
		t.Fatalf("write tampered: %v", err)
	}
	if _, err := NewFeedbackStore(dir, testLimits(), clk.now); !errors.Is(err, ErrFeedbackCorrupt) {
		t.Fatalf("corrupt store must fail loud with ErrFeedbackCorrupt, got %v", err)
	}
}

// TestRetentionPrunesOldestByCount asserts the count ring evicts oldest rows.
func TestRetentionPrunesOldestByCount(t *testing.T) {
	clk := newFakeClock()
	limits := testLimits()
	limits.MaxRowsPerShare = 3
	limits.Burst = 100
	limits.RatePerMinute = 100
	s, _ := newTestStore(t, limits, clk)

	for i := 0; i < 5; i++ {
		clk.advance(time.Second)
		if err := s.Accept("share-r", "rev-r", "8.8.8.8", []byte(fmt.Sprintf(`{"message":"m%d"}`, i))); err != nil {
			t.Fatalf("accept %d: %v", i, err)
		}
	}
	rows, _, _ := s.ReadByShare("share-r", "", 0)
	if len(rows) != 3 {
		t.Fatalf("count ring should keep 3, got %d", len(rows))
	}
	// Oldest (m0, m1) evicted; newest three (m2,m3,m4) survive in order.
	want := []string{`{"message":"m2"}`, `{"message":"m3"}`, `{"message":"m4"}`}
	for i, w := range want {
		if rows[i].Body != w {
			t.Fatalf("row %d = %q, want %q", i, rows[i].Body, w)
		}
	}
}

// TestRetentionPrunesByAge asserts rows older than the age window are evicted.
func TestRetentionPrunesByAge(t *testing.T) {
	clk := newFakeClock()
	limits := testLimits()
	limits.RetentionDays = 1
	limits.Burst = 100
	limits.RatePerMinute = 100
	s, _ := newTestStore(t, limits, clk)

	if err := s.Accept("share-a", "rev-a", "8.8.8.8", []byte(`{"message":"old"}`)); err != nil {
		t.Fatalf("accept old: %v", err)
	}
	clk.advance(48 * time.Hour) // older than 1-day window
	if err := s.Accept("share-a", "rev-a", "8.8.8.8", []byte(`{"message":"new"}`)); err != nil {
		t.Fatalf("accept new: %v", err)
	}
	rows, _, _ := s.ReadByShare("share-a", "", 0)
	if len(rows) != 1 || rows[0].Body != `{"message":"new"}` {
		t.Fatalf("age prune failed: %+v", rows)
	}
}

// TestXSSPayloadStoredInert proves a script/js payload is stored byte-identical
// and read back unchanged — inert data, never interpreted.
func TestXSSPayloadStoredInert(t *testing.T) {
	clk := newFakeClock()
	s, _ := newTestStore(t, testLimits(), clk)
	xss := `{"message":"<script>alert(1)</script> javascript:evil()"}`
	if err := s.Accept("share-xss", "rev-xss", "8.8.8.8", []byte(xss)); err != nil {
		t.Fatalf("accept xss: %v", err)
	}
	rows, _, _ := s.ReadByShare("share-xss", "", 0)
	if len(rows) != 1 {
		t.Fatalf("want 1 row, got %d", len(rows))
	}
	if rows[0].Body != xss {
		t.Fatalf("stored body altered: %q", rows[0].Body)
	}
	if !strings.Contains(rows[0].Body, "<script>") {
		t.Fatalf("expected inert literal <script> preserved")
	}
}

// TestReadByShareCursor asserts cursor pagination is stable and terminates.
func TestReadByShareCursor(t *testing.T) {
	clk := newFakeClock()
	limits := testLimits()
	limits.Burst = 100
	limits.RatePerMinute = 100
	s, _ := newTestStore(t, limits, clk)
	for i := 0; i < 5; i++ {
		clk.advance(time.Second)
		if err := s.Accept("share-p", "rev-p", "8.8.8.8", []byte(fmt.Sprintf(`{"message":"m%d"}`, i))); err != nil {
			t.Fatalf("accept %d: %v", i, err)
		}
	}
	var got []string
	cursor := ""
	for {
		page, next, err := s.ReadByShare("share-p", cursor, 2)
		if err != nil {
			t.Fatalf("page: %v", err)
		}
		for _, r := range page {
			got = append(got, r.Body)
		}
		if next == "" {
			break
		}
		cursor = next
	}
	if len(got) != 5 {
		t.Fatalf("cursor walk got %d rows, want 5: %v", len(got), got)
	}
	for i := 0; i < 5; i++ {
		if got[i] != fmt.Sprintf(`{"message":"m%d"}`, i) {
			t.Fatalf("cursor order wrong at %d: %q", i, got[i])
		}
	}
}

// TestInvalidLimitsRejected asserts a zero limit fails construction (no
// guard-disabling store).
func TestInvalidLimitsRejected(t *testing.T) {
	clk := newFakeClock()
	bad := testLimits()
	bad.MaxBodyBytes = 0
	if _, err := NewFeedbackStore(t.TempDir(), bad, clk.now); err == nil {
		t.Fatal("zero MaxBodyBytes must reject construction")
	}
}
