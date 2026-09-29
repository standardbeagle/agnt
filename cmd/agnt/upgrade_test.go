package main

import "testing"

// TestDecideUpgrade pins the semver-based upgrade decision: `agnt upgrade`
// must never treat a local dev/pre-release build (whose version string
// differs from the latest tag without being older) as something to
// "upgrade" back to the released version, and must never proceed against a
// same-or-newer local build unless --force is set.
func TestDecideUpgrade(t *testing.T) {
	cases := []struct {
		name        string
		local       string
		latest      string
		force       bool
		wantIsNewer bool
		wantProceed bool
		wantErr     bool
	}{
		{
			name:        "latest is newer, no force needed",
			local:       "1.2.3",
			latest:      "1.3.0",
			wantIsNewer: true,
			wantProceed: true,
		},
		{
			name:        "same version, refuses without force",
			local:       "1.2.3",
			latest:      "1.2.3",
			wantIsNewer: false,
			wantProceed: false,
		},
		{
			name:        "local ahead of latest (dev build), refuses without force",
			local:       "1.9.0",
			latest:      "1.3.0",
			wantIsNewer: false,
			wantProceed: false,
		},
		{
			name:        "local ahead of latest, force proceeds anyway",
			local:       "1.9.0",
			latest:      "1.3.0",
			force:       true,
			wantIsNewer: false,
			wantProceed: true,
		},
		{
			name:        "same version, force still reinstalls",
			local:       "1.2.3",
			latest:      "1.2.3",
			force:       true,
			wantIsNewer: false,
			wantProceed: true,
		},
		{
			name:    "unparseable latest version errors instead of silently upgrading",
			local:   "1.2.3",
			latest:  "not-a-version",
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			isNewer, proceed, err := decideUpgrade(tc.local, tc.latest, tc.force)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("decideUpgrade(%q, %q, %v): want error, got nil", tc.local, tc.latest, tc.force)
				}
				return
			}
			if err != nil {
				t.Fatalf("decideUpgrade(%q, %q, %v): unexpected error: %v", tc.local, tc.latest, tc.force, err)
			}
			if isNewer != tc.wantIsNewer {
				t.Errorf("isNewer = %v, want %v", isNewer, tc.wantIsNewer)
			}
			if proceed != tc.wantProceed {
				t.Errorf("proceed = %v, want %v", proceed, tc.wantProceed)
			}
		})
	}
}

// TestInstallMethodString locks the label used in `agnt upgrade` output.
func TestInstallMethodString(t *testing.T) {
	cases := map[installMethod]string{
		installNPM:     "npm",
		installPip:     "pip",
		installGo:      "go",
		installDirect:  "direct",
		installUnknown: "unknown",
	}
	for method, want := range cases {
		if got := method.String(); got != want {
			t.Errorf("installMethod(%d).String() = %q, want %q", method, got, want)
		}
	}
}
