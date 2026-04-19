package tools

import (
	"fmt"
	"strings"
)

// Field size limits for MCP tool inputs. Generous enough for normal usage,
// bounded enough to prevent denial-of-service via oversized payloads.
const (
	maxIDLength      = 256
	maxPathLength    = 4096
	maxURLLength     = 8192
	maxCodeLength    = 1 << 20 // 1MB for JS code execution
	maxMessageLength = 1 << 16 // 64KB for messages
	maxStringField   = 1024    // Generic string fields
	maxGrepPattern   = 4096
	maxArrayElements = 100
	maxChaosRules    = 50
	maxLimit         = 10000
	maxPort          = 65535
	maxTimeoutMs     = 300000 // 5 minutes
)

// validateStringLen checks a string field does not exceed maxLen.
func validateStringLen(field, value string, maxLen int) error {
	if len(value) > maxLen {
		return fmt.Errorf("%s too long: %d bytes (max %d)", field, len(value), maxLen)
	}
	return nil
}

// validateArrayLen checks a slice does not exceed maxLen elements.
func validateArrayLen[T any](field string, arr []T, maxLen int) error {
	if len(arr) > maxLen {
		return fmt.Errorf("%s too many elements: %d (max %d)", field, len(arr), maxLen)
	}
	return nil
}

// validatePort checks a port number is in valid range (0 means unset).
func validatePort(field string, port int) error {
	if port < 0 || port > maxPort {
		return fmt.Errorf("%s out of range: %d (must be 0-%d)", field, port, maxPort)
	}
	return nil
}

// validatePositiveLimit checks a limit is non-negative and bounded.
func validatePositiveLimit(field string, value, max int) error {
	if value < 0 || value > max {
		return fmt.Errorf("%s out of range: %d (must be 0-%d)", field, value, max)
	}
	return nil
}

// validateRunInput validates RunInput fields.
func validateRunInput(input RunInput) error {
	checks := []error{
		validateStringLen("path", input.Path, maxPathLength),
		validateStringLen("script_name", input.ScriptName, maxIDLength),
		validateStringLen("command", input.Command, maxPathLength),
		validateStringLen("id", input.ID, maxIDLength),
		validateStringLen("mode", string(input.Mode), maxIDLength),
		validateArrayLen("args", input.Args, maxArrayElements),
	}
	for _, err := range checks {
		if err != nil {
			return err
		}
	}
	for i, arg := range input.Args {
		if err := validateStringLen(fmt.Sprintf("args[%d]", i), arg, maxPathLength); err != nil {
			return err
		}
	}
	return nil
}

// validateProcInput validates ProcInput fields.
func validateProcInput(input ProcInput) error {
	checks := []error{
		validateStringLen("action", input.Action, maxIDLength),
		validateStringLen("process_id", input.ProcessID, maxIDLength),
		validateStringLen("script_name", input.ScriptName, maxIDLength),
		validateStringLen("stream", input.Stream, maxIDLength),
		validateStringLen("grep", input.Grep, maxGrepPattern),
		validatePort("port", input.Port),
		validatePositiveLimit("tail", input.Tail, maxLimit),
		validatePositiveLimit("head", input.Head, maxLimit),
		validatePositiveLimit("max_restarts", input.MaxRestarts, maxLimit),
	}
	for _, err := range checks {
		if err != nil {
			return err
		}
	}
	return nil
}

// validateProxyInput validates ProxyInput fields.
func validateProxyInput(input ProxyInput) error {
	checks := []error{
		validateStringLen("action", input.Action, maxIDLength),
		validateStringLen("id", input.ID, maxIDLength),
		validateStringLen("target_url", input.TargetURL, maxURLLength),
		validateStringLen("bind_address", input.BindAddress, maxStringField),
		validateStringLen("public_url", input.PublicURL, maxURLLength),
		validateStringLen("code", input.Code, maxCodeLength),
		validateStringLen("describe", input.Describe, maxIDLength),
		validateStringLen("search", input.Search, maxStringField),
		validateStringLen("category", input.Category, maxIDLength),
		validateStringLen("toast_type", input.ToastType, maxIDLength),
		validateStringLen("toast_title", input.ToastTitle, maxStringField),
		validateStringLen("toast_message", input.ToastMessage, maxMessageLength),
		validateStringLen("tunnel", input.Tunnel, maxIDLength),
		validateStringLen("tunnel_token", input.TunnelToken, maxStringField),
		validateStringLen("tunnel_region", input.TunnelRegion, maxIDLength),
		validateStringLen("tunnel_command", input.TunnelCommand, maxPathLength),
		validateStringLen("chaos_operation", input.ChaosOperation, maxIDLength),
		validateStringLen("chaos_preset", input.ChaosPreset, maxIDLength),
		validateStringLen("chaos_rule_id", input.ChaosRuleID, maxIDLength),
		validatePort("port", input.Port),
		validatePositiveLimit("toast_duration", input.ToastDuration, maxTimeoutMs),
		validateArrayLen("tunnel_args", input.TunnelArgs, maxArrayElements),
		validateArrayLen("chaos_rules", input.ChaosRules, maxChaosRules),
	}
	for _, err := range checks {
		if err != nil {
			return err
		}
	}
	for i, arg := range input.TunnelArgs {
		if err := validateStringLen(fmt.Sprintf("tunnel_args[%d]", i), arg, maxStringField); err != nil {
			return err
		}
	}
	for i, rule := range input.ChaosRules {
		if err := validateChaosRuleInput(fmt.Sprintf("chaos_rules[%d]", i), rule); err != nil {
			return err
		}
	}
	if input.ChaosRule != nil {
		if err := validateChaosRuleInput("chaos_rule", *input.ChaosRule); err != nil {
			return err
		}
	}
	return nil
}

