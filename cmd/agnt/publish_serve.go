// publish_serve.go implements `agnt publish serve --dir` (S9 of the
// walkthrough-publish epic): serve a folder of walkthrough JSON files behind
// stable public share URLs, re-publishing on edit and printing the FULL public
// URL — origin included, folding in a tunnel origin when one is requested.
//
// Why a full URL is the point of this command: the control plane returns the
// bare relative path "/s/{token}", which is unusable as a thing you hand to
// someone. Everything else here exists to make that URL keep working: the
// token-per-file store keeps it stable across edits, and the watcher keeps the
// content behind it current.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/spf13/cobra"

	"github.com/standardbeagle/agnt/internal/config"
	"github.com/standardbeagle/agnt/internal/httpcaps"
	"github.com/standardbeagle/agnt/internal/platform"
	"github.com/standardbeagle/agnt/internal/proxy"
	"github.com/standardbeagle/agnt/internal/publish"
	"github.com/standardbeagle/agnt/internal/tunnel"
)

const (
	// defaultPublishServeAddr is the local listen address for the public plane.
	defaultPublishServeAddr = ":8899"

	// publishServePollDrvFS / publishServePollLocal are the metadata poll
	// periods for the watched directory. Polling runs ALONGSIDE fsnotify, never
	// instead of it, mirroring internal/sshclient.DropWatcher: notifications on
	// WSL's /mnt/<drive> DrvFS/9P mounts are unreliable
	// (.claude/rules/wsl-audit.md), so the poll is the correctness path and
	// fsnotify is the promptness path. A DrvFS directory gets the tighter period
	// because there the poll is doing the real work.
	publishServePollDrvFS = 500 * time.Millisecond
	publishServePollLocal = 3 * time.Second
)

// rotate-on-resume policy: what serve does when it resumes a share persisted by
// an EARLIER run, whose plaintext token is unrecoverable (only sha256 is kept).
const (
	// rotateResumeYes mints a fresh token and reprints a working URL — the old
	// run's links die. Default; preserves the command's whole point (a URL).
	rotateResumeYes = "yes"
	// rotateResumeNo keeps the old token in place: the store still verifies it, so
	// links shared by the earlier run keep working, but this run cannot reprint
	// them. The least-destructive option.
	rotateResumeNo = "no"
	// rotateResumeFail refuses to start, so a scripted/CI invocation cannot
	// silently invalidate live links (consent-flag principle,
	// .claude/rules/lessons-ssh-transport.md §7).
	rotateResumeFail = "fail"
)

var publishCmd = &cobra.Command{
	Use:   "publish",
	Short: "Publish walkthroughs behind public share URLs",
}

var publishServeCmd = &cobra.Command{
	Use:   "serve",
	Short: "Serve a folder of walkthrough JSON files at public share URLs",
	Long: `Serve every *.json walkthrough in a folder behind an unguessable public share URL.

Each file gets one share token, and that token survives edits: editing a file
publishes a new revision under the SAME URL, so a link you already handed out
keeps working and shows the update. Deleting a file revokes its share
immediately and irreversibly.

The full public URL is printed for every share on boot, and printed again with
the tunnel origin once a tunnel comes up.

Examples:
  agnt publish serve --dir ./walkthroughs
  agnt publish serve --dir ./walkthroughs --addr :9000
  agnt publish serve --dir ./walkthroughs --tunnel cloudflare`,
	RunE: runPublishServeCmd,
}

var publishServeFlags struct {
	dir            string
	addr           string
	tunnel         string
	storeDir       string
	rotateOnResume string
}

func init() {
	f := publishServeCmd.Flags()
	f.StringVar(&publishServeFlags.dir, "dir", "", "directory of *.json walkthroughs to publish (required)")
	f.StringVar(&publishServeFlags.addr, "addr", defaultPublishServeAddr, "local listen address for the public plane")
	f.StringVar(&publishServeFlags.tunnel, "tunnel", "", "expose the public plane through a tunnel: cloudflare | ngrok | tailscale")
	f.StringVar(&publishServeFlags.storeDir, "store", "", "share store directory (default: per-folder dir under the user cache)")
	f.StringVar(&publishServeFlags.rotateOnResume, "rotate-on-resume", rotateResumeYes,
		"when resuming a prior run whose token is unrecoverable: yes (rotate + reprint, old links die) | no (keep old links working, don't reprint) | fail (refuse to start)")

	publishCmd.AddCommand(publishServeCmd)
	rootCmd.AddCommand(publishCmd)
}

func runPublishServeCmd(cmd *cobra.Command, args []string) error {
	ctx, cancel := signalContext()
	defer cancel()
	return runPublishServe(ctx, publishServeOptions{
		Dir:            publishServeFlags.dir,
		Addr:           publishServeFlags.addr,
		Tunnel:         publishServeFlags.tunnel,
		StoreDir:       publishServeFlags.storeDir,
		RotateOnResume: publishServeFlags.rotateOnResume,
		Out:            cmd.OutOrStdout(),
	})
}

