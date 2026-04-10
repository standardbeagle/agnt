package daemon

import (
	"context"
	"encoding/json"
	"time"

	"github.com/standardbeagle/agnt/internal/debug"
	"github.com/standardbeagle/agnt/internal/protocol"
	"github.com/standardbeagle/agnt/internal/proxy"
	hubpkg "github.com/standardbeagle/go-cli-server/hub"
	hubproto "github.com/standardbeagle/go-cli-server/protocol"
)

const (
	// streamKeepaliveInterval is how often heartbeat events are sent
	// when no real events have been emitted.
	streamKeepaliveInterval = 30 * time.Second
)

func (d *Daemon) hubHandleStreamEvents(ctx context.Context, conn *hubpkg.Connection, cmd *hubproto.Command) error {
	debug.Log("daemon", "STREAM-EVENTS: starting event stream")

	// Parse filter from request data
	var filter protocol.StreamEventFilter
	if len(cmd.Data) > 0 {
		if err := json.Unmarshal(cmd.Data, &filter); err != nil {
			debug.Log("daemon", "STREAM-EVENTS: invalid filter JSON: %v", err)
		}
	}

	// Build internal filter
	sf := buildStreamFilter(filter)

	// Register stream sink
	sink := d.alertHub.AddStreamSink(sf)
	defer d.alertHub.RemoveStreamSink(sink)

	// Send initial OK so client knows stream is active
	if err := conn.WriteOK("streaming"); err != nil {
		return err
	}

	// Stream events until context cancels or connection closes
	keepalive := time.NewTicker(streamKeepaliveInterval)
	defer keepalive.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case entry, ok := <-sink.Ch:
			if !ok {
				// Channel closed (unregistered)
				return conn.WriteEnd()
			}
			data, err := json.Marshal(entry)
			if err != nil {
				debug.Error("stream-events", "failed to marshal event: %v", err)
				continue
			}
			if err := conn.WriteChunk(data); err != nil {
				debug.Log("stream-events", "write failed, client disconnected: %v", err)
				return nil
			}
			keepalive.Reset(streamKeepaliveInterval)
		case <-keepalive.C:
			// Send keepalive as an empty chunk
			if err := conn.WriteChunk([]byte(`{"type":"keepalive"}`)); err != nil {
				debug.Log("stream-events", "keepalive write failed: %v", err)
				return nil
			}
		}
	}
}

// buildStreamFilter converts a protocol StreamEventFilter into an internal streamFilter.
func buildStreamFilter(f protocol.StreamEventFilter) streamFilter {
	sf := streamFilter{
		proxyID:  f.ProxyID,
		severity: f.Severity,
	}
	if len(f.Types) > 0 {
		sf.types = make(map[proxy.LogEntryType]bool, len(f.Types))
		for _, t := range f.Types {
			sf.types[proxy.LogEntryType(t)] = true
		}
	}
	return sf
}
