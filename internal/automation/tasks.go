package automation

import "time"

// TaskType identifies the type of automation task.
type TaskType string

const (
	// TaskTypeAuditProcess processes raw audit data into actionable output
	TaskTypeAuditProcess TaskType = "audit_process"

	// TaskTypeSummarize summarizes content
	TaskTypeSummarize TaskType = "summarize"

	// TaskTypePrioritize prioritizes action items
	TaskTypePrioritize TaskType = "prioritize"

	// TaskTypeGenerateFixes generates fix suggestions
	TaskTypeGenerateFixes TaskType = "generate_fixes"

	// TaskTypeCorrelate correlates related issues
	TaskTypeCorrelate TaskType = "correlate"
)

// Task represents an automation task to process.
type Task struct {
	// Type is the task type
	Type TaskType

	// Input is the task-specific input data
	Input interface{}

	// Context provides additional context (page URL, etc.)
	Context map[string]interface{}

	// Options configures task processing
	Options TaskOptions
}

// TaskOptions configures task processing.
type TaskOptions struct {
	// Model overrides the default model for this task
	Model string

	// MaxTokens limits the response tokens
	MaxTokens int

	// Temperature controls randomness (0.0-1.0, lower = more deterministic)
	Temperature float64
}

// Result represents the processed output.
type Result struct {
	// Type is the task type that was processed
	Type TaskType

	// Output is the task-specific output
	Output interface{}

	// Tokens is the number of tokens used
	Tokens int

	// Cost is the cost in USD
	Cost float64

	// Duration is the processing time
	Duration time.Duration

	// Error contains any processing error
	Error error
}

// AuditProcessInput is input for audit processing tasks.
type AuditProcessInput struct {
	// AuditType is the type of audit (accessibility, security, etc.)
	AuditType string `json:"audit_type"`

	// RawData is the raw audit output from browser
	RawData map[string]interface{} `json:"raw_data"`

	// PageURL is the URL being audited
	PageURL string `json:"page_url"`

	// PageTitle is the page title
	PageTitle string `json:"page_title"`
}

// AuditProcessOutput is output from audit processing.
type AuditProcessOutput struct {
	// Summary is a 1-2 sentence summary
	Summary string `json:"summary"`

	// Score is the 0-100 score
	Score int `json:"score"`

	// Grade is the A-F grade
	Grade string `json:"grade"`

	// CheckedAt is when the audit was performed
	CheckedAt string `json:"checked_at"`

	// ChecksRun lists the check IDs that were executed
	ChecksRun []string `json:"checks_run"`

	// Fixable contains issues with selectors that can be fixed
	Fixable []FixableIssue `json:"fixable"`

	// Informational contains non-actionable info
	Informational []InformationalIssue `json:"informational"`

	// Actions lists prioritized actions
	Actions []string `json:"actions"`

	// CorrelatedGroups groups related issues
	CorrelatedGroups []CorrelatedGroup `json:"correlated_groups,omitempty"`

	// Stats contains summary counts
	Stats AuditStats `json:"stats"`
}

// FixableIssue represents an actionable issue.
type FixableIssue struct {
	// ID is the unique issue ID
	ID string `json:"id"`

	// Type is the issue type
	Type string `json:"type"`

	// Severity is error, warning, or info
	Severity string `json:"severity"`

	// Impact is the 1-10 impact score
	Impact int `json:"impact"`

	// Selector is the CSS selector to target the element
	Selector string `json:"selector"`

	// Element is the truncated HTML of the element
	Element string `json:"element,omitempty"`

	// Message is the human-readable issue description
	Message string `json:"message"`

	// Fix is the specific fix instruction
	Fix string `json:"fix"`

	// Standard is the standard reference (WCAG, etc.)
	Standard string `json:"standard,omitempty"`
}

// InformationalIssue represents a non-actionable issue.
type InformationalIssue struct {
	// ID is the unique issue ID
	ID string `json:"id"`

	// Type is the issue type
	Type string `json:"type"`

	// Severity is usually "info"
	Severity string `json:"severity"`

	// Message is the human-readable description
	Message string `json:"message"`

	// Context contains additional context data
	Context map[string]interface{} `json:"context,omitempty"`
}

// CorrelatedGroup groups related issues.
type CorrelatedGroup struct {
	// Name is the group name
	Name string `json:"name"`

	// IssueIDs are the IDs of issues in this group
	IssueIDs []string `json:"issue_ids"`

	// Description describes why these issues are related
	Description string `json:"description"`

	// CommonFix is a fix that addresses all issues in the group
	CommonFix string `json:"common_fix,omitempty"`
}

// AuditStats contains summary counts.
type AuditStats struct {
	// Errors is the count of error-severity issues
	Errors int `json:"errors"`

	// Warnings is the count of warning-severity issues
	Warnings int `json:"warnings"`

	// Info is the count of info-severity issues
	Info int `json:"info"`

	// Fixable is the count of fixable issues
	Fixable int `json:"fixable"`

	// Informational is the count of informational issues
	Informational int `json:"informational"`
}

// NewAuditProcessTask creates a new audit processing task.
func NewAuditProcessTask(auditType string, rawData map[string]interface{}, pageURL, pageTitle string) Task {
	return Task{
		Type: TaskTypeAuditProcess,
		Input: AuditProcessInput{
			AuditType: auditType,
			RawData:   rawData,
			PageURL:   pageURL,
			PageTitle: pageTitle,
		},
		Context: map[string]interface{}{
			"page_url":   pageURL,
			"page_title": pageTitle,
		},
	}
}

// NewSummarizeTask creates a new summarization task.
func NewSummarizeTask(content string, context map[string]interface{}) Task {
	return Task{
		Type:    TaskTypeSummarize,
		Input:   content,
		Context: context,
	}
}

// NewPrioritizeTask creates a new prioritization task.
func NewPrioritizeTask(items []interface{}, context map[string]interface{}) Task {
	return Task{
		Type:    TaskTypePrioritize,
		Input:   items,
		Context: context,
	}
}
