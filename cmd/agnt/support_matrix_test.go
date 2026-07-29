package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/standardbeagle/agnt/internal/agentadapter"
	"github.com/stretchr/testify/assert"
)

// TestLookupAgentSupport asserts known adapters resolve to the right mechanism
// with non-empty text, unknown agents fall back to generic advice, and every
// matrix row is well-formed.
func TestLookupAgentSupport(t *testing.T) {
	cases := []struct {
		name string
		mech installMechanism
	}{
		{"claude", mechMarketplace},
		{"gemini", mechSkillFile},
		{"copilot", mechSkillFile},
		{"codex", mechSkillFile},
		{"opencode", mechSkillFile},
		{"cursor-agent", mechSkillFile},
		{"cursor", mechSkillFile},
		{"qwen", mechSkillFile},
		{"crush", mechSkillFile},
		{"kimi-cli", mechSkillFile},
		{"auggie", mechSkillFile},
		{"aider", mechNone},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := lookupAgentSupport(tc.name)
			assert.Equal(t, tc.mech, got.Mechanism)
			assert.NotEmpty(t, got.InstallText)
		})
	}

	// Unknown agent → generic skill-file advice.
	unknown := lookupAgentSupport("totally-made-up-agent")
	assert.Equal(t, genericAgentSupport, unknown)
	assert.Equal(t, mechSkillFile, unknown.Mechanism)
	assert.NotEmpty(t, unknown.InstallText)

	// Claude is the only marketplace agent; every row is well-formed.
	marketplaceCount := 0
	validMech := map[installMechanism]bool{mechMarketplace: true, mechSkillFile: true, mechNone: true}
	for name, s := range supportMatrix {
		assert.NotEmpty(t, s.InstallText, "%s has empty install text", name)
		assert.True(t, validMech[s.Mechanism], "%s has invalid mechanism %q", name, s.Mechanism)
		if s.Mechanism == mechMarketplace {
			marketplaceCount++
		}
	}
	assert.Equal(t, 1, marketplaceCount, "claude should be the only marketplace-install agent")
}

// TestBuildSetupSystemPromptPerAgent asserts the prompt carries the matrix
// install text for the resolved agent, with the right structure for skill-file
// vs none mechanisms.
func TestBuildSetupSystemPromptPerAgent(t *testing.T) {
	// Skill-file agent: emits that agent's matrix install text + self-check.
	gemini := buildSetupSystemPrompt("gemini")
	assert.Contains(t, gemini, supportMatrix["gemini"].InstallText, "must emit gemini's install text")
	assert.Contains(t, gemini, ".gemini/commands/", "gemini-specific path present")
	assert.Contains(t, strings.ToLower(gemini), "self-check", "skill-file path keeps self-check")
	assert.NotContains(t, gemini, "%!", "no leftover format verbs")

	// Marketplace agent (claude): emits the marketplace install text.
	claude := buildSetupSystemPrompt("claude")
	assert.Contains(t, claude, "/plugin install agnt", "claude marketplace text present")
	assert.Contains(t, claude, "agnt:setup-project")

	// none agent (aider): inline path, no skill self-check, carries its text.
	aider := buildSetupSystemPrompt("aider")
	assert.Contains(t, aider, supportMatrix["aider"].InstallText)
	assert.Contains(t, aider, "Configure the project", "none path drives inline config")
	assert.NotContains(t, strings.ToLower(aider), "self-check for the setup skill",
		"none agent should not be told to self-check for an installable skill")

	// Unknown agent: generic text, graceful, no panic / dangling verb.
	unknown := buildSetupSystemPrompt("mystery-cli")
	assert.Contains(t, unknown, genericAgentSupport.InstallText)
	assert.NotContains(t, unknown, "%!")
}

func TestSetupPromptDeliveryPrefersAdapterContextFile(t *testing.T) {
	dir := t.TempDir()

	kimi := agentadapter.DefaultRegistry().Lookup("kimi")
	_, prompt, injectStdin := phaseCmdArgsAndPrompt(kimi, "kimi", nil, true, "", dir)
	assert.False(t, injectStdin, "kimi should consume startup-loaded AGENTS.md")
	context, err := os.ReadFile(filepath.Join(dir, "AGENTS.md"))
	assert.NoError(t, err)
	assert.Contains(t, string(context), prompt)

	aider := agentadapter.DefaultRegistry().Lookup("aider")
	_, _, injectStdin = phaseCmdArgsAndPrompt(aider, "aider", nil, true, "", dir)
	assert.True(t, injectStdin, "aider has no automatically loaded context file")
}

// TestRenudgeAcrossRuns ties the negative-outcome marker to the gate across two
// "runs": a negative outcome writes a timestamped marker that suppresses the
// nudge within the TTL but re-enters after it. A positive outcome is permanent.
func TestRenudgeAcrossRuns(t *testing.T) {
	t0 := time.Date(2026, 5, 29, 9, 0, 0, 0, time.UTC)
	ttl := 7 * 24 * time.Hour

	// Run 1: gate fires (no config, no marker).
	assert.Equal(t, enterSetup, decideSetupGate(false, nil, t0, ttl))

	// Negative outcome → timestamped marker.
	neg := setupOutcomeMarker(false, t0)
	assert.False(t, neg.Permanent)

	// Run 2 within TTL → skip; just after TTL → re-enter.
	withinTTL := t0.Add(ttl - time.Hour)
	afterTTL := t0.Add(ttl + time.Hour)
	assert.Equal(t, skipSetup, decideSetupGate(false, &neg, withinTTL, ttl), "within TTL re-nudge suppressed")
	assert.Equal(t, enterSetup, decideSetupGate(false, &neg, afterTTL, ttl), "after TTL re-nudges")

	// Positive outcome → permanent: never re-nudges, even far past the TTL.
	pos := setupOutcomeMarker(true, t0)
	assert.True(t, pos.Permanent)
	assert.Equal(t, skipSetup, decideSetupGate(false, &pos, t0.Add(365*24*time.Hour), ttl))
}
