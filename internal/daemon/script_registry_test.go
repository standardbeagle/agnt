package daemon

import (
	"fmt"
	"sync"
	"testing"

	"github.com/standardbeagle/agnt/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestScriptState_String(t *testing.T) {
	tests := []struct {
		state ScriptState
		want  string
	}{
		{StateIdle, "idle"},
		{StateStarting, "starting"},
		{StateRunning, "running"},
		{StateFailed, "failed"},
		{StateStopped, "stopped"},
		{StateRestarting, "restarting"},
		{ScriptState(99), "unknown(99)"},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, tt.state.String())
	}
}

func TestScriptRegistry_Register(t *testing.T) {
	reg := NewScriptRegistry()
	cfg := &config.ScriptConfig{Run: "npm start"}

	entry, err := reg.Register("dev", "/home/user/myapp", cfg)
	require.NoError(t, err)
	assert.Equal(t, "dev", entry.Name)
	assert.Equal(t, "/home/user/myapp", entry.ProjectPath)
	assert.Equal(t, StateIdle, entry.State())
	assert.Equal(t, cfg, entry.Config)
	assert.Equal(t, makeProcessID("/home/user/myapp", "dev"), entry.ProcessID)
}

func TestScriptRegistry_RegisterReturnsExisting(t *testing.T) {
	reg := NewScriptRegistry()
	cfg := &config.ScriptConfig{Run: "npm start"}

	first, err := reg.Register("dev", "/home/user/myapp", cfg)
	require.NoError(t, err)

	second, err := reg.Register("dev", "/home/user/myapp", cfg)
	require.NoError(t, err)

	assert.Same(t, first, second, "second Register must return the same entry")
}

func TestScriptRegistry_RegisterValidation(t *testing.T) {
	reg := NewScriptRegistry()
	cfg := &config.ScriptConfig{Run: "npm start"}

	_, err := reg.Register("", "/home/user/myapp", cfg)
	assert.Error(t, err)

	_, err = reg.Register("dev", "", cfg)
	assert.Error(t, err)
}

func TestScriptRegistry_Get(t *testing.T) {
	reg := NewScriptRegistry()
	cfg := &config.ScriptConfig{Run: "npm start"}

	reg.Register("dev", "/home/user/myapp", cfg)

	entry, ok := reg.Get("dev", "/home/user/myapp")
	assert.True(t, ok)
	assert.Equal(t, "dev", entry.Name)

	_, ok = reg.Get("build", "/home/user/myapp")
	assert.False(t, ok)

	_, ok = reg.Get("dev", "/other/path")
	assert.False(t, ok)
}

func TestScriptRegistry_List(t *testing.T) {
	reg := NewScriptRegistry()
	cfg := &config.ScriptConfig{Run: "npm start"}

	reg.Register("dev", "/home/user/app1", cfg)
	reg.Register("build", "/home/user/app1", cfg)
	reg.Register("dev", "/home/user/app2", cfg)

	list := reg.List("/home/user/app1")
	assert.Len(t, list, 2)

	list = reg.List("/home/user/app2")
	assert.Len(t, list, 1)

	list = reg.List("/nonexistent")
	assert.Empty(t, list)
}

func TestScriptRegistry_ListAll(t *testing.T) {
	reg := NewScriptRegistry()
	cfg := &config.ScriptConfig{Run: "npm start"}

	reg.Register("dev", "/home/user/app1", cfg)
	reg.Register("build", "/home/user/app1", cfg)
	reg.Register("dev", "/home/user/app2", cfg)

	all := reg.ListAll()
	assert.Len(t, all, 3)
}

