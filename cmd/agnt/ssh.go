//go:build !windows

// Windows support for `agnt ssh` (native ConPTY resize signaling) is an
// explicit open gap — see the task's final report. This command is
// currently unix-only (matches the existing attach_unix.go / attach_windows.go
// split precedent in this package).
package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/pkg/sftp"
	"github.com/spf13/cobra"
	"github.com/standardbeagle/agnt/internal/daemon"
	"github.com/standardbeagle/agnt/internal/protocol"
	"github.com/standardbeagle/agnt/internal/sshclient"
	"golang.org/x/term"
)

var sshAttachName string
var sshNoBootstrap bool
var sshBootstrapConsent string
var sshCreateIfMissing bool
var sshNewSession bool
var sshReconnectMax int
var sshShowForwardStatus bool

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
	sshCmd.Flags().BoolVar(&sshCreateIfMissing, "create-if-missing", false, "if the named session-host session is gone on reconnect, create a fresh one instead of failing loud (spec invariant 24; default is hard-fail)")
	sshCmd.Flags().BoolVar(&sshNewSession, "new", false, "same effect as --create-if-missing for reconnect purposes: never hard-fail when the named session is gone")
	sshCmd.Flags().IntVar(&sshReconnectMax, "reconnect-max", 0, "maximum reconnect attempts before giving up (0 = unlimited, the interactive default)")
	sshCmd.Flags().BoolVar(&sshShowForwardStatus, "status", false, "show connection, reconnect, forward, push queue, and session status")
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

	remoteVersion, err := ensureRemoteBootstrap(client, host)
	if err != nil {
		client.Close()
		return err
	}

	control := startControlSocket(host, client, remotePath, attachName)
	forwarding := startReconnectForwarding(host, client, control.ProjectRoot())
	status := sshclient.NewStatusTracker(attachName)
	forwarding.status = status
	forwarding.control = control

	cols, rows := 80, 24
	if c, r, err := term.GetSize(int(os.Stdin.Fd())); err == nil {
		cols, rows = c, r
	}
	size := sshclient.TermSize{Cols: uint32(cols), Rows: uint32(rows)}

	session, err := sshclient.OpenPTYSession(client.SSH, attachName, remotePath, size)
	if err != nil {
		control.Stop()
		forwarding.Stop()
		client.Close()
		return fmt.Errorf("agnt ssh: %w", err)
	}
	writeSSHFirstScreen(os.Stderr, host, appVersion, remoteVersion, control.ProjectRoot(), attachName)
	forwarding.renderStatus()

	fd := int(os.Stdin.Fd())
	restore, rawErr := sshRawTerminal(fd)
	if rawErr == nil {
		defer restore()
	}

	allowCreate := sshCreateIfMissing || sshNewSession
	reconnector := &sshclient.Reconnector{
		Backoff:     sshclient.BackoffConfig{BaseDelay: time.Second, MaxDelay: 30 * time.Second, Jitter: rand.Float64},
		MaxAttempts: sshReconnectMax,
		OnStatus: func(msg string) {
			fmt.Fprintln(os.Stderr, msg)
			status.Observe(msg)
			forwarding.renderStatus()
		},
		Dial: func(ctx context.Context) (*sshclient.Client, error) {
			return sshclient.Dial(host, "", "", defaultUser, prompter)
		},
		Attach: func(ctx context.Context, c *sshclient.Client) (*sshclient.PTYSession, error) {
			return reattachRemoteSession(host, c, attachName, remotePath, size, allowCreate)
		},
	}

	return runSSHRelayLoop(host, remotePath, attachName, client, session, forwarding, control, reconnector)
}

