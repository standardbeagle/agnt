package config

import (
	"time"
)

// Config holds the complete server configuration.
type Config struct {
	// Settings are global server settings.
	Settings Settings `json:"settings"`

	// AI holds user-level defaults for agnt run / ai behavior.
	//
	// AIConfig's own fields carry only kdl tags, so marshalling this struct to
	// JSON would emit them under their Go names. Nothing does today — this config
	// is read from KDL — but the json tag here implies a round trip that does not
	// hold. Keep the two in sync if a JSON surface ever appears.
	AI *AIConfig `json:"ai,omitempty"`

	// Feedback holds the public-plane anonymous feedback ingestion limits (P8).
	Feedback FeedbackConfig `json:"feedback"`

	// PublicPlane holds the public-plane request-rate limits (artifact GET +
	// per-origin outbound fetch).
	PublicPlane PublicPlaneConfig `json:"public_plane"`

	// Warnings holds non-fatal notes collected while parsing the global
	// config: today, the keys that are still accepted but that nothing reads
	// (`version`, `languages`). Not a config key. The caller that loads the
	// config is expected to surface them where the operator will see them.
	Warnings []string `json:"-"`
}

// DefaultGracefulTimeout is the canonical graceful-shutdown budget: SIGTERM,
// wait this long, then SIGKILL. Referenced by the app config default, the
// daemon's process manager, and the self-upgrade path so the value cannot
// drift between copies.
const DefaultGracefulTimeout = 5 * time.Second

// Settings holds global configuration settings.
type Settings struct {
	// DefaultTimeout is the default process timeout.
	DefaultTimeout time.Duration `json:"default_timeout"`
	// MaxOutputBuffer is the per-stream output buffer size.
	MaxOutputBuffer int `json:"max_output_buffer"`
	// GracefulTimeout is the graceful shutdown timeout.
	GracefulTimeout time.Duration `json:"graceful_timeout"`
}

// DefaultConfig returns the default configuration.
func DefaultConfig() *Config {
	return &Config{
		Settings: Settings{
			DefaultTimeout:  0, // No timeout
			MaxOutputBuffer: 256 * 1024,
			GracefulTimeout: DefaultGracefulTimeout,
		},
		Feedback:    DefaultFeedbackConfig(),
		PublicPlane: DefaultPublicPlaneConfig(),
	}
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
