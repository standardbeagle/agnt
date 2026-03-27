package proxy

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAcceptsHTML(t *testing.T) {
	tests := []struct {
		name   string
		accept string
		want   bool
	}{
		{"browser navigation", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8", true},
		{"json api", "application/json", false},
		{"empty", "", false},
		{"wildcard only", "*/*", false},
		{"html only", "text/html", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/", nil)
			if tt.accept != "" {
				r.Header.Set("Accept", tt.accept)
			}
			assert.Equal(t, tt.want, acceptsHTML(r))
		})
	}
}

func TestServeLoadingPage(t *testing.T) {
	ps := &ProxyServer{
		startTime: time.Now().Add(-5 * time.Second),
	}

	w := httptest.NewRecorder()
	ps.serveLoadingPage(w, nil, "http://localhost:3000")

	resp := w.Result()
	require.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
	assert.Equal(t, "text/html; charset=utf-8", resp.Header.Get("Content-Type"))
	assert.Equal(t, "3", resp.Header.Get("Retry-After"))

	body := w.Body.String()
	assert.Contains(t, body, "Waiting for server")
	assert.Contains(t, body, "http://localhost:3000")
	assert.Contains(t, body, `meta http-equiv="refresh" content="3"`)
	assert.Contains(t, body, "<!DOCTYPE html>")
}

func TestServeLoadingPageTimerFormat(t *testing.T) {
	ps := &ProxyServer{
		startTime: time.Now().Add(-45 * time.Second),
	}

	w := httptest.NewRecorder()
	ps.serveLoadingPage(w, nil, "http://localhost:8080")

	body := w.Body.String()
	assert.Contains(t, body, "00:45")
}

func TestServeLoadingPageTimeout(t *testing.T) {
	// After maxLoadingWait, should serve error page instead of loading page
	ps := &ProxyServer{
		startTime: time.Now().Add(-90 * time.Second),
	}

	w := httptest.NewRecorder()
	ps.serveLoadingPage(w, nil, "http://localhost:3456")

	resp := w.Result()
	require.Equal(t, http.StatusBadGateway, resp.StatusCode)

	body := w.Body.String()
	assert.Contains(t, body, "Server not responding")
	assert.Contains(t, body, "http://localhost:3456")
	assert.Contains(t, body, "Retry")
	assert.NotContains(t, body, `meta http-equiv="refresh"`, "error page should not auto-refresh")
}

func TestLoadingPageDarkModeSupport(t *testing.T) {
	ps := &ProxyServer{
		startTime: time.Now(),
	}

	w := httptest.NewRecorder()
	ps.serveLoadingPage(w, nil, "http://localhost:3000")

	body := w.Body.String()
	assert.Contains(t, body, "prefers-color-scheme:dark")
}

func TestLoadingPageNoExternalDependencies(t *testing.T) {
	ps := &ProxyServer{
		startTime: time.Now(),
	}

	w := httptest.NewRecorder()
	ps.serveLoadingPage(w, nil, "http://localhost:3000")

	body := w.Body.String()
	// No external URLs (http:// or https://) except the target URL itself
	for _, line := range strings.Split(body, "\n") {
		if strings.Contains(line, "http://localhost:3000") {
			continue
		}
		if strings.Contains(line, "src=") || strings.Contains(line, "href=") {
			assert.NotContains(t, line, "http://", "page should be self-contained")
			assert.NotContains(t, line, "https://", "page should be self-contained")
		}
	}
}
