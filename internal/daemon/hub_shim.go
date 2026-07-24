package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/standardbeagle/agnt/internal/config"
	"github.com/standardbeagle/agnt/internal/debug"
	"github.com/standardbeagle/agnt/internal/protocol"
	"github.com/standardbeagle/agnt/internal/shims"
	hubpkg "github.com/standardbeagle/go-cli-server/hub"
	goprocess "github.com/standardbeagle/go-cli-server/process"
	hubproto "github.com/standardbeagle/go-cli-server/protocol"
)

// shimActions maps SHIM sub-verbs to handlers.
func (d *Daemon) shimActions() map[string]handlerFn {
	return map[string]handlerFn{
		protocol.SubVerbExec:     d.hubHandleShimExec,
		protocol.SubVerbRegister: d.hubHandleShimRegister,
	}
}

func (d *Daemon) hubHandleShim(ctx context.Context, conn *hubpkg.Connection, cmd *hubproto.Command) error {
	return newCommandRouter("SHIM").dispatch(ctx, conn, cmd, d.shimActions())
}

// hubHandleShimRegister records a shim install for lifecycle cleanup and
// makes sure the external watcher process is running.
func (d *Daemon) hubHandleShimRegister(ctx context.Context, conn *hubpkg.Connection, cmd *hubproto.Command) error {
	req, err := unmarshalCommand[protocol.ShimRegisterRequest](cmd)
	if err != nil || req.ProjectPath == "" || req.BinDir == "" {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "SHIM REGISTER requires project_path and bin_dir")
	}
	if err := shims.RecordInstall(req.ProjectPath, req.BinDir, req.SessionCode); err != nil {
		return conn.WriteInternalErr(err.Error())
	}
	d.ensureShimWatcher()
	return conn.WriteOK("shim install recorded")
}

// hubHandleShimExec routes one shimmed command from a managed shell. The
// response is always a ShimExecResponse JSON — even "route it yourself"
// (passthrough) — so the CLI never has to interpret transport errors as
// decisions. Transport errors fail open client-side.
func (d *Daemon) hubHandleShimExec(ctx context.Context, conn *hubpkg.Connection, cmd *hubproto.Command) error {
	req, err := unmarshalCommand[protocol.ShimExecRequest](cmd)
	if err != nil || req.ProjectPath == "" || req.Command == "" {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "SHIM EXEC requires project_path and command")
	}
	resp := d.routeShim(ctx, &req)
	data, _ := json.Marshal(resp)
	return conn.WriteJSON(data)
}

// passthroughResponse tells the shim to exec the real binary.
func passthroughResponse() *protocol.ShimExecResponse {
	return &protocol.ShimExecResponse{Action: "passthrough"}
}

// routeShim is the pure routing core, split from the connection wrapper so
// tests can drive it against a Daemon fixture without socket plumbing.
func (d *Daemon) routeShim(ctx context.Context, req *protocol.ShimExecRequest) *protocol.ShimExecResponse {
	// Defensive: only route commands that actually have shim scripts. A
	// hand-crafted SHIM EXEC for "rm" must never reach the process manager.
	if !isShimmedCommand(req.Command) {
		return passthroughResponse()
	}

	// Scope gate: only serve projects with a live registered session. The
	// shim dir is only ever on PATH for managed shells, so a request with
	// no session means something is spoofing or the session already ended —
	// either way, fail open.
	if d.sessionRegistry == nil || len(d.sessionRegistry.List(req.ProjectPath, false)) == 0 {
		return passthroughResponse()
	}

	cfg, err := config.LoadAgntConfig(req.ProjectPath)
	if err != nil || cfg == nil || !cfg.ShimsEnabled() {
		return passthroughResponse()
	}

	decision := shims.Resolve(req.Command, req.Args, cfg.Shims)
	if err := shims.ValidateAction(string(decision.Action)); err != nil {
		debug.Log("shim-hub", "invalid action in rule %q: %v", decision.RuleName, err)
		return passthroughResponse()
	}

	cmdline := req.Command
	if len(req.Args) > 0 {
		cmdline += " " + strings.Join(req.Args, " ")
	}

	switch decision.Action {
	case shims.ActionPass:
		return passthroughResponse()
	case shims.ActionIgnore:
		return &protocol.ShimExecResponse{
			Action:   "handled",
			Message:  fmt.Sprintf("agnt: %q ignored by shims rule %q", cmdline, decision.RuleName),
			ToolHint: "see .agnt.kdl shims block",
		}
	case shims.ActionBlock:
		return &protocol.ShimExecResponse{
			Action:   "blocked",
			ExitCode: 2,
			Message:  fmt.Sprintf("agnt: %q blocked by shims rule %q — use the agnt MCP tools instead", cmdline, decision.RuleName),
			ToolHint: "run / proc MCP tools",
		}
	case shims.ActionReroute:
		return d.shimRunOneShot(ctx, req, cmdline, decision.Dir, nil, nil)
	case shims.ActionRestartWatch:
		return d.shimRunOneShot(ctx, req, cmdline, "", nil, func() string {
			return d.shimRestartWatch(ctx, cfg, req.ProjectPath)
		})
	case shims.ActionQuiesce:
		stopNote := d.shimStopWatch(ctx, cfg, req.ProjectPath)
		return d.shimRunOneShot(ctx, req, cmdline, "", func() string { return stopNote }, func() string {
			return d.shimRestartWatch(ctx, cfg, req.ProjectPath)
		})
	default: // ActionRoute
		return d.shimRoute(ctx, req, cmdline, cfg, decision.RuleName != "")
	}
}

