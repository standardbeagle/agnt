package daemon

import (
	"sync"
	"testing"
	"time"

	"github.com/standardbeagle/agnt/internal/platform"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPathMatches(t *testing.T) {
	tests := []struct {
		name        string
		projectPath string
		procCwd     string
		want        bool
	}{
		{"exact match", "/home/user/project", "/home/user/project", true},
		{"subdirectory", "/home/user/project", "/home/user/project/src", true},
		{"different project", "/home/user/project", "/home/user/other", false},
		{"empty cwd", "/home/user/project", "", false},
		{"empty project", "", "/home/user/project", false},
		{"sibling prefix", "/home/user/proj", "/home/user/project", false},
		{"both empty", "", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := pathMatches(tt.projectPath, tt.procCwd)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestDevServerCommandsList(t *testing.T) {
	// Verify all required dev server commands are in the map
	required := []string{
		"node", "npm", "npx", "vite", "next", "webpack", "ts-node", "nodemon", "turbo",
		"go", "air",
		"dotnet",
		"flask", "uvicorn", "gunicorn", "python", "python3", "django",
		"cargo",
		"rails", "foreman", "bundle",
		"java", "mvn", "gradle",
	}

	for _, cmd := range required {
		_, ok := devServerCommands[cmd]
		assert.True(t, ok, "missing dev server command: %s", cmd)
	}
}

func TestDuplicateScannerNotify(t *testing.T) {
	// Test that the notification message is formatted correctly
	scanner := &DuplicateScanner{}

	var capturedMsg string
	scanner.OnNotify = func(msg string) {
		capturedMsg = msg
	}

	result := &CleanupResult{
		Killed: []KilledProcess{
			{PID: 12345, Command: "node", Reason: "duplicate node"},
			{PID: 12346, Command: "node", Reason: "duplicate node"},
			{PID: 12350, Command: "dotnet", Reason: "duplicate dotnet"},
		},
	}

	scanner.notify(result)

	assert.Contains(t, capturedMsg, "[agnt] cleaned up 3 duplicate process(es)")
	assert.Contains(t, capturedMsg, "dotnet (PID 12350)")
	assert.Contains(t, capturedMsg, "node (PID 12345, PID 12346)")
}

func TestDuplicateScannerNotifyEmpty(t *testing.T) {
	scanner := &DuplicateScanner{}
	called := false
	scanner.OnNotify = func(msg string) {
		called = true
	}

	scanner.notify(&CleanupResult{})
	assert.False(t, called, "notify should not be called for empty result")
}

func TestFindDuplicates_GroupsByCommand(t *testing.T) {
	scanner := &DuplicateScanner{}
	procs := []platform.ProcInfo{
		{PID: 100, Command: "node", Cwd: "/home/user/project"},
		{PID: 200, Command: "node", Cwd: "/home/user/project"},
		{PID: 300, Command: "vite", Cwd: "/home/user/project"},
	}
	managed := map[int]bool{}

	groups := scanner.findDuplicates(procs, "/home/user/project", managed)

	// Only "node" has duplicates (2 instances), "vite" is a single instance
	require.Len(t, groups, 1)
	assert.Equal(t, "node", groups[0].Command)
	assert.Len(t, groups[0].Procs, 2)
}

func TestFindDuplicates_SkipsManagedOnlyGroups(t *testing.T) {
	scanner := &DuplicateScanner{}
	procs := []platform.ProcInfo{
		{PID: 100, Command: "node", Cwd: "/home/user/project"},
		{PID: 200, Command: "node", Cwd: "/home/user/project"},
	}
	// Both are managed — no duplicates to kill
	managed := map[int]bool{100: true, 200: true}

	groups := scanner.findDuplicates(procs, "/home/user/project", managed)
	assert.Empty(t, groups)
}

func TestFindDuplicates_SortsByPIDAscending(t *testing.T) {
	scanner := &DuplicateScanner{}
	procs := []platform.ProcInfo{
		{PID: 500, Command: "node", Cwd: "/home/user/project"},
		{PID: 100, Command: "node", Cwd: "/home/user/project"},
		{PID: 300, Command: "node", Cwd: "/home/user/project"},
	}
	managed := map[int]bool{}

	groups := scanner.findDuplicates(procs, "/home/user/project", managed)
	require.Len(t, groups, 1)

	// Should be sorted by PID ascending (oldest first)
	assert.Equal(t, 100, groups[0].Procs[0].PID)
	assert.Equal(t, 300, groups[0].Procs[1].PID)
	assert.Equal(t, 500, groups[0].Procs[2].PID)
}

func TestKillDuplicates_KeepsOldestUnmanaged(t *testing.T) {
	scanner := &DuplicateScanner{}
	group := DuplicateGroup{
		Command: "node",
		Path:    "/home/user/project",
		Procs: []platform.ProcInfo{
			{PID: 100, Command: "node"}, // oldest unmanaged — keep
			{PID: 200, Command: "node"}, // duplicate — kill
			{PID: 300, Command: "node"}, // duplicate — kill
		},
	}
	managed := map[int]bool{}

	// killDuplicates calls platform.KillPID which will fail for non-existent
	// PIDs, but the logic of selecting which to keep vs kill is testable.
	killed := scanner.killDuplicates(group, managed)

	// PID 100 should be kept (oldest). PIDs 200 and 300 would be killed
	// (may fail with "no such process" but that's fine for logic testing).
	var killedPIDs []int
	for _, k := range killed {
		killedPIDs = append(killedPIDs, k.PID)
	}
	assert.NotContains(t, killedPIDs, 100, "oldest unmanaged process should be kept")
}

func TestKillDuplicates_NeverKillsManaged(t *testing.T) {
	scanner := &DuplicateScanner{}
	group := DuplicateGroup{
		Command: "node",
		Path:    "/home/user/project",
		Procs: []platform.ProcInfo{
			{PID: 50, Command: "node"},  // managed — never kill
			{PID: 100, Command: "node"}, // oldest unmanaged — keep
			{PID: 200, Command: "node"}, // duplicate — kill
		},
	}
	managed := map[int]bool{50: true}

	killed := scanner.killDuplicates(group, managed)

	var killedPIDs []int
	for _, k := range killed {
		killedPIDs = append(killedPIDs, k.PID)
	}
	assert.NotContains(t, killedPIDs, 50, "managed process must never be killed")
	assert.NotContains(t, killedPIDs, 100, "oldest unmanaged should be kept")
}

func TestKillDuplicates_IsLockFree(t *testing.T) {
	// Verify killDuplicates can run while another goroutine holds the lock.
	// This confirms the scan/kill split works — kills don't need the mutex.
	scanner := &DuplicateScanner{}

	// Hold the lock in a goroutine
	scanner.mu.Lock()
	lockHeld := make(chan struct{})
	done := make(chan struct{})

	go func() {
		defer close(done)
		close(lockHeld)
		// killDuplicates should not block on the lock
		group := DuplicateGroup{
			Command: "node",
			Path:    "/home/user/project",
			Procs: []platform.ProcInfo{
				{PID: 100, Command: "node"},
				{PID: 200, Command: "node"},
			},
		}
		scanner.killDuplicates(group, map[int]bool{})
	}()

	<-lockHeld

	// If killDuplicates needed the lock, this would deadlock.
	// Give it a generous timeout to be safe.
	select {
	case <-done:
		// killDuplicates completed without the lock — correct behavior
	case <-time.After(5 * time.Second):
		t.Fatal("killDuplicates blocked — it should not require the mutex")
	}

	scanner.mu.Unlock()
}

func TestLastScanAt_ZeroBeforeFirstScan(t *testing.T) {
	scanner := &DuplicateScanner{}
	assert.True(t, scanner.LastScanAt().IsZero(), "lastScanAt should be zero before any scan")
}

func TestConcurrentLastScanAt_NoRace(t *testing.T) {
	// Verify concurrent reads of LastScanAt don't race with writes.
	scanner := &DuplicateScanner{}

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Simulate the Phase 3 timestamp update from ScanAndCleanup
			scanner.mu.Lock()
			scanner.lastScanAt = time.Now()
			scanner.mu.Unlock()
		}()
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = scanner.LastScanAt()
		}()
	}
	wg.Wait()
}

func TestFindDuplicates_FiltersNonDevCommands(t *testing.T) {
	scanner := &DuplicateScanner{}
	procs := []platform.ProcInfo{
		{PID: 100, Command: "bash", Cwd: "/home/user/project"},
		{PID: 200, Command: "bash", Cwd: "/home/user/project"},
	}
	managed := map[int]bool{}

	groups := scanner.findDuplicates(procs, "/home/user/project", managed)
	assert.Empty(t, groups, "non-dev-server commands should not produce duplicate groups")
}

func TestFindDuplicates_FiltersByProjectPath(t *testing.T) {
	scanner := &DuplicateScanner{}
	procs := []platform.ProcInfo{
		{PID: 100, Command: "node", Cwd: "/home/user/project-a"},
		{PID: 200, Command: "node", Cwd: "/home/user/project-b"},
	}
	managed := map[int]bool{}

	groups := scanner.findDuplicates(procs, "/home/user/project-a", managed)
	assert.Empty(t, groups, "processes from different projects should not be grouped")
}
