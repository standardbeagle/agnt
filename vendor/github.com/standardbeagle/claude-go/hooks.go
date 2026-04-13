package claude

import (
	"context"
	"encoding/json"
)

// HookEvent represents the type of hook event.
type HookEvent string

const (
	HookEventPreToolUse       HookEvent = "PreToolUse"
	HookEventPostToolUse      HookEvent = "PostToolUse"
	HookEventUserPromptSubmit HookEvent = "UserPromptSubmit"
	HookEventStop             HookEvent = "Stop"
	HookEventSubagentStop     HookEvent = "SubagentStop"
	HookEventPreCompact       HookEvent = "PreCompact"
)

// HookContext provides context information for hook callbacks.
type HookContext struct {
	SessionID      string
	TranscriptPath string
	WorkingDir     string
}

// BaseHookInput contains common fields for all hook inputs.
type BaseHookInput struct {
	SessionID      string `json:"session_id"`
	TranscriptPath string `json:"transcript_path,omitempty"`
	WorkingDir     string `json:"cwd,omitempty"`
}

// PreToolUseInput is the input for PreToolUse hooks.
type PreToolUseInput struct {
	BaseHookInput
	ToolName  string                 `json:"tool_name"`
	ToolInput map[string]interface{} `json:"tool_input"`
}

// PostToolUseInput is the input for PostToolUse hooks.
type PostToolUseInput struct {
	BaseHookInput
	ToolName   string                 `json:"tool_name"`
	ToolInput  map[string]interface{} `json:"tool_input"`
	ToolResult interface{}            `json:"tool_result"`
	IsError    bool                   `json:"is_error,omitempty"`
}

// UserPromptSubmitInput is the input for UserPromptSubmit hooks.
type UserPromptSubmitInput struct {
	BaseHookInput
	Prompt string `json:"prompt"`
}

// StopInput is the input for Stop hooks.
type StopInput struct {
	BaseHookInput
	Reason string `json:"reason,omitempty"`
}

// SubagentStopInput is the input for SubagentStop hooks.
type SubagentStopInput struct {
	BaseHookInput
	SubagentID string `json:"subagent_id"`
	Reason     string `json:"reason,omitempty"`
}

// PreCompactInput is the input for PreCompact hooks.
type PreCompactInput struct {
	BaseHookInput
	TokenCount int `json:"token_count"`
}

// HookInput is a union type for all hook inputs.
type HookInput interface {
	hookInput()
}

func (PreToolUseInput) hookInput()       {}
func (PostToolUseInput) hookInput()      {}
func (UserPromptSubmitInput) hookInput() {}
func (StopInput) hookInput()             {}
func (SubagentStopInput) hookInput()     {}
func (PreCompactInput) hookInput()       {}

// HookOutput contains the response from a hook callback.
type HookOutput struct {
	// For PreToolUse hooks
	PermissionDecision       PermissionBehavior     `json:"permissionDecision,omitempty"`
	PermissionDecisionReason string                 `json:"permissionDecisionReason,omitempty"`
	UpdatedInput             map[string]interface{} `json:"updatedInput,omitempty"`

	// For PostToolUse hooks
	Feedback      string `json:"feedback,omitempty"`
	SystemMessage string `json:"systemMessage,omitempty"`

	// For UserPromptSubmit hooks
	UpdatedPrompt string `json:"updatedPrompt,omitempty"`
	Context       string `json:"context,omitempty"`

	// For all hooks
	Continue   *bool  `json:"continue,omitempty"`
	StopReason string `json:"stopReason,omitempty"`

	// For async hooks
	Async   bool   `json:"async,omitempty"`
	AsyncID string `json:"asyncId,omitempty"`
}

// HookCallback is the function signature for hook callbacks.
// ctx provides context for the hook execution.
// input is the event-specific input data.
// toolUseID is the ID of the tool use (only for tool-related hooks).
// hookCtx provides additional context information.
type HookCallback func(ctx context.Context, input HookInput, toolUseID string, hookCtx HookContext) (*HookOutput, error)