func TestScriptEntry_CompareAndSwapState(t *testing.T) {
	reg := NewScriptRegistry()
	cfg := &config.ScriptConfig{Run: "npm start"}
	entry, _ := reg.Register("dev", "/home/user/myapp", cfg)

	// Valid transition: Idle -> Starting
	ok := entry.CompareAndSwapState(StateIdle, StateStarting)
	assert.True(t, ok)
	assert.Equal(t, StateStarting, entry.State())

	// Invalid transition: tries Idle -> Running but state is Starting
	ok = entry.CompareAndSwapState(StateIdle, StateRunning)
	assert.False(t, ok)
	assert.Equal(t, StateStarting, entry.State())

	// Valid: Starting -> Running
	ok = entry.CompareAndSwapState(StateStarting, StateRunning)
	assert.True(t, ok)
	assert.Equal(t, StateRunning, entry.State())
}

func TestScriptEntry_SetState(t *testing.T) {
	reg := NewScriptRegistry()
	cfg := &config.ScriptConfig{Run: "npm start"}
	entry, _ := reg.Register("dev", "/home/user/myapp", cfg)

	entry.SetState(StateRunning)
	assert.Equal(t, StateRunning, entry.State())

	entry.SetState(StateFailed)
	assert.Equal(t, StateFailed, entry.State())
}

func TestScriptEntry_StateHistoryBounded(t *testing.T) {
	reg := NewScriptRegistry()
	cfg := &config.ScriptConfig{Run: "npm start"}
	entry, _ := reg.Register("dev", "/home/user/myapp", cfg)

	// Initial state (Idle) is already 1 transition.
	// Add 110 more to exceed the cap of 100.
	for i := 0; i < 110; i++ {
		if i%2 == 0 {
			entry.SetState(StateRunning)
		} else {
			entry.SetState(StateIdle)
		}
	}

	history := entry.StateHistory()
	assert.LessOrEqual(t, len(history), maxStateTransitions)
}

func TestScriptEntry_StateHistoryRecordsTransitions(t *testing.T) {
	reg := NewScriptRegistry()
	cfg := &config.ScriptConfig{Run: "npm start"}
	entry, _ := reg.Register("dev", "/home/user/myapp", cfg)

	entry.CompareAndSwapState(StateIdle, StateStarting)
	entry.CompareAndSwapState(StateStarting, StateRunning)

	history := entry.StateHistory()
	require.Len(t, history, 3) // Idle (initial) + Starting + Running
	assert.Equal(t, StateIdle, history[0].State)
	assert.Equal(t, StateStarting, history[1].State)
	assert.Equal(t, StateRunning, history[2].State)
}

func TestScriptEntry_OutputRingBuffer(t *testing.T) {
	reg := NewScriptRegistry()
	cfg := &config.ScriptConfig{Run: "npm start"}
	entry, _ := reg.Register("dev", "/home/user/myapp", cfg)

	// Add some lines
	entry.AppendOutput("line 1")
	entry.AppendOutput("line 2")
	entry.AppendOutput("line 3")

	lines := entry.OutputLines()
	assert.Equal(t, []string{"line 1", "line 2", "line 3"}, lines)
	assert.Equal(t, 3, entry.OutputLen())
}

func TestScriptEntry_OutputRingBufferOverflow(t *testing.T) {
	reg := NewScriptRegistry()
	cfg := &config.ScriptConfig{Run: "npm start"}
	entry, _ := reg.Register("dev", "/home/user/myapp", cfg)

	// Fill beyond capacity
	for i := 0; i < maxOutputLines+500; i++ {
		entry.AppendOutput(fmt.Sprintf("line %d", i))
	}

	lines := entry.OutputLines()
	assert.Len(t, lines, maxOutputLines)

	// Oldest should be line 500 (the first 500 were evicted)
	assert.Equal(t, "line 500", lines[0])
	assert.Equal(t, fmt.Sprintf("line %d", maxOutputLines+499), lines[maxOutputLines-1])
}

func TestScriptEntry_RestartMarker(t *testing.T) {
	reg := NewScriptRegistry()
	cfg := &config.ScriptConfig{Run: "npm start"}
	entry, _ := reg.Register("dev", "/home/user/myapp", cfg)

	entry.AppendOutput("run1 output")
	entry.AddRestartMarker()
	entry.AppendOutput("run2 output")

	lines := entry.OutputLines()
	require.Len(t, lines, 3)
	assert.Equal(t, "run1 output", lines[0])
	assert.Equal(t, restartMarker, lines[1])
	assert.Equal(t, "run2 output", lines[2])
}

