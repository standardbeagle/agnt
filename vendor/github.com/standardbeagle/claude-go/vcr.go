package claude

import (
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"sync"

	"gopkg.in/dnaeon/go-vcr.v3/cassette"
	"gopkg.in/dnaeon/go-vcr.v3/recorder"
)

// VCRMode determines whether the VCR records or replays.
type VCRMode int

const (
	// VCRModeAuto automatically determines mode based on cassette existence.
	// If cassette exists, replay. Otherwise, record.
	VCRModeAuto VCRMode = iota

	// VCRModeRecord always records, overwriting existing cassettes.
	VCRModeRecord

	// VCRModeReplay only replays from existing cassettes, fails if not found.
	VCRModeReplay

	// VCRModePassthrough disables VCR, passes through to real API.
	VCRModePassthrough
)

// VCRServer provides HTTP recording and replay for integration tests.
// It starts a local HTTP server that either:
// - Records requests to the real Anthropic API and saves them to cassettes
// - Replays previously recorded responses from cassettes
//
// Usage:
//
//	vcr := NewVCRServer("testdata/cassettes/my_test")
//	defer vcr.Close()
//	baseURL := vcr.Start()
//	// Use baseURL as ANTHROPIC_BASE_URL
type VCRServer struct {
	cassettePath string
	mode         VCRMode
	targetURL    string

	recorder *recorder.Recorder
	server   *http.Server
	listener net.Listener
	baseURL  string

	mu     sync.Mutex
	closed bool
}

// VCROption configures a VCRServer.
type VCROption func(*VCRServer)

// WithVCRMode sets the recording/replay mode.
func WithVCRMode(mode VCRMode) VCROption {
	return func(v *VCRServer) {
		v.mode = mode
	}
}

// WithTargetURL sets the target API URL for recording mode.
// Defaults to https://api.anthropic.com
func WithTargetURL(url string) VCROption {
	return func(v *VCRServer) {
		v.targetURL = url
	}
}

// NewVCRServer creates a new VCR server for the given cassette path.
// The cassette path should not include the .yaml extension.
func NewVCRServer(cassettePath string, opts ...VCROption) *VCRServer {
	v := &VCRServer{
		cassettePath: cassettePath,
		mode:         VCRModeAuto,
		targetURL:    "https://api.anthropic.com",
	}

	for _, opt := range opts {
		opt(v)
	}

	return v
}

// Start starts the VCR server and returns the base URL to use.
// This URL should be set as ANTHROPIC_BASE_URL.
func (v *VCRServer) Start() (string, error) {
	v.mu.Lock()
	defer v.mu.Unlock()

	if v.baseURL != "" {
		return v.baseURL, nil
	}

	// Determine effective mode
	mode := v.determineMode()

	if mode == VCRModePassthrough {
		// No VCR, just return the real URL
		v.baseURL = v.targetURL
		return v.baseURL, nil
	}

	// Ensure cassette directory exists
	cassetteDir := filepath.Dir(v.cassettePath)
	if err := os.MkdirAll(cassetteDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create cassette directory: %w", err)
	}

	// Configure recorder mode
	var recMode recorder.Mode
	switch mode {
	case VCRModeRecord:
		recMode = recorder.ModeRecordOnly
	case VCRModeReplay:
		recMode = recorder.ModeReplayOnly
	default:
		recMode = recorder.ModeReplayWithNewEpisodes
	}

	// Create recorder with options
	rec, err := recorder.NewWithOptions(&recorder.Options{
		CassetteName:       v.cassettePath,
		Mode:               recMode,
		SkipRequestLatency: true,
	})
	if err != nil {
		return "", fmt.Errorf("failed to create recorder: %w", err)
	}

	// Configure matcher and hooks
	rec.SetMatcher(v.requestMatcher)
	// Use AfterCaptureHook to sanitize each interaction immediately after capture
	rec.AddHook(v.sanitizeHook, recorder.AfterCaptureHook)

	v.recorder = rec

	// Create reverse proxy
	target, err := url.Parse(v.targetURL)
	if err != nil {
		rec.Stop()
		return "", fmt.Errorf("invalid target URL: %w", err)
	}

	proxy := httputil.NewSingleHostReverseProxy(target)

	// Use recorder's transport
	proxy.Transport = rec

	// Modify request to target the real API
	originalDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		originalDirector(req)
		req.Host = target.Host
		req.URL.Host = target.Host
		req.URL.Scheme = target.Scheme
	}

	// Start HTTP server
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		rec.Stop()
		return "", fmt.Errorf("failed to start listener: %w", err)
	}
	v.listener = listener

	v.server = &http.Server{
		Handler: proxy,
	}

	go v.server.Serve(listener)

	v.baseURL = fmt.Sprintf("http://%s", listener.Addr().String())
	return v.baseURL, nil
}

