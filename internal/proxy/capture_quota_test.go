package proxy

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// Every capture frame wrote a new file under the project's .agnt/audit and
// nothing bounded how many: a page looping captures (or one buggy script)
// filled the disk 16 MiB at a time. A proxy now keeps its newest captures
// within a count and a byte quota, removing the oldest it wrote itself.

func quotaCaptureFiles(t *testing.T, root string) []string {
	t.Helper()
	m, err := filepath.Glob(filepath.Join(root, ".agnt", AuditDirName, "screenshots", "screenshot-quota-*.png"))
	require.NoError(t, err)
	return m
}

func newQuotaProxy(t *testing.T) (*ProxyServer, string) {
	t.Helper()
	root := t.TempDir()
	ps, err := NewProxyServer(ProxyConfig{ID: "quota", TargetURL: "http://localhost:9999", MaxLogSize: 10, Path: root})
	require.NoError(t, err)
	t.Cleanup(ps.StopChaos)
	return ps, root
}

func TestSavePNGBytes_CountQuotaEvictsOldest(t *testing.T) {
	ps, root := newQuotaProxy(t)
	var first, last string
	for i := 0; i < maxCaptureFiles+3; i++ {
		p, err := ps.savePNGBytes(fmt.Sprintf("c%d", i), onePixelPNG)
		require.NoError(t, err)
		if i == 0 {
			first = p
		}
		last = p
	}
	require.Len(t, quotaCaptureFiles(t, root), maxCaptureFiles)
	require.FileExists(t, last)
	require.NoFileExists(t, first, "the oldest capture must be removed past the count quota")
}

func TestSavePNGBytes_ByteQuotaEvictsOldest(t *testing.T) {
	ps, root := newQuotaProxy(t)
	big := bytes.Repeat([]byte{0}, maxCaptureBytes/4+1)
	var paths []string
	for i := 0; i < 5; i++ {
		p, err := ps.savePNGBytes(fmt.Sprintf("b%d", i), big)
		require.NoError(t, err)
		paths = append(paths, p)
	}
	var total int64
	for _, p := range quotaCaptureFiles(t, root) {
		st, err := os.Stat(p)
		require.NoError(t, err)
		total += st.Size()
	}
	require.LessOrEqual(t, total, int64(maxCaptureBytes))
	require.FileExists(t, paths[4])
	require.NoFileExists(t, paths[0])
}

// A capture is the user's artifact: unlike a large exec result it outlives
// the proxy, so teardown keeps it.
func TestCaptures_SurviveTeardown(t *testing.T) {
	ps, _ := newQuotaProxy(t)
	p, err := ps.savePNGBytes("keep", onePixelPNG)
	require.NoError(t, err)
	ps.removeLargeResults()
	require.FileExists(t, p)
}
