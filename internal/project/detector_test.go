package project

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDetect_GoProject(t *testing.T) {
	// Create a temp directory with go.mod
	dir := t.TempDir()
	goMod := `module example.com/testproject

go 1.23
`
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(goMod), 0644); err != nil {
		t.Fatalf("failed to write go.mod: %v", err)
	}

	proj, err := Detect(dir)
	if err != nil {
		t.Fatalf("Detect failed: %v", err)
	}

	if proj.Type != ProjectGo {
		t.Errorf("expected type=go, got %s", proj.Type)
	}
	if proj.Name != "testproject" {
		t.Errorf("expected name=testproject, got %s", proj.Name)
	}
	if len(proj.Commands) == 0 {
		t.Error("expected commands to be populated")
	}
	if !HasCommand(proj, "test") {
		t.Error("expected 'test' command")
	}
	if !HasCommand(proj, "build") {
		t.Error("expected 'build' command")
	}
}

func TestDetect_NodeProject(t *testing.T) {
	// Create a temp directory with package.json
	dir := t.TempDir()
	packageJson := `{
  "name": "my-node-app",
  "version": "1.0.0",
  "scripts": {
    "test": "jest",
    "build": "tsc",
    "dev": "vite"
  }
}`
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(packageJson), 0644); err != nil {
		t.Fatalf("failed to write package.json: %v", err)
	}

	proj, err := Detect(dir)
	if err != nil {
		t.Fatalf("Detect failed: %v", err)
	}

	if proj.Type != ProjectNode {
		t.Errorf("expected type=node, got %s", proj.Type)
	}
	if proj.Name != "my-node-app" {
		t.Errorf("expected name=my-node-app, got %s", proj.Name)
	}
	if proj.PackageManager != "npm" {
		t.Errorf("expected package_manager=npm, got %s", proj.PackageManager)
	}
}

func TestDetect_NodeProjectWithPnpm(t *testing.T) {
	dir := t.TempDir()

	// Create package.json
	packageJson := `{"name": "pnpm-project"}`
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(packageJson), 0644); err != nil {
		t.Fatalf("failed to write package.json: %v", err)
	}

	// Create pnpm-lock.yaml
	if err := os.WriteFile(filepath.Join(dir, "pnpm-lock.yaml"), []byte("lockfileVersion: 6.0"), 0644); err != nil {
		t.Fatalf("failed to write pnpm-lock.yaml: %v", err)
	}

	proj, err := Detect(dir)
	if err != nil {
		t.Fatalf("Detect failed: %v", err)
	}

	if proj.PackageManager != "pnpm" {
		t.Errorf("expected package_manager=pnpm, got %s", proj.PackageManager)
	}
}

func TestDetect_PythonProject(t *testing.T) {
	dir := t.TempDir()

	// Create pyproject.toml
	pyproject := `[project]
name = "my-python-app"
version = "0.1.0"

[tool.ruff]
line-length = 100
`
	if err := os.WriteFile(filepath.Join(dir, "pyproject.toml"), []byte(pyproject), 0644); err != nil {
		t.Fatalf("failed to write pyproject.toml: %v", err)
	}

	proj, err := Detect(dir)
	if err != nil {
		t.Fatalf("Detect failed: %v", err)
	}

	if proj.Type != ProjectPython {
		t.Errorf("expected type=python, got %s", proj.Type)
	}
	if proj.Name != "my-python-app" {
		t.Errorf("expected name=my-python-app, got %s", proj.Name)
	}
	if proj.Metadata["linter"] != "ruff" {
		t.Errorf("expected linter=ruff in metadata, got %s", proj.Metadata["linter"])
	}
}

func TestDetect_PythonProjectWithRequirements(t *testing.T) {
	dir := t.TempDir()

	// Create requirements.txt only
	if err := os.WriteFile(filepath.Join(dir, "requirements.txt"), []byte("flask>=2.0"), 0644); err != nil {
		t.Fatalf("failed to write requirements.txt: %v", err)
	}

	proj, err := Detect(dir)
	if err != nil {
		t.Fatalf("Detect failed: %v", err)
	}

	if proj.Type != ProjectPython {
		t.Errorf("expected type=python, got %s", proj.Type)
	}
}

