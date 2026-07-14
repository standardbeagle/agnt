//go:build unix

package daemon

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/standardbeagle/agnt/internal/protocol"
	"github.com/stretchr/testify/require"
)

// publishTestWalkthrough returns a minimal valid published-walkthrough JSON.
func publishTestWalkthrough(t *testing.T) json.RawMessage {
	t.Helper()
	wt := map[string]any{
		"version": "v1",
		"id":      "wt-1",
		"title":   "Demo Walkthrough",
		"steps": []map[string]any{
			{"id": "s1", "title": "Step 1", "body": "hello", "advance": map[string]any{"type": "auto", "ms": 1000}},
		},
	}
	b, err := json.Marshal(wt)
	require.NoError(t, err)
	return b
}

// bootPublishSession boots a daemon + a session-registered client so control-plane
// PUBLISH verbs resolve a project scope.
func bootPublishSession(t *testing.T, projectPath string) (*Daemon, *Client) {
	t.Helper()
	sockPath := shortSockPath(t)
	d := newBootedDaemonWithConfig(t, DaemonConfig{SocketPath: sockPath})
	c := NewClient(WithSocketPath(sockPath))
	require.NoError(t, c.Connect())
	t.Cleanup(func() { _ = c.Close() })
	_, err := c.SessionRegister("sess-"+strings.ReplaceAll(projectPath, "/", "_"), "/tmp/pub.sock", projectPath, "test", nil)
	require.NoError(t, err)
	return d, c
}

// TestPublishRoundtrip pins the full control-plane lifecycle over the wire plus
// the public verify path: create returns the token once, status/list never leak
// it, revoke and rotate kill the old token immediately, and the public
// VerifyToken path serves the immutable revision with no project scope.
func TestPublishRoundtrip(t *testing.T) {
	d, c := bootPublishSession(t, "/proj-a")

	// create
	res, err := c.PublishCreate(protocol.PublishCreateRequest{Walkthrough: publishTestWalkthrough(t)})
	require.NoError(t, err)
	require.NotEmpty(t, res.ID)
	require.NotEmpty(t, res.Token, "token must be returned once at create")
	require.Equal(t, "/s/"+res.Token, res.ShareURL)
	token := res.Token
	id := res.ID

	// public verify path: token alone yields the immutable revision, no scope.
	rev, shareID, ok := d.publishStore.VerifyToken(token)
	require.True(t, ok, "valid token must verify on the public path")
	require.Equal(t, id, shareID)
	require.Equal(t, "wt-1", rev.ID)

	// status: never returns the token, only a hash prefix.
	info, err := c.PublishStatus(id)
	require.NoError(t, err)
	require.Equal(t, id, info.ID)
	require.NotContains(t, mustJSON(t, info), token, "status must not leak the plaintext token")
	require.Len(t, info.TokenHashPrefix, 8)
	require.False(t, info.Revoked)

	// list: project-scoped, no token.
	list, err := c.PublishList()
	require.NoError(t, err)
	require.Len(t, list.Shares, 1)
	require.NotContains(t, mustJSON(t, list), token, "list must not leak the plaintext token")

	// rotate: new token works, old token dead immediately.
	rot, err := c.PublishRotate(id)
	require.NoError(t, err)
	require.NotEmpty(t, rot.Token)
	require.NotEqual(t, token, rot.Token)
	if _, _, ok := d.publishStore.VerifyToken(token); ok {
		t.Fatal("old token still verifies after rotate")
	}
	if _, _, ok := d.publishStore.VerifyToken(rot.Token); !ok {
		t.Fatal("new token does not verify after rotate")
	}

	// revoke: token dies immediately, every public read is gone.
	require.NoError(t, c.PublishRevoke(id))
	if _, _, ok := d.publishStore.VerifyToken(rot.Token); ok {
		t.Fatal("token still verifies after revoke")
	}
	// status still reports it (audit trail), marked revoked.
	info, err = c.PublishStatus(id)
	require.NoError(t, err)
	require.True(t, info.Revoked)
}

// TestPublishRejectsInvalidArtifact pins that create validates via the P2
// validators over the wire.
func TestPublishRejectsInvalidArtifact(t *testing.T) {
	_, c := bootPublishSession(t, "/proj-a")
	_, err := c.PublishCreate(protocol.PublishCreateRequest{Walkthrough: json.RawMessage(`{"version":"v1","id":"x","title":"t","steps":[]}`)})
	require.Error(t, err, "walkthrough with zero steps must be rejected")
}

// TestPublishControlPlaneProjectScoped pins INV-1/INV-10 at the control layer: a
// share created under project A cannot be addressed by a session scoped to
// project B (it is reported not-found, no cross-project leak), and B's list is
// empty.
func TestPublishControlPlaneProjectScoped(t *testing.T) {
	sockPath := shortSockPath(t)
	d := newBootedDaemonWithConfig(t, DaemonConfig{SocketPath: sockPath})
	_ = d

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

	// B cannot see or address A's share.
	listB, err := cb.PublishList()
	require.NoError(t, err)
	require.Len(t, listB.Shares, 0, "project B must not see project A's shares")

	_, err = cb.PublishStatus(res.ID)
	require.Error(t, err, "project B must not address project A's share by id")

	err = cb.PublishRevoke(res.ID)
	require.Error(t, err, "project B must not revoke project A's share")
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return string(b)
}
