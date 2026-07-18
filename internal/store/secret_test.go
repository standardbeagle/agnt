package store

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidSecretName(t *testing.T) {
	for _, ok := range []string{"FIGMA_KEY", "_x", "A1", "lower_ok"} {
		assert.True(t, ValidSecretName(ok), ok)
	}
	for _, bad := range []string{"", "1ABC", "A-B", "A B", "A=B", "$PATH", "名前"} {
		assert.False(t, ValidSecretName(bad), bad)
	}
}

func TestFingerprint(t *testing.T) {
	assert.Equal(t, "XYZ1", Fingerprint("sk-supersecret-XYZ1"))
	assert.Equal(t, "", Fingerprint("short"), "short secrets get no fingerprint")
	assert.Equal(t, "", Fingerprint(""))
	assert.Equal(t, "5678", Fingerprint("12345678"), "8 chars is the minimum for a fingerprint")
}

func TestIsSecretEntry(t *testing.T) {
	assert.False(t, IsSecretEntry(nil))
	assert.False(t, IsSecretEntry(&StoreEntry{}))
	assert.False(t, IsSecretEntry(NewStoreEntry("v", map[string]any{MetaSecret: "true"})), "string true is not the marker")
	assert.False(t, IsSecretEntry(NewStoreEntry("v", map[string]any{MetaSecret: false})))
	assert.True(t, IsSecretEntry(NewStoreEntry("v", map[string]any{MetaSecret: true})))
}

func TestMaskedForRead_SecretEntry(t *testing.T) {
	secret := "sk-supersecret-XYZ1"
	e := NewStoreEntry(secret, map[string]any{MetaSecret: true, MetaFingerprint: Fingerprint(secret)})

	masked := MaskedForRead("FIGMA_KEY", e)
	require.NotSame(t, e, masked, "secret entry must be cloned, not mutated")
	assert.Equal(t, secret, e.Value, "original entry untouched")
	assert.Equal(t, "[secret:FIGMA_KEY ****XYZ1]", masked.Value)
	assert.Equal(t, TypeString, masked.Type)
	assert.Equal(t, e.CreatedAt, masked.CreatedAt)

	// The marshaled read-surface form must not contain the value.
	data, err := json.Marshal(masked)
	require.NoError(t, err)
	assert.Zero(t, strings.Count(string(data), secret), "masked entry JSON must not contain the secret value")
}

func TestMaskedForRead_NonSecretPassthrough(t *testing.T) {
	e := NewStoreEntry("plain", nil)
	assert.Same(t, e, MaskedForRead("k", e), "non-secret entries pass through unchanged")
}

// TestSecretRoundTrip_StoreHoldsValueReadIsMasked drives the store manager:
// the file holds the real value (needed for env injection) while the masked
// read form never exposes it.
func TestSecretRoundTrip_StoreHoldsValueReadIsMasked(t *testing.T) {
	m := NewStoreManager()
	base := t.TempDir()
	secret := "tok-abcdefgh-9999"

	require.NoError(t, m.Set(base, ScopeGlobal, "", "API_TOKEN", secret,
		map[string]any{MetaSecret: true, MetaFingerprint: Fingerprint(secret)}))

	e, err := m.Get(base, ScopeGlobal, "", "API_TOKEN")
	require.NoError(t, err)
	assert.Equal(t, secret, e.Value, "in-process consumers (env injection) read the real value")
	assert.True(t, IsSecretEntry(e))

	masked := MaskedForRead("API_TOKEN", e)
	assert.Equal(t, "[secret:API_TOKEN ****9999]", masked.Value)
}
