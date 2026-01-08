// Package debug provides debug logging utilities for agnt.
package debug

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

var (
	// enabled controls whether debug logging is active
	enabled atomic.Bool

	// logFile is the optional file to write debug logs to
	logFile     *os.File
	logFileMu   sync.Mutex
	logFilePath string

	// logger is the debug logger instance
	logger *log.Logger
)

func init() {
	// Check environment variable on startup
	if os.Getenv("AGNT_DEBUG") != "" {
		Enable()
	}

	// Initialize logger to stderr by default
	logger = log.New(os.Stderr, "", log.LstdFlags)
}

// Enable turns on debug logging.
func Enable() {
	enabled.Store(true)
}

// Disable turns off debug logging.
func Disable() {
	enabled.Store(false)
}

// IsEnabled returns whether debug logging is enabled.
func IsEnabled() bool {
	return enabled.Load()
}

// SetLogFile sets an optional file to write debug logs to.
// If path is empty, logs go to stderr only.
// The file is created/appended to in the user's cache directory.
func SetLogFile(name string) error {
	logFileMu.Lock()
	defer logFileMu.Unlock()

	// Close existing file if open
	if logFile != nil {
		logFile.Close()
		logFile = nil
	}

	if name == "" {
		logger.SetOutput(os.Stderr)
		logFilePath = ""
		return nil
	}

	// Create log file in cache directory
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		cacheDir = os.TempDir()
	}

	logDir := filepath.Join(cacheDir, "agnt", "logs")
	if err := os.MkdirAll(logDir, 0755); err != nil {
		return fmt.Errorf("failed to create log directory: %w", err)
	}

	logFilePath = filepath.Join(logDir, name)
	f, err := os.OpenFile(logFilePath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("failed to open log file: %w", err)
	}

	logFile = f

	// Write to both stderr and file
	logger.SetOutput(io.MultiWriter(os.Stderr, f))

	return nil
}

// GetLogFilePath returns the current log file path, or empty if not set.
func GetLogFilePath() string {
	logFileMu.Lock()
	defer logFileMu.Unlock()
	return logFilePath
}

// Close closes the log file if open.
func Close() {
	logFileMu.Lock()
	defer logFileMu.Unlock()

	if logFile != nil {
		logFile.Close()
		logFile = nil
	}
}

// Log logs a debug message if debug mode is enabled.
// Format: [DEBUG] [component] message
func Log(component, format string, args ...interface{}) {
	if !enabled.Load() {
		return
	}

	msg := fmt.Sprintf(format, args...)
	logger.Printf("[DEBUG] [%s] %s", component, msg)
}

// LogWithTimestamp logs a debug message with a high-precision timestamp.
func LogWithTimestamp(component, format string, args ...interface{}) {
	if !enabled.Load() {
		return
	}

	msg := fmt.Sprintf(format, args...)
	ts := time.Now().Format("15:04:05.000")
	logger.Printf("[DEBUG] [%s] [%s] %s", ts, component, msg)
}

// Error logs an error message (always logged, regardless of debug mode).
func Error(component, format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	logger.Printf("[ERROR] [%s] %s", component, msg)
}

// Warn logs a warning message (always logged, regardless of debug mode).
func Warn(component, format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	logger.Printf("[WARN] [%s] %s", component, msg)
}

// Info logs an info message (always logged, regardless of debug mode).
func Info(component, format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	logger.Printf("[INFO] [%s] %s", component, msg)
}

// Trace logs a detailed trace message (only when debug is enabled).
// Use for very verbose logging like individual function calls.
func Trace(component, format string, args ...interface{}) {
	if !enabled.Load() {
		return
	}

	msg := fmt.Sprintf(format, args...)
	ts := time.Now().Format("15:04:05.000000")
	logger.Printf("[TRACE] [%s] [%s] %s", ts, component, msg)
}

// DumpToFile writes all buffered logs to a file for debugging.
// Returns the file path.
func DumpToFile(prefix string) (string, error) {
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		cacheDir = os.TempDir()
	}

	logDir := filepath.Join(cacheDir, "agnt", "logs")
	if err := os.MkdirAll(logDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create log directory: %w", err)
	}

	filename := fmt.Sprintf("%s-%s.log", prefix, time.Now().Format("20060102-150405"))
	path := filepath.Join(logDir, filename)

	return path, nil
}