func TestDetect_UnknownProject(t *testing.T) {
	dir := t.TempDir()

	proj, err := Detect(dir)
	if err != nil {
		t.Fatalf("Detect failed: %v", err)
	}

	if proj.Type != ProjectUnknown {
		t.Errorf("expected type=unknown, got %s", proj.Type)
	}
	if len(proj.Commands) != 0 {
		t.Error("expected no commands for unknown project")
	}
}

func TestDetect_NonExistent(t *testing.T) {
	_, err := Detect("/nonexistent/path")
	if err == nil {
		t.Error("expected error for non-existent path")
	}
}

func TestDetect_File(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "file.txt")
	if err := os.WriteFile(file, []byte("test"), 0644); err != nil {
		t.Fatalf("failed to write file: %v", err)
	}

	_, err := Detect(file)
	if err != os.ErrInvalid {
		t.Errorf("expected ErrInvalid for file path, got %v", err)
	}
}

func TestGetCommandByName(t *testing.T) {
	proj := &Project{
		Commands: []CommandDef{
			{Name: "test", Description: "Run tests"},
			{Name: "build", Description: "Build project"},
		},
	}

	cmd := GetCommandByName(proj, "test")
	if cmd == nil {
		t.Fatal("expected to find 'test' command")
	}
	if cmd.Description != "Run tests" {
		t.Errorf("expected description='Run tests', got %s", cmd.Description)
	}

	cmd = GetCommandByName(proj, "nonexistent")
	if cmd != nil {
		t.Error("expected nil for nonexistent command")
	}
}

func TestGetCommandNames(t *testing.T) {
	proj := &Project{
		Commands: []CommandDef{
			{Name: "test"},
			{Name: "build"},
			{Name: "lint"},
		},
	}

	names := GetCommandNames(proj)
	if len(names) != 3 {
		t.Errorf("expected 3 names, got %d", len(names))
	}
}

func TestDefaultGoCommands(t *testing.T) {
	cmds := DefaultGoCommands()
	if len(cmds) == 0 {
		t.Error("expected non-empty Go commands")
	}

	// Check for essential commands
	hasTest := false
	hasBuild := false
	for _, cmd := range cmds {
		if cmd.Name == "test" {
			hasTest = true
		}
		if cmd.Name == "build" {
			hasBuild = true
		}
	}
	if !hasTest {
		t.Error("expected 'test' command in Go commands")
	}
	if !hasBuild {
		t.Error("expected 'build' command in Go commands")
	}
}

func TestDefaultNodeCommands(t *testing.T) {
	// Test npm
	npmCmds := DefaultNodeCommands("npm")
	for _, cmd := range npmCmds {
		if cmd.Name == "test" && cmd.Command != "npm" {
			t.Errorf("expected npm command, got %s", cmd.Command)
		}
	}

	// Test pnpm
	pnpmCmds := DefaultNodeCommands("pnpm")
	for _, cmd := range pnpmCmds {
		if cmd.Name == "test" && cmd.Command != "pnpm" {
			t.Errorf("expected pnpm command, got %s", cmd.Command)
		}
	}
}

func TestDefaultPythonCommands(t *testing.T) {
	cmds := DefaultPythonCommands()
	if len(cmds) == 0 {
		t.Error("expected non-empty Python commands")
	}

	hasTest := false
	for _, cmd := range cmds {
		if cmd.Name == "test" {
			hasTest = true
			if cmd.Command != "pytest" {
				t.Errorf("expected pytest command, got %s", cmd.Command)
			}
		}
	}
	if !hasTest {
		t.Error("expected 'test' command in Python commands")
	}
}