// runSSHRelayLoop owns the CONNECTED<->RECONNECTING cycle (task 09c). Each
// iteration: relay bytes until either the local ctx is cancelled (clean
// end) or client.Dead() fires (transport confirmed dead, task 04b's bounded
// keepalive probe — this function never re-implements that detection, only
// consumes the channel). On a confirmed-dead transport it tears down the
// old client/forwards and drives sshclient.Reconnector.Run to get a fresh
// client+session attached to the SAME named remote session, then resumes
// relaying. A single InputPump owns the real stdin for the whole loop so
// repeated reconnects never leave more than one goroutine blocked reading
// it (see reconnect.go's doc comment) and doubles as the Ctrl-C detector
// during RECONNECTING, since raw mode never delivers a real SIGINT for
// that byte.
func runSSHRelayLoop(host, remotePath, attachName string, client *sshclient.Client, session *sshclient.PTYSession, forwarding *reconnectForwarding, control *reconnectControl, reconnector *sshclient.Reconnector) error {
	// The control owner intentionally survives individual transport drops, but
	// it must not survive this relay loop itself: exhausted retries and Ctrl-C
	// during RECONNECTING both return from here and must release queued callers.
	defer control.Stop()

	var reconnectCancelMu sync.Mutex
	var reconnectCancel context.CancelFunc
	pump := sshclient.NewInputPump(func() {
		reconnectCancelMu.Lock()
		cancel := reconnectCancel
		reconnectCancelMu.Unlock()
		if cancel != nil {
			cancel()
		}
	})
	pump.Start(os.Stdin)

	for {
		pr, pw := io.Pipe()
		pump.SetTarget(pw)

		relayCtx, relayCancel := context.WithCancel(context.Background())
		watchDone := make(chan struct{})
		go func() {
			defer close(watchDone)
			select {
			case <-client.Dead():
				relayCancel()
			case <-relayCtx.Done():
			}
		}()

		stopResize := sshWatchResize(relayCtx, session)
		relayErr := session.Relay(relayCtx, pr, os.Stdout, os.Stderr)
		stopResize()
		relayCancel()
		<-watchDone
		pump.SetTarget(nil)
		pw.Close()
		session.Close()

		transportDead := isClosedChan(client.Dead())
		if !transportDead {
			control.Stop()
			forwarding.Stop()
			client.Close()
			if relayErr != nil && relayErr != context.Canceled {
				return fmt.Errorf("agnt ssh: session relay: %w", relayErr)
			}
			return nil
		}

		fmt.Fprintln(os.Stderr, "\nagnt ssh: connection lost (keepalive timeout)")
		if forwarding.status != nil {
			forwarding.status.Disconnected()
			forwarding.renderStatus()
		}
		control.Pause()
		forwarding.Pause()
		client.Close()

		reconnectCtx, cancel := context.WithCancel(context.Background())
		reconnectCancelMu.Lock()
		reconnectCancel = cancel
		reconnectCancelMu.Unlock()

		newClient, newSession, err := reconnector.Run(reconnectCtx)

		if err != nil {
			reconnectCancelMu.Lock()
			reconnectCancel = nil
			reconnectCancelMu.Unlock()
			cancel()
			if errors.Is(err, context.Canceled) {
				// The only thing that ever cancels reconnectCtx is the
				// pump's Ctrl-C interrupt callback above — a clean,
				// user-requested stop, not a failure (criterion 4).
				return nil
			}
			return fmt.Errorf("agnt ssh: reconnect failed: %w", err)
		}

		client = newClient
		session = newSession
		forwardReady := forwarding.Resume(client)
		queueDrained := control.Resume(client)
		resumeErr := completeSSHReconnect(reconnectCtx, forwardReady, queueDrained, forwarding.renderStatus)
		reconnectCancelMu.Lock()
		reconnectCancel = nil
		reconnectCancelMu.Unlock()
		cancel()
		if resumeErr != nil {
			// This is the same Ctrl-C cancellation used while reconnecting.
			// Do not resume a PTY after cancellation during reconcile/drain.
			return resumeErr
		}
	}
}

// completeSSHReconnect is the relay loop's final reconnect gate. The PTY may
// resume only after both forwarding truth and queued control work are current.
func completeSSHReconnect(ctx context.Context, forwardReady, queueDrained <-chan struct{}, render func()) error {
	if err := waitForLifecycle(ctx, forwardReady, queueDrained); err != nil {
		return err
	}
	render()
	return nil
}

