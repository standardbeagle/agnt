//go:build unix

package daemon

import (
	"net/http"
	"strings"
	"testing"

	"github.com/standardbeagle/agnt/internal/daemonclient"

	"github.com/standardbeagle/agnt/internal/config"
	"github.com/standardbeagle/agnt/internal/protocol"
	"github.com/stretchr/testify/require"
)

// TestPublicPlaneHonorsConfiguredMaxBodyBytes proves the LIVE public-plane body
// cap is the operator-configured value, not the spec §5 default. It configures
// max-body-bytes = 1024 (a quarter of the 4096 default) and asserts a 2000-byte
// body — which WOULD be accepted at the default cap — is rejected 413, while a
// small body under the configured cap is accepted. If the configured limit were
// silently dropped and defaults applied, the 2000-byte body would be a 202 and
// this test would fail.
func TestPublicPlaneHonorsConfiguredMaxBodyBytes(t *testing.T) {
	sockPath := shortSockPath(t)
	d := newBootedDaemonWithConfig(t, DaemonConfig{
		SocketPath:       sockPath,
		PublicListenAddr: "127.0.0.1:0",
		FeedbackLimits: config.FeedbackConfig{
			// Generous rate so the rate limiter never interferes with the body
			// assertion; the point under test is MaxBodyBytes.
			RatePerMinute: 100, Burst: 100,
			MaxBodyBytes:    1024,
			MaxRowsPerShare: 500, RetentionDays: 90,
		},
	})
	addr := d.PublicPlaneAddr()
	require.NotEmpty(t, addr, "public plane must be mounted")

	c := daemonclient.NewClient(daemonclient.WithSocketPath(sockPath))
	require.NoError(t, c.Connect())
	t.Cleanup(func() { _ = c.Close() })
	_, err := c.SessionRegister("sess-a", "/tmp/a.sock", "/proj-a", "test", nil)
	require.NoError(t, err)

	res, err := c.PublishCreate(protocol.PublishCreateRequest{Walkthrough: publishTestWalkthrough(t)})
	require.NoError(t, err)

	// A small body under the configured cap is accepted.
	require.Equal(t, http.StatusAccepted, postFeedback(t, addr, res.Token, `{"message":"ok"}`),
		"a body under the configured cap must be accepted")

	// A 2000-byte body is over the CONFIGURED 1024 cap but UNDER the 4096
	// default. It must 413 — proving the live limiter uses 1024, not 4096.
	big := `{"message":"` + strings.Repeat("x", 2000) + `"}`
	require.Greater(t, len(big), 1024)
	require.Less(t, len(big), 4096)
	require.Equal(t, http.StatusRequestEntityTooLarge, postFeedback(t, addr, res.Token, big),
		"a body over the configured cap (but under the default) must 413 — proves the configured cap is live")
}

// TestPublicPlaneHonorsConfiguredBurst proves the LIVE token-bucket burst is the
// operator-configured value, not the spec §5 default of 5. It configures burst =
// 2; the 3rd rapid POST must 429. At the default burst of 5 all three would be
// accepted, so a silently-dropped config would fail this test.
func TestPublicPlaneHonorsConfiguredBurst(t *testing.T) {
	sockPath := shortSockPath(t)
	d := newBootedDaemonWithConfig(t, DaemonConfig{
		SocketPath:       sockPath,
		PublicListenAddr: "127.0.0.1:0",
		FeedbackLimits: config.FeedbackConfig{
			RatePerMinute: 2, Burst: 2,
			MaxBodyBytes:    4096,
			MaxRowsPerShare: 500, RetentionDays: 90,
		},
	})
	addr := d.PublicPlaneAddr()
	require.NotEmpty(t, addr, "public plane must be mounted")

	c := daemonclient.NewClient(daemonclient.WithSocketPath(sockPath))
	require.NoError(t, c.Connect())
	t.Cleanup(func() { _ = c.Close() })
	_, err := c.SessionRegister("sess-a", "/tmp/a.sock", "/proj-a", "test", nil)
	require.NoError(t, err)

	res, err := c.PublishCreate(protocol.PublishCreateRequest{Walkthrough: publishTestWalkthrough(t)})
	require.NoError(t, err)

	// Burst 2: first two rapid POSTs pass, the third is rejected 429. This would
	// NOT happen at the default burst of 5.
	require.Equal(t, http.StatusAccepted, postFeedback(t, addr, res.Token, `{"message":"1"}`))
	require.Equal(t, http.StatusAccepted, postFeedback(t, addr, res.Token, `{"message":"2"}`))
	require.Equal(t, http.StatusTooManyRequests, postFeedback(t, addr, res.Token, `{"message":"3"}`),
		"the 3rd rapid POST must 429 at configured burst 2 — proves the configured burst is live, not the default 5")
}

// TestPublicPlaneHonorsConfiguredArtifactRate proves the LIVE artifact-GET rate
// cap is the operator-configured value, not the house default of burst 30. It
// configures artifact-burst = 2; the 3rd rapid GET must 429. At the default burst
// all three would be 200, so a silently-dropped config would fail this test —
// this is the §5 "config drives runtime behaviour" proof for the public-plane
// block.
func TestPublicPlaneHonorsConfiguredArtifactRate(t *testing.T) {
	sockPath := shortSockPath(t)
	d := newBootedDaemonWithConfig(t, DaemonConfig{
		SocketPath:       sockPath,
		PublicListenAddr: "127.0.0.1:0",
		PublicPlaneLimits: config.PublicPlaneConfig{
			ArtifactRatePerMinute: 2, ArtifactBurst: 2,
			// Generous outbound so it never interferes (the test share has no
			// upstream anyway).
			OutboundRatePerMinute: 1000, OutboundBurst: 1000,
		},
	})
	addr := d.PublicPlaneAddr()
	require.NotEmpty(t, addr, "public plane must be mounted")

	c := daemonclient.NewClient(daemonclient.WithSocketPath(sockPath))
	require.NoError(t, c.Connect())
	t.Cleanup(func() { _ = c.Close() })
	_, err := c.SessionRegister("sess-a", "/tmp/a.sock", "/proj-a", "test", nil)
	require.NoError(t, err)

	res, err := c.PublishCreate(protocol.PublishCreateRequest{Walkthrough: publishTestWalkthrough(t)})
	require.NoError(t, err)

	// Burst 2: first two rapid artifact GETs pass, the third is 429. This would
	// NOT happen at the house default burst of 30.
	code1, _ := getPublic(t, "http://"+addr+"/s/"+res.Token)
	require.Equal(t, http.StatusOK, code1)
	code2, _ := getPublic(t, "http://"+addr+"/s/"+res.Token)
	require.Equal(t, http.StatusOK, code2)
	code3, body3 := getPublic(t, "http://"+addr+"/s/"+res.Token)
	require.Equal(t, http.StatusTooManyRequests, code3,
		"the 3rd rapid GET must 429 at configured artifact-burst 2 — proves the configured rate is live, not the default 30")
	require.NotContains(t, body3, res.Token, "429 body must not echo the share token")
}
