package proxy

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/standardbeagle/agnt/internal/publish"
)

// errSink returns a fixed error, to prove the handler's status mapping.
type errSink struct{ err error }

func (s errSink) Accept(shareID, revisionID, remoteAddr string, body []byte) error { return s.err }

// TestFeedbackErrorStatusMapping asserts sink errors map to the right HTTP code:
// rate-limit → 429, oversize → 413, other → 422.
func TestFeedbackErrorStatusMapping(t *testing.T) {
	jsonCT := map[string]string{"Content-Type": "application/json"}
	fbPath := sharePrefix + validToken + "/feedback"

	cases := []struct {
		name string
		err  error
		want int
	}{
		{"rate limited", publish.ErrFeedbackRateLimited, http.StatusTooManyRequests},
		{"too large", publish.ErrFeedbackTooLarge, http.StatusRequestEntityTooLarge},
		{"invalid", publish.ErrFeedbackInvalid, http.StatusUnprocessableEntity},
		{"unknown", errors.New("boom"), http.StatusUnprocessableEntity},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newTestHandler(errSink{err: c.err})
			w := do(h, http.MethodPost, fbPath, `{"message":"hi"}`, jsonCT)
			if w.Code != c.want {
				t.Fatalf("got %d, want %d", w.Code, c.want)
			}
		})
	}
}

// bodyOfSize returns a valid-JSON feedback body whose total length is exactly n
// bytes, padding the message field so the MaxBytesReader cap is what decides
// accept/reject (not JSON validity, which the handler does not check).
func bodyOfSize(n int) string {
	const pre = `{"message":"`
	const post = `"}`
	pad := n - len(pre) - len(post)
	if pad < 0 {
		pad = 0
	}
	return pre + strings.Repeat("x", pad) + post
}

// TestConfiguredMaxBodyHonored proves the handler uses the CONFIGURED body cap,
// not the hardcoded 4096: a larger cap accepts an over-4096 body, and a smaller
// cap rejects at the smaller size.
func TestConfiguredMaxBodyHonored(t *testing.T) {
	jsonCT := map[string]string{"Content-Type": "application/json"}
	fbPath := sharePrefix + validToken + "/feedback"
	v := &fakeVerifier{token: validToken, rev: sampleWalkthrough(), id: "share-1"}

	// Larger-than-default cap: a 6000-byte body (over the old 4096 hardcode) is
	// accepted when the operator configured 8192.
	t.Run("larger cap accepts over-default body", func(t *testing.T) {
		h := NewPublicHandler(v, &capturingSink{}, 8192)
		w := do(h, http.MethodPost, fbPath, bodyOfSize(6000), jsonCT)
		if w.Code != http.StatusAccepted {
			t.Fatalf("6000-byte body under an 8192 cap: got %d, want 202", w.Code)
		}
	})

	// Smaller-than-default cap: a 2000-byte body is rejected 413 when the
	// operator configured 1024 — the small cap, not 4096, governs.
	t.Run("smaller cap rejects at smaller size", func(t *testing.T) {
		h := NewPublicHandler(v, &capturingSink{}, 1024)
		w := do(h, http.MethodPost, fbPath, bodyOfSize(2000), jsonCT)
		if w.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("2000-byte body under a 1024 cap: got %d, want 413", w.Code)
		}
		// And a body under the small cap still succeeds.
		if w := do(h, http.MethodPost, fbPath, bodyOfSize(500), jsonCT); w.Code != http.StatusAccepted {
			t.Fatalf("500-byte body under a 1024 cap: got %d, want 202", w.Code)
		}
	})
}

