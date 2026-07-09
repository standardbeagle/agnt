package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/standardbeagle/agnt/internal/automation"

	hubpkg "github.com/standardbeagle/go-cli-server/hub"
	hubproto "github.com/standardbeagle/go-cli-server/protocol"
)

func (d *Daemon) automateActions() map[string]handlerFn {
	return map[string]handlerFn{
		"PROCESS": d.hubHandleAutomateProcess,
		"BATCH":   d.hubHandleAutomateBatch,
	}
}

func (d *Daemon) hubHandleAutomate(ctx context.Context, conn *hubpkg.Connection, cmd *hubproto.Command) error {
	actions := d.automateActions()
	return newCommandRouter("AUTOMATE").
		withDefault(func(_ context.Context, conn *hubpkg.Connection, cmd *hubproto.Command) error {
			return writeStructuredErr(conn, "daemon", &hubproto.StructuredError{
				Code:         hubproto.ErrInvalidAction,
				Message:      "unknown AUTOMATE sub-command",
				Command:      "AUTOMATE",
				ValidActions: routerSubVerbs(actions),
			})
		}).
		dispatch(ctx, conn, cmd, actions)
}

// getOrCreateAutomator returns the automation processor, creating it on first use.

func (d *Daemon) getOrCreateAutomator() (*automation.Processor, error) {
	if d.automator != nil {
		return d.automator, nil
	}

	// Create automation processor with default config
	proc, err := automation.New(automation.DefaultConfig())
	if err != nil {
		return nil, fmt.Errorf("failed to create automation processor: %w", err)
	}

	d.automator = proc
	return d.automator, nil
}

// hubHandleAutomateProcess handles AUTOMATE PROCESS command.
// AUTOMATE PROCESS -- <json_task>

func (d *Daemon) hubHandleAutomateProcess(ctx context.Context, conn *hubpkg.Connection, cmd *hubproto.Command) error {
	if len(cmd.Data) == 0 {
		return conn.WriteErr(hubproto.ErrMissingParam, "task data required")
	}

	// Parse the task request
	req, err := unmarshalCommand[struct {
		Type    string                 `json:"type"`
		Data    map[string]interface{} `json:"data"`
		Context map[string]interface{} `json:"context"`
		Options struct {
			Model       string  `json:"model,omitempty"`
			MaxTokens   int     `json:"max_tokens,omitempty"`
			Temperature float64 `json:"temperature,omitempty"`
		} `json:"options,omitempty"`
	}](cmd)
	if err != nil {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "invalid task JSON: "+err.Error())
	}

	if req.Type == "" {
		return conn.WriteErr(hubproto.ErrMissingParam, "task type required")
	}

	// Get or create the automation processor
	proc, err := d.getOrCreateAutomator()
	if err != nil {
		return conn.WriteErr(hubproto.ErrInternal, err.Error())
	}

	// Create the task
	task := automation.Task{
		Type:    automation.TaskType(req.Type),
		Input:   req.Data,
		Context: req.Context,
		Options: automation.TaskOptions{
			Model:       req.Options.Model,
			MaxTokens:   req.Options.MaxTokens,
			Temperature: req.Options.Temperature,
		},
	}

	// Process the task
	startTime := time.Now()
	result, err := proc.Process(ctx, task)
	if err != nil {
		return conn.WriteErr(hubproto.ErrInternal, err.Error())
	}

	// Build response
	resp := map[string]interface{}{
		"success":  result.Error == nil,
		"duration": time.Since(startTime).String(),
	}

	if result.Error != nil {
		resp["error"] = result.Error.Error()
	} else {
		resp["result"] = result.Output
	}

	resp["tokens_used"] = result.Tokens
	resp["cost_usd"] = result.Cost

	data, _ := json.Marshal(resp)
	return conn.WriteJSON(data)
}

// hubHandleAutomateBatch handles AUTOMATE BATCH command.
// AUTOMATE BATCH -- <json_tasks>

func (d *Daemon) hubHandleAutomateBatch(ctx context.Context, conn *hubpkg.Connection, cmd *hubproto.Command) error {
	if len(cmd.Data) == 0 {
		return conn.WriteErr(hubproto.ErrMissingParam, "tasks data required")
	}

	// Parse the batch request
	req, err := unmarshalCommand[struct {
		Tasks []struct {
			Type    string                 `json:"type"`
			Data    map[string]interface{} `json:"data"`
			Context map[string]interface{} `json:"context"`
			Options struct {
				Model       string  `json:"model,omitempty"`
				MaxTokens   int     `json:"max_tokens,omitempty"`
				Temperature float64 `json:"temperature,omitempty"`
			} `json:"options,omitempty"`
		} `json:"tasks"`
	}](cmd)
	if err != nil {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "invalid batch JSON: "+err.Error())
	}

	if len(req.Tasks) == 0 {
		return conn.WriteErr(hubproto.ErrMissingParam, "at least one task required")
	}

	// Get or create the automation processor
	proc, err := d.getOrCreateAutomator()
	if err != nil {
		return conn.WriteErr(hubproto.ErrInternal, err.Error())
	}

	// Convert to automation tasks
	tasks := make([]automation.Task, len(req.Tasks))
	for i, t := range req.Tasks {
		tasks[i] = automation.Task{
			Type:    automation.TaskType(t.Type),
			Input:   t.Data,
			Context: t.Context,
			Options: automation.TaskOptions{
				Model:       t.Options.Model,
				MaxTokens:   t.Options.MaxTokens,
				Temperature: t.Options.Temperature,
			},
		}
	}

	// Process the batch
	startTime := time.Now()
	results, err := proc.ProcessBatch(ctx, tasks)
	if err != nil {
		return conn.WriteErr(hubproto.ErrInternal, err.Error())
	}

	// Build response
	resultList := make([]map[string]interface{}, len(results))
	var totalTokens int
	var totalCost float64
	var successCount, failCount int

	for i, result := range results {
		r := map[string]interface{}{
			"index":   i,
			"success": result != nil && result.Error == nil,
		}

		if result != nil {
			if result.Error != nil {
				r["error"] = result.Error.Error()
				failCount++
			} else {
				r["result"] = result.Output
				successCount++
			}
			r["tokens_used"] = result.Tokens
			r["cost_usd"] = result.Cost
			r["duration"] = result.Duration.String()
			totalTokens += result.Tokens
			totalCost += result.Cost
		} else {
			r["error"] = "no result"
			failCount++
		}

		resultList[i] = r
	}

	resp := map[string]interface{}{
		"results":      resultList,
		"total":        len(results),
		"succeeded":    successCount,
		"failed":       failCount,
		"total_tokens": totalTokens,
		"total_cost":   totalCost,
		"duration":     time.Since(startTime).String(),
	}

	data, _ := json.Marshal(resp)
	return conn.WriteJSON(data)
}

// hubHandleStopAll handles the STOP-ALL command.
// Stops all running processes, proxies, and tunnels without shutting down the daemon.
