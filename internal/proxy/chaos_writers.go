package proxy

import (
	"bufio"
	"context"
	"errors"
	"net"
	"net/http"
	"sync/atomic"
	"time"
)

// SlowDripWriter wraps http.ResponseWriter to stream bytes with delays.
// This simulates slow network connections or bandwidth throttling.
type SlowDripWriter struct {
	w           http.ResponseWriter
	bytesPerMs  int           // Bytes to write per millisecond
	chunkSize   int           // Size of each write chunk
	ctx         context.Context
	written     int64
	headersSent atomic.Bool
}

// NewSlowDripWriter creates a new slow-drip writer
func NewSlowDripWriter(w http.ResponseWriter, bytesPerMs, chunkSize int, ctx context.Context) *SlowDripWriter {
	if bytesPerMs <= 0 {
		bytesPerMs = 10 // Default: 10 bytes/ms = 10KB/s
	}
	if chunkSize <= 0 {
		chunkSize = 1 // Default: 1 byte chunks for maximum slowness
	}
	return &SlowDripWriter{
		w:          w,
		bytesPerMs: bytesPerMs,
		chunkSize:  chunkSize,
		ctx:        ctx,
	}
}

// Header returns the header map
func (sdw *SlowDripWriter) Header() http.Header {
	return sdw.w.Header()
}

// WriteHeader sends the HTTP response header with the provided status code
func (sdw *SlowDripWriter) WriteHeader(statusCode int) {
	if sdw.headersSent.CompareAndSwap(false, true) {
		sdw.w.WriteHeader(statusCode)
	}
}

// Write writes data with delays between chunks
func (sdw *SlowDripWriter) Write(p []byte) (int, error) {
	// Ensure headers are sent
	if !sdw.headersSent.Load() {
		sdw.WriteHeader(http.StatusOK)
	}

	written := 0
	for written < len(p) {
		// Check context cancellation
		select {
		case <-sdw.ctx.Done():
			return written, sdw.ctx.Err()
		default:
		}

		// Calculate chunk to write
		end := written + sdw.chunkSize
		if end > len(p) {
			end = len(p)
		}
		chunk := p[written:end]

		// Write chunk
		n, err := sdw.w.Write(chunk)
		written += n
		sdw.written += int64(n)

		if err != nil {
			return written, err
		}

		// Flush to ensure bytes are sent immediately
		if flusher, ok := sdw.w.(http.Flusher); ok {
			flusher.Flush()
		}

		// Calculate delay based on bytes written
		// bytesPerMs means we can write bytesPerMs bytes per millisecond
		// So for len(chunk) bytes, we wait (len(chunk) / bytesPerMs) milliseconds
		delayMs := len(chunk) / sdw.bytesPerMs
		if delayMs > 0 {
			select {
			case <-sdw.ctx.Done():
				return written, sdw.ctx.Err()
			case <-time.After(time.Duration(delayMs) * time.Millisecond):
			}
		}
	}

	return written, nil
}

// Flush implements http.Flusher
func (sdw *SlowDripWriter) Flush() {
	if flusher, ok := sdw.w.(http.Flusher); ok {
		flusher.Flush()
	}
}

// Hijack implements http.Hijacker
func (sdw *SlowDripWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if hijacker, ok := sdw.w.(http.Hijacker); ok {
		return hijacker.Hijack()
	}
	return nil, nil, errors.New("underlying ResponseWriter does not support hijacking")
}

// BytesWritten returns the total bytes written
func (sdw *SlowDripWriter) BytesWritten() int64 {
	return sdw.written
}

// ConnectionDropWriter wraps http.ResponseWriter to drop connection mid-response.
// This simulates network failures, connection resets, or abrupt disconnections.
type ConnectionDropWriter struct {
	w                http.ResponseWriter
	dropAfterPercent float64       // Drop after this percentage of expected body
	dropAfterBytes   int64         // Drop after this many bytes (takes precedence)
	expectedSize     int64         // Expected total response size
	bytesWritten     int64
	dropped          atomic.Bool
	headersSent      atomic.Bool
}