func waitForLifecycle(ctx context.Context, signals ...<-chan struct{}) error {
	for _, signal := range signals {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-signal:
		}
	}
	return nil
}

func writeSSHFirstScreen(w io.Writer, host, localVersion, remoteVersion, project, session string) {
	fmt.Fprint(w, sshclient.TerminalTitle(host, session))
	fmt.Fprint(w, sshclient.FormatSplash(sshclient.Splash{
		Host: host, LocalVersion: localVersion, RemoteVersion: remoteVersion,
		Project: project, Session: session, DetachHint: "Ctrl-\\ Ctrl-\\",
	}))
}

// reconnectForwarding owns the local listener identity across SSH transport
// transitions. Pause only removes transport-dependent relays; Resume swaps in
// a fresh SSH transport and daemon protocol connection, then PortForwardManager
// performs a fresh PROXY LIST reconciliation (invariant 25).
type reconnectForwarding struct {
	mu          sync.RWMutex
	host        string
	project     string
	daemonFwd   *sshclient.Forwarder
	ports       *sshclient.PortForwardManager
	dclient     *daemon.Client
	drops       *sshclient.DropWatcher
	dropSFTP    *sftp.Client
	pull        reversePullLifecycle
	reportEvent func(*daemon.Client, protocol.DeveloperEvent) error
	status      *sshclient.StatusTracker
	control     *reconnectControl
	onResume    func()
}

type reversePullLifecycle interface {
	Start(context.Context)
	Resume(context.Context, *daemon.Client, *sftp.Client)
	Stop()
}

var newReversePullManager = func(dclient *daemon.Client, sc *sftp.Client, host string, notify func(string)) reversePullLifecycle {
	return sshclient.NewRemotePullManager(dclient, sc, host, "", notify)
}

func startReconnectForwarding(host string, client *sshclient.Client, projectRoot ...string) *reconnectForwarding {
	root := ""
	if len(projectRoot) > 0 {
		root = projectRoot[0]
	}
	r := newReconnectForwardingOwner(host, root)
	r.start(client)
	return r
}

func newReconnectForwardingOwner(host, projectRoot string) *reconnectForwarding {
	return &reconnectForwarding{host: host, project: projectRoot, reportEvent: func(c *daemon.Client, e protocol.DeveloperEvent) error { return c.ReportDeveloperEvent(e) }}
}

// start initializes forwarding directly on the durable owner. In particular,
// callbacks installed by connectPorts capture r; constructing a temporary
// reconnectForwarding and copying its fields would leave those callbacks
// permanently bound to the temporary's first daemon client.
func (r *reconnectForwarding) start(client *sshclient.Client) {
	r.connectDrops(client)
	remotePath, err := sshclient.RemoteDaemonSocketPath(client.SSH)
	if err != nil {
		fmt.Fprintf(os.Stderr, "agnt ssh: could not discover remote daemon socket path (%v) — forwarding disabled\n", err)
		return
	}
	fwd, err := sshclient.NewForwarder(client, remotePath, sshclient.LocalForwardSocketPath(r.host))
	if err != nil {
		fmt.Fprintf(os.Stderr, "agnt ssh: could not start local daemon socket forwarding (%v) — forwarding disabled\n", err)
		return
	}
	r.daemonFwd = fwd
	go fwd.Serve()
	fmt.Fprintf(os.Stderr, "agnt ssh: forwarding remote daemon socket to %s\n", sshclient.LocalForwardSocketPath(r.host))
	fmt.Fprintf(os.Stderr, "  export AGNT_DAEMON_SOCKET=%s\n", sshclient.LocalForwardSocketPath(r.host))
	r.connectPorts(client)
}