// validateChaosRuleInput validates a single ChaosRuleInput.
func validateChaosRuleInput(prefix string, rule ChaosRuleInput) error {
	checks := []error{
		validateStringLen(prefix+".id", rule.ID, maxIDLength),
		validateStringLen(prefix+".name", rule.Name, maxIDLength),
		validateStringLen(prefix+".type", rule.Type, maxIDLength),
		validateStringLen(prefix+".url_pattern", rule.URLPattern, maxURLLength),
		validateStringLen(prefix+".error_message", rule.ErrorMessage, maxStringField),
		validateArrayLen(prefix+".methods", rule.Methods, maxArrayElements),
		validateArrayLen(prefix+".error_codes", rule.ErrorCodes, maxArrayElements),
	}
	for _, err := range checks {
		if err != nil {
			return err
		}
	}
	return nil
}

// validateProxyLogInput validates ProxyLogInput fields.
func validateProxyLogInput(input ProxyLogInput) error {
	checks := []error{
		validateStringLen("proxy_id", input.ProxyID, maxIDLength),
		validateStringLen("action", input.Action, maxIDLength),
		validateStringLen("url_pattern", input.URLPattern, maxURLLength),
		validateStringLen("since", input.Since, maxStringField),
		validateStringLen("until", input.Until, maxStringField),
		validateArrayLen("types", input.Types, maxArrayElements),
		validateArrayLen("methods", input.Methods, maxArrayElements),
		validateArrayLen("status_codes", input.StatusCodes, maxArrayElements),
		validateArrayLen("detail", input.Detail, maxArrayElements),
		validateArrayLen("diagnostic_levels", input.DiagnosticLevels, maxArrayElements),
		validatePositiveLimit("limit", input.Limit, maxLimit),
	}
	for _, err := range checks {
		if err != nil {
			return err
		}
	}
	return nil
}

// validateCurrentPageInput validates CurrentPageInput fields.
func validateCurrentPageInput(input CurrentPageInput) error {
	checks := []error{
		validateStringLen("proxy_id", input.ProxyID, maxIDLength),
		validateStringLen("action", input.Action, maxIDLength),
		validateStringLen("session_id", input.SessionID, maxIDLength),
		validateArrayLen("detail", input.Detail, maxArrayElements),
		validatePositiveLimit("limit", input.Limit, maxLimit),
	}
	for _, err := range checks {
		if err != nil {
			return err
		}
	}
	return nil
}

// validateDetectInput validates DetectInput fields.
func validateDetectInput(input DetectInput) error {
	return validateStringLen("path", input.Path, maxPathLength)
}

// validateGetErrorsInput validates GetErrorsInput fields.
func validateGetErrorsInput(input GetErrorsInput) error {
	checks := []error{
		validateStringLen("process_id", input.ProcessID, maxIDLength),
		validateStringLen("proxy_id", input.ProxyID, maxIDLength),
		validateStringLen("since", input.Since, maxStringField),
		validatePositiveLimit("limit", input.Limit, maxLimit),
	}
	for _, err := range checks {
		if err != nil {
			return err
		}
	}
	return nil
}

// validateSnapshotInput validates SnapshotInput fields.
func validateSnapshotInput(input SnapshotInput) error {
	checks := []error{
		validateStringLen("action", input.Action, maxIDLength),
		validateStringLen("name", input.Name, maxIDLength),
		validateStringLen("baseline", input.Baseline, maxIDLength),
		validateArrayLen("pages", input.Pages, maxArrayElements),
	}
	for _, err := range checks {
		if err != nil {
			return err
		}
	}
	return nil
}

