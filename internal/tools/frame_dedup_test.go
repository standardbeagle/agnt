package tools

import (
	"testing"
	"time"

	"github.com/standardbeagle/agnt/internal/proxy"
)

// TestDedup_DistinctFramesNotCollapsed: the same JS error raised in two distinct
// content frames must remain two findings; raised twice in one frame collapses
// to one with count 2. FrameID is part of the dedup key
// (docs/responsive-canonical-target.md §5.2/§6.2).
func TestDedup_DistinctFramesNotCollapsed(t *testing.T) {
	fe := &proxy.FrontendError{
		Message:   "TypeError: x is undefined",
		Source:    "app.js",
		LineNo:    10,
		ColNo:     5,
		Timestamp: time.Now(),
	}

	var errs []unifiedError
	errs = append(errs, convertJSErrorDirect("dev", fe, "frameA")...)
	errs = append(errs, convertJSErrorDirect("dev", fe, "frameB")...)
	deduped := deduplicateErrors(errs)
	if len(deduped) != 2 {
		t.Fatalf("same error in two frames must yield 2 findings, got %d", len(deduped))
	}

	// Same frame twice → one finding, count 2.
	var same []unifiedError
	same = append(same, convertJSErrorDirect("dev", fe, "frameA")...)
	same = append(same, convertJSErrorDirect("dev", fe, "frameA")...)
	collapsed := deduplicateErrors(same)
	if len(collapsed) != 1 {
		t.Fatalf("same error in same frame must collapse to 1, got %d", len(collapsed))
	}
	if collapsed[0].Count != 2 {
		t.Errorf("collapsed finding count = %d, want 2", collapsed[0].Count)
	}
	if collapsed[0].FrameID != "frameA" {
		t.Errorf("collapsed finding FrameID = %q, want frameA", collapsed[0].FrameID)
	}
}

// TestConvert_StampsFrameID: the converter copies the envelope frame id onto the
// unified error.
func TestConvert_StampsFrameID(t *testing.T) {
	entry := proxy.LogEntry{
		Type:    proxy.LogTypeError,
		FrameID: "frameZ",
		Error:   &proxy.FrontendError{Message: "boom", Timestamp: time.Now()},
	}
	out := convertProxyEntryDirect("dev", entry)
	if len(out) != 1 {
		t.Fatalf("expected 1 unified error, got %d", len(out))
	}
	if out[0].FrameID != "frameZ" {
		t.Errorf("FrameID = %q, want frameZ", out[0].FrameID)
	}
}