func (r *reconnectForwarding) connectDrops(client *sshclient.Client) {
	sftpClient, err := sshclient.NewSFTPClient(client.SSH)
	if err != nil {
		fmt.Fprintf(os.Stderr, "agnt ssh: could not start drop-folder sync (%v)\n", err)
		return
	}
	upload := sshclient.NewDropUpload(sftpClient, r.project)
	if r.drops == nil {
		home, err := os.UserHomeDir()
		if err != nil {
			sftpClient.Close()
			fmt.Fprintf(os.Stderr, "agnt ssh: could not resolve drop folder (%v)\n", err)
			return
		}
		hostDir := strings.NewReplacer("/", "_", "\\", "_").Replace(r.host)
		r.drops, err = sshclient.NewDropWatcher(filepath.Join(home, ".agnt", "drop", hostDir), upload, func(msg string) {
			fmt.Fprintln(os.Stderr, msg)
		})
		if err != nil {
			sftpClient.Close()
			fmt.Fprintf(os.Stderr, "agnt ssh: could not start drop-folder sync (%v)\n", err)
			return
		}
		fmt.Fprintf(os.Stderr, "agnt ssh: watching %s for files to sync\n", filepath.Join(home, ".agnt", "drop", hostDir))
	} else {
		r.drops.SetUpload(upload)
	}
	r.dropSFTP = sftpClient
}

func (r *reconnectForwarding) connectPorts(client *sshclient.Client) {
	dclient := daemon.NewClientWithPath(sshclient.LocalForwardSocketPath(r.host))
	if err := dclient.Connect(); err != nil {
		fmt.Fprintf(os.Stderr, "agnt ssh: could not connect to forwarded daemon socket for port forwarding/reverse capture pull (%v)\n", err)
		return
	}
	source := "ssh-forward-" + strings.NewReplacer("@", "-", ":", "-", "/", "-").Replace(r.host)
	if _, err := dclient.SessionRegister(source, "", "", "agnt ssh", nil); err != nil {
		dclient.Close()
		fmt.Fprintf(os.Stderr, "agnt ssh: could not register forwarding ownership (%v)\n", err)
		return
	}
	r.mu.Lock()
	r.dclient = dclient
	r.mu.Unlock()
	if r.ports == nil {
		var mgr *sshclient.PortForwardManager
		mgr = sshclient.NewPortForwardManager(client, dclient, func(msg string) { r.reportPortForward(msg, mgr.Status()) })
		mgr.SetOnChange(func(mappings []sshclient.Mapping) { r.publishForwardMappings(mappings) })
		r.ports = mgr
		r.ports.Start(context.Background())
	} else {
		r.ports.Resume(context.Background(), client, dclient)
	}
	r.connectPull(dclient)
}

func (r *reconnectForwarding) publishForwardMappings(mappings []sshclient.Mapping) {
	dclient := r.daemonClient()
	if dclient == nil {
		return
	}
	wire := make([]protocol.ForwardMapping, 0, len(mappings))
	for _, mapping := range mappings {
		wire = append(wire, protocol.ForwardMapping{ProxyID: mapping.ProxyID, RemotePort: mapping.RemotePort, LocalPort: mapping.LocalPort})
	}
	_ = dclient.SetForwardMappings(protocol.ForwardSet{Mappings: wire})
}

func (r *reconnectForwarding) connectPull(dclient *daemon.Client) {
	if r.dropSFTP == nil {
		fmt.Fprintln(os.Stderr, "agnt ssh: could not start reverse capture pull (SFTP unavailable)")
		return
	}
	if r.pull == nil {
		r.pull = newReversePullManager(dclient, r.dropSFTP, r.host, func(msg string) { fmt.Fprintln(os.Stderr, msg) })
		r.pull.Start(context.Background())
		return
	}
	r.pull.Resume(context.Background(), dclient, r.dropSFTP)
}

func (r *reconnectForwarding) reportPortForward(msg string, mappings []sshclient.Mapping) {
	fmt.Fprintln(os.Stderr, msg)
	r.renderStatus()
	if !strings.Contains(msg, "in use locally") {
		return
	}
	toastClient := r.daemonClient()
	if toastClient == nil || r.reportEvent == nil {
		return
	}
	for _, mapping := range mappings {
		if mapping.Remapped {
			_ = r.reportEvent(toastClient, protocol.DeveloperEvent{Kind: "forward_collision", ProxyID: mapping.ProxyID, ProjectPath: r.project, Severity: "warning", Title: "SSH port remapped", Message: fmt.Sprintf("remote :%d is available locally at http://127.0.0.1:%d", mapping.RemotePort, mapping.LocalPort)})
		}
	}
}

