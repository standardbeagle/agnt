//go:build unix

package daemon

// Platform-specific socket configuration for Unix systems.
// This file provides Unix-specific implementations.

// The main socket.go file contains all the Unix implementation
// since Unix domain sockets are the default.
// This file exists to ensure proper build tags for future
// platform-specific overrides.