// HookMatcher defines a pattern for matching hook events.
type HookMatcher struct {
	// Matcher is the pattern to match against tool names.
	// Can be "*" to match all, or a specific tool name.
	// For non-tool hooks, this is typically empty or "*".
	Matcher string

	// Hooks are the callbacks to execute when this matcher matches.
	Hooks []HookCallback

	// TimeoutMS is the timeout for hook execution in milliseconds.
	// If 0, a default timeout is used.
	TimeoutMS int
}

// HookRegistry manages hook registrations.
type HookRegistry struct {
	preToolUse       []HookMatcher
	postToolUse      []HookMatcher
	userPromptSubmit []HookMatcher
	stop             []HookMatcher
	subagentStop     []HookMatcher
	preCompact       []HookMatcher
}

// NewHookRegistry creates a new HookRegistry.
func NewHookRegistry() *HookRegistry {
	return &HookRegistry{}
}

// Register registers hooks for a specific event type.
func (r *HookRegistry) Register(event HookEvent, matchers []HookMatcher) {
	switch event {
	case HookEventPreToolUse:
		r.preToolUse = append(r.preToolUse, matchers...)
	case HookEventPostToolUse:
		r.postToolUse = append(r.postToolUse, matchers...)
	case HookEventUserPromptSubmit:
		r.userPromptSubmit = append(r.userPromptSubmit, matchers...)
	case HookEventStop:
		r.stop = append(r.stop, matchers...)
	case HookEventSubagentStop:
		r.subagentStop = append(r.subagentStop, matchers...)
	case HookEventPreCompact:
		r.preCompact = append(r.preCompact, matchers...)
	}
}

// GetMatchers returns the matchers for a specific event type.
func (r *HookRegistry) GetMatchers(event HookEvent) []HookMatcher {
	switch event {
	case HookEventPreToolUse:
		return r.preToolUse
	case HookEventPostToolUse:
		return r.postToolUse
	case HookEventUserPromptSubmit:
		return r.userPromptSubmit
	case HookEventStop:
		return r.stop
	case HookEventSubagentStop:
		return r.subagentStop
	case HookEventPreCompact:
		return r.preCompact
	default:
		return nil
	}
}

// ExecuteHooks executes all matching hooks for an event.
func (r *HookRegistry) ExecuteHooks(ctx context.Context, event HookEvent, input HookInput, toolUseID string, hookCtx HookContext) (*HookOutput, error) {
	matchers := r.GetMatchers(event)
	if len(matchers) == 0 {
		return nil, nil
	}

	// Get tool name for matching (if applicable)
	var toolName string
	switch in := input.(type) {
	case PreToolUseInput:
		toolName = in.ToolName
	case PostToolUseInput:
		toolName = in.ToolName
	}

	// Combine outputs from all matching hooks
	var combinedOutput *HookOutput

	for _, matcher := range matchers {
		// Check if matcher applies
		if matcher.Matcher != "" && matcher.Matcher != "*" && matcher.Matcher != toolName {
			continue
		}

		for _, hook := range matcher.Hooks {
			output, err := hook(ctx, input, toolUseID, hookCtx)
			if err != nil {
				return nil, NewHookError(err.Error(), string(event), toolName, err)
			}

			if output != nil {
				if combinedOutput == nil {
					combinedOutput = output
				} else {
					// Merge outputs (later hooks can override)
					mergeHookOutputs(combinedOutput, output)
				}

				// If a hook denies permission or stops execution, return immediately
				if output.PermissionDecision == PermissionBehaviorDeny {
					return combinedOutput, nil
				}
				if output.Continue != nil && !*output.Continue {
					return combinedOutput, nil
				}
			}
		}
	}

	return combinedOutput, nil
}

