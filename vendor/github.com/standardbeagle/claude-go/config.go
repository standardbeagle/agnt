package claude

import (
	"os"
	"strconv"
	"strings"
)

// Environment variable names for Claude Code configuration.
const (
	// API Authentication
	EnvAnthropicAPIKey   = "ANTHROPIC_API_KEY"
	EnvClaudeCodeAPIKey  = "CLAUDE_CODE_API_KEY"
	EnvClaudeAPIKey      = "CLAUDE_API_KEY"
	EnvClaudeAccessToken = "CLAUDE_ACCESS_TOKEN"

	// API Endpoint Configuration
	EnvAnthropicBaseURL = "ANTHROPIC_BASE_URL"
	EnvClaudeAPIURL     = "CLAUDE_API_URL"
	EnvClaudeCodeAPIURL = "CLAUDE_CODE_API_URL"

	// Model Configuration
	EnvClaudeModel          = "CLAUDE_MODEL"
	EnvClaudeCodeModel      = "CLAUDE_CODE_MODEL"
	EnvClaudeSmallFastModel = "CLAUDE_CODE_SMALL_FAST_MODEL"
	EnvClaudeBigModel       = "CLAUDE_CODE_BIG_MODEL"

	// MinMax Configuration
	EnvMinMaxAPIKey = "MINMAX_API_KEY"
	EnvMinMaxAuthToken = "ANTHROPIC_AUTH_TOKEN"

	// GLM Configuration
	EnvGLMAPIToken = "ANTHROPIC_AUTH_TOKEN"

	// Provider Selection
	EnvClaudeCodeUseBedrock = "CLAUDE_CODE_USE_BEDROCK"
	EnvClaudeCodeUseVertex  = "CLAUDE_CODE_USE_VERTEX"

	// AWS Bedrock Configuration
	EnvAWSRegion            = "AWS_REGION"
	EnvAWSDefaultRegion     = "AWS_DEFAULT_REGION"
	EnvAWSAccessKeyID       = "AWS_ACCESS_KEY_ID"
	EnvAWSSecretAccessKey   = "AWS_SECRET_ACCESS_KEY"
	EnvAWSSessionToken      = "AWS_SESSION_TOKEN"
	EnvAWSProfile           = "AWS_PROFILE"
	EnvBedrockEndpointURL   = "BEDROCK_ENDPOINT_URL"
	EnvBedrockCrossRegion   = "BEDROCK_CROSS_REGION"
	EnvBedrockPromptCaching = "BEDROCK_PROMPT_CACHING"

	// Google Vertex AI Configuration
	EnvVertexProject  = "ANTHROPIC_VERTEX_PROJECT_ID"
	EnvVertexRegion   = "ANTHROPIC_VERTEX_REGION"
	EnvVertexLocation = "CLOUD_ML_REGION"
	EnvGoogleProject  = "GOOGLE_CLOUD_PROJECT"
	EnvGoogleRegion   = "GOOGLE_CLOUD_REGION"

	// OAuth / Authentication
	EnvClaudeCodeSkipOAuth    = "CLAUDE_CODE_SKIP_OAUTH"
	EnvClaudeCodeDisableOAuth = "CLAUDE_CODE_DISABLE_OAUTH"
	EnvClaudeCodeTokens       = "CLAUDE_CODE_TOKENS"

	// Proxy Configuration
	EnvHTTPProxy  = "HTTP_PROXY"
	EnvHTTPSProxy = "HTTPS_PROXY"
	EnvNoProxy    = "NO_PROXY"

	// Runtime Configuration
	EnvClaudeCodeMaxTokens     = "CLAUDE_CODE_MAX_TOKENS"
	EnvClaudeCodeMaxTurns      = "CLAUDE_CODE_MAX_TURNS"
	EnvClaudeCodeTimeout       = "CLAUDE_CODE_TIMEOUT"
	EnvClaudeCodeMaxBudgetUSD  = "CLAUDE_CODE_MAX_BUDGET_USD"
	EnvClaudeCodeDebug         = "CLAUDE_CODE_DEBUG"
	EnvClaudeCodeVerbose       = "CLAUDE_CODE_VERBOSE"
	EnvClaudeCodeNoTelemetry   = "CLAUDE_CODE_NO_TELEMETRY"
	EnvClaudeCodeEntrypoint    = "CLAUDE_CODE_ENTRYPOINT"
	EnvClaudeCodeSkipPermcheck = "CLAUDE_CODE_SKIP_PERMCHECK"
)

