package daemon

import (
	"context"
	"testing"
)

type recordingFrameWriter struct {
	chunks []string
	ended  bool
}

func (w *recordingFrameWriter) WriteChunk(b []byte) error {
	w.chunks = append(w.chunks, string(b))
	return nil
}

func (w *recordingFrameWriter) WriteEnd() error {
	w.ended = true
	return nil
}

// When the session has exited, frames already queued for the attach (its
// final output and the exit frame) must still be written before the stream
// ends. Selecting between Done and the frame channel at random cut them off.
func TestStreamSessionHostFrames_DrainsQueuedFramesBeforeEnding(t *testing.T) {
	for i := 0; i < 50; i++ {
		frames := make(chan []byte, 16)
		for j := 0; j < 10; j++ {
			frames <- []byte(`{"type":"stdout"}`)
		}
		frames <- []byte(`{"type":"exit"}`)
		done := make(chan struct{})
		close(done)

		w := &recordingFrameWriter{}
		if err := streamSessionHostFrames(context.Background(), w, done, frames, "attach-1"); err != nil {
			t.Fatalf("stream: %v", err)
		}
		if !w.ended {
			t.Fatal("stream did not end")
		}
		if len(w.chunks) != 11 || w.chunks[10] != `{"type":"exit"}` {
			t.Fatalf("iteration %d: wrote %d chunks (%v), want all 11 ending in exit", i, len(w.chunks), w.chunks)
		}
	}
}