// NewConnectionDropWriter creates a new connection drop writer
func NewConnectionDropWriter(w http.ResponseWriter, dropAfterPercent float64, dropAfterBytes int64, expectedSize int64) *ConnectionDropWriter {
	cdw := &ConnectionDropWriter{
		w:                w,
		dropAfterPercent: dropAfterPercent,
		dropAfterBytes:   dropAfterBytes,
		expectedSize:     expectedSize,
	}

	// Calculate drop point from percentage if bytes not specified
	if dropAfterBytes <= 0 && dropAfterPercent > 0 && expectedSize > 0 {
		cdw.dropAfterBytes = int64(float64(expectedSize) * dropAfterPercent)
	}

	// Default: drop after 50% if nothing specified
	if cdw.dropAfterBytes <= 0 && expectedSize > 0 {
		cdw.dropAfterBytes = expectedSize / 2
	}

	return cdw
}

// Header returns the header map
func (cdw *ConnectionDropWriter) Header() http.Header {
	return cdw.w.Header()
}

// WriteHeader sends the HTTP response header
func (cdw *ConnectionDropWriter) WriteHeader(statusCode int) {
	if cdw.headersSent.CompareAndSwap(false, true) {
		cdw.w.WriteHeader(statusCode)
	}
}

// Write writes data and may drop the connection mid-write
func (cdw *ConnectionDropWriter) Write(p []byte) (int, error) {
	if cdw.dropped.Load() {
		return 0, errors.New("chaos: connection dropped")
	}

	// Ensure headers are sent
	if !cdw.headersSent.Load() {
		cdw.WriteHeader(http.StatusOK)
	}

	// Check if we should drop before writing
	remaining := cdw.dropAfterBytes - cdw.bytesWritten
	if remaining <= 0 && cdw.dropAfterBytes > 0 {
		cdw.drop()
		return 0, errors.New("chaos: connection dropped")
	}

	// Check if we need to write partial data before dropping
	if cdw.dropAfterBytes > 0 && int64(len(p)) > remaining {
		// Write partial data
		n, _ := cdw.w.Write(p[:remaining])
		cdw.bytesWritten += int64(n)

		// Flush before dropping
		if flusher, ok := cdw.w.(http.Flusher); ok {
			flusher.Flush()
		}

		// Drop connection
		cdw.drop()
		return n, errors.New("chaos: connection dropped")
	}

	// Normal write
	n, err := cdw.w.Write(p)
	cdw.bytesWritten += int64(n)
	return n, err
}

// drop closes the underlying connection
func (cdw *ConnectionDropWriter) drop() {
	if !cdw.dropped.CompareAndSwap(false, true) {
		return // Already dropped
	}

	// Use Hijacker to get raw connection and close it
	if hijacker, ok := cdw.w.(http.Hijacker); ok {
		conn, _, err := hijacker.Hijack()
		if err == nil && conn != nil {
			conn.Close()
		}
	}
}

// Flush implements http.Flusher
func (cdw *ConnectionDropWriter) Flush() {
	if !cdw.dropped.Load() {
		if flusher, ok := cdw.w.(http.Flusher); ok {
			flusher.Flush()
		}
	}
}

// Hijack implements http.Hijacker
func (cdw *ConnectionDropWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if hijacker, ok := cdw.w.(http.Hijacker); ok {
		return hijacker.Hijack()
	}
	return nil, nil, errors.New("underlying ResponseWriter does not support hijacking")
}

// IsDropped returns whether the connection has been dropped
func (cdw *ConnectionDropWriter) IsDropped() bool {
	return cdw.dropped.Load()
}

// BytesWritten returns total bytes written
func (cdw *ConnectionDropWriter) BytesWritten() int64 {
	return cdw.bytesWritten
}

// TruncationWriter wraps http.ResponseWriter to truncate response body.
// This simulates partial responses, incomplete downloads, or data loss.
type TruncationWriter struct {
	w               http.ResponseWriter
	truncatePercent float64       // Keep this percentage of body
	maxBytes        int64         // Maximum bytes to write (calculated from percent)
	bytesWritten    int64
	truncated       atomic.Bool
	headersSent     atomic.Bool
}

// NewTruncationWriter creates a new truncation writer
func NewTruncationWriter(w http.ResponseWriter, truncatePercent float64, expectedSize int64) *TruncationWriter {
	tw := &TruncationWriter{
		w:               w,
		truncatePercent: truncatePercent,
	}

	// Calculate max bytes from percentage
	if truncatePercent > 0 && truncatePercent < 1.0 && expectedSize > 0 {
		tw.maxBytes = int64(float64(expectedSize) * truncatePercent)
	}

	return tw
}

