package publish

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Retention must not depend on the next submission. A share nobody posts to
// any more kept its expired rows readable — and on disk — forever, because
// pruning only ran inside Accept.
func TestFeedbackRetention_ExpiresWithoutAnotherSubmission(t *testing.T) {
	limits := testLimits()
	limits.RetentionDays = 30
	past := func(clk *fakeClock) { clk.advance(31 * 24 * time.Hour) }

	t.Run("read", func(t *testing.T) {
		clk := newFakeClock()
		s, dir := newTestStore(t, limits, clk)
		if err := s.Accept("share-1", "rev-1", "1.2.3.4:5000", []byte(`{"message":"old"}`)); err != nil {
			t.Fatal(err)
		}
		clk.advance(20 * 24 * time.Hour)
		if err := s.Accept("share-1", "rev-1", "1.2.3.4:5000", []byte(`{"message":"newer"}`)); err != nil {
			t.Fatal(err)
		}

		// 11 more days: the first row is 31 days old, the second 11.
		clk.advance(11 * 24 * time.Hour)
		rows, _, err := s.ReadByShare("share-1", "", 0)
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != 1 || !strings.Contains(rows[0].Body, "newer") {
			t.Fatalf("rows = %+v, want only the unexpired one", rows)
		}
		if got := s.Count("share-1"); got != 1 {
			t.Fatalf("Count = %d, want 1", got)
		}
		// The drop is durable: the expired body is gone from the ring file.
		data, err := os.ReadFile(filepath.Join(dir, feedbackFileName("share-1")))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), "old") {
			t.Fatalf("expired row is still on disk: %s", data)
		}

		// Everything expired: the ring file goes too.
		past(clk)
		if rows, _, err := s.ReadByShare("share-1", "", 0); err != nil || len(rows) != 0 {
			t.Fatalf("rows = %+v, err = %v; want none", rows, err)
		}
		if _, err := os.Stat(filepath.Join(dir, feedbackFileName("share-1"))); !os.IsNotExist(err) {
			t.Fatalf("a fully expired ring must be removed from disk; stat err = %v", err)
		}
	})

	t.Run("count alone", func(t *testing.T) {
		clk := newFakeClock()
		s, _ := newTestStore(t, limits, clk)
		if err := s.Accept("share-1", "rev-1", "1.2.3.4:5000", []byte(`{"message":"old"}`)); err != nil {
			t.Fatal(err)
		}
		past(clk)
		if got := s.Count("share-1"); got != 0 {
			t.Fatalf("Count = %d, want 0 once the row has expired", got)
		}
	})

	t.Run("load", func(t *testing.T) {
		clk := newFakeClock()
		s, dir := newTestStore(t, limits, clk)
		if err := s.Accept("share-1", "rev-1", "1.2.3.4:5000", []byte(`{"message":"old"}`)); err != nil {
			t.Fatal(err)
		}
		past(clk)

		// A restart with no read and no submission in between.
		if _, err := NewFeedbackStore(dir, limits, clk.now); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(dir, feedbackFileName("share-1"))); !os.IsNotExist(err) {
			t.Fatalf("load must drop an expired ring from disk; stat err = %v", err)
		}
	})
}

// Two applyStyle ops on one element that set different properties are two ops.
// The signature omitted Props, so the second was rejected as a duplicate.
func TestVariantValidate_DistinctStyleOpsAreNotDuplicates(t *testing.T) {
	style := func(prop, val string) Op {
		return Op{Op: OpApplyStyle, Selector: "#hero", Props: map[string]string{prop: val}}
	}

	distinct := Variant{ID: "v1", Ops: []Op{style("color", "red"), style("background-color", "blue")}}
	if err := distinct.Validate(); err != nil {
		t.Fatalf("distinct style ops rejected: %v", err)
	}
	sameProp := Variant{ID: "v1", Ops: []Op{style("color", "red"), style("color", "blue")}}
	if err := sameProp.Validate(); err != nil {
		t.Fatalf("same property, different value is not a duplicate: %v", err)
	}

	// A genuine duplicate is still caught, whatever order the map iterates in.
	multi := func() Op {
		return Op{Op: OpApplyStyle, Selector: "#hero", Props: map[string]string{"color": "red", "margin": "0", "padding": "0"}}
	}
	dup := Variant{ID: "v1", Ops: []Op{multi(), multi()}}
	if err := dup.Validate(); err == nil || !strings.Contains(err.Error(), "duplicate op") {
		t.Fatalf("identical style ops must still be rejected as duplicates, got %v", err)
	}

	// Property/value boundaries are part of the key.
	a := Op{Op: OpApplyStyle, Selector: "#hero", Props: map[string]string{"ab": "c"}}
	b := Op{Op: OpApplyStyle, Selector: "#hero", Props: map[string]string{"a": "bc"}}
	if a.signature() == b.signature() {
		t.Fatal("different property/value splits produced the same signature")
	}
}