func TestScriptEntry_Counters(t *testing.T) {
	reg := NewScriptRegistry()
	cfg := &config.ScriptConfig{Run: "npm start"}
	entry, _ := reg.Register("dev", "/home/user/myapp", cfg)

	assert.Equal(t, int64(0), entry.StartCount())
	assert.Equal(t, int64(0), entry.FailCount())

	entry.IncrementStartCount()
	entry.IncrementStartCount()
	entry.IncrementFailCount()

	assert.Equal(t, int64(2), entry.StartCount())
	assert.Equal(t, int64(1), entry.FailCount())
}

func TestScriptEntry_ResolvedCommand(t *testing.T) {
	reg := NewScriptRegistry()
	cfg := &config.ScriptConfig{Run: "npm start"}
	entry, _ := reg.Register("dev", "/home/user/myapp", cfg)

	entry.SetResolvedCommand("sh", []string{"-c", "npm start"})
	cmd, args := entry.ResolvedCommand()
	assert.Equal(t, "sh", cmd)
	assert.Equal(t, []string{"-c", "npm start"}, args)
}

func TestScriptEntry_Sessions(t *testing.T) {
	reg := NewScriptRegistry()
	cfg := &config.ScriptConfig{Run: "npm start"}
	entry, _ := reg.Register("dev", "/home/user/myapp", cfg)

	entry.AddSession("claude-1")
	entry.AddSession("claude-2")

	sessions := entry.ListSessions()
	assert.Len(t, sessions, 2)
	assert.Contains(t, sessions, "claude-1")
	assert.Contains(t, sessions, "claude-2")

	entry.RemoveSession("claude-1")
	sessions = entry.ListSessions()
	assert.Len(t, sessions, 1)
	assert.Contains(t, sessions, "claude-2")
}

func TestScriptEntry_SessionSharing(t *testing.T) {
	reg := NewScriptRegistry()
	cfg := &config.ScriptConfig{Run: "npm start"}

	entry1, _ := reg.Register("dev", "/home/user/myapp", cfg)
	entry2, _ := reg.Register("dev", "/home/user/myapp", cfg)

	// Both registrations return the same entry
	entry1.AddSession("session-a")

	sessions := entry2.ListSessions()
	assert.Contains(t, sessions, "session-a")
}

func TestScriptEntry_ConcurrentOutputWrites(t *testing.T) {
	reg := NewScriptRegistry()
	cfg := &config.ScriptConfig{Run: "npm start"}
	entry, _ := reg.Register("dev", "/home/user/myapp", cfg)

	var wg sync.WaitGroup
	for g := 0; g < 10; g++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				entry.AppendOutput(fmt.Sprintf("goroutine-%d-line-%d", id, i))
			}
		}(g)
	}
	wg.Wait()

	lines := entry.OutputLines()
	assert.Equal(t, 1000, len(lines))
}

func TestScriptEntry_ConcurrentStateTransitions(t *testing.T) {
	reg := NewScriptRegistry()
	cfg := &config.ScriptConfig{Run: "npm start"}
	entry, _ := reg.Register("dev", "/home/user/myapp", cfg)

	var wg sync.WaitGroup
	// Multiple goroutines racing to CAS
	for g := 0; g < 10; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			entry.CompareAndSwapState(StateIdle, StateStarting)
		}()
	}
	wg.Wait()

	// Exactly one should have won the CAS
	assert.Equal(t, StateStarting, entry.State())
}

func TestScriptRegistry_IsolationByProject(t *testing.T) {
	reg := NewScriptRegistry()
	cfg := &config.ScriptConfig{Run: "npm start"}

	entry1, _ := reg.Register("dev", "/project/a", cfg)
	entry2, _ := reg.Register("dev", "/project/b", cfg)

	assert.NotSame(t, entry1, entry2, "different projects must have separate entries")

	entry1.SetState(StateRunning)
	assert.Equal(t, StateIdle, entry2.State(), "state must be isolated per project")
}

