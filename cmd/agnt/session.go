package main

import (
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/standardbeagle/agnt/internal/daemon"
	"github.com/standardbeagle/agnt/internal/protocol"

	"github.com/spf13/cobra"
)

var sessionCmd = &cobra.Command{
	Use:   "session",
	Short: "Manage agnt run sessions and schedule messages",
	Long: `Manage agnt run sessions and schedule messages for AI agents.

Sessions are created automatically when 'agnt run' starts. You can send messages
directly to a session or schedule messages for future delivery.

Examples:
  agnt session list
  agnt session list --global
  agnt session send claude-1 "Check the test results"
  agnt session schedule claude-1 5m "Verify this completed"
  agnt session tasks
  agnt session cancel task-abc123`,
}

var sessionListCmd = &cobra.Command{
	Use:   "list",
	Short: "List active sessions",
	Run:   runSessionList,
}

var sessionSendCmd = &cobra.Command{
	Use:   "send <code> <message>",
	Short: "Send a message to a session immediately",
	Args:  cobra.ExactArgs(2),
	Run:   runSessionSend,
}

var sessionScheduleCmd = &cobra.Command{
	Use:   "schedule <code> <duration> <message>",
	Short: "Schedule a message for future delivery",
	Long: `Schedule a message for future delivery.

Duration format:
  - "5m" = 5 minutes
  - "1h" = 1 hour
  - "1h30m" = 1 hour 30 minutes
  - "30s" = 30 seconds

Example:
  agnt session schedule claude-1 5m "Verify this completed"`,
	Args: cobra.ExactArgs(3),
	Run:  runSessionSchedule,
}

var sessionTasksCmd = &cobra.Command{
	Use:   "tasks",
	Short: "List scheduled tasks",
	Run:   runSessionTasks,
}

var sessionCancelCmd = &cobra.Command{
	Use:   "cancel <task_id>",
	Short: "Cancel a scheduled task",
	Args:  cobra.ExactArgs(1),
	Run:   runSessionCancel,
}

func init() {
	sessionCmd.AddCommand(sessionListCmd)
	sessionCmd.AddCommand(sessionSendCmd)
	sessionCmd.AddCommand(sessionScheduleCmd)
	sessionCmd.AddCommand(sessionTasksCmd)
	sessionCmd.AddCommand(sessionCancelCmd)

	// Add --global flag to list and tasks commands
	sessionListCmd.Flags().Bool("global", false, "Include sessions from all directories")
	sessionTasksCmd.Flags().Bool("global", false, "Include tasks from all directories")
}

func getSessionClient(cmd *cobra.Command) (*daemon.Client, error) {
	socketPath := getSocketPath(cmd)
	client := daemon.NewClient(daemon.WithSocketPath(socketPath))
	if err := client.Connect(); err != nil {
		return nil, fmt.Errorf("daemon is not running: %v", err)
	}
	return client, nil
}

func runSessionList(cmd *cobra.Command, args []string) {
	client, err := getSessionClient(cmd)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
	defer client.Close()

	global, _ := cmd.Flags().GetBool("global")

	// Get current working directory
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to get working directory: %v\n", err)
		os.Exit(1)
	}

	dirFilter := protocol.DirectoryFilter{
		Directory: cwd,
		Global:    global,
	}

	result, err := client.SessionList(dirFilter)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to list sessions: %v\n", err)
		os.Exit(1)
	}

	sessions, ok := result["sessions"].([]interface{})
	if !ok || len(sessions) == 0 {
		if global {
			fmt.Println("No active sessions")
		} else {
			fmt.Printf("No active sessions in %s\n", cwd)
			fmt.Println("Use --global to see all sessions")
		}
		return
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "CODE\tCOMMAND\tSTATUS\tPROJECT\tSTARTED")

	for _, s := range sessions {
		if sm, ok := s.(map[string]interface{}); ok {
			code := getString(sm, "code")
			command := getString(sm, "command")
			status := getString(sm, "status")
			projectPath := getString(sm, "project_path")

			started := ""
			if ts, ok := sm["started_at"].(string); ok {
				if t, err := time.Parse(time.RFC3339, ts); err == nil {
					started = time.Since(t).Round(time.Second).String() + " ago"
				}
			}

			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", code, command, status, projectPath, started)
		}
	}
	w.Flush()
}

