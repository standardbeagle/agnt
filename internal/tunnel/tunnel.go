// Package tunnel provides management for tunnel services like Cloudflare and ngrok.
package tunnel

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"

	"github.com/standardbeagle/agnt/internal/config"
	"sync"
	"sync/atomic"
	"time"

	"github.com/standardbeagle/agnt/internal/platform"
)

// Provider represents a tunnel service provider.
type Provider string

const (
	// ProviderCloudflare uses cloudflared for Cloudflare Quick Tunnels.
	ProviderCloudflare Provider = "cloudflare"
	// ProviderNgrok uses ngrok for tunneling.
	ProviderNgrok Provider = "ngrok"
	// ProviderTailscale uses `tailscale serve` for tailnet-private HTTPS at
	// the node's MagicDNS name (https://<machine>.<tailnet>.ts.net).
	// NOT public: use cloudflare/ngrok for that.
	ProviderTailscale Provider = "tailscale"
)

// supportedProviders is the closed set of providers this build can start; it
// must stay in step with Start's dispatch switch (which independently rejects an
// unknown provider, so a drift here is a bad error message, never a silent
// start).
var supportedProviders = map[Provider]bool{
	ProviderCloudflare: true,
	ProviderNgrok:      true,
	ProviderTailscale:  true,
}

// ParseProvider validates a user-supplied provider name. Callers that take a
// provider from a CLI flag or config value use this so a typo fails loudly at
// the boundary, naming the legal set, instead of being cast straight to Provider
// and only surfacing later as "unsupported tunnel provider" from Start.
func ParseProvider(s string) (Provider, error) {
	p := Provider(strings.ToLower(strings.TrimSpace(s)))
	if !supportedProviders[p] {
		return "", fmt.Errorf("unknown tunnel provider %q (cloudflare|ngrok|tailscale)", s)
	}
	return p, nil
}

// State represents the tunnel state.
type State uint32

const (
	StateIdle State = iota
	StateStarting
	StateConnected
	StateFailed
	StateStopped
)

func (s State) String() string {
	switch s {
	case StateIdle:
		return "idle"
	case StateStarting:
		return "starting"
	case StateConnected:
		return "connected"
	case StateFailed:
		return "failed"
	case StateStopped:
		return "stopped"
	default:
		return "unknown"
	}
}

// Config holds tunnel configuration.
type Config struct {
	Provider   Provider
	LocalPort  int
	LocalHost  string // defaults to "localhost"
	BinaryPath string // optional: path to tunnel binary, otherwise uses PATH
	ID         string // tunnel identifier
	Path       string // project path for session scoping

	// Named runs a locally-managed named Cloudflare tunnel instead of a
	// quick tunnel. Only valid with ProviderCloudflare.
	Named *NamedCloudflare
}

// NamedCloudflare identifies a named Cloudflare tunnel provisioned outside
// agnt: the tunnel, its DNS route and its credential file are created with
// the account API token on a workstation, and agnt only runs the tunnel. The
// account token never reaches agnt.
type NamedCloudflare struct {
	// TunnelID is the tunnel UUID. It appears in the hostname's DNS CNAME
	// and is not a secret. Running by UUID with a credentials file needs no
	// origin cert, which running by name would.
	TunnelID string
	// Hostname is the public hostname routed to the tunnel. cloudflared does
	// not print it, so it is the tunnel's public URL once connected.
	Hostname string
	// CredentialsFile is the path to the tunnel credential JSON. agnt never
	// reads it; it only checks the file is private to its owner.
	CredentialsFile string
}

// Tunnel represents a running tunnel instance.
type Tunnel struct {
	config    Config
	state     atomic.Uint32
	publicURL atomic.Pointer[string]
	done      chan struct{}
	doneOnce  sync.Once
	err       error
	errMu     sync.RWMutex

	// procMu guards cmd and cancel: Start publishes them while Stop reads
	// them concurrently.
	procMu sync.Mutex
	cmd    *exec.Cmd
	cancel context.CancelFunc

	// lastOutput is the provider's most recent non-blank output line. It is
	// where cloudflared, ngrok and tailscale say what they are doing, so it
	// is quoted when the provider exits or fails to produce a URL in time.
	lastOutput atomic.Pointer[string]

	// Callbacks
	onURL func(url string)
}

