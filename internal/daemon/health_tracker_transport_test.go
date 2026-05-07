package daemon

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHealthTracker_TransportError_TriggersOutage(t *testing.T) {
	tr := NewHealthTracker(nil, nil)
	tr.SetTransportConfig(TransportConfig{
		Threshold:        1,
		Window:           time.Second,
		RecoveryDebounce: 100 * time.Millisecond,
	})

	require.False(t, tr.IsProxyInTransportOutage("p1"), "fresh tracker should not report outage")

	tr.RecordTransportError("p1", time.Now())
	assert.True(t, tr.IsProxyInTransportOutage("p1"), "single err with threshold=1 must enter outage")
}

func TestHealthTracker_TransportError_BelowThreshold(t *testing.T) {
	tr := NewHealthTracker(nil, nil)
	tr.SetTransportConfig(TransportConfig{
		Threshold:        3,
		Window:           time.Second,
		RecoveryDebounce: 100 * time.Millisecond,
	})

	now := time.Now()
	tr.RecordTransportError("p1", now)
	tr.RecordTransportError("p1", now.Add(10*time.Millisecond))
	assert.False(t, tr.IsProxyInTransportOutage("p1"), "2 errs below threshold=3")

	tr.RecordTransportError("p1", now.Add(20*time.Millisecond))
	assert.True(t, tr.IsProxyInTransportOutage("p1"), "third err crosses threshold")
}

func TestHealthTracker_TransportError_WindowEviction(t *testing.T) {
	tr := NewHealthTracker(nil, nil)
	tr.SetTransportConfig(TransportConfig{
		Threshold:        2,
		Window:           500 * time.Millisecond,
		RecoveryDebounce: 100 * time.Millisecond,
	})

	now := time.Now()
	tr.RecordTransportError("p1", now)
	// Second err arrives outside the window — old timestamp evicted, new
	// count is 1, below threshold=2.
	tr.RecordTransportError("p1", now.Add(time.Second))
	assert.False(t, tr.IsProxyInTransportOutage("p1"), "stale timestamp must be evicted before threshold check")
}

func TestHealthTracker_RecoverySignal_ExitsOutage(t *testing.T) {
	tr := NewHealthTracker(nil, nil)
	tr.SetTransportConfig(TransportConfig{
		Threshold:        1,
		Window:           time.Second,
		RecoveryDebounce: 50 * time.Millisecond,
	})

	var recoveries atomic.Int32
	tr.SetOnTransportRecovery(func(string) { recoveries.Add(1) })

	now := time.Now()
	tr.RecordTransportError("p1", now)
	require.True(t, tr.IsProxyInTransportOutage("p1"))

	// Recovery within debounce — must NOT exit outage.
	tr.RecordRecoverySignal("p1", now.Add(20*time.Millisecond))
	assert.True(t, tr.IsProxyInTransportOutage("p1"), "recovery within debounce window must not flip outage")
	assert.Equal(t, int32(0), recoveries.Load(), "callback must not fire while debounced")

	// Recovery past debounce — must exit outage and fire callback exactly once.
	tr.RecordRecoverySignal("p1", now.Add(100*time.Millisecond))
	assert.False(t, tr.IsProxyInTransportOutage("p1"), "post-debounce recovery must exit outage")
	assert.Equal(t, int32(1), recoveries.Load(), "callback fires exactly once on outage exit")
}

func TestHealthTracker_RecoverySignal_NoOutage_NoOp(t *testing.T) {
	tr := NewHealthTracker(nil, nil)
	tr.SetTransportConfig(DefaultTransportConfig)

	var recoveries atomic.Int32
	tr.SetOnTransportRecovery(func(string) { recoveries.Add(1) })

	tr.RecordRecoverySignal("p1", time.Now())

	assert.False(t, tr.IsProxyInTransportOutage("p1"))
	assert.Equal(t, int32(0), recoveries.Load(), "no outage → no recovery callback")
}

func TestHealthTracker_ForgetProxy(t *testing.T) {
	tr := NewHealthTracker(nil, nil)
	tr.SetTransportConfig(TransportConfig{Threshold: 1, Window: time.Second, RecoveryDebounce: 0})

	tr.RecordTransportError("p1", time.Now())
	require.True(t, tr.IsProxyInTransportOutage("p1"))

	tr.ForgetProxy("p1")
	assert.False(t, tr.IsProxyInTransportOutage("p1"))
}

func TestHealthTracker_TransportRing_Bounded(t *testing.T) {
	tr := NewHealthTracker(nil, nil)
	tr.SetTransportConfig(TransportConfig{
		Threshold:        100, // very high so we never flip
		Window:           time.Hour,
		RecoveryDebounce: 0,
	})

	// Record many more errors than the ring size.
	now := time.Now()
	for i := 0; i < transportErrRingSize*10; i++ {
		tr.RecordTransportError("p1", now.Add(time.Duration(i)*time.Millisecond))
	}

	st, ok := tr.lookupTransport("p1")
	require.True(t, ok)
	st.mu.Lock()
	defer st.mu.Unlock()
	assert.LessOrEqual(t, len(st.errTimestamps), transportErrRingSize, "ring must stay bounded")
}