func (r *reconnectForwarding) renderStatus() {
	r.renderStatusTo(os.Stderr)
}

func (r *reconnectForwarding) renderStatusTo(w io.Writer, supplied ...[]sshclient.Mapping) {
	if !sshShowForwardStatus || r.status == nil {
		return
	}
	var mappings []sshclient.Mapping
	if len(supplied) > 0 {
		mappings = supplied[0]
	} else if r.ports != nil {
		mappings = r.ports.Status()
	}
	queued := 0
	if r.control != nil {
		queued = r.control.Depth()
	}
	fmt.Fprint(w, sshclient.FormatClientStatus(r.status.Snapshot(mappings, queued)))
}

func (r *reconnectForwarding) daemonClient() *daemon.Client {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.dclient
}

func (r *reconnectForwarding) Pause() {
	if r.pull != nil {
		r.pull.Stop()
	}
	if r.drops != nil {
		r.drops.SetUpload(nil)
	}
	if r.dropSFTP != nil {
		_ = r.dropSFTP.Close()
		r.dropSFTP = nil
	}
	if r.ports != nil {
		r.ports.Pause()
	}
	r.mu.Lock()
	dclient := r.dclient
	r.dclient = nil
	r.mu.Unlock()
	if dclient != nil {
		dclient.Close()
	}
	if r.daemonFwd != nil {
		r.daemonFwd.Pause()
	}
}

func (r *reconnectForwarding) Resume(client *sshclient.Client) <-chan struct{} {
	if r.daemonFwd == nil {
		r.start(client)
		if r.onResume != nil {
			r.onResume()
		}
		return r.forwardReconciled()
	}
	remotePath, err := sshclient.RemoteDaemonSocketPath(client.SSH)
	if err != nil {
		fmt.Fprintf(os.Stderr, "agnt ssh: could not rediscover remote daemon socket after reconnect: %v\n", err)
		return closedLifecycleSignal()
	}
	r.daemonFwd.Resume(client, remotePath)
	r.connectDrops(client)
	r.connectPorts(client)
	if r.onResume != nil {
		r.onResume()
	}
	return r.forwardReconciled()
}

func (r *reconnectForwarding) forwardReconciled() <-chan struct{} {
	if r.ports == nil {
		return closedLifecycleSignal()
	}
	return r.ports.Reconciled()
}

func closedLifecycleSignal() <-chan struct{} {
	done := make(chan struct{})
	close(done)
	return done
}

func (r *reconnectForwarding) Stop() {
	if r.pull != nil {
		r.pull.Stop()
		r.pull = nil
	}
	if r.dropSFTP != nil {
		_ = r.dropSFTP.Close()
		r.dropSFTP = nil
	}
	if r.drops != nil {
		_ = r.drops.Close()
		r.drops = nil
	}
	if r.ports != nil {
		r.ports.Stop()
	}
	r.publishForwardMappings(nil)
	if dclient := r.daemonClient(); dclient != nil {
		dclient.Close()
	}
	if r.daemonFwd != nil {
		_ = r.daemonFwd.Close()
	}
}

