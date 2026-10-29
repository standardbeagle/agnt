package daemon

import (
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/standardbeagle/agnt/internal/config"
)

// reconcile.go implements live `.agnt.kdl` reconciliation: bringing the
// running scripts/proxies for a project in line with a freshly loaded config
// WITHOUT restarting the daemon or the AI session. It is the piece that lets
// `agnt run`/`agnt ai` setup write a config that takes effect in-place, and
// lets a hand-edited config be applied live.
//
// The diff is pure and signature-based: a script/proxy "changed" when the
// launch-relevant fields of its config differ from the snapshot captured when
// it was last started. This file owns only the classification; the apply path
// (ReconcileProject, reconcile_apply.go) reuses the existing start/stop/restart
// primitives, which piece C hardened for reliable same-port restarts.

// ReconcilePlan is the set of actions needed to bring running state in line
// with desired config. Each slice holds resource names (script names / proxy
// ids), sorted for deterministic application and testing.
type ReconcilePlan struct {
	StartScripts   []string // declared + autostart, not currently running
	StopScripts    []string // running, no longer declared
	RestartScripts []string // running + declared, but launch config changed

	StartProxies   []string // declared + autostart, not currently running
	StopProxies    []string // running, no longer declared
	RestartProxies []string // running + declared, but target/port changed

	// NeedsRestart names running, signature-unchanged scripts whose declared
	// config changed in a field that cannot be applied to the live process
	// (ports, depends-on — see scriptRuntimeFieldDiff). These are informational:
	// nothing here is auto-applied by RunAutostart, since the field is by
	// definition not part of scriptSignature. Each entry is
	// "<name>: <field>[, <field>...]" so the response never silently drops
	// a declared-but-unapplied edit (daemon-architecture.md § Silent Failure
	// Prohibition). Cheap-to-reload fields (url-matchers, auto-restart) are
	// applied live instead and never appear here.
	NeedsRestart []string
}

// IsEmpty reports whether the plan would make no changes and flagged no
// needs-restart edits.
func (p ReconcilePlan) IsEmpty() bool {
	return len(p.StartScripts) == 0 && len(p.StopScripts) == 0 && len(p.RestartScripts) == 0 &&
		len(p.StartProxies) == 0 && len(p.StopProxies) == 0 && len(p.RestartProxies) == 0 &&
		len(p.NeedsRestart) == 0
}

// scriptRuntimeFieldDiff classifies a running script's config fields that
// scriptSignature deliberately excludes (they do not change how the process
// launches, so they never trigger RestartScripts). urlMatchersChanged and
// autoRestartChanged are cheap to hot-reload directly onto the live process
// (URLTracker.SetURLMatchers, ProcessAutoRestarter.Register/Unregister).
// needsRestartFields lists fields that changed but can only take effect on
// an actual relaunch (ports feed pre-flight cleanup at start time only;
// depends-on only affects start-ordering). Hooks are deliberately not
// listed: process_exit_watcher.go and process_autorestart.go read the hook
// command fresh from d.scriptConfigs at fire time, so storing the edited
// config (which the caller does regardless of this diff) is itself the full
// hot-reload — no restart needed.
func scriptRuntimeFieldDiff(old, new *config.ScriptConfig) (urlMatchersChanged, autoRestartChanged bool, needsRestartFields []string) {
	if old == nil || new == nil {
		return false, false, nil
	}
	urlMatchersChanged = !reflect.DeepEqual(old.URLMatchers, new.URLMatchers)
	autoRestartChanged = old.AutoRestart != new.AutoRestart
	if !reflect.DeepEqual(old.Ports, new.Ports) {
		needsRestartFields = append(needsRestartFields, "ports")
	}
	if !reflect.DeepEqual(old.DependsOn, new.DependsOn) {
		needsRestartFields = append(needsRestartFields, "depends-on")
	}
	return urlMatchersChanged, autoRestartChanged, needsRestartFields
}

