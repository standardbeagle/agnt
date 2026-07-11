//go:build !windows

// Windows support for `agnt ssh` (native ConPTY resize signaling) is an
// explicit open gap — see the task's final report. This command is
// currently unix-only (matches the existing attach_unix.go / attach_windows.go
// split precedent in this package).
package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/spf13/cobra"
	"github.com/standardbeagle/agnt/internal/daemon"
	"github.com/standardbeagle/agnt/internal/sshclient"
	"golang.org/x/term"
)

var sshAttachName string
var sshNoBootstrap bool
var sshBootstrapConsent string

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
	sshCmd.Flags().BoolVar(&sshNoBootstrap, "no-bootstrap", false, "skip the remote agnt binary version check and install entirely")
	sshCmd.Flags().StringVar(&sshBootstrapConsent, "bootstrap", "", `consent for installing/upgrading the remote agnt binary: must be "yes" when stdin is not a terminal and an install is needed`)
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

	defaultUser := os.Getenv("USER")
	prompter := sshclient.StdioPrompter()

	client, err := sshclient.Dial(host, "", "", defaultUser, prompter)
	if err != nil {
		return fmt.Errorf("agnt ssh: %w", err)
	}
	defer client.Close()

	if err := ensureRemoteBootstrap(client, host); err != nil {
		return err
	}

	stopForwarding := startDaemonSocketForwarding(host, client)
	defer stopForwarding()

	stopPortForwarding := startPortForwarding(host, client)
	defer stopPortForwarding()

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

// ensureRemoteBootstrap implements the task's connect-time contract: check
// the remote agnt binary (missing, version-mismatched, or fine) and, if an
// install is needed, gate on explicit consent before doing anything —
// --no-bootstrap skips the whole check, --bootstrap=yes is the required
// non-interactive consent (scripted/CI usage must never silently install a
// binary on a remote host), and an interactive terminal falls back to a
// y/N prompt. Failure to resolve consent, or the install itself failing,
// aborts before the PTY session opens — an incompatible or missing remote
// binary means 'agnt attach' on the far side would fail anyway.
func ensureRemoteBootstrap(client *sshclient.Client, host string) error {
	if sshNoBootstrap {
		return nil
	}

	execPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("agnt ssh: resolving local binary path for bootstrap: %w", err)
	}
	opts := sshclient.BootstrapOptions{
		LocalVersion:    appVersion,
		LocalBinaryPath: execPath,
	}

	decision, err := sshclient.CheckRemoteBinary(client.SSH, opts)
	if err != nil {
		return fmt.Errorf("agnt ssh: bootstrap check on %s: %w", host, err)
	}
	if !decision.NeedsInstall {
		return nil
	}

	fmt.Fprintf(os.Stderr, "agnt ssh: %s — will install via %s\n", decision.Reason, decision.Source)

	if !isTerminal(os.Stdin) {
		if sshBootstrapConsent != "yes" {
			return fmt.Errorf("agnt ssh: remote agnt binary needs install on %s (%s) but stdin is not a terminal — pass --bootstrap=yes to consent, or --no-bootstrap to skip", host, decision.Reason)
		}
	} else if sshBootstrapConsent != "yes" {
		fmt.Fprintf(os.Stderr, "Install agnt on %s now? [y/N] ", host)
		reader := bufio.NewReader(os.Stdin)
		line, _ := reader.ReadString('\n')
		answer := strings.ToLower(strings.TrimSpace(line))
		if answer != "y" && answer != "yes" {
			return fmt.Errorf("agnt ssh: remote agnt binary bootstrap declined for %s", host)
		}
	}

	if err := sshclient.InstallRemoteBinary(client.SSH, opts, decision); err != nil {
		return fmt.Errorf("agnt ssh: installing agnt on %s: %w", host, err)
	}
	fmt.Fprintf(os.Stderr, "agnt ssh: installed agnt to %s on %s\n", decision.FinalPath, host)
	return nil
}

// startDaemonSocketForwarding discovers the remote daemon's socket path over
// the existing SSH connection, opens a local unix-socket forwarder for it,
// and prints the export line pointed at by the task's acceptance criteria:
// "local AGNT_DAEMON_SOCKET=... agnt monitor streams remote events". It does
// NOT set the env var for this process's own children — this command never
// spawns 'agnt monitor'/'agnt doctor' itself; the user runs those in another
// terminal after exporting the printed variable there.
//
// Forwarding failure is non-fatal to the PTY session: it is surfaced loudly
// on stderr and the returned stop func is a no-op, so a remote daemon that
// isn't running (or an old agnt binary without 'daemon socket-path') doesn't
// prevent the interactive session from proceeding.
func startDaemonSocketForwarding(host string, client *sshclient.Client) func() {
	remoteSocketPath, err := sshclient.RemoteDaemonSocketPath(client.SSH)
	if err != nil {
		fmt.Fprintf(os.Stderr, "agnt ssh: could not discover remote daemon socket path (%v) — local AGNT_DAEMON_SOCKET forwarding disabled\n", err)
		return func() {}
	}

	localSocketPath := sshclient.LocalForwardSocketPath(host)
	forwarder, err := sshclient.NewForwarder(client, remoteSocketPath, localSocketPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "agnt ssh: could not start local daemon socket forwarding (%v) — AGNT_DAEMON_SOCKET forwarding disabled\n", err)
		return func() {}
	}

	go func() {
		if serveErr := forwarder.Serve(); serveErr != nil {
			fmt.Fprintf(os.Stderr, "agnt ssh: daemon socket forwarder stopped: %v\n", serveErr)
		}
	}()

	fmt.Fprintf(os.Stderr, "agnt ssh: forwarding remote daemon socket to %s\n", localSocketPath)
	fmt.Fprintf(os.Stderr, "agnt ssh: run this in another terminal to point agnt monitor/agnt doctor/the MCP daemon tool at the remote daemon:\n")
	fmt.Fprintf(os.Stderr, "  export AGNT_DAEMON_SOCKET=%s\n", localSocketPath)

	return func() {
		if err := forwarder.Close(); err != nil {
			fmt.Fprintf(os.Stderr, "agnt ssh: closing daemon socket forwarder: %v\n", err)
		}
	}
}

// startPortForwarding dials the just-forwarded remote daemon socket (see
// startDaemonSocketForwarding) as a *daemon.Client and hands it, plus the
// SSH transport, to a sshclient.PortForwardManager so every remote proxy the
// developer starts/stops for the rest of this session gets a same-port-
// preferred local listener automatically — no manual `ssh -L` required (task
// 07 of the remote-ssh epic). Like daemon socket forwarding, failure here is
// non-fatal to the interactive PTY session: it is surfaced loudly on stderr
// and the returned stop func is a no-op.
func startPortForwarding(host string, client *sshclient.Client) func() {
	localSocketPath := sshclient.LocalForwardSocketPath(host)
	dclient := daemon.NewClientWithPath(localSocketPath)
	if err := dclient.Connect(); err != nil {
		fmt.Fprintf(os.Stderr, "agnt ssh: could not connect to forwarded daemon socket for port forwarding (%v) — remote proxies will not be auto-forwarded\n", err)
		return func() {}
	}

	mgr := sshclient.NewPortForwardManager(client, dclient, func(msg string) {
		fmt.Fprintf(os.Stderr, "%s\n", msg)
	})
	mgr.Start(context.Background())

	return func() {
		mgr.Stop()
		dclient.Close()
	}
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
