package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/standardbeagle/agnt/internal/sshclient"
	"golang.org/x/term"
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
	files, destRelPath, ambiguousDest := splitPushArgs(args)
	if ambiguousDest {
		if err := confirmAmbiguousDest(cmd, destRelPath); err != nil {
			return err
		}
	}

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

// splitPushArgs applies the "file... [dest-rel-path]" contract. The last of
// several arguments is treated as the destination directory when it is not an
// existing local file. Two cases produce a destination:
//
//   - it names an existing directory → unambiguous destination (ambiguousDest
//     is false).
//   - it does not exist on disk at all → it *might* be an intended new remote
//     directory, but it is just as likely a typo'd filename that would
//     otherwise silently become a remote directory. ambiguousDest is true so
//     the caller can warn/confirm rather than misroute the push.
//
// Otherwise every argument is a file and the destination defaults (empty
// string, resolved by PushToInbox to DefaultInboxDir).
func splitPushArgs(args []string) (files []string, destRelPath string, ambiguousDest bool) {
	if len(args) < 2 {
		return args, "", false
	}
	last := args[len(args)-1]
	info, err := os.Stat(last)
	if err == nil && !info.IsDir() {
		return args, "", false
	}
	ambiguous := err != nil // stat failed: last does not name an existing path
	return args[:len(args)-1], last, ambiguous
}

// confirmAmbiguousDest warns that an ambiguous trailing argument (one that
// does not name an existing local file) is about to be treated as a remote
// destination directory, and requires confirmation before doing so. In a
// non-interactive session it fails loud rather than silently misrouting the
// push, matching the Silent Failure Prohibition.
func confirmAmbiguousDest(cmd *cobra.Command, dest string) error {
	out := cmd.ErrOrStderr()
	fmt.Fprintf(out, "agnt push: %q does not name an existing local file; it will be used as a remote destination directory.\n", dest)
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return fmt.Errorf("agnt push: refusing to auto-route ambiguous trailing argument %q as a remote destination directory in a non-interactive session; pass only files to push, or run interactively to confirm", dest)
	}
	fmt.Fprint(out, "Proceed treating it as a remote directory? [y/N] ")
	line, _ := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
	if strings.ToLower(strings.TrimSpace(line)) != "y" {
		return fmt.Errorf("agnt push: aborted — trailing argument %q was not confirmed as a destination", dest)
	}
	return nil
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