// diffByKey classifies keys across desired and running signature maps:
//
//   - in desired, not running          → start
//   - in running, not desired          → stop
//   - in both, signatures differ       → restart
//   - in both, signatures equal        → (unchanged, omitted)
//
// The signature is an opaque string the caller builds from the launch-relevant
// fields, so the same diff serves both scripts and proxies. Results are sorted.
func diffByKey(desired, running map[string]string) (start, stop, restart []string) {
	for name, want := range desired {
		have, ok := running[name]
		switch {
		case !ok:
			start = append(start, name)
		case have != want:
			restart = append(restart, name)
		}
	}
	for name := range running {
		if _, ok := desired[name]; !ok {
			stop = append(stop, name)
		}
	}
	sort.Strings(start)
	sort.Strings(stop)
	sort.Strings(restart)
	return start, stop, restart
}

// computeReconcile builds a ReconcilePlan from desired and running signature
// maps for scripts and proxies. Callers supply only the entries that count as
// "desired" (e.g. autostart-eligible) and "running"; the diff does the rest.
func computeReconcile(desiredScripts, runningScripts, desiredProxies, runningProxies map[string]string) ReconcilePlan {
	var plan ReconcilePlan
	plan.StartScripts, plan.StopScripts, plan.RestartScripts = diffByKey(desiredScripts, runningScripts)
	plan.StartProxies, plan.StopProxies, plan.RestartProxies = diffByKey(desiredProxies, runningProxies)
	return plan
}

// scriptSignature captures the launch-relevant fields of a script config. A
// change to any of these means the running process is launching differently
// than the config now declares, so it must be restarted. Cosmetic fields
// (url-matchers, alerts) are intentionally excluded — they do not change how
// the process is launched.
func scriptSignature(s *config.ScriptConfig) string {
	if s == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString(s.Run)
	b.WriteByte('\x1f')
	b.WriteString(s.Command)
	b.WriteByte('\x1f')
	b.WriteString(strings.Join(s.Args, "\x00"))
	b.WriteByte('\x1f')
	b.WriteString(s.Shell)
	b.WriteByte('\x1f')
	b.WriteString(strings.Join(s.ShellArgs, "\x00"))
	b.WriteByte('\x1f')
	b.WriteString(s.Cwd)
	b.WriteByte('\x1f')
	// Env affects runtime behavior; fold it in deterministically.
	keys := make([]string, 0, len(s.Env))
	for k := range s.Env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(s.Env[k])
		b.WriteByte('\x00')
	}
	return b.String()
}

// proxySignature captures the launch-relevant fields of a proxy config: the
// effective target and the requested listen port/bind. A change means the
// proxy is pointing somewhere new or binding differently and must restart.
func proxySignature(p *config.ProxyConfig) string {
	if p == nil {
		return ""
	}
	target := p.URL
	if target == "" {
		target = p.Target
	}
	return strings.Join([]string{
		target,
		p.Host,
		strconv.Itoa(p.Port),
		strconv.Itoa(p.ListenPort),
		p.Bind,
		p.Script,
		p.URLPattern,
		strconv.Itoa(p.FallbackPort),
		strconv.Itoa(p.MaxLogSize),
		strconv.FormatBool(p.SkipTLSVerify),
		strconv.FormatBool(p.AllowExternal),
		p.PublicURL,
		strings.Join(p.WaitFor, "\x00"),
		cloudflareTunnelSignature(p.CloudflareTunnel),
	}, "\x1f")
}

// cloudflareTunnelSignature folds the cloudflare-tunnel block into the proxy
// signature, so editing the tunnel or its Access application restarts the
// proxy with the new values.
func cloudflareTunnelSignature(c *config.CloudflareTunnelConfig) string {
	if c == nil {
		return ""
	}
	var team, aud string
	if c.Access != nil {
		team, aud = c.Access.TeamDomain, c.Access.AUD
	}
	return strings.Join([]string{
		c.ID, c.Hostname, c.CredentialsFile, team, aud,
		strconv.FormatBool(c.AllowUnauthenticated),
	}, "\x00")
}