func TestScriptEntry_Ownership(t *testing.T) {
	reg := NewScriptRegistry()
	cfg := &config.ScriptConfig{Run: "npm start"}
	entry, _ := reg.Register("dev", "/home/user/myapp", cfg)

	// Initially unowned
	assert.Equal(t, "", entry.Owner())

	// Set owner
	entry.SetOwner("session-1")
	assert.Equal(t, "session-1", entry.Owner())

	// Change owner
	entry.SetOwner("session-2")
	assert.Equal(t, "session-2", entry.Owner())
}

func TestScriptEntry_ObserverCount(t *testing.T) {
	reg := NewScriptRegistry()
	cfg := &config.ScriptConfig{Run: "npm start"}
	entry, _ := reg.Register("dev", "/home/user/myapp", cfg)

	assert.Equal(t, 0, entry.ObserverCount())

	entry.AddSession("s1")
	assert.Equal(t, 1, entry.ObserverCount())

	entry.AddSession("s2")
	entry.AddSession("s3")
	assert.Equal(t, 3, entry.ObserverCount())

	entry.RemoveSession("s2")
	assert.Equal(t, 2, entry.ObserverCount())
}

func TestScriptEntry_TransferOwnership(t *testing.T) {
	reg := NewScriptRegistry()
	cfg := &config.ScriptConfig{Run: "npm start"}
	entry, _ := reg.Register("dev", "/home/user/myapp", cfg)

	entry.SetOwner("owner-1")
	entry.AddSession("observer-a")
	entry.AddSession("observer-b")

	// Remove owner from observers (simulating disconnect)
	// Transfer ownership should pick one of the remaining observers
	newOwner := entry.TransferOwnership()
	assert.NotEmpty(t, newOwner)
	assert.Equal(t, newOwner, entry.Owner())
	assert.Contains(t, []string{"observer-a", "observer-b"}, newOwner)
}

func TestScriptEntry_TransferOwnershipNoObservers(t *testing.T) {
	reg := NewScriptRegistry()
	cfg := &config.ScriptConfig{Run: "npm start"}
	entry, _ := reg.Register("dev", "/home/user/myapp", cfg)

	entry.SetOwner("owner-1")

	// No observers — transfer clears ownership
	newOwner := entry.TransferOwnership()
	assert.Equal(t, "", newOwner)
	assert.Equal(t, "", entry.Owner())
}

func TestScriptEntry_ConcurrentStartProtection(t *testing.T) {
	reg := NewScriptRegistry()
	cfg := &config.ScriptConfig{Run: "npm start"}
	entry, _ := reg.Register("dev", "/home/user/myapp", cfg)

	// Simulate two sessions racing to start the same script via CAS
	var wg sync.WaitGroup
	winners := make(chan int, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			if entry.CompareAndSwapState(StateIdle, StateStarting) {
				winners <- id
			}
		}(i)
	}
	wg.Wait()
	close(winners)

	// Exactly one goroutine should win the CAS
	var winnerIDs []int
	for id := range winners {
		winnerIDs = append(winnerIDs, id)
	}
	assert.Len(t, winnerIDs, 1, "exactly one goroutine should win the CAS")
	assert.Equal(t, StateStarting, entry.State())
}

func TestScriptEntry_OwnershipAtomicity(t *testing.T) {
	reg := NewScriptRegistry()
	cfg := &config.ScriptConfig{Run: "npm start"}
	entry, _ := reg.Register("dev", "/home/user/myapp", cfg)

	// Concurrent ownership changes should not panic or corrupt
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			code := fmt.Sprintf("session-%d", id)
			entry.SetOwner(code)
			entry.Owner()
		}(i)
	}
	wg.Wait()

	// Owner should be one of the sessions
	owner := entry.Owner()
	assert.NotEmpty(t, owner)
}
