package daemon

import (
	"testing"
)

func TestExtractPortFromCommand(t *testing.T) {
	tests := []struct {
		name     string
		command  string
		args     []string
		expected int
	}{
		{"next dev with -p", "npm", []string{"run", "dev"}, 0}, // No port in args
		{"next dev with -p 3000", "next", []string{"dev", "-p", "3000"}, 3000},
		{"vite with --port", "vite", []string{"--port", "5173"}, 5173},
		{"vite with --port=", "vite", []string{"--port=5173"}, 5173},
		{"npm run with port in args", "npm", []string{"run", "dev", "--", "-p", "4000"}, 4000},
		{"go run", "go", []string{"run", "main.go"}, 0},
		{"localhost:port pattern", "node", []string{"server.js", "--host", "localhost:8080"}, 8080},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractPortFromCommand(tt.command, tt.args)
			if got != tt.expected {
				t.Errorf("extractPortFromCommand(%q, %v) = %d, want %d", tt.command, tt.args, got, tt.expected)
			}
		})
	}
}

func TestExtractPortFromPackageJsonScript(t *testing.T) {
	tests := []struct {
		name     string
		script   string
		expected int
	}{
		{"next dev -p 3465", "next dev -p 3465", 3465},
		{"next dev -p 3000", "next dev -p 3000", 3000},
		{"vite --port 5173", "vite --port 5173", 5173},
		{"vite --port=8080", "vite --port=8080", 8080},
		{"just next dev", "next dev", 0},
		{"PORT=3000 node", "PORT=3000 node server.js", 3000},
		{"webpack dev server", "webpack serve --port 9000", 9000},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractPortFromCommand(tt.script, nil)
			if got != tt.expected {
				t.Errorf("extractPortFromCommand(%q, nil) = %d, want %d", tt.script, got, tt.expected)
			}
		})
	}
}

func TestDetectEADDRINUSE(t *testing.T) {
	tests := []struct {
		name     string
		output   string
		expected int
	}{
		{
			"Next.js EADDRINUSE",
			`Error: listen EADDRINUSE: address already in use :::3465`,
			3465,
		},
		{
			"Node.js EADDRINUSE",
			`Error: listen EADDRINUSE: address already in use 127.0.0.1:3000`,
			3000,
		},
		{
			"Generic address in use",
			`Failed to start server\nError: address already in use :8080`,
			8080,
		},
		{
			"Port already in use message",
			`Error: port 4000 is already in use`,
			4000,
		},
		{
			"No error",
			`Server started successfully on port 3000`,
			0,
		},
		{
			"Empty",
			"",
			0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := detectEADDRINUSE(tt.output)
			if got != tt.expected {
				t.Errorf("detectEADDRINUSE(%q) = %d, want %d", tt.output, got, tt.expected)
			}
		})
	}
}
