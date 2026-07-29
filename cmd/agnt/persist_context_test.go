package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/standardbeagle/agnt/internal/agentadapter"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUpsertManagedBlock_EmptyFile(t *testing.T) {
	out := upsertManagedBlock("", "HELLO")
	if !strings.Contains(out, persistBlockBegin) || !strings.Contains(out, persistBlockEnd) {
		t.Fatalf("missing markers:\n%s", out)
	}
	if !strings.Contains(out, "HELLO") {
		t.Fatalf("missing body:\n%s", out)
	}
}

func TestUpsertManagedBlock_PreservesUserContent(t *testing.T) {
	existing := "# My Project\n\nUser notes here.\n"
	out := upsertManagedBlock(existing, "AGNT")
	if !strings.Contains(out, "# My Project") || !strings.Contains(out, "User notes here.") {
		t.Fatalf("user content lost:\n%s", out)
	}
	if !strings.Contains(out, "AGNT") {
		t.Fatalf("body not appended:\n%s", out)
	}
}

func TestUpsertManagedBlock_ReplacesInPlace(t *testing.T) {
	first := upsertManagedBlock("# Doc\n\nkeep me\n", "OLD BODY")
	second := upsertManagedBlock(first, "NEW BODY")
	if strings.Contains(second, "OLD BODY") {
		t.Fatalf("old body not replaced:\n%s", second)
	}
	if !strings.Contains(second, "NEW BODY") {
		t.Fatalf("new body missing:\n%s", second)
	}
	if !strings.Contains(second, "keep me") {
		t.Fatalf("user content lost on replace:\n%s", second)
	}
	if strings.Count(second, persistBlockBegin) != 1 {
		t.Fatalf("expected exactly one managed block, got %d:\n%s",
			strings.Count(second, persistBlockBegin), second)
	}
}

func TestUpsertManagedBlock_Idempotent(t *testing.T) {
	once := upsertManagedBlock("user stuff\n", "BODY")
	twice := upsertManagedBlock(once, "BODY")
	if once != twice {
		t.Fatalf("not idempotent:\nonce:\n%s\ntwice:\n%s", once, twice)
	}
}

// TestPhaseCmdArgsAndPromptWritesOnlyToCallerDir pins the containment property
// that motivated threading projectDir through phaseCmdArgsAndPrompt: the
// agent-context file lands in the directory the CALLER supplied, and nothing is
// created relative to the process cwd.
//
// The second half is the half with teeth. This test deliberately does NOT call
// os.Chdir — it runs with the process cwd left at the package directory inside
// the repo. If phaseCmdArgsAndPrompt ever goes back to resolving the
// destination with os.Getwd(), the write lands at cmd/agnt/AGENTS.md and the
// cwd assertions below fail. That artifact is not cosmetic: every worktrack
// gate reads the working tree including untracked files, so a suite run that
// regenerates it dirties the tree for whatever unrelated change is in flight.
// os.Chdir cannot prevent that, because it is process-global rather than
// per-test isolation.
func TestPhaseCmdArgsAndPromptWritesOnlyToCallerDir(t *testing.T) {
	cwdAtStart, err := os.Getwd()
	require.NoError(t, err)

	// Several adapters with a context file, so the guard covers the matrix
	// rather than the one agent that happened to reproduce the leak.
	for _, agent := range []string{"kimi", "gemini", "codex"} {
		t.Run(agent, func(t *testing.T) {
			adapter := agentadapter.DefaultRegistry().Lookup(agent)
			require.NotNil(t, adapter, "adapter %q must resolve", agent)

			contextFile := lookupAgentSupport(adapter.Name()).ContextFile
			require.NotEmpty(t, contextFile, "%s must have a context file", agent)

			dir := t.TempDir()
			_, prompt, injectStdin := phaseCmdArgsAndPrompt(adapter, agent, nil, true, "", dir)

			// The write went to the caller-supplied directory.
			got, err := os.ReadFile(filepath.Join(dir, contextFile))
			require.NoError(t, err, "%s must be written under the caller's dir", contextFile)
			assert.Contains(t, string(got), prompt)
			assert.False(t, injectStdin, "a successful context write suppresses the stdin fallback")

			// ...and nothing was written relative to the process cwd. This is
			// what fails if the projectDir parameter is reverted to os.Getwd().
			_, err = os.Stat(filepath.Join(cwdAtStart, contextFile))
			assert.True(t, os.IsNotExist(err),
				"%s must not be created relative to the process cwd %s", contextFile, cwdAtStart)
		})
	}

	// The cwd itself must be untouched — no test here may rely on moving the
	// whole process to contain a write.
	cwdAtEnd, err := os.Getwd()
	require.NoError(t, err)
	assert.Equal(t, cwdAtStart, cwdAtEnd, "process cwd must not be used as isolation")
}

// TestWritePersistentContextIgnoresProcessCwd asserts the writer honours its
// projectDir argument exclusively: an empty dir is a no-op rather than a
// cwd-relative write, and the atomic rename leaves no temp file behind.
func TestWritePersistentContextIgnoresProcessCwd(t *testing.T) {
	cwdAtStart, err := os.Getwd()
	require.NoError(t, err)

	assert.False(t, writePersistentContext("gemini", "", "some prompt"),
		"empty projectDir must not be treated as the cwd")
	_, err = os.Stat(filepath.Join(cwdAtStart, "GEMINI.md"))
	assert.True(t, os.IsNotExist(err), "no GEMINI.md may appear next to the process cwd")

	dir := t.TempDir()
	require.True(t, writePersistentContext("gemini", dir, "some prompt"))
	assert.FileExists(t, filepath.Join(dir, "GEMINI.md"))
	_, err = os.Stat(filepath.Join(dir, "GEMINI.md.agnt.tmp"))
	assert.True(t, os.IsNotExist(err), "atomic rename must leave no temp file behind")

	// Claude never gets a context file (it uses --append-system-prompt).
	assert.False(t, writePersistentContext("claude", dir, "some prompt"))
	_, err = os.Stat(filepath.Join(dir, "CLAUDE.md"))
	assert.True(t, os.IsNotExist(err), "claude must not get a persisted context file")
}
