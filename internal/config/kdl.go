package config

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	kdl "github.com/sblinch/kdl-go"
)

// KDL configuration file names
const (
	GlobalConfigFile  = "config.kdl"
	ProjectConfigFile = ".agnt.kdl"
)

// KDLConfig represents the KDL configuration structure.
// Uses kdl struct tags for unmarshaling.
type KDLConfig struct {
	Version   string       `kdl:"version"`
	Settings  KDLSettings  `kdl:"settings"`
	Languages KDLLanguages `kdl:"languages"`
}

// KDLSettings holds global settings from KDL.
type KDLSettings struct {
	DefaultTimeout  int `kdl:"default-timeout"`
	MaxOutputBuffer int `kdl:"max-output-buffer"`
	GracefulTimeout int `kdl:"graceful-timeout"`
}

// KDLLanguages holds language configurations.
type KDLLanguages struct {
	Go     *KDLLanguage `kdl:"go"`
	Node   *KDLLanguage `kdl:"node"`
	Python *KDLLanguage `kdl:"python"`
}

// KDLLanguage holds configuration for a specific language.
type KDLLanguage struct {
	Markers              []string               `kdl:"markers"`
	Priority             int                    `kdl:"priority"`
	PackageManagerDetect bool                   `kdl:"package-manager-detect"`
	Commands             map[string]*KDLCommand `kdl:"commands"`
}

// KDLCommand holds a command configuration.
type KDLCommand struct {
	Command    string            `kdl:"cmd"`
	Args       []string          `kdl:"args"`
	Timeout    int               `kdl:"timeout"`
	Persistent bool              `kdl:"persistent"`
	Env        map[string]string `kdl:"env"`
}

// KDLProjectConfig holds per-project configuration.
type KDLProjectConfig struct {
	Language       string                 `kdl:"language"`
	PackageManager string                 `kdl:"package-manager"`
	Commands       map[string]*KDLCommand `kdl:"commands"`
}

// LoadGlobalConfig loads the global configuration from the default location.
func LoadGlobalConfig() (*Config, error) {
	// Try XDG config dir first
	configDir := os.Getenv("XDG_CONFIG_HOME")
	if configDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return DefaultConfig(), nil
		}
		configDir = filepath.Join(home, ".config")
	}

	configPath := filepath.Join(configDir, "agnt", GlobalConfigFile)

	// If file doesn't exist, return defaults
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		return DefaultConfig(), nil
	}

	return LoadConfigFile(configPath)
}

// LoadConfigFile loads configuration from a specific file path.
func LoadConfigFile(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	return ParseKDLConfig(string(data))
}

// ParseKDLConfig parses KDL configuration data.
func ParseKDLConfig(data string) (*Config, error) {
	var kdlCfg KDLConfig
	if err := kdl.Unmarshal([]byte(data), &kdlCfg); err != nil {
		return nil, err
	}

	return kdlConfigToConfig(&kdlCfg), nil
}

// kdlConfigToConfig converts KDL config to our Config type.
func kdlConfigToConfig(kdlCfg *KDLConfig) *Config {
	cfg := DefaultConfig()

	if kdlCfg.Version != "" {
		cfg.Version = kdlCfg.Version
	}

	// Settings
	if kdlCfg.Settings.DefaultTimeout > 0 {
		cfg.Settings.DefaultTimeout = time.Duration(kdlCfg.Settings.DefaultTimeout) * time.Second
	}
	if kdlCfg.Settings.MaxOutputBuffer > 0 {
		cfg.Settings.MaxOutputBuffer = kdlCfg.Settings.MaxOutputBuffer
	}
	if kdlCfg.Settings.GracefulTimeout > 0 {
		cfg.Settings.GracefulTimeout = time.Duration(kdlCfg.Settings.GracefulTimeout) * time.Second
	}

	// Languages
	if kdlCfg.Languages.Go != nil {
		mergeLanguageConfig(cfg, "go", kdlCfg.Languages.Go)
	}
	if kdlCfg.Languages.Node != nil {
		mergeLanguageConfig(cfg, "node", kdlCfg.Languages.Node)
	}
	if kdlCfg.Languages.Python != nil {
		mergeLanguageConfig(cfg, "python", kdlCfg.Languages.Python)
	}

	return cfg
}