func runSessionSend(cmd *cobra.Command, args []string) {
	client, err := getSessionClient(cmd)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
	defer client.Close()

	code := args[0]
	message := args[1]

	result, err := client.SessionSend(code, message)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to send message: %v\n", err)
		os.Exit(1)
	}

	if getBool(result, "success") {
		fmt.Printf("Message sent to session %s\n", code)
	} else {
		fmt.Fprintf(os.Stderr, "Failed to send message: %s\n", getString(result, "message"))
		os.Exit(1)
	}
}

func runSessionSchedule(cmd *cobra.Command, args []string) {
	client, err := getSessionClient(cmd)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
	defer client.Close()

	code := args[0]
	duration := args[1]
	message := args[2]

	result, err := client.SessionSchedule(code, duration, message)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to schedule message: %v\n", err)
		os.Exit(1)
	}

	if getBool(result, "success") {
		taskID := getString(result, "task_id")
		deliverAt := ""
		if ts, ok := result["deliver_at"].(string); ok {
			if t, err := time.Parse(time.RFC3339, ts); err == nil {
				deliverAt = t.Format(time.RFC1123)
			}
		}
		fmt.Printf("Message scheduled for session %s\n", code)
		fmt.Printf("  Task ID: %s\n", taskID)
		fmt.Printf("  Delivery: %s\n", deliverAt)
	} else {
		fmt.Fprintf(os.Stderr, "Failed to schedule message: %s\n", getString(result, "message"))
		os.Exit(1)
	}
}

func runSessionTasks(cmd *cobra.Command, args []string) {
	client, err := getSessionClient(cmd)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
	defer client.Close()

	global, _ := cmd.Flags().GetBool("global")

	// Get current working directory
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to get working directory: %v\n", err)
		os.Exit(1)
	}

	dirFilter := protocol.DirectoryFilter{
		Directory: cwd,
		Global:    global,
	}

	result, err := client.SessionTasks(dirFilter)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to list tasks: %v\n", err)
		os.Exit(1)
	}

	tasks, ok := result["tasks"].([]interface{})
	if !ok || len(tasks) == 0 {
		if global {
			fmt.Println("No scheduled tasks")
		} else {
			fmt.Printf("No scheduled tasks in %s\n", cwd)
			fmt.Println("Use --global to see all tasks")
		}
		return
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tSESSION\tSTATUS\tDELIVER AT\tMESSAGE")

	for _, t := range tasks {
		if tm, ok := t.(map[string]interface{}); ok {
			id := getString(tm, "id")
			sessionCode := getString(tm, "session_code")
			status := getString(tm, "status")
			message := getString(tm, "message")

			// Truncate message if too long
			if len(message) > 40 {
				message = message[:37] + "..."
			}

			deliverAt := ""
			if ts, ok := tm["deliver_at"].(string); ok {
				if t, err := time.Parse(time.RFC3339, ts); err == nil {
					deliverAt = t.Format(time.Kitchen)
				}
			}

			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", id, sessionCode, status, deliverAt, message)
		}
	}
	w.Flush()
}

func runSessionCancel(cmd *cobra.Command, args []string) {
	client, err := getSessionClient(cmd)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
	defer client.Close()

	taskID := args[0]

	err = client.SessionCancel(taskID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to cancel task: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Task %s cancelled\n", taskID)
}

// getString extracts a string value from a map.
func getString(m map[string]interface{}, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

// getBool extracts a bool value from a map.
func getBool(m map[string]interface{}, key string) bool {
	if v, ok := m[key].(bool); ok {
		return v
	}
	return false
}
