package proxy

import "time"

// logEntryFixedBytes approximates a LogEntry value (about 224B) plus the small
// heap objects most entries point at.
const logEntryFixedBytes = 512

// entryBytes estimates one entry's retained size: the fixed part plus the
// captured HTTP bodies, which dominate for traffic entries.
func entryBytes(e *LogEntry) int64 {
	n := int64(logEntryFixedBytes)
	if e.HTTP != nil {
		n += int64(len(e.HTTP.RequestBody) + len(e.HTTP.ResponseBody))
	}
	return n
}

// RetainedBytes estimates the memory the ring holds.
func (tl *TrafficLogger) RetainedBytes() int64 {
	tl.mu.RLock()
	defer tl.mu.RUnlock()
	var n int64
	for i := range tl.entries {
		n += entryBytes(&tl.entries[i])
	}
	return n
}

// Trim keeps only the newest keep entries and compacts the backing array, so
// the memory is returned rather than just overwritten. keep >= the number of
// entries held only compacts spare capacity. It returns how many entries were
// dropped and an estimate of the bytes released. Dropped entries still count
// toward Stats().Dropped.
func (tl *TrafficLogger) Trim(keep int) (dropped int, freed int64) {
	if keep < 0 {
		keep = 0
	}
	tl.mu.Lock()
	defer tl.mu.Unlock()

	head := tl.head.Load()
	available := int(min(head, int64(tl.maxSize)))
	start := 0
	if head > int64(tl.maxSize) {
		start = int(head % int64(tl.maxSize))
	}
	skip := 0
	if available > keep {
		skip = available - keep
	}

	kept := make([]LogEntry, 0, available-skip)
	for i := 0; i < available; i++ {
		e := tl.entries[(start+i)%tl.maxSize]
		if i < skip {
			freed += entryBytes(&e)
			continue
		}
		kept = append(kept, e)
	}
	spare := int64(cap(tl.entries)-len(tl.entries)) * 224
	if skip == 0 && spare < 4096 {
		return 0, 0 // nothing worth compacting
	}
	freed += spare

	// Rebuild as a fresh, unwrapped ring: positions restart at len(kept) and
	// the dropped entries stay counted as dropped.
	tl.trimmed.Add(int64(skip))
	tl.entries = kept
	tl.head.Store(int64(len(kept)))
	tl.count.Store(int64(len(kept)))
	return skip, freed
}

// RetainedBytes estimates the memory held by tracked page sessions.
func (pt *PageTracker) RetainedBytes() int64 {
	return ptQuery(pt, func() int64 {
		var n int64
		for _, s := range pt.sessions {
			n += sessionBytes(s)
		}
		return n
	})
}

func sessionBytes(s *PageSession) int64 {
	n := int64(logEntryFixedBytes)
	for i := range s.Navigations {
		n += int64(len(s.Navigations[i].RequestBody)+len(s.Navigations[i].ResponseBody)) + logEntryFixedBytes
	}
	for i := range s.Resources {
		n += int64(len(s.Resources[i].RequestBody)+len(s.Resources[i].ResponseBody)) + logEntryFixedBytes
	}
	return n
}

// ReclaimIdle evicts page sessions with no activity for idleFor and returns the
// estimated bytes released. The tracker evicts only when over its session cap,
// so without this an idle tab's captured bodies lived until 100 newer tabs
// appeared or the proxy stopped.
func (pt *PageTracker) ReclaimIdle(idleFor time.Duration) int64 {
	return ptQuery(pt, func() int64 {
		now := time.Now()
		var freed int64
		for id, s := range pt.sessions {
			if now.Sub(s.LastActivity) >= idleFor {
				freed += sessionBytes(s)
				pt.evictSession(id)
			}
		}
		return freed
	})
}

// Reclaim gives memory back. Soft drops only what is already stale (page
// sessions idle past the tracker's own timeout) and compacts the log ring.
// Hard also drops sessions idle for 30s and keeps just the newest quarter of
// the traffic log. It returns the estimated bytes released.
func (ps *ProxyServer) Reclaim(hard bool) int64 {
	var freed int64
	logger := ps.Logger()
	if logger != nil {
		keep := logger.maxSize // Soft: no entry is dropped, spare capacity is compacted
		if hard {
			keep = max(1, logger.maxSize/4)
		}
		_, f := logger.Trim(keep)
		freed += f
	}
	if pt := ps.PageTracker(); pt != nil {
		idle := pt.sessionTimeout
		if hard {
			idle = 30 * time.Second
		}
		freed += pt.ReclaimIdle(idle)
	}
	return freed
}

// RetainedBytes estimates the memory this proxy's history holds.
func (ps *ProxyServer) RetainedBytes() int64 {
	var n int64
	if l := ps.Logger(); l != nil {
		n += l.RetainedBytes()
	}
	if pt := ps.PageTracker(); pt != nil {
		n += pt.RetainedBytes()
	}
	return n
}
