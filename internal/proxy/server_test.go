package proxy

import (
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewProxyServer_DefaultVerifiesTLS(t *testing.T) {
	config := ProxyConfig{
		ID:         "tls-default",
		TargetURL:  "https://localhost:9999",
		ListenPort: 0,
		MaxLogSize: 100,
	}

	ps, err := NewProxyServer(config)
	require.NoError(t, err)

	transport := extractHTTPTransport(t, ps)
	if transport.TLSClientConfig != nil {
		assert.False(t, transport.TLSClientConfig.InsecureSkipVerify,
			"default config should verify TLS certificates")
	}
	// TLSClientConfig == nil also means verification is enabled (Go default)
}

func TestNewProxyServer_SkipTLSVerifyDisablesCertCheck(t *testing.T) {
	config := ProxyConfig{
		ID:            "tls-skip",
		TargetURL:     "https://localhost:9999",
		ListenPort:    0,
		MaxLogSize:    100,
		SkipTLSVerify: true,
	}

	ps, err := NewProxyServer(config)
	require.NoError(t, err)

	transport := extractHTTPTransport(t, ps)
	require.NotNil(t, transport.TLSClientConfig,
		"SkipTLSVerify should set TLSClientConfig")
	assert.True(t, transport.TLSClientConfig.InsecureSkipVerify,
		"SkipTLSVerify: true should set InsecureSkipVerify")
}

func TestNewProxyServer_SkipTLSVerifyFalseVerifiesCerts(t *testing.T) {
	config := ProxyConfig{
		ID:            "tls-explicit-false",
		TargetURL:     "https://localhost:9999",
		ListenPort:    0,
		MaxLogSize:    100,
		SkipTLSVerify: false,
	}

	ps, err := NewProxyServer(config)
	require.NoError(t, err)

	transport := extractHTTPTransport(t, ps)
	if transport.TLSClientConfig != nil {
		assert.False(t, transport.TLSClientConfig.InsecureSkipVerify,
			"explicit SkipTLSVerify: false should verify TLS certificates")
	}
}

// extractHTTPTransport unwraps the ChaosTransport to get the underlying *http.Transport.
func extractHTTPTransport(t *testing.T, ps *ProxyServer) *http.Transport {
	t.Helper()

	chaosTransport, ok := ps.proxy.Transport.(*ChaosTransport)
	require.True(t, ok, "expected ChaosTransport wrapper, got %T", ps.proxy.Transport)

	underlying := chaosTransport.underlying
	httpTransport, ok := underlying.(*http.Transport)
	require.True(t, ok, "expected *http.Transport underneath ChaosTransport, got %T", underlying)

	return httpTransport
}

func TestNewProxyServer_DefaultBindAddressIsLocalhost(t *testing.T) {
	config := ProxyConfig{
		ID:         "bind-default",
		TargetURL:  "http://localhost:3000",
		ListenPort: 0,
		MaxLogSize: 100,
	}

	ps, err := NewProxyServer(config)
	require.NoError(t, err)
	assert.Equal(t, "127.0.0.1", ps.BindAddress)
}

func TestNewProxyServer_ExternalBindRejectsWithoutAllowExternal(t *testing.T) {
	cases := []struct {
		name string
		addr string
	}{
		{"all-interfaces-ipv4", "0.0.0.0"},
		{"all-interfaces-ipv6", "::"},
		{"specific-external-ip", "192.168.1.100"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			config := ProxyConfig{
				ID:          "bind-ext-" + tc.name,
				TargetURL:   "http://localhost:3000",
				ListenPort:  0,
				MaxLogSize:  100,
				BindAddress: tc.addr,
			}

			_, err := NewProxyServer(config)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "allow_external")
			assert.Contains(t, err.Error(), tc.addr)
		})
	}
}

