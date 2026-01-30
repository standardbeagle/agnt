// Package main provides a test web server for integration testing.
// It serves static files and provides configurable endpoints for testing
// various scenarios like errors, delays, and WebSocket connections.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

var (
	port      = flag.Int("port", 18080, "Port to listen on")
	staticDir = flag.String("static", "", "Static files directory (default: ../static relative to binary)")
	errorRate = flag.Float64("error-rate", 0, "Probability of returning 500 error (0-1)")
	delayMs   = flag.Int("delay", 0, "Response delay in milliseconds")
	verbose   = flag.Bool("verbose", false, "Enable verbose logging")
)

// RequestLog stores information about requests for verification in tests.
type RequestLog struct {
	mu       sync.RWMutex
	requests []RequestEntry
}

// RequestEntry represents a logged request.
type RequestEntry struct {
	Time       time.Time         `json:"time"`
	Method     string            `json:"method"`
	Path       string            `json:"path"`
	Headers    map[string]string `json:"headers"`
	RemoteAddr string            `json:"remote_addr"`
}

func (rl *RequestLog) Add(entry RequestEntry) {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	rl.requests = append(rl.requests, entry)
	// Keep only last 1000 requests
	if len(rl.requests) > 1000 {
		rl.requests = rl.requests[len(rl.requests)-1000:]
	}
}

func (rl *RequestLog) GetAll() []RequestEntry {
	rl.mu.RLock()
	defer rl.mu.RUnlock()
	result := make([]RequestEntry, len(rl.requests))
	copy(result, rl.requests)
	return result
}

func (rl *RequestLog) Clear() {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	rl.requests = nil
}

var requestLog = &RequestLog{}

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true // Allow all origins for testing
	},
}

func main() {
	flag.Parse()

	// Determine static directory
	static := *staticDir
	if static == "" {
		// Default to ../static relative to this file's directory
		exe, err := os.Executable()
		if err == nil {
			static = filepath.Join(filepath.Dir(exe), "..", "static")
		} else {
			static = "./testdata/webapps/static"
		}
	}

	// Verify static directory exists
	if _, err := os.Stat(static); os.IsNotExist(err) {
		// Try relative to working directory
		cwd, _ := os.Getwd()
		static = filepath.Join(cwd, "testdata", "webapps", "static")
		if _, err := os.Stat(static); os.IsNotExist(err) {
			log.Fatalf("Static directory not found: %s", static)
		}
	}

	mux := http.NewServeMux()

	// Logging middleware
	logMiddleware := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			entry := RequestEntry{
				Time:       time.Now(),
				Method:     r.Method,
				Path:       r.URL.Path,
				Headers:    make(map[string]string),
				RemoteAddr: r.RemoteAddr,
			}
			for k, v := range r.Header {
				if len(v) > 0 {
					entry.Headers[k] = v[0]
				}
			}
			requestLog.Add(entry)

			if *verbose {
				log.Printf("%s %s from %s", r.Method, r.URL.Path, r.RemoteAddr)
			}

			// Apply delay if configured
			if *delayMs > 0 {
				time.Sleep(time.Duration(*delayMs) * time.Millisecond)
			}

			next.ServeHTTP(w, r)
		})
	}

	// Static file server
	fileServer := http.FileServer(http.Dir(static))
	mux.Handle("/", logMiddleware(fileServer))

	// API endpoints for testing
	mux.HandleFunc("/api/echo", handleEcho)
	mux.HandleFunc("/api/error", handleError)
	mux.HandleFunc("/api/delay", handleDelay)
	mux.HandleFunc("/api/requests", handleRequests)
	mux.HandleFunc("/api/health", handleHealth)
	mux.HandleFunc("/ws", handleWebSocket)

	addr := fmt.Sprintf(":%d", *port)
	log.Printf("Test server starting on http://localhost%s", addr)
	log.Printf("Static files from: %s", static)
	if *errorRate > 0 {
		log.Printf("Error rate: %.1f%%", *errorRate*100)
	}
	if *delayMs > 0 {
		log.Printf("Response delay: %dms", *delayMs)
	}

	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatalf("Server failed: %v", err)
	}
}

// handleEcho returns the request body and headers as JSON.
func handleEcho(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	response := map[string]interface{}{
		"method":  r.Method,
		"path":    r.URL.Path,
		"query":   r.URL.Query(),
		"headers": r.Header,
	}

	json.NewEncoder(w).Encode(response)
}

// handleError returns configurable error responses.
func handleError(w http.ResponseWriter, r *http.Request) {
	code := 500
	if codeParam := r.URL.Query().Get("code"); codeParam != "" {
		fmt.Sscanf(codeParam, "%d", &code)
	}

	message := r.URL.Query().Get("message")
	if message == "" {
		message = "Test error response"
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]interface{}{
		"error":   true,
		"code":    code,
		"message": message,
	})
}

// handleDelay returns a response after a configurable delay.
func handleDelay(w http.ResponseWriter, r *http.Request) {
	delayMs := 1000
	if delayParam := r.URL.Query().Get("ms"); delayParam != "" {
		fmt.Sscanf(delayParam, "%d", &delayMs)
	}

	time.Sleep(time.Duration(delayMs) * time.Millisecond)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"delayed": true,
		"ms":      delayMs,
	})
}

// handleRequests returns the request log for test verification.
func handleRequests(w http.ResponseWriter, r *http.Request) {
	if r.Method == "DELETE" {
		requestLog.Clear()
		w.WriteHeader(http.StatusNoContent)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"requests": requestLog.GetAll(),
	})
}

// handleHealth returns a simple health check response.
func handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status": "ok",
		"time":   time.Now().Format(time.RFC3339),
	})
}

// handleWebSocket provides a WebSocket echo endpoint for testing.
func handleWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("WebSocket upgrade failed: %v", err)
		return
	}
	defer conn.Close()

	if *verbose {
		log.Printf("WebSocket connection established from %s", r.RemoteAddr)
	}

	for {
		messageType, message, err := conn.ReadMessage()
		if err != nil {
			if *verbose {
				log.Printf("WebSocket read error: %v", err)
			}
			break
		}

		if *verbose {
			log.Printf("WebSocket received: %s", string(message))
		}

		// Echo the message back
		if err := conn.WriteMessage(messageType, message); err != nil {
			if *verbose {
				log.Printf("WebSocket write error: %v", err)
			}
			break
		}
	}
}