// publishServeOptions is the whole input to a serve run. It exists so the
// command body is drivable in-process by tests without cobra, a real signal
// handler, or a fixed port.
type publishServeOptions struct {
	Dir      string
	Addr     string
	Tunnel   string
	StoreDir string
	// RotateOnResume is the yes|no|fail policy for a share resumed from an earlier
	// run whose token is unrecoverable. Empty is treated as "yes".
	RotateOnResume string
	Out            io.Writer

	// PollInterval overrides the metadata poll period. Zero picks the DrvFS or
	// local default for Dir.
	PollInterval time.Duration
	// OnListen, when non-nil, is called with the actually-bound address once the
	// public listener is up. Tests use it instead of racing on log output or
	// guessing a port.
	OnListen func(addr string)
	// OnPublish, when non-nil, is called after every successful publish pass
	// with the origin used for the printed URLs and the shares in file order.
	OnPublish func(origin string, shares []publishedShare)
}

// publishedShare is one served file's share, as printed and as handed to
// OnPublish. Token is the plaintext share token — the URL IS the credential
// here, which is the entire point of this command.
type publishedShare struct {
	File       string
	Title      string
	ID         string
	Token      string
	RevisionID string
}

// URL renders this share's full public URL under origin.
func (p publishedShare) URL(origin string) string {
	return strings.TrimSuffix(origin, "/") + "/s/" + p.Token
}

// loadedWalkthrough is one validated walkthrough plus the file it came from.
type loadedWalkthrough struct {
	File string
	PW   *publish.PublishedWalkthrough
}

// loadWalkthroughDir reads every *.json file in dir as one walkthrough,
// strict-decoded and fully validated through the existing publish validators.
//
// DECISION — one bad file aborts the load; it is NEVER skipped (Silent Failure
// Prohibition, .claude/rules/daemon-architecture.md). The offending file is
// named in the error. Two reasons, and the second is a correctness requirement
// rather than a preference:
//
//   - A demo that silently comes up missing one walkthrough looks like it
//     worked. The publisher finds out from their audience.
//   - A publish pass ends in Store.ReconcileFiles, which REVOKES — irreversibly
//     — every file-backed share whose file is absent from the set it is handed.
//     A partial set therefore reads as "those files were deleted". Refusing to
//     ever return a partial set is what makes a transient read error unable to
//     mass-revoke live shares; the guard is structural, not a check callers must
//     remember. The same applies to the directory read itself: an unreadable
//     directory (permissions, a transiently-absent /mnt mount) is an error, not
//     an empty folder.
func loadWalkthroughDir(dir string) ([]loadedWalkthrough, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read walkthrough dir %s: %w", dir, err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names) // deterministic publish + print order
	out := make([]loadedWalkthrough, 0, len(names))
	for _, name := range names {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return nil, fmt.Errorf("walkthrough %s: read: %w", name, err)
		}
		pw, err := publish.DecodePublishedWalkthrough(data)
		if err != nil {
			return nil, fmt.Errorf("walkthrough %s: %w", name, err)
		}
		out = append(out, loadedWalkthrough{File: name, PW: pw})
	}
	return out, nil
}

// publishServer holds one serve run's mutable state: the durable share store,
// the plaintext tokens minted during THIS run (the store keeps only hashes), and
// the current public origin.
type publishServer struct {
	dir            string
	store          *publish.Store
	out            io.Writer
	rotateOnResume string // yes | no | fail; "" behaves as yes

	mu     sync.Mutex
	tokens map[string]string // file -> plaintext token, this run only
	shares []publishedShare
	origin string
	// resumeKeptFiles remembers files whose earlier-run token was KEPT (not
	// rotated) under --rotate-on-resume=no, so the "kept" notice prints once
	// rather than on every subsequent republish of an unchanged file.
	resumeKeptFiles map[string]bool
	// emptyHeld is the two-poll guard state: true once a zero-walkthrough read
	// has been observed and held once, pending a confirming second read before a
	// mass revoke is allowed. Cleared by any non-empty read.
	emptyHeld bool

	onPublish func(origin string, shares []publishedShare)
}