// mergeLanguageConfig merges KDL language config into the main config.
func mergeLanguageConfig(cfg *Config, name string, kdlLang *KDLLanguage) {
	langCfg := cfg.Languages[name]

	if len(kdlLang.Markers) > 0 {
		langCfg.Markers = kdlLang.Markers
	}
	if kdlLang.Priority > 0 {
		langCfg.Priority = kdlLang.Priority
	}
	langCfg.PackageManagerDetect = kdlLang.PackageManagerDetect

	// Merge commands
	for cmdName, kdlCmd := range kdlLang.Commands {
		if kdlCmd == nil {
			continue
		}
		cmdCfg := CommandConfig{
			Command:    kdlCmd.Command,
			Args:       kdlCmd.Args,
			Timeout:    kdlCmd.Timeout,
			Persistent: kdlCmd.Persistent,
			Env:        kdlCmd.Env,
		}
		langCfg.Commands[cmdName] = cmdCfg
	}

	cfg.Languages[name] = langCfg
}

// LoadProjectConfig loads per-project configuration from .agnt.kdl.
func LoadProjectConfig(projectPath string) (*ProjectConfig, error) {
	configPath := filepath.Join(projectPath, ProjectConfigFile)

	// If file doesn't exist, return nil (no project config)
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		return nil, nil
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, err
	}

	return ParseProjectConfig(string(data))
}

// ParseProjectConfig parses per-project KDL configuration.
func ParseProjectConfig(data string) (*ProjectConfig, error) {
	var kdlCfg KDLProjectConfig
	if err := kdl.Unmarshal([]byte(data), &kdlCfg); err != nil {
		return nil, err
	}

	return kdlProjectConfigToProjectConfig(&kdlCfg), nil
}

// kdlProjectConfigToProjectConfig converts KDL project config to ProjectConfig.
func kdlProjectConfigToProjectConfig(kdlCfg *KDLProjectConfig) *ProjectConfig {
	cfg := &ProjectConfig{
		Language:       kdlCfg.Language,
		PackageManager: kdlCfg.PackageManager,
		Commands:       make(map[string]CommandConfig),
	}

	for name, kdlCmd := range kdlCfg.Commands {
		if kdlCmd == nil {
			continue
		}
		cfg.Commands[name] = CommandConfig{
			Command:    kdlCmd.Command,
			Args:       kdlCmd.Args,
			Timeout:    kdlCmd.Timeout,
			Persistent: kdlCmd.Persistent,
			Env:        kdlCmd.Env,
		}
	}

	return cfg
}

// GlobalConfigPath returns the path to the global config file.
func GlobalConfigPath() string {
	configDir := os.Getenv("XDG_CONFIG_HOME")
	if configDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		configDir = filepath.Join(home, ".config")
	}
	return filepath.Join(configDir, "agnt", GlobalConfigFile)
}

// WriteDefaultConfig writes a default config file with documentation.
func WriteDefaultConfig(path string) error {
	defaultKDL := `// agnt Configuration
// See documentation for full options

version "1.0"

settings {
    // Default process timeout in seconds (0 = no timeout)
    default-timeout 0
    // Output buffer size in bytes (256KB default)
    max-output-buffer 262144
    // Graceful shutdown timeout in seconds
    graceful-timeout 5
}

languages {
    go {
        markers "go.mod"
        priority 100
        commands {
            test { cmd "go" "test" "-v" "./..." }
            build { cmd "go" "build" "-v" "./..." }
            lint { cmd "golangci-lint" "run" }
        }
    }

    node {
        markers "package.json"
        package-manager-detect true
        priority 90
        commands {
            test { cmd "npm" "test" }
            build { cmd "npm" "run" "build" }
            dev { cmd "npm" "run" "dev"; persistent true }
        }
    }

    python {
        markers "pyproject.toml" "setup.py" "requirements.txt"
        priority 80
        commands {
            test { cmd "pytest" "-v" }
            lint { cmd "ruff" "check" "." }
        }
    }
}
`
	// Create directory if needed
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	return os.WriteFile(path, []byte(strings.TrimSpace(defaultKDL)+"\n"), 0644)
}
