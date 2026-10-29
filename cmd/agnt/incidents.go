package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/standardbeagle/agnt/internal/daemonclient"
	"github.com/standardbeagle/agnt/internal/incident"
	"github.com/standardbeagle/agnt/internal/protocol"
	"github.com/standardbeagle/agnt/internal/proxy"
	"github.com/standardbeagle/agnt/internal/tools"
)

// sessionCodeEnv is set in the agent's environment by `agnt run`; agent-side
// integrations (editor plugins, mods) inherit it, so they never pass a code.
const sessionCodeEnv = "AGNT_SESSION_CODE"

var incidentsCmd = &cobra.Command{
	Use:   "incidents",
	Short: "Inspect, stream and pause a session's incident inbox",
	Long: `Scriptable access to one session's incident inbox, for agent-side
integrations (OpenCode plugin, Claude Code mod) that cannot hold a daemon socket.

Every subcommand targets one session: --session, else $` + sessionCodeEnv + `. Inboxes are
per session, so two agents in one checkout never see each other's incidents.

Examples:
  agnt incidents list --json            # preview; never marks anything read
  agnt incidents watch                  # NDJSON, one line per digest ping
  agnt incidents watch --register       # no agnt run: own a session, first line names it
  agnt incidents read --json            # consuming pull: marks the rows it returns read
  agnt incidents watch -- t3 thread send $THREAD "{message}"
  agnt incidents forwarding pause`,
}

var incidentsListCmd = &cobra.Command{
	Use:   "list",
	Short: "Show the session's incidents without marking them read",
	Args:  cobra.NoArgs,
	RunE:  runIncidentsList,
}

var incidentsWatchCmd = &cobra.Command{
	Use:   "watch [-- command [args...]]",
	Short: "Stream this session's incident digests as NDJSON, or run a command per digest",
	Long: `Stream this session's incident digests as NDJSON.

With a command after "--", each digest runs that command instead (nothing is
echoed to stdout): one run at a time, in stream order. Digests have already
passed the incident pipeline's dedup, batch and activity gates, so the command
sees the same stream an agent would.

Inside the command's arguments {message}, {level}, {session} and {json} are
replaced per digest. The same fields arrive as AGNT_EVENT_MESSAGE,
AGNT_EVENT_LEVEL, AGNT_EVENT_SESSION and AGNT_EVENT_TYPE, and the JSON line is
on stdin. The command is never run through a shell, and digest text can be
page-authored, so treat it as untrusted input.

Example (send each digest to a T3 Code thread):
  agnt incidents watch -- t3 thread send "$THREAD" "{message}"`,
	Args: cobra.ArbitraryArgs,
	RunE: runIncidentsWatch,
}

var incidentsReadCmd = &cobra.Command{
	Use:   "read",
	Short: "Pull the session's incidents and mark the returned rows read",
	Long: `Consuming pull from the session's incident inbox, the CLI form of the
get_incidents tool with mark_read. Poll it with the returned cursor, or use
"incidents list" for a preview that leaves unread state untouched.`,
	Args: cobra.NoArgs,
	RunE: runIncidentsRead,
}

var incidentsForwardingCmd = &cobra.Command{
	Use:       "forwarding <pause|resume>",
	Short:     "Pause or resume agent-inbound incident push for the session",
	Long:      "Pause drops the push only; incidents still land in the inbox and stay pullable.",
	Args:      cobra.ExactArgs(1),
	ValidArgs: []string{"pause", "resume"},
	RunE:      runIncidentsForwarding,
}

func init() {
	incidentsCmd.PersistentFlags().String("session", "", "Session code (default $"+sessionCodeEnv+")")
	incidentsWatchCmd.Flags().Bool("register", false, "Own a session when none is given (agent not started by 'agnt run'); the first output line names it")
	incidentsListCmd.Flags().Bool("json", false, "Output the raw query result as JSON")
	incidentsListCmd.Flags().StringSlice("severity", nil, "Only these severities (critical,error,warning,info)")
	incidentsListCmd.Flags().String("since", "", "Cursor (RFC3339) from a previous result")
	incidentsListCmd.Flags().Int("limit", 20, "Maximum incidents (max 100)")
	incidentsWatchCmd.Flags().Duration("exec-timeout", defaultExecTimeout, "Per-digest limit for a command given after --")
	incidentsReadCmd.Flags().Bool("json", false, "Output the raw query result as JSON")
	incidentsReadCmd.Flags().StringSlice("severity", nil, "Only these severities (critical,error,warning,info)")
	incidentsReadCmd.Flags().String("since", "", "Cursor (RFC3339) from a previous result")
	incidentsReadCmd.Flags().Int("limit", 20, "Maximum incidents (max 100)")
	incidentsReadCmd.Flags().String("profile", "changed", "Triage lens: bug | changed | release | full")
	incidentsCmd.AddCommand(incidentsListCmd, incidentsReadCmd, incidentsWatchCmd, incidentsForwardingCmd)
}

