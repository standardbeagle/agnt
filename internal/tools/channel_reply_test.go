package tools

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestExtractProxyIDs(t *testing.T) {
	t.Run("empty response", func(t *testing.T) {
		result := map[string]interface{}{}
		ids := extractProxyIDs(result)
		assert.Nil(t, ids)
	})

	t.Run("two proxies", func(t *testing.T) {
		result := map[string]interface{}{
			"proxies": []interface{}{
				map[string]interface{}{"id": "dev", "status": "running"},
				map[string]interface{}{"id": "api", "status": "running"},
			},
		}
		ids := extractProxyIDs(result)
		assert.Equal(t, []string{"dev", "api"}, ids)
	})

	t.Run("skips entries without id", func(t *testing.T) {
		result := map[string]interface{}{
			"proxies": []interface{}{
				map[string]interface{}{"status": "running"},
				map[string]interface{}{"id": "dev"},
			},
		}
		ids := extractProxyIDs(result)
		assert.Equal(t, []string{"dev"}, ids)
	})

	t.Run("count field present", func(t *testing.T) {
		result := map[string]interface{}{
			"count": float64(0),
		}
		ids := extractProxyIDs(result)
		assert.Nil(t, ids)
	})
}

func TestBuildDirFilter(t *testing.T) {
	t.Run("no session falls back to project path", func(t *testing.T) {
		dt := &DaemonTools{}
		filter := dt.scopeFilter(nil)
		assert.Equal(t, "", filter.SessionCode)
		// When no session is set, getProjectPath() falls back to cwd.
		assert.NotEmpty(t, filter.Directory)
	})

	t.Run("uses session code when set", func(t *testing.T) {
		dt := &DaemonTools{}
		dt.SetSessionCode("sess-123")
		filter := dt.scopeFilter(nil)
		assert.Equal(t, "sess-123", filter.SessionCode)
		assert.Equal(t, "", filter.Directory)
	})
}

func TestChannelReplyValidation(t *testing.T) {
	t.Run("empty content returns error", func(t *testing.T) {
		dt := &DaemonTools{}
		handler := dt.makeChannelReplyHandler()
		result, _, _ := handler(nil, nil, ChannelReplyInput{Content: ""})
		assert.NotNil(t, result)
		assert.True(t, result.IsError)
	})

	t.Run("invalid severity returns error", func(t *testing.T) {
		dt := &DaemonTools{}
		handler := dt.makeChannelReplyHandler()
		result, _, _ := handler(nil, nil, ChannelReplyInput{
			Content:  "hello",
			Severity: "critical",
		})
		assert.NotNil(t, result)
		assert.True(t, result.IsError)
	})
}
