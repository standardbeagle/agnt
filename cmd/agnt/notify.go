package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/standardbeagle/agnt/internal/daemonclient"

	"github.com/spf13/cobra"
	"golang.org/x/term"
)

var notifyCmd = &cobra.Command{
	Use:   "notify",
	Short: "Send a notification to all active browser proxies (alias for `agnt hook notification`)",
	Long: `Send a toast notification that will be displayed in the browser's floating indicator.

This is a thin alias for ` + "`agnt hook notification`" + `: it dispatches a hook event
into the daemon's ring buffer, and the daemon's drain goroutine fans the event
out to every active proxy as a browser toast. Both ` + "`agnt notify`" + ` and
` + "`agnt hook notification`" + ` produce identical browser behavior.

Phase 3 of the hook dispatcher consolidation collapsed the legacy per-proxy
ProxyToast loop into the daemon-side drain. This subcommand is preserved as a
back-compat alias for hook scripts that already shell out to ` + "`agnt notify`" + `.`,
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
	// non-interactive callers never see it.
	if term.IsTerminal(int(os.Stderr.Fd())) {
		fmt.Fprintln(os.Stderr,
			"agnt notify is deprecated; use 'agnt hook notification' "+
				"(this warning only appears on interactive TTYs and will not break hook scripts)")
	}

	socketPath := getSocketPath(cmd)

	// Phase 3: notify is now a pure HookSend alias. The daemon's drain
	// goroutine handles the per-proxy BroadcastToast loop on the
	// notification event path, so this command no longer needs to
	// enumerate proxies client-side. See drainHooks → fanOutHookEvent →
	// broadcastNotificationToast in internal/daemon/hub_hook.go.
	//
	// All errors are silently swallowed (exit 0):
	//   - daemon down → hook scripts must never break the agent loop
	//   - 50ms deadline exceeded → daemon is wedged, surfacing the
	//     timeout to the calling shell would just spam Claude's output
	//   - any other error → same reasoning
	exitCode := dispatchNotifyHook(socketPath, notifyType, notifyTitle, notifyMessage)
	os.Exit(exitCode)
}

// dispatchNotifyHook is the pure-Go core of runNotify: it builds the
// notification payload, opens a short-lived daemon client, and fires a
// HookSend with a hard 50ms budget. Returns the desired process exit
// code. Split out from runNotify so unit tests can drive every branch
// without calling os.Exit.
//
// Contract: ANY failure returns exit 0. Hook scripts are fire-and-forget
// signals from the agent's perspective; surfacing transient daemon
// issues to the caller would either break the agent loop or pollute
// Claude's transcript with noise. The only non-zero exit path is the
// arg validation failure in runNotify above (--message required).
func dispatchNotifyHook(socketPath, kind, title, message string) int {
	type notifyBody struct {
		Type    string `json:"type"`
		Title   string `json:"title,omitempty"`
		Message string `json:"message"`
	}
	body, err := json.Marshal(notifyBody{Type: kind, Title: title, Message: message})
	if err != nil {
		// Marshal of a fixed-shape struct of strings should never fail.
		// If it somehow does, treat it as a hook-broken condition and
		// exit 0 — fail-quiet is the contract.
		return 0
	}

	client := daemonclient.NewClient(daemonclient.WithSocketPath(socketPath))
	if err := client.Connect(); err != nil {
		// Daemon not running — silent exit 0 keeps hook scripts fast
		// and prevents broken pipes from cascading into agent failures.
		return 0
	}
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	if err := client.HookSend(ctx, "notification", json.RawMessage(body), nil); err != nil {
		// Both ErrHookDaemonDown and ErrHookDeadline are explicitly
		// swallowed. Any other error from HookSend (marshal, internal
		// daemon error) is also swallowed by design — this command
		// must never fail loudly.
		switch {
		case errors.Is(err, daemonclient.ErrHookDaemonDown):
		case errors.Is(err, daemonclient.ErrHookDeadline):
		default:
		}
		return 0
	}
	return 0
}