// TunnelInfo contains information about a tunnel.
type TunnelInfo struct {
	ID        string   `json:"id"`
	Provider  Provider `json:"provider"`
	State     string   `json:"state"`
	PublicURL string   `json:"public_url,omitempty"`
	LocalAddr string   `json:"local_addr"`
	Path      string   `json:"path,omitempty"`
	Error     string   `json:"error,omitempty"`
}

// New creates a new tunnel with the given configuration.
func New(config Config) *Tunnel {
	if config.LocalHost == "" {
		config.LocalHost = "localhost"
	}
	return &Tunnel{
		config: config,
		done:   make(chan struct{}),
	}
}

// OnURL sets a callback that's invoked when the public URL is discovered.
func (t *Tunnel) OnURL(fn func(url string)) {
	t.onURL = fn
}

// Start starts the tunnel and returns immediately.
// Use WaitForURL to wait for the public URL to be available.
func (t *Tunnel) Start(ctx context.Context) error {
	if !t.compareAndSwapState(StateIdle, StateStarting) {
		return fmt.Errorf("tunnel already started")
	}

	ctx, cancel := context.WithCancel(ctx)
	// Publish cancel before any provider goroutine starts so a concurrent
	// Stop always has happens-before visibility of it.
	t.procMu.Lock()
	t.cancel = cancel
	t.procMu.Unlock()

	if t.config.Named != nil && t.config.Provider != ProviderCloudflare {
		err := fmt.Errorf("named tunnels are cloudflare-only, got provider %s", t.config.Provider)
		t.setState(StateFailed)
		t.setError(err)
		t.closeDone()
		return err
	}

	switch t.config.Provider {
	case ProviderCloudflare:
		return t.startCloudflare(ctx)
	case ProviderNgrok:
		return t.startNgrok(ctx)
	case ProviderTailscale:
		return t.startTailscale(ctx)
	default:
		err := fmt.Errorf("unsupported tunnel provider: %s", t.config.Provider)
		t.setState(StateFailed)
		t.setError(err)
		t.closeDone()
		return err
	}
}

// Stop stops the tunnel.
func (t *Tunnel) Stop(ctx context.Context) error {
	if t.State() == StateIdle {
		return nil
	}

	t.procMu.Lock()
	cancel := t.cancel
	cmd := t.cmd
	t.procMu.Unlock()

	if cancel != nil {
		cancel()
	}

	if cmd != nil && cmd.Process != nil && !t.exited() {
		// A process that has already exited (it crashed, or the cancel above
		// got there first) is the outcome Stop wants, not a failure to stop.
		// The error for that is not portable — os.ErrProcessDone on Unix, a
		// raw "invalid argument" or "access denied" on Windows depending on
		// whether it has been reaped — so the test is whether the process is
		// in fact gone, which the wait goroutine reports by closing done.
		if err := cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			select {
			case <-t.done:
			case <-time.After(killExitGrace):
				return fmt.Errorf("failed to kill tunnel process: %w", err)
			case <-ctx.Done():
				return fmt.Errorf("failed to kill tunnel process: %w", err)
			}
		}
	}

	// Wait for done or context timeout
	select {
	case <-t.done:
	case <-ctx.Done():
		return ctx.Err()
	}

	t.setState(StateStopped)
	return nil
}

// killExitGrace is how long Stop waits for a process whose Kill failed to
// turn out to have exited on its own.
const killExitGrace = time.Second

// exited reports whether the tunnel process has been reaped (done is closed).
func (t *Tunnel) exited() bool {
	select {
	case <-t.done:
		return true
	default:
		return false
	}
}

// State returns the current tunnel state.
func (t *Tunnel) State() State {
	return State(t.state.Load())
}

// PublicURL returns the public URL if available.
func (t *Tunnel) PublicURL() string {
	if ptr := t.publicURL.Load(); ptr != nil {
		return *ptr
	}
	return ""
}

