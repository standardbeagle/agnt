package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/standardbeagle/agnt/internal/config"
	"github.com/standardbeagle/agnt/internal/platform"
	"github.com/standardbeagle/agnt/internal/protocol"

	hubpkg "github.com/standardbeagle/go-cli-server/hub"
	hubproto "github.com/standardbeagle/go-cli-server/protocol"
	scriptpkg "github.com/standardbeagle/go-cli-server/script"
)

// portsCache memoizes ListListeningPorts across the overview's 2s status tick.
// The /proc/*/fd walk is non-trivial on a busy host; a short TTL keeps rapid
// PORTS QUERY calls cheap without making the inventory noticeably stale.
var portsCache struct {
	mu      sync.Mutex
	at      time.Time
	owners  []config.PortOwner
	managed map[int]bool
	orphans []platform.OrphanPGID
}

const portsCacheTTL = 4 * time.Second

// cachedPortsData returns the listening-port inventory, managed-PID set, and
// orphan process groups, refreshed together under one 4s TTL. All three derive
// from full host scans (config.ListListeningPorts /proc walk, collectManagedPIDs
// platform.Scan + parent-chain walk, ScanOrphanedPGIDs /proc walk) that the
// overview's 2s status tick would otherwise pay on every PORTS QUERY.
func (d *Daemon) cachedPortsData(ctx context.Context) ([]config.PortOwner, map[int]bool, []platform.OrphanPGID) {
	portsCache.mu.Lock()
	defer portsCache.mu.Unlock()
	if !portsCache.at.IsZero() && time.Since(portsCache.at) < portsCacheTTL {
		return portsCache.owners, portsCache.managed, portsCache.orphans
	}
	portsCache.owners = config.ListListeningPorts(ctx)
	portsCache.managed = d.collectManagedPIDs()
	portsCache.orphans = d.scanOrphans()
	portsCache.at = time.Now()
	return portsCache.owners, portsCache.managed, portsCache.orphans
}

// systemProcNames are OS/infra daemons whose listening ports are noise in a
// dev overview. Port-whisperer hides "system apps" by default; this is the
// equivalent denylist. Databases (postgres/mysql/redis/mongo) and docker are
// intentionally NOT here — they are dev-relevant.
var systemProcNames = map[string]bool{
	"systemd": true, "systemd-resolve": true, "systemd-resolved": true,
	"systemd-network": true, "systemd-networkd": true, "systemd-timesyn": true,
	"sshd": true, "cupsd": true, "cups-browsed": true, "avahi-daemon": true,
	"dbus-daemon": true, "dbus-broker": true, "chronyd": true, "rpcbind": true,
	"rpc.statd": true, "dnsmasq": true, "named": true, "snapd": true,
	"ModemManager": true, "NetworkManager": true, "wpa_supplicant": true,
	"postfix": true, "master": true, "smbd": true, "nmbd": true, "winbindd": true,
	"exim4": true, "slapd": true, "rpc.mountd": true,
}

// portIsSystem decides whether a listening port should be hidden from the
// default overview. Managed and conflict ports are always shown. Otherwise a
// port is "system" when its owner is an OS daemon, a privileged (<1024) infra
// port, an unattributable socket (owned by another uid — typically root infra),
// or a Windows-side listener seen from WSL (host noise).
func portIsSystem(o config.PortOwner, status string) bool {
	if status == "managed" || status == "conflict" {
		return false
	}
	if o.Windows {
		return true
	}
	if o.PID == 0 || o.Name == "" {
		return true
	}
	if systemProcNames[o.Name] {
		return true
	}
	if o.Port < 1024 {
		return true
	}
	return false
}

// hubHandlePorts routes the PORTS verb: QUERY (listening-port inventory +
// orphan process groups) and CLEAN-ORPHANS (reap orphaned pgids).
func (d *Daemon) portsActions() map[string]handlerFn {
	return map[string]handlerFn{
		protocol.SubVerbQuery:          d.hubHandlePortsQuery,
		protocol.SubVerbSetForwards:    d.hubHandlePortsSetForwards,
		protocol.SubVerbDeveloperEvent: d.hubHandleDeveloperEvent,
		protocol.SubVerbCleanOrphans:   d.hubHandlePortsCleanOrphans,
	}
}

