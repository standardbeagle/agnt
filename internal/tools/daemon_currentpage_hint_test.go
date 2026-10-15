package tools

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/standardbeagle/agnt/internal/daemonclient"
	"github.com/stretchr/testify/require"
)

// currentpage with no proxy_id answers with the proxies to pick from. Those
// candidates are the caller's project's, not every project's: listing
// another project's proxy ids leaks them across projects and invites the
// agent to read a page it does not own.
func TestCurrentPage_MissingProxyIDHintIsProjectScoped(t *testing.T) {
	dt, sock := attachTestDaemon(t)
	project, err := os.Getwd()
	require.NoError(t, err)
	other, err := os.MkdirTemp("", "agnt-cp-other")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(other) })

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	t.Cleanup(backend.Close)

	starter := daemonclient.NewClient(daemonclient.WithSocketPath(sock))
	require.NoError(t, starter.Connect())
	t.Cleanup(func() { _ = starter.Close() })
	for id, path := range map[string]string{"cp-mine": project, "cp-theirs": other} {
		_, err := starter.ProxyStart(id, backend.URL, 0, 0, path)
		require.NoError(t, err, "start %s", id)
		t.Cleanup(func() { _ = starter.ProxyStop(id) })
	}

	res, _, err := dt.makeCurrentPageHandler()(context.Background(), nil, CurrentPageInput{})
	require.NoError(t, err)
	require.True(t, res.IsError)
	text := resultText(res)
	require.Contains(t, text, "cp-mine")
	require.False(t, strings.Contains(text, "cp-theirs"), "hint lists another project's proxy: %s", text)
}
