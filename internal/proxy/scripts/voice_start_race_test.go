package scripts

import (
	"strings"
	"testing"
)

// TestVoiceStartIsCancellable guards the pending-start handling in voice.js.
// getUserMedia waits on the permission prompt, and the old code acted on its
// result unconditionally: a stop() during the prompt still opened the
// microphone and sent voice_start, and a double toggle() opened two captures
// and leaked the first stream. A start now carries a token that stop()
// clears, a resolved stream for a cancelled start is stopped at once, and
// toggle() treats a pending start as on.
//
// Source guard only: the behaviour is proven in
// jstest/voice-start-race.test.js (make test-js).
func TestVoiceStartIsCancellable(t *testing.T) {
	src := moduleScript["voice"]
	if src == "" {
		t.Fatal("voice module source not embedded")
	}
	// The snippets span lines; a CRLF checkout (Windows CI) must still match.
	src = strings.ReplaceAll(src, "\r\n", "\n")
	for _, want := range []struct{ snippet, why string }{
		{"if (voiceState.proxy.pendingStart !== token) {\n          stopTracks(stream);", "a stream resolved for a cancelled start must be stopped"},
		{"voiceState.proxy.pendingStart = 0;\n\n    if (voiceState.proxy.processor)", "cleanupProxyVoice (stop) must cancel a pending start"},
		{"if (voiceState.isListening || voiceState.proxy.pendingStart) {", "toggle() must treat a pending start as on"},
		{"if (voiceState.proxy.pendingStart || voiceState.proxy.audioStream) {", "a second start() while one is pending or live must not open another capture"},
	} {
		if !strings.Contains(src, want.snippet) {
			t.Errorf("voice.js: %s (missing %q)", want.why, want.snippet)
		}
	}
}
