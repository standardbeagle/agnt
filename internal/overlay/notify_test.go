package overlay

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNotifyStore_DedupCountAndTTL(t *testing.T) {
	var s notifyStore
	t0 := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)

	s.add(Notification{Level: LevelInfo, Text: "hi"}, t0)
	s.add(Notification{Level: LevelInfo, Text: "hi"}, t0.Add(time.Second))
	views, next := s.snapshot(t0.Add(time.Second))
	require.Len(t, views, 1, "same level+text dedups")
	assert.Equal(t, 2, views[0].Count)
	assert.Equal(t, t0.Add(time.Second+ttlInfo), next, "re-notify refreshes the TTL")

	// Different level with the same text is a different notification.
	s.add(Notification{Level: LevelWarn, Text: "hi"}, t0.Add(time.Second))
	views, _ = s.snapshot(t0.Add(time.Second))
	require.Len(t, views, 2)

	// Info expires first (4s), warn later (8s).
	views, _ = s.snapshot(t0.Add(time.Second + ttlInfo + time.Millisecond))
	require.Len(t, views, 1)
	assert.Equal(t, LevelWarn, views[0].Level)
	views, next = s.snapshot(t0.Add(time.Second + ttlWarn + time.Millisecond))
	assert.Empty(t, views)
	assert.True(t, next.IsZero(), "nothing pending once empty")
}

func TestNotifyStore_ExplicitIDAndSticky(t *testing.T) {
	var s notifyStore
	t0 := time.Now()
	s.add(Notification{ID: "fwd", Level: LevelInfo, Text: "paused"}, t0)
	s.add(Notification{ID: "fwd", Level: LevelInfo, Text: "resumed"}, t0)
	views, _ := s.snapshot(t0)
	require.Len(t, views, 1, "same ID replaces in place")
	assert.Equal(t, "resumed", views[0].Text)

	s.add(Notification{ID: "pin", Level: LevelError, Text: "stuck", Sticky: true}, t0)
	views, next := s.snapshot(t0.Add(time.Hour))
	require.Len(t, views, 1, "sticky survives any elapsed time")
	assert.Equal(t, "stuck", views[0].Text)
	assert.True(t, next.IsZero(), "a sticky entry arms no timer")
	assert.True(t, s.remove("pin"))
	assert.False(t, s.remove("pin"))
	assert.Equal(t, 0, s.len())
}

func TestNotifyStore_CapDropsOldestNonSticky(t *testing.T) {
	var s notifyStore
	t0 := time.Now()
	s.add(Notification{ID: "keep", Text: "sticky", Sticky: true}, t0)
	for i := 0; i < maxNotifications; i++ {
		s.add(Notification{Text: fmt.Sprintf("n%d", i)}, t0)
	}
	views, _ := s.snapshot(t0)
	require.Len(t, views, maxNotifications)
	assert.Equal(t, "sticky", views[0].Text, "sticky entry is never the one evicted")
	assert.Equal(t, "n1", views[1].Text, "oldest non-sticky (n0) was dropped")
}

func TestNotificationLines_CollapsesOverflow(t *testing.T) {
	var views []NotificationView
	for i := 0; i < maxVisibleNotifications+2; i++ {
		views = append(views, NotificationView{Level: LevelInfo, Text: fmt.Sprintf("m%d", i)})
	}
	views[len(views)-1].Count = 3
	lines := notificationLines(views)
	require.Len(t, lines, maxVisibleNotifications+1)
	assert.Equal(t, " +2 more", lines[0].text)
	assert.Equal(t, " ● m2", lines[1].text, "oldest visible after the overflow row")
	assert.Equal(t, " ● m4 (×3)", lines[len(lines)-1].text, "newest last, with repeat count")
	assert.Nil(t, notificationLines(nil))
}

