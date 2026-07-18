package daemon

import (
	"testing"

	"github.com/standardbeagle/agnt/internal/proxy"
	"github.com/standardbeagle/agnt/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// secretTestDaemon builds the minimal daemon shape these units need:
// wireSecretSink and injectSecretEnv touch only d.storem.
func secretTestDaemon() *Daemon {
	return &Daemon{storem: store.NewStoreManager()}
}

func TestWireSecretSink_DeliversToProjectStoreMarkedSecret(t *testing.T) {
	d := secretTestDaemon()
	projectPath := t.TempDir()

	ps, err := proxy.NewProxyServer(proxy.ProxyConfig{
		ID:        "secret-test",
		TargetURL: "http://127.0.0.1:1",
		Path:      projectPath,
	})
	require.NoError(t, err)
	t.Cleanup(ps.PageTracker().Stop)

	d.wireSecretSink(ps)

	const secret = "sk-figma-secret-ABCD"
	require.NoError(t, ps.DeliverSecret("FIGMA_KEY", secret))

	e, err := d.storem.Get(projectPath, store.ScopeGlobal, "", "FIGMA_KEY")
	require.NoError(t, err)
	assert.Equal(t, secret, e.Value, "store holds the real value for env injection")
	assert.True(t, store.IsSecretEntry(e), "entry must carry the secret marker")
	assert.Equal(t, "ABCD", e.Metadata[store.MetaFingerprint])

	// The read-surface form (what STORE GET returns) is masked.
	assert.Equal(t, "[secret:FIGMA_KEY ****ABCD]", store.MaskedForRead("FIGMA_KEY", e).Value)
}

func TestWireSecretSink_NoPath_NoSink(t *testing.T) {
	d := secretTestDaemon()
	ps, err := proxy.NewProxyServer(proxy.ProxyConfig{
		ID:        "pathless",
		TargetURL: "http://127.0.0.1:1",
	})
	require.NoError(t, err)
	t.Cleanup(ps.PageTracker().Stop)

	d.wireSecretSink(ps)
	err = ps.DeliverSecret("KEY_NAME", "value-12345678")
	require.Error(t, err, "a pathless proxy has no project store; delivery must fail loud")
	assert.Contains(t, err.Error(), "no secret sink")
}

func TestInjectSecretEnv(t *testing.T) {
	d := secretTestDaemon()
	projectPath := t.TempDir()

	// Two secrets, one plain entry, one secret with a non-env-safe name,
	// one secret with a non-string value.
	require.NoError(t, d.storem.Set(projectPath, store.ScopeGlobal, "", "B_KEY", "b-value-1234",
		map[string]any{store.MetaSecret: true}))
	require.NoError(t, d.storem.Set(projectPath, store.ScopeGlobal, "", "A_KEY", "a-value-1234",
		map[string]any{store.MetaSecret: true}))
	require.NoError(t, d.storem.Set(projectPath, store.ScopeGlobal, "", "plain", "not-a-secret", nil))
	require.NoError(t, d.storem.Set(projectPath, store.ScopeGlobal, "", "bad-name", "x-value-1234",
		map[string]any{store.MetaSecret: true}))
	require.NoError(t, d.storem.Set(projectPath, store.ScopeGlobal, "", "NUM_KEY", 42,
		map[string]any{store.MetaSecret: true}))

	env := d.injectSecretEnv(projectPath, []string{"EXISTING=1"})
	assert.Equal(t, []string{"EXISTING=1", "A_KEY=a-value-1234", "B_KEY=b-value-1234"}, env,
		"only string-valued, env-named secrets are injected, sorted, after existing env")

	// No project path or no entries: env passes through untouched.
	assert.Equal(t, []string{"X=1"}, d.injectSecretEnv("", []string{"X=1"}))
	assert.Equal(t, []string{"X=1"}, d.injectSecretEnv(t.TempDir(), []string{"X=1"}))
}
