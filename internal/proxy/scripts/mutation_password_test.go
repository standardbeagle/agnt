package scripts

import (
	"strings"
	"testing"
)

// TestMutationRedactsPasswordValueAttribute guards the password redaction in
// mutation.js. Attribute tracking is on by default and records old and new
// values; a page that mirrors the typed value into a password field's value
// attribute (React controlled inputs) put the password in the mutation log
// that reaches the agent.
//
// Source guard only: the behaviour is proven in
// jstest/mutation-password.test.js (make test-js).
func TestMutationRedactsPasswordValueAttribute(t *testing.T) {
	src := moduleScript["mutation"]
	if src == "" {
		t.Fatal("mutation module source not embedded")
	}
	for _, want := range []struct{ snippet, why string }{
		{"if (mutation.attributeName === 'value' && isPasswordInput(target)) {", "a password field's value attribute change must be redacted"},
		{"old_value: aOldValue,", "the recorded old value must be the redacted one"},
		{"function isPasswordInput(el) {", "password detection helper must exist"},
	} {
		if !strings.Contains(src, want.snippet) {
			t.Errorf("mutation.js: %s (missing %q)", want.why, want.snippet)
		}
	}
}
