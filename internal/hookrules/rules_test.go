package hookrules

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBuiltinRuleSet confirms every builtin pattern compiles. A panic here
// is a programming error — the catalog is hand-authored and must be
// correct in every build. Pair with the regression corpus below that
// checks each pattern matches the commands it should.
func TestBuiltinRuleSet(t *testing.T) {
	rs := BuiltinRuleSet()
	require.NotNil(t, rs)
	assert.Equal(t, DefaultBypassEnv, rs.BypassEnv)
	assert.Len(t, rs.BashRules, 9, "builtin catalog should have exactly 9 bash rules; update this assertion when adding to the catalog")
	assert.GreaterOrEqual(t, len(rs.PromptRules), 2)
	for _, r := range rs.BashRules {
		assert.NotNil(t, r.Pattern, "rule %q has nil compiled pattern", r.Raw)
		assert.NotEmpty(t, r.Action, "rule %q missing action", r.Raw)
	}
}

// TestBashRegressionCorpus is the table-driven regression corpus the task
// acceptance criteria calls out. Each row is a command + expected decision.
// Adding a new builtin pattern requires adding at least one positive and
// one negative case here.
func TestBashRegressionCorpus(t *testing.T) {
	rs := BuiltinRuleSet()
	cases := []struct {
		name    string
		cmd     string
		want    Action
		hasRule bool
	}{
		// npm/pnpm/yarn/bun dev/start/serve
		{"npm run dev", "npm run dev", ActionBlock, true},
		{"pnpm dev", "pnpm dev", ActionBlock, true},
		{"yarn start", "yarn start", ActionBlock, true},
		{"bun run serve", "bun run serve", ActionBlock, true},
		{"npm test does not match", "npm test", ActionAllow, false},
		{"pnpm install does not match", "pnpm install", ActionAllow, false},

		// go run
		{"go run cmd server", "go run ./cmd/server", ActionBlock, true},
		{"go build does not match", "go build ./...", ActionAllow, false},

		// kill family
		{"kill pid", "kill 12345", ActionBlock, true},
		{"killall node", "killall node", ActionBlock, true},
		{"pkill vite", "pkill vite", ActionBlock, true},
		{"echo killer does not match", "echo killer", ActionAllow, false},

		// lsof / ss / netstat
		{"lsof -i", "lsof -i :3000", ActionSoftWarn, true},
		{"ss -tlnp", "ss -tlnp", ActionSoftWarn, true},
		{"netstat -lnp", "netstat -lnp", ActionSoftWarn, true},

		// tail -f
		{"tail -f log", "tail -f /tmp/app.log", ActionBlock, true},
		{"cat does not match", "cat /tmp/app.log", ActionAllow, false},

		// curl localhost
		{"curl localhost", "curl http://localhost:3000/api", ActionSoftWarn, true},
		{"curl external allowed", "curl https://example.com", ActionAllow, false},

		// grep error
		{"grep -i error", "grep -i Error /tmp/app.log", ActionSoftWarn, true},
		{"grep unrelated", "grep hello /tmp/foo", ActionAllow, false},

		// ps aux | grep
		{"ps grep", "ps aux | grep node", ActionSoftWarn, true},
		{"ps alone", "ps aux", ActionAllow, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := rs.MatchBash(tc.cmd)
			assert.Equal(t, tc.want, d.Action, "command: %q", tc.cmd)
			if tc.hasRule {
				require.NotNil(t, d.Rule, "expected a matched rule for %q", tc.cmd)
				if d.Action == ActionBlock || d.Action == ActionSoftWarn {
					assert.NotEmpty(t, d.Replacement, "block/warn rule for %q must cite a replacement", tc.cmd)
				}
			}
		})
	}
}

// TestBashBypassMarker documents the # agnt-allow inline bypass.
func TestBashBypassMarker(t *testing.T) {
	rs := BuiltinRuleSet()
	cmd := "npm run dev # agnt-allow"
	// The matcher itself still fires — bypass is enforced at the CLI
	// layer (runCheckBashImpl), not in the pure rule engine. This
	// keeps the engine predictable: if you call MatchBash, you get a
	// decision; the CLI chooses what to do with it.
	d := rs.MatchBash(cmd)
	assert.Equal(t, ActionBlock, d.Action)
	assert.True(t, CommandHasBypassMarker(cmd))
	assert.False(t, CommandHasBypassMarker("npm run dev"))
}

func TestPromptRegressionCorpus(t *testing.T) {
	rs := BuiltinRuleSet()
	cases := []struct {
		name  string
		text  string
		nHits int
	}{
		{"start the dev server", "please start the dev server", 1},
		{"launch server variant", "can you launch the server", 1},
		{"check logs", "check the logs for errors", 1},
		{"tail errors", "tail errors please", 1},
		{"unrelated text", "refactor the login button", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hits := rs.MatchPrompt(tc.text)
			assert.GreaterOrEqual(t, len(hits), tc.nHits, "prompt %q expected >=%d reminders, got %d", tc.text, tc.nHits, len(hits))
		})
	}
}

// BenchmarkCheckBash runs the full Match path against a representative
// command (one that exercises the evaluation loop to a middle rule).
// The task acceptance criterion is <1ms per match; Go benchmarks report
// ns/op — target is <1e6.
func BenchmarkCheckBash(b *testing.B) {
	rs := BuiltinRuleSet()
	cmd := "npm run dev --watch"
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		d := rs.MatchBash(cmd)
		if d.Action != ActionBlock {
			b.Fatalf("unexpected action %s", d.Action)
		}
	}
}

// TestMatchBashLatency is a coarse runtime guard-rail in case someone adds
// a catastrophic-backtracking regex. Not a real benchmark — just a sanity
// ceiling that will fail loudly on obvious regressions.
func TestMatchBashLatency(t *testing.T) {
	rs := BuiltinRuleSet()
	// Run a few thousand matches and confirm we're well under the 1s
	// per-invocation budget. Real p99 target is microseconds; 100ms
	// here is intentionally loose to avoid flakiness on CI runners.
	start := time.Now()
	iters := 10_000
	for i := 0; i < iters; i++ {
		_ = rs.MatchBash("npm run dev")
	}
	elapsed := time.Since(start)
	perOp := elapsed / time.Duration(iters)
	assert.Less(t, perOp, time.Millisecond, "match should be <1ms per op, got %v", perOp)
}

func TestInAgntRunSession(t *testing.T) {
	if InAgntRunSession(nil) {
		t.Error("nil getenv should report no session")
	}
	if InAgntRunSession(func(string) string { return "" }) {
		t.Error("unset AGNT_RUN should report no session")
	}
	if !InAgntRunSession(func(k string) string {
		if k == AgntRunEnv {
			return "1"
		}
		return ""
	}) {
		t.Error("AGNT_RUN=1 should report an active session")
	}
}

func TestMatchBash_IgnoresQuotedLiterals(t *testing.T) {
	rs := BuiltinRuleSet()
	allowed := []string{
		`grep -n "npm run dev" logs.txt`,
		`echo "remember this later"`,
		`rg "go run ./cmd" --files-with-matches`,
	}
	for _, c := range allowed {
		if d := rs.MatchBash(c); d.Action != ActionAllow {
			t.Errorf("quoted literal should be allowed, got %q for: %s", d.Action, c)
		}
	}
	blocked := []string{
		`npm` + ` run ` + `dev`,
		`cd app && npm` + ` run ` + `dev`,
	}
	for _, c := range blocked {
		if d := rs.MatchBash(c); d.Action != ActionBlock {
			t.Errorf("real dev-server command should block, got %q for: %s", d.Action, c)
		}
	}
}
