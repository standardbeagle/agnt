package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDecideSetupGate sweeps the full decision matrix: config presence
// dominates, then marker presence, then marker age vs the TTL.
func TestDecideSetupGate(t *testing.T) {
	now := time.Date(2026, 5, 29, 12, 0, 0, 0, time.UTC)
	ttl := 7 * 24 * time.Hour

	fresh := &firstRunMarker{LastNudge: now.Add(-1 * time.Hour)}       // 1h old, within TTL
	atBoundary := &firstRunMarker{LastNudge: now.Add(-ttl)}            // exactly TTL old
	stale := &firstRunMarker{LastNudge: now.Add(-30 * 24 * time.Hour)} // 30d old

	cases := []struct {
		name      string
		hasConfig bool
		marker    *firstRunMarker
		want      setupGateDecision
	}{
		{"no-config-no-marker-enters", false, nil, enterSetup},
		{"marker-within-ttl-skips", false, fresh, skipSetup},
		{"marker-older-than-ttl-enters", false, stale, enterSetup},
		{"marker-exactly-ttl-enters", false, atBoundary, enterSetup}, // >= boundary fires
		{"config-present-skips-even-no-marker", true, nil, skipSetup},
		{"config-present-skips-even-stale-marker", true, stale, skipSetup},
		{"config-present-skips-even-fresh-marker", true, fresh, skipSetup},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := decideSetupGate(tc.hasConfig, tc.marker, now, ttl)
			assert.Equal(t, tc.want, got)
		})
	}
}

// TestFirstRunMarkerRoundTrip covers write→read fidelity plus the
// corrupt/absent → no-marker (no panic) contract.
func TestFirstRunMarkerRoundTrip(t *testing.T) {
	dir := t.TempDir()

	// Absent file → no marker, no error, no panic.
	absent := filepath.Join(dir, "absent")
	m, err := readFirstRunMarker(absent)
	require.NoError(t, err)
	assert.Nil(t, m)

	// Write then read returns the same instant.
	path := filepath.Join(dir, "sub", "marker") // sub dir must be created by writer
	want := time.Date(2026, 5, 1, 9, 30, 15, 0, time.UTC)
	require.NoError(t, writeFirstRunMarker(path, firstRunMarker{LastNudge: want}))

	got, err := readFirstRunMarker(path)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.True(t, want.Equal(got.LastNudge), "round-trip lost the timestamp: %v != %v", want, got.LastNudge)
	assert.Equal(t, want.Unix(), got.LastNudge.Unix())

	// No stray temp file left behind after the atomic rename.
	_, statErr := os.Stat(path + ".tmp")
	assert.True(t, os.IsNotExist(statErr), "temp file should be renamed away")

	// Corrupt JSON → no marker, no error, no panic.
	corrupt := filepath.Join(dir, "corrupt")
	require.NoError(t, os.WriteFile(corrupt, []byte("{not json"), 0o644))
	cm, err := readFirstRunMarker(corrupt)
	require.NoError(t, err)
	assert.Nil(t, cm)

	// Zero-timestamp marker is treated as absent (no nudge recorded).
	zero := filepath.Join(dir, "zero")
	require.NoError(t, os.WriteFile(zero, []byte(`{"last_nudge":"0001-01-01T00:00:00Z"}`), 0o644))
	zm, err := readFirstRunMarker(zero)
	require.NoError(t, err)
	assert.Nil(t, zm)
}

// TestFirstRunStatePath asserts per-project keying and the absence of any
// in-repo write target.
func TestFirstRunStatePath(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	a := firstRunStatePath("/home/user/projA")
	b := firstRunStatePath("/home/user/projB")
	assert.NotEqual(t, a, b, "distinct projects must get distinct markers")
	assert.Equal(t, a, firstRunStatePath("/home/user/projA"), "same project must be stable")
	assert.Contains(t, a, filepath.Join("agnt", "firstrun"))
	// Marker lives under XDG state, never inside the project tree.
	assert.NotContains(t, a, "projA"+string(os.PathSeparator))
}