func (d *Daemon) hubHandlePortsSetForwards(_ context.Context, conn *hubpkg.Connection, cmd *hubproto.Command) error {
	set, err := unmarshalCommand[protocol.ForwardSet](cmd)
	source := conn.SessionCode()
	if err != nil || source == "" {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "PORTS SET-FORWARDS requires a registered session")
	}
	d.forwardMappings.Store(source, append([]protocol.ForwardMapping(nil), set.Mappings...))
	return conn.WriteOK("forward mappings replaced")
}

func (d *Daemon) hubHandleDeveloperEvent(_ context.Context, conn *hubpkg.Connection, cmd *hubproto.Command) error {
	event, err := unmarshalCommand[protocol.DeveloperEvent](cmd)
	if err != nil || event.Kind == "" || event.Message == "" || event.ProjectPath == "" {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "PORTS DEVELOPER-EVENT requires kind, project_path, and message")
	}
	d.eventHub.DeliverDeveloperEvent(event)
	return conn.WriteOK("developer event delivered")
}

func (d *Daemon) hubHandlePorts(ctx context.Context, conn *hubpkg.Connection, cmd *hubproto.Command) error {
	actions := d.portsActions()
	return newCommandRouter("PORTS").
		withDefault(func(_ context.Context, conn *hubpkg.Connection, cmd *hubproto.Command) error {
			return writeStructuredErr(conn, "daemon", &hubproto.StructuredError{
				Code:         hubproto.ErrInvalidAction,
				Message:      "unknown action",
				Command:      "PORTS",
				Action:       cmd.SubVerb,
				ValidActions: routerSubVerbs(actions),
			})
		}).
		dispatch(ctx, conn, cmd, actions)
}

// hubHandlePortsQuery returns every listening TCP port with its owner and a
// managed/unmanaged/conflict classification, plus the current orphaned process
// groups. Classification:
//   - managed:    owner PID is in the daemon's managed-PID set (or a descendant)
//   - conflict:   port is declared in .agnt.kdl but its owner is not managed
//   - unmanaged:  any other listening port
func (d *Daemon) hubHandlePortsQuery(ctx context.Context, conn *hubpkg.Connection, cmd *hubproto.Command) error {
	dirFilter, err := unmarshalCommand[protocol.DirectoryFilter](cmd)
	if err != nil {
		return conn.WriteErr(hubproto.ErrInvalidArgs, fmt.Sprintf("invalid filter JSON: %v", err))
	}
	projectPath, _, err := d.resolveProjectScope(dirFilter, conn.SessionCode())
	if err != nil {
		return conn.WriteErr(hubproto.ErrInvalidArgs, err.Error())
	}

	owners, managed, orphanPGIDs := d.cachedPortsData(ctx)
	declared := d.declaredPortUses(projectPath)
	forwarded := d.forwardMappingsByRemotePort()

	ports := make([]map[string]interface{}, 0, len(owners))
	for _, o := range owners {
		status := classifyPortStatus(o, managed, declared[o.Port], func(scriptName string) bool {
			return d.scriptAppearsActive(projectPath, scriptName)
		})
		row := map[string]interface{}{
			"port":    o.Port,
			"pid":     o.PID,
			"name":    o.Name,
			"windows": o.Windows,
			"status":  status,
			"system":  portIsSystem(o, status),
		}
		if mapping, ok := forwarded[o.Port]; ok {
			row["forwarded"], row["local_port"], row["proxy_id"] = true, mapping.LocalPort, mapping.ProxyID
		}
		ports = append(ports, row)
	}

	orphans := make([]map[string]interface{}, 0)
	for _, orph := range orphanPGIDs {
		orphans = append(orphans, map[string]interface{}{
			"pgid":    orph.PGID,
			"members": orph.Members,
			"count":   len(orph.Members),
		})
	}

	data, _ := json.Marshal(map[string]interface{}{
		"ports":   ports,
		"orphans": orphans,
	})
	return conn.WriteJSON(data)
}

