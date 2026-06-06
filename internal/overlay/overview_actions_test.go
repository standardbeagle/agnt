package overlay

import (
	"bytes"
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeSummarizer is a test StatusSummarizer.
type fakeSummarizer struct {
	available bool
	summary   string
	err       error
	calls     atomic.Int32
}

func (f *fakeSummarizer) IsAvailable() bool { return f.available }

func (f *fakeSummarizer) Summarize(_ context.Context) (*SummaryResult, error) {
	f.calls.Add(1)
	if f.err != nil {
		return nil, f.err
	}
	return &SummaryResult{Summary: f.summary}, nil
}

// fakeConnector is a test DaemonConnector.
type fakeConnector struct {
	calls atomic.Int32
	err   error
}

func (f *fakeConnector) Connect() error {
	f.calls.Add(1)
	return f.err
}

func (f *fakeConnector) IsConnected() bool { return f.err == nil }

// newOverviewRouter builds a router focused on the overview panel with a
// buffer-backed renderer (so renders don't hit os.Stdout). The returned
// writeRecorder is the PTY; the overview actions must never write to it.
func newOverviewRouter(t *testing.T) (*InputRouter, *Overlay, *writeRecorder) {
	t.Helper()
	rec := &writeRecorder{}
	cfg := DefaultConfig()
	cfg.ShowIndicator = false
	ov := New(rec, 80, 24, cfg)
	ov.renderer = NewRenderer(&bytes.Buffer{}, 80, 24)
	router := NewInputRouter(rec, ov)
	ov.panelItems = []PanelItem{{Type: "overview", Label: "overview"}}
	ov.panelMode = true
	ov.panelIndex = 0
	ov.state.Store(int32(StateMenu))
	return router, ov, rec
}

func TestDrawOverview_ActionsAndConnectionStates(t *testing.T) {
	panels := []PanelItem{{Type: "overview", Label: "overview"}}
	connected := Status{DaemonConnected: ConnectionConnected}
	disconnected := Status{DaemonConnected: ConnectionDisconnected}

	render := func(status Status, a OverviewActions) string {
		var buf bytes.Buffer
		r := NewRenderer(&buf, 80, 24)
		r.DrawPanelView(panels, 0, status, 0, false, "", 0, false, a)
		return buf.String()
	}

	// "summarize" (lowercase) appears only on the actions line, gated by
	// SummarizeEnabled (the footer uses capital "Summarize").
	assert.Contains(t, render(connected, OverviewActions{SummarizeEnabled: true}), "summarize")
	assert.NotContains(t, render(connected, OverviewActions{SummarizeEnabled: false}), "summarize")
	assert.Contains(t, render(connected, OverviewActions{SummarizeEnabled: true, Summarizing: true}), "summarizing")
	assert.Contains(t, render(connected, OverviewActions{SummarizeEnabled: true, SummaryErr: "boom"}), "summarize failed")

	// Connection-line states.
	assert.Contains(t, render(disconnected, OverviewActions{}), "reconnect")
	assert.Contains(t, render(disconnected, OverviewActions{Connecting: true}), "connecting")
	assert.Contains(t, render(disconnected, OverviewActions{ConnectErr: "connection refused"}), "connection refused")
}

func TestBuildPanelItems_InjectsSummaryAtIndex1(t *testing.T) {
	cfg := DefaultConfig()
	cfg.ShowIndicator = false
	o := New(nil, 80, 24, cfg)

	o.mu.Lock()
	o.summaryText = "2 scripts up, 0 errors"
	o.buildPanelItems()
	o.mu.Unlock()

	require.GreaterOrEqual(t, len(o.panelItems), 2)
	assert.Equal(t, "overview", o.panelItems[0].Type)
	assert.Equal(t, "summary", o.panelItems[1].Type)
	assert.Contains(t, o.panelItems[1].Content, "2 scripts up, 0 errors")

	// Rebuilding does not duplicate the summary panel.
	o.mu.Lock()
	o.buildPanelItems()
	o.mu.Unlock()
	count := 0
	for _, p := range o.panelItems {
		if p.Type == "summary" {
			count++
		}
	}
	assert.Equal(t, 1, count, "summary panel must not be duplicated on rebuild")
}

func TestOverviewSummarize_OpensSummaryPanelNoPTYWrite(t *testing.T) {
	router, ov, rec := newOverviewRouter(t)
	sum := &fakeSummarizer{available: true, summary: "all systems nominal"}
	router.SetSummarizer(sum)

	router.handleMenuKey("m")

	require.Eventually(t, func() bool {
		ov.mu.Lock()
		defer ov.mu.Unlock()
		return ov.summaryText == "all systems nominal"
	}, 2*time.Second, 10*time.Millisecond)

	ov.mu.Lock()
	require.GreaterOrEqual(t, len(ov.panelItems), 2)
	assert.Equal(t, "summary", ov.panelItems[1].Type)
	assert.Equal(t, 1, ov.panelIndex, "summary panel must be focused")
	ov.mu.Unlock()

	assert.Equal(t, int32(1), sum.calls.Load())
	assert.Empty(t, rec.getWrites(), "summary must not be injected into the PTY")
}

func TestOverviewSummarize_UnavailableSetsError(t *testing.T) {
	router, ov, _ := newOverviewRouter(t)
	router.SetSummarizer(&fakeSummarizer{available: false})

	router.handleMenuKey("m")

	ov.mu.Lock()
	defer ov.mu.Unlock()
	assert.Contains(t, ov.summaryErr, "not available")
	assert.Empty(t, ov.summaryText)
	for _, p := range ov.panelItems {
		assert.NotEqual(t, "summary", p.Type, "no summary panel on unavailable summarizer")
	}
}

func TestOverviewSummarize_NoSummarizerIsNoop(t *testing.T) {
	router, ov, _ := newOverviewRouter(t)

	router.handleMenuKey("m") // no summarizer configured

	ov.mu.Lock()
	defer ov.mu.Unlock()
	assert.Empty(t, ov.summaryText)
	assert.Empty(t, ov.summaryErr)
}

func TestOverviewReconnect_CallsConnectWhenDisconnected(t *testing.T) {
	router, ov, _ := newOverviewRouter(t)
	ov.UpdateStatus(Status{DaemonConnected: ConnectionDisconnected})
	conn := &fakeConnector{}
	router.SetDaemonConnector(conn)

	router.handleMenuKey("c")

	require.Eventually(t, func() bool {
		return conn.calls.Load() == 1
	}, 2*time.Second, 10*time.Millisecond)
}

func TestOverviewReconnect_NoopWhenConnected(t *testing.T) {
	router, ov, _ := newOverviewRouter(t)
	ov.UpdateStatus(Status{DaemonConnected: ConnectionConnected})
	conn := &fakeConnector{}
	router.SetDaemonConnector(conn)

	router.handleMenuKey("c")

	// Give any errant goroutine a chance to run, then assert Connect was not called.
	time.Sleep(50 * time.Millisecond)
	assert.Equal(t, int32(0), conn.calls.Load(), "reconnect must be a no-op when already connected")
}
