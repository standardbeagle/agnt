package chromedp

import (
	"os"
	"path/filepath"
	"testing"
)

// touchOf returns the touch value a step list sets, and how many steps set it.
func touchOf(steps []emulationStep) (value bool, count int) {
	for _, s := range steps {
		if s.touch != nil {
			value = *s.touch
			count++
		}
	}
	return value, count
}

// TestEmulateSteps_TouchIsSetExplicitlyForEveryPreset pins the fix for touch
// emulation leaking between captures: a preset used to send the touch command
// only when it wanted touch ON, so once a mobile capture had run, every later
// desktop capture in the session kept a coarse pointer. Every preset must now
// state its touch value, in both directions.
func TestEmulateSteps_TouchIsSetExplicitlyForEveryPreset(t *testing.T) {
	for i := range DefaultViewports {
		v := &DefaultViewports[i]
		steps := v.emulateSteps()

		touch, n := touchOf(steps)
		if n != 1 {
			t.Errorf("%s: touch emulation set %d times, want exactly once", v.Name, n)
			continue
		}
		if touch != v.Touch {
			t.Errorf("%s: touch emulation set to %v, want %v", v.Name, touch, v.Touch)
		}

		var metrics int
		for _, s := range steps {
			if s.clear {
				t.Errorf("%s: applying a preset must not clear the metrics override", v.Name)
			}
			if s.metrics != nil {
				metrics++
				if s.metrics != v {
					t.Errorf("%s: metrics step carries a different preset", v.Name)
				}
			}
		}
		if metrics != 1 {
			t.Errorf("%s: device metrics set %d times, want once", v.Name, metrics)
		}
		if len(v.ToEmulateActions()) != len(steps) {
			t.Errorf("%s: ToEmulateActions length differs from its steps", v.Name)
		}
	}

	// The case that used to leak, stated outright.
	desktop := GetViewport("desktop")
	if desktop == nil || desktop.Touch {
		t.Fatal("desktop preset missing or touch-enabled; this test needs a non-touch preset")
	}
	if touch, n := touchOf(desktop.emulateSteps()); n != 1 || touch {
		t.Errorf("desktop preset must send touch=false (got value=%v, steps=%d)", touch, n)
	}
}

// TestResetEmulationSteps_ClearsMetricsAndTouch: restoring after a capture
// removes the metrics override and turns touch off.
func TestResetEmulationSteps_ClearsMetricsAndTouch(t *testing.T) {
	steps := resetEmulationSteps()
	var cleared bool
	for _, s := range steps {
		if s.clear {
			cleared = true
		}
		if s.metrics != nil {
			t.Error("reset must not install a metrics override")
		}
	}
	if !cleared {
		t.Error("reset must clear the device-metrics override")
	}
	if touch, n := touchOf(steps); n != 1 || touch {
		t.Errorf("reset must send touch=false exactly once (got value=%v, steps=%d)", touch, n)
	}
	if len(ResetEmulationActions()) != len(steps) {
		t.Error("ResetEmulationActions length differs from its steps")
	}
}

// TestApplyViewport_NilPresetIsANoOp: a capture without a viewport must not
// touch emulation at all — it would otherwise reset an override it never set.
// The session is not running, so any Run would return an error.
func TestApplyViewport_NilPresetIsANoOp(t *testing.T) {
	s := NewSession(SessionConfig{ID: "t"})
	restore, err := applyViewport(s, nil)
	if err != nil {
		t.Fatalf("applyViewport(nil) = %v", err)
	}
	if err := restore(); err != nil {
		t.Errorf("restore after a nil preset ran a command: %v", err)
	}
}

// TestFinishCapture_RestoreFailureFailsTheCapture: a screenshot that was taken
// but left the session emulated is an error, not a quiet success.
func TestFinishCapture_RestoreFailureFailsTheCapture(t *testing.T) {
	s := NewSession(SessionConfig{ID: "t"}) // not running: every Run fails
	restoreFails := func() error { return s.Run(ResetEmulationActions()...) }

	result := &ScreenshotResult{Path: "x.png"}
	var err error
	finishCapture(restoreFails, &result, &err)
	if err == nil || result != nil {
		t.Errorf("restore failure must replace the result with an error (result=%v err=%v)", result, err)
	}

	// An earlier capture error is the one reported.
	result, err = nil, os.ErrNotExist
	finishCapture(restoreFails, &result, &err)
	if err != os.ErrNotExist {
		t.Errorf("restore failure overwrote the capture error: %v", err)
	}
}

// TestReserveScreenshotPath_SameSecondSameLabelIsUnique: the filename's
// timestamp has one-second resolution, so repeated captures with the same
// stem inside a second used to share a path and overwrite each other.
func TestReserveScreenshotPath_SameSecondSameLabelIsUnique(t *testing.T) {
	dir := t.TempDir()
	const stem = "viewport-home-desktop-20260101-120000"

	seen := map[string]bool{}
	for i := 0; i < 5; i++ {
		p := reserveScreenshotPath(dir, stem)
		if seen[p] {
			t.Fatalf("path %s issued twice", p)
		}
		seen[p] = true
		if filepath.Dir(p) != dir || filepath.Ext(p) != ".png" {
			t.Errorf("unexpected path %s", p)
		}
	}
	if !seen[filepath.Join(dir, stem+".png")] {
		t.Error("the first capture must keep the plain name")
	}
	if !seen[filepath.Join(dir, stem+"-2.png")] {
		t.Error("the second capture must get the -2 suffix")
	}

	// A file already on disk (written before a restart) is not overwritten.
	onDisk := filepath.Join(dir, "clip-x-20260101-120001.png")
	if err := os.WriteFile(onDisk, []byte("png"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := reserveScreenshotPath(dir, "clip-x-20260101-120001"); got == onDisk {
		t.Errorf("reserved a path that already exists on disk: %s", got)
	}
}
