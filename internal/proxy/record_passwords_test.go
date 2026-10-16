package proxy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const recordPasswordsFlag = "window.__devtool_record_passwords=true;"

// The record-passwords opt-in reaches the page through the same inline config
// script as the auth-breakout rules: on the chrome shell and on the content
// frame, and only while no public URL is bound.
func TestRecordPasswords_FlagDeliveredInPageConfig(t *testing.T) {
	serve := func(ps *ProxyServer, req *http.Request) string {
		t.Helper()
		resp := htmlResp(sampleDoc, req)
		require.NoError(t, ps.modifyResponse(resp))
		return readBody(t, resp)
	}
	topLevel := func() *http.Request {
		req := httptest.NewRequest(http.MethodGet, "/login", nil)
		req.Header.Set("Sec-Fetch-Dest", "document")
		return req
	}
	contentFrame := func() *http.Request {
		return httptest.NewRequest(http.MethodGet, "/login?"+frameMarkerParam+"="+frameIDForPath("/login"), nil)
	}

	off := &ProxyServer{ListenAddr: ":8080", ID: "px"}
	assert.NotContains(t, serve(off, topLevel()), "__devtool_record_passwords", "default: the shell carries no opt-in")
	assert.NotContains(t, serve(off, contentFrame()), "__devtool_record_passwords", "default: the content frame carries no opt-in")

	on := &ProxyServer{ListenAddr: ":8080", ID: "px", recordPasswords: true}
	assert.Contains(t, serve(on, topLevel()), recordPasswordsFlag)
	assert.Contains(t, serve(on, contentFrame()), recordPasswordsFlag)

	on.SetPublicURL("https://dev.example.com")
	assert.NotContains(t, serve(on, topLevel()), "__devtool_record_passwords", "a bound public URL turns the page flag off")
	assert.NotContains(t, serve(on, contentFrame()), "__devtool_record_passwords", "a bound public URL turns the page flag off")
}

// passwordTelemetry is what the shipped scripts send for a password field
// with record-passwords on: an input value and a keydown, both marked, and a
// marked value-attribute change.
func passwordTelemetry() []*wsMessage {
	return []*wsMessage{
		{Type: "interactions", Data: map[string]interface{}{"events": []interface{}{
			map[string]interface{}{"event_type": "input", "value": "hunter2", "password": true,
				"target": map[string]interface{}{"tag": "input", "id": "pw"}},
			map[string]interface{}{"event_type": "keydown", "password": true,
				"key":    map[string]interface{}{"key": "Enter", "code": "Enter"},
				"target": map[string]interface{}{"tag": "input", "id": "pw"}},
			// Identified by the recorded type attribute alone.
			map[string]interface{}{"event_type": "input", "value": "hunter3",
				"target": map[string]interface{}{"tag": "input", "attributes": map[string]interface{}{"type": "Password"}}},
			// An ordinary field's value is kept whoever sends it.
			map[string]interface{}{"event_type": "input", "value": "alice@example.test",
				"target": map[string]interface{}{"tag": "input", "id": "email"}},
		}}},
		{Type: "mutations", Data: map[string]interface{}{"events": []interface{}{
			map[string]interface{}{"mutation_type": "attributes",
				"target":    map[string]interface{}{"tag": "input", "id": "pw"},
				"attribute": map[string]interface{}{"name": "value", "old_value": "hunter1", "new_value": "hunter2", "password": true}},
			// An added subtree whose markup still holds a password value.
			map[string]interface{}{"mutation_type": "added",
				"target": map[string]interface{}{"tag": "main"},
				"added": []interface{}{map[string]interface{}{"tag": "form", "password": true,
					"html": `<form><input type="password" value="hunter4"></form>`}}},
		}}},
	}
}

func newRecordPasswordsProxy(t *testing.T, optIn bool) *ProxyServer {
	t.Helper()
	ps, err := NewProxyServer(ProxyConfig{
		ID: "rp", TargetURL: "http://localhost:3000", ListenPort: 8124,
		MaxLogSize: 100, Path: t.TempDir(), RecordPasswords: optIn,
	})
	require.NoError(t, err)
	t.Cleanup(func() { ps.pageTracker.Stop(); ps.StopChaos() })
	return ps
}

