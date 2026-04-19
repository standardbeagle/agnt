package config

import "testing"

func TestAIConfig_CheatSheetEnabled_DefaultsTrue(t *testing.T) {
	// Nil AIConfig → true (default).
	var ai *AIConfig
	if !ai.CheatSheetEnabled() {
		t.Error("nil AIConfig.CheatSheetEnabled() = false, want true")
	}
	// Non-nil but no explicit HelpersCheatSheet → true.
	ai = &AIConfig{}
	if !ai.CheatSheetEnabled() {
		t.Error("AIConfig{}.CheatSheetEnabled() = false, want true")
	}
}

func TestAIConfig_CheatSheetEnabled_ExplicitFalse(t *testing.T) {
	f := false
	ai := &AIConfig{HelpersCheatSheet: &f}
	if ai.CheatSheetEnabled() {
		t.Error("AIConfig{HelpersCheatSheet:&false}.CheatSheetEnabled() = true, want false")
	}
}

func TestAIConfig_CheatSheetEnabled_ExplicitTrue(t *testing.T) {
	tr := true
	ai := &AIConfig{HelpersCheatSheet: &tr}
	if !ai.CheatSheetEnabled() {
		t.Error("AIConfig{HelpersCheatSheet:&true}.CheatSheetEnabled() = false, want true")
	}
}

func TestAIConfig_CheatSheetEnabled_KDLParsing(t *testing.T) {
	// With KDL `ai { helpers-cheat-sheet false }`, CheatSheetEnabled() must
	// report false. This pins the kdl tag name so a rename breaks loudly.
	kdl := `ai {
    helpers-cheat-sheet false
}
`
	cfg, err := ParseAgntConfig(kdl)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if cfg.AI == nil {
		t.Fatal("expected AI config parsed, got nil")
	}
	if cfg.AI.CheatSheetEnabled() {
		t.Error("helpers-cheat-sheet false → CheatSheetEnabled() should be false")
	}

	// Sanity: omitting the field → default true.
	cfg2, err := ParseAgntConfig(`ai {}` + "\n")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !cfg2.AI.CheatSheetEnabled() {
		t.Error("omitted helpers-cheat-sheet → CheatSheetEnabled() should be true (default)")
	}
}