// Info returns information about the tunnel.
func (t *Tunnel) Info() TunnelInfo {
	info := TunnelInfo{
		ID:        t.config.ID,
		Provider:  t.config.Provider,
		State:     t.State().String(),
		PublicURL: t.PublicURL(),
		LocalAddr: fmt.Sprintf("%s:%d", t.config.LocalHost, t.config.LocalPort),
		Path:      t.config.Path,
	}

	t.errMu.RLock()
	if t.err != nil {
		info.Error = t.err.Error()
	}
	t.errMu.RUnlock()

	return info
}

// Path returns the project path for this tunnel.
func (t *Tunnel) Path() string {
	return t.config.Path
}

// ID returns the tunnel ID.
func (t *Tunnel) ID() string {
	return t.config.ID
}

// WaitForURL waits for the public URL to be available or timeout.
func (t *Tunnel) WaitForURL(ctx context.Context) (string, error) {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-t.done:
			if url := t.PublicURL(); url != "" {
				return url, nil
			}
			t.errMu.RLock()
			err := t.err
			t.errMu.RUnlock()
			if err != nil {
				return "", err
			}
			return "", fmt.Errorf("tunnel closed without providing URL")
		case <-ticker.C:
			if url := t.PublicURL(); url != "" {
				return url, nil
			}
		}
	}
}

// LastOutput returns the provider's most recent non-blank output line,
// capped at maxExitTailBytes, or "" when it has printed nothing.
func (t *Tunnel) LastOutput() string {
	last := t.lastOutput.Load()
	if last == nil {
		return ""
	}
	tail := *last
	if len(tail) > maxExitTailBytes {
		tail = tail[:maxExitTailBytes] + "…"
	}
	return tail
}

// Done returns a channel that's closed when the tunnel exits.
func (t *Tunnel) Done() <-chan struct{} {
	return t.done
}

func (t *Tunnel) setState(s State) {
	t.state.Store(uint32(s))
}

func (t *Tunnel) compareAndSwapState(old, new State) bool {
	return t.state.CompareAndSwap(uint32(old), uint32(new))
}

// markConnected transitions Starting → Connected. Output-parse goroutines
// use this instead of an unconditional store so a late URL line can never
// resurrect a tunnel that already reached a terminal state (Failed/Stopped).
func (t *Tunnel) markConnected() {
	t.compareAndSwapState(StateStarting, StateConnected)
}

// closeDone closes t.done exactly once; every terminal path (start error,
// process exit, unsupported provider) must funnel through it so Stop and
// WaitForURL never block until context expiry.
func (t *Tunnel) closeDone() {
	t.doneOnce.Do(func() { close(t.done) })
}

func (t *Tunnel) setError(err error) {
	t.errMu.Lock()
	t.err = err
	t.errMu.Unlock()
}

func (t *Tunnel) setPublicURL(url string) {
	t.publicURL.Store(&url)
	if t.onURL != nil {
		t.onURL(url)
	}
}

// cloudflared output patterns
var (
	// Matches: https://something-something.trycloudflare.com
	cloudflareURLPattern = regexp.MustCompile(`https://[a-z0-9-]+\.trycloudflare\.com`)
)

func (t *Tunnel) startCloudflare(ctx context.Context) error {
	binary := t.config.BinaryPath
	if binary == "" {
		binary = "cloudflared"
	}

	// Check if binary exists
	if _, err := exec.LookPath(binary); err != nil {
		err = fmt.Errorf("cloudflared not found in PATH: %w", err)
		t.setState(StateFailed)
		t.setError(err)
		t.closeDone()
		return err
	}

	localURL := config.LocalHTTPURL(t.config.LocalHost, t.config.LocalPort)
	args, err := cloudflareArgs(localURL, t.config.Named)
	if err != nil {
		t.setState(StateFailed)
		t.setError(err)
		t.closeDone()
		return err
	}
	// cloudflared logs to stderr.
	return t.launch(ctx, "cloudflared", exec.CommandContext(ctx, binary, args...), t.cloudflareLine)
}

