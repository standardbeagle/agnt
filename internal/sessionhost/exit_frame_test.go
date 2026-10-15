package sessionhost

import (
	"encoding/json"
	"testing"
	"time"

	goprocess "github.com/standardbeagle/go-cli-server/process"
)

// The exit frame is the subscriber's only notice that the child is gone. A
// subscriber whose buffer is full of live output must still receive it: live
// output may be dropped (and resynced), the exit frame never.
func TestExitFrameNotDroppedForFullSubscriber(t *testing.T) {
	s := &Session{
		subs:       make(map[string]*subscriber),
		scrollback: goprocess.NewRingBuffer(64 * 1024),
		doneCh:     make(chan struct{}),
	}
	ch, _, _ := s.Attach(4)

	// Fill the subscriber's buffer and keep going, as a chatty child does
	// while the attached client is slow.
	for i := 0; i < 50; i++ {
		s.broadcast([]byte("output"))
	}
	exitData, _ := json.Marshal(ExitData{Code: 3})
	s.broadcastFrame(Frame{Type: "exit", Data: exitData})

	types, _ := decodeFrames(t, ch, 200*time.Millisecond)
	if len(types) == 0 || types[len(types)-1] != "exit" {
		t.Fatalf("frames = %v, want the exit frame last", types)
	}
}
