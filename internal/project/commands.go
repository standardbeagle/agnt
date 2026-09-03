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

// DefaultDotnetCommands returns the default commands for a .NET project.
func DefaultDotnetCommands() []CommandDef {
	return []CommandDef{
		{
			Name:        "dev",
			Description: "Start the app with hot reload",
			Command:     "dotnet",
			Args:        []string{"watch", "run"},
			Persistent:  true,
		},
		{
			Name:        "test",
			Description: "Run dotnet tests",
			Command:     "dotnet",
			Args:        []string{"test"},
			Timeout:     600,
		},
		{
			Name:        "build",
			Description: "Build the project",
			Command:     "dotnet",
			Args:        []string{"build"},
			Timeout:     600,
		},
		{
			Name:        "format",
			Description: "Format the code",
			Command:     "dotnet",
			Args:        []string{"format"},
			Timeout:     120,
		},
	}
}

// DefaultWailsCommands returns the default commands for a Wails (Go desktop) project.
func DefaultWailsCommands() []CommandDef {
	return []CommandDef{
		{
			Name:        "dev",
			Description: "Start Wails development server with hot reload",
			Command:     "wails",
			Args:        []string{"dev"},
			Persistent:  true,
		},
		{
			Name:        "build",
			Description: "Build the Wails application",
			Command:     "wails",
			Args:        []string{"build"},
			Timeout:     600,
		},
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
			Name:        "doctor",
			Description: "Check Wails dependencies",
			Command:     "wails",
			Args:        []string{"doctor"},
			Timeout:     60,
		},
		{
			Name:        "generate",
			Description: "Generate Wails bindings",
			Command:     "wails",
			Args:        []string{"generate", "module"},
			Timeout:     60,
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

// DefaultRubyCommands returns the commands for a Bundler project. rspec
// selects `bundle exec rspec` over `rake test`.
func DefaultRubyCommands(rspec bool) []CommandDef {
	test := CommandDef{Name: "test", Description: "Run the test suite", Command: "bundle", Args: []string{"exec", "rake", "test"}, Timeout: 600}
	if rspec {
		test.Args = []string{"exec", "rspec"}
	}
	return []CommandDef{
		test,
		{Name: "lint", Description: "Run rubocop", Command: "bundle", Args: []string{"exec", "rubocop"}, Timeout: 120},
	}
}

// DefaultRailsCommands returns the commands for a Rails app.
func DefaultRailsCommands(rspec bool) []CommandDef {
	test := CommandDef{Name: "test", Description: "Run the Rails test suite", Command: "bin/rails", Args: []string{"test"}, Timeout: 600}
	if rspec {
		test = CommandDef{Name: "test", Description: "Run rspec", Command: "bundle", Args: []string{"exec", "rspec"}, Timeout: 600}
	}
	return []CommandDef{
		test,
		{Name: "lint", Description: "Run rubocop", Command: "bundle", Args: []string{"exec", "rubocop"}, Timeout: 120},
	}
}

// DefaultJekyllCommands returns the commands for a Jekyll site.
func DefaultJekyllCommands() []CommandDef {
	return []CommandDef{
		{Name: "build", Description: "Build the site", Command: "bundle", Args: []string{"exec", "jekyll", "build"}, Timeout: 300},
	}
}

// DefaultPHPCommands returns the commands for a Composer project.
func DefaultPHPCommands(pint bool) []CommandDef {
	cmds := []CommandDef{
		{Name: "test", Description: "Run phpunit", Command: "vendor/bin/phpunit", Timeout: 600},
	}
	if pint {
		cmds = append(cmds, CommandDef{Name: "lint", Description: "Check formatting with pint", Command: "vendor/bin/pint", Args: []string{"--test"}, Timeout: 120})
	}
	return cmds
}

// DefaultLaravelCommands returns the commands for a Laravel app.
func DefaultLaravelCommands(pint bool) []CommandDef {
	cmds := []CommandDef{
		{Name: "test", Description: "Run the Laravel test suite", Command: "php", Args: []string{"artisan", "test"}, Timeout: 600},
	}
	if pint {
		cmds = append(cmds, CommandDef{Name: "lint", Description: "Check formatting with pint", Command: "vendor/bin/pint", Args: []string{"--test"}, Timeout: 120})
	}
	return cmds
}

// DefaultElixirCommands returns the commands for a Mix project.
func DefaultElixirCommands() []CommandDef {
	return []CommandDef{
		{Name: "test", Description: "Run mix test", Command: "mix", Args: []string{"test"}, Timeout: 600},
		{Name: "lint", Description: "Check formatting", Command: "mix", Args: []string{"format", "--check-formatted"}, Timeout: 120},
		{Name: "build", Description: "Compile with warnings as errors", Command: "mix", Args: []string{"compile", "--warnings-as-errors"}, Timeout: 600},
	}
}

// DefaultHugoCommands returns the commands for a Hugo site.
func DefaultHugoCommands() []CommandDef {
	return []CommandDef{
		{Name: "build", Description: "Build the site", Command: "hugo", Timeout: 300},
	}
}

// DefaultMkdocsCommands returns the commands for an mkdocs site.
func DefaultMkdocsCommands() []CommandDef {
	return []CommandDef{
		{Name: "build", Description: "Build the site (strict)", Command: "mkdocs", Args: []string{"build", "--strict"}, Timeout: 300},
	}
}
