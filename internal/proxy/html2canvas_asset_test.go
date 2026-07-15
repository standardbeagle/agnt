package proxy

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/standardbeagle/agnt/internal/proxy/scripts"
)

// TestHandleHtml2Canvas verifies the on-demand capture library is served with a
// cacheable JS content type and carries the real library body — the endpoint
// window.__devtool_ensureHtml2canvas injects on first screenshot.
func TestHandleHtml2Canvas(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/__devtool_html2canvas", nil)
	rec := httptest.NewRecorder()

	handleHtml2Canvas(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "javascript") {
		t.Errorf("Content-Type = %q, want javascript", ct)
	}
	if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "max-age") {
		t.Errorf("Cache-Control = %q, want a cacheable max-age", cc)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "html2canvas-pro 1.5.8") {
		t.Error("served asset does not contain the html2canvas library body")
	}
	if body != scripts.GetHtml2Canvas() {
		t.Error("served asset does not match scripts.GetHtml2Canvas()")
	}
}
