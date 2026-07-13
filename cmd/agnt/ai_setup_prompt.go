package main

import (
	"time"

	"github.com/standardbeagle/agnt/internal/debug"
)

// aiSetupSystemPrompt returns the first-run setup system prompt when the gate
// fires for projectPath, and records a nudge marker so it is not re-injected
// within the re-nudge TTL. Prompt construction and marker persistence are
// platform-neutral; only the surrounding interactive AI process is Unix-only.
func aiSetupSystemPrompt(projectPath string) string {
	if hasResolvedConfig(projectPath) {
		return ""
	}
	marker, _ := readFirstRunMarker(firstRunStatePath(projectPath))
	if decideSetupGate(false, marker, time.Now(), renudgeTTLForProject(projectPath)) != enterSetup {
		return ""
	}
	// Best-effort: a failed marker write only re-shows the setup nudge next run.
	if err := writeFirstRunMarker(firstRunStatePath(projectPath), setupOutcomeMarker(false, time.Now())); err != nil {
		debug.Log("firstrun", "marker write failed (will re-nudge): %v", err)
	}
	return buildSetupSystemPrompt("claude")
}