// APIProvider represents the cloud provider for the Claude API.
type APIProvider string

const (
	APIProviderAnthropic APIProvider = "anthropic"
	APIProviderBedrock   APIProvider = "bedrock"
	APIProviderVertex    APIProvider = "vertex"
	APIProviderGLM       APIProvider = "glm"       // ChatGLM / Zhipu AI
	APIProviderMinMax    APIProvider = "minmax"    // MiniMax
)

// ModelTier represents different model quality/speed tiers.
type ModelTier string

const (
	ModelTierDefault   ModelTier = ""
	ModelTierSmallFast ModelTier = "small-fast" // Haiku-class
	ModelTierDefault_  ModelTier = "default"    // Sonnet-class
	ModelTierBig       ModelTier = "big"        // Opus-class
)

// Config holds all configuration options for the Claude SDK.
// It can be populated from environment variables, code, or both.
type Config struct {
	// API Authentication
	APIKey      string `json:"api_key,omitempty"`
	AccessToken string `json:"access_token,omitempty"`

	// API Endpoint
	BaseURL string `json:"base_url,omitempty"`

	// Provider Selection
	Provider APIProvider `json:"provider,omitempty"`

	// Model Configuration
	Model          string `json:"model,omitempty"`
	SmallFastModel string `json:"small_fast_model,omitempty"`
	BigModel       string `json:"big_model,omitempty"`

	// AWS Bedrock Configuration
	Bedrock *BedrockConfig `json:"bedrock,omitempty"`

	// Google Vertex Configuration
	Vertex *VertexConfig `json:"vertex,omitempty"`

	// Proxy Configuration
	Proxy *ProxyConfig `json:"proxy,omitempty"`

	// Runtime Limits
	MaxTokens    int     `json:"max_tokens,omitempty"`
	MaxTurns     int     `json:"max_turns,omitempty"`
	TimeoutSecs  int     `json:"timeout_secs,omitempty"`
	MaxBudgetUSD float64 `json:"max_budget_usd,omitempty"`

	// Behavior Flags
	Debug       bool `json:"debug,omitempty"`
	Verbose     bool `json:"verbose,omitempty"`
	NoTelemetry bool `json:"no_telemetry,omitempty"`
	SkipOAuth   bool `json:"skip_oauth,omitempty"`

	// CLI Path
	CLIPath string `json:"cli_path,omitempty"`
}

// BedrockConfig holds AWS Bedrock-specific configuration.
type BedrockConfig struct {
	Region            string `json:"region,omitempty"`
	EndpointURL       string `json:"endpoint_url,omitempty"`
	AccessKeyID       string `json:"access_key_id,omitempty"`
	SecretAccessKey   string `json:"secret_access_key,omitempty"`
	SessionToken      string `json:"session_token,omitempty"`
	Profile           string `json:"profile,omitempty"`
	CrossRegion       bool   `json:"cross_region,omitempty"`
	PromptCaching     bool   `json:"prompt_caching,omitempty"`
	PromptCacheTypes  string `json:"prompt_cache_types,omitempty"`
	UseStreamingTypes bool   `json:"use_streaming_types,omitempty"`
}

// VertexConfig holds Google Vertex AI-specific configuration.
type VertexConfig struct {
	ProjectID string `json:"project_id,omitempty"`
	Region    string `json:"region,omitempty"`
}

// ProxyConfig holds HTTP proxy configuration.
type ProxyConfig struct {
	HTTPProxy  string `json:"http_proxy,omitempty"`
	HTTPSProxy string `json:"https_proxy,omitempty"`
	NoProxy    string `json:"no_proxy,omitempty"`
}

