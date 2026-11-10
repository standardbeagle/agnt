package proxy

import (
	"bytes"
	"testing"
)

// TestVoiceBacklog_BoundedInBytes: audio held while a Deepgram dial is in
// flight was capped only by frame count, and a frame may be up to the 16 MiB
// WebSocket limit, so 64 held frames could pin 1 GiB per connection. The
// backlog is now capped in bytes too; a frame that would cross the cap is
// dropped whole, keeping the stream frame-aligned.
func TestVoiceBacklog_BoundedInBytes(t *testing.T) {
	ps := newGateProxy(t)
	c, _ := newGateConn(t, ps, true)
	c.voiceMu.Lock()
	c.voicePending++
	c.voiceMu.Unlock()

	big := bytes.Repeat([]byte{0x7f}, voiceBacklogMaxBytes/4+1)
	for i := 0; i < voiceBacklogMaxFrames; i++ {
		c.handleBinaryFrame(big)
	}
	c.voiceMu.Lock()
	held := 0
	for _, f := range c.voiceBacklog {
		held += len(f)
	}
	frames := len(c.voiceBacklog)
	c.voiceMu.Unlock()
	if held > voiceBacklogMaxBytes {
		t.Fatalf("backlog holds %d bytes in %d frames, cap %d", held, frames, voiceBacklogMaxBytes)
	}
	if frames == 0 {
		t.Fatal("no frame held: frames under the cap must still be kept")
	}
}