// shimRoute executes the "route" action by command class. explicitRule is
// true when a config rule (not the class default) selected route — an
// explicit route on a generic command runs it as a managed one-shot rather
// than passing through.
func (d *Daemon) shimRoute(ctx context.Context, req *protocol.ShimExecRequest, cmdline string, cfg *config.AgntConfig, explicitRule bool) *protocol.ShimExecResponse {
	switch shims.Classify(req.Command, req.Args) {
	case shims.ClassDevServer:
		return d.shimStartDevServer(ctx, req, cmdline, cfg)
	case shims.ClassOneShot:
		return d.shimRunOneShot(ctx, req, cmdline, "", nil, nil)
	case shims.ClassKill:
		return d.shimKill(ctx, req, cmdline)
	case shims.ClassPort:
		return d.shimPortReport(req)
	default:
		if explicitRule {
			return d.shimRunOneShot(ctx, req, cmdline, "", nil, nil)
		}
		return passthroughResponse()
	}
}

// shimStartDevServer starts (or reuses) a long-lived managed process for a
// dev-server invocation. When the invocation maps to a configured script
// (npm run dev → script "dev") it goes through StartScriptExplicit so the
// script registry, proxies, and autostart bookkeeping stay coherent;
// otherwise it runs as a plain managed process keyed by a command slug.
func (d *Daemon) shimStartDevServer(ctx context.Context, req *protocol.ShimExecRequest, cmdline string, cfg *config.AgntConfig) *protocol.ShimExecResponse {
	if name := shimScriptName(req, cfg); name != "" {
		scriptCfg := cfg.Scripts[name]
		if err := d.StartScriptExplicit(ctx, name, scriptCfg, req.ProjectPath, cfg.Proxies); err != nil {
			return shimErrorResponse(cmdline, err)
		}
		processID := makeProcessID(req.ProjectPath, name)
		proc, _ := d.hub.ProcessManager().GetByPath(processID, req.ProjectPath)
		return &protocol.ShimExecResponse{
			Action:   "handled",
			Message:  fmt.Sprintf("agnt: %q is managed by agnt (script %q%s)", cmdline, name, shimProcSuffix(proc)),
			ToolHint: fmt.Sprintf(`proc { action: "restart", id: "%s" } or proc { action: "output", id: "%s" }`, name, name),
			Output:   shimOutputTail(proc),
		}
	}

	id := "shim-" + shimSlug(cmdline)
	procCfg := goprocess.ProcessConfig{
		ID:          id,
		ProjectPath: req.ProjectPath,
		WorkingDir:  shimWorkingDir(req),
		Command:     req.Command,
		Args:        req.Args,
		Env:         d.injectSecretEnv(req.ProjectPath, nil),
	}
	result, err := d.hub.ProcessManager().StartOrReuse(ctx, procCfg)
	if err != nil {
		return shimErrorResponse(cmdline, err)
	}
	if !result.Reused {
		d.watchProcessExit(result.Process)
	}
	return &protocol.ShimExecResponse{
		Action:   "handled",
		Message:  fmt.Sprintf("agnt: %q is managed by agnt (process %q%s)", cmdline, id, shimProcSuffix(result.Process)),
		ToolHint: fmt.Sprintf(`proc { action: "output", id: "%s" }`, id),
		Output:   shimOutputTail(result.Process),
	}
}