// publishPass loads the directory and publishes every walkthrough, then — and
// only then — reconciles deletions. Any failure returns before the reconcile,
// so a failed or partial pass can never be read as "the files are gone".
//
// held reports that this pass observed a zero-walkthrough directory for the
// FIRST time and deliberately did nothing (no publish, no reconcile) rather than
// mass-revoke on a single empty read — see the two-poll guard below. A held pass
// leaves every share serving and asks the caller not to advance its change
// fingerprint, so the next poll re-reads and either confirms or clears the empty.
func (s *publishServer) publishPass() (held bool, err error) {
	loaded, err := loadWalkthroughDir(s.dir)
	if err != nil {
		return false, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Two-poll guard against an unmounted-yet-present directory. os.ReadDir on a
	// dropped /mnt mount succeeds with ZERO entries, indistinguishable from "the
	// operator deleted every file" — and ReconcileFiles below revokes irreversibly.
	// So a zero-walkthrough read never revokes on its own: the first one is HELD
	// (shares keep serving, the caller does not advance its change fingerprint) and
	// only a second consecutive empty read is trusted enough to mass-revoke. A
	// genuine delete-everything still converges — one poll later.
	// (.claude/rules/publish-security-review-lessons.md §12)
	if len(loaded) == 0 {
		if !s.emptyHeld {
			s.emptyHeld = true
			if len(s.shares) > 0 {
				fmt.Fprintf(s.out, "publish serve: %s read as empty; holding %d live share(s) for a confirming second read before revoking (a transiently-unmounted directory also reads as empty)\n", s.dir, len(s.shares))
			}
			return true, nil
		}
		// Second consecutive empty read: trusted. Fall through to publish nothing
		// and let ReconcileFiles revoke the now-genuinely-absent files.
	} else {
		s.emptyHeld = false
	}

	shares := make([]publishedShare, 0, len(loaded))
	var resumeRotated, resumeKept []string // files whose token was resumed from an earlier run
	for _, lw := range loaded {
		id, token, revID, err := s.store.PublishFile(lw.PW, s.dir, lw.File)
		if err != nil {
			return false, fmt.Errorf("publish %s: %w", lw.File, err)
		}
		if token == "" {
			// An unchanged file or an in-run edit: no new secret was minted, so
			// reuse the plaintext this run already holds.
			token = s.tokens[lw.File]
		}
		if token == "" && !s.resumeHandled(lw.File) {
			// A share persisted by an EARLIER serve run: only sha256(token) was
			// kept, so the plaintext is unrecoverable by design. What we do about it
			// is the operator's --rotate-on-resume choice, because rotating kills
			// every URL that run handed out.
			switch s.resumePolicy() {
			case rotateResumeFail:
				return false, fmt.Errorf("%s was shared by an earlier run whose token is unrecoverable; --rotate-on-resume=fail refuses to silently invalidate it (use =yes to rotate and reprint a fresh URL, or =no to keep the old link working without reprinting)", lw.File)
			case rotateResumeNo:
				// Keep the old token: the store still verifies it, so links from the
				// earlier run keep working. We just cannot reprint them (token stays "").
				resumeKept = append(resumeKept, lw.File)
				if s.resumeKeptFiles == nil {
					s.resumeKeptFiles = map[string]bool{}
				}
				s.resumeKeptFiles[lw.File] = true
			default: // rotateResumeYes
				token, err = s.store.Rotate(id)
				if err != nil {
					return false, fmt.Errorf("publish %s: rotate token: %w", lw.File, err)
				}
				resumeRotated = append(resumeRotated, lw.File)
			}
		}
		s.tokens[lw.File] = token
		shares = append(shares, publishedShare{
			File: lw.File, Title: lw.PW.Title, ID: id, Token: token, RevisionID: string(revID),
		})
	}
	s.printResumeNoticeLocked(resumeRotated, resumeKept)

	// Reconcile ONLY from a complete, fully-published set. ReconcileFiles
	// revokes every file-backed share absent from present[], and revocation has
	// no undo — see loadWalkthroughDir's decision note.
	present := make([]string, 0, len(shares))
	for _, sh := range shares {
		present = append(present, sh.File)
	}
	revoked, err := s.store.ReconcileFiles(s.dir, present)
	if err != nil {
		return false, fmt.Errorf("reconcile deleted files: %w", err)
	}
	for _, id := range revoked {
		fmt.Fprintf(s.out, "publish serve: revoked share %s — its file is gone\n", id)
	}
	// Drop tokens for files that went away so a returning filename cannot reuse
	// a token the store has already revoked.
	for file := range s.tokens {
		if !containsString(present, file) {
			delete(s.tokens, file)
		}
	}

	s.shares = shares
	origin := s.origin
	if s.onPublish != nil {
		s.onPublish(origin, shares)
	}
	s.printSharesLocked(origin)
	return false, nil
}

// setOrigin swaps the public origin (a tunnel coming up) and reprints every
// share URL against it.
func (s *publishServer) setOrigin(origin string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.origin = origin
	if s.onPublish != nil {
		s.onPublish(origin, s.shares)
	}
	s.printSharesLocked(origin)
}

// printSharesLocked prints the FULL public URL of every share. Caller holds mu.
// A share with no plaintext token this run (kept under --rotate-on-resume=no) is
// printed without a URL: this run cannot reconstruct it from the stored hash.
func (s *publishServer) printSharesLocked(origin string) {
	fmt.Fprintf(s.out, "publish serve: %d share(s) at %s\n", len(s.shares), origin)
	for _, sh := range s.shares {
		if sh.Token == "" {
			fmt.Fprintf(s.out, "  %s  %q  (existing link preserved; not reprintable — rotate to mint a new URL)\n", sh.File, sh.Title)
			continue
		}
		fmt.Fprintf(s.out, "  %s  %q  %s\n", sh.File, sh.Title, sh.URL(origin))
	}
}

// resumePolicy returns the effective yes|no|fail policy ("" behaves as yes).
func (s *publishServer) resumePolicy() string {
	if s.rotateOnResume == "" {
		return rotateResumeYes
	}
	return s.rotateOnResume
}

// resumeHandled reports whether a --rotate-on-resume=no decision has already been
// recorded for file, so its notice does not repeat on later republishes.
func (s *publishServer) resumeHandled(file string) bool {
	return s.resumeKeptFiles[file]
}

// printResumeNoticeLocked prints ONE distinct block per resume outcome, with a
// count and explicit wording that the earlier run's links now 404. This is the
// loud notice S9's single inline line was too quiet to be (an operator who
// shared a link and restarted has silently broken it for real viewers). Caller
// holds mu.
func (s *publishServer) printResumeNoticeLocked(rotated, kept []string) {
	if len(rotated) > 0 {
		fmt.Fprintf(s.out, "publish serve: RESUME — rotated %d share(s) recovered from an earlier run.\n", len(rotated))
		fmt.Fprintf(s.out, "  The earlier run's plaintext tokens are unrecoverable (only sha256 is stored),\n")
		fmt.Fprintf(s.out, "  so any URL you shared from that run now returns 404 to its viewers.\n")
		fmt.Fprintf(s.out, "  Fresh URLs for these files are listed below:\n")
		for _, f := range rotated {
			fmt.Fprintf(s.out, "    %s\n", f)
		}
	}
	if len(kept) > 0 {
		fmt.Fprintf(s.out, "publish serve: RESUME — kept %d share(s) from an earlier run WITHOUT rotating (--rotate-on-resume=no).\n", len(kept))
		fmt.Fprintf(s.out, "  The earlier run's links still work (the store verifies them), but this run\n")
		fmt.Fprintf(s.out, "  cannot reprint their URLs; rotate to mint a fresh printable token.\n")
		for _, f := range kept {
			fmt.Fprintf(s.out, "    %s\n", f)
		}
	}
}

func containsString(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

// runPublishServe is the whole command: open the store, publish the folder,
// serve the public plane, optionally raise a tunnel, and watch for edits until
// ctx is cancelled.
func runPublishServe(ctx context.Context, opts publishServeOptions) error {
	if opts.Out == nil {
		opts.Out = os.Stdout
	}
	if opts.Dir == "" {
		return errors.New("publish serve: --dir is required")
	}
	dir, err := filepath.Abs(opts.Dir)
	if err != nil {
		return fmt.Errorf("publish serve: resolve --dir: %w", err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("publish serve: --dir: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("publish serve: --dir %s is not a directory", dir)
	}
	addr := opts.Addr
	if addr == "" {
		addr = defaultPublishServeAddr
	}

	// Parse --tunnel BEFORE binding anything, so a typo fails immediately
	// instead of after the folder is already published.
	var provider tunnel.Provider
	if opts.Tunnel != "" {
		provider, err = tunnel.ParseProvider(opts.Tunnel)
		if err != nil {
			return fmt.Errorf("publish serve: %w", err)
		}
	}

	// Validate --rotate-on-resume at the boundary (Config Authority): an unknown
	// value is a loud rejection, not a silent fallback to the default.
	rotateOnResume := strings.ToLower(strings.TrimSpace(opts.RotateOnResume))
	if rotateOnResume == "" {
		rotateOnResume = rotateResumeYes
	}
	switch rotateOnResume {
	case rotateResumeYes, rotateResumeNo, rotateResumeFail:
	default:
		return fmt.Errorf("publish serve: --rotate-on-resume %q is not one of yes|no|fail", opts.RotateOnResume)
	}

	storeDir := opts.StoreDir
	if storeDir == "" {
		if storeDir, err = defaultPublishServeStoreDir(dir); err != nil {
			return fmt.Errorf("publish serve: %w", err)
		}
	}
	store, err := publish.New(filepath.Join(storeDir, "shares"), nil)
	if err != nil {
		return fmt.Errorf("publish serve: open share store: %w", err)
	}
	// Honor the operator's feedback{} block (Config Authority): a parsed key that
	// does not reach the live limiter is a bug, so load it here rather than
	// hardcoding defaults. A malformed config fails loud.
	appCfg, err := config.LoadGlobalConfig()
	if err != nil {
		return fmt.Errorf("publish serve: load config: %w", err)
	}
	limits := feedbackLimitsFor(appCfg.Feedback)
	// Persist viewer feedback rather than accepting and dropping it: a nil sink
	// is a documented safe stub, but silently discarding what a viewer typed is
	// exactly the failure this repo prohibits.
	feedback, err := publish.NewFeedbackStore(filepath.Join(storeDir, "feedback"), limits, nil)
	if err != nil {
		return fmt.Errorf("publish serve: open feedback store: %w", err)
	}

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("publish serve: listen on %s: %w", addr, err)
	}
	boundAddr := ln.Addr().String()

	srv := &publishServer{
		dir:            dir,
		store:          store,
		out:            opts.Out,
		rotateOnResume: rotateOnResume,
		tokens:         make(map[string]string),
		origin:         localOrigin(boundAddr),
		onPublish:      opts.OnPublish,
	}

	fmt.Fprintf(opts.Out, "publish serve: watching %s, listening on %s\n", dir, srv.origin)
	// Loud exposure advisory: a bare :port (or 0.0.0.0 / ::) binds ALL interfaces,
	// LAN-reachable without a tunnel. Per the operator posture (AGNT.md § Exposure
	// Posture) the intended default is loopback + --tunnel; surface a non-loopback
	// bind rather than reject it (exposure is the operator's decision).
	if host, _, splitErr := net.SplitHostPort(boundAddr); splitErr == nil {
		if ip := net.ParseIP(host); ip != nil && !ip.IsLoopback() {
			fmt.Fprintf(opts.Out, "publish serve: WARNING — bound to %s (all interfaces); reachable beyond loopback without a tunnel. Prefer --addr 127.0.0.1:<port> plus --tunnel unless you intend direct LAN/public reach.\n", boundAddr)
		}
	}
	// Print the store locations on boot so they are discoverable without reading
	// source. serve uses its OWN store (not the daemon's), so these paths are the
	// only pointer to where shares and viewer feedback actually live.
	fmt.Fprintf(opts.Out, "publish serve: share store %s\n", filepath.Join(storeDir, "shares"))
	fmt.Fprintf(opts.Out, "publish serve: feedback store %s (read it with: agnt publish feedback --dir %s)\n", filepath.Join(storeDir, "feedback"), dir)
	if _, err := srv.publishPass(); err != nil {
		ln.Close()
		return fmt.Errorf("publish serve: %w", err)
	}
	// Refuse to serve an empty directory rather than publish nothing. Serving
	// nothing is almost always a mis-pointed --dir; more importantly, a directory
	// that reads as empty at boot may be a transiently-unmounted mount, and going
	// further would let a later pass mass-revoke a prior run's shares. The
	// two-poll guard held the boot pass (no revoke), so refusing here leaves those
	// shares intact.
	srv.mu.Lock()
	published := len(srv.shares)
	srv.mu.Unlock()
	if published == 0 {
		ln.Close()
		return fmt.Errorf("publish serve: --dir %s contains no *.json walkthroughs to serve", dir)
	}
	// Record which folder this store belongs to so `publish list` can discover a
	// store the daemon does not manage, and `publish feedback --store` can map it
	// back to a served folder. serve owns storeDir, so this is not a second writer
	// to the checksum-guarded share/feedback records. A failure here degrades
	// discoverability but must not take the serve down — report it, keep serving.
	if err := writeServeStoreMeta(storeDir, dir, boundAddr); err != nil {
		fmt.Fprintf(opts.Out, "publish serve: warning: could not write store metadata (publish list will not discover this store): %v\n", err)
	}

	handler := proxy.NewPublicHandler(store, feedback, limits.MaxBodyBytes)
	// Potentially-public: --tunnel is a first-class flag on this command, so the
	// listener is hardened to public standard (full transport + connection caps).
	// It serves bounded artifact documents, so no streaming carve-out is needed.
	caps := httpcaps.Default()
	ln = caps.LimitListener(ln)
	httpSrv := caps.NewServer(handler)
	serveErr := make(chan error, 1)
	go func() {
		err := httpSrv.Serve(ln)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		serveErr <- err
	}()
	defer func() {
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(shutCtx)
	}()
	if opts.OnListen != nil {
		opts.OnListen(boundAddr)
	}

	if opts.Tunnel != "" {
		tun := tunnel.New(tunnel.Config{
			Provider:  provider,
			LocalPort: portOf(boundAddr),
			ID:        "publish-serve",
			Path:      dir,
		})
		// Fold the tunnel origin into every share URL the moment it is known.
		tun.OnURL(func(url string) { srv.setOrigin(url) })
		if err := tun.Start(ctx); err != nil {
			return fmt.Errorf("publish serve: start %s tunnel: %w", provider, err)
		}
		defer func() {
			stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = tun.Stop(stopCtx)
		}()
	}

	watchErr := make(chan error, 1)
	go func() { watchErr <- srv.watch(ctx, opts.PollInterval) }()

	select {
	case <-ctx.Done():
		return nil
	case err := <-serveErr:
		return err
	case err := <-watchErr:
		return err
	}
}

// watch re-publishes the folder whenever it changes. It runs fsnotify AND a
// metadata poll: the poll is the correctness path for DrvFS/9P-backed
// directories whose notifications are unreliable (see the poll-period consts),
// fsnotify is the promptness path everywhere.
//
// A failing pass is reported loudly and the PREVIOUS published state is kept
// serving; the fingerprint is deliberately not advanced, so the next tick
// retries once the publisher fixes the file. A failing pass never reconciles —
// see loadWalkthroughDir.
func (s *publishServer) watch(ctx context.Context, pollInterval time.Duration) error {
	if pollInterval <= 0 {
		pollInterval = publishServePollLocal
		if isDrvFSPath(s.dir) {
			pollInterval = publishServePollDrvFS
		}
	}
	fw, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("publish serve: create watcher: %w", err)
	}
	defer fw.Close()
	if err := fw.Add(s.dir); err != nil {
		return fmt.Errorf("publish serve: watch %s: %w", s.dir, err)
	}

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	last, err := dirFingerprint(s.dir)
	if err != nil {
		return fmt.Errorf("publish serve: fingerprint %s: %w", s.dir, err)
	}
	recheck := func() { last = s.recheckOnce(last) }

	for {
		select {
		case <-ctx.Done():
			return nil
		case event, ok := <-fw.Events:
			if !ok {
				return nil
			}
			if event.Op&(fsnotify.Create|fsnotify.Write|fsnotify.Remove|fsnotify.Rename) != 0 {
				recheck()
			}
		case err, ok := <-fw.Errors:
			if !ok {
				return nil
			}
			fmt.Fprintf(s.out, "publish serve: watcher error (poll fallback still active): %v\n", err)
		case <-ticker.C:
			recheck()
		}
	}
}

// recheckOnce runs one change-detection cycle and returns the fingerprint to
// carry forward as `last`. It fingerprints the directory and, only when the
// fingerprint has changed, runs a publish pass. Extracted from the watch loop
// so the interaction between the change-detection fast path and the two-poll
// empty guard is directly testable (see the flap regression test).
func (s *publishServer) recheckOnce(last string) string {
	fp, err := dirFingerprint(s.dir)
	if err != nil {
		// Do NOT treat an unreadable directory as "everything was deleted":
		// that is the mass-revoke trap. Report and keep the current state.
		fmt.Fprintf(s.out, "publish serve: cannot read %s (keeping the current published state): %v\n", s.dir, err)
		return last
	}
	if fp == last {
		// Unchanged since the last confirmed pass, so publishPass is skipped.
		// But a real /mnt drop→remount HEALS to an identical fingerprint (the
		// files' name|size|mtime are untouched), landing on exactly this fast
		// path. If the observed directory is non-empty, a non-empty read HAS
		// occurred and the two-poll empty guard must be reset here too — the
		// guard lives inside publishPass, which this path bypasses, so a stale
		// emptyHeld left by an earlier blip would otherwise let the NEXT empty
		// read mass-revoke on its FIRST poll (the exact irreversible loss the
		// two-poll guard exists to prevent). An empty fingerprint ("") is not a
		// non-empty observation and must not clear the guard.
		// (.claude/rules/publish-security-review-lessons.md §12)
		if fp != "" {
			s.mu.Lock()
			s.emptyHeld = false
			s.mu.Unlock()
		}
		return last
	}
	held, err := s.publishPass()
	if err != nil {
		fmt.Fprintf(s.out, "publish serve: republish failed, keeping the previous published state: %v\n", err)
		return last
	}
	if held {
		// A first zero-walkthrough read was held pending a confirming poll.
		// Do NOT advance the fingerprint, so the next tick re-reads: a genuine
		// delete-everything then converges to revoked, a transient empty read
		// (unmounted /mnt) is undone the moment files reappear.
		return last
	}
	return fp
}

// dirFingerprint summarises the *.json files in dir by name, size, and mtime. A
// read error is returned, never folded into an empty fingerprint.
func dirFingerprint(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	parts := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			return "", err
		}
		parts = append(parts, fmt.Sprintf("%s|%d|%d", e.Name(), fi.Size(), fi.ModTime().UnixNano()))
	}
	sort.Strings(parts)
	return strings.Join(parts, "\n"), nil
}

