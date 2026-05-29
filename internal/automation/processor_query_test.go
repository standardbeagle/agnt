package automation

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	claude "github.com/standardbeagle/claude-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// assistantWithText builds an AssistantMessage carrying a single text block,
// the shape claude.GetText extracts from.
func assistantWithText(text string) claude.AssistantMessage {
	return claude.AssistantMessage{
		Type:    "assistant",
		Content: []claude.ContentBlock{claude.TextBlock{Type: "text", Text: text}},
	}
}

// resultMessage builds a ResultMessage with the given cost and token usage.
func resultMessage(cost float64, in, out int) claude.ResultMessage {
	return claude.ResultMessage{
		Type:         "result",
		TotalCostUSD: cost,
		Usage:        &claude.Usage{InputTokens: in, OutputTokens: out},
	}
}

// fakeQuery returns a queryFunc that yields the supplied messages and error,
// recording the prompt and opts it was called with.
func fakeQuery(msgs []claude.MessageType, err error, capturePrompt *string, captureOpts **claude.AgentOptions) queryFunc {
	return func(_ context.Context, prompt string, opts *claude.AgentOptions) ([]claude.MessageType, error) {
		if capturePrompt != nil {
			*capturePrompt = prompt
		}
		if captureOpts != nil {
			*captureOpts = opts
		}
		return msgs, err
	}
}

// TestProcessHappyPath drives Process with a fake that returns an assistant
// message plus a result message; asserts GetText extraction, cost/token
// population, JSON parsing, and a success stat increment.
func TestProcessHappyPath(t *testing.T) {
	p, err := New(DefaultConfig())
	require.NoError(t, err)

	var gotPrompt string
	var gotOpts *claude.AgentOptions
	p.queryFn = fakeQuery(
		[]claude.MessageType{
			assistantWithText(`{"summary":"all good","score":91}`),
			resultMessage(0.0042, 120, 80),
		},
		nil, &gotPrompt, &gotOpts,
	)

	res, err := p.Process(context.Background(), NewSummarizeTask("hello", nil))
	require.NoError(t, err)
	require.NotNil(t, res)

	assert.NoError(t, res.Error, "no per-result error on happy path")
	assert.Equal(t, TaskTypeSummarize, res.Type, "result echoes task type")
	assert.InDelta(t, 0.0042, res.Cost, 1e-9, "cost taken from ResultMessage")
	assert.Equal(t, 200, res.Tokens, "tokens = input + output")
	assert.Positive(t, res.Duration, "duration measured")

	out, ok := res.Output.(map[string]interface{})
	require.True(t, ok, "valid JSON parsed into a structured map")
	assert.Equal(t, "all good", out["summary"])
	assert.InDelta(t, 91.0, out["score"], 1e-9)

	assert.Contains(t, gotPrompt, "Process the following data:", "prompt built from task input")
	require.NotNil(t, gotOpts)
	assert.Equal(t, summarizePrompt, gotOpts.SystemPrompt, "system prompt resolved from registry")

	s := p.Stats()
	assert.Equal(t, int64(1), s.TasksProcessed)
	assert.Equal(t, int64(1), s.TasksSucceeded, "success counted")
	assert.Equal(t, int64(0), s.TasksFailed)
	assert.Equal(t, int64(200), s.TotalTokens)
}