// TestFeedbackThreadsRevisionAndRemoteAddr asserts the handler hands the real
// remote address and a non-empty revision digest to the sink (INV-7 keying),
// never a client X-Forwarded-For.
func TestFeedbackThreadsRevisionAndRemoteAddr(t *testing.T) {
	sink := &capturingSink{}
	h := newTestHandler(sink)
	w := do(h, http.MethodPost, sharePrefix+validToken+"/feedback", `{"message":"hi"}`,
		map[string]string{"Content-Type": "application/json", "X-Forwarded-For": "6.6.6.6"})
	if w.Code != http.StatusAccepted {
		t.Fatalf("feedback: got %d, want 202", w.Code)
	}
	if sink.calls != 1 {
		t.Fatalf("sink calls = %d", sink.calls)
	}
	if sink.revisionID == "" {
		t.Fatal("handler did not thread a revision id to the sink")
	}
	// httptest default RemoteAddr is 192.0.2.1:1234; the XFF header must be ignored.
	if sink.remoteAddr == "6.6.6.6" || sink.remoteAddr == "" {
		t.Fatalf("remoteAddr = %q; must be the real peer, never the XFF header", sink.remoteAddr)
	}
}

// TestRevokedShareRejectsWriteAgainstRealStore proves a revoked/unknown token
// 404s at the routing gate and NEVER reaches the durable store — no persisted
// record. The real *publish.FeedbackStore is wired as the sink to prove the
// construction path.
func TestRevokedShareRejectsWriteAgainstRealStore(t *testing.T) {
	clk := time.Unix(1_700_000_000, 0).UTC()
	store, err := publish.NewFeedbackStore(t.TempDir(), publish.FeedbackLimits{
		RatePerMinute: 10, Burst: 5, MaxBodyBytes: 4096, MaxRowsPerShare: 500, RetentionDays: 90,
	}, func() time.Time { return clk })
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	v := &fakeVerifier{token: validToken, rev: sampleWalkthrough(), id: "share-1"}
	h := NewPublicHandler(v, store, 0)
	jsonCT := map[string]string{"Content-Type": "application/json"}

	// A live token persists one row.
	if w := do(h, http.MethodPost, sharePrefix+validToken+"/feedback", `{"message":"live"}`, jsonCT); w.Code != http.StatusAccepted {
		t.Fatalf("live feedback: got %d, want 202", w.Code)
	}
	if store.Count("share-1") != 1 {
		t.Fatalf("live feedback not persisted: count=%d", store.Count("share-1"))
	}

	// A revoked/unknown token 404s and persists nothing (the verifier returns
	// ok=false for any other token, exactly as a revoked share does — INV-4).
	w := do(h, http.MethodPost, sharePrefix+"revoked-token/feedback", `{"message":"nope"}`, jsonCT)
	if w.Code != http.StatusNotFound {
		t.Fatalf("revoked feedback: got %d, want 404", w.Code)
	}
	rows, _, _ := store.ReadByRevision("", "", 0)
	for _, r := range rows {
		if r.Body == `{"message":"nope"}` {
			t.Fatal("revoked-share feedback was persisted — INV-4 violated")
		}
	}
}

// TestRealStoreRateLimitReturns429 wires the real store and drives past the burst
// to prove the end-to-end 429 path.
func TestRealStoreRateLimitReturns429(t *testing.T) {
	clk := time.Unix(1_700_000_000, 0).UTC()
	store, err := publish.NewFeedbackStore(t.TempDir(), publish.FeedbackLimits{
		RatePerMinute: 10, Burst: 3, MaxBodyBytes: 4096, MaxRowsPerShare: 500, RetentionDays: 90,
	}, func() time.Time { return clk })
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	v := &fakeVerifier{token: validToken, rev: sampleWalkthrough(), id: "share-1"}
	h := NewPublicHandler(v, store, 0)
	jsonCT := map[string]string{"Content-Type": "application/json"}
	fbPath := sharePrefix + validToken + "/feedback"

	for i := 0; i < 3; i++ {
		if w := do(h, http.MethodPost, fbPath, `{"message":"hi"}`, jsonCT); w.Code != http.StatusAccepted {
			t.Fatalf("burst post %d: got %d, want 202", i, w.Code)
		}
	}
	if w := do(h, http.MethodPost, fbPath, `{"message":"hi"}`, jsonCT); w.Code != http.StatusTooManyRequests {
		t.Fatalf("over-burst post: got %d, want 429", w.Code)
	}
	if store.Dropped() != 1 {
		t.Fatalf("dropped count = %d, want 1", store.Dropped())
	}
}
