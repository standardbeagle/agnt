package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/standardbeagle/agnt/internal/agentadapter"
)

// dirEntries lists a directory's entry names, so a test can assert that
// nothing at all was written rather than guessing which filename to look for.
func dirEntries(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

// TestPersistContextHonoursAdapterDisabled is the regression for `disabled
// true` leaving the context-file write on: the override blanks BuildArgs and
// InitialStdin but keeps the adapter's name, which is all the writer keyed on.
func TestPersistContextHonoursAdapterDisabled(t *testing.T) {
	reg := agentadapter.DefaultRegistry()
	reg.SetOverrides(map[string]agentadapter.Override{"gemini": {Disabled: true}})
	adapter := reg.Lookup("gemini")
	require.NotNil(t, adapter)
	require.True(t, agentadapter.InjectionDisabled(adapter))

	// Setup phase goes through the real call site. The coding phase builds its
	// prompt by dialing the daemon, so its write is exercised through the same
	// helper the call site uses rather than through a live socket.
	dir := t.TempDir()
	phaseCmdArgsAndPrompt(adapter, "gemini", nil, true, "", dir)
	assert.Empty(t, dirEntries(t, dir), "disabled adapter must not get a context file in setup")
	assert.False(t, persistAdapterContext(adapter, dir, "coding prompt"))
	assert.Empty(t, dirEntries(t, dir), "disabled adapter must not get a context file in a coding session")

	// Mutation guard: the same adapter without the override does write.
	dir = t.TempDir()
	plain := agentadapter.DefaultRegistry().Lookup("gemini")
	require.False(t, agentadapter.InjectionDisabled(plain))
	require.True(t, persistAdapterContext(plain, dir, "some prompt"))
	assert.FileExists(t, filepath.Join(dir, "GEMINI.md"))
}

// TestPersistContextSkipsUnknownCommands: `agnt run <any command>` resolves to
// the Universal adapter, which used to inherit the generic AGENTS.md and so
// created that file in the cwd for `agnt run bash`.
func TestPersistContextSkipsUnknownCommands(t *testing.T) {
	for _, command := range []string{"bash", "/usr/bin/vim", "npm"} {
		adapter := agentadapter.Universal(command)
		dir := t.TempDir()
		phaseCmdArgsAndPrompt(adapter, command, nil, true, "", dir)
		assert.Empty(t, dirEntries(t, dir), "%s must not get a context file in setup", command)
		assert.False(t, persistAdapterContext(adapter, dir, "coding prompt"))
		assert.Empty(t, dirEntries(t, dir), "%s must not get a context file in a coding session", command)
	}
}

// TestPersistContextHonoursConfigOverride: the writer used to load
// <projectDir>/.agnt.kdl directly, so `persist-context false` in the file
// named by --config was ignored.
func TestPersistContextHonoursConfigOverride(t *testing.T) {
	cfgDir := t.TempDir()
	cfgPath := filepath.Join(cfgDir, "other.kdl")
	require.NoError(t, os.WriteFile(cfgPath, []byte("ai {\n    persist-context false\n}\n"), 0o644))

	prev := configOverride
	configOverride = cfgPath
	t.Cleanup(func() { configOverride = prev })

	dir := t.TempDir()
	assert.False(t, writePersistentContext("gemini", dir, "some prompt"))
	assert.Empty(t, dirEntries(t, dir), "--config persist-context=false must suppress the write")

	// Mutation guard: without the override the same call writes.
	configOverride = ""
	require.True(t, writePersistentContext("gemini", dir, "some prompt"))
}

// TestAiderGetsNoContextFileButIsToldSo pins the aider decision: agnt does not
// write a CONVENTIONS.md aider would never read, does not add an unverified
// `--read` flag, and says that the session is unsteered.
func TestAiderGetsNoContextFileButIsToldSo(t *testing.T) {
	adapter := agentadapter.DefaultRegistry().Lookup("aider")
	require.NotNil(t, adapter)

	dir := t.TempDir()
	launch, prompt, _ := phaseCmdArgsAndPrompt(adapter, "aider", []string{"--model", "x"}, true, "", dir)
	assert.False(t, persistAdapterContext(adapter, dir, "coding prompt"))
	assert.Empty(t, dirEntries(t, dir), "aider must not get a context file")
	assert.Equal(t, []string{"--model", "x"}, launch.Args, "agnt must not add flags to aider's argv")

	msg := unsteeredAgentNotice(adapter, false, nil, prompt)
	assert.Contains(t, msg, "aider")
	assert.Contains(t, msg, "--read")

	// No notice when guidance was delivered, switched off, or the command is
	// not an agent agnt knows.
	gemini := agentadapter.DefaultRegistry().Lookup("gemini")
	assert.Empty(t, unsteeredAgentNotice(gemini, true, nil, prompt), "persisted context is delivery")
	claude := agentadapter.DefaultRegistry().Lookup("claude")
	assert.Empty(t, unsteeredAgentNotice(claude, false, nil, prompt), "flag injection is delivery")
	assert.Empty(t, unsteeredAgentNotice(agentadapter.Universal("bash"), false, nil, prompt), "unknown command is not an unsteered agent")

	reg := agentadapter.DefaultRegistry()
	reg.SetOverrides(map[string]agentadapter.Override{"aider": {Disabled: true}})
	assert.Empty(t, unsteeredAgentNotice(reg.Lookup("aider"), false, nil, prompt), "disabled is a choice, not a failure")
}
