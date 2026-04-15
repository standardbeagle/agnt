package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/standardbeagle/agnt/internal/daemon"
	"github.com/standardbeagle/agnt/internal/protocol"

	"github.com/spf13/cobra"
	"golang.org/x/term"
)

var notifyCmd = &cobra.Command{
	Use:   "notify",
	Short: "Send a notification to all active browser proxies",
	Long: `Send a toast notification that will be displayed in the browser's floating indicator.

This is typically called by hook scripts to notify the browser of Claude's actions.`,
	Run: runNotify,
}

var (
	notifyType    string
	notifyTitle   string
	notifyMessage string
)

func init() {
	notifyCmd.Flags().StringVar(&notifyType, "type", "info", "Notification type (success, error, warning, info)")
	notifyCmd.Flags().StringVar(&notifyTitle, "title", "", "Notification title")
	notifyCmd.Flags().StringVar(&notifyMessage, "message", "", "Notification message")

	rootCmd.AddCommand(notifyCmd)
}

func runNotify(cmd *cobra.Command, args []string) {
	if notifyMessage == "" {
		fmt.Fprintln(os.Stderr, "Error: --message required")
		os.Exit(1)
	}

	// Deprecation warning on interactive stderr only. Hook scripts pipe
	// stderr to the void (or Claude's transcript) — we must not break
	// them by printing warnings to a non-TTY target. This is gated by a
	// strict TTY check, not by -q/--quiet, so CI logs stay clean and
	// non-interactive callers never see it. Phase 3 moves notify into a
	// pure hook alias; until then both paths fire.
	if term.IsTerminal(int(os.Stderr.Fd())) {
		fmt.Fprintln(os.Stderr,
			"agnt notify is deprecated; use 'agnt hook notification' "+
				"(this warning only appears on interactive TTYs and will not break hook scripts)")
	}

	socketPath := getSocketPath(cmd)

	// Fire-and-forget hook dispatch. Phase 3 will use this to fan out
	// notification events through the drain goroutine, so even if
	// the legacy ProxyToast path below goes away the event shape is
	// already flowing. Errors are intentionally swallowed: this call
	// must never break the existing notify behavior, and both paths
	// are optional best-effort signals from the agent's point of view.
	sendNotifyHook(socketPath, notifyType, notifyTitle, notifyMessage)

	client := daemon.NewClient(daemon.WithSocketPath(socketPath))
	if err := client.Connect(); err != nil {
		// Daemon not running - silently exit (don't block hooks)
		os.Exit(0)
	}
	defer client.Close()

	// Get list of all proxies
	dirFilter := protocol.DirectoryFilter{Global: true}
	result, err := client.ProxyList(dirFilter)
	if err != nil {
		os.Exit(0) // Silently fail
	}

	proxies, ok := result["proxies"].([]interface{})
	if !ok || len(proxies) == 0 {
		// No proxies running
		os.Exit(0)
	}

	// Build toast config
	toast := protocol.ToastConfig{
		Type:    notifyType,
		Title:   notifyTitle,
		Message: notifyMessage,
	}

	// Send toast to each proxy
	for _, p := range proxies {
		proxyMap, ok := p.(map[string]interface{})
		if !ok {
			continue
		}
		id, ok := proxyMap["id"].(string)
		if !ok {
			continue
		}

		_, _ = client.ProxyToast(id, toast)
	}
}

// sendNotifyHook fires a best-effort hook event for the notify payload.
// It opens a dedicated short-lived daemon client and uses the hot-path
// HookSend with a 50ms hard budget. Every error is swallowed — this is
// fire-and-forget by contract, because the legacy ProxyToast path is
// still the primary delivery channel until phase 3.
//
// The event name "notification" matches the Claude Code hook nomenclature
// and is what phase 3 will route to toast fan-out from inside the drain
// goroutine.
func sendNotifyHook(socketPath, kind, title, message string) {
	type notifyBody struct {
		Type    string `json:"type"`
		Title   string `json:"title,omitempty"`
		Message string `json:"message"`
	}
	body, err := json.Marshal(notifyBody{Type: kind, Title: title, Message: message})
	if err != nil {
		return
	}
	client := daemon.NewClient(daemon.WithSocketPath(socketPath))
	if err := client.Connect(); err != nil {
		return
	}
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_ = client.HookSend(ctx, "notification", json.RawMessage(body), nil)
}
