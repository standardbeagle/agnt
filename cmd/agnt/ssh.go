//go:build !windows

// Windows support for `agnt ssh` (native ConPTY resize signaling) is an
// explicit open gap — see the task's final report. This command is
// currently unix-only (matches the existing attach_unix.go / attach_windows.go
// split precedent in this package).
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/spf13/cobra"
	"github.com/standardbeagle/agnt/internal/sshclient"
	"golang.org/x/term"
)

var (
	sshAttachName string
	sshTool       string
)

var sshCmd = &cobra.Command{
	Use:   "ssh <host>[:path]",
	Short: "Open a session-host PTY on a remote agnt daemon over SSH",
	Long: `Connect to <host> over SSH (resolving ~/.ssh/config for HostName,
User, Port, IdentityFile, and ProxyJump, exactly as the OpenSSH client
would), verify the host key against ~/.ssh/known_hosts, authenticate via
ssh-agent then IdentityFile(s) then interactive password/keyboard-interactive,
and attach a PTY to a remote session-host session by execing
'agnt attach <name> --create-if-missing --cwd <path>' on the far side.

Host[:path] parsing rule (documented here since it is a judgment call, not
an inferred default): the argument is split on the FIRST colon. Everything
before it is the host; everything after it (if any) is the REMOTE working
directory, not a port — ssh_config's own Port directive (or the default 22)
supplies the port, matching the spec's "host[:path]" contract rather than
ssh(1)'s unrelated "host:port" shorthand. IPv6 / bracketed host forms are
out of scope for this simple split.

This command carries only the SSH transport, auth, host-key verification,
and PTY relay. Port forwarding, SFTP, and remote-binary bootstrap are
implemented by other tasks in the remote-ssh epic.`,
	Args: cobra.ExactArgs(1),
	RunE: runSSH,
}

func init() {
	sshCmd.Flags().StringVar(&sshAttachName, "attach", "", "remote session-host session name (default: derived from local cwd basename)")
	sshCmd.Flags().StringVar(&sshTool, "tool", "", "AI tool to select on the remote side (placeholder — see OPEN GAPS in task report; not yet wired to any remote flag)")
	rootCmd.AddCommand(sshCmd)
}

// parseHostPath applies the host[:path] rule documented in sshCmd's Long
// help: split on the FIRST colon. Returns host and remotePath (remotePath
// is "" if no colon is present).
func parseHostPath(arg string) (host, remotePath string) {
	idx := strings.IndexByte(arg, ':')
	if idx < 0 {
		return arg, ""
	}
	return arg[:idx], arg[idx+1:]
}

func runSSH(cmd *cobra.Command, args []string) error {
	host, remotePath := parseHostPath(args[0])
	if host == "" {
		return fmt.Errorf("agnt ssh: empty host in %q", args[0])
	}

	attachName := sshAttachName
	if attachName == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("agnt ssh: resolving default --attach name: %w", err)
		}
		attachName = filepath.Base(cwd)
	}

	if sshTool != "" {
		fmt.Fprintf(os.Stderr, "agnt ssh: --tool %q is not yet wired to the remote agnt attach command (remote tool selection is a different task's scope) — proceeding without it\n", sshTool)
	}

	defaultUser := os.Getenv("USER")
	prompter := sshclient.StdioPrompter()

	client, err := sshclient.Dial(host, "", "", defaultUser, prompter)
	if err != nil {
		return fmt.Errorf("agnt ssh: %w", err)
	}
	defer client.Close()

	cols, rows := 80, 24
	if c, r, err := term.GetSize(int(os.Stdin.Fd())); err == nil {
		cols, rows = c, r
	}
	size := sshclient.TermSize{Cols: uint32(cols), Rows: uint32(rows)}

	session, err := sshclient.OpenPTYSession(client.SSH, attachName, remotePath, size)
	if err != nil {
		return fmt.Errorf("agnt ssh: %w", err)
	}
	defer session.Close()

	fd := int(os.Stdin.Fd())
	restore, rawErr := sshRawTerminal(fd)
	if rawErr == nil {
		defer restore()
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stopResize := sshWatchResize(ctx, session)
	defer stopResize()

	go func() {
		select {
		case <-client.Dead():
			fmt.Fprintln(os.Stderr, "\nagnt ssh: SSH transport appears dead (keepalive timeout) — reconnect is not yet implemented in this command; exiting")
			cancel()
		case <-ctx.Done():
		}
	}()

	relayErr := session.Relay(ctx, os.Stdin, os.Stdout, os.Stderr)
	if restore != nil {
		restore()
	}
	if relayErr != nil && relayErr != context.Canceled {
		return fmt.Errorf("agnt ssh: session relay: %w", relayErr)
	}
	return nil
}

// sshRawTerminal puts the local terminal into raw mode for the duration of
// the PTY relay, returning a restore func safe to call more than once.
func sshRawTerminal(fd int) (func(), error) {
	oldState, err := term.MakeRaw(fd)
	if err != nil {
		return func() {}, err
	}
	restored := false
	return func() {
		if restored {
			return
		}
		restored = true
		_ = term.Restore(fd, oldState)
	}, nil
}

// sshWatchResize watches SIGWINCH and forwards new terminal dimensions to
// the remote session as "window-change" requests until ctx is cancelled.
// Actual OS signal handling lives here (not in internal/sshclient) so that
// package stays signal-agnostic and testable.
func sshWatchResize(ctx context.Context, session *sshclient.PTYSession) func() {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGWINCH)
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		for {
			select {
			case <-ctx.Done():
				signal.Stop(ch)
				return
			case <-ch:
				if cols, rows, err := term.GetSize(int(os.Stdin.Fd())); err == nil {
					_ = session.Resize(sshclient.TermSize{Cols: uint32(cols), Rows: uint32(rows)})
				}
			}
		}
	}()
	return func() {
		signal.Stop(ch)
		<-stopped
	}
}
