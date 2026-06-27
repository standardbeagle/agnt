package main

import (
	"fmt"
	"io"
	"time"

	"github.com/spf13/cobra"
	"github.com/standardbeagle/agnt/internal/selflog"
)

var (
	hookLogTail   int
	hookLogClear  bool
	hookLogFollow bool
	hookLogPath   string
)

// hookLogCmd surfaces the persistent self-error log — the always-on record
// of agnt's own fire-and-forget failures (hook-dispatcher drops, incident
// pinger delivery failures, and similar paths that must stay silent to
// their caller). It reads selflog.DefaultPath() directly, so it works even
// when the daemon is down (which is exactly when most of these drops
// happen).
var hookLogCmd = &cobra.Command{
	Use:   "log",
	Short: "Show agnt's persistent self-error log (hook drops & fire-and-forget failures)",
	Long: `Show the persistent self-error log at ${XDG_CACHE_HOME:-$HOME/.cache}/agnt/errors.log.

This is where agnt records its OWN failures that, by design, must stay silent
to the caller: hook-dispatcher drops (daemon wedged/unreachable), incident
pinger delivery failures, and similar fire-and-forget paths. Each line is:

  <RFC3339 timestamp> <component> <message>

Examples:
  agnt hook log                 # last 50 entries
  agnt hook log --tail 200      # last 200
  agnt hook log --follow        # stream new entries as they land
  agnt hook log --clear         # wipe the log`,
	RunE: func(cmd *cobra.Command, args []string) error {
		path := hookLogPath
		if path == "" {
			path = selflog.DefaultPath()
		}
		if path == "" {
			return fmt.Errorf("could not resolve self-error log path (no cache or home dir)")
		}

		if hookLogClear {
			if err := selflog.Clear(path); err != nil {
				return fmt.Errorf("clear %s: %w", path, err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Cleared %s\n", path)
			return nil
		}

		entries, err := selflog.Read(path, hookLogTail)
		if err != nil {
			return fmt.Errorf("read %s: %w", path, err)
		}
		out := cmd.OutOrStdout()
		if len(entries) == 0 {
			fmt.Fprintf(out, "No self-errors logged (%s)\n", path)
		} else {
			for _, e := range entries {
				printSelfLogEntry(out, e)
			}
		}

		if hookLogFollow {
			return followSelfLog(cmd, path)
		}
		return nil
	},
}

func printSelfLogEntry(w io.Writer, e selflog.Entry) {
	fmt.Fprintf(w, "%s  %-16s  %s\n", e.Time.Local().Format("2006-01-02 15:04:05"), e.Component, e.Message)
}

// followSelfLog polls the log for growth and prints new entries until the
// command's context is cancelled (Ctrl-C). A poll loop (not fsnotify)
// keeps the dependency surface zero and is plenty for a low-rate log.
func followSelfLog(cmd *cobra.Command, path string) error {
	ctx := cmd.Context()
	seen := 0
	if entries, err := selflog.Read(path, 0); err == nil {
		seen = len(entries)
	}
	ticker := time.NewTicker(700 * time.Millisecond)
	defer ticker.Stop()
	out := cmd.OutOrStdout()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			entries, err := selflog.Read(path, 0)
			if err != nil {
				continue
			}
			if len(entries) < seen {
				// File was cleared/rotated under us; resync.
				seen = 0
			}
			for _, e := range entries[seen:] {
				printSelfLogEntry(out, e)
			}
			seen = len(entries)
		}
	}
}

func init() {
	hookLogCmd.Flags().IntVar(&hookLogTail, "tail", 50, "Show the last N entries (0 = all)")
	hookLogCmd.Flags().BoolVar(&hookLogClear, "clear", false, "Clear the self-error log and exit")
	hookLogCmd.Flags().BoolVarP(&hookLogFollow, "follow", "f", false, "Stream new entries as they are appended")
	hookLogCmd.Flags().StringVar(&hookLogPath, "path", "", "Override the log path (default: selflog.DefaultPath())")
	// stdout is the deliverable here; keep usage noise off it on error.
	hookLogCmd.SilenceUsage = true
	hookCmd.AddCommand(hookLogCmd)
}
