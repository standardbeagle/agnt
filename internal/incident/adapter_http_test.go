package incident

import (
	"testing"

	"github.com/standardbeagle/agnt/internal/proxy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func httpEntry(method, url string, status int) proxy.HTTPLogEntry {
	return proxy.HTTPLogEntry{Method: method, URL: url, StatusCode: status}
}

func TestFromHTTPEntry_StormFingerprint_MergesDistinctURLsSameProxy(t *testing.T) {
	a, okA := FromHTTPEntry(httpEntry("GET", "/api/users", 500), "dev")
	b, okB := FromHTTPEntry(httpEntry("GET", "/api/orders", 503), "dev")
	require.True(t, okA)
	require.True(t, okB)
	assert.Equal(t, a.Fingerprint, b.Fingerprint,
		"any 5xx from the same proxy shares one storm fingerprint")
	assert.Equal(t, "5xx", a.Category)
}

func TestFromHTTPEntry_StormFingerprint_DistinctPerProxy(t *testing.T) {
	a, _ := FromHTTPEntry(httpEntry("GET", "/api/users", 500), "dev")
	b, _ := FromHTTPEntry(httpEntry("GET", "/api/users", 500), "staging")
	assert.NotEqual(t, a.Fingerprint, b.Fingerprint,
		"two proxies' storms must not merge")
}

func TestFromHTTPEntry_StormFingerprint_4xxDistinctFrom5xx(t *testing.T) {
	a, _ := FromHTTPEntry(httpEntry("GET", "/x", 500), "dev")
	b, _ := FromHTTPEntry(httpEntry("GET", "/x", 404), "dev")
	assert.NotEqual(t, a.Fingerprint, b.Fingerprint)
	assert.Equal(t, "5xx", a.Category)
	assert.Equal(t, "4xx", b.Category)
}

func TestFromHTTPEntry_SummaryKeepsURL(t *testing.T) {
	ev, _ := FromHTTPEntry(httpEntry("GET", "/api/users", 500), "dev")
	assert.Contains(t, ev.Summary, "/api/users",
		"summary keeps the human URL even though the fingerprint drops it")
	assert.Equal(t, "/api/users", ev.Ctx.URL)
}