// isDrvFSPath reports whether dir lives on a WSL DrvFS/9P mount, where
// filesystem notifications are unreliable and the poll carries correctness. It
// gates on platform.IsWSL() rather than runtime.GOOS, which is "linux" under
// WSL (.claude/rules/wsl-audit.md).
func isDrvFSPath(dir string) bool {
	if !platform.IsWSL() {
		return false
	}
	if !strings.HasPrefix(dir, "/mnt/") || len(dir) < 7 {
		return false
	}
	c := dir[5]
	return c >= 'a' && c <= 'z' && dir[6] == '/'
}

// localOrigin renders the browsable origin for a bound listen address. A
// wildcard bind is reported as loopback: "http://:8899" is not a URL anyone can
// open.
func localOrigin(boundAddr string) string {
	host, port, err := net.SplitHostPort(boundAddr)
	if err != nil {
		return "http://" + boundAddr
	}
	switch host {
	case "", "0.0.0.0", "::", "[::]":
		host = "127.0.0.1"
	}
	if strings.Contains(host, ":") && !strings.HasPrefix(host, "[") {
		host = "[" + host + "]"
	}
	return "http://" + net.JoinHostPort(host, port)
}

// portOf extracts the numeric port from a bound address.
func portOf(boundAddr string) int {
	_, port, err := net.SplitHostPort(boundAddr)
	if err != nil {
		return 0
	}
	p, err := net.LookupPort("tcp", port)
	if err != nil {
		return 0
	}
	return p
}