func (d *Daemon) forwardMappingsByRemotePort() map[int]protocol.ForwardMapping {
	forwarded := make(map[int]protocol.ForwardMapping)
	d.forwardMappings.Range(func(_, value any) bool {
		for _, mapping := range value.([]protocol.ForwardMapping) {
			forwarded[mapping.RemotePort] = mapping
		}
		return true
	})
	return forwarded
}

// hubHandlePortsCleanOrphans reaps every orphaned process group owned by the
// caller's uid (leader dead, members alive). The reap is platform-specific
// (Unix pgid kill; no-op on Windows, where Job Objects own cascade-kill).
func (d *Daemon) hubHandlePortsCleanOrphans(ctx context.Context, conn *hubpkg.Connection, cmd *hubproto.Command) error {
	reaped, failed := d.reapOrphans()

	data, _ := json.Marshal(map[string]interface{}{
		"reaped_count": len(reaped),
		"reaped_pgids": reaped,
		"failed":       failed,
	})
	return conn.WriteJSON(data)
}

type declaredPortUseKind string

const (
	declaredPortScript        declaredPortUseKind = "script"
	declaredPortProxyFallback declaredPortUseKind = "proxy_fallback"
)

type declaredPortUse struct {
	Kind       declaredPortUseKind
	ScriptName string
	ProxyName  string
}

// classifyPortStatus classifies one listening port for the overview.
//
// A proxy fallback port is a target/backend port, not a port agnt needs to bind,
// so it is never a conflict by itself. On native Windows and WSL interop,
// descendant ownership is often unavailable: the managed shell is known, but the
// child dev server owns the socket as a Windows PID. If the declaring script is
// active, treat that Windows listener as effectively managed to avoid false
// conflict rows for healthy dev servers.
func classifyPortStatus(o config.PortOwner, managed map[int]bool, uses []declaredPortUse, scriptActive func(string) bool) string {
	if o.PID > 0 && managed[o.PID] {
		return "managed"
	}
	hasScriptDeclaration := false
	for _, use := range uses {
		if use.Kind != declaredPortScript {
			continue
		}
		hasScriptDeclaration = true
		if o.Windows && scriptActive != nil && scriptActive(use.ScriptName) {
			return "managed"
		}
	}
	if hasScriptDeclaration {
		return "conflict"
	}
	return "unmanaged"
}

// declaredPortUses returns ports declared in the project's .agnt.kdl, preserving
// why each port was declared. Empty set on any load failure.
func (d *Daemon) declaredPortUses(projectPath string) map[int][]declaredPortUse {
	out := make(map[int][]declaredPortUse)
	if projectPath == "" {
		return out
	}
	cfg, err := config.LoadAgntConfig(projectPath)
	if err != nil || cfg == nil {
		return out
	}
	for scriptName, sc := range cfg.Scripts {
		for _, p := range sc.Ports {
			if p > 0 {
				out[p] = append(out[p], declaredPortUse{Kind: declaredPortScript, ScriptName: scriptName})
			}
		}
	}
	for proxyName, pc := range cfg.Proxies {
		if pc.FallbackPort > 0 {
			out[pc.FallbackPort] = append(out[pc.FallbackPort], declaredPortUse{
				Kind:       declaredPortProxyFallback,
				ScriptName: pc.Script,
				ProxyName:  proxyName,
			})
		}
	}
	return out
}

func (d *Daemon) scriptAppearsActive(projectPath, scriptName string) bool {
	if d == nil || d.scriptRegistry == nil || projectPath == "" || scriptName == "" {
		return false
	}
	entry, ok := d.scriptRegistry.Get(scriptName, projectPath)
	if !ok || entry == nil {
		return false
	}
	switch entry.State() {
	case scriptpkg.StateStarting, scriptpkg.StateRunning, scriptpkg.StateRestarting:
		return true
	default:
		return false
	}
}
