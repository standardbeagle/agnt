package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/standardbeagle/agnt/internal/autoconfig"
	"github.com/standardbeagle/agnt/internal/config"
	"github.com/standardbeagle/agnt/internal/debug"
	"github.com/standardbeagle/agnt/internal/project"
)

// tryAutoConfig attempts to configure a project deterministically — detect its
// type and write a sensible `.agnt.kdl` (dev server + proxy, lint/test as
// on-demand scripts) with NO LLM involvement. It returns true when it wrote a
// config; false when the project is unknown/complex enough that the LLM-driven
// setup flow should handle it instead.
//
// This is the fast path for the common case: a package.json web app, a dotnet
// site, a Go or Python repo. The owner's principle — only complicated configs
// need detailed LLM instructions — is realized here.
func tryAutoConfig(projectPath string) bool {
	detected, err := project.Detect(projectPath)
	if err != nil {
		debug.Log("run", "auto-config: detect failed: %v", err)
		return false
	}

	kdl, ok := autoconfig.Generate(detected)
	if !ok {
		debug.Log("run", "auto-config: no confident config for %s project", detected.Type)
		return false
	}

	// Never write a config we cannot parse back — a malformed generated file
	// would be worse than falling through to the LLM setup flow.
	if _, err := config.ParseAgntConfig(kdl); err != nil {
		debug.Log("run", "auto-config: generated invalid KDL: %v", err)
		return false
	}

	target := filepath.Join(projectPath, config.AgntConfigFileName)
	if err := os.WriteFile(target, []byte(kdl), 0o644); err != nil {
		debug.Log("run", "auto-config: write %s failed: %v", target, err)
		return false
	}

	fmt.Fprintf(os.Stdout,
		"agnt: detected %s project %q — wrote %s. Edit it to customize; changes apply live.\n",
		detected.Type, detected.Name, config.AgntConfigFileName)
	return true
}
