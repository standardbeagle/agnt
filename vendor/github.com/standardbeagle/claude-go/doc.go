// Package claude provides a Go SDK for automating Claude Code CLI
// with concurrent session management, real-time streaming, custom tools,
// and hooks. This is the Go equivalent of the Python Claude Agent SDK.
//
// This library enables Go applications to interact with Claude Code CLI
// programmatically, supporting both single queries and multi-turn conversations
// with proper session management, file operations, and concurrent execution.
//
// # Quick Start - Simple Query
//
// For one-shot queries without session management:
//
//	messages, err := claude.Query(ctx, "What is 2+2?", nil)
//	if err != nil {
//		log.Fatal(err)
//	}
//	for _, msg := range messages {
//		if text := claude.GetText(msg); text != "" {
//			fmt.Println(text)
//		}
//	}
//
// # Client-Based Usage
//
//	client := claude.New(&claude.AgentOptions{
//		PermissionMode: claude.PermissionModeBypassPermission,
//		Interactive:    false, // Use -p mode for simple queries
//	})
//	defer client.Close()
//
//	resp, err := client.Query(ctx, &claude.QueryRequest{
//		Prompt: "Write a hello world function in Go",
//	})
//	if err != nil {
//		log.Fatal(err)
//	}
//
//	for msg := range resp.Messages {
//		fmt.Printf("%s: %s\n", msg.Type, msg.Content)
//	}
//
// # Multi-turn Conversations
//
//	client := claude.New(&claude.AgentOptions{
//		PermissionMode: claude.PermissionModeBypassPermission,
//		Interactive:    true, // Enable session persistence
//	})
//
//	resp, err := client.Query(ctx, &claude.QueryRequest{
//		Prompt: "My name is Alice. Remember this.",
//	})
//
//	session, _ := client.GetSession(resp.SessionID)
//	session.SendMessage("What is my name?") // Claude will remember "Alice"
//
// # Concurrent Sessions
//
//	var wg sync.WaitGroup
//	for _, prompt := range prompts {
//		wg.Add(1)
//		go func(p string) {
//			defer wg.Done()
//			resp, _ := client.Query(ctx, &claude.QueryRequest{Prompt: p})
//			// Process response...
//		}(prompt)
//	}
//	wg.Wait()
//
// # Custom Tools (In-Process MCP Servers)
//
// Define custom tools that run directly in your Go application:
//
//	// Create a tool using the builder pattern
//	addTool := claude.Tool("add", "Add two numbers").
//		Param("a", "number", "First number").
//		Param("b", "number", "Second number").
//		Required("a", "b").
//		HandlerFunc(func(ctx context.Context, args map[string]interface{}) (string, error) {
//			a := args["a"].(float64)
//			b := args["b"].(float64)
//			return fmt.Sprintf("%g", a+b), nil
//		})
//
//	// Create an SDK MCP server with your tools
//	server := claude.CreateSDKMCPServer("calculator", "1.0.0", addTool)
//
//	// Register with client
//	client := claude.New(&claude.AgentOptions{
//		PermissionMode: claude.PermissionModeBypassPermission,
//		AllowedTools:   []string{"mcp__calculator__add"},
//	})
//	client.RegisterSDKMCPServer("calculator", server)
//
// # Hooks
//
// Intercept and control tool execution with hooks:
//
//	// Create a hook to block dangerous commands
//	bashBlocker := func(ctx context.Context, input claude.HookInput, toolUseID string, hookCtx claude.HookContext) (*claude.HookOutput, error) {
//		if preInput, ok := input.(claude.PreToolUseInput); ok {
//			if cmd, ok := preInput.ToolInput["command"].(string); ok {
//				if strings.Contains(cmd, "rm -rf") {
//					return &claude.HookOutput{
//						PermissionDecision: claude.PermissionBehaviorDeny,
//						PermissionDecisionReason: "Dangerous command blocked",
//					}, nil
//				}
//			}
//		}
//		return nil, nil
//	}
//
//	registry := claude.NewHookRegistry()
//	registry.Register(claude.HookEventPreToolUse, []claude.HookMatcher{
//		{Matcher: "Bash", Hooks: []claude.HookCallback{bashBlocker}},
//	})
//
//	client := claude.New(&claude.AgentOptions{
//		Hooks: registry,
//	})
//
// # Stream-JSON Format
//
// Claude Code's --output-format stream-json produces JSONL where assistant
// and user messages wrap content in a nested "message" key:
//
//	{"type":"assistant","message":{"content":[{"type":"text","text":"Hello!"}]}}
//
// ParseMessage handles both this nested format and flat content automatically.
//
// # Structured Message Types
//
// The SDK provides typed messages for parsing CLI JSON output:
//
//	msg, err := claude.ParseMessage(jsonData)
//
//	switch m := msg.(type) {
//	case claude.AssistantMessage:
//		for _, block := range m.Content {
//			switch b := block.(type) {
//			case claude.TextBlock:
//				fmt.Println(b.Text)
//			case claude.ToolUseBlock:
//				fmt.Printf("Tool: %s\n", b.Name)
//			case claude.ThinkingBlock:
//				// Extended thinking
//			case claude.ToolResultBlock:
//				// Tool execution result
//			}
//		}
//	case claude.ResultMessage:
//		fmt.Printf("Cost: $%.4f\n", m.TotalCostUSD)
//		fmt.Println(m.Result) // Complete response text
//	}
//
// # File Operations
//
//	client := claude.New(&claude.AgentOptions{
//		PermissionMode:   claude.PermissionModeBypassPermission,
//		Interactive:      true,
//		WorkingDirectory: "/path/to/project",
//		AddDirectories:   []string{"/path/to/project"},
//	})
//
//	resp, _ := client.Query(ctx, &claude.QueryRequest{
//		Prompt: "Create a file called main.go with a hello world program",
//	})
//
// # Configuration Options (AgentOptions)
//
// The AgentOptions struct provides extensive configuration matching
// the Python SDK's ClaudeAgentOptions:
//
//   - Model: Specify Claude model ("sonnet", "opus", etc.)
//   - MaxTurns: Maximum conversation turns
//   - MaxThinkingTokens: Maximum tokens for thinking
//   - MaxBudgetUSD: Maximum cost budget
//   - SystemPrompt: Custom system prompt
//   - Interactive: Enable persistent sessions vs single queries
//   - PermissionMode: Control Claude's permission level
//   - WorkingDirectory: Set working directory for Claude process
//   - AddDirectories: Grant access to additional directories
//   - AllowedTools/DisallowedTools: Control tool usage
//   - MCPServers: Configure MCP servers
//   - Hooks: Register hook callbacks
//   - Sandbox: Configure sandbox settings
//   - Resume/ContinueConversation: Session continuation
//   - Environment: Set environment variables for Claude process
//   - CLIPath: Custom path to Claude CLI
//
// # Error Handling
//
// The SDK provides typed errors for precise error handling:
//
//	resp, err := client.Query(ctx, req)
//	if err != nil {
//		switch {
//		case claude.IsCLINotFound(err):
//			log.Fatal("Install Claude CLI: npm install -g @anthropic-ai/claude-code")
//		case claude.IsProcessError(err):
//			log.Fatal("CLI process failed")
//		default:
//			log.Fatal(err)
//		}
//	}
//
//	// Runtime errors
//	for err := range resp.Errors {
//		log.Printf("Claude error: %v", err)
//	}
//
// Error types include:
//   - ClaudeSDKError: Base error type
//   - CLINotFoundError: Claude CLI not installed
//   - CLIConnectionError: Connection issues
//   - ProcessError: CLI process failures
//   - CLIJSONDecodeError: JSON parsing errors
//   - MessageParseError: Message parsing errors
//   - SessionClosedError: Using a closed session
//   - ToolError: Tool execution failures
//   - HookError: Hook execution failures
//
// # CLI Discovery
//
// The SDK automatically finds the Claude CLI in this order:
//  1. CLAUDE_CLI_PATH environment variable
//  2. Current working directory
//  3. System PATH
//  4. Common installation locations (npm, yarn, homebrew)
//
// # Thread Safety
//
// The library is designed to be thread-safe:
//   - Multiple goroutines can create sessions concurrently
//   - Sessions can be safely accessed from multiple goroutines
//   - Proper cleanup is handled automatically with context cancellation
//   - Client.Close() safely shuts down all sessions
//
// # Requirements
//
//   - Go 1.21 or later
//   - Claude CLI installed and accessible in PATH
//   - Valid Claude authentication (API key or subscription)
//
// # Authentication
//
// The library uses Claude CLI's existing authentication. Ensure Claude CLI
// is properly authenticated before using this library:
//
//	claude setup-token
//
// Or set environment variables:
//
//	export ANTHROPIC_API_KEY=your-api-key
package claude