// isClosedChan reports whether ch is already closed, without blocking.
func isClosedChan(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

// reattachRemoteSession implements the reconnect-only AttachFunc: it never
// sends --create-if-missing/--cwd over the wire (spec invariant 24 — never
// re-create the named session on reconnect). It confirms the session still
// exists via the daemon protocol directly (through an ephemeral daemon
// socket forward over the freshly-dialed client), and only if allowCreate is
// set does it create a replacement — also via the daemon protocol
// (SESSION-HOST CREATE), not by relying on 'agnt attach' flags — before
// exec'ing the bare reattach command.
func reattachRemoteSession(host string, client *sshclient.Client, name, remotePath string, size sshclient.TermSize, allowCreate bool) (*sshclient.PTYSession, error) {
	if err := ensureRemoteSessionAttachable(host, client, name, remotePath, size, allowCreate); err != nil {
		return nil, err
	}
	return sshclient.OpenPTYSessionWithCommand(client.SSH, sshclient.RemoteReattachCommand(name), size)
}

// ensureRemoteSessionAttachable confirms the named session-host session
// still exists on the remote daemon, or (only when allowCreate is set)
// creates a fresh one — see reattachRemoteSession's doc comment for why this
// goes through the daemon protocol rather than 'agnt attach' CLI flags.
// Returns an error wrapping sshclient.ErrSessionMissing when the session is
// gone and allowCreate is false.
func ensureRemoteSessionAttachable(host string, client *sshclient.Client, name, remotePath string, size sshclient.TermSize, allowCreate bool) error {
	remoteSocketPath, err := sshclient.RemoteDaemonSocketPath(client.SSH)
	if err != nil {
		return fmt.Errorf("agnt ssh: discovering remote daemon socket for reconnect check: %w", err)
	}

	localPath := sshclient.LocalForwardSocketPath(host) + fmt.Sprintf(".reconnect-probe-%d", time.Now().UnixNano())
	fw, err := sshclient.NewForwarder(client, remoteSocketPath, localPath)
	if err != nil {
		return fmt.Errorf("agnt ssh: opening reconnect probe forward: %w", err)
	}
	defer fw.Close()
	go fw.Serve()

	dclient := daemon.NewClientWithPath(localPath)
	var connectErr error
	for i := 0; i < 20; i++ {
		if connectErr = dclient.Connect(); connectErr == nil {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if connectErr != nil {
		return fmt.Errorf("agnt ssh: connecting to reconnect probe forward: %w", connectErr)
	}
	defer dclient.Close()

	result, err := dclient.SessionHostList(protocol.DirectoryFilter{Global: true})
	if err != nil {
		return fmt.Errorf("agnt ssh: listing remote session-host sessions for reconnect: %w", err)
	}
	if _, ok := matchSessionHostID(result, name); ok {
		return nil
	}
	if !allowCreate {
		return fmt.Errorf("%w: %q", sshclient.ErrSessionMissing, name)
	}

	if _, err := dclient.SessionHostCreate(protocol.SessionHostCreateConfig{
		Name:        name,
		ProjectPath: remotePath,
		Command:     "sh",
		Cols:        int(size.Cols),
		Rows:        int(size.Rows),
	}); err != nil {
		return fmt.Errorf("agnt ssh: creating remote session-host session %q: %w", name, err)
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
func ensureRemoteBootstrap(client *sshclient.Client, host string) (string, error) {
	if sshNoBootstrap {
		return "unknown (--no-bootstrap)", nil
	}

	execPath, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("agnt ssh: resolving local binary path for bootstrap: %w", err)
	}
	opts := sshclient.BootstrapOptions{
		LocalVersion:    appVersion,
		LocalBinaryPath: execPath,
	}

	decision, err := sshclient.CheckRemoteBinary(client.SSH, opts)
	if err != nil {
		return "", fmt.Errorf("agnt ssh: bootstrap check on %s: %w", host, err)
	}
	if !decision.NeedsInstall {
		return decision.RemoteVersion, nil
	}

	fmt.Fprintf(os.Stderr, "agnt ssh: %s — will install via %s\n", decision.Reason, decision.Source)

	if !isTerminal(os.Stdin) {
		if sshBootstrapConsent != "yes" {
			return "", fmt.Errorf("agnt ssh: remote agnt binary needs install on %s (%s) but stdin is not a terminal — pass --bootstrap=yes to consent, or --no-bootstrap to skip", host, decision.Reason)
		}
	} else if sshBootstrapConsent != "yes" {
		fmt.Fprintf(os.Stderr, "Install agnt on %s now? [y/N] ", host)
		reader := bufio.NewReader(os.Stdin)
		line, _ := reader.ReadString('\n')
		answer := strings.ToLower(strings.TrimSpace(line))
		if answer != "y" && answer != "yes" {
			return "", fmt.Errorf("agnt ssh: remote agnt binary bootstrap declined for %s", host)
		}
	}

	if err := sshclient.InstallRemoteBinary(client.SSH, opts, decision); err != nil {
		return "", fmt.Errorf("agnt ssh: installing agnt on %s: %w", host, err)
	}
	fmt.Fprintf(os.Stderr, "agnt ssh: installed agnt to %s on %s\n", decision.FinalPath, host)
	return opts.LocalVersion, nil
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

// startControlSocket registers the local control socket (task 08a) that
// 'agnt push' discovers to find this active session: it resolves the
// remote project root, opens an SFTP subsystem over client, and serves
// ping/push requests until the returned stop func is called. Like daemon
// socket forwarding and port forwarding, failure here is non-fatal to the
// interactive PTY session — it is surfaced loudly on stderr and the
// returned stop func is a no-op, so a remote host without SFTP support (or
// a local ~/.agnt/ssh directory that can't be created) doesn't prevent the
// session from proceeding; it only means 'agnt push' won't find it.
const reconnectPushQueueCapacity = 32

type reconnectControl struct {
	listener    net.Listener
	queue       *sshclient.PushQueue
	projectRoot string
	onPause     func()
	onResume    func()
	onFlushed   func()
}

func (c *reconnectControl) Depth() int {
	if c == nil || c.queue == nil {
		return 0
	}
	return c.queue.Depth()
}

func (c *reconnectControl) ProjectRoot() string {
	if c == nil {
		return ""
	}
	return c.projectRoot
}

func startControlSocket(host string, client *sshclient.Client, remotePath string, sessionName ...string) *reconnectControl {
	projectRoot, err := sshclient.ResolveRemoteProjectRoot(client.SSH, remotePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "agnt ssh: could not resolve remote project root (%v) — 'agnt push' will not find this session\n", err)
		return &reconnectControl{}
	}

	ln, err := sshclient.ListenControl(host)
	if err != nil {
		fmt.Fprintf(os.Stderr, "agnt ssh: could not register control socket (%v) — 'agnt push' will not find this session\n", err)
		return &reconnectControl{}
	}

	sc, err := sshclient.NewSFTPClient(client.SSH)
	if err != nil {
		fmt.Fprintf(os.Stderr, "agnt ssh: could not open SFTP subsystem (%v) — 'agnt push' will not find this session\n", err)
		ln.Close()
		return &reconnectControl{}
	}

	name := ""
	if len(sessionName) > 0 {
		name = sessionName[0]
	}
	notify := func(remotePath string, size int64) error {
		if name == "" {
			return fmt.Errorf("attached session name unavailable")
		}
		return sshclient.NotifyFileArrived(sshclient.LocalForwardSocketPath(host), name, projectRoot, remotePath, size)
	}
	queue := sshclient.NewPushQueue(projectRoot, reconnectPushQueueCapacity, notify, func(msg string) {
		fmt.Fprintln(os.Stderr, msg)
	})
	queue.SetSFTP(sc)
	go sshclient.ServePushQueue(ln, queue)
	return &reconnectControl{listener: ln, queue: queue, projectRoot: projectRoot}
}

func (c *reconnectControl) Pause() {
	if c != nil && c.queue != nil {
		c.queue.Reconnecting()
		if c.onPause != nil {
			c.onPause()
		}
	}
}

func (c *reconnectControl) Resume(client *sshclient.Client) <-chan struct{} {
	if c != nil && c.queue != nil {
		c.queue.Connected(func() (*sftp.Client, error) { return sshclient.NewSFTPClient(client.SSH) })
		if c.onResume != nil {
			c.onResume()
		}
		drained := c.queue.Drained()
		if c.onFlushed != nil {
			go func() {
				<-drained
				c.onFlushed()
			}()
		}
		return drained
	}
	return closedLifecycleSignal()
}

func (c *reconnectControl) Stop() {
	if c == nil {
		return
	}
	if c.listener != nil {
		_ = c.listener.Close()
	}
	if c.queue != nil {
		c.queue.Close()
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