// recorded returns the interactions and mutations the proxy kept, and the
// whole traffic log as JSON.
func recorded(t *testing.T, ps *ProxyServer) ([]*InteractionEvent, []*MutationEvent, string) {
	t.Helper()
	var ins []*InteractionEvent
	var muts []*MutationEvent
	entries := ps.Logger().Query(LogFilter{Limit: 1000})
	for _, e := range entries {
		switch e.Type {
		case LogTypeInteraction:
			ins = append(ins, e.Interaction)
		case LogTypeMutation:
			muts = append(muts, e.Mutation)
		}
	}
	raw, err := json.Marshal(entries)
	require.NoError(t, err)
	require.Len(t, ins, 4, "premise: every interaction is recorded")
	require.Len(t, muts, 2, "premise: the mutations are recorded")
	return ins, muts, string(raw)
}

func TestRecordPasswords_KeptOnlyForTrustedConnectionWithOptIn(t *testing.T) {
	t.Run("trusted with opt-in: recorded", func(t *testing.T) {
		ps := newRecordPasswordsProxy(t, true)
		c, _ := newGateConn(t, ps, true)
		for _, msg := range passwordTelemetry() {
			dispatch(c, msg)
		}
		ins, muts, _ := recorded(t, ps)
		assert.Equal(t, "hunter2", ins[0].Value)
		assert.True(t, ins[0].Password)
		assert.Equal(t, "Enter", ins[1].Key.Key)
		assert.Equal(t, "hunter3", ins[2].Value)
		assert.Equal(t, "alice@example.test", ins[3].Value)
		assert.Equal(t, "hunter1", muts[0].Attribute.OldValue)
		assert.Equal(t, "hunter2", muts[0].Attribute.NewValue)
		assert.Contains(t, muts[1].Added[0].HTML, "hunter4")
	})

	redacted := func(t *testing.T, ps *ProxyServer, trusted bool) {
		t.Helper()
		c, _ := newGateConn(t, ps, trusted)
		for _, msg := range passwordTelemetry() {
			dispatch(c, msg)
		}
		ins, muts, raw := recorded(t, ps)
		assert.NotContains(t, raw, "hunter", "no password value may reach the traffic log")
		assert.Equal(t, redactedPasswordValue, ins[0].Value)
		assert.True(t, ins[0].Password, "the redacted event keeps its marker")
		assert.Equal(t, &KeyboardInfo{Masked: true}, ins[1].Key)
		assert.Equal(t, redactedPasswordValue, ins[2].Value)
		assert.Equal(t, "alice@example.test", ins[3].Value, "a non-password value is still recorded")
		assert.Equal(t, redactedPasswordValue, muts[0].Attribute.OldValue)
		assert.Equal(t, redactedPasswordValue, muts[0].Attribute.NewValue)
		assert.Equal(t, redactedPasswordHTML, muts[1].Added[0].HTML)

	}

	t.Run("untrusted with opt-in: redacted", func(t *testing.T) {
		redacted(t, newRecordPasswordsProxy(t, true), false)
	})
	t.Run("trusted without opt-in: redacted", func(t *testing.T) {
		redacted(t, newRecordPasswordsProxy(t, false), true)
	})
	t.Run("trusted with opt-in while a public URL is bound: redacted", func(t *testing.T) {
		ps := newRecordPasswordsProxy(t, true)
		ps.SetPublicURL("https://dev.example.com")
		redacted(t, ps, true)
	})
}

func TestRecordPasswords_FlagIsStrictlyTheOptIn(t *testing.T) {
	ps := &ProxyServer{}
	assert.Empty(t, ps.recordPasswordsJS())
	ps.recordPasswords = true
	assert.True(t, strings.HasPrefix(ps.recordPasswordsJS(), "window.__devtool_record_passwords=true"))
}
