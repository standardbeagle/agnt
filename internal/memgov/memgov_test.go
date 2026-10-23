package memgov

import (
	"sync/atomic"
	"testing"
	"time"
)

type fakeCache struct {
	name  string
	size  atomic.Int64
	calls atomic.Int64
	last  atomic.Int64
}

func (f *fakeCache) Name() string { return f.name }
func (f *fakeCache) Size() int64  { return f.size.Load() }
func (f *fakeCache) Reclaim(l Level) int64 {
	f.calls.Add(1)
	f.last.Store(int64(l))
	n := f.size.Swap(0)
	return n
}

type clock struct{ t atomic.Int64 }

func (c *clock) now() time.Time          { return time.Unix(0, c.t.Load()) }
func (c *clock) advance(d time.Duration) { c.t.Add(int64(d)) }

func newTestGov(limit int64, heap *atomic.Uint64, c *clock, frees *atomic.Int64, onReclaim func(Level, int64, []string)) *Governor {
	return New(Config{
		Limit:     limit,
		OnReclaim: onReclaim,
		readHeap:  func() uint64 { return heap.Load() },
		freeOS:    func() { frees.Add(1) },
		now:       c.now,
	})
}

func TestTick_NothingToDoWhenQuiet(t *testing.T) {
	var heap atomic.Uint64
	var frees atomic.Int64
	c := &clock{}
	c.t.Store(1)
	g := newTestGov(100<<20, &heap, c, &frees, nil)
	f := &fakeCache{name: "c"}
	g.Register(f)
	heap.Store(10 << 20)
	g.Tick()
	if f.calls.Load() != 0 {
		t.Fatal("reclaimed with no pressure, release or idleness")
	}
}

func TestTick_PressureEscalatesSoftThenHard(t *testing.T) {
	var heap atomic.Uint64
	var frees atomic.Int64
	c := &clock{}
	c.t.Store(1)
	var hardNames []string
	g := newTestGov(100<<20, &heap, c, &frees, func(l Level, _ int64, n []string) {
		if l == Hard {
			hardNames = n
		}
	})
	f := &fakeCache{name: "traffic"}
	g.Register(f)

	f.size.Store(1 << 20)
	heap.Store(75 << 20) // > 70%
	g.Tick()
	if f.last.Load() != int64(Soft) || f.calls.Load() != 1 {
		t.Fatalf("want one Soft reclaim, got calls=%d level=%d", f.calls.Load(), f.last.Load())
	}

	f.size.Store(1 << 20)
	heap.Store(95 << 20) // > 90%
	g.Tick()
	if f.last.Load() != int64(Hard) {
		t.Fatalf("want Hard, got %d", f.last.Load())
	}
	if len(hardNames) != 1 || hardNames[0] != "traffic" {
		t.Fatalf("Hard reclaim not surfaced with the cache name: %v", hardNames)
	}
}

func TestTick_IdleReclaimsOncePerIdlePeriod(t *testing.T) {
	var heap atomic.Uint64
	var frees atomic.Int64
	c := &clock{}
	c.t.Store(1)
	g := newTestGov(0, &heap, c, &frees, nil)
	f := &fakeCache{name: "c"}
	g.Register(f)

	c.advance(61 * time.Second)
	f.size.Store(1)
	g.Tick()
	g.Tick()
	if f.calls.Load() != 1 {
		t.Fatalf("idle reclaim ran %d times, want 1", f.calls.Load())
	}
	g.NoteActivity()
	c.advance(61 * time.Second)
	f.size.Store(1)
	g.Tick()
	if f.calls.Load() != 2 {
		t.Fatalf("idle reclaim after new activity: calls=%d, want 2", f.calls.Load())
	}
}

func TestTick_FreeOSMemoryOnlyAfterLargeReleaseAndRateLimited(t *testing.T) {
	var heap atomic.Uint64
	var frees atomic.Int64
	c := &clock{}
	c.t.Store(1)
	g := newTestGov(0, &heap, c, &frees, nil)
	g.Register(&fakeCache{name: "c"})

	g.NoteRelease(1 << 20)
	g.Tick() // below the 32MiB trigger: nothing
	if frees.Load() != 0 {
		t.Fatal("FreeOSMemory on a small release")
	}

	c.advance(10 * time.Minute)
	g.NoteRelease(40 << 20)
	g.Tick()
	if frees.Load() != 1 {
		t.Fatalf("FreeOSMemory calls = %d after a large release, want 1", frees.Load())
	}

	g.NoteRelease(40 << 20)
	c.advance(time.Minute)
	g.Tick()
	if frees.Load() != 1 {
		t.Fatalf("FreeOSMemory not rate limited: %d calls", frees.Load())
	}
}

func TestRegister_UnregisterRemovesOnlyThatRegistration(t *testing.T) {
	var heap atomic.Uint64
	var frees atomic.Int64
	c := &clock{}
	c.t.Store(1)
	g := newTestGov(0, &heap, c, &frees, nil)
	old := &fakeCache{name: "same"}
	unOld := g.Register(old)
	newer := &fakeCache{name: "same"}
	g.Register(newer)
	unOld()
	if got := len(g.Stats().Reclaimers); got != 1 {
		t.Fatalf("reclaimers after unregistering the old one = %d, want 1", got)
	}
}

type panicker struct{}

func (panicker) Name() string        { return "p" }
func (panicker) Size() int64         { return 0 }
func (panicker) Reclaim(Level) int64 { panic("boom") }

func TestReclaim_ContainsAPanickingReclaimer(t *testing.T) {
	var heap atomic.Uint64
	var frees atomic.Int64
	c := &clock{}
	c.t.Store(1)
	g := newTestGov(0, &heap, c, &frees, nil)
	good := &fakeCache{name: "good"}
	g.Register(panicker{})
	g.Register(good)
	good.size.Store(5)
	g.NoteRelease(64 << 20)
	g.Tick()
	if good.calls.Load() != 1 {
		t.Fatal("a panicking reclaimer stopped the others")
	}
}

func TestResolveLimit(t *testing.T) {
	t.Setenv("GOMEMLIMIT", "")
	cases := map[string]int64{"": DefaultLimit, "512m": 512 << 20, "2g": 2 << 30, "off": 0, "0": 0, "junk": DefaultLimit, "1024k": 1 << 20}
	for in, want := range cases {
		t.Setenv("AGNT_MEMORY_LIMIT", in)
		got, ok := ResolveLimit()
		if !ok || got != want {
			t.Errorf("AGNT_MEMORY_LIMIT=%q -> (%d,%v), want (%d,true)", in, got, ok, want)
		}
	}
	t.Setenv("GOMEMLIMIT", "300MiB")
	if _, ok := ResolveLimit(); ok {
		t.Error("an existing GOMEMLIMIT must be left alone")
	}
}