// LoadConfigFromEnv creates a Config populated from environment variables.
// Values can be overridden programmatically after loading.
func LoadConfigFromEnv() *Config {
	cfg := &Config{}

	// API Key (check multiple env vars in order of preference)
	cfg.APIKey = getFirstEnv(EnvClaudeCodeAPIKey, EnvClaudeAPIKey, EnvAnthropicAPIKey)
	cfg.AccessToken = os.Getenv(EnvClaudeAccessToken)

	// Base URL
	cfg.BaseURL = getFirstEnv(EnvClaudeCodeAPIURL, EnvClaudeAPIURL, EnvAnthropicBaseURL)

	// Provider selection - detect from base URL first
	baseURL := getFirstEnv(EnvClaudeCodeAPIURL, EnvClaudeAPIURL, EnvAnthropicBaseURL)
	if strings.Contains(baseURL, "minimax.io") {
		cfg.Provider = APIProviderMinMax
	} else if strings.Contains(baseURL, "z.ai") || strings.Contains(baseURL, "chatglm.cn") {
		cfg.Provider = APIProviderGLM
	} else if envBool(EnvClaudeCodeUseBedrock) {
		cfg.Provider = APIProviderBedrock
	} else if envBool(EnvClaudeCodeUseVertex) {
		cfg.Provider = APIProviderVertex
	} else {
		cfg.Provider = APIProviderAnthropic
	}

	// Model configuration
	cfg.Model = getFirstEnv(EnvClaudeCodeModel, EnvClaudeModel)
	cfg.SmallFastModel = os.Getenv(EnvClaudeSmallFastModel)
	cfg.BigModel = os.Getenv(EnvClaudeBigModel)

	// Bedrock configuration
	if cfg.Provider == APIProviderBedrock || os.Getenv(EnvAWSRegion) != "" {
		cfg.Bedrock = &BedrockConfig{
			Region:          getFirstEnv(EnvAWSRegion, EnvAWSDefaultRegion),
			EndpointURL:     os.Getenv(EnvBedrockEndpointURL),
			AccessKeyID:     os.Getenv(EnvAWSAccessKeyID),
			SecretAccessKey: os.Getenv(EnvAWSSecretAccessKey),
			SessionToken:    os.Getenv(EnvAWSSessionToken),
			Profile:         os.Getenv(EnvAWSProfile),
			CrossRegion:     envBool(EnvBedrockCrossRegion),
			PromptCaching:   envBool(EnvBedrockPromptCaching),
		}
	}

	// Vertex configuration
	if cfg.Provider == APIProviderVertex || os.Getenv(EnvVertexProject) != "" {
		cfg.Vertex = &VertexConfig{
			ProjectID: getFirstEnv(EnvVertexProject, EnvGoogleProject),
			Region:    getFirstEnv(EnvVertexRegion, EnvVertexLocation, EnvGoogleRegion),
		}
	}

	// Proxy configuration
	httpProxy := os.Getenv(EnvHTTPProxy)
	httpsProxy := os.Getenv(EnvHTTPSProxy)
	noProxy := os.Getenv(EnvNoProxy)
	if httpProxy != "" || httpsProxy != "" {
		cfg.Proxy = &ProxyConfig{
			HTTPProxy:  httpProxy,
			HTTPSProxy: httpsProxy,
			NoProxy:    noProxy,
		}
	}

	// Runtime limits
	cfg.MaxTokens = envInt(EnvClaudeCodeMaxTokens)
	cfg.MaxTurns = envInt(EnvClaudeCodeMaxTurns)
	cfg.TimeoutSecs = envInt(EnvClaudeCodeTimeout)
	cfg.MaxBudgetUSD = envFloat(EnvClaudeCodeMaxBudgetUSD)

	// Behavior flags
	cfg.Debug = envBool(EnvClaudeCodeDebug)
	cfg.Verbose = envBool(EnvClaudeCodeVerbose)
	cfg.NoTelemetry = envBool(EnvClaudeCodeNoTelemetry)
	cfg.SkipOAuth = envBool(EnvClaudeCodeSkipOAuth) || envBool(EnvClaudeCodeDisableOAuth)

	// CLI Path
	cfg.CLIPath = os.Getenv("CLAUDE_CLI_PATH")

	return cfg
}

