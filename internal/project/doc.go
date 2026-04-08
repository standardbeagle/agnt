// Package project detects project types (Go, Node, Python) and their
// associated dev server commands. It scans for lock files, config files,
// and conventions to determine the project type and available scripts.
//
// Key types:
//   - Project: detected project with type, root path, and commands
//   - ProjectType: enum (GoProject, NodeProject, PythonProject)
//   - CommandDef: a runnable command with label and command string
package project
