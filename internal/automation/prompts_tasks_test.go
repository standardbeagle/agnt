package automation

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPromptRegistryDefaults asserts every TaskType constant resolves to a
// non-empty prompt and that an unknown type returns "" — the latter is
// load-bearing because Process treats "" as "unknown task type".
func TestPromptRegistryDefaults(t *testing.T) {
	r := DefaultPromptRegistry()

	for _, tt := range []TaskType{
		TaskTypeAuditProcess,
		TaskTypeSummarize,
		TaskTypePrioritize,
		TaskTypeGenerateFixes,
		TaskTypeCorrelate,
	} {
		assert.NotEmpty(t, r.Get(tt), "prompt for %q must be non-empty", tt)
	}

	assert.Empty(t, r.Get("does-not-exist"), "unknown type returns empty (Process relies on this)")
	assert.Empty(t, r.Get(""), "empty type returns empty")
}

// TestPromptRegistrySetOverrides asserts Set overrides a prompt and adds new
// task types, leaving others intact.
func TestPromptRegistrySetOverrides(t *testing.T) {
	r := DefaultPromptRegistry()
	original := r.Get(TaskTypeSummarize)
	require.NotEmpty(t, original)

	r.Set(TaskTypeSummarize, "custom summarize prompt")
	assert.Equal(t, "custom summarize prompt", r.Get(TaskTypeSummarize), "override applied")
	assert.NotEqual(t, original, r.Get(TaskTypeSummarize))

	r.Set("brand-new", "new prompt")
	assert.Equal(t, "new prompt", r.Get("brand-new"), "new type registered")

	assert.NotEmpty(t, r.Get(TaskTypeAuditProcess), "unrelated prompt untouched")
}

// TestPromptRegistryConcurrent drives concurrent Get/Set under -race to prove
// the RWMutex guards the map.
func TestPromptRegistryConcurrent(t *testing.T) {
	r := DefaultPromptRegistry()

	const goroutines = 16
	const iters = 200
	var wg sync.WaitGroup
	wg.Add(goroutines * 2)

	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < iters; j++ {
				_ = r.Get(TaskTypeSummarize)
				_ = r.Get(TaskTypeAuditProcess)
			}
		}()
		go func(id int) {
			defer wg.Done()
			tt := TaskType("worker")
			for j := 0; j < iters; j++ {
				r.Set(tt, "v")
			}
		}(i)
	}
	wg.Wait()

	assert.Equal(t, "v", r.Get("worker"), "final write observable")
	assert.NotEmpty(t, r.Get(TaskTypeSummarize), "default prompt survived concurrent access")
}

// TestNewAuditProcessTask asserts the audit task constructor populates Type,
// Input, and Context.
func TestNewAuditProcessTask(t *testing.T) {
	raw := map[string]interface{}{"issues": 3}
	task := NewAuditProcessTask("accessibility", raw, "http://x.test", "Home")

	assert.Equal(t, TaskTypeAuditProcess, task.Type)

	in, ok := task.Input.(AuditProcessInput)
	require.True(t, ok, "input typed as AuditProcessInput")
	assert.Equal(t, "accessibility", in.AuditType)
	assert.Equal(t, "http://x.test", in.PageURL)
	assert.Equal(t, "Home", in.PageTitle)
	assert.Equal(t, raw, in.RawData)

	require.NotNil(t, task.Context)
	assert.Equal(t, "http://x.test", task.Context["page_url"])
	assert.Equal(t, "Home", task.Context["page_title"])
}

// TestNewSummarizeTask asserts the summarize constructor wires Type, Input, and
// Context through.
func TestNewSummarizeTask(t *testing.T) {
	ctx := map[string]interface{}{"page_url": "http://y.test"}
	task := NewSummarizeTask("some content", ctx)

	assert.Equal(t, TaskTypeSummarize, task.Type)
	assert.Equal(t, "some content", task.Input)
	require.NotNil(t, task.Context)
	assert.Equal(t, "http://y.test", task.Context["page_url"])
	assert.Len(t, task.Context, 1)
}

// TestNewPrioritizeTask asserts the prioritize constructor wires Type, Input,
// and Context through.
func TestNewPrioritizeTask(t *testing.T) {
	items := []interface{}{"a", "b", "c"}
	ctx := map[string]interface{}{"source": "audit"}
	task := NewPrioritizeTask(items, ctx)

	assert.Equal(t, TaskTypePrioritize, task.Type)
	gotItems, ok := task.Input.([]interface{})
	require.True(t, ok)
	assert.Len(t, gotItems, 3)
	assert.Equal(t, items, gotItems)
	require.NotNil(t, task.Context)
	assert.Equal(t, "audit", task.Context["source"])
}
