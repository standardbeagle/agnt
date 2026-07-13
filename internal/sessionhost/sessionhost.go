// Package sessionhost implements daemon-owned, detachable PTY sessions
// (the "session-host" flavor described in
// docs/superpowers/specs/2026-07-03-remote-ssh-design.md §1-2). Unlike a
// classic `agnt run` session, the PTY child here is spawned and owned by
// the daemon process itself, so it survives the attaching client
// disconnecting (SSH drop, terminal close, laptop sleep).
//
// Concurrency mirrors the ProcessManager conventions used elsewhere in this
// codebase: a sync.Map registry, atomic state fields, and a single mutex
// reserved for the PTY resize/primary-attach arbitration that cannot be made
// lock-free without losing correctness.
package sessionhost

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"

	"github.com/creack/pty"
	"github.com/standardbeagle/agnt/internal/scope"
	goprocess "github.com/standardbeagle/go-cli-server/process"
)

// DefaultScrollback is the default per-session ring buffer capacity (1MB),
// per spec §1.4. Sized 4x the existing 256KB MaxOutputBuffer default because
// a session-host ring holds multiplexed interactive output (agent + human +
// tool output), not one process's stdout/stderr.
const DefaultScrollback = 1024 * 1024

// exitDrainGrace bounds how long waitLoop lets readLoop drain after the direct
// child exits. A background descendant can retain the PTY slave indefinitely;
// after this grace we close the master to unblock the reader and publish the
// leader's terminal state.
const exitDrainGrace = 2 * time.Second

// Status is the lifecycle state of a session-host session.
type Status string

const (
	StatusRunning Status = "running"
	StatusExited  Status = "exited"
)

// Frame is the wire envelope for SESSION-HOST ATTACH, one JSON object per
// WriteChunk/inbound line, discriminated by Type. See spec §1.3.
type Frame struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data,omitempty"`
}

// ResizeData is the payload of a "resize" frame.
type ResizeData struct {
	Cols int `json:"cols"`
	Rows int `json:"rows"`
}

// ExitData is the payload of an "exit" frame.
type ExitData struct {
	Code int `json:"code"`
}

// ReplayMarkerData is the payload of the one-time "replay-marker" frame.
type ReplayMarkerData struct {
	Truncated bool `json:"truncated"`
}

// CreateConfig configures a new session-host session. Mirrors
// SessionHostCreateConfig in the design spec.
type CreateConfig struct {
	Name        string
	ProjectPath string
	Command     string
	Args        []string
	Cols        int
	Rows        int
	Env         map[string]string
	// ScrollbackSize overrides DefaultScrollback; <=0 uses the default.
	ScrollbackSize int
}

// subscriber is one attached client's live-output fan-out channel. Buffered
// so a slow attach reader cannot block the single PTY-reader goroutine
// (mirrors the Pinger "never blocks delivery" contract in the incident
// pipeline).
type subscriber struct {
	id string
	ch chan []byte

	// desynced is set when a broadcast was dropped because ch was full. A
	// dropped stdout chunk permanently corrupts this subscriber's terminal
	// byte stream, so instead of silently continuing we re-sync it with a
	// fresh scrollback snapshot (replay-marker + full snapshot) as soon as ch
	// has room again — see broadcastFrame / trySendResync.
	desynced atomic.Bool
}

// Session is a single daemon-owned PTY child plus its scrollback ring and
// attached-client fan-out set.
type Session struct {
	ID          string
	Name        string
	ProjectPath string
	Command     string
	Args        []string
	StartedAt   time.Time

	// SessionPGID is the PTY child's own pgid (== PID, since creack/pty's
	// pty.Start sets Setsid on the child). Same containment invariant as
	// classic sessions, captured directly instead of over the wire — see
	// spec §2.2 invariant 9.
	SessionPGID int

	cmd  *exec.Cmd
	ptmx *os.File

	scrollback *goprocess.RingBuffer

	status   atomic.Value // Status
	exitCode atomic.Int32

	subsMu    sync.RWMutex
	subs      map[string]*subscriber
	primaryID atomic.Value // string; "" means no primary yet

	lastAttached atomic.Value // time.Time

	beforeSetSize func() // deterministic test seam inside the fd control callback

	doneCh    chan struct{}
	readDone  chan struct{}
	closeOnce sync.Once
	ptmxOnce  sync.Once

	nextAttachID atomic.Int64
}