// incidentSession resolves the target session: flag, else environment. There is
// no cwd guess here on purpose — two sessions in one checkout would be ambiguous.
func incidentSession(cmd *cobra.Command) (string, error) {
	code, _ := cmd.Flags().GetString("session")
	if code == "" {
		code = os.Getenv(sessionCodeEnv)
	}
	if code == "" {
		return "", fmt.Errorf("no session: pass --session or run inside `agnt run` ($%s)", sessionCodeEnv)
	}
	return code, nil
}

func runIncidentsList(cmd *cobra.Command, _ []string) error {
	code, err := incidentSession(cmd)
	if err != nil {
		return err
	}
	client, err := getSessionClient(cmd)
	if err != nil {
		return err
	}
	defer client.Close()

	sev, _ := cmd.Flags().GetStringSlice("severity")
	since, _ := cmd.Flags().GetString("since")
	limit, _ := cmd.Flags().GetInt("limit")
	asJSON, _ := cmd.Flags().GetBool("json")

	// Detail summary and mark_read false: this is a preview, so the inbox's
	// unread state (and the next digest ping) is untouched.
	res, err := client.IncidentQuery(protocol.IncidentQueryFilter{
		SessionCode: code,
		Severities:  sev,
		Since:       since,
		Limit:       limit,
		Profile:     "full",
	})
	if err != nil {
		return err
	}
	return writeIncidentList(cmd.OutOrStdout(), res, asJSON)
}

func runIncidentsRead(cmd *cobra.Command, _ []string) error {
	code, err := incidentSession(cmd)
	if err != nil {
		return err
	}
	client, err := getSessionClient(cmd)
	if err != nil {
		return err
	}
	defer client.Close()

	sev, _ := cmd.Flags().GetStringSlice("severity")
	since, _ := cmd.Flags().GetString("since")
	limit, _ := cmd.Flags().GetInt("limit")
	profile, _ := cmd.Flags().GetString("profile")
	asJSON, _ := cmd.Flags().GetBool("json")

	res, err := client.IncidentQuery(protocol.IncidentQueryFilter{
		SessionCode: code,
		Severities:  sev,
		Since:       since,
		Limit:       limit,
		Profile:     profile,
		MarkRead:    true,
	})
	if err != nil {
		return err
	}
	return writeIncidentList(cmd.OutOrStdout(), res, asJSON)
}

func writeIncidentList(w io.Writer, res *protocol.IncidentQueryResult, asJSON bool) error {
	if asJSON {
		return json.NewEncoder(w).Encode(res)
	}
	if res.ScopeAmbiguous {
		fmt.Fprintln(w, "session not found; candidates:")
		for _, c := range res.ScopeCandidates {
			fmt.Fprintf(w, "  %s  %s\n", c.SessionCode, c.ProjectPath)
		}
		return nil
	}
	if len(res.Incidents) == 0 {
		fmt.Fprintln(w, "no incidents")
		return nil
	}
	for _, in := range res.Incidents {
		b, _ := json.Marshal(in)
		fmt.Fprintln(w, string(b))
	}
	if res.Truncated {
		fmt.Fprintf(w, "(truncated; continue with --since %s)\n", res.Cursor)
	}
	return nil
}

// digestLine is one NDJSON line of `incidents watch`.
type digestLine struct {
	Type        string          `json:"type"`
	SessionCode string          `json:"session_code"`
	Level       string          `json:"level"`
	Message     string          `json:"message"`
	Payload     json.RawMessage `json:"payload,omitempty"`
}

// digestLineFor renders a digest stream entry, or reports false for any other
// entry type.
func digestLineFor(session string, entry proxy.LogEntry) (digestLine, bool) {
	if entry.Type != proxy.LogTypeIncidentDigest || entry.Custom == nil {
		return digestLine{}, false
	}
	line := digestLine{
		Type:        "incident_digest",
		SessionCode: session,
		Level:       entry.Custom.Level,
		Message:     entry.Custom.Message,
	}
	if p, ok := entry.Custom.Data[incident.DigestPayloadKey]; ok {
		if b, err := json.Marshal(p); err == nil {
			line.Payload = b
		}
	}
	return line, true
}

// ownedSessionCommand names the sessions `incidents watch --register` creates.
const ownedSessionCommand = "claude-mod"

// ownedSessionBeat matches the 30s client heartbeat the daemon's 60s stale
// cutoff expects.
const ownedSessionBeat = 30 * time.Second