// shimRunOneShot runs a bounded command (build/test) as a managed process,
// waits for exit, and returns the exit code plus output tail. The process
// is removed from the registry afterwards so repeated builds don't
// accumulate dead entries. preNote/postNote contribute notes to the
// feedback message (used by quiesce/restart-watch). postNote runs on EVERY
// exit path once preNote ran — quiesce stops the watch first, and leaving
// it dead because the build failed to start or the client timed out would
// silently take down the user's dev server.
//
// If the request context dies first (client timeout/disconnect) the
// process is stopped and deregistered: the shim CLI fail-opens by exec'ing
// the real binary, so leaving the managed copy alive would run the command
// twice and leak its registry entry.
func (d *Daemon) shimRunOneShot(ctx context.Context, req *protocol.ShimExecRequest, cmdline, rerouteDir string, preNote, postNote func() string) (resp *protocol.ShimExecResponse) {
	workingDir := shimWorkingDir(req)
	if rerouteDir != "" {
		workingDir = resolveWorkingDir(req.ProjectPath, rerouteDir)
	}
	id := fmt.Sprintf("shim-%s-%d", shimSlug(cmdline), time.Now().UnixNano())
	procCfg := goprocess.ProcessConfig{
		ID:          id,
		ProjectPath: req.ProjectPath,
		WorkingDir:  workingDir,
		Command:     req.Command,
		Args:        req.Args,
		Env:         d.injectSecretEnv(req.ProjectPath, nil),
	}

	var notes []string
	if preNote != nil {
		if n := preNote(); n != "" {
			notes = append(notes, n)
		}
	}
	if postNote != nil {
		defer func() {
			if n := postNote(); n != "" {
				if resp.Message == "" {
					resp.Message = "agnt: " + n
				} else {
					resp.Message += "\nagnt: " + n
				}
			}
		}()
	}

	result, err := d.hub.ProcessManager().StartOrReuse(ctx, procCfg)
	if err != nil {
		return shimErrorResponse(cmdline, err)
	}
	proc := result.Process

	select {
	case <-proc.Done():
	case <-ctx.Done():
		// daemonclient.Client gave up while the command still runs — stop and
		// deregister so the managed copy can't outlive the request (see
		// the doc comment for the double-run hazard).
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 10*time.Second)
		_ = d.hub.ProcessManager().Stop(stopCtx, id)
		stopCancel()
		d.hub.ProcessManager().RemoveByPath(id, req.ProjectPath)
		return shimErrorResponse(cmdline, ctx.Err())
	}

	exitCode := proc.ExitCode()
	out := shimOutputTail(proc)
	d.hub.ProcessManager().RemoveByPath(id, req.ProjectPath)

	msg := fmt.Sprintf("agnt: %q ran managed by agnt (exit %d)", cmdline, exitCode)
	if rerouteDir != "" {
		msg += fmt.Sprintf(" in %s", workingDir)
	}
	for _, n := range notes {
		msg += "\nagnt: " + n
	}
	return &protocol.ShimExecResponse{
		Action:   "handled",
		ExitCode: exitCode,
		Message:  msg,
		ToolHint: `run / proc MCP tools run builds and tests with captured output`,
		Output:   out,
	}
}

// shimKill routes the kill family. A signal is only intercepted when every
// target is a daemon-managed process of the SAME project; anything else
// passes through to the real kill so unmanaged PIDs behave exactly as the
// shell expects. All targets are validated BEFORE any stop is issued — a
// mixed managed/unmanaged invocation must never be half-applied and then
// re-executed by the real kill.
func (d *Daemon) shimKill(ctx context.Context, req *protocol.ShimExecRequest, cmdline string) *protocol.ShimExecResponse {
	// Only a terminating signal may become a managed Stop. Probes
	// (`kill -0`), listing (`kill -l`), and non-terminating signals
	// (HUP, USR1, ...) must reach the real binary untouched — converting
	// a liveness probe into a Stop would kill the process being probed.
	if !shimInterceptableSignal(req.Args) {
		return passthroughResponse()
	}
	pm := d.hub.ProcessManager()
	var targets []*goprocess.ManagedProcess

	switch req.Command {
	case "kill":
		pids := shimKillPIDs(req.Args)
		if len(pids) == 0 {
			return passthroughResponse()
		}
		for _, pid := range pids {
			proc := pm.GetByPID(pid)
			if proc == nil || proc.ProjectPath != req.ProjectPath {
				return passthroughResponse()
			}
			targets = append(targets, proc)
		}
	case "killall", "pkill":
		name := shimKillNameArg(req.Args)
		if name == "" {
			return passthroughResponse()
		}
		for _, proc := range pm.List() {
			if proc.ProjectPath != req.ProjectPath {
				continue
			}
			if shimProcNameMatches(proc, name) {
				targets = append(targets, proc)
			}
		}
		if len(targets) == 0 {
			return passthroughResponse()
		}
	}

	var stopped []string
	for _, proc := range targets {
		if err := pm.Stop(ctx, proc.ID); err != nil {
			return shimErrorResponse(cmdline, err)
		}
		stopped = append(stopped, fmt.Sprintf("%q (pid %d)", proc.ID, proc.PID()))
	}

	return &protocol.ShimExecResponse{
		Action:   "handled",
		Message:  fmt.Sprintf("agnt: stopped managed process %s", strings.Join(stopped, ", ")),
		ToolHint: `proc { action: "stop", id: "<id>" }`,
	}
}