// cloudflareArgs builds the cloudflared command line: a quick tunnel when
// named is nil, otherwise `tunnel run` for the named tunnel by UUID.
func cloudflareArgs(localURL string, named *NamedCloudflare) ([]string, error) {
	if named == nil {
		return []string{"tunnel", "--url", localURL}, nil
	}
	if named.TunnelID == "" || named.Hostname == "" || named.CredentialsFile == "" {
		return nil, fmt.Errorf("named cloudflare tunnel needs a tunnel id, hostname and credentials file")
	}
	if err := checkCredentialsFile(named.CredentialsFile); err != nil {
		return nil, err
	}
	return []string{
		"tunnel", "--no-autoupdate", "run",
		"--credentials-file", named.CredentialsFile,
		"--url", localURL,
		named.TunnelID,
	}, nil
}

// checkCredentialsFile refuses a credential file that is missing, not a
// regular file, or readable by anyone but its owner. A world-readable tunnel
// credential lets any local user run the tunnel; refusing to start keeps the
// fix (chmod 600) in front of the operator instead of shipping the leak.
func checkCredentialsFile(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("tunnel credentials file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("tunnel credentials file %s is not a regular file", path)
	}
	// Windows has no POSIX mode bits to check. WSL reports /mnt/c files as
	// 0777 and is refused here: keep the credential under the WSL home.
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("tunnel credentials file %s is mode %04o; it must be private to its owner (chmod 600)", path, info.Mode().Perm())
	}
	return nil
}

// cloudflaredRegistered is the line cloudflared logs once an edge connection
// for a named tunnel is up. It is the tunnel's own readiness signal.
const cloudflaredRegistered = "Registered tunnel connection"

func (t *Tunnel) parseCloudflareOutput(r io.Reader) {
	forEachLine(r, t.cloudflareLine)
}

func (t *Tunnel) cloudflareLine(line string) {
	if named := t.config.Named; named != nil {
		if strings.Contains(line, cloudflaredRegistered) && t.PublicURL() == "" {
			t.setPublicURL("https://" + named.Hostname)
			t.markConnected()
		}
		return
	}
	if match := cloudflareURLPattern.FindString(line); match != "" {
		t.setPublicURL(match)
		t.markConnected()
	}
}

// maxTunnelLineBytes is the longest provider output line that is parsed.
const maxTunnelLineBytes = 64 << 10

// forEachLine calls fn for every line of a tunnel child's output, and keeps
// reading until the stream ends.
//
// Draining to the end is the point. The child writes to a pipe; once nobody
// reads it the pipe fills and the child blocks in its next log write, which
// stalls the tunnel. bufio.Scanner stops for good at the first line over its
// 64 KiB limit, which did exactly that. Here a line over maxTunnelLineBytes is
// discarded whole, up to its newline — never parsed as a truncated head, and
// its tail never handed to fn as if it were a fresh line — and reading goes on
// with the next one. Nothing of an over-long line is buffered past the cap.
func forEachLine(r io.Reader, fn func(line string)) {
	br := bufio.NewReader(r)
	var line []byte
	discarding := false
	for {
		chunk, isPrefix, err := br.ReadLine()
		if err != nil {
			return
		}
		if !discarding {
			if len(line)+len(chunk) > maxTunnelLineBytes {
				discarding = true
				line = line[:0]
			} else {
				line = append(line, chunk...)
			}
		}
		if isPrefix {
			continue
		}
		if !discarding {
			fn(string(line))
		}
		line = line[:0]
		discarding = false
	}
}

// providerWaitDelay bounds how long Wait keeps reading a provider's output
// after the process itself has exited, in case a descendant still holds the
// pipe open.
const providerWaitDelay = 2 * time.Second

// maxExitTailBytes caps the provider output line quoted in an exit error.
const maxExitTailBytes = 512