// Create spawns a new daemon-owned PTY child and starts its output-broadcast
// reader. The returned Session is already registered for output capture but
// has zero attached clients until Attach is called.
func Create(cfg CreateConfig) (*Session, error) {
	if cfg.Command == "" {
		return nil, fmt.Errorf("sessionhost: command is required")
	}
	cols, rows := cfg.Cols, cfg.Rows
	if cols <= 0 {
		cols = 80
	}
	if rows <= 0 {
		rows = 24
	}

	cmd := exec.Command(cfg.Command, cfg.Args...)
	cmd.Dir = cfg.ProjectPath
	env := os.Environ()
	for k, v := range cfg.Env {
		env = append(env, k+"="+v)
	}
	cmd.Env = env

	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
	if err != nil {
		return nil, fmt.Errorf("sessionhost: failed to start pty: %w", err)
	}

	pgid := 0
	if cmd.Process != nil {
		pgid = cmd.Process.Pid // pty.Start sets Setsid; pgid == PID.
	}

	size := cfg.ScrollbackSize
	if size <= 0 {
		size = DefaultScrollback
	}

	s := &Session{
		ID:          generateID(cfg.Name),
		Name:        cfg.Name,
		ProjectPath: cfg.ProjectPath,
		Command:     cfg.Command,
		Args:        cfg.Args,
		StartedAt:   time.Now(),
		SessionPGID: pgid,
		cmd:         cmd,
		ptmx:        ptmx,
		scrollback:  goprocess.NewRingBuffer(size),
		subs:        make(map[string]*subscriber),
		doneCh:      make(chan struct{}),
		readDone:    make(chan struct{}),
	}
	s.status.Store(StatusRunning)
	s.exitCode.Store(-1)
	s.primaryID.Store("")
	s.lastAttached.Store(time.Time{})

	go s.readLoop()
	go s.waitLoop()

	return s, nil
}

var idCounter atomic.Int64

// generateID builds a stable-ish session id from the user-facing name (or a
// generated fallback) plus a monotonic counter, avoiding collisions when the
// same name is reused after a prior session exited.
func generateID(name string) string {
	n := idCounter.Add(1)
	if name == "" {
		return fmt.Sprintf("session-host-%d", n)
	}
	return fmt.Sprintf("%s-%d", name, n)
}

// readLoop is the single reader of the PTY fd: it feeds the scrollback ring
// and fans raw bytes out to every current subscriber. Single capture point —
// no duplicate PTY reads, per spec §1.4.
func (s *Session) readLoop() {
	defer close(s.readDone)
	defer s.closePTY()
	buf := make([]byte, 32*1024)
	for {
		n, err := s.ptmx.Read(buf)
		if n > 0 {
			chunk := make([]byte, n)
			copy(chunk, buf[:n])
			s.broadcast(chunk)
		}
		if err != nil {
			return
		}
	}
}

// waitLoop reaps the child and marks the session exited once it does, so
// KILL/status queries observe OS truth rather than daemon-cached state.
func (s *Session) waitLoop() {
	err := s.cmd.Wait()
	// A child exit makes the PTY master readable until its buffered output is
	// drained, then Read returns EIO. Do not mark the session terminal (or close
	// the master) before readLoop reaches that point: under load that used to
	// discard the child's final output.
	select {
	case <-s.readDone:
	case <-time.After(exitDrainGrace):
		_ = s.closePTY()
	}
	code := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			code = exitErr.ExitCode()
		} else {
			code = -1
		}
	}
	s.exitCode.Store(int32(code))
	s.status.Store(StatusExited)

	exitData, _ := json.Marshal(ExitData{Code: code})
	s.broadcastFrame(Frame{Type: "exit", Data: exitData})

	s.closeOnce.Do(func() { close(s.doneCh) })
}

// broadcast fans a raw stdout chunk out to every subscriber as a "stdout"
// frame (base64-encoded, since PTY output is not guaranteed valid UTF-8
// mid-escape-sequence — spec §1.3).
func (s *Session) broadcast(chunk []byte) {
	data, _ := json.Marshal(base64.StdEncoding.EncodeToString(chunk))
	payload, err := json.Marshal(Frame{Type: "stdout", Data: data})
	if err != nil {
		return
	}

	// The scrollback write and the fan-out happen under the same lock that
	// Attach takes exclusively. Writing outside it let a chunk land in the
	// snapshot a concurrent Attach was taking AND be delivered live to that
	// same subscriber (duplicated), or be delivered before its replay-marker
	// (which the client honors by clearing, so the chunk is lost).
	s.subsMu.RLock()
	defer s.subsMu.RUnlock()
	s.scrollback.Write(chunk)
	s.fanOutLocked(payload, "stdout")
}

func (s *Session) broadcastFrame(f Frame) {
	payload, err := json.Marshal(f)
	if err != nil {
		return
	}
	s.subsMu.RLock()
	defer s.subsMu.RUnlock()
	s.fanOutLocked(payload, f.Type)
}

