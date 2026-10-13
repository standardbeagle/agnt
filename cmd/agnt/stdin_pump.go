package main

import (
	"io"
	"os"
	"sync"
	"sync/atomic"
)

// stdinPump is the single reader of a blocking input stream (os.Stdin) for the
// whole process. Consumers do not read the stream; they take a lease and read
// from that. A lease can be closed — its blocked Read returns io.EOF at once —
// which a Read on os.Stdin itself cannot do.
//
// Two bugs came from readers that could not be ended:
//
//   - `agnt run --no-overlay` waited for a keypress after the agent exited,
//     because teardown joined a goroutine parked in os.Stdin.Read.
//   - The first-run setup → relaunch flow starts two PTY phases in one process.
//     Phase 1's input router stopped, but its reader stayed parked in Read and
//     took the first keystrokes of phase 2.
//
// With the pump there is exactly one goroutine in os.Stdin.Read for the life
// of the process. Bytes that arrive while no lease is open (between phases)
// are held and delivered to the next lease, not lost.
type stdinPump struct {
	src   io.Reader
	limit int // max bytes held while nobody is reading

	start sync.Once
	mu    sync.Mutex
	cond  *sync.Cond
	buf   []byte
	err   error  // terminal read error from src; sticky
	gen   uint64 // generation of the live lease; 0 = none
}

// stdinPumpLimit bounds the bytes buffered while no lease is consuming. Past
// it the pump stops reading, leaving further input in the kernel's tty buffer.
const stdinPumpLimit = 64 * 1024

func newStdinPump(src io.Reader) *stdinPump {
	p := &stdinPump{src: src, limit: stdinPumpLimit}
	p.cond = sync.NewCond(&p.mu)
	return p
}

// processStdin is the process-wide pump over os.Stdin. Its read goroutine
// starts on the first Acquire, so commands that never lease stdin never touch
// it.
var processStdin = newStdinPump(os.Stdin)

func (p *stdinPump) run() {
	for {
		chunk := make([]byte, 256)
		n, err := p.src.Read(chunk)

		p.mu.Lock()
		// Backpressure: with a full buffer and no one draining it, wait
		// rather than grow without bound.
		for n > 0 && len(p.buf) > 0 && len(p.buf)+n > p.limit {
			p.cond.Wait()
		}
		p.buf = append(p.buf, chunk[:n]...)
		if err != nil {
			p.err = err
		}
		p.cond.Broadcast()
		p.mu.Unlock()

		if err != nil {
			return
		}
	}
}

// Acquire opens a lease and makes it the pump's only consumer. Any earlier
// lease still open is ended (its Read returns io.EOF): the stream has one
// owner at a time.
func (p *stdinPump) Acquire() *stdinLease {
	p.start.Do(func() { go p.run() })
	p.mu.Lock()
	defer p.mu.Unlock()
	p.gen++
	p.cond.Broadcast()
	return &stdinLease{p: p, gen: p.gen}
}

// stdinLease is one consumer's view of the pump. It is an io.ReadCloser.
type stdinLease struct {
	p      *stdinPump
	gen    uint64
	closed atomic.Bool
}

// Read blocks until input is available, the lease is closed or superseded
// (io.EOF), or the underlying stream ends (its error, after buffered bytes).
func (l *stdinLease) Read(b []byte) (int, error) {
	if len(b) == 0 {
		return 0, nil
	}
	p := l.p
	p.mu.Lock()
	defer p.mu.Unlock()
	for {
		if l.closed.Load() || l.gen != p.gen {
			return 0, io.EOF
		}
		if len(p.buf) > 0 {
			n := copy(b, p.buf)
			p.buf = p.buf[n:]
			if len(p.buf) == 0 {
				p.buf = nil // release the backing array
			}
			p.cond.Broadcast() // room for the pump
			return n, nil
		}
		if p.err != nil {
			return 0, p.err
		}
		p.cond.Wait()
	}
}

// Close ends the lease, waking a blocked Read with io.EOF. Unread bytes stay
// in the pump for the next lease. Idempotent.
func (l *stdinLease) Close() error {
	if l.closed.Swap(true) {
		return nil
	}
	l.p.mu.Lock()
	l.p.cond.Broadcast()
	l.p.mu.Unlock()
	return nil
}

// stdinModal is the modal-capture half of a stdin owner: a startup prompt
// borrows the input stream through it instead of opening a second reader on
// os.Stdin. Both stdin owners implement it — the overlay input router and
// rawStdinRelay.
type stdinModal interface {
	BeginModalCapture() <-chan byte
	EndModalCapture()
}

// rawStdinRelay is the stdin owner for `agnt run --no-overlay`: a plain
// stdin → PTY copy that can lend the stream to a modal prompt. Without the
// modal hook the port-conflict prompt read os.Stdin itself and raced this
// copy for the answer keystroke, which could land in the agent instead.
type rawStdinRelay struct {
	in    io.Reader
	out   io.Writer
	modal atomic.Pointer[chan byte]
}

func newRawStdinRelay(in io.Reader, out io.Writer) *rawStdinRelay {
	return &rawStdinRelay{in: in, out: out}
}

// BeginModalCapture diverts subsequent input bytes to the returned channel
// until EndModalCapture is called.
func (r *rawStdinRelay) BeginModalCapture() <-chan byte {
	ch := make(chan byte, 16)
	r.modal.Store(&ch)
	return ch
}

// EndModalCapture restores the plain copy.
func (r *rawStdinRelay) EndModalCapture() {
	r.modal.Store(nil)
}

// Run copies in to out until in ends or out rejects a write.
func (r *rawStdinRelay) Run() {
	buf := make([]byte, 4096)
	for {
		n, err := r.in.Read(buf)
		if n > 0 {
			if mc := r.modal.Load(); mc != nil {
				// A prompt owns input. Like the router, drop what its
				// channel cannot take rather than leak it to the child.
				for _, b := range buf[:n] {
					select {
					case *mc <- b:
					default:
					}
				}
			} else if _, werr := r.out.Write(buf[:n]); werr != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}