// validateBrowserInput validates BrowserInput fields.
func validateBrowserInput(input BrowserInput) error {
	checks := []error{
		validateStringLen("action", input.Action, maxIDLength),
		validateStringLen("id", input.ID, maxIDLength),
		validateStringLen("url", input.URL, maxURLLength),
		validateStringLen("proxy_id", input.ProxyID, maxIDLength),
		validateStringLen("binary_path", input.BinaryPath, maxPathLength),
	}
	for _, err := range checks {
		if err != nil {
			return err
		}
	}
	return nil
}

// validateAutomationInput validates AutomationInput fields.
func validateAutomationInput(input AutomationInput) error {
	checks := []error{
		validateStringLen("action", input.Action, maxIDLength),
		validateStringLen("id", input.ID, maxIDLength),
		validateStringLen("url", input.URL, maxURLLength),
		validateStringLen("proxy_id", input.ProxyID, maxIDLength),
		validateStringLen("type", input.Type, maxIDLength),
		validateStringLen("label", input.Label, maxIDLength),
		validateStringLen("selector", input.Selector, maxStringField),
		validateStringLen("viewport", input.Viewport, maxIDLength),
		validateStringLen("script", input.Script, maxCodeLength),
		validateStringLen("session_id", input.SessionID, maxIDLength),
	}
	for _, err := range checks {
		if err != nil {
			return err
		}
	}
	return nil
}

// validateTunnelInput validates TunnelInput fields.
func validateTunnelInput(input TunnelInput) error {
	checks := []error{
		validateStringLen("action", input.Action, maxIDLength),
		validateStringLen("id", input.ID, maxIDLength),
		validateStringLen("provider", input.Provider, maxIDLength),
		validateStringLen("local_host", input.LocalHost, maxStringField),
		validateStringLen("binary_path", input.BinaryPath, maxPathLength),
		validateStringLen("proxy_id", input.ProxyID, maxIDLength),
		validatePort("local_port", input.LocalPort),
	}
	for _, err := range checks {
		if err != nil {
			return err
		}
	}
	return nil
}

// validateDaemonInput validates DaemonInput fields.
func validateDaemonInput(input DaemonInput) error {
	return validateStringLen("action", input.Action, maxIDLength)
}

// validateSessionInput validates SessionInput fields.
func validateSessionInput(input SessionInput) error {
	checks := []error{
		validateStringLen("action", input.Action, maxIDLength),
		validateStringLen("code", input.Code, maxIDLength),
		validateStringLen("message", input.Message, maxMessageLength),
		validateStringLen("duration", input.Duration, maxIDLength),
		validateStringLen("task_id", input.TaskID, maxIDLength),
	}
	for _, err := range checks {
		if err != nil {
			return err
		}
	}
	return nil
}

// validateStoreInput validates StoreInput fields.
func validateStoreInput(input StoreInput) error {
	checks := []error{
		validateStringLen("action", input.Action, maxIDLength),
		validateStringLen("scope", input.Scope, maxIDLength),
		validateStringLen("scope_key", input.ScopeKey, maxURLLength),
		validateStringLen("key", input.Key, maxStringField),
	}
	for _, err := range checks {
		if err != nil {
			return err
		}
	}
	// Validate metadata keys/values if present
	if err := validateArrayLen("metadata", mapKeys(input.Metadata), maxArrayElements); err != nil {
		return err
	}
	for k := range input.Metadata {
		if err := validateStringLen("metadata key", k, maxStringField); err != nil {
			return err
		}
	}
	return nil
}

// validateResponsiveAuditInputSize validates size limits for ResponsiveAuditInput.
// This complements the existing validateResponsiveAuditInput which validates semantics.
func validateResponsiveAuditInputSize(input ResponsiveAuditInput) error {
	checks := []error{
		validateStringLen("proxy_id", input.ProxyID, maxIDLength),
		validateArrayLen("viewports", input.Viewports, maxArrayElements),
		validateArrayLen("checks", input.Checks, maxArrayElements),
		validatePositiveLimit("timeout", input.Timeout, maxTimeoutMs),
	}
	for _, err := range checks {
		if err != nil {
			return err
		}
	}
	for i, vp := range input.Viewports {
		if err := validateStringLen(fmt.Sprintf("viewports[%d].name", i), vp.Name, maxIDLength); err != nil {
			return err
		}
	}
	for i, check := range input.Checks {
		if err := validateStringLen(fmt.Sprintf("checks[%d]", i), check, maxIDLength); err != nil {
			return err
		}
	}
	return nil
}

// mapKeys returns the keys of a map as a slice.
func mapKeys(m map[string]any) []string {
	if m == nil {
		return nil
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

// validationError formats a validation error with the tool context.
func validationError(toolName string, err error) string {
	return fmt.Sprintf("invalid %s input: %s", toolName, strings.TrimSpace(err.Error()))
}