// Header returns the header map
func (tw *TruncationWriter) Header() http.Header {
	return tw.w.Header()
}

// WriteHeader sends the HTTP response header
func (tw *TruncationWriter) WriteHeader(statusCode int) {
	if tw.headersSent.CompareAndSwap(false, true) {
		tw.w.WriteHeader(statusCode)
	}
}

// Write writes data up to the truncation limit
func (tw *TruncationWriter) Write(p []byte) (int, error) {
	if tw.truncated.Load() {
		// Silently discard data after truncation
		return len(p), nil
	}

	// Ensure headers are sent
	if !tw.headersSent.Load() {
		tw.WriteHeader(http.StatusOK)
	}

	// Check if we've hit the limit
	if tw.maxBytes > 0 {
		remaining := tw.maxBytes - tw.bytesWritten
		if remaining <= 0 {
			tw.truncated.Store(true)
			return len(p), nil // Pretend we wrote it all
		}

		if int64(len(p)) > remaining {
			// Truncate this write
			p = p[:remaining]
			tw.truncated.Store(true)
		}
	}

	n, err := tw.w.Write(p)
	tw.bytesWritten += int64(n)
	return n, err
}

// Flush implements http.Flusher
func (tw *TruncationWriter) Flush() {
	if flusher, ok := tw.w.(http.Flusher); ok {
		flusher.Flush()
	}
}

// Hijack implements http.Hijacker
func (tw *TruncationWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if hijacker, ok := tw.w.(http.Hijacker); ok {
		return hijacker.Hijack()
	}
	return nil, nil, errors.New("underlying ResponseWriter does not support hijacking")
}

// IsTruncated returns whether the response was truncated
func (tw *TruncationWriter) IsTruncated() bool {
	return tw.truncated.Load()
}

// BytesWritten returns total bytes written
func (tw *TruncationWriter) BytesWritten() int64 {
	return tw.bytesWritten
}

// MaxBytes returns the maximum bytes limit
func (tw *TruncationWriter) MaxBytes() int64 {
	return tw.maxBytes
}

// ErrorInjectionWriter wraps http.ResponseWriter to inject HTTP errors.
// Instead of proxying the actual response, it returns an error response.
type ErrorInjectionWriter struct {
	w           http.ResponseWriter
	statusCode  int
	message     string
	injected    atomic.Bool
	headersSent atomic.Bool
}

// NewErrorInjectionWriter creates a new error injection writer
func NewErrorInjectionWriter(w http.ResponseWriter, statusCode int, message string) *ErrorInjectionWriter {
	if message == "" {
		message = http.StatusText(statusCode)
	}
	return &ErrorInjectionWriter{
		w:          w,
		statusCode: statusCode,
		message:    message,
	}
}

// Header returns the header map
func (ew *ErrorInjectionWriter) Header() http.Header {
	return ew.w.Header()
}

// WriteHeader ignores the status code and writes the error status
func (ew *ErrorInjectionWriter) WriteHeader(statusCode int) {
	if ew.headersSent.CompareAndSwap(false, true) {
		// Override with our error status
		ew.w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		ew.w.Header().Set("X-Chaos-Injected", "true")
		ew.w.WriteHeader(ew.statusCode)
	}
}

// Write discards the actual response and writes the error message
func (ew *ErrorInjectionWriter) Write(p []byte) (int, error) {
	if ew.injected.CompareAndSwap(false, true) {
		// First write - send our error message instead
		if !ew.headersSent.Load() {
			ew.WriteHeader(ew.statusCode)
		}
		ew.w.Write([]byte(ew.message))
	}
	// Pretend we wrote it all
	return len(p), nil
}

// Flush implements http.Flusher
func (ew *ErrorInjectionWriter) Flush() {
	if flusher, ok := ew.w.(http.Flusher); ok {
		flusher.Flush()
	}
}

// Hijack implements http.Hijacker
func (ew *ErrorInjectionWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if hijacker, ok := ew.w.(http.Hijacker); ok {
		return hijacker.Hijack()
	}
	return nil, nil, errors.New("underlying ResponseWriter does not support hijacking")
}

// StatusCode returns the injected status code
func (ew *ErrorInjectionWriter) StatusCode() int {
	return ew.statusCode
}
