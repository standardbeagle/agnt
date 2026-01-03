// Package automation provides a CLI automation layer using claude-go
// for agent-based processing of tasks like audit result transformation.
package automation

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	claude "github.com/standardbeagle/claude-go"
)

// Processor handles agent-based automation tasks.
type Processor struct {
	mu          sync.RWMutex
	defaultOpts *claude.AgentOptions
	prompts     *PromptRegistry
	config      ProcessorConfig
	stats       ProcessorStats
	closed      atomic.Bool
}

// ProcessorConfig configures the automation processor.
type ProcessorConfig struct {
	// Model is the default model to use (haiku recommended for most tasks)
	Model string

	// MaxBudgetUSD is the maximum cost per task in USD
	MaxBudgetUSD float64

	// MaxTurns is the maximum conversation turns
	MaxTurns int

	// WorkingDir is the working directory context
	WorkingDir string

	// AllowedTools specifies tools the agent can use
	AllowedTools []string

	// DisallowedTools specifies tools the agent cannot use
	DisallowedTools []string

	// TimeoutSecs is the timeout for each task
	TimeoutSecs int
}

// ProcessorStats tracks processor statistics.
type ProcessorStats struct {
	TasksProcessed  int64
	TasksSucceeded  int64
	TasksFailed     int64
	TotalTokens     int64
	TotalCostUSD    float64
	AverageDuration time.Duration
}

// DefaultConfig returns config optimized for automation tasks.
func DefaultConfig() ProcessorConfig {
	return ProcessorConfig{
		Model:        "haiku", // Fast and cheap for processing
		MaxBudgetUSD: 0.01,    // $0.01 limit per task
		MaxTurns:     3,       // Usually 1-2 turns needed
		TimeoutSecs:  30,      // 30 second timeout
		DisallowedTools: []string{ // No file/bash access for processing
			"Bash", "Write", "Edit", "Read",
		},
	}
}

// New creates a new automation processor.
func New(cfg ProcessorConfig) (*Processor, error) {
	if cfg.Model == "" {
		cfg.Model = "haiku"
	}
	if cfg.MaxTurns == 0 {
		cfg.MaxTurns = 3
	}
	if cfg.TimeoutSecs == 0 {
		cfg.TimeoutSecs = 30
	}

	opts := &claude.AgentOptions{
		Model:           cfg.Model,
		MaxTurns:        cfg.MaxTurns,
		MaxBudgetUSD:    cfg.MaxBudgetUSD,
		TimeoutSecs:     cfg.TimeoutSecs,
		AllowedTools:    cfg.AllowedTools,
		DisallowedTools: cfg.DisallowedTools,
		PermissionMode:  claude.PermissionModeBypassPermission,
	}

	if cfg.WorkingDir != "" {
		opts.WorkingDirectory = cfg.WorkingDir
	}

	return &Processor{
		defaultOpts: opts,
		prompts:     DefaultPromptRegistry(),
		config:      cfg,
	}, nil
}

// Process runs a single task and returns the result.
func (p *Processor) Process(ctx context.Context, task Task) (*Result, error) {
	if p.closed.Load() {
		return nil, fmt.Errorf("processor is closed")
	}

	startTime := time.Now()

	// Get the system prompt for this task type
	systemPrompt := p.prompts.Get(task.Type)
	if systemPrompt == "" {
		return nil, fmt.Errorf("unknown task type: %s", task.Type)
	}

	// Build the user prompt from task input
	userPrompt, err := p.buildUserPrompt(task)
	if err != nil {
		return nil, fmt.Errorf("failed to build user prompt: %w", err)
	}

	// Create options with the system prompt
	opts := &claude.AgentOptions{
		Model:          p.config.Model,
		MaxTurns:       p.config.MaxTurns,
		MaxBudgetUSD:   p.config.MaxBudgetUSD,
		TimeoutSecs:    p.config.TimeoutSecs,
		SystemPrompt:   systemPrompt,
		PermissionMode: claude.PermissionModeBypassPermission,
	}

	// Apply task-specific options
	if task.Options.Model != "" {
		opts.Model = task.Options.Model
	}

	// Run the query
	messages, err := claude.Query(ctx, userPrompt, opts)
	if err != nil {
		atomic.AddInt64(&p.stats.TasksProcessed, 1)
		atomic.AddInt64(&p.stats.TasksFailed, 1)
		return &Result{
			Type:     task.Type,
			Error:    err,
			Duration: time.Since(startTime),
		}, nil
	}

	// Parse the result from messages
	result := &Result{
		Type:     task.Type,
		Duration: time.Since(startTime),
	}

	// Extract the final text content
	var textContent string
	for _, msg := range messages {
		switch m := msg.(type) {
		case claude.AssistantMessage:
			// Extract text from content blocks
			textContent = claude.GetText(m)
		case claude.ResultMessage:
			result.Cost = m.TotalCostUSD
			if m.Usage != nil {
				result.Tokens = m.Usage.InputTokens + m.Usage.OutputTokens
			}
		}
	}

	// Try to parse as JSON for structured output
	if textContent != "" {
		var output interface{}
		if err := json.Unmarshal([]byte(textContent), &output); err == nil {
			result.Output = output
		} else {
			// Return as plain text if not JSON
			result.Output = textContent
		}
	}

	// Update stats
	atomic.AddInt64(&p.stats.TasksProcessed, 1)
	atomic.AddInt64(&p.stats.TasksSucceeded, 1)

	return result, nil
}

// ProcessBatch runs multiple tasks concurrently.
func (p *Processor) ProcessBatch(ctx context.Context, tasks []Task) ([]*Result, error) {
	if p.closed.Load() {
		return nil, fmt.Errorf("processor is closed")
	}

	results := make([]*Result, len(tasks))
	var wg sync.WaitGroup
	var mu sync.Mutex
	var firstErr error

	for i, task := range tasks {
		wg.Add(1)
		go func(idx int, t Task) {
			defer wg.Done()

			result, err := p.Process(ctx, t)
			if err != nil {
				mu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				mu.Unlock()
				return
			}

			mu.Lock()
			results[idx] = result
			mu.Unlock()
		}(i, task)
	}

	wg.Wait()

	if firstErr != nil {
		return results, firstErr
	}

	return results, nil
}

// Stats returns the processor statistics.
func (p *Processor) Stats() ProcessorStats {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.stats
}

// Close shuts down the processor.
func (p *Processor) Close() error {
	p.closed.Store(true)
	return nil
}

// buildUserPrompt constructs the user prompt from task input.
func (p *Processor) buildUserPrompt(task Task) (string, error) {
	// Marshal the input to JSON
	inputJSON, err := json.MarshalIndent(task.Input, "", "  ")
	if err != nil {
		return "", fmt.Errorf("failed to marshal input: %w", err)
	}

	// Add context if present
	var contextStr string
	if len(task.Context) > 0 {
		contextJSON, err := json.MarshalIndent(task.Context, "", "  ")
		if err == nil {
			contextStr = fmt.Sprintf("\n\nContext:\n%s", string(contextJSON))
		}
	}

	return fmt.Sprintf("Process the following data:\n\n%s%s", string(inputJSON), contextStr), nil
}
