// Package tools exposes agnt's functionality as MCP tools for AI agents.
// Each tool is registered with an MCP server via RegisterDaemonTools().
//
// Tools communicate with the daemon over IPC (socket/pipe) using the
// daemon.Client. DaemonTools manages the connection lifecycle, session
// attachment, and auto-start configuration.
//
// Handler functions are organized by domain in daemon_*.go files.
// Converter and formatting functions live in converters.go.
package tools
