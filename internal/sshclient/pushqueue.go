package sshclient

import (
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/pkg/sftp"
)

var (
	ErrPushQueueFull   = errors.New("sshclient: reconnect push queue full")
	ErrPushQueueClosed = errors.New("sshclient: reconnect push queue closed")
)

type queuedPush struct {
	fileName    string
	destRelPath string
	body        []byte
	result      chan pushResult
}

type pushResult struct {
	remotePath string
	err        error
}

// PushQueue keeps the local control surface alive while SSH reconnects.
// Calls accepted in that interval wait in a bounded FIFO and complete only
// after the replacement transport reaches CONNECTED. The replacement SFTP
// subsystem is represented by a factory and is deliberately opened by the
// first push that needs it, never by Connected itself.
type PushQueue struct {
	mu          sync.Mutex
	opMu        sync.Mutex
	projectRoot string
	cap         int
	notify      FileArrivalNotifier
	report      func(string)
	state       pushQueueState
	pending     []*queuedPush
	sftp        *sftp.Client
	openSFTP    func() (*sftp.Client, error)
	flushActive bool
	drained     chan struct{}
	queued      chan struct{}
}

type pushQueueState uint8

const (
	pushQueueConnected pushQueueState = iota
	pushQueueReconnecting
	pushQueueFlushing
	pushQueueClosed
)

func NewPushQueue(projectRoot string, capacity int, notify FileArrivalNotifier, report func(string)) *PushQueue {
	if capacity < 1 {
		capacity = 1
	}
	drained := make(chan struct{})
	close(drained)
	return &PushQueue{projectRoot: projectRoot, cap: capacity, notify: notify, report: report, state: pushQueueConnected, drained: drained, queued: make(chan struct{})}
}

func (q *PushQueue) SetSFTP(sc *sftp.Client) {
	q.mu.Lock()
	q.sftp = sc
	q.openSFTP = nil
	q.state = pushQueueConnected
	q.mu.Unlock()
}

func (q *PushQueue) Reconnecting() {
	q.opMu.Lock()
	q.mu.Lock()
	old := q.sftp
	q.sftp = nil
	q.openSFTP = nil
	if q.state != pushQueueClosed {
		q.state = pushQueueReconnecting
		q.resetDrainedLocked()
		q.queued = make(chan struct{})
	}
	q.mu.Unlock()
	if old != nil {
		_ = old.Close()
	}
	q.opMu.Unlock()
}

// Connected installs a lazy factory. If pushes arrived during reconnect,
// their FIFO flush starts now and the first item opens SFTP; with an empty
// queue this method performs no transport work at all.
func (q *PushQueue) Connected(open func() (*sftp.Client, error)) {
	q.mu.Lock()
	if q.state == pushQueueClosed {
		q.mu.Unlock()
		return
	}
	q.openSFTP = open
	if len(q.pending) == 0 {
		q.state = pushQueueConnected
		q.closeDrainedLocked()
		q.mu.Unlock()
		return
	}
	q.state = pushQueueFlushing
	if q.flushActive {
		q.mu.Unlock()
		return
	}
	q.flushActive = true
	q.mu.Unlock()
	go q.flush()
}

func (q *PushQueue) Push(fileName, destRelPath string, src io.Reader) (string, error) {
	body, err := io.ReadAll(src)
	if err != nil {
		return "", fmt.Errorf("sshclient: queueing push %s: %w", fileName, err)
	}
	item := &queuedPush{fileName: fileName, destRelPath: destRelPath, body: body, result: make(chan pushResult, 1)}

	q.mu.Lock()
	switch q.state {
	case pushQueueClosed:
		q.mu.Unlock()
		return "", ErrPushQueueClosed
	case pushQueueReconnecting, pushQueueFlushing:
		if len(q.pending) >= q.cap {
			msg := fmt.Sprintf("agnt ssh: push %q dropped: reconnect queue full (capacity %d)", fileName, q.cap)
			report := q.report
			q.mu.Unlock()
			if report != nil {
				report(msg)
			}
			return "", fmt.Errorf("%w: %s", ErrPushQueueFull, msg)
		}
		q.pending = append(q.pending, item)
		select {
		case <-q.queued:
		default:
			close(q.queued)
		}
		q.mu.Unlock()
		result := <-item.result
		return result.remotePath, result.err
	case pushQueueConnected:
		q.mu.Unlock()
		return q.execute(item)
	default:
		q.mu.Unlock()
		return "", ErrPushQueueClosed
	}
}