// Close stops the VCR server and saves any recorded cassettes.
func (v *VCRServer) Close() error {
	v.mu.Lock()
	defer v.mu.Unlock()

	if v.closed {
		return nil
	}
	v.closed = true

	var errs []error

	if v.server != nil {
		if err := v.server.Close(); err != nil {
			errs = append(errs, err)
		}
	}

	if v.recorder != nil {
		if err := v.recorder.Stop(); err != nil {
			errs = append(errs, err)
		}
	}

	if len(errs) > 0 {
		return errs[0]
	}
	return nil
}

// BaseURL returns the base URL of the VCR server.
// Returns empty string if not started.
func (v *VCRServer) BaseURL() string {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.baseURL
}

// IsRecording returns true if the VCR is in recording mode.
func (v *VCRServer) IsRecording() bool {
	v.mu.Lock()
	defer v.mu.Unlock()

	if v.recorder == nil {
		return false
	}
	return v.recorder.IsRecording()
}

// determineMode returns the effective mode based on configuration and cassette existence.
func (v *VCRServer) determineMode() VCRMode {
	if v.mode != VCRModeAuto {
		return v.mode
	}

	// Check if cassette exists
	cassettePath := v.cassettePath + ".yaml"
	if _, err := os.Stat(cassettePath); err == nil {
		return VCRModeReplay
	}

	return VCRModeRecord
}

// sanitizeHook removes sensitive data from recorded interactions.
func (v *VCRServer) sanitizeHook(i *cassette.Interaction) error {
	// Remove API key from request headers
	if i.Request.Headers != nil {
		if _, ok := i.Request.Headers["X-Api-Key"]; ok {
			i.Request.Headers["X-Api-Key"] = []string{"[REDACTED]"}
		}
		if _, ok := i.Request.Headers["Authorization"]; ok {
			i.Request.Headers["Authorization"] = []string{"[REDACTED]"}
		}
		// Remove other potentially sensitive headers
		delete(i.Request.Headers, "Cookie")
		delete(i.Request.Headers, "Set-Cookie")
	}

	// Remove sensitive response headers
	if i.Response.Headers != nil {
		delete(i.Response.Headers, "Set-Cookie")
	}

	return nil
}

// requestMatcher matches requests for replay.
func (v *VCRServer) requestMatcher(r *http.Request, i cassette.Request) bool {
	// Match on method and path (ignore host since we're proxying)
	if r.Method != i.Method {
		return false
	}

	// Compare paths
	reqPath := r.URL.Path
	casPath := ""
	if parsedURL, err := url.Parse(i.URL); err == nil {
		casPath = parsedURL.Path
	}

	return reqPath == casPath
}

// VCRTestHelper provides convenient test setup for VCR-based tests.
type VCRTestHelper struct {
	vcr      *VCRServer
	origEnv  map[string]string
	envVars  []string
}

// NewVCRTestHelper creates a test helper that manages VCR and environment.
func NewVCRTestHelper(cassetteName string, opts ...VCROption) *VCRTestHelper {
	cassettePath := filepath.Join("testdata", "cassettes", cassetteName)
	return &VCRTestHelper{
		vcr:     NewVCRServer(cassettePath, opts...),
		origEnv: make(map[string]string),
		envVars: []string{
			EnvAnthropicBaseURL,
			EnvClaudeAPIURL,
			EnvClaudeCodeAPIURL,
		},
	}
}

// Setup starts the VCR and configures environment variables.
// Returns a cleanup function that should be deferred.
func (h *VCRTestHelper) Setup() (cleanup func(), err error) {
	// Save original environment
	for _, key := range h.envVars {
		h.origEnv[key] = os.Getenv(key)
	}

	// Start VCR server
	baseURL, err := h.vcr.Start()
	if err != nil {
		return nil, fmt.Errorf("failed to start VCR: %w", err)
	}

	// Set environment to use VCR
	os.Setenv(EnvAnthropicBaseURL, baseURL)

	cleanup = func() {
		// Restore original environment
		for key, val := range h.origEnv {
			if val == "" {
				os.Unsetenv(key)
			} else {
				os.Setenv(key, val)
			}
		}
		// Close VCR
		h.vcr.Close()
	}

	return cleanup, nil
}

// BaseURL returns the VCR server's base URL.
func (h *VCRTestHelper) BaseURL() string {
	return h.vcr.BaseURL()
}

// IsRecording returns true if recording new interactions.
func (h *VCRTestHelper) IsRecording() bool {
	return h.vcr.IsRecording()
}