// TestBuildSetupSystemPrompt asserts the Claude setup prompt carries the three
// load-bearing instructions: self-check, run the skill, request install.
func TestBuildSetupSystemPrompt(t *testing.T) {
	p := buildSetupSystemPrompt("claude")

	assert.Contains(t, p, "agnt:setup-project", "must name the setup skill")
	lower := strings.ToLower(p)
	assert.Contains(t, lower, "check", "must instruct a self-check")
	assert.Contains(t, lower, "install", "must instruct requesting install when skill absent")
	assert.Contains(t, lower, "run", "must instruct running the skill")
	assert.NotEmpty(t, p)

	// Empty adapter name degrades gracefully without panicking or leaving a
	// dangling format verb.
	def := buildSetupSystemPrompt("")
	assert.Contains(t, def, "agnt:setup-project")
	assert.NotContains(t, def, "%!", "no leftover format verbs")
}

// TestDecideSetupGatePermanentMarker covers the Slice B addition: a permanent
// marker (positive setup outcome) suppresses the nudge forever, even when the
// timestamp is far older than the TTL and no config is present.
func TestDecideSetupGatePermanentMarker(t *testing.T) {
	now := time.Date(2026, 5, 29, 12, 0, 0, 0, time.UTC)
	ttl := 7 * 24 * time.Hour
	ancient := &firstRunMarker{LastNudge: now.Add(-365 * 24 * time.Hour), Permanent: true}

	assert.Equal(t, skipSetup, decideSetupGate(false, ancient, now, ttl),
		"permanent marker must suppress the nudge regardless of age")
	// Sanity: same age WITHOUT permanent would re-nudge.
	notPerm := &firstRunMarker{LastNudge: ancient.LastNudge}
	assert.Equal(t, enterSetup, decideSetupGate(false, notPerm, now, ttl))
}

// TestSetupOutcomeMarker asserts positive→permanent, negative→timestamped-only,
// and that the round-trip through write/read preserves the Permanent flag.
func TestSetupOutcomeMarker(t *testing.T) {
	now := time.Date(2026, 5, 29, 8, 0, 0, 0, time.UTC)

	pos := setupOutcomeMarker(true, now)
	assert.True(t, pos.Permanent)
	assert.True(t, now.Equal(pos.LastNudge))

	neg := setupOutcomeMarker(false, now)
	assert.False(t, neg.Permanent)
	assert.True(t, now.Equal(neg.LastNudge))

	// Permanent flag survives the marker file round-trip.
	path := filepath.Join(t.TempDir(), "m")
	require.NoError(t, writeFirstRunMarker(path, pos))
	got, err := readFirstRunMarker(path)
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.True(t, got.Permanent)
}

// flowRec records the side effects of runFirstRunFlow so each scenario can
// assert launch count/order, replayed args, the reap-before-phase-2 ordering,
// and which marker was persisted.
type flowRec struct {
	events    []string
	launchArg [][]string
	written   []firstRunMarker
	reaped    []int
	notices   []string
}

// newFlowDeps builds a firstRunDeps wired to a flowRec. hasConfigSeq supplies
// successive return values for d.hasConfig() (gate read, then outcome read);
// setupPgid is returned from the setup-phase launch; setupErr aborts it.
func newFlowDeps(rec *flowRec, args []string, hasConfigSeq []bool, setupPgid int, setupErr error) firstRunDeps {
	hc := hasConfigSeq
	now := time.Date(2026, 5, 29, 10, 0, 0, 0, time.UTC)
	return firstRunDeps{
		now: now,
		ttl: 7 * 24 * time.Hour,
		hasConfig: func() bool {
			v := hc[0]
			hc = hc[1:]
			return v
		},
		readMarker:  func() (*firstRunMarker, error) { return nil, nil },
		writeMarker: func(m firstRunMarker) error { rec.written = append(rec.written, m); return nil },
		args:        args,
		launch: func(setupPhase bool, a []string) (int, error) {
			cp := append([]string(nil), a...)
			rec.launchArg = append(rec.launchArg, cp)
			if setupPhase {
				rec.events = append(rec.events, "launch:setup")
				return setupPgid, setupErr
			}
			rec.events = append(rec.events, "launch:coding")
			return 0, nil
		},
		reap: func(pgid int) {
			rec.events = append(rec.events, "reap")
			rec.reaped = append(rec.reaped, pgid)
		},
		notice: func(msg string) { rec.notices = append(rec.notices, msg) },
	}
}

