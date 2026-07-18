package proxy

// Secret-entry flow tests (host-safe, no real browser): a real gorilla WS
// client dials the live proxy's /__devtool_metrics endpoint and submits a
// secret exactly as toast.js does ("secret_submit"). The tests assert the
// zero-leak invariant: the value reaches ONLY the daemon-wired sink, while
// the full agent-visible transcript — every traffic-log entry, every overlay
// notification, every frame broadcast back to the browser — contains the
// secret value ZERO times.

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// overlayCapture runs a fake agent-overlay server on a unix socket and
// records every request body the proxy's OverlayNotifier posts to it —
// this is the channel/PTY-bound side of the transcript.
type overlayCapture struct {
	mu     sync.Mutex
	bodies []string
}

func (oc *overlayCapture) all() []string {
	oc.mu.Lock()
	defer oc.mu.Unlock()
	return append([]string(nil), oc.bodies...)
}

func startOverlayCapture(t *testing.T) (*overlayCapture, string) {
	t.Helper()
	oc := &overlayCapture{}
	socketPath := filepath.Join(t.TempDir(), "overlay.sock")
	ln, err := net.Listen("unix", socketPath)
	require.NoError(t, err)
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		oc.mu.Lock()
		oc.bodies = append(oc.bodies, string(body))
		oc.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	})}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return oc, socketPath
}

// submitSecret drives the browser side of the flow and returns the ack.
func submitSecret(t *testing.T, listenAddr, name, value string) map[string]interface{} {
	t.Helper()
	conn := dialMetrics(t, listenAddr)
	require.NoError(t, conn.WriteJSON(map[string]interface{}{
		"type": "secret_submit",
		"data": map[string]interface{}{"name": name, "value": value},
		"url":  "/app",
	}))
	// Read frames until the secret_ack arrives (activity broadcasts may
	// interleave). Every read frame doubles as browser-bound transcript.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
		_, raw, err := conn.ReadMessage()
		require.NoError(t, err, "waiting for secret_ack")
		var frame map[string]interface{}
		require.NoError(t, json.Unmarshal(raw, &frame))
		if frame["type"] == "secret_ack" {
			return frame
		}
	}
	t.Fatal("no secret_ack received")
	return nil
}

// transcriptDump marshals every agent-visible surface into one string:
// all traffic-log entries plus all captured overlay notification bodies.
func transcriptDump(t *testing.T, ps *ProxyServer, oc *overlayCapture) string {
	t.Helper()
	var sb strings.Builder
	for _, entry := range ps.Logger().Query(LogFilter{Limit: 1000}) {
		data, err := json.Marshal(entry)
		require.NoError(t, err)
		sb.Write(data)
		sb.WriteByte('\n')
	}
	for _, body := range oc.all() {
		sb.WriteString(body)
		sb.WriteByte('\n')
	}
	return sb.String()
}

func TestSecretSubmit_E2E_ValueNeverEntersTranscript(t *testing.T) {
	backend := newWrapBackend(t)
	ps := startWrapProxy(t, backend)

	oc, socketPath := startOverlayCapture(t)
	ps.OverlayNotifier().SetEndpoint(socketPath)

	const secretName = "FIGMA_KEY"
	const secretValue = "sk-live-supersecret-token-XYZ1"

	var sinkMu sync.Mutex
	var sinkName, sinkValue string
	ps.SetSecretSink(func(name, value string) error {
		sinkMu.Lock()
		defer sinkMu.Unlock()
		sinkName, sinkValue = name, value
		return nil
	})

	ack := submitSecret(t, ps.ListenAddr, secretName, secretValue)
	assert.Equal(t, true, ack["ok"], "submission must be acked ok")
	assert.Equal(t, secretName, ack["name"])

	// The sink — and only the sink — received the value.
	sinkMu.Lock()
	assert.Equal(t, secretName, sinkName)
	assert.Equal(t, secretValue, sinkValue)
	sinkMu.Unlock()

	// The fingerprint-only receipt must reach the panel log and the overlay.
	require.Eventually(t, func() bool {
		return len(ps.Logger().Query(LogFilter{Types: []LogEntryType{LogTypePanelMessage}})) == 1 &&
			len(oc.all()) == 1
	}, 3*time.Second, 20*time.Millisecond, "secret receipt must reach panel log and overlay")

	transcript := transcriptDump(t, ps, oc)
	require.NotEmpty(t, transcript)

	// THE invariant: the secret value appears ZERO times anywhere the agent
	// (or the channel) can see.
	assert.Zero(t, strings.Count(transcript, secretValue),
		"secret value must appear zero times in the MCP/channel transcript")

	// While the name + fingerprint receipt does appear.
	assert.Contains(t, transcript, secretName)
	assert.Contains(t, transcript, "****XYZ1")
}

