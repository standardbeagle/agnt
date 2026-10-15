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
	if !strings.Contains(src, "message.type === 'secret_ack'") {
		t.Fatal("toast.js does not handle the server's secret_ack")
	}
	handler := strings.Index(src, "function handleSecretAck")
	okCheck := strings.Index(src, "ack.ok === true")
	if handler < 0 || okCheck < handler {
		t.Fatal("toast.js handleSecretAck must branch on ack.ok === true")
	}
	const saved = "saved to the agnt store"
	if n := strings.Count(src, saved); n != 1 {
		t.Fatalf("toast.js has %d %q messages, want exactly one (the ok:true branch)", n, saved)
	}
	if at := strings.Index(src, saved); at < okCheck {
		t.Fatalf("toast.js reports %q outside the ack.ok === true branch", saved)
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