// fanOutLocked delivers payload to every subscriber. Caller holds subsMu.
func (s *Session) fanOutLocked(payload []byte, frameType string) {
	for _, sub := range s.subs {
		// A previously-desynced subscriber must be re-synced with a fresh
		// scrollback snapshot before it can accept live frames again;
		// otherwise the gap left by the earlier drop stays in its stream.
		if sub.desynced.Load() {
			if !s.trySendResync(sub) {
				// Still no room — stay desynced and skip this frame too.
				continue
			}
			sub.desynced.Store(false)
			// readLoop writes each chunk to scrollback *before* broadcasting
			// it, so the snapshot just replayed already contains this stdout
			// frame — delivering it live too would render it twice. Skip the
			// live copy for stdout; non-scrollback frames (e.g. "exit") are
			// not in the snapshot and must still be delivered.
			if frameType == "stdout" {
				continue
			}
		}
		select {
		case sub.ch <- payload:
		default:
			// Slow subscriber: drop rather than block the single PTY
			// reader (matches "Pinger never blocks delivery"), but mark it
			// desynced so we resync (not silently corrupt) once it drains.
			sub.desynced.Store(true)
		}
	}
}

// trySendResync attempts to re-sync a desynced subscriber by enqueuing a
// replay-marker followed by the full current scrollback snapshot. It only
// succeeds if both frames fit without blocking; a partial send leaves the
// subscriber desynced to retry on the next broadcast (a duplicate marker just
// clears the client screen again, which is harmless). Caller holds subsMu.
func (s *Session) trySendResync(sub *subscriber) bool {
	// Cheap pre-check: if the channel has no room, the marker send below would
	// fail anyway. Bail before taking the scrollback Snapshot (a full ring copy
	// + marshal + base64), so a persistently-full subscriber doesn't force that
	// allocation on every single stdout frame — it would otherwise throttle the
	// single PTY reader and every other subscriber while the slow one stays full.
	if len(sub.ch) >= cap(sub.ch) {
		return false
	}
	snapshot, truncated := s.scrollback.Snapshot()
	markerData, _ := json.Marshal(ReplayMarkerData{Truncated: truncated})
	marker, _ := json.Marshal(Frame{Type: "replay-marker", Data: markerData})

	select {
	case sub.ch <- marker:
	default:
		return false
	}

	if len(snapshot) > 0 {
		data, _ := json.Marshal(base64.StdEncoding.EncodeToString(snapshot))
		replay, _ := json.Marshal(Frame{Type: "stdout", Data: data})
		select {
		case sub.ch <- replay:
		default:
			// Marker sent but snapshot didn't fit; remain desynced so the
			// next broadcast retries the full resync.
			return false
		}
	}
	return true
}

// Status returns the current lifecycle status.
func (s *Session) Status() Status {
	return s.status.Load().(Status)
}

// ExitCode returns the child's exit code, or -1 if still running.
func (s *Session) ExitCode() int {
	return int(s.exitCode.Load())
}

// LastAttached returns the timestamp of the most recent Attach call, or the
// zero time if never attached.
func (s *Session) LastAttached() time.Time {
	return s.lastAttached.Load().(time.Time)
}

// Attach registers a new subscriber and returns:
//   - a channel of already-framed JSON payloads to write to the connection
//     (starts with one replay-marker frame, then the full scrollback as one
//     stdout frame, then live frames — spec §1.4 invariant 6),
//   - the attach id (used by Detach/SendInput to identify this attach),
//   - whether this attach is primary (first attach, or promoted after the
//     prior primary detached — spec §1.3 invariant 3).
//
// The caller must range over the returned channel until it closes (Detach)
// or the session exits.
func (s *Session) Attach(bufferedFrames int) (ch <-chan []byte, attachID string, isPrimary bool) {
	if bufferedFrames <= 0 {
		bufferedFrames = 64
	}
	if bufferedFrames < 2 {
		bufferedFrames = 2 // room for the replay-marker and the snapshot frame
	}
	id := fmt.Sprintf("attach-%d", s.nextAttachID.Add(1))
	sub := &subscriber{id: id, ch: make(chan []byte, bufferedFrames)}

	// Snapshot, enqueue the replay frames, and only then publish the subscriber
	// — all under the exclusive lock the producer takes to write scrollback and
	// fan out.
	//
	// Registering first, as this did, opened a window before the marker was
	// enqueued: a live frame could reach the subscriber ahead of its
	// replay-marker (the client clears on the marker, so that output is lost),
	// and on a chatty child the 64-slot buffer could fill in that window — the
	// producer only drops, so the blocking `sub.ch <- marker` below would then
	// wedge the ATTACH handler forever, since nothing drains sub.ch until Attach
	// returns it. Here the channel is empty and unseen, so neither can happen.
	s.subsMu.Lock()
	snapshot, truncated := s.scrollback.Snapshot()
	markerData, _ := json.Marshal(ReplayMarkerData{Truncated: truncated})
	marker, _ := json.Marshal(Frame{Type: "replay-marker", Data: markerData})
	sub.ch <- marker
	if len(snapshot) > 0 {
		data, _ := json.Marshal(base64.StdEncoding.EncodeToString(snapshot))
		replay, _ := json.Marshal(Frame{Type: "stdout", Data: data})
		sub.ch <- replay
	}
	s.subs[id] = sub
	s.subsMu.Unlock()

	s.lastAttached.Store(time.Now())

	// First attach ever becomes primary; static for the session's lifetime
	// per spec non-goals (no promotion UI) — but if the primary detached
	// (removed itself from primaryID), the next attach may claim it. We
	// implement "first to attach after primary slot is empty wins".
	isPrimary = s.primaryID.CompareAndSwap("", id)

	return sub.ch, id, isPrimary
}