// The stack paints bottom-anchored rows directly above the status bar,
// brackets itself with cursor save/restore, and clears rows it no longer
// uses so an expired notification leaves no residue.
func TestRenderer_DrawNotifications_RowsAndClear(t *testing.T) {
	const w, h = 40, 24
	var buf bytes.Buffer
	r := NewRenderer(&buf, w, h)

	rows := r.DrawNotifications([]NotificationView{
		{Level: LevelWarn, Text: "one"},
		{Level: LevelError, Text: "two"},
	}, 0)
	assert.Equal(t, 2, rows)
	out := buf.String()
	assert.True(t, strings.HasPrefix(out, CursorSave+CursorHide))
	assert.True(t, strings.HasSuffix(out, CursorRestore+CursorShow))
	assert.Contains(t, out, fmt.Sprintf("\x1b[%d;1H", h-2), "older row sits two above the status bar")
	assert.Contains(t, out, fmt.Sprintf("\x1b[%d;1H", h-1), "newest row sits directly above the status bar")
	assert.NotContains(t, out, fmt.Sprintf("\x1b[%d;1H", h), "never touches the status row")
	assert.Contains(t, out, BgRed+FgBrightWhite+" ✖ two")

	buf.Reset()
	rows = r.DrawNotifications([]NotificationView{{Level: LevelInfo, Text: "two"}}, rows)
	assert.Equal(t, 1, rows)
	out = buf.String()
	assert.Contains(t, out, fmt.Sprintf("\x1b[%d;1H", h-2)+ClearLine, "row vacated by the expired entry is cleared")

	buf.Reset()
	rows = r.DrawNotifications(nil, rows)
	assert.Equal(t, 0, rows)
	assert.Contains(t, buf.String(), fmt.Sprintf("\x1b[%d;1H", h-1)+ClearLine)

	buf.Reset()
	assert.Equal(t, 0, r.DrawNotifications(nil, 0))
	assert.Empty(t, buf.String(), "nothing to paint and nothing to clear writes nothing")
}

// Padding is by display width: an emoji row must fill exactly the terminal
// width, never wrap past it (the old len()-based status padding wrapped).
func TestRenderer_PadToWidth_DisplayCells(t *testing.T) {
	r := NewRenderer(&bytes.Buffer{}, 12, 5)
	padded := r.padToWidth(" 🔇 muted", 12)
	assert.Equal(t, 12, r.estimateVisibleLength(padded))
	assert.Equal(t, 12, r.estimateVisibleLength(r.padToWidth("this line is far too long for twelve cells", 12)))
	assert.Equal(t, "", r.padToWidth("x", 0))
}

// Overlay.Notify paints immediately in indicator state and repaints on
// expiry so the row is cleared without a caller-owned timer.
func TestOverlay_NotifyPaintsAndExpires(t *testing.T) {
	const w, h = 60, 20
	rw := &renderSafeWriter{}
	o := New(nil, w, h, DefaultConfig())
	o.renderer = NewRenderer(rw, w, h)

	o.Notify(Notification{Level: LevelInfo, Text: "hello", TTL: 30 * time.Millisecond})
	assert.Contains(t, rw.String(), " ● hello")
	assert.True(t, o.HasNotifications())

	require.Eventually(t, func() bool { return !o.HasNotifications() }, 2*time.Second, 5*time.Millisecond)
	require.Eventually(t, func() bool {
		return strings.Contains(rw.String(), fmt.Sprintf("\x1b[%d;1H", h-1)+ClearLine)
	}, 2*time.Second, 5*time.Millisecond, "expiry repaint clears the row")
}

func TestOverlay_NotifySkipsAltScreenAndHiddenState(t *testing.T) {
	const w, h = 60, 20
	rw := &renderSafeWriter{}
	o := New(nil, w, h, DefaultConfig())
	o.renderer = NewRenderer(rw, w, h)
	o.SetAltScreenChecker(func() bool { return true })

	o.Notify(Notification{Level: LevelError, Text: "vim is up", Sticky: true})
	assert.NotContains(t, rw.String(), "vim is up", "no paint over a child alt screen")
	assert.True(t, o.HasNotifications(), "still queued for when the main screen returns")

	o.ClearNotification("") // no such key: no-op
	o.ClearNotification(Notification{Level: LevelError, Text: "vim is up"}.key())
	assert.False(t, o.HasNotifications())
}

func TestLineNotifier_Format(t *testing.T) {
	var b bytes.Buffer
	n := NewLineNotifier(&b, false)
	n.Notify(Notification{Level: LevelInfo, Text: "plain"})
	n.Notify(Notification{Level: LevelWarn, Text: "careful"})
	n.Notify(Notification{Level: LevelError, Text: "broken"})
	assert.Equal(t, "agnt: plain\nagnt: warning: careful\nagnt: error: broken\n", b.String())

	b.Reset()
	NewLineNotifier(&b, true).Notify(Notification{Text: "raw"})
	assert.Equal(t, "agnt: raw\r\n", b.String())
}

func TestStartupSplash_YieldsToNotifications(t *testing.T) {
	out := &splashSafeWriter{}
	yield := false
	s := NewStartupSplash(out, 80, 24).YieldTo(func() bool { return yield })
	s.render("tip one")
	assert.Contains(t, out.String(), "tip one")

	yield = true
	before := len(out.String())
	s.render("tip two")
	assert.NotContains(t, out.String()[before:], "tip two", "yielded splash paints nothing")
	assert.Contains(t, out.String()[before:], ClearLine, "and clears its own row once")
	before = len(out.String())
	s.render("tip three")
	assert.Equal(t, before, len(out.String()), "no repeated clears while yielding")

	yield = false
	s.render("tip four")
	assert.Contains(t, out.String(), "tip four")
}
