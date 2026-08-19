package config

import (
	"fmt"
	"math"
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
	Version     string          `kdl:"version"`
	Settings    KDLSettings     `kdl:"settings"`
	Languages   KDLLanguages    `kdl:"languages"`
	AI          *AIConfig       `kdl:"ai"`
	Feedback    *KDLFeedback    `kdl:"feedback"`
	PublicPlane *KDLPublicPlane `kdl:"public-plane"`
}

// KDLSettings holds global settings from KDL.
//
// The two timeout fields are float64 rather than int so a fractional literal
// can be detected and REFUSED at parse time. kdl-go silently truncates a
// float into an int struct field with no error, and for default-timeout that
// truncation lands on 0 — the "no timeout / run forever" sentinel — so a
// half-second limit would silently become an unbounded run (the bound-into-
// absence hazard in .claude/rules/config-contracts.md). Sub-second granularity
// is not supported end to end, so we reject rather than pretend to honor it.
type KDLSettings struct {
	DefaultTimeout  float64 `kdl:"default-timeout"`
	MaxOutputBuffer int     `kdl:"max-output-buffer"`
	GracefulTimeout float64 `kdl:"graceful-timeout"`
}

// validate rejects sub-second timeout values. The KDL timeout fields are whole
// seconds; a fractional value cannot be honored (there is no consumer that
// applies sub-second process timeouts), so accepting it would be a
// parse-but-no-effect lie and truncating it silently is worse — for
// default-timeout it lands on the "no timeout" sentinel. Refuse with an
// actionable message naming the field, the value, and the granularity limit.
func (s KDLSettings) validate() error {
	if err := requireWholeSeconds("settings.default-timeout", s.DefaultTimeout); err != nil {
		return err
	}
	return requireWholeSeconds("settings.graceful-timeout", s.GracefulTimeout)
}

func requireWholeSeconds(field string, v float64) error {
	if v != math.Trunc(v) {
		return fmt.Errorf("%s must be a whole number of seconds (got %v); sub-second timeouts are not supported", field, v)
	}
	return nil
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
//
// Timeout is float64 (not int) for the same reason as KDLSettings' timeout
// fields: kdl-go silently truncates a float literal into an int struct field
// with no error, and the consumer treats timeout 0 as the "no timeout / run
// forever" sentinel — so a fractional `timeout 0.5` would silently collapse to
// an unbounded run (the bound-into-absence hazard in
// .claude/rules/config-contracts.md). Sub-second granularity is not supported,
// so we REFUSE a fractional value at parse time rather than pretend to honor it.
type KDLCommand struct {
	Command    string            `kdl:"cmd"`
	Args       []string          `kdl:"args"`
	Timeout    float64           `kdl:"timeout"`
	Persistent bool              `kdl:"persistent"`
	Env        map[string]string `kdl:"env"`
}

// validateKDLCommands rejects any command whose timeout is not a whole number
// of seconds, naming the command so the error is actionable. Reuses
// requireWholeSeconds (the same guard KDLSettings.validate applies) so the
// granularity rule lives in exactly one place.
func validateKDLCommands(commands map[string]*KDLCommand) error {
	for name, cmd := range commands {
		if cmd == nil {
			continue
		}
		if err := requireWholeSeconds(fmt.Sprintf("command %s.timeout", name), cmd.Timeout); err != nil {
			return err
		}
	}
	return nil
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
	if err := kdlCfg.Settings.validate(); err != nil {
		return nil, err
	}
	for _, lang := range []*KDLLanguage{kdlCfg.Languages.Go, kdlCfg.Languages.Node, kdlCfg.Languages.Python} {
		if lang == nil {
			continue
		}
		if err := validateKDLCommands(lang.Commands); err != nil {
			return nil, err
		}
	}

	return kdlConfigToConfig(&kdlCfg), nil
}

// kdlConfigToConfig converts KDL config to our Config type.
func kdlConfigToConfig(kdlCfg *KDLConfig) *Config {
	cfg := DefaultConfig()

	if kdlCfg.Version != "" {
		cfg.Version = kdlCfg.Version
	}
	if kdlCfg.AI != nil {
		cfg.AI = kdlCfg.AI
	}

	// Feedback limits: a present block overrides defaults key-by-key; an absent
	// block keeps the spec §5 defaults. toFeedbackConfig normalizes so an omitted
	// or non-positive key never disables a guard.
	cfg.Feedback = kdlCfg.Feedback.toFeedbackConfig()

	// Public-plane request-rate limits: same override-key-by-key / normalize
	// semantics as the feedback block, so an absent block keeps the house
	// defaults and a partial block never disables a guard.
	cfg.PublicPlane = kdlCfg.PublicPlane.toPublicPlaneConfig()

	// Settings. Timeout values are validated whole seconds (see KDLSettings.validate),
	// so the int64 cast is lossless; 0 stays the "no timeout" sentinel via the >0 guard.
	if kdlCfg.Settings.DefaultTimeout > 0 {
		cfg.Settings.DefaultTimeout = time.Duration(int64(kdlCfg.Settings.DefaultTimeout)) * time.Second
	}
	if kdlCfg.Settings.MaxOutputBuffer > 0 {
		cfg.Settings.MaxOutputBuffer = kdlCfg.Settings.MaxOutputBuffer
	}
	if kdlCfg.Settings.GracefulTimeout > 0 {
		cfg.Settings.GracefulTimeout = time.Duration(int64(kdlCfg.Settings.GracefulTimeout)) * time.Second
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
			Command: kdlCmd.Command,
			Args:    kdlCmd.Args,
			// Validated whole seconds (validateKDLCommands), so the int cast is lossless.
			Timeout:    int(kdlCmd.Timeout),
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
	if err := validateKDLCommands(kdlCfg.Commands); err != nil {
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
			Command: kdlCmd.Command,
			Args:    kdlCmd.Args,
			// Validated whole seconds (validateKDLCommands), so the int cast is lossless.
			Timeout:    int(kdlCmd.Timeout),
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
    // Default process timeout, whole seconds (0 = no timeout; sub-second not supported)
    default-timeout 0
    // Output buffer size in bytes (256KB default)
    max-output-buffer 262144
    // Graceful shutdown timeout, whole seconds (sub-second not supported)
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
