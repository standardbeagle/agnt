package daemon

import (
	"testing"
	"time"
)

// The overview refreshes right after kill-orphans / kill-port; a cache still
// holding the pre-kill scan showed the killed entries as alive.
func TestInvalidatePortsCacheForcesRescan(t *testing.T) {
	portsCache.mu.Lock()
	portsCache.at = time.Now()
	portsCache.mu.Unlock()

	invalidatePortsCache()

	portsCache.mu.Lock()
	defer portsCache.mu.Unlock()
	if !portsCache.at.IsZero() {
		t.Fatal("cache still fresh after invalidation; the next PORTS QUERY would serve the stale scan")
	}
}