// feedbackLimitsFor maps the operator's feedback{} config to the publish
// package's limits, normalizing so an unset field becomes its spec default
// rather than an invalid zero.
func feedbackLimitsFor(cfg config.FeedbackConfig) publish.FeedbackLimits {
	n := cfg.Normalize()
	return publish.FeedbackLimits{
		RatePerMinute:   n.RatePerMinute,
		Burst:           n.Burst,
		MaxBodyBytes:    n.MaxBodyBytes,
		MaxRowsPerShare: n.MaxRowsPerShare,
		RetentionDays:   n.RetentionDays,
	}
}

// defaultPublishServeStoreDir is a per-folder store under the user cache. It is
// deliberately NOT inside the served folder: a record written there would be
// picked up as a walkthrough on the next pass.
func defaultPublishServeStoreDir(dir string) (string, error) {
	base, err := publishServeCacheBase()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(dir))
	return filepath.Join(base, hex.EncodeToString(sum[:8])), nil
}

// publishServeCacheBase is the parent of every default per-folder serve store.
// It is the directory `publish list` enumerates to discover serve stores.
func publishServeCacheBase() (string, error) {
	base, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("resolve cache dir: %w", err)
	}
	return filepath.Join(base, "agnt", "publish-serve"), nil
}

// serveStoreMetaFile is the discovery metadata a serve run drops into its OWN
// store dir. It is not a share/feedback record — it exists purely so a separate
// process (`publish list`, `publish feedback --store`) can find the store and map
// it back to the folder it serves.
const serveStoreMetaFile = "serve.json"