func TestDetect_WailsProject(t *testing.T) {
	// Create a temp directory with go.mod and wails.json
	dir := t.TempDir()
	goMod := `module example.com/mydesktopapp

go 1.23
`
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(goMod), 0644); err != nil {
		t.Fatalf("failed to write go.mod: %v", err)
	}

	wailsJson := `{"name": "MyDesktopApp", "outputfilename": "mydesktopapp"}`
	if err := os.WriteFile(filepath.Join(dir, "wails.json"), []byte(wailsJson), 0644); err != nil {
		t.Fatalf("failed to write wails.json: %v", err)
	}

	// Create frontend directory with pnpm lock file
	frontendDir := filepath.Join(dir, "frontend")
	if err := os.Mkdir(frontendDir, 0755); err != nil {
		t.Fatalf("failed to create frontend dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(frontendDir, "pnpm-lock.yaml"), []byte("lockfileVersion: 6.0"), 0644); err != nil {
		t.Fatalf("failed to write pnpm-lock.yaml: %v", err)
	}

	proj, err := Detect(dir)
	if err != nil {
		t.Fatalf("Detect failed: %v", err)
	}

	// Should still be detected as Go type
	if proj.Type != ProjectGo {
		t.Errorf("expected type=go, got %s", proj.Type)
	}

	// Should have Wails metadata
	if proj.Metadata["framework"] != "wails" {
		t.Errorf("expected framework=wails in metadata, got %s", proj.Metadata["framework"])
	}

	// Should detect pnpm for frontend
	if proj.Metadata["frontend_package_manager"] != "pnpm" {
		t.Errorf("expected frontend_package_manager=pnpm, got %s", proj.Metadata["frontend_package_manager"])
	}

	// Should have Wails-specific commands
	if !HasCommand(proj, "dev") {
		t.Error("expected 'dev' command for Wails project")
	}

	// Check that dev command uses wails
	devCmd := GetCommandByName(proj, "dev")
	if devCmd == nil || devCmd.Command != "wails" {
		t.Error("expected 'dev' command to use wails")
	}
	if !devCmd.Persistent {
		t.Error("expected 'dev' command to be persistent")
	}

	// Should have wails build command
	if !HasCommand(proj, "build") {
		t.Error("expected 'build' command for Wails project")
	}
	buildCmd := GetCommandByName(proj, "build")
	if buildCmd == nil || buildCmd.Command != "wails" {
		t.Error("expected 'build' command to use wails")
	}

	// Should have doctor command
	if !HasCommand(proj, "doctor") {
		t.Error("expected 'doctor' command for Wails project")
	}
}

func TestDefaultWailsCommands(t *testing.T) {
	cmds := DefaultWailsCommands()
	if len(cmds) == 0 {
		t.Error("expected non-empty Wails commands")
	}

	// Check for essential Wails commands
	hasDev := false
	hasBuild := false
	hasDoctor := false
	for _, cmd := range cmds {
		switch cmd.Name {
		case "dev":
			hasDev = true
			if cmd.Command != "wails" {
				t.Errorf("expected wails command for dev, got %s", cmd.Command)
			}
			if !cmd.Persistent {
				t.Error("expected dev command to be persistent")
			}
		case "build":
			hasBuild = true
			if cmd.Command != "wails" {
				t.Errorf("expected wails command for build, got %s", cmd.Command)
			}
		case "doctor":
			hasDoctor = true
			if cmd.Command != "wails" {
				t.Errorf("expected wails command for doctor, got %s", cmd.Command)
			}
		}
	}
	if !hasDev {
		t.Error("expected 'dev' command in Wails commands")
	}
	if !hasBuild {
		t.Error("expected 'build' command in Wails commands")
	}
	if !hasDoctor {
		t.Error("expected 'doctor' command in Wails commands")
	}
}

func TestDetect_DotnetSolution(t *testing.T) {
	for _, ext := range []string{".sln", ".slnx"} {
		t.Run(ext, func(t *testing.T) {
			dir := t.TempDir()
			// Projects live under src/; only the solution marks the root.
			if err := os.WriteFile(filepath.Join(dir, "Track"+ext), []byte("<Solution/>"), 0644); err != nil {
				t.Fatal(err)
			}
			proj, err := Detect(dir)
			if err != nil {
				t.Fatalf("Detect failed: %v", err)
			}
			if proj.Type != ProjectDotnet {
				t.Fatalf("expected type=dotnet, got %s", proj.Type)
			}
			if proj.Name != "Track" {
				t.Errorf("expected name=Track, got %s", proj.Name)
			}
			if proj.Metadata["solution"] != "Track"+ext {
				t.Errorf("expected solution metadata %q, got %q", "Track"+ext, proj.Metadata["solution"])
			}
			if !HasCommand(proj, "dev") {
				t.Error("expected 'dev' command")
			}
		})
	}
}
