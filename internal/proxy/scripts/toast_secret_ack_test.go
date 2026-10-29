package scripts

import (
	"strings"
	"testing"
)

// TestToastSecretSavedOnlyOnAck guards the secret-entry outcome in toast.js.
// The server answers every secret_submit with secret_ack {name, ok, error}
// (internal/proxy/ws_messages.go handleSecretSubmit): ok is false when the
// store write fails or the connection may not write secrets. toast.js used to
// say "saved" as soon as the frame went out and never read the ack, so a
// refused or failed secret was reported to the user as stored.
//
// Source guard only: the behaviour (no "saved" before the ack, the refusal
// text on ok:false, an unknown-outcome notice when no ack comes) is proven in
// jstest/toast-secret-ack.test.js (make test-js).
func TestToastSecretSavedOnlyOnAck(t *testing.T) {
	src := moduleScript["toast"]
	if src == "" {
		t.Fatal("toast module source not embedded")
	}
	// The ack resolves the pending submission with ok only when the server
	// said so, and a missing ack times out as a failure.
	mustContain(t, "toast.js", src,
		"message.type === 'secret_ack'",
		"pending.cb(message.ok === true,",
		"finish(false, 'No response — try again.');",
	)
	// "saved" is reported once, from onSubmitted, and onSubmitted runs only
	// in finish's ok branch, never straight from the form's submit.
	const saved = "saved to the agnt store"
	if n := strings.Count(src, saved); n != 1 {
		t.Fatalf("toast.js has %d %q messages, want exactly one (onSubmitted)", n, saved)
	}
	if n := strings.Count(src, "options.onSubmitted()"); n != 1 {
		t.Fatalf("toast.js calls onSubmitted %d times, want exactly one (finish's ok branch)", n)
	}
	finish := strings.Index(src, "function finish(ok, reason)")
	call := strings.Index(src, "if (ok) {\n            if (options.onSubmitted) options.onSubmitted();")
	if finish < 0 || call < finish {
		t.Fatal("toast.js must call onSubmitted only inside finish's ok branch")
	}
}

// TestToastSecretPromptNeverEvicted guards maxVisible eviction in toast.js. A
// secret-entry prompt persists (duration 0) until submitted or dismissed; the
// eviction loop used to dismiss state.toasts[0] unconditionally, so three
// ordinary toasts arriving while the user typed a secret threw the prompt away.
//
// Source guard only: the behaviour (prompts survive a burst, ordinary toasts
// still evict oldest-first, prompts alone may exceed the cap) is proven in
// jstest/toast-secret-evict.test.js (make test-js).
func TestToastSecretPromptNeverEvicted(t *testing.T) {
	src := moduleScript["toast"]
	if src == "" {
		t.Fatal("toast module source not embedded")
	}
	mustContain(t, "toast.js", src,
		"secretPrompt: options.input === 'secret' && !!options.name",
		"if (state.toasts[evictAt].secretPrompt) {",
	)
	mustNotContain(t, "toast.js", src, "dismiss(state.toasts[0].id);")
}
