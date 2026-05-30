package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/standardbeagle/agnt/internal/config"
	"github.com/standardbeagle/agnt/internal/debug"
)

// firstRunMarker records when the `agnt run` first-run setup nudge last
// fired for a project. It is stored per-project under XDG state (NOT in the
// repo) so the nudge survives across clones and is suppressed within the
// re-nudge TTL after a negative outcome.
type firstRunMarker struct {
	// LastNudge is the timestamp the setup nudge last fired. Stored in UTC.
	LastNudge time.Time `json:"last_nudge"`

	// Permanent marks a positive outcome (the setup run wrote `.agnt.kdl`).
	// A permanent marker suppresses the nudge forever, independent of the
	// re-nudge TTL. A negative outcome leaves this false so the timestamped
	// marker only suppresses the nudge until the TTL elapses.
	Permanent bool `json:"permanent,omitempty"`
}

// setupGateDecision is the outcome of the first-run gate.
type setupGateDecision int

const (
	// skipSetup launches the coding agent normally.
	skipSetup setupGateDecision = iota
	// enterSetup drives the one-time setup phase first.
	enterSetup
)

// decideSetupGate decides whether `agnt run` should enter the one-time setup
// phase. It is pure so the gate logic is table-testable without touching the
// filesystem or a PTY.
//
//   - A present `.agnt.kdl` (hasConfig) always skips setup — the project is
//     already configured.
//   - With no config and no marker, setup fires (genuine first run).
//   - A permanent marker (positive outcome) suppresses the nudge forever.
//   - With no config and a timestamped marker, setup fires only once the
//     marker is older than the re-nudge TTL; within the TTL the user already
//     declined.
func decideSetupGate(hasConfig bool, marker *firstRunMarker, now time.Time, ttl time.Duration) setupGateDecision {
	if hasConfig {
		return skipSetup
	}
	if marker == nil {
		return enterSetup
	}
	if marker.Permanent {
		return skipSetup
	}
	if now.Sub(marker.LastNudge) >= ttl {
		return enterSetup
	}
	return skipSetup
}

// setupOutcomeMarker builds the marker to persist after a setup run, given
// whether the run produced a config. A positive outcome (`.agnt.kdl` written)
// is permanent; a negative outcome is a timestamped marker that re-nudges
// after the TTL.
func setupOutcomeMarker(positive bool, now time.Time) firstRunMarker {
	return firstRunMarker{LastNudge: now.UTC(), Permanent: positive}
}

// firstRunDeps are the injected dependencies for runFirstRunFlow. The callback
// shape keeps the orchestration pure and table-testable: real wiring supplies
// PTY launches + a pgid reaper, tests supply fakes.
type firstRunDeps struct {
	now         time.Time
	ttl         time.Duration
	hasConfig   func() bool                     // re-evaluated for the post-setup outcome read
	readMarker  func() (*firstRunMarker, error) // load the current marker
	writeMarker func(firstRunMarker) error      // persist the outcome marker
	args        []string                        // original argv, replayed verbatim into both phases
	launch      func(setupPhase bool, args []string) (pgid int, err error)
	reap        func(pgid int) // reap the setup child's pgid before phase 2
}

// runFirstRunFlow drives the optional two-phase setup→relaunch for `agnt run`.
//
//   - Gate skips → a single coding-phase launch (autostart on), args replayed
//     verbatim.
//   - Gate fires → setup-phase launch, reap its pgid, read the outcome
//     (`.agnt.kdl` present?), persist the marker (positive=permanent,
//     negative=timestamped TTL). On a positive outcome relaunch the coding
//     phase with the same args; a negative outcome stops after setup (the user
//     declined; the timestamped marker suppresses the nudge until the TTL).
//
// Phase 2 reuses d.args by value, so the relaunched child's argv is
// byte-identical to the original — no separate capture/quote round-trip.
func runFirstRunFlow(d firstRunDeps) error {
	marker, _ := d.readMarker()
	if decideSetupGate(d.hasConfig(), marker, d.now, d.ttl) == skipSetup {
		_, err := d.launch(false, d.args)
		return err
	}

	pgid, err := d.launch(true, d.args)
	if err != nil {
		return err
	}
	// Reap the setup child's process group before phase 2 so backgrounded
	// jobs (npm run dev &, etc.) can't hold ports the coding child needs.
	d.reap(pgid)

	positive := d.hasConfig()
	if err := d.writeMarker(setupOutcomeMarker(positive, d.now)); err != nil {
		debug.Log("run", "failed to write first-run marker: %v", err)
	}
	if !positive {
		return nil
	}
	_, err = d.launch(false, d.args)
	return err
}

// renudgeTTLForProject loads the re-nudge TTL from the project config, falling
// back to the default when no config exists (the gate-firing case) or a load
// error occurs. Mirrors the nil-safe pattern used elsewhere in this package.
func renudgeTTLForProject(projectPath string) time.Duration {
	configPath := config.FindAgntConfigFile(projectPath)
	cfg := config.DefaultAgntConfig()
	if configPath != "" {
		if loaded, err := config.LoadAgntConfigFile(configPath); err == nil && loaded != nil {
			cfg = loaded
		}
	}
	return cfg.Setup.RenudgeTTL()
}

// firstRunStatePath returns the per-project marker path under XDG state,
// honoring XDG_STATE_HOME and falling back to ~/.local/state (then the OS
// temp dir as a last resort) — matching daemon.GetLogPath's layout. The
// project is keyed by a hash of its absolute path so distinct checkouts get
// distinct markers and the filename never collides with sibling projects.
func firstRunStatePath(projectPath string) string {
	abs, err := filepath.Abs(projectPath)
	if err != nil {
		abs = projectPath
	}
	sum := sha256.Sum256([]byte(abs))
	key := hex.EncodeToString(sum[:])[:16]

	stateHome := os.Getenv("XDG_STATE_HOME")
	if stateHome == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return filepath.Join(os.TempDir(), "agnt", "firstrun", key)
		}
		stateHome = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(stateHome, "agnt", "firstrun", key)
}

// readFirstRunMarker loads the marker at path. An absent or corrupt file is
// reported as "no marker" (nil, nil) — both mean the same thing to the gate:
// the user has not been nudged, so treat it as a first run. Only an
// unexpected IO error (e.g. permission denied) is surfaced.
func readFirstRunMarker(path string) (*firstRunMarker, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var m firstRunMarker
	if err := json.Unmarshal(data, &m); err != nil {
		// Corrupt marker — treat as absent rather than wedging the run.
		return nil, nil
	}
	if m.LastNudge.IsZero() {
		return nil, nil
	}
	return &m, nil
}

// writeFirstRunMarker persists the marker at path, creating parent dirs as
// needed. The write is atomic (temp file + rename) so a crash mid-write never
// leaves a corrupt marker that would mask a genuine re-nudge.
func writeFirstRunMarker(path string, m firstRunMarker) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
