package automation

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRecordResultAggregates verifies recordResult folds counts, tokens, cost,
// and duration into stats, and that Stats derives AverageDuration. Regression
// guard for the previously-dead TotalTokens/TotalCostUSD/AverageDuration fields.
func TestRecordResultAggregates(t *testing.T) {
	p, err := New(DefaultConfig())
	require.NoError(t, err)

	p.recordResult(&Result{Tokens: 100, Cost: 0.01, Duration: 2 * time.Second}, true)
	p.recordResult(&Result{Tokens: 50, Cost: 0.02, Duration: 4 * time.Second}, false)

	s := p.Stats()
	assert.Equal(t, int64(2), s.TasksProcessed, "both tasks counted")
	assert.Equal(t, int64(1), s.TasksSucceeded, "one success")
	assert.Equal(t, int64(1), s.TasksFailed, "one failure")
	assert.Equal(t, int64(150), s.TotalTokens, "tokens summed (was always zero)")
	assert.InDelta(t, 0.03, s.TotalCostUSD, 1e-9, "cost summed (was always zero)")
	assert.Equal(t, 3*time.Second, s.AverageDuration, "avg of 2s and 4s (was always zero)")
}

// TestStatsZeroBeforeAnyTask asserts AverageDuration stays zero with no
// processed tasks (no divide-by-zero).
func TestStatsZeroBeforeAnyTask(t *testing.T) {
	p, err := New(DefaultConfig())
	require.NoError(t, err)
	s := p.Stats()
	assert.Zero(t, s.TasksProcessed)
	assert.Zero(t, s.AverageDuration, "no divide-by-zero on empty processor")
}

// TestStatsNoRace drives concurrent recordResult writers against Stats readers.
// Regression guard for the prior atomic-write vs mutex-read data race. Run with
// -race to catch a regression.
func TestStatsNoRace(t *testing.T) {
	p, err := New(DefaultConfig())
	require.NoError(t, err)

	const writers = 8
	const perWriter = 200
	var wg sync.WaitGroup
	wg.Add(writers + 2)

	for i := 0; i < writers; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < perWriter; j++ {
				p.recordResult(&Result{Tokens: 1, Cost: 0.001, Duration: time.Millisecond}, j%2 == 0)
			}
		}()
	}
	for i := 0; i < 2; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < perWriter; j++ {
				_ = p.Stats()
			}
		}()
	}
	wg.Wait()

	s := p.Stats()
	assert.Equal(t, int64(writers*perWriter), s.TasksProcessed, "all writes accounted")
	assert.Equal(t, s.TasksSucceeded+s.TasksFailed, s.TasksProcessed, "success+fail == processed")
	assert.Equal(t, int64(writers*perWriter), s.TotalTokens, "no lost token increments")
}

// TestBuildUserPrompt covers the pure prompt builder: input is marshalled,
// the Context block appears only when non-empty.
func TestBuildUserPrompt(t *testing.T) {
	p, err := New(DefaultConfig())
	require.NoError(t, err)

	noCtx, err := p.buildUserPrompt(Task{Input: map[string]string{"k": "v"}})
	require.NoError(t, err)
	assert.Contains(t, noCtx, "Process the following data:")
	assert.Contains(t, noCtx, `"k": "v"`)
	assert.NotContains(t, noCtx, "Context:", "no context block when Context is empty")

	withCtx, err := p.buildUserPrompt(Task{
		Input:   map[string]string{"k": "v"},
		Context: map[string]interface{}{"page_url": "http://x"},
	})
	require.NoError(t, err)
	assert.Contains(t, withCtx, "Context:")
	assert.Contains(t, withCtx, "page_url")
}

// TestProcessGuards covers the no-subprocess error branches of Process.
func TestProcessGuards(t *testing.T) {
	p, err := New(DefaultConfig())
	require.NoError(t, err)

	_, err = p.Process(t.Context(), Task{Type: "does_not_exist", Input: map[string]string{}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown task type")

	require.NoError(t, p.Close())
	_, err = p.Process(t.Context(), Task{Type: TaskTypeSummarize})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "closed")
}
