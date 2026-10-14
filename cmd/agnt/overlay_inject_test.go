package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// The daemon delivers `agnt session send`, the browser's session send and the
// incident pipeline's PTY hint as a plain-text POST to /inject. The route must
// exist on the overlay's mux: without it every one of those sends was a 404.
func TestOverlayInjectRouteTypesMessage(t *testing.T) {
	w := &recordingWriter{}
	o, clk := makeTestOverlay(w)

	mux := http.NewServeMux()
	mux.HandleFunc("/inject", o.handleInject)

	done := make(chan int)
	go func() {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/inject", strings.NewReader("check the build\n")))
		done <- rec.Code
	}()
	// No scanner is wired, so the fallback types the message and submits it;
	// release its Enter retries.
	for range testRetryDelays {
		clk.awaitRetry(t).fire()
	}
	if code := <-done; code != http.StatusOK {
		t.Fatalf("/inject: got %d, want 200", code)
	}
	if got := string(w.allBytes()); !strings.HasPrefix(got, "check the build\r") {
		t.Fatalf("PTY got %q, want the message followed by Enter", got)
	}
}

func TestOverlayInjectRejectsEmptyAndGet(t *testing.T) {
	o, _ := makeTestOverlay(&recordingWriter{})
	for _, tc := range []struct {
		method, body string
		want         int
	}{
		{http.MethodGet, "", http.StatusMethodNotAllowed},
		{http.MethodPost, "  \n", http.StatusBadRequest},
	} {
		rec := httptest.NewRecorder()
		o.handleInject(rec, httptest.NewRequest(tc.method, "/inject", strings.NewReader(tc.body)))
		if rec.Code != tc.want {
			t.Errorf("%s %q: got %d, want %d", tc.method, tc.body, rec.Code, tc.want)
		}
	}
}

// /type must answer before the Enter retries finish: the scheduler gives a
// delivery 5s and retypes the message when that runs out.
func TestOverlayTypeAnswersBeforeEnterRetries(t *testing.T) {
	w := &recordingWriter{}
	o, clk := makeTestOverlay(w)

	rec := httptest.NewRecorder()
	o.handleType(rec, httptest.NewRequest(http.MethodPost, "/type",
		strings.NewReader(`{"text":"hi","enter":true,"instant":true}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("/type: got %d", rec.Code)
	}
	// The handler returned while typeText is still parked on its first retry.
	clk.awaitRetry(t).fire()
	clk.awaitRetry(t).fire()
	clk.awaitRetry(t).fire()
}

// Two injections must not interleave: the second one's text may only start
// after the first one's Enter retries are over.
func TestTypeTextSerializesInjections(t *testing.T) {
	w := &recordingWriter{}
	o, clk := makeTestOverlay(w)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		o.typeText(TypeMessage{Text: "first", Enter: true, Instant: true})
	}()
	first := clk.awaitRetry(t) // first is parked in its retry window

	wg.Add(1)
	go func() {
		defer wg.Done()
		o.typeText(TypeMessage{Text: "second", Enter: true, Instant: true})
	}()
	time.Sleep(50 * time.Millisecond) // give "second" the chance to write if unserialized
	if got := string(w.allBytes()); strings.Contains(got, "second") {
		t.Fatalf("second injection wrote during the first's retry window: %q", got)
	}

	first.fire()
	clk.awaitRetry(t).fire()
	clk.awaitRetry(t).fire()
	for range testRetryDelays {
		clk.awaitRetry(t).fire()
	}
	wg.Wait()
	got := string(w.allBytes())
	if i, j := strings.Index(got, "first"), strings.Index(got, "second"); i < 0 || j < i {
		t.Fatalf("injections out of order: %q", got)
	}
}

// screenProbe simulates the agent's input box: the typed text shows near the
// cursor until the Enter that submits it, which is the submitAt-th Enter
// (0 = never submitted).
func screenProbe(w *recordingWriter, submitAt int) func(string) (bool, bool) {
	return func(string) (bool, bool) {
		enters := w.countEnters()
		submitted := submitAt > 0 && enters >= submitAt
		return !submitted, true
	}
}

// An agent that accepts a message and works without printing gets no stray
// Enters: once the message left the input box, the retries stop.
func TestEnterRetries_StopWhenMessageLeftInput(t *testing.T) {
	w := &recordingWriter{}
	o, _ := makeTestOverlay(w)
	o.SetInputProbe(screenProbe(w, 1))

	done := make(chan struct{})
	go func() {
		defer close(done)
		o.typeText(TypeMessage{Text: "check the build", Enter: true, Instant: true})
	}()
	awaitDone(t, done) // no retry wait is ever reached
	if n := w.countEnters(); n != 1 {
		t.Fatalf("got %d Enters, want 1: the message was accepted silently", n)
	}
}

// A message still in the input box after an Enter (the agent took it as a
// newline) is retried as before.
func TestEnterRetries_ContinueWhileMessageInInput(t *testing.T) {
	w := &recordingWriter{}
	o, clk := makeTestOverlay(w)
	o.SetInputProbe(screenProbe(w, 3))

	done := make(chan struct{})
	go func() {
		defer close(done)
		o.typeText(TypeMessage{Text: "check the build", Enter: true, Instant: true})
	}()
	clk.awaitRetry(t).fire() // second Enter
	clk.awaitRetry(t).fire() // third Enter submits
	awaitDone(t, done)
	if n := w.countEnters(); n != 3 {
		t.Fatalf("got %d Enters, want 3: retries until the message left the input", n)
	}
}

// When the screen never showed the message (a paste the agent folded into a
// placeholder), silence is all there is: every retry is still sent.
func TestEnterRetries_UnseenMessageKeepsSilenceRule(t *testing.T) {
	w := &recordingWriter{}
	o, clk := makeTestOverlay(w)
	o.SetInputProbe(func(string) (bool, bool) { return false, true })

	done := make(chan struct{})
	go func() {
		defer close(done)
		o.typeText(TypeMessage{Text: "check the build", Enter: true, Instant: true})
	}()
	for range testRetryDelays {
		clk.awaitRetry(t).fire()
	}
	awaitDone(t, done)
	if n := w.countEnters(); n != 4 {
		t.Fatalf("got %d Enters, want 4", n)
	}
}

func TestLatestSenderKeepsNewestAndOrder(t *testing.T) {
	var s latestSender
	release := make(chan struct{})
	inFlight := make(chan struct{})
	var mu sync.Mutex
	var got []int
	record := func(n int) func() {
		return func() {
			if n == 0 {
				close(inFlight)
				<-release // the first send blocks, like a slow daemon
			}
			mu.Lock()
			got = append(got, n)
			mu.Unlock()
		}
	}
	start := time.Now()
	s.Send(record(0))
	<-inFlight
	for i := 1; i <= 5; i++ {
		s.Send(record(i))
	}
	if time.Since(start) > time.Second {
		t.Fatal("Send blocked behind a slow send")
	}
	close(release)
	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		n := len(got)
		mu.Unlock()
		if n == 2 || time.Now().After(deadline) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 2 || got[0] != 0 || got[1] != 5 {
		t.Fatalf("want [0 5] (in-flight, then newest only), got %v", got)
	}
}
