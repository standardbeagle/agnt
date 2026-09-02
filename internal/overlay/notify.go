package overlay

import (
	"fmt"
	"io"
	"sync"
	"time"
)

// Level is the severity of a user-facing notification. It selects the row
// style in the terminal stack, the default TTL, and the prefix on the
// line-writer fallback.
type Level int

const (
	LevelInfo Level = iota
	LevelWarn
	LevelError
)

func (l Level) String() string {
	switch l {
	case LevelWarn:
		return "warning"
	case LevelError:
		return "error"
	default:
		return "info"
	}
}

// Default time-to-live per level. An error stays long enough to be read
// after the eye is drawn by the colour; an info line just needs to register.
const (
	ttlInfo  = 4 * time.Second
	ttlWarn  = 8 * time.Second
	ttlError = 15 * time.Second

	// maxNotifications bounds the store; the oldest non-sticky entry is
	// dropped on overflow so a burst can never grow the stack unboundedly.
	maxNotifications = 8
	// maxVisibleNotifications bounds the rows painted above the status bar.
	// Older entries beyond this are collapsed into a "+N more" row.
	maxVisibleNotifications = 3
)

// Notification is one user-facing message. The zero TTL selects the level
// default; Sticky pins the entry until ClearNotification(ID) or a
// same-ID Notify with Sticky=false replaces it.
type Notification struct {
	// ID is the dedup key. Empty means dedup on Level+Text. Re-notifying an
	// active ID bumps its count and refreshes its TTL instead of stacking.
	ID     string
	Level  Level
	Text   string
	TTL    time.Duration
	Sticky bool
}

func (n Notification) key() string {
	if n.ID != "" {
		return n.ID
	}
	return fmt.Sprintf("%d:%s", n.Level, n.Text)
}

func (n Notification) ttl() time.Duration {
	if n.TTL > 0 {
		return n.TTL
	}
	switch n.Level {
	case LevelError:
		return ttlError
	case LevelWarn:
		return ttlWarn
	default:
		return ttlInfo
	}
}

// Notifier is the single user-message sink. The terminal overlay implements
// it with a stacked, TTL-expiring toast above the status bar; outside a
// PTY session NewLineNotifier writes plain lines instead.
type Notifier interface {
	Notify(Notification)
}

// NotifierFunc adapts a function to Notifier.
type NotifierFunc func(Notification)

func (f NotifierFunc) Notify(n Notification) { f(n) }

// NewLineNotifier returns a Notifier that writes one plain line per
// notification: "agnt: <text>" for info, "agnt: <level>: <text>" otherwise.
// crlf selects "\r\n" endings for a terminal already in raw mode. No dedup
// or TTL: a line writer has no way to retract what it wrote.
func NewLineNotifier(w io.Writer, crlf bool) Notifier {
	eol := "\n"
	if crlf {
		eol = "\r\n"
	}
	return NotifierFunc(func(n Notification) {
		if n.Level == LevelInfo {
			fmt.Fprintf(w, "agnt: %s%s", n.Text, eol)
			return
		}
		fmt.Fprintf(w, "agnt: %s: %s%s", n.Level, n.Text, eol)
	})
}

// NotificationView is one rendered row of the stack.
type NotificationView struct {
	Level Level
	Text  string
	Count int // >1 when the same notification repeated while active
}

type notifyEntry struct {
	Notification
	count   int
	expires time.Time // zero for sticky
}

// notifyStore holds the active notifications in arrival order (oldest
// first). It is touched at human rates from a handful of goroutines, so a
// mutex is the right tool; the render path only reads a snapshot.
type notifyStore struct {
	mu      sync.Mutex
	entries []*notifyEntry
}

// add inserts or refreshes n and returns true when the visible set changed
// in a way that needs a repaint (always, for now — a refreshed TTL changes
// nothing on screen but a bumped count does).
func (s *notifyStore) add(n Notification, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked(now)
	key := n.key()
	for _, e := range s.entries {
		if e.key() == key {
			e.Notification = n
			e.count++
			e.expires = expiry(n, now)
			return
		}
	}
	if len(s.entries) >= maxNotifications {
		s.dropOldestLocked()
	}
	s.entries = append(s.entries, &notifyEntry{Notification: n, count: 1, expires: expiry(n, now)})
}

func expiry(n Notification, now time.Time) time.Time {
	if n.Sticky {
		return time.Time{}
	}
	return now.Add(n.ttl())
}

// dropOldestLocked evicts the oldest non-sticky entry, or the oldest entry
// of all when every entry is sticky.
func (s *notifyStore) dropOldestLocked() {
	for i, e := range s.entries {
		if !e.Sticky {
			s.entries = append(s.entries[:i], s.entries[i+1:]...)
			return
		}
	}
	s.entries = s.entries[1:]
}

func (s *notifyStore) remove(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, e := range s.entries {
		if e.key() == id {
			s.entries = append(s.entries[:i], s.entries[i+1:]...)
			return true
		}
	}
	return false
}

func (s *notifyStore) pruneLocked(now time.Time) {
	kept := s.entries[:0]
	for _, e := range s.entries {
		if e.expires.IsZero() || now.Before(e.expires) {
			kept = append(kept, e)
		}
	}
	for i := len(kept); i < len(s.entries); i++ {
		s.entries[i] = nil
	}
	s.entries = kept
}

// snapshot prunes expired entries and returns the active views (oldest
// first) plus the earliest pending expiry (zero when nothing expires).
func (s *notifyStore) snapshot(now time.Time) ([]NotificationView, time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked(now)
	views := make([]NotificationView, 0, len(s.entries))
	var next time.Time
	for _, e := range s.entries {
		views = append(views, NotificationView{Level: e.Level, Text: e.Text, Count: e.count})
		if !e.expires.IsZero() && (next.IsZero() || e.expires.Before(next)) {
			next = e.expires
		}
	}
	return views, next
}

func (s *notifyStore) len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.entries)
}

// Notify adds n to the terminal stack and repaints when the stack is
// visible. Expiry is driven by a single timer armed for the earliest
// pending TTL, re-armed on every paint, so no per-notification goroutine
// exists and an idle overlay runs no ticker.
func (o *Overlay) Notify(n Notification) {
	o.notifications.add(n, time.Now())
	o.repaintNotifications()
}

// ClearNotification removes the notification with the given ID (or
// Level+Text key) and repaints. Used to retract a sticky notification.
func (o *Overlay) ClearNotification(id string) {
	if o.notifications.remove(id) {
		o.repaintNotifications()
	}
}

// HasNotifications reports whether any notification is active. The startup
// splash consults it to yield the rows above the status bar.
func (o *Overlay) HasNotifications() bool {
	return o.notifications.len() > 0
}

func (o *Overlay) repaintNotifications() {
	if !o.showBar.Load() || o.State() != StateIndicator || o.redrawPaused.Load() {
		return
	}
	o.mu.Lock()
	o.draw()
	o.mu.Unlock()
}

// drawNotificationsLocked paints the stack (indicator state only) and arms
// the expiry timer for the next repaint. Must hold o.mu.
func (o *Overlay) drawNotificationsLocked() {
	if o.isChildInAltScreen() {
		return
	}
	views, next := o.notifications.snapshot(time.Now())
	o.notifyRows = o.renderer.DrawNotifications(views, o.notifyRows)
	if o.notifyTimer != nil {
		o.notifyTimer.Stop()
		o.notifyTimer = nil
	}
	if !next.IsZero() {
		// +1ms so the timer lands strictly after expiry and prune sees it.
		o.notifyTimer = time.AfterFunc(time.Until(next)+time.Millisecond, o.repaintNotifications)
	}
}