// holdOwnedSession registers a session for an agent that was not started by
// `agnt run` (a Claude Code mod), keeps it alive, and unregisters it when ctx
// ends. The registering connection is the session's control connection, so it
// stays open for the whole life of the watch. The returned stop waits for the
// unregister.
func holdOwnedSession(ctx context.Context, socket, project string) (string, func(), error) {
	client := daemonclient.NewClient(daemonclient.WithSocketPath(socket))
	if err := client.Connect(); err != nil {
		return "", nil, fmt.Errorf("daemon is not running: %v", err)
	}
	code, err := client.SessionGenerateCode(ownedSessionCommand)
	if err != nil {
		_ = client.Close()
		return "", nil, err
	}
	// Overlay "-": there is no overlay socket, as for the MCP channel session.
	if _, err := client.SessionRegister(code, "-", project, ownedSessionCommand, nil); err != nil {
		_ = client.Close()
		return "", nil, fmt.Errorf("register session: %w", err)
	}

	hctx, hcancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		tick := time.NewTicker(ownedSessionBeat)
		defer tick.Stop()
		for {
			select {
			case <-hctx.Done():
				if err := client.SessionUnregister(code); err != nil {
					fmt.Fprintf(os.Stderr, "incidents watch: unregister %s: %v\n", code, err)
				}
				_ = client.Close()
				return
			case <-tick.C:
				if err := client.SessionHeartbeat(code); err == nil {
					continue
				}
				// A restarted daemon has no record of the session: register the
				// same code again (the owner pid is ours, so it is a reconnect).
				_ = client.Close()
				client = daemonclient.NewClient(daemonclient.WithSocketPath(socket))
				if err := client.Connect(); err != nil {
					fmt.Fprintf(os.Stderr, "incidents watch: session %s: daemon unreachable: %v\n", code, err)
					continue
				}
				if _, err := client.SessionRegister(code, "-", project, ownedSessionCommand, nil); err != nil {
					fmt.Fprintf(os.Stderr, "incidents watch: re-register %s: %v\n", code, err)
				}
			}
		}
	}()
	return code, func() { hcancel(); <-done }, nil
}

func runIncidentsWatch(cmd *cobra.Command, _ []string) error {
	register, _ := cmd.Flags().GetBool("register")
	code, err := incidentSession(cmd)
	if err != nil && !register {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// A consumer expects a long-lived child: reconnect through daemon restarts,
	// as `agnt monitor` does. The directory is only the fallback when the
	// session is rejected; the subscription itself is pinned to the session.
	project, _ := os.Getwd()
	enc := json.NewEncoder(cmd.OutOrStdout())
	emit := func(l digestLine) { _ = enc.Encode(l) }
	if dash := cmd.ArgsLenAtDash(); dash >= 0 {
		timeout, _ := cmd.Flags().GetDuration("exec-timeout")
		emit = digestCommandSink(ctx, cmd.Flags().Args()[dash:], timeout, os.Stderr, os.Stderr)
	} else if len(cmd.Flags().Args()) > 0 {
		return fmt.Errorf("unexpected arguments %q: put a command after --", cmd.Flags().Args())
	}
	if err != nil {
		// No session in the environment and --register given: own one. Say which,
		// on the first line, so the caller can pass it to its other commands.
		var stop func()
		code, stop, err = holdOwnedSession(ctx, getSocketPath(cmd), project)
		if err != nil {
			return err
		}
		defer stop()
		if err := enc.Encode(digestLine{Type: "session", SessionCode: code}); err != nil {
			return err
		}
	}
	filter := tools.IncidentDigestFilter(code, project)
	for {
		client := daemonclient.NewClient(daemonclient.WithSocketPath(getSocketPath(cmd)))
		err := client.StreamEvents(ctx, filter, func(entry proxy.LogEntry) error {
			if line, ok := digestLineFor(code, entry); ok {
				emit(line)
			}
			return nil
		})
		if ctx.Err() != nil {
			return nil
		}
		fmt.Fprintf(os.Stderr, "incidents watch: %v; reconnecting in 2s\n", err)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(2 * time.Second):
		}
	}
}

func runIncidentsForwarding(cmd *cobra.Command, args []string) error {
	code, err := incidentSession(cmd)
	if err != nil {
		return err
	}
	var paused bool
	switch strings.ToLower(args[0]) {
	case "pause":
		paused = true
	case "resume":
	default:
		return fmt.Errorf("expected pause or resume, got %q", args[0])
	}
	client, err := getSessionClient(cmd)
	if err != nil {
		return err
	}
	defer client.Close()
	if err := setForwardingForSession(client, code, paused); err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "forwarding %s for %s\n", map[bool]string{true: "paused", false: "resumed"}[paused], code)
	return nil
}

// setForwardingForSession binds client to code's session and flips forwarding.
// OVERLAY FORWARDING acts only on the connection's own session and takes no
// selector (a write path must not reach across sessions), and SESSION ATTACH
// binds by directory. So resolve the code's project, attach, and refuse unless
// the daemon bound exactly the requested session: with two agents in one
// checkout the attach can land on the other one, and pausing it would be wrong.
func setForwardingForSession(client *daemonclient.Client, code string, paused bool) error {
	sess, err := client.SessionGet(code)
	if err != nil {
		return fmt.Errorf("session %q: %w", code, err)
	}
	dir, _ := sess["project_path"].(string)
	if dir == "" {
		return fmt.Errorf("session %q has no project path", code)
	}
	att, err := client.SessionAttach(dir)
	if err != nil {
		return fmt.Errorf("attach to %s: %w", dir, err)
	}
	if got, _ := att["session_code"].(string); got != code {
		return fmt.Errorf("directory %s resolves to session %q, not %q: refusing to change another session's forwarding", dir, got, code)
	}
	return client.SetForwarding(paused)
}
