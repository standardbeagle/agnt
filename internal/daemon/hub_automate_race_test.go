package daemon

import (
	"sync"
	"testing"

	"github.com/standardbeagle/agnt/internal/automation"
	"github.com/stretchr/testify/require"
)

// Every connection has its own dispatch goroutine, so two AUTOMATE requests
// can reach the lazy init at once. The unsynchronized check-then-set was a
// data race (-race reports it) and could build two processors, handing the
// callers different instances.
func TestGetOrCreateAutomator_ConcurrentCallersShareOneProcessor(t *testing.T) {
	d := &Daemon{}
	const callers = 16
	got := make([]*automation.Processor, callers)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			p, err := d.getOrCreateAutomator()
			require.NoError(t, err)
			got[i] = p
		}()
	}
	close(start)
	wg.Wait()
	for i := range got {
		require.NotNil(t, got[i])
		require.Same(t, got[0], got[i], "every caller must get the same processor")
	}
}
