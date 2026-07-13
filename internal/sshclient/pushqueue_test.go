package sshclient

import (
	"bytes"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pkg/sftp"
)

func TestPushQueueReconnectFlushesFIFOAndOpensSFTPLazily(t *testing.T) {
	root, initial := newSFTPFixture(t)
	var orderMu sync.Mutex
	var order []string
	q := NewPushQueue(root, 4, func(remotePath string, _ int64) error {
		orderMu.Lock()
		order = append(order, remotePath)
		orderMu.Unlock()
		return nil
	}, nil)
	q.SetSFTP(initial)
	q.Reconnecting()

	type result struct {
		path string
		err  error
	}
	results := make([]chan result, 3)
	for i, name := range []string{"one.txt", "two.txt", "three.txt"} {
		results[i] = make(chan result, 1)
		go func(i int, name string) {
			path, err := q.Push(name, "", bytes.NewBufferString(name))
			results[i] <- result{path, err}
		}(i, name)
		waitForPushQueueDepth(t, q, i+1)
	}

	var mu sync.Mutex
	opened := 0
	_, replacement := newSFTPFixture(t)
	q.Connected(func() (*sftp.Client, error) {
		mu.Lock()
		opened++
		mu.Unlock()
		return replacement, nil
	})

	for i := range results {
		select {
		case got := <-results[i]:
			if got.err != nil {
				t.Fatalf("push %d: %v", i, got.err)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("push %d did not flush", i)
		}
	}
	mu.Lock()
	if opened != 1 {
		t.Fatalf("SFTP factory opened %d times, want once", opened)
	}
	mu.Unlock()
	orderMu.Lock()
	gotOrder := append([]string(nil), order...)
	orderMu.Unlock()
	for i := range gotOrder {
		gotOrder[i] = strings.TrimSuffix(gotOrder[i], ".txt")
		if at := strings.LastIndex(gotOrder[i], "/"); at >= 0 {
			gotOrder[i] = gotOrder[i][at+1:]
		}
	}
	if strings.Join(gotOrder, ",") != "one,two,three" {
		t.Fatalf("flush order = %v, want FIFO", gotOrder)
	}
}

func TestPushQueueReconnectDoesNotOpenSFTPUntilPush(t *testing.T) {
	q := NewPushQueue("/project", 2, nil, nil)
	opened := make(chan struct{}, 1)
	q.Reconnecting()
	q.Connected(func() (*sftp.Client, error) {
		opened <- struct{}{}
		return nil, errors.New("fixture open")
	})
	select {
	case <-opened:
		t.Fatal("SFTP opened eagerly during Connected")
	default:
	}
}

func TestPushQueueLifecycleSignalsQueueAndDrainWithoutPolling(t *testing.T) {
	q := NewPushQueue("/project", 2, nil, nil)
	q.Reconnecting()
	queued := q.Queued()
	drained := q.Drained()
	result := make(chan error, 1)
	go func() {
		_, err := q.Push("event.txt", "", strings.NewReader("event"))
		result <- err
	}()
	select {
	case <-queued:
	case <-time.After(time.Second):
		t.Fatal("queued event did not fire")
	}
	if q.Depth() != 1 {
		t.Fatalf("depth after queued event = %d, want 1", q.Depth())
	}
	q.Connected(func() (*sftp.Client, error) { return nil, errors.New("fixture flush") })
	select {
	case <-drained:
	case <-time.After(time.Second):
		t.Fatal("drained event did not fire")
	}
	if q.Depth() != 0 {
		t.Fatalf("depth after drained event = %d, want 0", q.Depth())
	}
	if err := <-result; err == nil || !strings.Contains(err.Error(), "fixture flush") {
		t.Fatalf("queued result = %v", err)
	}
	firstDrained := drained
	q.Reconnecting()
	secondDrained := q.Drained()
	if firstDrained == secondDrained {
		t.Fatal("reconnect generation reused stale drained signal")
	}
	q.Connected(func() (*sftp.Client, error) { return nil, errors.New("unused") })
	select {
	case <-secondDrained:
	case <-time.After(time.Second):
		t.Fatal("empty second generation did not drain")
	}
}

func TestPushQueueOverflowFailsLoudAndEmitsEvent(t *testing.T) {
	events := make(chan string, 1)
	q := NewPushQueue("/project", 1, nil, func(msg string) { events <- msg })
	q.Reconnecting()

	first := make(chan error, 1)
	go func() {
		_, err := q.Push("one.txt", "", bytes.NewBufferString("one"))
		first <- err
	}()
	waitForPushQueueDepth(t, q, 1)

	_, err := q.Push("two.txt", "", bytes.NewBufferString("two"))
	if !errors.Is(err, ErrPushQueueFull) || !strings.Contains(err.Error(), "two.txt") {
		t.Fatalf("overflow error = %v, want loud ErrPushQueueFull naming file", err)
	}
	select {
	case msg := <-events:
		if !strings.Contains(msg, "two.txt") || !strings.Contains(msg, "queue full") {
			t.Fatalf("event = %q, want file and queue-full detail", msg)
		}
	case <-time.After(time.Second):
		t.Fatal("overflow emitted no visible event")
	}
	q.Close()
	if err := <-first; !errors.Is(err, ErrPushQueueClosed) {
		t.Fatalf("queued caller after close = %v", err)
	}
}

func waitForPushQueueDepth(t *testing.T, q *PushQueue, want int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if q.Depth() == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("queue depth = %d, want %d", q.Depth(), want)
}
