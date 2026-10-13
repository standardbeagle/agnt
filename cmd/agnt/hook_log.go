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
	// Long is set in init from hookLogLong: it names the log's real path on
	// this machine, which is only known at run time.
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

// hookLogLong is the long help for `agnt hook log`, built around path, the
// log's location on this machine (selflog.DefaultPath()).
//
// The help used to state ${XDG_CACHE_HOME:-$HOME/.cache}/agnt/errors.log for
// everyone. That is right on Linux only: the log lives in the per-user cache
// directory (os.UserCacheDir), which is ~/Library/Caches on macOS and
// %LocalAppData% on Windows, and XDG_CACHE_HOME is not consulted there. So the
// help prints the path the command will actually read.
func hookLogLong(path string) string {
	where := "The location could not be determined on this machine (no cache or home\ndirectory); pass --path or set AGNT_ERROR_LOG."
	if path != "" {
		where = "On this machine it is:\n\n  " + path + "\n\n" +
			"That is agnt/errors.log in the per-user cache directory (~/.cache or\n" +
			"$XDG_CACHE_HOME on Linux, ~/Library/Caches on macOS, %LocalAppData% on\n" +
			"Windows). AGNT_ERROR_LOG, when set, replaces the whole path."
	}
	return `Show agnt's persistent self-error log.

` + where + `

This is where agnt records its OWN failures that, by design, must stay silent
to the caller: hook-dispatcher drops (daemon wedged/unreachable), incident
pinger delivery failures, and similar fire-and-forget paths. Each line is:

  <RFC3339 timestamp> <component> <message>

Examples:
  agnt hook log                 # last 50 entries
  agnt hook log --tail 200      # last 200
  agnt hook log --follow        # stream new entries as they land
  agnt hook log --clear         # wipe the log`
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
	hookLogCmd.Long = hookLogLong(selflog.DefaultPath())
	hookLogCmd.Flags().StringVar(&hookLogPath, "path", "", "Read this file instead of the default log (see the path above)")
	// stdout is the deliverable here; keep usage noise off it on error.
	hookLogCmd.SilenceUsage = true
	hookCmd.AddCommand(hookLogCmd)
}
