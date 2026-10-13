//go:build !darwin

package platform

// ProcessName is the macOS process-name lookup (process_name_darwin.go). On
// Linux the name comes from /proc and on Windows from the process image path,
// both read by internal/config, so there is nothing for this to add and it
// returns "".
func ProcessName(pid int) string { return "" }
