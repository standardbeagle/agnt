package config

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"time"

	kdl "github.com/sblinch/kdl-go"
)

// GlobalConfigFile is the name of the per-user config file, read from
// $XDG_CONFIG_HOME/agnt (default ~/.config/agnt). The per-project file is
// AgntConfigFileName.
const GlobalConfigFile = "config.kdl"

// KDLConfig represents the KDL configuration structure.
// Uses kdl struct tags for unmarshaling.
type KDLConfig struct {
	Version     string          `kdl:"version"`
	Languages   KDLLanguages    `kdl:"languages"`
	Settings    KDLSettings     `kdl:"settings"`
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
	// A negative default-timeout would fall through the `> 0` guard in
	// ParseKDLConfig onto the "no timeout" sentinel. (graceful-timeout ≤ 0
	// documentedly restores its positive default, so it needs no check.)
	if s.DefaultTimeout < 0 {
		return fmt.Errorf("settings.default-timeout must not be negative (got %v); use 0 for no timeout", s.DefaultTimeout)
	}
	return requireWholeSeconds("settings.graceful-timeout", s.GracefulTimeout)
}

func requireWholeSeconds(field string, v float64) error {
	if v != math.Trunc(v) {
		return fmt.Errorf("%s must be a whole number of seconds (got %v); sub-second timeouts are not supported", field, v)
	}
	return nil
}

// KDLLanguages is the accepted-but-ignored `languages` block. It is declared
// only so a config written from the old default template still parses;
// ParseKDLConfig reports it with a no-effect warning.
type KDLLanguages struct {
	Go     *KDLLanguage `kdl:"go"`
	Node   *KDLLanguage `kdl:"node"`
	Python *KDLLanguage `kdl:"python"`
}

// KDLLanguage mirrors the old per-language block shape (see KDLLanguages).
type KDLLanguage struct {
	Markers              []string               `kdl:"markers"`
	Priority             int                    `kdl:"priority"`
	PackageManagerDetect bool                   `kdl:"package-manager-detect"`
	Commands             map[string]*KDLCommand `kdl:"commands"`
}

// KDLCommand mirrors the old per-command shape (see KDLLanguages).
type KDLCommand struct {
	Command    string            `kdl:"cmd"`
	Args       []string          `kdl:"args"`
	Timeout    float64           `kdl:"timeout"`
	Persistent bool              `kdl:"persistent"`
	Env        map[string]string `kdl:"env"`
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

	cfg := kdlConfigToConfig(&kdlCfg)
	// `version` and `languages` are parsed for compatibility with config files
	// written from the old default template, but nothing reads them. They keep
	// loading; they are reported rather than silently doing nothing.
	if kdlCfg.Version != "" {
		cfg.Warnings = append(cfg.Warnings, "version has no effect: nothing reads it")
	}
	if kdlCfg.Languages.Go != nil || kdlCfg.Languages.Node != nil || kdlCfg.Languages.Python != nil {
		cfg.Warnings = append(cfg.Warnings, "languages has no effect: project commands come from project detection, not this block")
	}
	return cfg, nil
}

// kdlConfigToConfig converts KDL config to our Config type.
func kdlConfigToConfig(kdlCfg *KDLConfig) *Config {
	cfg := DefaultConfig()

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
	return cfg
}