// CreateVCRClient creates a Claude client configured to use the VCR.
func (h *VCRTestHelper) CreateVCRClient(opts *AgentOptions) *Client {
	if opts == nil {
		opts = &AgentOptions{}
	}

	// Ensure environment includes VCR base URL
	if opts.Environment == nil {
		opts.Environment = make(map[string]string)
	}
	opts.Environment[EnvAnthropicBaseURL] = h.BaseURL()

	return New(opts)
}

// VCRRecorder provides direct access to the go-vcr recorder for testing.
// Use this when you want to test HTTP interactions directly without the proxy server.
type VCRRecorder struct {
	cassettePath string
	mode         VCRMode
	targetURL    string
	recorder     *recorder.Recorder
	closed       bool
	mu           sync.Mutex
}

// NewVCRRecorder creates a VCR recorder for direct HTTP client use.
// This is useful for testing HTTP-level interactions without the proxy overhead.
func NewVCRRecorder(cassettePath string, opts ...VCROption) (*VCRRecorder, error) {
	// Apply options using a temporary VCRServer to parse them
	temp := &VCRServer{
		cassettePath: cassettePath,
		mode:         VCRModeAuto,
		targetURL:    "https://api.anthropic.com",
	}
	for _, opt := range opts {
		opt(temp)
	}

	v := &VCRRecorder{
		cassettePath: cassettePath,
		mode:         temp.mode,
		targetURL:    temp.targetURL,
	}

	// Ensure cassette directory exists
	cassetteDir := filepath.Dir(cassettePath)
	if err := os.MkdirAll(cassetteDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create cassette directory: %w", err)
	}

	// Determine effective mode
	mode := v.mode
	if mode == VCRModeAuto {
		cassFile := cassettePath + ".yaml"
		if _, err := os.Stat(cassFile); err == nil {
			mode = VCRModeReplay
		} else {
			mode = VCRModeRecord
		}
	}

	if mode == VCRModePassthrough {
		return v, nil
	}

	// Configure recorder mode
	var recMode recorder.Mode
	switch mode {
	case VCRModeRecord:
		recMode = recorder.ModeRecordOnly
	case VCRModeReplay:
		recMode = recorder.ModeReplayOnly
	default:
		recMode = recorder.ModeReplayWithNewEpisodes
	}

	rec, err := recorder.NewWithOptions(&recorder.Options{
		CassetteName:       cassettePath,
		Mode:               recMode,
		SkipRequestLatency: true,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create recorder: %w", err)
	}

	// Add sanitization hook
	rec.AddHook(func(i *cassette.Interaction) error {
		// Remove API key from request headers
		if i.Request.Headers != nil {
			if _, ok := i.Request.Headers["X-Api-Key"]; ok {
				i.Request.Headers["X-Api-Key"] = []string{"[REDACTED]"}
			}
			if _, ok := i.Request.Headers["Authorization"]; ok {
				i.Request.Headers["Authorization"] = []string{"[REDACTED]"}
			}
			delete(i.Request.Headers, "Cookie")
			delete(i.Request.Headers, "Set-Cookie")
		}
		if i.Response.Headers != nil {
			delete(i.Response.Headers, "Set-Cookie")
		}
		return nil
	}, recorder.AfterCaptureHook)

	v.recorder = rec
	return v, nil
}

// HTTPClient returns an http.Client configured for VCR recording/replay.
func (v *VCRRecorder) HTTPClient() *http.Client {
	if v.recorder != nil {
		return &http.Client{Transport: v.recorder}
	}
	return http.DefaultClient
}

// Close stops the recorder and saves the cassette.
func (v *VCRRecorder) Close() error {
	v.mu.Lock()
	defer v.mu.Unlock()

	if v.closed {
		return nil
	}
	v.closed = true

	if v.recorder != nil {
		return v.recorder.Stop()
	}
	return nil
}

// IsRecording returns true if the recorder is in recording mode.
func (v *VCRRecorder) IsRecording() bool {
	if v.recorder == nil {
		return false
	}
	return v.recorder.IsRecording()
}

// RecordingTransport returns an http.RoundTripper that records/replays.
// Useful for direct API testing without the CLI.
func (v *VCRServer) RecordingTransport() http.RoundTripper {
	if v.recorder != nil {
		return v.recorder
	}

	// Return default transport if not recording
	return &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: false},
	}
}

// HTTPClient returns an http.Client configured for VCR recording/replay.
// Useful for testing HTTP-level interactions directly.
func (v *VCRServer) HTTPClient() *http.Client {
	return &http.Client{
		Transport: v.RecordingTransport(),
	}
}