// TestRunFirstRunFlow_SuppressedNudgeIsLoud pins that launching with no config
// under a still-valid negative marker tells the user why nothing autostarts,
// while a configured project and a permanent marker stay quiet.
func TestRunFirstRunFlow_SuppressedNudgeIsLoud(t *testing.T) {
	args := []string{"opencode"}
	now := time.Date(2026, 5, 29, 10, 0, 0, 0, time.UTC)

	t.Run("no config, declined within TTL", func(t *testing.T) {
		rec := &flowRec{}
		d := newFlowDeps(rec, args, []bool{false}, 0, nil)
		d.readMarker = func() (*firstRunMarker, error) {
			return &firstRunMarker{LastNudge: now.Add(-24 * time.Hour)}, nil
		}
		require.NoError(t, runFirstRunFlow(d))
		assert.Equal(t, []string{"launch:coding"}, rec.events)
		require.Len(t, rec.notices, 1)
		assert.Contains(t, rec.notices[0], ".agnt.kdl")
		assert.Contains(t, rec.notices[0], "agnt init")
		assert.Contains(t, rec.notices[0], "24h0m0s ago")
	})

	t.Run("config present stays quiet", func(t *testing.T) {
		rec := &flowRec{}
		require.NoError(t, runFirstRunFlow(newFlowDeps(rec, args, []bool{true}, 0, nil)))
		assert.Empty(t, rec.notices)
	})

	t.Run("no config, permanent marker", func(t *testing.T) {
		rec := &flowRec{}
		d := newFlowDeps(rec, args, []bool{false}, 0, nil)
		d.readMarker = func() (*firstRunMarker, error) { return &firstRunMarker{Permanent: true}, nil }
		require.NoError(t, runFirstRunFlow(d))
		require.Len(t, rec.notices, 1, "a permanent marker with no config still means nothing autostarts")
		assert.NotContains(t, rec.notices[0], "declined")
	})
}

func TestRunFirstRunFlow(t *testing.T) {
	args := []string{"claude", "-p", "do the thing"}

	t.Run("gate-skip-single-coding-launch", func(t *testing.T) {
		rec := &flowRec{}
		// hasConfig true at gate → skip; outcome read never happens.
		require.NoError(t, runFirstRunFlow(newFlowDeps(rec, args, []bool{true}, 0, nil)))
		assert.Equal(t, []string{"launch:coding"}, rec.events)
		assert.Empty(t, rec.written, "skip path writes no marker")
		assert.Empty(t, rec.reaped, "skip path reaps nothing")
		assert.Equal(t, args, rec.launchArg[0], "args replayed verbatim")
	})

	t.Run("positive-outcome-relaunches-with-reap-between", func(t *testing.T) {
		rec := &flowRec{}
		// gate=false (fire), outcome=true (config written) → relaunch.
		require.NoError(t, runFirstRunFlow(newFlowDeps(rec, args, []bool{false, true}, 4242, nil)))
		assert.Equal(t, []string{"launch:setup", "reap", "launch:coding"}, rec.events,
			"setup → reap → coding, in that order")
		require.Len(t, rec.written, 1)
		assert.True(t, rec.written[0].Permanent, "positive outcome writes permanent marker")
		assert.Equal(t, []int{4242}, rec.reaped, "setup child's pgid is reaped")
		// Byte-identical replay across both phases.
		require.Len(t, rec.launchArg, 2)
		assert.Equal(t, args, rec.launchArg[0])
		assert.Equal(t, args, rec.launchArg[1])
		assert.Equal(t, rec.launchArg[0], rec.launchArg[1])
	})

	t.Run("negative-outcome-stops-after-setup", func(t *testing.T) {
		rec := &flowRec{}
		// gate=false (fire), outcome=false (declined) → no relaunch.
		require.NoError(t, runFirstRunFlow(newFlowDeps(rec, args, []bool{false, false}, 7, nil)))
		assert.Equal(t, []string{"launch:setup", "reap"}, rec.events, "no coding relaunch on negative outcome")
		require.Len(t, rec.written, 1)
		assert.False(t, rec.written[0].Permanent, "negative outcome writes timestamped marker")
	})

	t.Run("setup-launch-error-aborts-before-reap-and-marker", func(t *testing.T) {
		rec := &flowRec{}
		// Only the gate's hasConfig() runs — the error exits before the
		// outcome read, so a single-element sequence suffices.
		err := runFirstRunFlow(newFlowDeps(rec, args, []bool{false}, 0, assert.AnError))
		require.Error(t, err)
		assert.Equal(t, []string{"launch:setup"}, rec.events, "error short-circuits before reap/relaunch")
		assert.Empty(t, rec.written, "no marker written when setup launch fails")
		assert.Empty(t, rec.reaped)
	})

	t.Run("no-prompt-args-replayed-unchanged", func(t *testing.T) {
		rec := &flowRec{}
		bare := []string{"claude"} // fresh interactive, no -p / -- task
		require.NoError(t, runFirstRunFlow(newFlowDeps(rec, bare, []bool{false, true}, 1, nil)))
		// Both phases get exactly the bare args — no injected/replayed prompt.
		require.Len(t, rec.launchArg, 2)
		assert.Equal(t, bare, rec.launchArg[0])
		assert.Equal(t, bare, rec.launchArg[1])
	})
}

