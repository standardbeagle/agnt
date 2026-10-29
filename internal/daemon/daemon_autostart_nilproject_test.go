//go:build unix

package daemon

import (
	"context"
	"testing"
)

// TestRunAutostartNonInteractive_NoProjectBlock pins the non-interactive
// autostart path against a nil dereference when .agnt.kdl declares scripts
// but no `project {}` block. EffectivePortConflictPolicy() defaults an
// unset Project to "prompt", and the non-interactive override path writes
// agntConfig.Project.PortConflict = "skip" straight into that pointer —
// which is nil whenever the config never declared a project block. Any
// autostart-eligible script with no project block reproduces this: the
// daemon must degrade to "skip" (documented non-interactive fallback), not
// panic.
func TestRunAutostartNonInteractive_NoProjectBlock(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, clusterAConfig()) // no `project {}` block

	d := newDaemon(t, dir)

	result := d.RunAutostartNonInteractive(context.Background(), dir)

	if result == nil {
		t.Fatal("RunAutostartNonInteractive returned nil result")
	}
	if len(result.Errors) != 0 {
		t.Errorf("expected no errors, got: %v", result.Errors)
	}
}
