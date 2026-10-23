// Package memgov is the daemon's thin memory governor: one soft ceiling for the
// Go runtime, a registry of caches that can give memory back, and explicit
// reclaim (including returning pages to the OS) after large releases or when
// the daemon has gone idle.
//
// It is advisory. A Reclaimer drops cached history (traffic logs, idle page
// sessions, expired dedup windows); the governor never stops a process, proxy
// or session. Anything a Reclaimer drops that an agent might have wanted must
// be reported through Config.OnReclaim so it is visible, not just debug-logged.
package memgov

import (
	"context"
	"os"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Level says how hard a Reclaimer should work.
type Level int

const (
	// Soft drops only data that is already stale (idle, expired, finished).
	Soft Level = iota + 1
	// Hard may also drop live-but-old history to get under the ceiling.
	Hard
)

func (l Level) String() string {
	if l == Hard {
		return "hard"
	}
	return "soft"
}

// Reclaimer is a cache that can give memory back.
type Reclaimer interface {
	Name() string
	// Size is the cache's current retained bytes (an estimate is fine).
	Size() int64
	// Reclaim releases memory and returns the bytes released (an estimate).
	Reclaim(Level) int64
}

// Config tunes the governor. Zero values take the defaults noted.
type Config struct {
	// Limit is the soft heap ceiling in bytes. 0 disables pressure-based
	// reclaim (idle and release-triggered reclaim still run).
	Limit int64
	// Interval between checks. Default 30s.
	Interval time.Duration
	// IdleAfter is how long without NoteActivity counts as idle. Default 60s.
	IdleAfter time.Duration
	// ReleaseTrigger is the cumulative NoteRelease bytes that provoke a Soft
	// reclaim. Default 32MiB.
	ReleaseTrigger int64
	// FreeOSMinFreed is the freed (or released) bytes needed before the
	// governor calls debug.FreeOSMemory. Default 16MiB.
	FreeOSMinFreed int64
	// FreeOSEvery rate-limits debug.FreeOSMemory, a stop-the-world GC.
	// Default 5 minutes.
	FreeOSEvery time.Duration
	// OnReclaim is told about every reclaim that released memory, and is where
	// a Hard reclaim is surfaced to the agent.
	OnReclaim func(level Level, freed int64, names []string)

	// Test seams.
	readHeap func() uint64
	freeOS   func()
	now      func() time.Time
}

func (c *Config) defaults() {
	if c.Interval <= 0 {
		c.Interval = 30 * time.Second
	}
	if c.IdleAfter <= 0 {
		c.IdleAfter = 60 * time.Second
	}
	if c.ReleaseTrigger <= 0 {
		c.ReleaseTrigger = 32 << 20
	}
	if c.FreeOSMinFreed <= 0 {
		c.FreeOSMinFreed = 16 << 20
	}
	if c.FreeOSEvery <= 0 {
		c.FreeOSEvery = 5 * time.Minute
	}
	if c.readHeap == nil {
		c.readHeap = func() uint64 {
			var m runtime.MemStats
			runtime.ReadMemStats(&m)
			return m.HeapInuse
		}
	}
	if c.freeOS == nil {
		c.freeOS = debug.FreeOSMemory
	}
	if c.now == nil {
		c.now = time.Now
	}
}

// Stat is one Reclaimer's size, for the doctor report.
type Stat struct {
	Name string `json:"name"`
	Size int64  `json:"size_bytes"`
}

// Stats is a point-in-time governor report.
type Stats struct {
	LimitBytes     int64  `json:"limit_bytes"`
	HeapInuseBytes uint64 `json:"heap_inuse_bytes"`
	Reclaims       int64  `json:"reclaims"`
	FreedBytes     int64  `json:"freed_bytes"`
	FreeOSCalls    int64  `json:"free_os_calls"`
	Reclaimers     []Stat `json:"reclaimers"`
}

// Governor coordinates reclaim. Create with New, run with Start.
type Governor struct {
	cfg Config

	mu         sync.Mutex
	reclaimers map[uint64]Reclaimer
	nextID     uint64

	released     atomic.Int64 // bytes released since the last reclaim pass
	lastActivity atomic.Int64 // unix nanos
	idleDone     atomic.Bool  // an idle reclaim already ran since the last activity

	reclaims    atomic.Int64
	freed       atomic.Int64
	freeOSCalls atomic.Int64
	lastFreeOS  time.Time // guarded by tickMu
	tickMu      sync.Mutex
}

// New returns a governor. It does nothing until Start.
func New(cfg Config) *Governor {
	cfg.defaults()
	g := &Governor{cfg: cfg, reclaimers: make(map[uint64]Reclaimer)}
	g.lastActivity.Store(cfg.now().UnixNano())
	return g
}

// Register adds a Reclaimer and returns the function that removes exactly this
// registration (a same-name replacement registered later is left alone).
func (g *Governor) Register(r Reclaimer) (unregister func()) {
	if g == nil {
		return func() {}
	}
	g.mu.Lock()
	g.nextID++
	id := g.nextID
	g.reclaimers[id] = r
	g.mu.Unlock()
	return func() {
		g.mu.Lock()
		delete(g.reclaimers, id)
		g.mu.Unlock()
	}
}

// NoteRelease tells the governor a large resource was just released (a proxy
// stopped, a session cleaned up), so the next tick should reclaim and return
// pages to the OS rather than wait for the background scavenger.
func (g *Governor) NoteRelease(bytes int64) {
	if g != nil && bytes > 0 {
		g.released.Add(bytes)
	}
}

// NoteActivity marks the daemon as busy. Cheap enough for a hot path.
func (g *Governor) NoteActivity() {
	if g == nil {
		return
	}
	now := g.cfg.now().UnixNano()
	// Per output line and per proxied request: skip the stores when the last
	// mark is under a second old.
	if now-g.lastActivity.Load() < int64(time.Second) {
		return
	}
	g.lastActivity.Store(now)
	g.idleDone.Store(false)
}

// Start runs the check loop until ctx is done.
func (g *Governor) Start(ctx context.Context) {
	go func() {
		t := time.NewTicker(g.cfg.Interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				g.Tick()
			}
		}
	}()
}

