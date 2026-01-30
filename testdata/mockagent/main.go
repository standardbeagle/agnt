// Package main provides a mock AI agent for testing agnt's PTY overlay system.
//
// It simulates Claude Code's Ink-based terminal UI behavior:
// - Displays a prompt and accepts text input
// - Produces output when "processing" a message
// - Configurable response delays and behaviors
// - Can be controlled via environment variables for test scenarios
//
// Usage:
//
//	go run ./testdata/mockagent
//	MOCK_DELAY=500 go run ./testdata/mockagent  # 500ms response delay
//	MOCK_ECHO=true go run ./testdata/mockagent  # Echo input back immediately
package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
)

var (
	responseDelay = flag.Int("delay", 100, "Response delay in milliseconds")
	echoMode      = flag.Bool("echo", false, "Echo input back immediately")
	promptStr     = flag.String("prompt", "> ", "Prompt string")
	verbose       = flag.Bool("verbose", false, "Enable verbose logging to stderr")
	outputLines   = flag.Int("output-lines", 3, "Number of output lines per response")
	exitOnEmpty   = flag.Bool("exit-on-empty", false, "Exit on empty input")
)

func main() {
	flag.Parse()

	// Override from environment
	if delay := os.Getenv("MOCK_DELAY"); delay != "" {
		if d, err := strconv.Atoi(delay); err == nil {
			*responseDelay = d
		}
	}
	if echo := os.Getenv("MOCK_ECHO"); echo == "true" || echo == "1" {
		*echoMode = true
	}
	if prompt := os.Getenv("MOCK_PROMPT"); prompt != "" {
		*promptStr = prompt
	}
	if lines := os.Getenv("MOCK_OUTPUT_LINES"); lines != "" {
		if l, err := strconv.Atoi(lines); err == nil {
			*outputLines = l
		}
	}

	logf := func(format string, args ...interface{}) {
		if *verbose {
			fmt.Fprintf(os.Stderr, "[mockagent] "+format+"\n", args...)
		}
	}

	logf("Starting mock agent (delay=%dms, echo=%v)", *responseDelay, *echoMode)

	// Handle signals gracefully
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigChan
		logf("Received signal: %v", sig)
		fmt.Println("\nGoodbye!")
		os.Exit(0)
	}()

	// Print welcome message
	fmt.Println("Mock Agent v1.0")
	fmt.Println("Simulates Claude Code's terminal UI for testing")
	fmt.Println("Type 'help' for commands, 'exit' to quit")
	fmt.Println()

	reader := bufio.NewReader(os.Stdin)
	messageCount := 0

	for {
		// Print prompt
		fmt.Print(*promptStr)

		// Read input line
		input, err := reader.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				logf("EOF received")
				fmt.Println("\nEOF - exiting")
				break
			}
			logf("Read error: %v", err)
			continue
		}

		input = strings.TrimSpace(input)
		logf("Received input: %q", input)

		if input == "" {
			if *exitOnEmpty {
				logf("Empty input, exiting")
				break
			}
			continue
		}

		messageCount++

		// Handle commands
		switch strings.ToLower(input) {
		case "exit", "quit", "q":
			fmt.Println("Goodbye!")
			return

		case "help", "?":
			printHelp()
			continue

		case "status":
			fmt.Printf("Messages processed: %d\n", messageCount)
			fmt.Printf("Response delay: %dms\n", *responseDelay)
			fmt.Printf("Echo mode: %v\n", *echoMode)
			continue

		case "slow":
			*responseDelay = 2000
			fmt.Println("Response delay set to 2000ms")
			continue

		case "fast":
			*responseDelay = 50
			fmt.Println("Response delay set to 50ms")
			continue

		case "busy":
			// Simulate busy agent producing lots of output
			fmt.Println("Simulating busy agent...")
			for i := 0; i < 10; i++ {
				time.Sleep(100 * time.Millisecond)
				fmt.Printf("Working... %d%%\n", (i+1)*10)
			}
			fmt.Println("Done!")
			continue

		case "error":
			// Simulate an error response
			fmt.Println("Error: Something went wrong!")
			fmt.Println("This is a simulated error for testing.")
			continue
		}

		// Process the message
		processMessage(input, messageCount, logf)
	}
}

func processMessage(input string, count int, logf func(string, ...interface{})) {
	// Simulate processing delay
	if *responseDelay > 0 {
		logf("Delaying %dms before response", *responseDelay)
		time.Sleep(time.Duration(*responseDelay) * time.Millisecond)
	}

	if *echoMode {
		// Simple echo mode
		fmt.Printf("Echo: %s\n", input)
		return
	}

	// Generate a response
	fmt.Printf("\n[Message #%d received]\n", count)
	fmt.Printf("Input length: %d characters\n", len(input))

	// Generate multiple lines of output to test activity detection
	for i := 0; i < *outputLines; i++ {
		time.Sleep(50 * time.Millisecond) // Small delay between lines
		fmt.Printf("Processing line %d/%d...\n", i+1, *outputLines)
	}

	// Summary
	wordCount := len(strings.Fields(input))
	fmt.Printf("Processed %d words successfully.\n\n", wordCount)
}

func printHelp() {
	fmt.Println(`
Available commands:
  help, ?     - Show this help
  status      - Show current status
  slow        - Set response delay to 2000ms
  fast        - Set response delay to 50ms
  busy        - Simulate busy agent with lots of output
  error       - Simulate an error response
  exit, quit  - Exit the agent

Environment variables:
  MOCK_DELAY=<ms>       - Response delay in milliseconds
  MOCK_ECHO=true        - Echo input back immediately
  MOCK_PROMPT=<str>     - Custom prompt string
  MOCK_OUTPUT_LINES=<n> - Number of output lines per response

Any other input is processed as a message.
`)
}
