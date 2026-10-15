package overlay

import (
	"bytes"
	"fmt"
	"io"
	"runtime"
	"strings"
	"testing"
	"time"
)

// currentGoroutineID parses this goroutine's id from its stack header.
func currentGoroutineID() string {
	buf := make([]byte, 64)
	buf = buf[:runtime.Stack(buf, false)]
	var id int
	if _, err := fmt.Sscanf(string(buf), "goroutine %d ", &id); err != nil {
		return ""
	}
	return fmt.Sprint(id)
}

// producersOf counts the goroutines Run started from goroutine runner.
func producersOf(runner string) int {
	buf := make([]byte, 1<<20)
	buf = buf[:runtime.Stack(buf, true)]
	return strings.Count(string(buf), "(*InputRouter).Run in goroutine "+runner+"\n")
}

// After Stop, Run is gone but its producer goroutine is still delivering what
// the input source yields. It must give up once the router is stopped: a
// blind send into the 16-byte channel blocks forever once nobody reads it,
// leaking the goroutine and pinning the scanner's reader with it.
func TestStoppedRouterReleasesItsProducer(t *testing.T) {
	rec := &writeRecorder{}
	ov := New(rec, 80, 24, DefaultConfig())
	ov.renderer = NewRenderer(&bytes.Buffer{}, 80, 24)
	ov.state.Store(int32(StateHidden))
	pr, pw := io.Pipe()
	router := NewInputRouter(rec, ov)
	router.input = pr

	runnerID := make(chan string, 1)
	returned := make(chan struct{})
	go func() {
		runnerID <- currentGoroutineID()
		_ = router.Run()
		close(returned)
	}()
	runner := <-runnerID
	if runner == "" {
		t.Fatal("could not read the runner goroutine id")
	}
	deadline := time.Now().Add(5 * time.Second)
	for producersOf(runner) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}

	router.Stop()
	select {
	case <-returned:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after Stop")
	}

	// More input than the channel holds arrives after the router stopped.
	if _, err := pw.Write(bytes.Repeat([]byte("a"), 64)); err != nil {
		t.Fatalf("write: %v", err)
	}
	_ = pw.Close()

	deadline = time.Now().Add(5 * time.Second)
	for producersOf(runner) != 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if n := producersOf(runner); n != 0 {
		t.Fatalf("%d producer goroutine(s) still alive after Stop and EOF; the input send never gave up", n)
	}
}