// mergeHookOutputs merges source into target, overriding non-zero values.
func mergeHookOutputs(target, source *HookOutput) {
	if source.PermissionDecision != "" {
		target.PermissionDecision = source.PermissionDecision
	}
	if source.PermissionDecisionReason != "" {
		target.PermissionDecisionReason = source.PermissionDecisionReason
	}
	if source.UpdatedInput != nil {
		target.UpdatedInput = source.UpdatedInput
	}
	if source.Feedback != "" {
		target.Feedback = source.Feedback
	}
	if source.SystemMessage != "" {
		target.SystemMessage = source.SystemMessage
	}
	if source.UpdatedPrompt != "" {
		target.UpdatedPrompt = source.UpdatedPrompt
	}
	if source.Context != "" {
		target.Context = source.Context
	}
	if source.Continue != nil {
		target.Continue = source.Continue
	}
	if source.StopReason != "" {
		target.StopReason = source.StopReason
	}
	if source.Async {
		target.Async = source.Async
	}
	if source.AsyncID != "" {
		target.AsyncID = source.AsyncID
	}
}

// ParseHookInput parses raw JSON into the appropriate HookInput type.
func ParseHookInput(event HookEvent, raw json.RawMessage) (HookInput, error) {
	switch event {
	case HookEventPreToolUse:
		var input PreToolUseInput
		if err := json.Unmarshal(raw, &input); err != nil {
			return nil, err
		}
		return input, nil
	case HookEventPostToolUse:
		var input PostToolUseInput
		if err := json.Unmarshal(raw, &input); err != nil {
			return nil, err
		}
		return input, nil
	case HookEventUserPromptSubmit:
		var input UserPromptSubmitInput
		if err := json.Unmarshal(raw, &input); err != nil {
			return nil, err
		}
		return input, nil
	case HookEventStop:
		var input StopInput
		if err := json.Unmarshal(raw, &input); err != nil {
			return nil, err
		}
		return input, nil
	case HookEventSubagentStop:
		var input SubagentStopInput
		if err := json.Unmarshal(raw, &input); err != nil {
			return nil, err
		}
		return input, nil
	case HookEventPreCompact:
		var input PreCompactInput
		if err := json.Unmarshal(raw, &input); err != nil {
			return nil, err
		}
		return input, nil
	default:
		return nil, NewMessageParseError("unknown hook event", raw, nil)
	}
}

// Hooks is a map of hook events to their matchers, used for configuration.
type Hooks map[HookEvent][]HookMatcher

// ToRegistry converts a Hooks map to a HookRegistry.
func (h Hooks) ToRegistry() *HookRegistry {
	registry := NewHookRegistry()
	for event, matchers := range h {
		registry.Register(event, matchers)
	}
	return registry
}

// Helper functions for creating common hook patterns.

// DenyTool creates a hook that denies a specific tool.
func DenyTool(reason string) HookCallback {
	return func(ctx context.Context, input HookInput, toolUseID string, hookCtx HookContext) (*HookOutput, error) {
		return &HookOutput{
			PermissionDecision:       PermissionBehaviorDeny,
			PermissionDecisionReason: reason,
		}, nil
	}
}

// AllowTool creates a hook that allows a specific tool.
func AllowTool() HookCallback {
	return func(ctx context.Context, input HookInput, toolUseID string, hookCtx HookContext) (*HookOutput, error) {
		return &HookOutput{
			PermissionDecision: PermissionBehaviorAllow,
		}, nil
	}
}

// StopExecution creates a hook that stops execution.
func StopExecution(reason string) HookCallback {
	return func(ctx context.Context, input HookInput, toolUseID string, hookCtx HookContext) (*HookOutput, error) {
		cont := false
		return &HookOutput{
			Continue:   &cont,
			StopReason: reason,
		}, nil
	}
}

// AddContext creates a hook that adds context to the prompt.
func AddContext(contextStr string) HookCallback {
	return func(ctx context.Context, input HookInput, toolUseID string, hookCtx HookContext) (*HookOutput, error) {
		return &HookOutput{
			Context: contextStr,
		}, nil
	}
}
