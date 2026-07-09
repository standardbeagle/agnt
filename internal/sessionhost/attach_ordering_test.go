package sessionhost

import (
	"encoding/base64"
	"encoding/json"
	goprocess "github.com/standardbeagle/go-cli-server/process"
	"strings"
	"sync"
	"testing"
	"time"
)

// decodeFrames drains ch until it is empty, returning the frame types in order
// and the concatenated stdout bytes.
func decodeFrames(t *testing.T, ch <-chan []byte, timeout time.Duration) (types []string, stdout string) {
	t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case payload, ok := <-ch:
			if !ok {
				return types, stdout
			}
			var f Frame
			if err := json.Unmarshal(payload, &f); err != nil {
				t.Fatalf("bad frame: %v", err)
			}
			types = append(types, f.Type)
			if f.Type == "stdout" {
				var b64 string
				if err := json.Unmarshal(f.Data, &b64); err != nil {
					t.Fatalf("bad stdout data: %v", err)
				}
				raw, err := base64.StdEncoding.DecodeString(b64)
				if err != nil {
					t.Fatalf("bad base64: %v", err)
				}
				stdout += string(raw)
			}
		case <-deadline:
			return types, stdout
		}
	}
}

// The replay-marker must be the first frame a subscriber ever sees. The client
// clears its screen on the marker, so any live frame delivered ahead of it is
// erased — the output is gone for good.
//
// Attach used to publish the subscriber before enqueuing the marker, leaving a
// window in which the producer could deliver live stdout first.
func TestAttach_ReplayMarkerPrecedesAnyLiveFrame(t *testing.T) {
	s := &Session{
		subs:       make(map[string]*subscriber),
		scrollback: goprocess.NewRingBuffer(64 * 1024),
		doneCh:     make(chan struct{}),
	}

	// A producer hammering the fan-out, as a chatty PTY child does.
	stop := make(chan struct{})
	var producer sync.WaitGroup
	producer.Add(1)
	go func() {
		defer producer.Done()
		for {
			select {
			case <-stop:
				return
			default:
				s.broadcast([]byte("x"))
			}
		}
	}()
	defer func() {
		close(stop)
		producer.Wait()
	}()

	// Attach repeatedly against the live producer; every attach must lead with
	// the marker.
	for i := 0; i < 200; i++ {
		ch, id, _ := s.Attach(64)
		select {
		case payload := <-ch:
			var f Frame
			if err := json.Unmarshal(payload, &f); err != nil {
				t.Fatalf("bad frame: %v", err)
			}
			if f.Type != "replay-marker" {
				t.Fatalf("attach %d: first frame = %q, want replay-marker (live output raced ahead of it)", i, f.Type)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("attach %d: no frame delivered", i)
		}
		s.Detach(id)
	}
}

// Attach must not block. The producer drops when a subscriber's channel is full,
// so a subscriber published before its marker was enqueued could have its buffer
// filled by the producer — and Attach's blocking send would then wait forever,
// since nothing drains the channel until Attach returns it.
func TestAttach_DoesNotBlockAgainstAFloodingProducer(t *testing.T) {
	s := &Session{
		subs:       make(map[string]*subscriber),
		scrollback: goprocess.NewRingBuffer(64 * 1024),
		doneCh:     make(chan struct{}),
	}

	stop := make(chan struct{})
	var producer sync.WaitGroup
	producer.Add(1)
	go func() {
		defer producer.Done()
		chunk := []byte(strings.Repeat("y", 256))
		for {
			select {
			case <-stop:
				return
			default:
				s.broadcast(chunk)
			}
		}
	}()

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 100; i++ {
			// A tiny buffer makes the old window trivially reachable.
			_, id, _ := s.Attach(2)
			s.Detach(id)
		}
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		close(stop)
		producer.Wait()
		t.Fatal("Attach blocked: its buffer filled before the marker was enqueued")
	}
	close(stop)
	producer.Wait()
}

// The snapshot handed to a new subscriber must contain exactly the output
// broadcast before it attached — no chunk both replayed and delivered live
// (duplicated), none skipped.
func TestAttach_SnapshotAndLiveStreamDoNotOverlap(t *testing.T) {
	s := &Session{
		subs:       make(map[string]*subscriber),
		scrollback: goprocess.NewRingBuffer(64 * 1024),
		doneCh:     make(chan struct{}),
	}

	for i := 0; i < 10; i++ {
		s.broadcast([]byte("A"))
	}

	ch, id, _ := s.Attach(64)
	defer s.Detach(id)

	for i := 0; i < 5; i++ {
		s.broadcast([]byte("B"))
	}

	types, stdout := decodeFrames(t, ch, 2*time.Second)
	if len(types) == 0 || types[0] != "replay-marker" {
		t.Fatalf("frames = %v, want replay-marker first", types)
	}
	if want := strings.Repeat("A", 10) + strings.Repeat("B", 5); stdout != want {
		t.Errorf("stream = %q, want %q (a chunk was duplicated or dropped)", stdout, want)
	}
}