// TestFirstRunOrCodingSetupOnly covers the `agnt init` path: a single setup
// phase, no relaunch, and a permanent marker written only when setup produced a
// config. No t.Parallel(): mutates the setupOnlyMode package global.
func TestFirstRunOrCodingSetupOnly(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	dir := t.TempDir()

	setupOnlyMode = true
	t.Cleanup(func() { setupOnlyMode = false })

	var phases []bool
	launch := func(setupPhase bool, _ []string) (int, error) {
		phases = append(phases, setupPhase)
		return 0, nil
	}
	noReap := func(int) {}

	// No config produced → one setup phase, no relaunch, no marker.
	require.NoError(t, firstRunOrCoding(dir, []string{"claude"}, launch, noReap))
	assert.Equal(t, []bool{true}, phases, "init runs exactly one setup phase")
	m, err := readFirstRunMarker(firstRunStatePath(dir))
	require.NoError(t, err)
	assert.Nil(t, m, "no config written → no marker")

	// Setup wrote a config → permanent marker recorded.
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".agnt.kdl"), []byte("project { }\n"), 0o644))
	phases = nil
	require.NoError(t, firstRunOrCoding(dir, []string{"claude"}, launch, noReap))
	assert.Equal(t, []bool{true}, phases, "still a single setup phase, never a relaunch")
	m2, err := readFirstRunMarker(firstRunStatePath(dir))
	require.NoError(t, err)
	require.NotNil(t, m2)
	assert.True(t, m2.Permanent, "successful init writes a permanent marker")
}

// TestFirstRunOrCodingVerbDriven asserts the gate is verb-driven: setup fires
// for any agent (here a non-Claude command), not just Claude, when the project
// has no config and no marker. Before the verb-driven change this path went
// straight to a coding launch for everything but Claude.
func TestFirstRunOrCodingVerbDriven(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	dir := t.TempDir()
	// setupOnlyMode defaults to false (normal `agnt run`).

	var phases []bool
	launch := func(setupPhase bool, _ []string) (int, error) {
		phases = append(phases, setupPhase)
		return 0, nil
	}
	noReap := func(int) {}

	// Non-Claude agent, no config, no marker → setup phase fires.
	require.NoError(t, firstRunOrCoding(dir, []string{"gemini"}, launch, noReap))
	require.NotEmpty(t, phases)
	assert.True(t, phases[0], "setup fires for any agent, not just claude")

	// With a config already present, the gate skips setup → a single coding
	// launch, regardless of agent.
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".agnt.kdl"), []byte("project { }\n"), 0o644))
	phases = nil
	require.NoError(t, firstRunOrCoding(dir, []string{"copilot"}, launch, noReap))
	assert.Equal(t, []bool{false}, phases, "configured project → coding launch, no setup")
}

// TestFirstRunOrCoding_AutoConfigSkipsSetup verifies the deterministic path:
// a simple project (package.json with a dev script) is auto-configured in Go,
// so the run goes straight to coding with NO LLM setup phase.
func TestFirstRunOrCoding_AutoConfigSkipsSetup(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "package.json"),
		[]byte(`{"name":"web","scripts":{"dev":"vite","test":"vitest","lint":"eslint"}}`), 0o644))

	var phases []bool
	launch := func(setupPhase bool, _ []string) (int, error) {
		phases = append(phases, setupPhase)
		return 0, nil
	}
	require.NoError(t, firstRunOrCoding(dir, []string{"claude"}, launch, func(int) {}))

	// Auto-config wrote a real .agnt.kdl deterministically...
	data, err := os.ReadFile(filepath.Join(dir, ".agnt.kdl"))
	require.NoError(t, err, "auto-config must write .agnt.kdl")
	assert.Contains(t, string(data), "npm run dev", "generated dev script")
	// ...and the run went straight to coding — no LLM setup phase.
	assert.Equal(t, []bool{false}, phases, "auto-configured project skips LLM setup")
}