// shimPortReport answers lsof/fuser with the managed-process/port view for
// the project plus the pointer to the cleanup tool.
func (d *Daemon) shimPortReport(req *protocol.ShimExecRequest) *protocol.ShimExecResponse {
	var b strings.Builder
	fmt.Fprintf(&b, "%-24s %-8s %-10s %s\n", "PROCESS", "PID", "STATE", "COMMAND")
	count := 0
	for _, proc := range d.hub.ProcessManager().List() {
		if proc.ProjectPath != req.ProjectPath {
			continue
		}
		fmt.Fprintf(&b, "%-24s %-8d %-10s %s %s\n", proc.ID, proc.PID(), proc.State(), proc.Command, strings.Join(proc.Args, " "))
		count++
	}
	if count == 0 {
		b.WriteString("(no managed processes for this project)\n")
	}
	return &protocol.ShimExecResponse{
		Action:   "handled",
		Message:  fmt.Sprintf("agnt: %q intercepted — agnt tracks managed processes and ports", req.Command),
		ToolHint: `proc { action: "list" } or proc { action: "cleanup_port", port: N }`,
		Output:   b.String(),
	}
}

// shimStopWatch stops the configured watch script if it is running and
// returns a human-readable note ("" when there was nothing to stop).
func (d *Daemon) shimStopWatch(ctx context.Context, cfg *config.AgntConfig, projectPath string) string {
	name := shims.WatchScriptName(cfg)
	if name == "" {
		return ""
	}
	processID := makeProcessID(projectPath, name)
	proc, err := d.hub.ProcessManager().GetByPath(processID, projectPath)
	if err != nil || proc == nil || !proc.IsRunning() {
		return ""
	}
	if err := d.hub.ProcessManager().Stop(ctx, processID); err != nil {
		return fmt.Sprintf("failed to stop watch script %q: %v", name, err)
	}
	return fmt.Sprintf("stopped watch script %q while the command runs", name)
}

// shimRestartWatch (re)starts the configured watch script and returns a
// human-readable note ("" when no watch script is configured).
func (d *Daemon) shimRestartWatch(ctx context.Context, cfg *config.AgntConfig, projectPath string) string {
	name := shims.WatchScriptName(cfg)
	if name == "" {
		return ""
	}
	scriptCfg := cfg.Scripts[name]
	if scriptCfg == nil {
		return ""
	}
	if err := d.StartScriptExplicit(ctx, name, scriptCfg, projectPath, cfg.Proxies); err != nil {
		return fmt.Sprintf("failed to restart watch script %q: %v", name, err)
	}
	return fmt.Sprintf("restarted watch script %q afterwards", name)
}

// --- helpers ---

func isShimmedCommand(cmd string) bool {
	for _, c := range shims.CommandNames() {
		if c == cmd {
			return true
		}
	}
	return false
}

// shimScriptName maps a dev-server invocation to a configured script name:
// `npm run dev` → "dev", `yarn start` → "start", when that script exists in
// .agnt.kdl. Returns "" when no script matches.
func shimScriptName(req *protocol.ShimExecRequest, cfg *config.AgntConfig) string {
	if cfg == nil || len(cfg.Scripts) == 0 {
		return ""
	}
	fields := strings.Fields(strings.Join(req.Args, " "))
	switch req.Command {
	case "npm", "pnpm", "yarn", "bun":
		if len(fields) > 0 && fields[0] == "run" {
			fields = fields[1:]
		}
		if len(fields) == 1 {
			if _, ok := cfg.Scripts[fields[0]]; ok {
				return fields[0]
			}
		}
	case "go":
		if len(fields) >= 1 && fields[0] == "run" && len(fields) == 2 {
			if _, ok := cfg.Scripts[fields[1]]; ok {
				return fields[1]
			}
		}
	}
	return ""
}