// launch starts a provider process with its stdout and stderr both read, line
// by line, into handle, and supervises it until it exits.
//
// Output goes through a pipe launch owns rather than StdoutPipe/StderrPipe:
// with those, Wait closes the read end as soon as the process exits, racing
// the reader for the last lines — which are exactly the lines that say why a
// tunnel died. Here Wait returns only once everything the process wrote has
// been handed to the reader (bounded by providerWaitDelay), so the last line
// is known when the exit is reported.
//
// Any exit the caller did not ask for (ctx still live) marks the tunnel
// failed, including a clean exit status: a tunnel provider that stops on its
// own has stopped tunnelling, whatever its status says. The error quotes the
// provider's last output line, which is where cloudflared, ngrok and
// tailscale state the reason (a missing authtoken, an unreachable edge).
func (t *Tunnel) launch(ctx context.Context, name string, cmd *exec.Cmd, handle func(line string)) error {
	pr, pw := io.Pipe()
	cmd.Stdout = pw
	cmd.Stderr = pw
	cmd.WaitDelay = providerWaitDelay

	if err := cmd.Start(); err != nil {
		_ = pw.Close()
		_ = pr.Close()
		return t.fail(fmt.Errorf("failed to start %s: %w", name, err))
	}

	t.procMu.Lock()
	t.cmd = cmd
	t.procMu.Unlock()

	parsed := make(chan struct{})
	go func() {
		defer close(parsed)
		forEachLine(pr, func(line string) {
			if trimmed := strings.TrimSpace(line); trimmed != "" {
				t.lastOutput.Store(&trimmed)
			}
			handle(line)
		})
	}()

	go func() {
		defer t.closeDone()
		waitErr := cmd.Wait()
		_ = pw.Close()
		<-parsed
		if ctx.Err() != nil {
			return // Stop (or the caller's context) ended it: not a failure.
		}
		if waitErr == nil {
			waitErr = errors.New("exited unexpectedly with status 0")
		}
		err := fmt.Errorf("%s exited: %w", name, waitErr)
		if tail := t.LastOutput(); tail != "" {
			err = fmt.Errorf("%w (last output: %s)", err, tail)
		}
		t.setError(err)
		t.setState(StateFailed)
	}()

	return nil
}

// fail records a start-time failure as the tunnel's terminal state and
// returns it.
func (t *Tunnel) fail(err error) error {
	t.setState(StateFailed)
	t.setError(err)
	t.closeDone()
	return err
}

// ngrok output patterns
var (
	// Matches a URL on one of ngrok's own domains: https://abc123.ngrok.io,
	// .ngrok.app, .ngrok.dev, .ngrok-free.app, or .ngrok-free.dev (the free
	// domain current accounts get). The set is the one the proxy's control
	// WebSocket recognises for an ngrok tunnel (tunnelKindDomains).
	ngrokURLPattern = regexp.MustCompile(`https://[a-z0-9-]+\.(?:ngrok-free\.(?:app|dev)|ngrok\.(?:app|dev|io))`)
)

func (t *Tunnel) startNgrok(ctx context.Context) error {
	binary := t.config.BinaryPath
	if binary == "" {
		binary = "ngrok"
	}

	// Check if binary exists
	if _, err := exec.LookPath(binary); err != nil {
		err = fmt.Errorf("ngrok not found in PATH: %w", err)
		t.setState(StateFailed)
		t.setError(err)
		t.closeDone()
		return err
	}

	cmd := exec.CommandContext(ctx, binary, ngrokArgs(t.config.LocalHost, t.config.LocalPort)...)
	return t.launch(ctx, "ngrok", cmd, t.ngrokLine)
}

// ngrokArgs is the `ngrok http` command line. The log flags are what make the
// public URL readable at all: with its output on a pipe ngrok runs no console
// UI (console_ui defaults to "iftty") and logs nothing (log defaults to
// false), so without them nothing ever names the URL and WaitForURL waits for
// a line that never comes. With them ngrok prints, among its logfmt lines,
// `msg="started tunnel" ... url=https://<name>.ngrok-free.app`.
func ngrokArgs(host string, port int) []string {
	return []string{"http", "--log=stdout", "--log-format=logfmt", ngrokTarget(host, port)}
}

