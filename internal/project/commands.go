package project

// CommandDef defines a runnable command for a project.
type CommandDef struct {
	// Name is the command identifier (e.g., "test", "lint", "build").
	Name string `json:"name"`
	// Description is a human-readable description.
	Description string `json:"description"`
	// Command is the executable to run.
	Command string `json:"command"`
	// Args are the default arguments.
	Args []string `json:"args,omitempty"`
	// Timeout is the default timeout in seconds (0 = no timeout).
	Timeout int `json:"timeout,omitempty"`
	// Persistent indicates this is a long-running process (dev server).
	Persistent bool `json:"persistent,omitempty"`
}

// DefaultGoCommands returns the default commands for a Go project.
func DefaultGoCommands() []CommandDef {
	return []CommandDef{
		{
			Name:        "test",
			Description: "Run Go tests",
			Command:     "go",
			Args:        []string{"test", "-v", "./..."},
			Timeout:     300,
		},
		{
			Name:        "test-race",
			Description: "Run Go tests with race detector",
			Command:     "go",
			Args:        []string{"test", "-v", "-race", "./..."},
			Timeout:     600,
		},
		{
			Name:        "build",
			Description: "Build the Go project",
			Command:     "go",
			Args:        []string{"build", "-v", "./..."},
			Timeout:     300,
		},
		{
			Name:        "lint",
			Description: "Run golangci-lint",
			Command:     "golangci-lint",
			Args:        []string{"run", "./..."},
			Timeout:     120,
		},
		{
			Name:        "vet",
			Description: "Run go vet",
			Command:     "go",
			Args:        []string{"vet", "./..."},
			Timeout:     120,
		},
		{
			Name:        "fmt-check",
			Description: "Check formatting with gofmt",
			Command:     "gofmt",
			Args:        []string{"-l", "."},
			Timeout:     60,
		},
		{
			Name:        "mod-tidy",
			Description: "Run go mod tidy",
			Command:     "go",
			Args:        []string{"mod", "tidy"},
			Timeout:     60,
		},
		{
			Name:        "run",
			Description: "Run the main package",
			Command:     "go",
			Args:        []string{"run", "."},
			Persistent:  true,
		},
	}
}

// DefaultNodeCommands returns the default commands for a Node.js project.
func DefaultNodeCommands(packageManager string) []CommandDef {
	if packageManager == "" {
		packageManager = "npm"
	}

	// Map of npm commands to other package manager equivalents
	// For most commands, the pattern is the same
	var runPrefix []string
	switch packageManager {
	case "npm":
		runPrefix = []string{"run"}
	case "pnpm":
		runPrefix = []string{} // pnpm doesn't need "run" for scripts
	case "yarn":
		runPrefix = []string{} // yarn doesn't need "run" for scripts
	case "bun":
		runPrefix = []string{"run"}
	}

	testCmd := append([]string{}, runPrefix...)
	testCmd = append(testCmd, "test")

	lintCmd := append([]string{}, runPrefix...)
	lintCmd = append(lintCmd, "lint")

	buildCmd := append([]string{}, runPrefix...)
	buildCmd = append(buildCmd, "build")

	devCmd := append([]string{}, runPrefix...)
	devCmd = append(devCmd, "dev")

	startCmd := append([]string{}, runPrefix...)
	startCmd = append(startCmd, "start")

	return []CommandDef{
		{
			Name:        "test",
			Description: "Run tests",
			Command:     packageManager,
			Args:        testCmd,
			Timeout:     300,
		},
		{
			Name:        "lint",
			Description: "Run linter",
			Command:     packageManager,
			Args:        lintCmd,
			Timeout:     120,
		},
		{
			Name:        "build",
			Description: "Build the project",
			Command:     packageManager,
			Args:        buildCmd,
			Timeout:     300,
		},
		{
			Name:        "dev",
			Description: "Start development server",
			Command:     packageManager,
			Args:        devCmd,
			Persistent:  true,
		},
		{
			Name:        "start",
			Description: "Start production server",
			Command:     packageManager,
			Args:        startCmd,
			Persistent:  true,
		},
		{
			Name:        "install",
			Description: "Install dependencies",
			Command:     packageManager,
			Args:        []string{"install"},
			Timeout:     300,
		},
		{
			Name:        "typecheck",
			Description: "Run TypeScript type checking",
			Command:     packageManager,
			Args:        append(runPrefix, "typecheck"),
			Timeout:     120,
		},
	}
}

// DefaultPythonCommands returns the default commands for a Python project.
func DefaultPythonCommands() []CommandDef {
	return []CommandDef{
		{
			Name:        "test",
			Description: "Run pytest",
			Command:     "pytest",
			Args:        []string{"-v"},
			Timeout:     300,
		},
		{
			Name:        "test-cov",
			Description: "Run pytest with coverage",
			Command:     "pytest",
			Args:        []string{"-v", "--cov=.", "--cov-report=term-missing"},
			Timeout:     300,
		},
		{
			Name:        "lint",
			Description: "Run ruff linter",
			Command:     "ruff",
			Args:        []string{"check", "."},
			Timeout:     120,
		},
		{
			Name:        "lint-fix",
			Description: "Run ruff with auto-fix",
			Command:     "ruff",
			Args:        []string{"check", "--fix", "."},
			Timeout:     120,
		},
		{
			Name:        "format",
			Description: "Run ruff formatter",
			Command:     "ruff",
			Args:        []string{"format", "."},
			Timeout:     60,
		},
		{
			Name:        "format-check",
			Description: "Check formatting with ruff",
			Command:     "ruff",
			Args:        []string{"format", "--check", "."},
			Timeout:     60,
		},
		{
			Name:        "typecheck",
			Description: "Run mypy type checker",
			Command:     "mypy",
			Args:        []string{"."},
			Timeout:     120,
		},
		{
			Name:        "install",
			Description: "Install dependencies with pip",
			Command:     "pip",
			Args:        []string{"install", "-r", "requirements.txt"},
			Timeout:     300,
		},
		{
			Name:        "install-dev",
			Description: "Install dev dependencies",
			Command:     "pip",
			Args:        []string{"install", "-e", ".[dev]"},
			Timeout:     300,
		},
	}
}

// GetCommandByName finds a command by name in a project.
func GetCommandByName(proj *Project, name string) *CommandDef {
	for i := range proj.Commands {
		if proj.Commands[i].Name == name {
			return &proj.Commands[i]
		}
	}
	return nil
}

// HasCommand checks if a project has a command with the given name.
func HasCommand(proj *Project, name string) bool {
	return GetCommandByName(proj, name) != nil
}

// GetCommandNames returns all command names for a project.
func GetCommandNames(proj *Project) []string {
	names := make([]string, len(proj.Commands))
	for i, cmd := range proj.Commands {
		names[i] = cmd.Name
	}
	return names
}
