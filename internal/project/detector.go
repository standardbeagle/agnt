package project

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// ProjectType identifies the kind of project.
type ProjectType string

const (
	// ProjectGo is a Go project (go.mod).
	ProjectGo ProjectType = "go"
	// ProjectNode is a Node.js project (package.json).
	ProjectNode ProjectType = "node"
	// ProjectPython is a Python project (pyproject.toml, setup.py, requirements.txt).
	ProjectPython ProjectType = "python"
	// ProjectUnknown is an unrecognized project type.
	ProjectUnknown ProjectType = "unknown"
)

// Project represents a detected development project.
type Project struct {
	// Path is the absolute path to the project root.
	Path string `json:"path"`
	// Type is the detected project type.
	Type ProjectType `json:"type"`
	// Name is the project name (from config files or directory name).
	Name string `json:"name"`
	// Commands are the available commands for this project.
	Commands []CommandDef `json:"commands"`
	// PackageManager is the detected package manager (for Node.js).
	PackageManager string `json:"package_manager,omitempty"`
	// Metadata holds additional project-specific info.
	Metadata map[string]string `json:"metadata,omitempty"`
}

// Detect examines the given path and returns project information.
// Returns a Project with Type=ProjectUnknown if no project type is detected.
func Detect(path string) (*Project, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}

	// Stat the path to verify it exists
	info, err := os.Stat(absPath)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, os.ErrInvalid
	}

	// Try each detector in priority order
	if proj := detectGo(absPath); proj != nil {
		return proj, nil
	}
	if proj := detectNode(absPath); proj != nil {
		return proj, nil
	}
	if proj := detectPython(absPath); proj != nil {
		return proj, nil
	}

	// Unknown project type
	return &Project{
		Path:     absPath,
		Type:     ProjectUnknown,
		Name:     filepath.Base(absPath),
		Commands: nil,
	}, nil
}

// detectGo checks for a Go project.
func detectGo(path string) *Project {
	goModPath := filepath.Join(path, "go.mod")
	if _, err := os.Stat(goModPath); err != nil {
		return nil
	}

	proj := &Project{
		Path:     path,
		Type:     ProjectGo,
		Name:     parseGoModuleName(goModPath),
		Commands: DefaultGoCommands(),
		Metadata: make(map[string]string),
	}

	// Check for common Go tools
	if fileExists(filepath.Join(path, ".golangci.yml")) || fileExists(filepath.Join(path, ".golangci.yaml")) {
		proj.Metadata["linter"] = "golangci-lint"
	}

	return proj
}

// parseGoModuleName extracts the module name from go.mod.
func parseGoModuleName(goModPath string) string {
	data, err := os.ReadFile(goModPath)
	if err != nil {
		return filepath.Base(filepath.Dir(goModPath))
	}

	re := regexp.MustCompile(`(?m)^module\s+(\S+)`)
	if m := re.FindSubmatch(data); len(m) > 1 {
		moduleName := string(m[1])
		// Return just the last part for cleaner names
		parts := strings.Split(moduleName, "/")
		return parts[len(parts)-1]
	}

	return filepath.Base(filepath.Dir(goModPath))
}

// detectNode checks for a Node.js project.
func detectNode(path string) *Project {
	packagePath := filepath.Join(path, "package.json")
	if _, err := os.Stat(packagePath); err != nil {
		return nil
	}

	proj := &Project{
		Path:     path,
		Type:     ProjectNode,
		Name:     parsePackageJsonName(packagePath),
		Commands: nil, // Will be populated based on package manager
		Metadata: make(map[string]string),
	}

	// Detect package manager
	proj.PackageManager = detectPackageManager(path)

	// Set commands based on package manager
	proj.Commands = DefaultNodeCommands(proj.PackageManager)

	// Check for TypeScript
	if fileExists(filepath.Join(path, "tsconfig.json")) {
		proj.Metadata["typescript"] = "true"
	}

	// Parse scripts from package.json
	scripts := parsePackageJsonScripts(packagePath)
	if len(scripts) > 0 {
		proj.Metadata["scripts"] = strings.Join(scripts, ",")
	}

	return proj
}

