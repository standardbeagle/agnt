package overlay

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func ni(id, summary string) NoticeInfo {
	return NoticeInfo{ID: id, Domain: "proxy", Severity: "error", Summary: summary}
}

func TestVisibleNotices_AllWhenNoneDismissed(t *testing.T) {
	o := &Overlay{}
	o.status = Status{Notices: []NoticeInfo{ni("proxy:a", "a not created"), ni("proxy:b", "b not created")}}

	visible := o.VisibleNotices()

	require.Len(t, visible, 2)
	assert.Equal(t, "proxy:a", visible[0].ID)
	assert.Equal(t, "proxy:b", visible[1].ID)
}

func TestDismissNoticeByIndex_HidesThatNotice(t *testing.T) {
	o := &Overlay{}
	o.status = Status{Notices: []NoticeInfo{ni("proxy:a", "a"), ni("proxy:b", "b")}}

	o.DismissNoticeByIndex(1) // 1-based: dismiss "proxy:a"

	visible := o.VisibleNotices()
	require.Len(t, visible, 1)
	assert.Equal(t, "proxy:b", visible[0].ID)
}

func TestDismissNoticeByIndex_OutOfRangeIsNoOp(t *testing.T) {
	o := &Overlay{}
	o.status = Status{Notices: []NoticeInfo{ni("proxy:a", "a")}}

	o.DismissNoticeByIndex(0)
	o.DismissNoticeByIndex(5)
	o.DismissNoticeByIndex(-1)

	assert.Len(t, o.VisibleNotices(), 1)
}

func TestDismiss_PersistsAcrossStatusUpdatesWithSameID(t *testing.T) {
	o := &Overlay{}
	o.status = Status{Notices: []NoticeInfo{ni("proxy:a", "a"), ni("proxy:b", "b")}}
	o.DismissNoticeByIndex(1) // dismiss proxy:a

	// New status poll, proxy:a still failing (same ID) — must stay hidden.
	o.status = Status{Notices: []NoticeInfo{ni("proxy:a", "a"), ni("proxy:b", "b")}}

	visible := o.VisibleNotices()
	require.Len(t, visible, 1)
	assert.Equal(t, "proxy:b", visible[0].ID)
}

func TestDismiss_PruneOnResolveLetsRecurrenceReshow(t *testing.T) {
	o := &Overlay{}
	o.status = Status{Notices: []NoticeInfo{ni("proxy:a", "a")}}
	o.DismissNoticeByIndex(1)
	require.Empty(t, o.VisibleNotices())

	// proxy:a resolves — disappears from the active set. Prune the dismissal.
	o.status = Status{Notices: []NoticeInfo{}}
	require.Empty(t, o.VisibleNotices())

	// Same failure recurs later: must re-show (dismissal was pruned).
	o.status = Status{Notices: []NoticeInfo{ni("proxy:a", "a")}}
	visible := o.VisibleNotices()
	require.Len(t, visible, 1)
	assert.Equal(t, "proxy:a", visible[0].ID)
}

func TestDismissAll_ClearsAllVisible(t *testing.T) {
	o := &Overlay{}
	o.status = Status{Notices: []NoticeInfo{ni("proxy:a", "a"), ni("proxy:b", "b"), ni("script:c", "c")}}

	o.DismissAllNotices()

	assert.Empty(t, o.VisibleNotices())
}

func TestDispatchPalette_DismissCommands(t *testing.T) {
	router, ov, _ := newOverviewRouter(t)
	ov.status = Status{Notices: []NoticeInfo{ni("proxy:a", "a"), ni("proxy:b", "b"), ni("script:c", "c")}}

	dispatch := func(name, args string) {
		ov.mu.Lock()
		router.dispatchPaletteCommand(PaletteCommand{Name: name}, args)
		ov.mu.Unlock()
	}

	dispatch("dismiss", "2") // dismiss "proxy:b"
	visible := ov.VisibleNotices()
	require.Len(t, visible, 2)
	assert.Equal(t, "proxy:a", visible[0].ID)
	assert.Equal(t, "script:c", visible[1].ID)

	dispatch("dismiss-all", "")
	assert.Empty(t, ov.VisibleNotices())
}

func TestPaletteRegistry_IncludesDismiss(t *testing.T) {
	seen := map[string]bool{}
	for _, c := range paletteCommands {
		seen[c.Name] = true
	}
	assert.True(t, seen["dismiss"], "registry must expose dismiss")
	assert.True(t, seen["dismiss-all"], "registry must expose dismiss-all")
}