// shimWorkingDir prefers the shell's cwd when it sits inside the project
// (monorepo subdirs), else the project root. Uses filepath.Rel rather than
// a prefix test so "/proj2" is not accepted as inside "/proj".
func shimWorkingDir(req *protocol.ShimExecRequest) string {
	if req.Cwd != "" {
		if rel, err := filepath.Rel(req.ProjectPath, req.Cwd); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return req.Cwd
		}
	}
	return req.ProjectPath
}

// shimSlug builds a stable process-ID fragment from a command line.
func shimSlug(cmdline string) string {
	var b strings.Builder
	for _, r := range cmdline {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r + 32)
		case b.Len() > 0 && !strings.HasSuffix(b.String(), "-"):
			b.WriteByte('-')
		}
		if b.Len() >= 40 {
			break
		}
	}
	return strings.Trim(b.String(), "-")
}

// shimOutputTail returns the last ~4KB of combined process output, or "".
func shimOutputTail(proc *goprocess.ManagedProcess) string {
	if proc == nil {
		return ""
	}
	data, _ := proc.CombinedOutput()
	const maxTail = 4096
	if len(data) > maxTail {
		data = data[len(data)-maxTail:]
		// Don't split a line mid-way.
		if i := strings.IndexByte(string(data), '\n'); i >= 0 && i < len(data)-1 {
			data = data[i+1:]
		}
	}
	return strings.TrimRight(string(data), "\n")
}

// shimProcSuffix renders ", pid 12345, running" for feedback messages.
func shimProcSuffix(proc *goprocess.ManagedProcess) string {
	if proc == nil {
		return ""
	}
	return fmt.Sprintf(", pid %d, %s", proc.PID(), proc.State())
}

func shimErrorResponse(cmdline string, err error) *protocol.ShimExecResponse {
	return &protocol.ShimExecResponse{
		Action:   "handled",
		ExitCode: 1,
		Message:  fmt.Sprintf("agnt: failed to run %q managed: %v", cmdline, err),
	}
}

// shimInterceptableSignal reports whether a kill-family invocation
// requests a terminating signal the daemon can honor via a managed Stop:
// the default (SIGTERM) or an explicit TERM/KILL in any of the -9, -KILL,
// -s KILL, --signal KILL, --signal=KILL forms. Everything else — liveness
// probes (-0), signal listing (-l), reload/notification signals (HUP,
// USR1, ...), or flags we don't recognize — is not interceptable.
func shimInterceptableSignal(args []string) bool {
	sig := ""
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "-l" || a == "--list":
			return false
		case a == "-s" || a == "--signal":
			if i+1 < len(args) {
				sig = args[i+1]
				i++
			}
		case strings.HasPrefix(a, "--signal="):
			sig = strings.TrimPrefix(a, "--signal=")
		case len(a) > 1 && strings.HasPrefix(a, "-"):
			sig = a[1:]
		}
	}
	switch strings.TrimPrefix(strings.ToUpper(sig), "SIG") {
	case "", "9", "15", "TERM", "KILL":
		return true
	}
	return false
}

// shimKillPIDs extracts numeric PIDs from kill args, skipping flags (-9,
// -TERM, --signal) and their values.
func shimKillPIDs(args []string) []int {
	var pids []int
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			continue
		}
		if strings.HasPrefix(a, "-") {
			// -s SIGKILL / --signal SIGKILL consume the next arg.
			if a == "-s" || a == "--signal" {
				i++
			}
			continue
		}
		if pid, err := strconv.Atoi(a); err == nil && pid > 0 {
			pids = append(pids, pid)
		}
	}
	return pids
}

// shimKillNameArg returns the process-name operand of killall/pkill.
func shimKillNameArg(args []string) string {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if strings.HasPrefix(a, "-") {
			if a == "-s" || a == "--signal" || a == "-u" || a == "--user" {
				i++
			}
			continue
		}
		return a
	}
	return ""
}

// shimProcNameMatches reports whether a managed process matches a
// killall/pkill name operand: exact or basename match on the command.
func shimProcNameMatches(proc *goprocess.ManagedProcess, name string) bool {
	if proc.Command == name {
		return true
	}
	if i := strings.LastIndexAny(proc.Command, `/\\`); i >= 0 && proc.Command[i+1:] == name {
		return true
	}
	return false
}