// ToAgentOptions converts Config to AgentOptions for use with the SDK.
func (c *Config) ToAgentOptions() *AgentOptions {
	opts := &AgentOptions{
		Model:        c.Model,
		MaxTurns:     c.MaxTurns,
		MaxBudgetUSD: c.MaxBudgetUSD,
		Debug:        c.Debug,
		Verbose:      c.Verbose,
		CLIPath:      c.CLIPath,
	}

	// Build environment variables to pass to CLI
	env := make(map[string]string)

	if c.APIKey != "" {
		env[EnvAnthropicAPIKey] = c.APIKey
	}
	if c.AccessToken != "" {
		env[EnvClaudeAccessToken] = c.AccessToken
	}
	if c.BaseURL != "" {
		env[EnvAnthropicBaseURL] = c.BaseURL
	}

	// Provider-specific environment
	switch c.Provider {
	case APIProviderBedrock:
		env[EnvClaudeCodeUseBedrock] = "1"
		if c.Bedrock != nil {
			if c.Bedrock.Region != "" {
				env[EnvAWSRegion] = c.Bedrock.Region
			}
			if c.Bedrock.EndpointURL != "" {
				env[EnvBedrockEndpointURL] = c.Bedrock.EndpointURL
			}
			if c.Bedrock.AccessKeyID != "" {
				env[EnvAWSAccessKeyID] = c.Bedrock.AccessKeyID
			}
			if c.Bedrock.SecretAccessKey != "" {
				env[EnvAWSSecretAccessKey] = c.Bedrock.SecretAccessKey
			}
			if c.Bedrock.SessionToken != "" {
				env[EnvAWSSessionToken] = c.Bedrock.SessionToken
			}
			if c.Bedrock.Profile != "" {
				env[EnvAWSProfile] = c.Bedrock.Profile
			}
			if c.Bedrock.CrossRegion {
				env[EnvBedrockCrossRegion] = "1"
			}
			if c.Bedrock.PromptCaching {
				env[EnvBedrockPromptCaching] = "1"
			}
		}
	case APIProviderVertex:
		env[EnvClaudeCodeUseVertex] = "1"
		if c.Vertex != nil {
			if c.Vertex.ProjectID != "" {
				env[EnvVertexProject] = c.Vertex.ProjectID
			}
			if c.Vertex.Region != "" {
				env[EnvVertexRegion] = c.Vertex.Region
			}
		}
	}

	// Model tiers
	if c.SmallFastModel != "" {
		env[EnvClaudeSmallFastModel] = c.SmallFastModel
	}
	if c.BigModel != "" {
		env[EnvClaudeBigModel] = c.BigModel
	}

	// Behavior flags
	if c.NoTelemetry {
		env[EnvClaudeCodeNoTelemetry] = "1"
	}
	if c.SkipOAuth {
		env[EnvClaudeCodeSkipOAuth] = "1"
	}

	// Proxy configuration
	if c.Proxy != nil {
		if c.Proxy.HTTPProxy != "" {
			env[EnvHTTPProxy] = c.Proxy.HTTPProxy
		}
		if c.Proxy.HTTPSProxy != "" {
			env[EnvHTTPSProxy] = c.Proxy.HTTPSProxy
		}
		if c.Proxy.NoProxy != "" {
			env[EnvNoProxy] = c.Proxy.NoProxy
		}
	}

	if len(env) > 0 {
		opts.Environment = env
	}

	return opts
}