func (q *PushQueue) flush() {
	for {
		q.mu.Lock()
		if q.state == pushQueueClosed || q.state == pushQueueReconnecting {
			q.flushActive = false
			q.mu.Unlock()
			return
		}
		if len(q.pending) == 0 {
			q.state = pushQueueConnected
			q.flushActive = false
			q.closeDrainedLocked()
			q.mu.Unlock()
			return
		}
		item := q.pending[0]
		q.pending = q.pending[1:]
		q.mu.Unlock()

		path, err := q.execute(item)
		item.result <- pushResult{remotePath: path, err: err}
	}
}

func (q *PushQueue) execute(item *queuedPush) (string, error) {
	q.opMu.Lock()
	defer q.opMu.Unlock()

	q.mu.Lock()
	sc := q.sftp
	open := q.openSFTP
	q.mu.Unlock()
	if sc == nil {
		if open == nil {
			return "", errors.New("sshclient: push transport unavailable")
		}
		var err error
		sc, err = open()
		if err != nil {
			return "", fmt.Errorf("sshclient: reopening SFTP after reconnect: %w", err)
		}
		q.mu.Lock()
		if q.state == pushQueueClosed || q.state == pushQueueReconnecting {
			q.mu.Unlock()
			_ = sc.Close()
			return "", errors.New("sshclient: push transport changed while reopening SFTP")
		}
		q.sftp = sc
		q.mu.Unlock()
	}

	remotePath, err := PushToInbox(sc, q.projectRoot, item.destRelPath, item.fileName, bytesReader(item.body))
	if err != nil {
		return "", err
	}
	if q.notify != nil {
		if err := q.notify(remotePath, int64(len(item.body))); err != nil {
			return remotePath, fmt.Errorf("upload completed at %s but agent notice failed: %w", remotePath, err)
		}
	}
	return remotePath, nil
}

func bytesReader(body []byte) io.Reader { return &byteReader{body: body} }

type byteReader struct{ body []byte }

func (r *byteReader) Read(p []byte) (int, error) {
	if len(r.body) == 0 {
		return 0, io.EOF
	}
	n := copy(p, r.body)
	r.body = r.body[n:]
	return n, nil
}

func (q *PushQueue) Depth() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.pending)
}

// Drained closes after every push accepted during the current reconnect has
// completed. Reconnecting starts a fresh generation before callers enqueue.
func (q *PushQueue) Drained() <-chan struct{} {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.drained
}

// Queued closes when the current reconnect generation accepts its first push.
func (q *PushQueue) Queued() <-chan struct{} {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.queued
}

func (q *PushQueue) resetDrainedLocked() {
	select {
	case <-q.drained:
		q.drained = make(chan struct{})
	default:
	}
}

func (q *PushQueue) closeDrainedLocked() {
	select {
	case <-q.drained:
	default:
		close(q.drained)
	}
}

func (q *PushQueue) ProjectRoot() string { return q.projectRoot }

func (q *PushQueue) Close() {
	q.opMu.Lock()
	q.mu.Lock()
	if q.state == pushQueueClosed {
		q.mu.Unlock()
		q.opMu.Unlock()
		return
	}
	q.state = pushQueueClosed
	q.closeDrainedLocked()
	pending := q.pending
	q.pending = nil
	sc := q.sftp
	q.sftp = nil
	q.mu.Unlock()
	if sc != nil {
		_ = sc.Close()
	}
	q.opMu.Unlock()
	for _, item := range pending {
		item.result <- pushResult{err: ErrPushQueueClosed}
	}
}