// Tick runs one check. Exported for tests and for an explicit "reclaim now".
func (g *Governor) Tick() {
	g.tickMu.Lock()
	defer g.tickMu.Unlock()

	heap := int64(g.cfg.readHeap())
	level := Level(0)
	pendingRelease := g.released.Load()

	switch {
	case g.cfg.Limit > 0 && heap > g.cfg.Limit/10*9:
		level = Hard
	case g.cfg.Limit > 0 && heap > g.cfg.Limit/10*7:
		level = Soft
	case pendingRelease >= g.cfg.ReleaseTrigger:
		level = Soft
	case g.cfg.now().Sub(time.Unix(0, g.lastActivity.Load())) >= g.cfg.IdleAfter && !g.idleDone.Load():
		level = Soft
		g.idleDone.Store(true)
	}
	if level == 0 {
		return
	}

	freed, names := g.reclaim(level)
	g.released.Store(0)
	if freed > 0 && g.cfg.OnReclaim != nil {
		g.cfg.OnReclaim(level, freed, names)
	}

	// Return pages to the OS only when something substantial let go, and not
	// more often than FreeOSEvery: it forces a full GC.
	if (freed >= g.cfg.FreeOSMinFreed || pendingRelease >= g.cfg.FreeOSMinFreed) &&
		g.cfg.now().Sub(g.lastFreeOS) >= g.cfg.FreeOSEvery {
		g.cfg.freeOS()
		g.lastFreeOS = g.cfg.now()
		g.freeOSCalls.Add(1)
	}
}

func (g *Governor) reclaim(level Level) (freed int64, names []string) {
	g.mu.Lock()
	rs := make([]Reclaimer, 0, len(g.reclaimers))
	for _, r := range g.reclaimers {
		rs = append(rs, r)
	}
	g.mu.Unlock()

	for _, r := range rs {
		if n := safeReclaim(r, level); n > 0 {
			freed += n
			names = append(names, r.Name())
		}
	}
	g.reclaims.Add(1)
	g.freed.Add(freed)
	return freed, names
}

// safeReclaim contains a panicking Reclaimer: one broken cache must not take
// the daemon down from its housekeeping goroutine.
func safeReclaim(r Reclaimer, level Level) (n int64) {
	defer func() {
		if recover() != nil {
			n = 0
		}
	}()
	return r.Reclaim(level)
}

// Stats reports the governor's state.
func (g *Governor) Stats() Stats {
	s := Stats{
		LimitBytes:     g.cfg.Limit,
		HeapInuseBytes: g.cfg.readHeap(),
		Reclaims:       g.reclaims.Load(),
		FreedBytes:     g.freed.Load(),
		FreeOSCalls:    g.freeOSCalls.Load(),
	}
	g.mu.Lock()
	rs := make([]Reclaimer, 0, len(g.reclaimers))
	for _, r := range g.reclaimers {
		rs = append(rs, r)
	}
	g.mu.Unlock()
	for _, r := range rs {
		s.Reclaimers = append(s.Reclaimers, Stat{Name: r.Name(), Size: r.Size()})
	}
	return s
}

// DefaultLimit is the soft heap ceiling used when AGNT_MEMORY_LIMIT is unset.
const DefaultLimit = 1 << 30

// ResolveLimit returns the soft ceiling in bytes. A GOMEMLIMIT already in the
// environment wins (the runtime applied it; ok=false means "leave it"). Else
// AGNT_MEMORY_LIMIT is a byte count with an optional k/m/g suffix, and "0" or
// "off" disables the ceiling.
func ResolveLimit() (limit int64, ok bool) {
	if os.Getenv("GOMEMLIMIT") != "" {
		return 0, false
	}
	v := strings.ToLower(strings.TrimSpace(os.Getenv("AGNT_MEMORY_LIMIT")))
	if v == "" {
		return DefaultLimit, true
	}
	if v == "0" || v == "off" {
		return 0, true
	}
	mult := int64(1)
	switch {
	case strings.HasSuffix(v, "g"):
		mult, v = 1<<30, strings.TrimSuffix(v, "g")
	case strings.HasSuffix(v, "m"):
		mult, v = 1<<20, strings.TrimSuffix(v, "m")
	case strings.HasSuffix(v, "k"):
		mult, v = 1<<10, strings.TrimSuffix(v, "k")
	}
	n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
	if err != nil || n <= 0 {
		return DefaultLimit, true
	}
	return n * mult, true
}

// ApplyLimit installs the soft ceiling with the runtime and returns what was
// applied (0 when none). The Go limit is soft: it makes the collector and
// scavenger work harder near the ceiling and never fails an allocation.
func ApplyLimit() int64 {
	limit, ok := ResolveLimit()
	if !ok || limit <= 0 {
		return 0
	}
	debug.SetMemoryLimit(limit)
	return limit
}
