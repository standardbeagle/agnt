package tools

// DualBackend holds either a daemon client or a legacy direct-access value,
// and dispatches to the appropriate path based on which is non-nil.
// D is the daemon type, L is the legacy type.
//
// Usage:
//
//	b := DualBackend[DaemonTools, proxy.ProxyManager]{Daemon: dt, Legacy: pm}
//	result, err := b.Dispatch(
//	    func(d *DaemonTools) (T, error) { ... },
//	    func(l *proxy.ProxyManager) (T, error) { ... },
//	)
type DualBackend[D, L any] struct {
	Daemon *D
	Legacy *L
}

// Dispatch calls daemonFn if the daemon is non-nil, otherwise calls legacyFn.
func (b DualBackend[D, L]) Dispatch(daemonFn func(*D) error, legacyFn func(*L) error) error {
	if b.Daemon != nil {
		return daemonFn(b.Daemon)
	}
	return legacyFn(b.Legacy)
}

// DispatchResult calls daemonFn if the daemon is non-nil, otherwise calls legacyFn.
// Use this variant when the dispatch function returns a value alongside an error.
func DispatchResult[D, L, R any](b DualBackend[D, L], daemonFn func(*D) (R, error), legacyFn func(*L) (R, error)) (R, error) {
	if b.Daemon != nil {
		return daemonFn(b.Daemon)
	}
	return legacyFn(b.Legacy)
}
