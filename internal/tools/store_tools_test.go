package tools

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The daemon answers STORE GET with the entry object itself, and STORE LIST /
// GET-ALL with no count. The tool used to look for an "entry" wrapper, so every
// get of a stored key reported "key not found", and list/get_all reported a
// count of 0 beside a non-empty result.
func TestStoreTool_RoundTripsThroughDaemon(t *testing.T) {
	dt, _ := attachTestDaemon(t)
	handler := dt.makeStoreHandler()
	ctx := context.Background()

	res, _, err := handler(ctx, nil, StoreInput{Action: "set", Scope: "global", Key: "greeting", Value: "hello"})
	require.NoError(t, err)
	require.Nil(t, res, "set failed: %v", res)

	res, out, err := handler(ctx, nil, StoreInput{Action: "get", Scope: "global", Key: "greeting"})
	require.NoError(t, err)
	require.Nil(t, res, "get of a stored key must succeed, got %v", res)
	require.NotNil(t, out.Entry)
	assert.Equal(t, "hello", out.Entry.Value)
	assert.True(t, out.Success)

	res, out, err = handler(ctx, nil, StoreInput{Action: "list", Scope: "global"})
	require.NoError(t, err)
	require.Nil(t, res)
	assert.Equal(t, []string{"greeting"}, out.Keys)
	assert.Equal(t, 1, out.Count)

	res, out, err = handler(ctx, nil, StoreInput{Action: "get_all", Scope: "global"})
	require.NoError(t, err)
	require.Nil(t, res)
	require.Contains(t, out.Entries, "greeting")
	assert.Equal(t, 1, out.Count)
}

func TestStoreGetEntry_MissingEntryIsNil(t *testing.T) {
	assert.Nil(t, storeGetEntry(map[string]interface{}{}))
	got := storeGetEntry(map[string]interface{}{"entry": map[string]interface{}{"value": "v", "type": "string"}})
	require.NotNil(t, got)
	assert.Equal(t, "v", got.Value)
}