func TestNewProxyServer_ExternalBindAllowedWithFlag(t *testing.T) {
	cases := []struct {
		name string
		addr string
	}{
		{"all-interfaces-ipv4", "0.0.0.0"},
		{"all-interfaces-ipv6", "::"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			config := ProxyConfig{
				ID:            "bind-ok-" + tc.name,
				TargetURL:     "http://localhost:3000",
				ListenPort:    0,
				MaxLogSize:    100,
				BindAddress:   tc.addr,
				AllowExternal: true,
			}

			ps, err := NewProxyServer(config)
			require.NoError(t, err)
			assert.Equal(t, tc.addr, ps.BindAddress)
		})
	}
}

func TestNewProxyServer_LocalhostBindNeedsNoFlag(t *testing.T) {
	cases := []struct {
		name string
		addr string
		want string
	}{
		{"empty-defaults-to-localhost", "", "127.0.0.1"},
		{"explicit-localhost-ip", "127.0.0.1", "127.0.0.1"},
		{"ipv6-loopback", "::1", "::1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			config := ProxyConfig{
				ID:          "bind-local-" + tc.name,
				TargetURL:   "http://localhost:3000",
				ListenPort:  0,
				MaxLogSize:  100,
				BindAddress: tc.addr,
			}

			ps, err := NewProxyServer(config)
			require.NoError(t, err)
			assert.Equal(t, tc.want, ps.BindAddress)
		})
	}
}

func TestIsExternalBindAddress(t *testing.T) {
	cases := []struct {
		addr     string
		external bool
	}{
		{"", false},
		{"127.0.0.1", false},
		{"localhost", false},
		{"::1", false},
		{"0.0.0.0", true},
		{"::", true},
		{"192.168.1.1", true},
		{"10.0.0.1", true},
		{"not-an-ip", false}, // unparseable falls through
	}
	for _, tc := range cases {
		t.Run(tc.addr, func(t *testing.T) {
			assert.Equal(t, tc.external, isExternalBindAddress(tc.addr))
		})
	}
}

func TestNewProxyServer_HTTPTargetNoTLSConfig(t *testing.T) {
	// For plain HTTP targets, SkipTLSVerify should still be respected
	// but TLSClientConfig may or may not be set depending on the base transport
	config := ProxyConfig{
		ID:         "http-target",
		TargetURL:  "http://localhost:3000",
		ListenPort: 0,
		MaxLogSize: 100,
	}

	ps, err := NewProxyServer(config)
	require.NoError(t, err)

	transport := extractHTTPTransport(t, ps)
	// For HTTP targets with default config, InsecureSkipVerify should NOT be set
	if transport.TLSClientConfig != nil {
		assert.False(t, transport.TLSClientConfig.InsecureSkipVerify,
			"HTTP target should not have InsecureSkipVerify set")
	}
}

// --- Health check probe tests ---

func TestProbeTCP_Success(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer listener.Close()

	err = probeTCP(listener.Addr().String(), time.Second)
	assert.NoError(t, err, "probeTCP should succeed when backend is listening")
}

func TestProbeTCP_Failure(t *testing.T) {
	err := probeTCP("127.0.0.1:1", 100*time.Millisecond)
	assert.Error(t, err, "probeTCP should fail when nothing is listening")
}

func TestProbeBackend_HealthyToUnhealthy(t *testing.T) {
	config := ProxyConfig{
		ID:                  "health-dead",
		TargetURL:           "http://127.0.0.1:1",
		ListenPort:          0,
		MaxLogSize:          100,
		HealthCheckInterval: 30 * time.Second,
		HealthFailThreshold: 3,
	}

	ps, err := NewProxyServer(config)
	require.NoError(t, err)
	ps.healthProbeTimeout = 100 * time.Millisecond // fast timeout for test
	ps.backendHealthy.Store(true)

	// First two failures: should not trigger unhealthy
	ps.probeBackend()
	assert.True(t, ps.backendHealthy.Load(), "should still be healthy after 1 failure")
	assert.Equal(t, int32(1), ps.healthFailCount.Load())

	ps.probeBackend()
	assert.True(t, ps.backendHealthy.Load(), "should still be healthy after 2 failures")
	assert.Equal(t, int32(2), ps.healthFailCount.Load())

	// Third failure: should trigger unhealthy
	ps.probeBackend()
	assert.False(t, ps.backendHealthy.Load(), "should be unhealthy after 3 failures")
	assert.Equal(t, int32(3), ps.healthFailCount.Load())

	// Verify diagnostic was logged
	diags := ps.logger.Query(LogFilter{Types: []LogEntryType{LogTypeDiagnostic}})
	require.Len(t, diags, 1, "should have logged one unhealthy diagnostic")
	assert.Equal(t, "backend_unhealthy", diags[0].Diagnostic.Event)
}

