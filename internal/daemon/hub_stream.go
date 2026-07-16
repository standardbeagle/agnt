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
	filter, err := unmarshalCommand[protocol.StreamEventFilter](cmd)
	if err != nil {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "invalid stream filter: "+err.Error())
	}

	// Resolve the subscription through the same fail-loud session-scope gate
	// as list/query handlers. A raw, sessionless subscriber must explicitly
	// request global access instead of silently receiving every project.
	sf, err := d.resolveStreamFilter(filter, conn.SessionCode())
	if err != nil {
		return conn.WriteErr(hubproto.ErrInvalidArgs, err.Error())
	}

	// Register stream sink
	sink := d.eventHub.AddStreamSink(sf)
	defer d.eventHub.RemoveStreamSink(sink)

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

func (d *Daemon) resolveStreamFilter(f protocol.StreamEventFilter, connSessionCode string) (streamFilter, error) {
	directory := f.Directory
	if directory == "" {
		directory = f.ProjectPath
	}
	sc, err := d.resolveScope(protocol.DirectoryFilter{
		SessionCode:    f.SessionCode,
		Directory:      directory,
		Global:         f.Global,
		GlobalOverride: f.GlobalOverride,
	}, connSessionCode)
	if err != nil {
		return streamFilter{}, err
	}
	sf := buildStreamFilter(f)
	if sc.IsUnscoped() {
		sf.projectPath = ""
	} else {
		sf.projectPath = sc.ProjectPath()
	}
	return sf, nil
}

// buildStreamFilter converts a protocol StreamEventFilter into an internal streamFilter.
func buildStreamFilter(f protocol.StreamEventFilter) streamFilter {
	sf := streamFilter{
		proxyID:     f.ProxyID,
		projectPath: f.ProjectPath,
		processID:   f.ProcessID,
		severity:    f.Severity,
		grep:        f.Grep,
		grepStream:  f.GrepStream,
	}
	if len(f.Types) > 0 {
		sf.types = make(map[proxy.LogEntryType]bool, len(f.Types))
		for _, t := range f.Types {
			sf.types[proxy.LogEntryType(t)] = true
		}
	}
	return sf
}
