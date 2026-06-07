package overlay

import "sync"

// noticeDismissals tracks notice IDs the developer has dismissed for this
// session. Session-only: never persisted. A dismissed ID is pruned once the
// underlying notice is no longer active (resolved), so a later recurrence of
// the same failure re-shows.
type noticeDismissals struct {
	mu        sync.Mutex
	dismissed map[string]bool
}

func (d *noticeDismissals) dismiss(id string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.dismissed == nil {
		d.dismissed = make(map[string]bool)
	}
	d.dismissed[id] = true
}

// filter returns the notices not currently dismissed, and prunes dismissed IDs
// that are absent from the active set (so recurrences re-show).
func (d *noticeDismissals) filter(active []NoticeInfo) []NoticeInfo {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.dismissed) == 0 {
		return active
	}
	present := make(map[string]bool, len(active))
	for _, n := range active {
		present[n.ID] = true
	}
	for id := range d.dismissed {
		if !present[id] {
			delete(d.dismissed, id)
		}
	}
	out := make([]NoticeInfo, 0, len(active))
	for _, n := range active {
		if !d.dismissed[n.ID] {
			out = append(out, n)
		}
	}
	return out
}

// VisibleNotices returns the current notices minus any the developer dismissed
// this session. Pruning of resolved dismissals happens here.
func (o *Overlay) VisibleNotices() []NoticeInfo {
	o.statusMu.RLock()
	active := o.status.Notices
	o.statusMu.RUnlock()
	return o.notices.filter(active)
}

// DismissNoticeByIndex dismisses the notice at the given 1-based position in the
// currently-visible list. Out-of-range indices are no-ops.
func (o *Overlay) DismissNoticeByIndex(idx int) {
	visible := o.VisibleNotices()
	if idx < 1 || idx > len(visible) {
		return
	}
	o.notices.dismiss(visible[idx-1].ID)
}

// DismissAllNotices dismisses every currently-visible notice.
func (o *Overlay) DismissAllNotices() {
	for _, n := range o.VisibleNotices() {
		o.notices.dismiss(n.ID)
	}
}
