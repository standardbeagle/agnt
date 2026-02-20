package proxy

import (
	"net/http"
	"testing"

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