// TestProcessOutputJSONvsText covers the JSON-vs-plain-text branch of Process:
// valid JSON text becomes a structured value; non-JSON text stays a string.
func TestProcessOutputJSONvsText(t *testing.T) {
	tests := []struct {
		name      string
		text      string
		wantJSON  bool
		assertOut func(t *testing.T, out interface{})
	}{
		{
			name:     "object",
			text:     `{"a":1,"b":"two"}`,
			wantJSON: true,
			assertOut: func(t *testing.T, out interface{}) {
				m, ok := out.(map[string]interface{})
				require.True(t, ok, "object parses to map")
				assert.InDelta(t, 1.0, m["a"], 1e-9)
				assert.Equal(t, "two", m["b"])
			},
		},
		{
			name:     "array",
			text:     `[1,2,3]`,
			wantJSON: true,
			assertOut: func(t *testing.T, out interface{}) {
				arr, ok := out.([]interface{})
				require.True(t, ok, "array parses to slice")
				assert.Len(t, arr, 3)
			},
		},
		{
			name:     "plain text",
			text:     "this is not json at all",
			wantJSON: false,
			assertOut: func(t *testing.T, out interface{}) {
				s, ok := out.(string)
				require.True(t, ok, "non-JSON stays a string")
				assert.Equal(t, "this is not json at all", s)
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p, err := New(DefaultConfig())
			require.NoError(t, err)
			p.queryFn = fakeQuery(
				[]claude.MessageType{assistantWithText(tc.text), resultMessage(0, 1, 1)},
				nil, nil, nil,
			)

			res, err := p.Process(context.Background(), NewSummarizeTask("x", nil))
			require.NoError(t, err)
			require.NotNil(t, res)
			require.NotNil(t, res.Output, "output populated for non-empty text")
			tc.assertOut(t, res.Output)

			_, isString := res.Output.(string)
			assert.Equal(t, !tc.wantJSON, isString, "string only for non-JSON input")
			assert.Equal(t, int64(1), p.Stats().TasksSucceeded)
		})
	}
}

// TestProcessQueryError covers the documented contract: a query error yields a
// Result{Error:...} with a nil Go error, and increments TasksFailed.
func TestProcessQueryError(t *testing.T) {
	p, err := New(DefaultConfig())
	require.NoError(t, err)

	sentinel := errors.New("subprocess exploded")
	p.queryFn = fakeQuery(nil, sentinel, nil, nil)

	res, err := p.Process(context.Background(), NewSummarizeTask("x", nil))
	require.NoError(t, err, "query error is surfaced via Result, not the Go error")
	require.NotNil(t, res)

	assert.ErrorIs(t, res.Error, sentinel, "query error carried on the Result")
	assert.Equal(t, TaskTypeSummarize, res.Type)
	assert.Nil(t, res.Output, "no output on failure")
	assert.Zero(t, res.Tokens, "no tokens on failure")

	s := p.Stats()
	assert.Equal(t, int64(1), s.TasksProcessed)
	assert.Equal(t, int64(1), s.TasksFailed, "failure counted")
	assert.Equal(t, int64(0), s.TasksSucceeded)
}

// TestProcessBatchOrdering asserts batch results land at their task index
// regardless of completion order, lengths match, and an empty input yields an
// empty (non-nil) result slice.
func TestProcessBatchOrdering(t *testing.T) {
	p, err := New(DefaultConfig())
	require.NoError(t, err)

	// Gate completion so task 0 finishes last, proving index placement is
	// independent of completion order. No sleeps: a channel per index gates
	// each goroutine deterministically.
	gate := make(chan struct{})
	var started sync.WaitGroup
	started.Add(3)
	p.queryFn = func(_ context.Context, prompt string, _ *claude.AgentOptions) ([]claude.MessageType, error) {
		started.Done()
		// First task waits for the gate; others proceed immediately. The text
		// echoes the prompt's index marker so we can verify index alignment.
		// Each input is "task-N"; the prompt embeds "task-N".
		marker := prompt[strings.Index(prompt, "task-")+len("task-")]
		if marker == '0' {
			<-gate
		}
		return []claude.MessageType{assistantWithText("\"" + string(marker) + "\""), resultMessage(0, 1, 1)}, nil
	}

	tasks := []Task{
		{Type: TaskTypeSummarize, Input: "task-0"},
		{Type: TaskTypeSummarize, Input: "task-1"},
		{Type: TaskTypeSummarize, Input: "task-2"},
	}

	var results []*Result
	var batchErr error
	done := make(chan struct{})
	go func() {
		results, batchErr = p.ProcessBatch(context.Background(), tasks)
		close(done)
	}()

	started.Wait() // all three goroutines entered queryFn
	close(gate)    // release task 0 last
	<-done

	require.NoError(t, batchErr)
	require.Len(t, results, len(tasks), "one result per task")
	for i, r := range results {
		require.NotNil(t, r, "result %d present", i)
		assert.Equal(t, string(rune('0'+i)), r.Output, "result %d aligned to its task index", i)
	}
	assert.Equal(t, int64(3), p.Stats().TasksSucceeded)
}

