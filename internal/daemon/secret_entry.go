package daemon

import (
	"sort"

	"github.com/standardbeagle/agnt/internal/proxy"
	"github.com/standardbeagle/agnt/internal/store"
)

// wireSecretSink connects a proxy's browser-submitted secret-entry path to
// the project store. The value arrives from the proxy's "secret_submit"
// branch (which never places it in any event) and lands here as a
// secret-marked store entry: metadata {secret:true, fingerprint:last-4}.
// Every agent-visible read of the entry is masked (see hub_store.go); the
// only consumption path is env injection at spawn (injectSecretEnv).
func (d *Daemon) wireSecretSink(server *proxy.ProxyServer) {
	if server == nil || server.Path == "" || d.storem == nil {
		return
	}
	projectPath := server.Path
	server.SetSecretSink(func(name, value string) error {
		return d.storem.Set(projectPath, store.ScopeGlobal, "", name, value, map[string]any{
			store.MetaSecret:      true,
			store.MetaFingerprint: store.Fingerprint(value),
		})
	})
}

// injectSecretEnv appends NAME=value pairs for every secret-marked global
// store entry of the project, so agnt-managed processes can consume secrets
// by NAME without the value ever passing through the agent or the channel
// event stream. Non-secret entries are never injected; non-string or
// invalidly named secrets are skipped. Keys are sorted for determinism.
func (d *Daemon) injectSecretEnv(projectPath string, env []string) []string {
	if projectPath == "" || d.storem == nil {
		return env
	}
	entries, err := d.storem.GetAll(projectPath, store.ScopeGlobal, "")
	if err != nil {
		return env
	}
	keys := make([]string, 0, len(entries))
	for key, e := range entries {
		if store.IsSecretEntry(e) && store.ValidSecretName(key) {
			if _, ok := e.Value.(string); ok {
				keys = append(keys, key)
			}
		}
	}
	sort.Strings(keys)
	for _, key := range keys {
		env = append(env, key+"="+entries[key].Value.(string))
	}
	return env
}
