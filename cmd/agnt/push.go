package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/standardbeagle/agnt/internal/sshclient"
)

var pushHost string

var pushCmd = &cobra.Command{
	Use:   "push <file...> [dest-rel-path]",
	Short: "Push one or more local files to the active 'agnt ssh' session's remote project",
	Long: `Upload one or more local files to the remote project of an active
'agnt ssh' session, discovered via its local control socket (no host
argument needed when exactly one session is active; pass --host to
disambiguate when more than one is running).

Each file lands at <remote-project-root>/.agnt-inbox/<basename> by
default. An optional trailing dest-rel-path argument — recognized because
it does not name an existing local file — is treated as a directory
relative to the remote project root, so each file lands at
<remote-project-root>/<dest-rel-path>/<basename> instead.

The destination is confirmed to stay within the remote project root
(rejecting "..", absolute paths, and symlinks that resolve outside it)
before anything is written, and each upload is atomic: a failed or
interrupted push never leaves a partial file at the final path.`,
	Args: cobra.MinimumNArgs(1),
	RunE: runPush,
}

func init() {
	pushCmd.Flags().StringVar(&pushHost, "host", "", "target host, required when more than one 'agnt ssh' session is active")
	rootCmd.AddCommand(pushCmd)
}

func runPush(cmd *cobra.Command, args []string) error {
	files, destRelPath := splitPushArgs(args)

	host := pushHost
	if host == "" {
		resolved, err := resolveActiveHost()
		if err != nil {
			return err
		}
		host = resolved
	}

	for _, localPath := range files {
		remotePath, err := pushOneLocalFile(host, localPath, destRelPath)
		if err != nil {
			return fmt.Errorf("agnt push: %s: %w", localPath, err)
		}
		fmt.Fprintf(os.Stdout, "agnt push: %s -> %s:%s\n", localPath, host, remotePath)
	}
	return nil
}

// splitPushArgs applies the "file... [dest-rel-path]" contract: if there is
// more than one argument and the last one is NOT an existing local file, it
// is treated as the destination directory and every remaining argument is a
// file to push. Otherwise every argument is a file and the destination
// defaults (empty string, resolved by PushToInbox to DefaultInboxDir).
func splitPushArgs(args []string) (files []string, destRelPath string) {
	if len(args) < 2 {
		return args, ""
	}
	last := args[len(args)-1]
	if info, err := os.Stat(last); err == nil && !info.IsDir() {
		return args, ""
	}
	return args[:len(args)-1], last
}

// resolveActiveHost discovers the single live 'agnt ssh' control socket.
// Zero live sessions or more than one without --host both fail loud, per
// the Silent Failure Prohibition — a caller must never have a push
// silently go to the wrong host, or silently do nothing.
func resolveActiveHost() (string, error) {
	hosts, err := sshclient.DiscoverActiveHosts()
	if err != nil {
		return "", fmt.Errorf("agnt push: discovering active 'agnt ssh' sessions: %w", err)
	}
	switch len(hosts) {
	case 0:
		return "", fmt.Errorf("agnt push: %w — start 'agnt ssh <host>' first", sshclient.ErrNoActiveSession)
	case 1:
		return hosts[0], nil
	default:
		return "", fmt.Errorf("agnt push: multiple active 'agnt ssh' sessions (%s) — pass --host to disambiguate", strings.Join(hosts, ", "))
	}
}

// pushOneLocalFile opens localPath, stats its size, and streams it to
// host's control socket via sshclient.PushOneFile.
func pushOneLocalFile(host, localPath, destRelPath string) (string, error) {
	f, err := os.Open(localPath)
	if err != nil {
		return "", fmt.Errorf("opening local file: %w", err)
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return "", fmt.Errorf("stat local file: %w", err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("is a directory, not a file")
	}

	fileName := info.Name()
	return sshclient.PushOneFile(host, fileName, destRelPath, info.Size(), f)
}
