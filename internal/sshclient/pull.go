package sshclient

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/pkg/sftp"
	"github.com/standardbeagle/agnt/internal/protocol"
	"github.com/standardbeagle/agnt/internal/proxy"
)

// pullEventStream is the part of daemon.Client used by RemotePullManager.
// Keeping the boundary narrow also lets tests drive the same LogEntry shape
// produced by the real forwarded STREAM-EVENTS connection.
type pullEventStream interface {
	StreamEvents(context.Context, protocol.StreamEventFilter, func(proxy.LogEntry) error) error
}

// RemotePullManager downloads files referenced by remote screenshot events.
// The event stream is only the notification path; file bytes always travel
// over SFTP and are activated locally by rename after the copy succeeds.
type RemotePullManager struct {
	mu      sync.Mutex
	events  pullEventStream
	sftp    *sftp.Client
	host    string
	baseDir string
	notify  func(string)
	cancel  context.CancelFunc
	wg      sync.WaitGroup
	retry   time.Duration
}

// NewRemotePullManager creates a reverse-pull consumer. baseDir may be empty,
// in which case ~/.agnt/drop is used. notify receives both successful local
// paths and loud stream/download errors; callers normally print these lines.
func NewRemotePullManager(events pullEventStream, sc *sftp.Client, host, baseDir string, notify func(string)) *RemotePullManager {
	return &RemotePullManager{
		events:  events,
		sftp:    sc,
		host:    safePathComponent(host),
		baseDir: baseDir,
		notify:  notify,
		retry:   2 * time.Second,
	}
}

// Start begins consuming screenshot and snapshot-related capture events.
// Repeated Start calls replace and fully drain the previous subscription.
func (m *RemotePullManager) Start(ctx context.Context) {
	m.Stop()
	m.mu.Lock()
	ctx, m.cancel = context.WithCancel(ctx)
	m.wg.Add(1)
	m.mu.Unlock()
	go m.run(ctx)
}

// Resume swaps in the event and SFTP connections established after an SSH
// reconnect, then starts a fresh subscription.
func (m *RemotePullManager) Resume(ctx context.Context, events pullEventStream, sc *sftp.Client) {
	m.Stop()
	m.mu.Lock()
	m.events = events
	m.sftp = sc
	m.mu.Unlock()
	m.Start(ctx)
}

// Stop cancels the stream and waits until all event/download work is drained.
func (m *RemotePullManager) Stop() {
	m.mu.Lock()
	cancel := m.cancel
	m.cancel = nil
	m.mu.Unlock()
	if cancel != nil {
		cancel()
		m.wg.Wait()
	}
}

func (m *RemotePullManager) run(ctx context.Context) {
	defer m.wg.Done()
	filter := protocol.StreamEventFilter{Types: []string{
		string(proxy.LogTypeScreenshot),
		string(proxy.LogTypeScreenshotCapture),
		string(proxy.LogTypeSketchCapture),
	}}
	for ctx.Err() == nil {
		m.mu.Lock()
		events := m.events
		retry := m.retry
		m.mu.Unlock()
		if events == nil {
			m.report("agnt ssh: reverse pull unavailable: daemon event stream is nil")
		} else if err := events.StreamEvents(ctx, filter, m.handleEvent); err != nil && ctx.Err() == nil {
			m.report(fmt.Sprintf("agnt ssh: reverse pull event stream failed: %v", err))
		}
		if ctx.Err() != nil {
			return
		}
		timer := time.NewTimer(retry)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return
		case <-timer.C:
		}
	}
}

func (m *RemotePullManager) handleEvent(entry proxy.LogEntry) error {
	remotePath := pullEventPath(entry)
	if remotePath == "" {
		m.report(fmt.Sprintf("agnt ssh: reverse pull received %s event without a remote file path", entry.Type))
		return nil
	}
	localPath, err := m.download(remotePath)
	if err != nil {
		m.report(fmt.Sprintf("agnt ssh: reverse pull failed for %s: %v", remotePath, err))
		return nil
	}
	m.report(fmt.Sprintf("agnt ssh: downloaded remote capture to %s", localPath))
	return nil
}

func pullEventPath(entry proxy.LogEntry) string {
	switch entry.Type {
	case proxy.LogTypeScreenshot:
		if entry.Screenshot != nil && entry.Screenshot.Error == "" {
			return entry.Screenshot.FilePath
		}
	case proxy.LogTypeScreenshotCapture:
		if entry.ScreenshotCapture != nil {
			return entry.ScreenshotCapture.FilePath
		}
	case proxy.LogTypeSketchCapture:
		if entry.SketchCapture != nil {
			return entry.SketchCapture.FilePath
		}
	}
	return ""
}

func (m *RemotePullManager) download(remotePath string) (localPath string, err error) {
	name := filepath.Base(filepath.Clean(remotePath))
	if name == "." || name == string(filepath.Separator) || name == "" {
		return "", fmt.Errorf("invalid remote file path %q", remotePath)
	}
	m.mu.Lock()
	sc, baseDir, host := m.sftp, m.baseDir, m.host
	m.mu.Unlock()
	if sc == nil {
		return "", fmt.Errorf("SFTP client is nil")
	}
	if baseDir == "" {
		home, homeErr := os.UserHomeDir()
		if homeErr != nil {
			return "", fmt.Errorf("resolving home directory: %w", homeErr)
		}
		baseDir = filepath.Join(home, ".agnt", "drop")
	}
	dir := filepath.Join(baseDir, host, "from-remote")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("creating local drop directory %s: %w", dir, err)
	}

	remote, err := sc.Open(remotePath)
	if err != nil {
		return "", fmt.Errorf("opening remote file: %w", err)
	}
	defer remote.Close()

	tmp, err := os.CreateTemp(dir, "."+name+".pull-*")
	if err != nil {
		return "", fmt.Errorf("creating local temporary file: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() {
		tmp.Close()
		if err != nil {
			os.Remove(tmpPath)
		}
	}()
	if _, err = io.Copy(tmp, remote); err != nil {
		return "", fmt.Errorf("copying remote file: %w", err)
	}
	if err = tmp.Sync(); err != nil {
		return "", fmt.Errorf("syncing local temporary file: %w", err)
	}
	if err = tmp.Close(); err != nil {
		return "", fmt.Errorf("closing local temporary file: %w", err)
	}
	localPath = filepath.Join(dir, name)
	if err = os.Rename(tmpPath, localPath); err != nil {
		return "", fmt.Errorf("activating local file %s: %w", localPath, err)
	}
	return localPath, nil
}

func (m *RemotePullManager) report(message string) {
	if m.notify != nil {
		m.notify(message)
	}
}

func safePathComponent(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "remote"
	}
	return strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' || r == 0 {
			return '_'
		}
		return r
	}, value)
}
