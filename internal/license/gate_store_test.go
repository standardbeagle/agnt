package license

import (
	"errors"
	"testing"
	"time"
)

func TestStoreRoundTrip(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	if _, err := Load(); !errors.Is(err, ErrNoLicense) {
		t.Errorf("Load empty: want ErrNoLicense, got %v", err)
	}
	blob := "SOME-LICENSE-BLOB"
	if err := Save(blob); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got != blob {
		t.Errorf("Load: got %q want %q", got, blob)
	}
	if err := Remove(); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := Load(); !errors.Is(err, ErrNoLicense) {
		t.Errorf("Load after Remove: want ErrNoLicense, got %v", err)
	}
	// Remove on absent file is not an error.
	if err := Remove(); err != nil {
		t.Errorf("Remove absent: %v", err)
	}
}

func TestManagerCheck(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	priv := testKeypair(t)

	// Freeze clock for deterministic grace evaluation.
	fixed := time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)
	prevNow := now
	now = func() time.Time { return fixed }
	t.Cleanup(func() { now = prevNow })

	install := func(t *testing.T, expiry time.Time, caps []string) {
		t.Helper()
		p := samplePayload(expiry)
		p.Capabilities = caps
		blob, err := Mint(priv, p)
		if err != nil {
			t.Fatalf("Mint: %v", err)
		}
		if err := Save(blob); err != nil {
			t.Fatalf("Save: %v", err)
		}
	}

	t.Run("missing license blocks", func(t *testing.T) {
		_ = Remove()
		m := NewManager()
		_, err := m.Check(CapWholeSite)
		var ge *GateError
		if !errors.As(err, &ge) || ge.State != StateMissing {
			t.Errorf("want GateError/Missing, got %v", err)
		}
	})

	t.Run("valid grants capability", func(t *testing.T) {
		install(t, fixed.Add(30*24*time.Hour), []string{string(CapWholeSite)})
		m := NewManager()
		warn, err := m.Check(CapWholeSite)
		if err != nil {
			t.Fatalf("Check valid: %v", err)
		}
		if warn != "" {
			t.Errorf("valid should not warn, got %q", warn)
		}
	})

	t.Run("valid but capability not granted", func(t *testing.T) {
		install(t, fixed.Add(30*24*time.Hour), []string{string(CapWholeSite)})
		m := NewManager()
		_, err := m.Check(CapAdvancedTesting)
		var ge *GateError
		if !errors.As(err, &ge) || ge.State != StateValid {
			t.Errorf("want GateError/Valid, got %v", err)
		}
	})

	t.Run("grace allows with warning", func(t *testing.T) {
		install(t, fixed.Add(-2*24*time.Hour), []string{string(CapWholeSite)})
		m := NewManager()
		warn, err := m.Check(CapWholeSite)
		if err != nil {
			t.Fatalf("Check grace: %v", err)
		}
		if warn == "" {
			t.Error("grace should warn")
		}
	})

	t.Run("expired blocks", func(t *testing.T) {
		install(t, fixed.Add(-30*24*time.Hour), []string{string(CapWholeSite)})
		m := NewManager()
		_, err := m.Check(CapWholeSite)
		var ge *GateError
		if !errors.As(err, &ge) || ge.State != StateExpired {
			t.Errorf("want GateError/Expired, got %v", err)
		}
	})

	t.Run("invalid blob blocks", func(t *testing.T) {
		if err := Save("garbage-not-a-license"); err != nil {
			t.Fatalf("Save: %v", err)
		}
		m := NewManager()
		_, err := m.Check(CapWholeSite)
		var ge *GateError
		if !errors.As(err, &ge) || ge.State != StateInvalid {
			t.Errorf("want GateError/Invalid, got %v", err)
		}
	})

	t.Run("reload picks up new license", func(t *testing.T) {
		_ = Remove()
		m := NewManager()
		if _, err := m.Check(CapWholeSite); err == nil {
			t.Fatal("expected block before install")
		}
		install(t, fixed.Add(30*24*time.Hour), []string{string(CapWholeSite)})
		if err := m.Reload(); err != nil {
			t.Fatalf("Reload: %v", err)
		}
		if _, err := m.Check(CapWholeSite); err != nil {
			t.Errorf("after reload: %v", err)
		}
	})
}