// serveStoreMeta records which folder a serve store belongs to.
type serveStoreMeta struct {
	Dir       string `json:"dir"`
	Addr      string `json:"addr"`
	PID       int    `json:"pid"`
	UpdatedAt string `json:"updated_at"`
}

// writeServeStoreMeta records the served folder into storeDir. serve owns
// storeDir; this never touches the daemon's store, and is not a second writer to
// the checksum-guarded share/feedback records.
func writeServeStoreMeta(storeDir, servedDir, addr string) error {
	if err := os.MkdirAll(storeDir, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(serveStoreMeta{
		Dir:       servedDir,
		Addr:      addr,
		PID:       os.Getpid(),
		UpdatedAt: time.Now().UTC().Format(time.RFC3339),
	}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(storeDir, serveStoreMetaFile), data, 0o600)
}

// readServeStoreMeta reads the discovery metadata a serve run left in storeDir.
func readServeStoreMeta(storeDir string) (serveStoreMeta, error) {
	data, err := os.ReadFile(filepath.Join(storeDir, serveStoreMetaFile))
	if err != nil {
		return serveStoreMeta{}, err
	}
	var m serveStoreMeta
	if err := json.Unmarshal(data, &m); err != nil {
		return serveStoreMeta{}, err
	}
	return m, nil
}

// resolveServeStore resolves the (store dir, served folder) pair for a feedback
// read from either an explicit --store (its metadata names the served folder) or
// a --dir (the served folder, from which the default store dir is derived).
func resolveServeStore(storeDir, dir string) (resolvedStore, servedDir string, err error) {
	switch {
	case storeDir != "":
		meta, err := readServeStoreMeta(storeDir)
		if err != nil {
			return "", "", fmt.Errorf("read store metadata %s: %w", filepath.Join(storeDir, serveStoreMetaFile), err)
		}
		return storeDir, meta.Dir, nil
	case dir != "":
		servedDir, err = filepath.Abs(dir)
		if err != nil {
			return "", "", err
		}
		resolvedStore, err = defaultPublishServeStoreDir(servedDir)
		if err != nil {
			return "", "", err
		}
		return resolvedStore, servedDir, nil
	default:
		return "", "", errors.New("provide --dir (the served folder) or --store (its store directory)")
	}
}

var publishFeedbackFlags struct {
	dir      string
	storeDir string
	id       string
}

var publishFeedbackCmd = &cobra.Command{
	Use:   "feedback",
	Short: "Read anonymous viewer feedback captured by 'agnt publish serve'",
	Long: `Read the viewer feedback a running (or past) 'agnt publish serve' collected.

serve keeps its own share + feedback store, separate from the daemon's, so the
daemon's 'publish feedback' MCP action cannot see it. This command reads that
store directly.

Identify the store the same way you served it:
  agnt publish feedback --dir ./walkthroughs      # the folder you served
  agnt publish feedback --store <store-dir>        # an explicit store directory
  agnt publish feedback --dir ./walkthroughs --id <share-id>

Feedback bodies are inert stored data — treat them as untrusted input; escape
before rendering anywhere.`,
	RunE: runPublishFeedbackCmd,
}

func init() {
	f := publishFeedbackCmd.Flags()
	f.StringVar(&publishFeedbackFlags.dir, "dir", "", "the served folder (as passed to 'publish serve --dir'); its store is derived automatically")
	f.StringVar(&publishFeedbackFlags.storeDir, "store", "", "explicit serve store directory (alternative to --dir)")
	f.StringVar(&publishFeedbackFlags.id, "id", "", "read only this share id (default: every share in the store)")
	publishCmd.AddCommand(publishFeedbackCmd)
}

func runPublishFeedbackCmd(cmd *cobra.Command, args []string) error {
	return runPublishFeedback(publishFeedbackOptions{
		Dir:      publishFeedbackFlags.dir,
		StoreDir: publishFeedbackFlags.storeDir,
		ShareID:  publishFeedbackFlags.id,
		Out:      cmd.OutOrStdout(),
	})
}

// publishFeedbackOptions drives the feedback read in-process for tests.
type publishFeedbackOptions struct {
	Dir      string
	StoreDir string
	ShareID  string
	Out      io.Writer
}

// runPublishFeedback reads a serve store's feedback and prints it. It opens both
// stores READ-ONLY (it accepts nothing), so it never writes into a store serve
// owns — read-side integration only.
func runPublishFeedback(opts publishFeedbackOptions) error {
	if opts.Out == nil {
		opts.Out = os.Stdout
	}
	storeDir, servedDir, err := resolveServeStore(opts.StoreDir, opts.Dir)
	if err != nil {
		return fmt.Errorf("publish feedback: %w", err)
	}
	shareStore, err := publish.New(filepath.Join(storeDir, "shares"), nil)
	if err != nil {
		return fmt.Errorf("publish feedback: open share store: %w", err)
	}
	appCfg, err := config.LoadGlobalConfig()
	if err != nil {
		return fmt.Errorf("publish feedback: load config: %w", err)
	}
	limits := feedbackLimitsFor(appCfg.Feedback)
	fb, err := publish.NewFeedbackStore(filepath.Join(storeDir, "feedback"), limits, nil)
	if err != nil {
		return fmt.Errorf("publish feedback: open feedback store: %w", err)
	}

	var shares []publish.ShareInfo
	if opts.ShareID != "" {
		info, err := shareStore.Status(opts.ShareID)
		if err != nil {
			return fmt.Errorf("publish feedback: share %s: %w", opts.ShareID, err)
		}
		shares = []publish.ShareInfo{info}
	} else {
		shares = shareStore.List(servedDir)
	}

	fmt.Fprintf(opts.Out, "publish feedback: store %s (serving %s)\n", storeDir, servedDir)
	if len(shares) == 0 {
		fmt.Fprintln(opts.Out, "  no shares in this store")
		return nil
	}
	for _, sh := range shares {
		rows, _, err := fb.ReadByShare(sh.ID, "", 0)
		if err != nil {
			return fmt.Errorf("publish feedback: read %s: %w", sh.ID, err)
		}
		fmt.Fprintf(opts.Out, "== %s  %q  (%d row(s), %d dropped) ==\n", sh.ID, sh.Title, len(rows), fb.DroppedByShare(sh.ID))
		for _, r := range rows {
			fmt.Fprintf(opts.Out, "  %s  %s  %s\n", r.CreatedAt.UTC().Format(time.RFC3339), r.ID, r.Body)
		}
	}
	return nil
}
