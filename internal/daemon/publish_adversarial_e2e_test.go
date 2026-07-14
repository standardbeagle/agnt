//go:build unix

package daemon

// publish_adversarial_e2e_test.go is the P10 gate's daemon tier: it proves the
// adversarial negatives against the REAL, fully-booted daemon public listener —
// not an isolated hand-wired handler. The share is minted through the real MCP
// PublishCreate path and the probes hit the production-mounted listener
// (PublicListenAddr), so this exercises the actual wiring a public viewer would
// reach. It complements the proxy-tier capstone (publish_e2e_test.go), which
// drives an ephemeral PublicHandler directly.

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"

	"github.com/standardbeagle/agnt/internal/protocol"
	"github.com/standardbeagle/agnt/internal/proxy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// rawDaemonGet writes a LITERAL request line to the mounted listener so a
// non-canonical path reaches the server's traversal defence unmolested by any
// client-side URL cleaning. Returns the status code.
func rawDaemonGet(t *testing.T, addr, rawPath string) int {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	require.NoError(t, err)
	defer conn.Close()
	fmt.Fprintf(conn, "GET %s HTTP/1.1\r\nHost: e2e\r\nConnection: close\r\n\r\n", rawPath)
	data, err := io.ReadAll(conn)
	require.NoError(t, err)
	line := string(data)
	if i := strings.IndexByte(line, '\n'); i >= 0 {
		line = line[:i]
	}
	fields := strings.Fields(line)
	require.GreaterOrEqual(t, len(fields), 2, "status line: %q", line)
	var code int
	fmt.Sscanf(fields[1], "%d", &code)
	return code
}

// TestE2EDaemonPublicPlaneAdversarial runs the security-core probes against the
// REAL production-mounted daemon listener: the dev-control surface is
// unreachable, path traversal serves no file, an unknown token is an
// indistinguishable 404, and revoke/rotate kill every route on the live listener.
func TestE2EDaemonPublicPlaneAdversarial(t *testing.T) {
	d, c := bootPublicPlaneSession(t, "/proj-a")
	addr := d.PublicPlaneAddr()
	require.NotEmpty(t, addr)

	res, err := c.PublishCreate(protocol.PublishCreateRequest{Walkthrough: publishTestWalkthrough(t)})
	require.NoError(t, err)
	token := res.Token

	// Sanity: the live listener serves the artifact for the real token.
	code, body := getPublic(t, "http://"+addr+"/s/"+token)
	require.Equal(t, http.StatusOK, code)
	require.Contains(t, body, "<!DOCTYPE html>")

	t.Run("dev_control_surface_unreachable_on_production_listener", func(t *testing.T) {
		for _, probe := range []string{
			"/__devtool_metrics",
			"/__devtool/exec",
			"/__devtool/ws",
			"/__devtool/inject.js",
			"/__devtool/inject.deadbeef.js", // forged hash must NOT serve the dev bundle
			"/__devtool_axe",
		} {
			pcode, pbody := getPublic(t, "http://"+addr+probe)
			assert.Truef(t, pcode == http.StatusForbidden || pcode == http.StatusNotFound,
				"%s: got %d, want 403/404", probe, pcode)
			assert.NotContainsf(t, pbody, "__devtool", "%s leaked a dev marker", probe)
		}
	})

	t.Run("public_asset_serves_only_the_public_bundle", func(t *testing.T) {
		acode, abody := getPublic(t, "http://"+addr+proxy.PublicInstrumentationAssetPath())
		require.Equal(t, http.StatusOK, acode)
		assert.Contains(t, abody, "__walkthroughViewer")
		assert.NotContains(t, abody, "__devtool", "public bundle must carry no dev control surface")
	})

	t.Run("path_traversal_serves_no_file", func(t *testing.T) {
		for _, tr := range []string{
			"/s/" + token + "/../daemon.go",
			"/s/" + token + "/%2e%2e/daemon.go",
			"//etc/passwd",
		} {
			assert.Equalf(t, http.StatusNotFound, rawDaemonGet(t, addr, tr), "traversal %q must 404", tr)
		}
	})

	t.Run("unknown_token_no_oracle", func(t *testing.T) {
		u1, b1 := getPublic(t, "http://"+addr+"/s/unknown-token-one")
		u2, b2 := getPublic(t, "http://"+addr+"/s/unknown-token-two")
		require.Equal(t, http.StatusNotFound, u1)
		require.Equal(t, http.StatusNotFound, u2)
		assert.Equal(t, b1, b2, "unknown-token 404 bodies must be identical (no oracle)")
	})

	t.Run("rotate_then_revoke_kill_routes_on_live_listener", func(t *testing.T) {
		// Rotate: old token dies on the live listener, new token serves.
		rot, err := c.PublishRotate(res.ID)
		require.NoError(t, err)
		oldCode, _ := getPublic(t, "http://"+addr+"/s/"+token)
		assert.Equal(t, http.StatusNotFound, oldCode, "rotated-out token must 404 on the live listener")
		newCode, _ := getPublic(t, "http://"+addr+"/s/"+rot.Token)
		assert.Equal(t, http.StatusOK, newCode, "rotated-in token must serve")

		// Revoke: the new token — and every route — dies.
		require.NoError(t, c.PublishRevoke(res.ID))
		for _, sub := range []string{"", "/variants.json", "/walkthrough.json"} {
			rcode, _ := getPublic(t, "http://"+addr+"/s/"+rot.Token+sub)
			assert.Equalf(t, http.StatusNotFound, rcode, "revoked /s/{token}%s must 404", sub)
		}
		assert.Equal(t, http.StatusNotFound,
			postFeedback(t, addr, rot.Token, `{"message":"after revoke"}`),
			"feedback after revoke must 404")
	})
}