func TestSecretSubmit_NoSink_FailsLoudWithoutValue(t *testing.T) {
	backend := newWrapBackend(t)
	ps := startWrapProxy(t, backend)
	oc, socketPath := startOverlayCapture(t)
	ps.OverlayNotifier().SetEndpoint(socketPath)

	const secretValue = "orphaned-secret-value-9876"
	ack := submitSecret(t, ps.ListenAddr, "ORPHAN_KEY", secretValue)
	assert.Equal(t, false, ack["ok"], "no sink configured must fail loud, not store silently")
	assert.Contains(t, ack["error"], "no secret sink")

	// Failure receipt still leaks nothing.
	require.Eventually(t, func() bool {
		return len(ps.Logger().Query(LogFilter{Types: []LogEntryType{LogTypePanelMessage}})) == 1
	}, 3*time.Second, 20*time.Millisecond)
	transcript := transcriptDump(t, ps, oc)
	assert.Zero(t, strings.Count(transcript, secretValue))
	assert.Contains(t, transcript, "FAILED")
}

func TestSecretSubmit_RejectsInvalidNameAndEmptyValue(t *testing.T) {
	backend := newWrapBackend(t)
	ps := startWrapProxy(t, backend)

	sinkCalled := false
	ps.SetSecretSink(func(_, _ string) error { sinkCalled = true; return nil })

	badName := submitSecret(t, ps.ListenAddr, "not a valid=name", "some-value-12345678")
	assert.Equal(t, false, badName["ok"], "invalid env-style name must be rejected")

	empty := submitSecret(t, ps.ListenAddr, "GOOD_NAME", "")
	assert.Equal(t, false, empty["ok"], "empty value must be rejected")
	assert.Contains(t, empty["error"], "empty secret value")

	assert.False(t, sinkCalled, "rejected submissions must never reach the sink")
}

func TestBroadcastToastOpts_CarriesSecretInputFields(t *testing.T) {
	backend := newWrapBackend(t)
	ps := startWrapProxy(t, backend)
	conn := dialMetrics(t, ps.ListenAddr)

	// Give the connection a beat to register before broadcasting.
	require.Eventually(t, func() bool {
		n, err := ps.BroadcastToastOpts(ToastOptions{
			Type: "info", Title: "Secret needed", Message: "Paste your Figma key",
			Input: "secret", Name: "FIGMA_KEY",
		})
		return err == nil && n == 1
	}, 3*time.Second, 50*time.Millisecond, "toast must reach the connected client")

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
		_, raw, err := conn.ReadMessage()
		require.NoError(t, err)
		var frame map[string]interface{}
		require.NoError(t, json.Unmarshal(raw, &frame))
		if frame["type"] != "toast" {
			continue
		}
		payload, ok := frame["payload"].(map[string]interface{})
		require.True(t, ok)
		if payload["input"] == nil {
			continue // e.g. an unrelated toast
		}
		assert.Equal(t, "secret", payload["input"])
		assert.Equal(t, "FIGMA_KEY", payload["name"])
		assert.Equal(t, "Paste your Figma key", payload["message"])
		return
	}
	t.Fatal("no secret-input toast frame received")
}
