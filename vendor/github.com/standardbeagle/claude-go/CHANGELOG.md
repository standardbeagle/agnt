# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [1.0.0] - 2025-01-26

### Added
- Initial release of claude-go library
- Core `Client` struct with concurrent session management
- `Session` struct for individual Claude CLI process management
- Support for both interactive and single-query modes
- Real-time message streaming via channels
- Thread-safe operations with proper cleanup and context cancellation
- Comprehensive configuration options via `Options` struct:
  - Model selection (sonnet, opus, etc.)
  - Permission modes (bypassPermissions, acceptEdits, etc.)
  - Working directory and additional directory access
  - Tool allowlisting/blocklisting
  - Custom system prompts
  - Environment variable configuration
- Multi-turn conversation support with session persistence
- File operation capabilities (create, read, modify files)
- Concurrent session execution with goroutines
- Comprehensive test suite covering:
  - Basic single queries
  - Multi-turn conversations with memory
  - File operations (creation, reading, modification)
  - Concurrent session management
  - Complex multi-step workflows
- Complete example implementations:
  - Basic usage example
  - Concurrent processing example  
  - Interactive CLI example
- Proper error handling with separate error channels
- UUID-based session ID management
- Automatic process lifecycle management
- Support for Claude CLI's `--session-id` parameter
- Integration with Claude CLI's permission system

### Technical Details
- Go 1.21+ compatibility
- Thread-safe concurrent operations
- Proper resource cleanup with context cancellation
- Real-time streaming via Go channels
- Integration with Claude CLI's stdin/stdout interface
- Support for both `-p` (print) and interactive modes
- Comprehensive test coverage with integration tests
- GitHub Actions CI/CD pipeline
- Complete documentation with Go docs

### Dependencies
- `github.com/google/uuid v1.6.0` - For UUID generation

### Requirements
- Go 1.21 or later
- Claude CLI installed and accessible in PATH
- Valid Claude authentication (API key or subscription)

[Unreleased]: https://github.com/standardbeagle/claude-go/compare/v1.0.0...HEAD
[1.0.0]: https://github.com/standardbeagle/claude-go/releases/tag/v1.0.0