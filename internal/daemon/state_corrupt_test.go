package daemon

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStateManagerKeepsCorruptFileAndReportsLoadError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	sm := NewStateManager(StateManagerConfig{StatePath: path, AutoLoad: true})
	defer sm.Close()

	if sm.LoadError() == nil {
		t.Fatal("corrupt state file produced no LoadError")
	}
	if matches, _ := filepath.Glob(path + ".corrupt-*"); len(matches) != 1 {
		t.Fatalf("corrupt file not preserved: %v", matches)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("corrupt file left in place to be overwritten")
	}
}
