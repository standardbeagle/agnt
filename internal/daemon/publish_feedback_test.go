//go:build unix

package daemon

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/standardbeagle/agnt/internal/config"
	"github.com/standardbeagle/agnt/internal/protocol"
	"github.com/stretchr/testify/require"
)

// postFeedback POSTs a feedback body to the mounted public plane and returns the
// HTTP status.
func postFeedback(t *testing.T, addr, token, body string) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, "http://"+addr+"/s/"+token+"/feedback", strings.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	_, _ = io.ReadAll(resp.Body)
	return resp.StatusCode
}

// getPublic GETs a public route and returns status + body.
func getPublic(t *testing.T, url string) (int, string) {
	t.Helper()
	resp, err := http.Get(url)
	require.NoError(t, err)
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// bootPublicPlaneSession boots a daemon with the public listener mounted plus a
// session-registered client for the given project.
func bootPublicPlaneSession(t *testing.T, projectPath string) (*Daemon, *Client) {
	t.Helper()
	sockPath := shortSockPath(t)
	d := newBootedDaemonWithConfig(t, DaemonConfig{SocketPath: sockPath, PublicListenAddr: "127.0.0.1:0"})
	require.NotEmpty(t, d.PublicPlaneAddr(), "public plane must be mounted on a real listener")
	c := NewClient(WithSocketPath(sockPath))
	require.NoError(t, c.Connect())
	t.Cleanup(func() { _ = c.Close() })
	_, err := c.SessionRegister("sess-"+strings.ReplaceAll(projectPath, "/", "_"), "/tmp/pub.sock", projectPath, "test", nil)
	require.NoError(t, err)
	return d, c
}

// TestPublicPlaneServesArtifactAndFeedbackReadable is the P9 end-to-end proof:
// the mounted public handler serves an artifact for a valid token, a viewer's
// feedback POST is persisted, that feedback is readable via the owner-scoped MCP
// surface — and a dev-control path stays unreachable on the public plane.
func TestPublicPlaneServesArtifactAndFeedbackReadable(t *testing.T) {
	d, c := bootPublicPlaneSession(t, "/proj-a")
	addr := d.PublicPlaneAddr()

	res, err := c.PublishCreate(protocol.PublishCreateRequest{Walkthrough: publishTestWalkthrough(t)})
	require.NoError(t, err)
	token := res.Token

	// The public artifact route serves the immutable revision for a valid token.
	code, body := getPublic(t, "http://"+addr+"/s/"+token)
	require.Equal(t, http.StatusOK, code, "valid token must serve the artifact")
	require.Contains(t, body, "<!DOCTYPE html>")

	// A dev-control path is structurally unreachable on the public plane.
	ctrlCode, _ := getPublic(t, "http://"+addr+"/__devtool/ws")
	require.Equal(t, http.StatusForbidden, ctrlCode, "dev-control path must be forbidden on the public plane")

	// An anonymous viewer POSTs feedback; it is accepted + persisted.
	require.Equal(t, http.StatusAccepted, postFeedback(t, addr, token, `{"message":"great walkthrough","rating":5}`))

	// The owner reads it back through the MCP/control surface.
	fb, err := c.PublishFeedback(res.ID, "", 0)
	require.NoError(t, err)
	require.Len(t, fb.Rows, 1)
	require.Contains(t, fb.Rows[0].Body, "great walkthrough")
	require.Equal(t, 1, fb.Total)
	require.Equal(t, int64(0), fb.Dropped)

	// Redaction: the token never appears in the feedback read output.
	require.NotContains(t, mustJSON(t, fb), token, "feedback read must not leak the token")
}

// TestFeedbackReadOwnerScoped pins that project B cannot read project A's
// feedback — the SAME ownership gate as status/revoke/rotate (a foreign share is
// not-found, no cross-project leak).
func TestFeedbackReadOwnerScoped(t *testing.T) {
	sockPath := shortSockPath(t)
	d := newBootedDaemonWithConfig(t, DaemonConfig{SocketPath: sockPath, PublicListenAddr: "127.0.0.1:0"})
	addr := d.PublicPlaneAddr()

	ca := NewClient(WithSocketPath(sockPath))
	require.NoError(t, ca.Connect())
	t.Cleanup(func() { _ = ca.Close() })
	_, err := ca.SessionRegister("sess-a", "/tmp/a.sock", "/proj-a", "test", nil)
	require.NoError(t, err)

	cb := NewClient(WithSocketPath(sockPath))
	require.NoError(t, cb.Connect())
	t.Cleanup(func() { _ = cb.Close() })
	_, err = cb.SessionRegister("sess-b", "/tmp/b.sock", "/proj-b", "test", nil)
	require.NoError(t, err)

	res, err := ca.PublishCreate(protocol.PublishCreateRequest{Walkthrough: publishTestWalkthrough(t)})
	require.NoError(t, err)
	require.Equal(t, http.StatusAccepted, postFeedback(t, addr, res.Token, `{"message":"hi from a viewer"}`))

	// A (owner) reads it.
	fbA, err := ca.PublishFeedback(res.ID, "", 0)
	require.NoError(t, err)
	require.Len(t, fbA.Rows, 1)

	// B cannot address A's share at all — not-found, never A's feedback.
	_, err = cb.PublishFeedback(res.ID, "", 0)
	require.Error(t, err, "project B must not read project A's feedback")
}

// TestFeedbackArrivalEventScopedToOwningProject pins the arrival event's project
// scoping: a subscriber on project A receives the counts-only arrival event when
// A's share gets feedback, a subscriber on project B does NOT, and the event
// carries no token/body.
func TestFeedbackArrivalEventScopedToOwningProject(t *testing.T) {
	d, c := bootPublicPlaneSession(t, "/proj-a")
	addr := d.PublicPlaneAddr()

	subA := d.feedbackHub.Subscribe("/proj-a")
	require.NotNil(t, subA)
	t.Cleanup(func() { d.feedbackHub.Unsubscribe(subA) })
	subB := d.feedbackHub.Subscribe("/proj-b")
	require.NotNil(t, subB)
	t.Cleanup(func() { d.feedbackHub.Unsubscribe(subB) })

	res, err := c.PublishCreate(protocol.PublishCreateRequest{Walkthrough: publishTestWalkthrough(t)})
	require.NoError(t, err)
	token := res.Token

	require.Equal(t, http.StatusAccepted, postFeedback(t, addr, token, `{"message":"scoped ping"}`))

	select {
	case ev := <-subA.C:
		require.Equal(t, res.ID, ev.ShareID)
		require.Equal(t, 1, ev.Total)
		require.NotEmpty(t, ev.Remediation)
		// No token / no body on the event.
		evJSON := mustJSON(t, ev)
		require.NotContains(t, evJSON, token, "arrival event must not carry the token")
		require.NotContains(t, evJSON, "scoped ping", "arrival event must not carry the feedback body")
	case <-time.After(2 * time.Second):
		t.Fatal("project A subscriber did not receive the arrival event")
	}

	select {
	case <-subB.C:
		t.Fatal("project B subscriber must NOT receive project A's feedback event")
	case <-time.After(200 * time.Millisecond):
		// expected: no delivery to the wrong project.
	}
}

// TestFeedbackDroppedCountObservable pins that rate-limit-dropped posts are
// observable via the control read's Dropped count and surface on the arrival
// event, without crashing the public plane (429, not a crash).
func TestFeedbackDroppedCountObservable(t *testing.T) {
	sockPath := shortSockPath(t)
	// Rate 1/min, burst 1: the 2nd+ POST in the window is dropped (429).
	d := newBootedDaemonWithConfig(t, DaemonConfig{
		SocketPath:       sockPath,
		PublicListenAddr: "127.0.0.1:0",
		FeedbackLimits: config.FeedbackConfig{
			RatePerMinute: 1, Burst: 1, MaxBodyBytes: 4096, MaxRowsPerShare: 500, RetentionDays: 90,
		},
	})
	addr := d.PublicPlaneAddr()

	c := NewClient(WithSocketPath(sockPath))
	require.NoError(t, c.Connect())
	t.Cleanup(func() { _ = c.Close() })
	_, err := c.SessionRegister("sess-a", "/tmp/a.sock", "/proj-a", "test", nil)
	require.NoError(t, err)

	res, err := c.PublishCreate(protocol.PublishCreateRequest{Walkthrough: publishTestWalkthrough(t)})
	require.NoError(t, err)

	require.Equal(t, http.StatusAccepted, postFeedback(t, addr, res.Token, `{"message":"first"}`))
	require.Equal(t, http.StatusTooManyRequests, postFeedback(t, addr, res.Token, `{"message":"second"}`),
		"the over-budget post must 429, not crash")

	fb, err := c.PublishFeedback(res.ID, "", 0)
	require.NoError(t, err)
	require.Equal(t, 1, fb.Total, "only the accepted post is stored")
	require.GreaterOrEqual(t, fb.Dropped, int64(1), "the dropped post must be observable")
}

// TestFeedbackReadRedactionNoToken pins that create returns the token once and
// the feedback read never contains it, even as a substring in a row body.
func TestFeedbackReadRedactionNoToken(t *testing.T) {
	d, c := bootPublicPlaneSession(t, "/proj-a")
	addr := d.PublicPlaneAddr()

	res, err := c.PublishCreate(protocol.PublishCreateRequest{Walkthrough: publishTestWalkthrough(t)})
	require.NoError(t, err)
	token := res.Token
	require.NotEmpty(t, token, "create returns the token once")

	require.Equal(t, http.StatusAccepted, postFeedback(t, addr, token, `{"message":"nothing secret"}`))

	fb, err := c.PublishFeedback(res.ID, "", 0)
	require.NoError(t, err)
	raw, err := json.Marshal(fb)
	require.NoError(t, err)
	require.NotContains(t, string(raw), token, "feedback read must never contain the token")
}
