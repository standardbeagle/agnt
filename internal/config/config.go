package config

import (
	"time"

	"github.com/standardbeagle/agnt/internal/project"
)

// Config holds the complete server configuration.
type Config struct {
	// Version is the config file version.
	Version string `json:"version"`

	// Settings are global server settings.
	Settings Settings `json:"settings"`

	// Languages holds language-specific configurations.
	Languages map[string]LanguageConfig `json:"languages"`
}

// Settings holds global configuration settings.
type Settings struct {
	// DefaultTimeout is the default process timeout.
	DefaultTimeout time.Duration `json:"default_timeout"`
	// MaxOutputBuffer is the per-stream output buffer size.
	MaxOutputBuffer int `json:"max_output_buffer"`
	// GracefulTimeout is the graceful shutdown timeout.
	GracefulTimeout time.Duration `json:"graceful_timeout"`
}

// LanguageConfig holds configuration for a specific language.
type LanguageConfig struct {
	// Markers are files that identify this project type.
	Markers []string `json:"markers"`
	// Priority determines detection order (higher = first).
	Priority int `json:"priority"`
	// Commands are the available commands for this language.
	Commands map[string]CommandConfig `json:"commands"`
	// PackageManagerDetect enables automatic package manager detection (Node.js).
	PackageManagerDetect bool `json:"package_manager_detect,omitempty"`
}

// CommandConfig holds configuration for a single command.
type CommandConfig struct {
	// Command is the executable name.
	Command string `json:"command"`
	// Args are the default arguments.
	Args []string `json:"args"`
	// Description is a human-readable description.
	Description string `json:"description,omitempty"`
	// Timeout is the command timeout in seconds.
	Timeout int `json:"timeout,omitempty"`
	// Persistent indicates this is a long-running process.
	Persistent bool `json:"persistent,omitempty"`
	// Env holds environment variables to set.
	Env map[string]string `json:"env,omitempty"`
}

// DefaultConfig returns the default configuration.
func DefaultConfig() *Config {
	return &Config{
		Version: "1.0",
		Settings: Settings{
			DefaultTimeout:  0, // No timeout
			MaxOutputBuffer: 256 * 1024,
			GracefulTimeout: 5 * time.Second,
		},
		Languages: map[string]LanguageConfig{
			"go": {
				Markers:  []string{"go.mod"},
				Priority: 100,
				Commands: goCommandsToConfig(project.DefaultGoCommands()),
			},
			"node": {
				Markers:              []string{"package.json"},
				Priority:             90,
				PackageManagerDetect: true,
				Commands:             nodeCommandsToConfig(project.DefaultNodeCommands("npm")),
			},
			"python": {
				Markers:  []string{"pyproject.toml", "setup.py", "requirements.txt"},
				Priority: 80,
				Commands: pythonCommandsToConfig(project.DefaultPythonCommands()),
			},
		},
	}
}

// ToCommandDef converts a CommandConfig to a project.CommandDef.
func (c *CommandConfig) ToCommandDef(name string) project.CommandDef {
	return project.CommandDef{
		Name:        name,
		Description: c.Description,
		Command:     c.Command,
		Args:        c.Args,
		Timeout:     c.Timeout,
		Persistent:  c.Persistent,
	}
}

// goCommandsToConfig converts project.CommandDef slice to config map.
func goCommandsToConfig(cmds []project.CommandDef) map[string]CommandConfig {
	result := make(map[string]CommandConfig)
	for _, cmd := range cmds {
		result[cmd.Name] = CommandConfig{
			Command:     cmd.Command,
			Args:        cmd.Args,
			Description: cmd.Description,
			Timeout:     cmd.Timeout,
			Persistent:  cmd.Persistent,
		}
	}
	return result
}

// nodeCommandsToConfig converts project.CommandDef slice to config map.
func nodeCommandsToConfig(cmds []project.CommandDef) map[string]CommandConfig {
	return goCommandsToConfig(cmds) // Same conversion logic
}

// pythonCommandsToConfig converts project.CommandDef slice to config map.
func pythonCommandsToConfig(cmds []project.CommandDef) map[string]CommandConfig {
	return goCommandsToConfig(cmds) // Same conversion logic
}

// GetLanguageCommands returns command definitions for a language.
func (c *Config) GetLanguageCommands(lang string) []project.CommandDef {
	langConfig, ok := c.Languages[lang]
	if !ok {
		return nil
	}

	cmds := make([]project.CommandDef, 0, len(langConfig.Commands))
	for name, cmdConfig := range langConfig.Commands {
		cmds = append(cmds, cmdConfig.ToCommandDef(name))
	}
	return cmds
}

// MergeProjectConfig merges per-project config with global config.
func (c *Config) MergeProjectConfig(projConfig *ProjectConfig) *Config {
	if projConfig == nil {
		return c
	}

	// Create a copy to avoid modifying the original
	merged := *c

	// Override language-specific commands if project specifies them
	if projConfig.Language != "" {
		if langConfig, ok := merged.Languages[projConfig.Language]; ok {
			// Merge commands
			for name, cmd := range projConfig.Commands {
				langConfig.Commands[name] = cmd
			}
			merged.Languages[projConfig.Language] = langConfig
		}
	}

	return &merged
}

// ProjectConfig holds per-project configuration from .agnt.kdl.
type ProjectConfig struct {
	// Language overrides the detected language.
	Language string `json:"language,omitempty"`
	// PackageManager overrides the detected package manager.
	PackageManager string `json:"package_manager,omitempty"`
	// Commands override default commands.
	Commands map[string]CommandConfig `json:"commands,omitempty"`
}

// Validate checks the configuration for errors.
func (c *Config) Validate() error {
	// Basic validation - can be extended
	if c.Settings.MaxOutputBuffer <= 0 {
		c.Settings.MaxOutputBuffer = 256 * 1024
	}
	if c.Settings.GracefulTimeout <= 0 {
		c.Settings.GracefulTimeout = 5 * time.Second
	}
	return nil
}
