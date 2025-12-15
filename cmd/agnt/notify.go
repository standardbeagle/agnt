package main

import (
	"fmt"
	"os"

	"github.com/standardbeagle/agnt/internal/daemon"
	"github.com/standardbeagle/agnt/internal/protocol"

	"github.com/spf13/cobra"
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

	socketPath := getSocketPath(cmd)

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