// isLoopbackHost reports whether host is this machine's loopback, as a name or
// an address. An empty host counts: New defaults it to "localhost".
func isLoopbackHost(host string) bool {
	host = strings.Trim(host, "[]")
	if host == "" || strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// ngrokTarget is the forwarding target for `ngrok http`: the bare port for a
// loopback host (the form this always used), host:port for any other, so a
// configured LocalHost is forwarded to rather than silently replaced by
// localhost.
func ngrokTarget(host string, port int) string {
	if isLoopbackHost(host) {
		return strconv.Itoa(port)
	}
	return net.JoinHostPort(strings.Trim(host, "[]"), strconv.Itoa(port))
}

func (t *Tunnel) parseNgrokOutput(r io.Reader) {
	forEachLine(r, t.ngrokLine)
}

func (t *Tunnel) ngrokLine(line string) {
	if match := ngrokURLPattern.FindString(line); match != "" {
		t.setPublicURL(match)
		t.markConnected()
	}
}

// tailscale serve output patterns
var (
	// Matches tailnet MagicDNS URLs like https://machine.tailnet.ts.net.
	// MagicDNS labels are DNS-safe (lowercase letters, digits, hyphens);
	// the trailing path is consumed up to first whitespace.
	tailscaleURLPattern = regexp.MustCompile(`https://[a-z0-9-]+\.[a-z0-9.-]+\.ts\.net\S*`)
)

// tailscaleDNSFallbackDelay is the grace period after process start before
// we synthesise the MagicDNS URL when serve hasn't printed one. Var (not
// const) so tests can shorten it.
var tailscaleDNSFallbackDelay = 750 * time.Millisecond

func (t *Tunnel) startTailscale(ctx context.Context) error {
	binary := t.config.BinaryPath
	if binary == "" {
		binary = "tailscale"
	}

	if _, err := exec.LookPath(binary); err != nil {
		err = fmt.Errorf("tailscale not found in PATH: %w", err)
		t.setState(StateFailed)
		t.setError(err)
		t.closeDone()
		return err
	}

	// `tailscale serve <port>` forwards to this machine's loopback. A LocalHost
	// that is not loopback cannot be honoured by that form, and used to be
	// dropped without a word — the tunnel came up pointing at localhost
	// instead. Refuse it, so the configuration either works or says why.
	if !isLoopbackHost(t.config.LocalHost) {
		err := fmt.Errorf("tailscale serve forwards to localhost only; local host %q is not supported (use cloudflare or ngrok for a non-local target)", t.config.LocalHost)
		t.setState(StateFailed)
		t.setError(err)
		t.closeDone()
		return err
	}

	// Foreground `tailscale serve <port>` — blocks until killed and
	// tears down the serve mapping on exit. Do NOT pass --bg; that
	// would detach from the lifecycle and leave the serve config
	// behind on Stop().
	cmd := exec.CommandContext(ctx, binary, "serve", fmt.Sprintf("%d", t.config.LocalPort))

	// launch reads stdout and stderr both: serve's URL output stream is not
	// version-stable.
	if err := t.launch(ctx, "tailscale serve", cmd, t.tailscaleLine); err != nil {
		return err
	}

	// MagicDNS fallback: if serve hasn't printed a parseable URL within
	// the grace window, query `tailscale status --json` for Self.DNSName
	// and synthesise the URL. Belt-and-suspenders so WaitForURL resolves
	// even if a future tailscale release changes serve's stdout format.
	go func() {
		select {
		case <-time.After(tailscaleDNSFallbackDelay):
		case <-t.done:
			return
		}
		if t.PublicURL() != "" {
			return
		}
		if name := platform.TailscaleDNSName(ctx); name != "" {
			t.setPublicURL("https://" + name)
			t.markConnected()
		}
	}()

	return nil
}

func (t *Tunnel) parseTailscaleOutput(r io.Reader) {
	forEachLine(r, t.tailscaleLine)
}

func (t *Tunnel) tailscaleLine(line string) {
	if match := tailscaleURLPattern.FindString(line); match != "" {
		t.setPublicURL(match)
		t.markConnected()
	}
}
