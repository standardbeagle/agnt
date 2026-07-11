package sshclient

import (
	"fmt"

	"github.com/standardbeagle/agnt/internal/updater"
)

// versionsCompatible defines the explicit compatibility window for task 05's
// connect-time bootstrap check: local and remote agnt versions are
// compatible (no upgrade offered) when they share the same major.minor.
// Patch releases are bugfix-only in this project's versioning convention
// (see scripts/release.sh), so the CLI surface and wire contracts this
// command depends on (RemoteAttachCommand's flags, 'agnt daemon
// socket-path', the exec-channel upload commands in bootstrap_upload.go)
// are stable within a minor release. A major or minor mismatch means the
// remote's flags/verbs may have changed shape, so bootstrap offers an
// upgrade rather than risk a broken session.
func versionsCompatible(local, remote string) (bool, error) {
	lMaj, lMin, _, err := updater.ParseVersion(local)
	if err != nil {
		return false, fmt.Errorf("sshclient: parsing local version %q: %w", local, err)
	}
	rMaj, rMin, _, err := updater.ParseVersion(remote)
	if err != nil {
		return false, fmt.Errorf("sshclient: parsing remote version %q: %w", remote, err)
	}
	return lMaj == rMaj && lMin == rMin, nil
}