// parsePackageJsonName extracts the name from package.json.
func parsePackageJsonName(packagePath string) string {
	data, err := os.ReadFile(packagePath)
	if err != nil {
		return filepath.Base(filepath.Dir(packagePath))
	}

	var pkg struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(data, &pkg); err == nil && pkg.Name != "" {
		return pkg.Name
	}

	return filepath.Base(filepath.Dir(packagePath))
}

// parsePackageJsonScripts returns available npm scripts.
func parsePackageJsonScripts(packagePath string) []string {
	data, err := os.ReadFile(packagePath)
	if err != nil {
		return nil
	}

	var pkg struct {
		Scripts map[string]string `json:"scripts"`
	}
	if err := json.Unmarshal(data, &pkg); err != nil {
		return nil
	}

	scripts := make([]string, 0, len(pkg.Scripts))
	for name := range pkg.Scripts {
		scripts = append(scripts, name)
	}
	return scripts
}

// detectPackageManager determines which package manager to use.
func detectPackageManager(path string) string {
	// Check for lock files in priority order
	if fileExists(filepath.Join(path, "pnpm-lock.yaml")) {
		return "pnpm"
	}
	if fileExists(filepath.Join(path, "yarn.lock")) {
		return "yarn"
	}
	if fileExists(filepath.Join(path, "bun.lockb")) {
		return "bun"
	}
	// Default to npm
	return "npm"
}

// detectPython checks for a Python project.
func detectPython(path string) *Project {
	// Check markers in priority order
	markers := []string{"pyproject.toml", "setup.py", "setup.cfg", "requirements.txt"}
	found := false
	var marker string
	for _, m := range markers {
		if fileExists(filepath.Join(path, m)) {
			found = true
			marker = m
			break
		}
	}
	if !found {
		return nil
	}

	proj := &Project{
		Path:     path,
		Type:     ProjectPython,
		Name:     parsePythonProjectName(path, marker),
		Commands: DefaultPythonCommands(),
		Metadata: make(map[string]string),
	}

	// Check for common Python tools
	if fileExists(filepath.Join(path, "pyproject.toml")) {
		proj.Metadata["config"] = "pyproject.toml"
		// Check for poetry
		if containsString(filepath.Join(path, "pyproject.toml"), "tool.poetry") {
			proj.Metadata["manager"] = "poetry"
		}
		// Check for ruff
		if containsString(filepath.Join(path, "pyproject.toml"), "tool.ruff") {
			proj.Metadata["linter"] = "ruff"
		}
	}
	if fileExists(filepath.Join(path, "ruff.toml")) {
		proj.Metadata["linter"] = "ruff"
	}
	if fileExists(filepath.Join(path, ".flake8")) {
		if proj.Metadata["linter"] == "" {
			proj.Metadata["linter"] = "flake8"
		}
	}

	return proj
}

// parsePythonProjectName tries to extract the project name.
func parsePythonProjectName(path, marker string) string {
	if marker == "pyproject.toml" {
		data, err := os.ReadFile(filepath.Join(path, marker))
		if err == nil {
			// Simple regex to find name in pyproject.toml
			re := regexp.MustCompile(`(?m)^name\s*=\s*"([^"]+)"`)
			if m := re.FindSubmatch(data); len(m) > 1 {
				return string(m[1])
			}
		}
	}
	if marker == "setup.py" {
		data, err := os.ReadFile(filepath.Join(path, marker))
		if err == nil {
			// Simple regex to find name in setup.py
			re := regexp.MustCompile(`name\s*=\s*['"]([\w-]+)['"]`)
			if m := re.FindSubmatch(data); len(m) > 1 {
				return string(m[1])
			}
		}
	}
	return filepath.Base(path)
}

// Helper functions

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func containsString(path, substr string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	return strings.Contains(string(data), substr)
}