func TestProbeBackend_UnhealthyRecovery(t *testing.T) {
	backend, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer backend.Close()

	_, port, _ := net.SplitHostPort(backend.Addr().String())
	config := ProxyConfig{
		ID:                  "health-recover",
		TargetURL:           "http://127.0.0.1:" + port,
		ListenPort:          0,
		MaxLogSize:          100,
		HealthCheckInterval: 30 * time.Second,
		HealthFailThreshold: 2,
	}

	ps, err := NewProxyServer(config)
	require.NoError(t, err)

	// Simulate prior unhealthy state
	ps.healthFailCount.Store(2)
	ps.backendHealthy.Store(false)

	// Backend is listening, so probe should succeed and trigger recovery
	ps.probeBackend()

	assert.True(t, ps.backendHealthy.Load(), "should recover when backend responds")
	assert.Equal(t, int32(0), ps.healthFailCount.Load(), "failure count should reset on recovery")
}

func TestProbeBackend_HealthyBackendStaysHealthy(t *testing.T) {
	backend, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer backend.Close()

	_, port, _ := net.SplitHostPort(backend.Addr().String())
	config := ProxyConfig{
		ID:                  "health-ok",
		TargetURL:           "http://127.0.0.1:" + port,
		ListenPort:          0,
		MaxLogSize:          100,
		HealthCheckInterval: 30 * time.Second,
		HealthFailThreshold: 3,
	}

	ps, err := NewProxyServer(config)
	require.NoError(t, err)
	ps.backendHealthy.Store(true)

	for i := 0; i < 5; i++ {
		ps.probeBackend()
	}

	assert.True(t, ps.backendHealthy.Load(), "should stay healthy")
	assert.Equal(t, int32(0), ps.healthFailCount.Load(), "failure count should stay at 0")
}

func TestStats_IncludesBackendHealth(t *testing.T) {
	backend, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer backend.Close()

	_, port, _ := net.SplitHostPort(backend.Addr().String())
	config := ProxyConfig{
		ID:                  "health-stats",
		TargetURL:           "http://127.0.0.1:" + port,
		ListenPort:          0,
		MaxLogSize:          100,
		HealthCheckInterval: 30 * time.Second,
		HealthFailThreshold: 3,
	}

	ps, err := NewProxyServer(config)
	require.NoError(t, err)
	ps.backendHealthy.Store(true)

	stats := ps.Stats()
	assert.True(t, stats.BackendHealthy, "BackendHealthy should be true")
	assert.Equal(t, int32(0), stats.ConsecutiveFailures, "no failures expected")

	// Simulate some failures
	ps.healthFailCount.Store(2)
	stats = ps.Stats()
	assert.Equal(t, int32(2), stats.ConsecutiveFailures)
}

func TestHealthCheckInterval_Default(t *testing.T) {
	config := ProxyConfig{
		ID:         "health-defaults",
		TargetURL:  "http://localhost:3000",
		ListenPort: 0,
		MaxLogSize: 100,
	}

	ps, err := NewProxyServer(config)
	require.NoError(t, err)

	assert.Equal(t, 30*time.Second, ps.healthCheckInterval, "default interval should be 30s")
	assert.Equal(t, int32(3), ps.healthFailThreshold, "default threshold should be 3")
}

func TestHealthCheckThreshold_Custom(t *testing.T) {
	config := ProxyConfig{
		ID:                  "health-custom-threshold",
		TargetURL:           "http://localhost:3000",
		ListenPort:          0,
		MaxLogSize:          100,
		HealthFailThreshold: 5,
	}

	ps, err := NewProxyServer(config)
	require.NoError(t, err)

	assert.Equal(t, int32(5), ps.healthFailThreshold, "custom threshold should be set")
}