// TestProcessBatchEmptyAndError covers the empty-slice and firstErr branches of
// ProcessBatch.
func TestProcessBatchEmptyAndError(t *testing.T) {
	p, err := New(DefaultConfig())
	require.NoError(t, err)
	p.queryFn = fakeQuery([]claude.MessageType{assistantWithText("\"ok\""), resultMessage(0, 1, 1)}, nil, nil, nil)

	// Empty input -> empty, non-nil results, no error.
	empty, err := p.ProcessBatch(context.Background(), nil)
	require.NoError(t, err)
	assert.NotNil(t, empty, "empty result slice is non-nil")
	assert.Len(t, empty, 0)

	// An unknown task type returns a Go error from Process, which becomes
	// firstErr; the slot for that task stays nil.
	tasks := []Task{
		{Type: TaskTypeSummarize, Input: "ok"},
		{Type: "nope", Input: "bad"},
	}
	results, err := p.ProcessBatch(context.Background(), tasks)
	require.Error(t, err, "firstErr propagated from the unknown task type")
	assert.Contains(t, err.Error(), "unknown task type")
	require.Len(t, results, 2, "slice still sized to tasks")
	assert.Nil(t, results[1], "errored task leaves a nil slot")
}

// TestProcessBatchClosed asserts a closed processor refuses batch work.
func TestProcessBatchClosed(t *testing.T) {
	p, err := New(DefaultConfig())
	require.NoError(t, err)
	require.NoError(t, p.Close())

	results, err := p.ProcessBatch(context.Background(), []Task{{Type: TaskTypeSummarize}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "closed")
	assert.Nil(t, results, "no results from a closed processor")
}

// unmarshalable is a value json.Marshal cannot encode (contains a channel),
// used to drive buildUserPrompt's marshal-error path.
type unmarshalable struct {
	Ch chan int
}

// TestBuildUserPromptMarshalError covers the marshal-error branch of
// buildUserPrompt (and, transitively, Process's prompt-build error path).
func TestBuildUserPromptMarshalError(t *testing.T) {
	p, err := New(DefaultConfig())
	require.NoError(t, err)

	_, err = p.buildUserPrompt(Task{Input: unmarshalable{Ch: make(chan int)}})
	require.Error(t, err, "channel-bearing input fails to marshal")
	assert.Contains(t, err.Error(), "marshal input")

	// Process surfaces the same failure as a Go error before any query runs.
	called := false
	p.queryFn = func(context.Context, string, *claude.AgentOptions) ([]claude.MessageType, error) {
		called = true
		return nil, nil
	}
	_, perr := p.Process(context.Background(), Task{Type: TaskTypeSummarize, Input: unmarshalable{Ch: make(chan int)}})
	require.Error(t, perr)
	assert.Contains(t, perr.Error(), "build user prompt")
	assert.False(t, called, "query not invoked when prompt build fails")
}

// TestNewDefaults asserts New applies the documented zero-value fallbacks and
// wires opts/queryFn.
func TestNewDefaults(t *testing.T) {
	p, err := New(ProcessorConfig{}) // all zero
	require.NoError(t, err)
	require.NotNil(t, p)

	assert.Equal(t, "haiku", p.config.Model, "empty model defaults to haiku")
	assert.Equal(t, 3, p.config.MaxTurns, "zero MaxTurns defaults to 3")
	assert.Equal(t, 30, p.config.TimeoutSecs, "zero TimeoutSecs defaults to 30")
	require.NotNil(t, p.defaultOpts)
	assert.Equal(t, claude.PermissionModeBypassPermission, p.defaultOpts.PermissionMode)
	assert.Equal(t, "haiku", p.defaultOpts.Model)
	require.NotNil(t, p.queryFn, "queryFn defaults to claude.Query")
}

// TestDefaultConfig asserts DefaultConfig's documented values, including the
// disallowed tool set.
func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()
	assert.Equal(t, "haiku", cfg.Model)
	assert.Equal(t, 3, cfg.MaxTurns)
	assert.Equal(t, 30, cfg.TimeoutSecs)
	assert.InDelta(t, 0.01, cfg.MaxBudgetUSD, 1e-9)
	for _, tool := range []string{"Bash", "Write", "Edit", "Read"} {
		assert.Contains(t, cfg.DisallowedTools, tool, "%s must be disallowed", tool)
	}
}