// Merge combines two configs, with the other config taking precedence.
func (c *Config) Merge(other *Config) *Config {
	if other == nil {
		return c
	}

	merged := *c

	if other.APIKey != "" {
		merged.APIKey = other.APIKey
	}
	if other.AccessToken != "" {
		merged.AccessToken = other.AccessToken
	}
	if other.BaseURL != "" {
		merged.BaseURL = other.BaseURL
	}
	if other.Provider != "" {
		merged.Provider = other.Provider
	}
	if other.Model != "" {
		merged.Model = other.Model
	}
	if other.SmallFastModel != "" {
		merged.SmallFastModel = other.SmallFastModel
	}
	if other.BigModel != "" {
		merged.BigModel = other.BigModel
	}
	if other.Bedrock != nil {
		merged.Bedrock = other.Bedrock
	}
	if other.Vertex != nil {
		merged.Vertex = other.Vertex
	}
	if other.Proxy != nil {
		merged.Proxy = other.Proxy
	}
	if other.MaxTokens > 0 {
		merged.MaxTokens = other.MaxTokens
	}
	if other.MaxTurns > 0 {
		merged.MaxTurns = other.MaxTurns
	}
	if other.TimeoutSecs > 0 {
		merged.TimeoutSecs = other.TimeoutSecs
	}
	if other.MaxBudgetUSD > 0 {
		merged.MaxBudgetUSD = other.MaxBudgetUSD
	}
	if other.Debug {
		merged.Debug = true
	}
	if other.Verbose {
		merged.Verbose = true
	}
	if other.NoTelemetry {
		merged.NoTelemetry = true
	}
	if other.SkipOAuth {
		merged.SkipOAuth = true
	}
	if other.CLIPath != "" {
		merged.CLIPath = other.CLIPath
	}

	return &merged
}

// NewConfigFromOptions creates a Config from AgentOptions.
// This is useful for round-tripping between the two formats.
func NewConfigFromOptions(opts *AgentOptions) *Config {
	if opts == nil {
		return &Config{}
	}

	cfg := &Config{
		Model:        opts.Model,
		MaxTurns:     opts.MaxTurns,
		MaxBudgetUSD: opts.MaxBudgetUSD,
		Debug:        opts.Debug,
		Verbose:      opts.Verbose,
		CLIPath:      opts.CLIPath,
	}

	// Extract known environment variables from Options.Environment
	if opts.Environment != nil {
		if v := opts.Environment[EnvAnthropicAPIKey]; v != "" {
			cfg.APIKey = v
		}
		if v := opts.Environment[EnvClaudeAccessToken]; v != "" {
			cfg.AccessToken = v
		}
		if v := opts.Environment[EnvAnthropicBaseURL]; v != "" {
			cfg.BaseURL = v
		}
		if v := opts.Environment[EnvClaudeCodeUseBedrock]; v != "" && v != "0" && v != "false" {
			cfg.Provider = APIProviderBedrock
		}
		if v := opts.Environment[EnvClaudeCodeUseVertex]; v != "" && v != "0" && v != "false" {
			cfg.Provider = APIProviderVertex
		}
	}

	return cfg
}

// Helper functions

func getFirstEnv(keys ...string) string {
	for _, key := range keys {
		if v := os.Getenv(key); v != "" {
			return v
		}
	}
	return ""
}

func envBool(key string) bool {
	v := strings.ToLower(os.Getenv(key))
	return v == "1" || v == "true" || v == "yes" || v == "on"
}

func envInt(key string) int {
	v := os.Getenv(key)
	if v == "" {
		return 0
	}
	i, _ := strconv.Atoi(v)
	return i
}

func envFloat(key string) float64 {
	v := os.Getenv(key)
	if v == "" {
		return 0
	}
	f, _ := strconv.ParseFloat(v, 64)
	return f
}

// Standard model identifiers for convenience.
const (
	ModelClaude4Opus   = "claude-opus-4-20250514"
	ModelClaude45Opus  = "claude-opus-4-5-20251101"
	ModelClaude4Sonnet = "claude-sonnet-4-20250514"
	ModelClaude35Haiku = "claude-3-5-haiku-20241022"

	// Shorthand aliases
	ModelOpus   = "opus"
	ModelSonnet = "sonnet"
	ModelHaiku  = "haiku"

	// GLM (ChatGLM/Zhipu AI) models
	ModelGLM4   = "glm-4"
	ModelGLM4Plus = "glm-4-plus"
	ModelGLM3_5 = "glm-3-5-turbo"

	// MinMax models
	ModelMinMaxABAB6_5Chat = "minimax-abab6.5-chat"
	ModelMinMaxABAB6Chat   = "minimax-abab6-chat"
)

// Model tier defaults
const (
	DefaultSmallFastModel = ModelClaude35Haiku
	DefaultModel          = ModelClaude4Sonnet
	DefaultBigModel       = ModelClaude45Opus
)
