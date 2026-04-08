// Package overlay provides terminal output processing for the agnt PTY layer.
// It handles activity monitoring, ANSI escape sequence filtering, and
// alert pattern matching in process output.
//
// Key types:
//   - ActivityMonitor: tracks terminal activity for stall detection
//   - ProtectedWriter: filters dangerous ANSI sequences from PTY output
//   - OutputGate: freezes/unfreezes terminal output for menu display
//   - AlertScanner: pattern-matches process output for compile errors,
//     panics, and exceptions
package overlay
