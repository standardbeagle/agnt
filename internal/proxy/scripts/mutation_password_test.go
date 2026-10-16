package scripts

import (
	"strings"
	"testing"
)

// TestMutationRedactsPasswordValueAttribute guards the password redaction in
// mutation.js. Attribute tracking is on by default and records old and new
// values; a page that mirrors the typed value into a password field's value
// attribute (React controlled inputs) put the password in the mutation log
// that reaches the agent. The value is kept only with the operator's
// record-passwords opt-in, and the change is always marked password so the
// proxy can redact it for an untrusted connection.
//
// Source guard only: the behaviour is proven in
// jstest/mutation-password.test.js and jstest/interaction.test.js
// (make test-js).
func TestMutationRedactsPasswordValueAttribute(t *testing.T) {
	src := moduleScript["mutation"]
	if src == "" {
		t.Fatal("mutation module source not embedded")
	}
	for _, want := range []struct{ snippet, why string }{
		{"var aPassword = mutation.attributeName === 'value' && isPasswordInput(target);", "a password field's value attribute change must be identified"},
		{"if (aPassword && !recordPasswords()) {", "it must be redacted unless record-passwords is on"},
		{"return window.__devtool_record_passwords === true;", "the opt-in must be the proxy's page config, strictly true"},
		{"old_value: aOldValue,", "the recorded old value must be the redacted one"},
		{"password: aPassword || undefined", "the change must carry the password marker the proxy redacts by"},
		{"function isPasswordInput(el) {", "password detection helper must exist"},
	} {
		if !strings.Contains(src, want.snippet) {
			t.Errorf("mutation.js: %s (missing %q)", want.why, want.snippet)
		}
	}
	isrc := moduleScript["interaction"]
	for _, want := range []struct{ snippet, why string }{
		{"if (isPasswordField(target)) interaction.password = true;", "every event on a password field must carry the marker"},
		{"if (target.value && (!isPasswordField(target) || recordPasswords())) {", "a password value must be sent only with record-passwords"},
		{"return window.__devtool_record_passwords === true;", "the opt-in must be the proxy's page config, strictly true"},
	} {
		if !strings.Contains(isrc, want.snippet) {
			t.Errorf("interaction.js: %s (missing %q)", want.why, want.snippet)
		}
	}
}