// Detach removes an attach's subscription. If it was primary, the primary
// slot is cleared so a future attach may claim it (spec §1.3 invariant 4 —
// detach never touches the PTY child).
func (s *Session) Detach(attachID string) {
	s.subsMu.Lock()
	if sub, ok := s.subs[attachID]; ok {
		delete(s.subs, attachID)
		close(sub.ch)
	}
	s.subsMu.Unlock()

	s.primaryID.CompareAndSwap(attachID, "")
}

// IsPrimary reports whether attachID currently holds the primary (write)
// slot.
func (s *Session) IsPrimary(attachID string) bool {
	return s.primaryID.Load().(string) == attachID
}

// AttachedCount returns the number of currently attached clients, for
// `agnt session list` / `SESSION-HOST LIST` display.
func (s *Session) AttachedCount() int {
	s.subsMu.RLock()
	defer s.subsMu.RUnlock()
	return len(s.subs)
}

// WriteStdin writes raw bytes to the PTY child's stdin. Callers must check
// IsPrimary first — non-primary stdin is rejected at the protocol layer
// (spec §1.3 invariant 3), not here.
func (s *Session) WriteStdin(data []byte) error {
	_, err := s.ptmx.Write(data)
	return err
}

// Resize changes the PTY window size for all current attaches (spec §1.3
// invariant 8 — no partial re-render, client owns redraw).
func (s *Session) Resize(cols, rows int) error {
	return setPTYSize(s.ptmx, cols, rows, s.beforeSetSize)
}

// Done returns a channel that closes when the PTY child exits.
func (s *Session) Done() <-chan struct{} {
	return s.doneCh
}

// Close closes the PTY fd (does not itself kill the child — callers that
// want the containment guarantee use platform.KillSessionPGID with
// s.SessionPGID, mirroring killSessionPGID for classic sessions).
//
// Safe to call alongside a concurrent self-exit: the fd is closed once.
func (s *Session) Close() error {
	return s.closePTY()
}

// closePTY closes the PTY fd exactly once, whichever of waitLoop or Close gets
// there first. Later callers report the first close's result.
func (s *Session) closePTY() error {
	var err error
	s.ptmxOnce.Do(func() { err = s.ptmx.Close() })
	return err
}

// Registry is a lock-free (sync.Map-backed) collection of session-host
// sessions, mirroring ProcessManager's registry conventions.
type Registry struct {
	sessions sync.Map // map[string]*Session

	totalCreated atomic.Int64
	totalKilled  atomic.Int64
}

// NewRegistry creates an empty session-host registry.
func NewRegistry() *Registry {
	return &Registry{}
}

// Add registers a newly created session.
func (r *Registry) Add(s *Session) {
	r.sessions.Store(s.ID, s)
	r.totalCreated.Add(1)
}

// Get looks up a session by id.
func (r *Registry) Get(id string) (*Session, bool) {
	v, ok := r.sessions.Load(id)
	if !ok {
		return nil, false
	}
	return v.(*Session), true
}

// Remove deletes a session from the registry (does not close/kill it —
// callers do that first).
func (r *Registry) Remove(id string) {
	if _, ok := r.sessions.LoadAndDelete(id); ok {
		r.totalKilled.Add(1)
	}
}

// List returns all sessions, optionally filtered by project path (empty
// projectPath or global=true returns everything).
func (r *Registry) List(projectPath string, global bool) []*Session {
	want := scope.NormalizePath(projectPath)
	var out []*Session
	r.sessions.Range(func(_, v any) bool {
		s := v.(*Session)
		if global || projectPath == "" || scope.NormalizePath(s.ProjectPath) == want {
			out = append(out, s)
		}
		return true
	})
	return out
}

// Count returns the number of sessions currently registered.
func (r *Registry) Count() int {
	n := 0
	r.sessions.Range(func(_, _ any) bool { n++; return true })
	return n
}
