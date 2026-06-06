package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"syscall"
	"time"

	"github.com/standardbeagle/agnt/internal/config"
	"github.com/standardbeagle/agnt/internal/platform"
	"github.com/standardbeagle/agnt/internal/protocol"

	hubpkg "github.com/standardbeagle/go-cli-server/hub"
	hubproto "github.com/standardbeagle/go-cli-server/protocol"
)

// portsCache memoizes ListListeningPorts across the overview's 2s status tick.
// The /proc/*/fd walk is non-trivial on a busy host; a short TTL keeps rapid
// PORTS QUERY calls cheap without making the inventory noticeably stale.
var portsCache struct {
	mu     sync.Mutex
	at     time.Time
	owners []config.PortOwner
}

const portsCacheTTL = 4 * time.Second

func cachedListeningPorts(ctx context.Context) []config.PortOwner {
	portsCache.mu.Lock()
	defer portsCache.mu.Unlock()
	if !portsCache.at.IsZero() && time.Since(portsCache.at) < portsCacheTTL {
		return portsCache.owners
	}
	portsCache.owners = config.ListListeningPorts(ctx)
	portsCache.at = time.Now()
	return portsCache.owners
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
func (d *Daemon) hubHandlePorts(ctx context.Context, conn *hubpkg.Connection, cmd *hubproto.Command) error {
	valid := []string{protocol.SubVerbQuery, protocol.SubVerbCleanOrphans}
	return newCommandRouter("PORTS").
		withDefault(func(_ context.Context, conn *hubpkg.Connection, cmd *hubproto.Command) error {
			return writeStructuredErr(conn, "daemon", &hubproto.StructuredError{
				Code:         hubproto.ErrInvalidAction,
				Message:      "unknown action",
				Command:      "PORTS",
				Action:       cmd.SubVerb,
				ValidActions: valid,
			})
		}).
		dispatch(ctx, conn, cmd, map[string]handlerFn{
			protocol.SubVerbQuery:        d.hubHandlePortsQuery,
			protocol.SubVerbCleanOrphans: d.hubHandlePortsCleanOrphans,
		})
}

// hubHandlePortsQuery returns every listening TCP port with its owner and a
// managed/unmanaged/conflict classification, plus the current orphaned process
// groups. Classification:
//   - managed:    owner PID is in the daemon's managed-PID set (or a descendant)
//   - conflict:   port is declared in .agnt.kdl but its owner is not managed
//   - unmanaged:  any other listening port
func (d *Daemon) hubHandlePortsQuery(ctx context.Context, conn *hubpkg.Connection, cmd *hubproto.Command) error {
	dirFilter, err := unmarshalCommand[hubproto.DirectoryFilter](cmd)
	if err != nil {
		return conn.WriteErr(hubproto.ErrInvalidArgs, fmt.Sprintf("invalid filter JSON: %v", err))
	}
	projectPath, _, err := d.resolveProjectScope(protocol.DirectoryFilter{
		Global:      dirFilter.Global,
		SessionCode: dirFilter.SessionCode,
		Directory:   dirFilter.Directory,
	}, conn.SessionCode())
	if err != nil {
		return conn.WriteErr(hubproto.ErrInvalidArgs, err.Error())
	}

	owners := cachedListeningPorts(ctx)
	managed := d.collectManagedPIDs()
	declared := d.declaredPorts(projectPath)

	ports := make([]map[string]interface{}, 0, len(owners))
	for _, o := range owners {
		status := "unmanaged"
		switch {
		case o.PID > 0 && managed[o.PID]:
			status = "managed"
		case declared[o.Port]:
			status = "conflict"
		}
		ports = append(ports, map[string]interface{}{
			"port":    o.Port,
			"pid":     o.PID,
			"name":    o.Name,
			"windows": o.Windows,
			"status":  status,
			"system":  portIsSystem(o, status),
		})
	}

	orphans := make([]map[string]interface{}, 0)
	for _, orph := range platform.ScanOrphanedPGIDs(syscall.Getuid(), d.orphanScanExcludes()) {
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

// hubHandlePortsCleanOrphans reaps every orphaned process group owned by the
// caller's uid (leader dead, members alive) using the same kill primitive as
// the startup orphan scan.
func (d *Daemon) hubHandlePortsCleanOrphans(ctx context.Context, conn *hubpkg.Connection, cmd *hubproto.Command) error {
	excludes := d.orphanScanExcludes()
	orphans := platform.ScanOrphanedPGIDs(syscall.Getuid(), excludes)

	selfPID := syscall.Getpid()
	reaped := make([]int, 0, len(orphans))
	failed := make([]map[string]interface{}, 0)
	for _, orph := range orphans {
		if err := platform.KillSessionPGID(orph.PGID, selfPID, startupOrphanPGIDGrace, false); err != nil {
			failed = append(failed, map[string]interface{}{"pgid": orph.PGID, "error": err.Error()})
			continue
		}
		reaped = append(reaped, orph.PGID)
	}

	data, _ := json.Marshal(map[string]interface{}{
		"reaped_count": len(reaped),
		"reaped_pgids": reaped,
		"failed":       failed,
	})
	return conn.WriteJSON(data)
}

// declaredPorts returns the set of ports declared in the project's .agnt.kdl
// (script ports + proxy fallback ports). Empty set on any load failure.
func (d *Daemon) declaredPorts(projectPath string) map[int]bool {
	out := make(map[int]bool)
	if projectPath == "" {
		return out
	}
	cfg, err := config.LoadAgntConfig(projectPath)
	if err != nil || cfg == nil {
		return out
	}
	for _, sc := range cfg.Scripts {
		for _, p := range sc.Ports {
			if p > 0 {
				out[p] = true
			}
		}
	}
	for _, pc := range cfg.Proxies {
		if pc.FallbackPort > 0 {
			out[pc.FallbackPort] = true
		}
	}
	return out
}

// orphanScanExcludes returns the pgid set the orphan scan must never reap:
// the daemon's own process group (defensive — the daemon should not be a
// session leader of an orphan, but exclude it regardless).
func (d *Daemon) orphanScanExcludes() map[int]bool {
	excludes := make(map[int]bool)
	if pgid, err := syscall.Getpgid(syscall.Getpid()); err == nil && pgid > 1 {
		excludes[pgid] = true
	}
	return excludes
}
