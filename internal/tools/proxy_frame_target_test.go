package tools

import (
	"strings"
	"testing"
)

// TestResolveExecTarget: Target wins over frame_id; outer→@chrome; inner→empty
// (server picks active frame); explicit frame id passes through.
func TestResolveExecTarget(t *testing.T) {
	cases := []struct {
		target, frameID, want string
	}{
		{"outer", "", "@chrome"},
		{"shell", "", "@chrome"},
		{"chrome", "", "@chrome"},
		{"inner", "", ""},
		{"active", "", ""},
		{"OUTER", "", "@chrome"}, // case-insensitive
		{" inner ", "", ""},      // trimmed
		{"", "frameX", "frameX"}, // no target → frame_id passthrough
		{"", "", ""},
		{"frameZ", "ignored", "frameZ"}, // explicit token wins over frame_id
	}
	for _, c := range cases {
		if got := resolveExecTarget(c.target, c.frameID); got != c.want {
			t.Errorf("resolveExecTarget(%q,%q) = %q, want %q", c.target, c.frameID, got, c.want)
		}
	}
}

// TestBuildNavigateJS: each direction yields the right navigation, deferred to a
// microtask so the exec reply is sent before the page unloads; goto requires url.
func TestBuildNavigateJS(t *testing.T) {
	dirs := map[string]string{
		"back":    "history.back()",
		"forward": "history.forward()",
		"reload":  "location.reload()",
		"refresh": "location.reload()",
	}
	for dir, want := range dirs {
		js, err := buildNavigateJS(dir, "")
		if err != nil {
			t.Fatalf("buildNavigateJS(%q): %v", dir, err)
		}
		if !strings.Contains(js, want) {
			t.Errorf("buildNavigateJS(%q) missing %q: %s", dir, want, js)
		}
		if !strings.Contains(js, "setTimeout(") {
			t.Errorf("buildNavigateJS(%q) must defer navigation: %s", dir, js)
		}
	}

	js, err := buildNavigateJS("goto", "http://example.com/x?a=1")
	if err != nil {
		t.Fatalf("goto: %v", err)
	}
	if !strings.Contains(js, `location.assign("http://example.com/x?a=1")`) {
		t.Errorf("goto must assign the url literal: %s", js)
	}

	if _, err := buildNavigateJS("goto", ""); err == nil {
		t.Errorf("goto without url must error")
	}
	if _, err := buildNavigateJS("sideways", ""); err == nil {
		t.Errorf("unknown direction must error")
	}
}

// TestBuildNavigateJS_EscapesURL: a hostile url cannot break out of the JS string.
func TestBuildNavigateJS_EscapesURL(t *testing.T) {
	js, err := buildNavigateJS("goto", `http://x/");alert(1);//`)
	if err != nil {
		t.Fatal(err)
	}
	// The closing quote in the url must be backslash-escaped so it cannot end the
	// JS string literal early.
	if !strings.Contains(js, `\")`) {
		t.Errorf("url quote must be escaped: %s", js)
	}
	if strings.Contains(js, `assign("http://x/");`) {
		t.Errorf("url must not break out of the string literal: %s", js)
	}
}

// TestBuildResizeJS: emits the shell helper call with the requested dimensions
// and a fallback message when the shell is absent.
func TestBuildResizeJS(t *testing.T) {
	js := buildResizeJS(375, 0)
	if !strings.Contains(js, "__devtool_resize_content(375,0)") {
		t.Errorf("resize must call the shell helper with dims: %s", js)
	}
	if !strings.Contains(js, "requires the chrome shell") {
		t.Errorf("resize must carry a no-shell fallback: %s", js)
	}
}
