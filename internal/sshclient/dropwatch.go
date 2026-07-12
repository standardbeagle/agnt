package sshclient

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/pkg/sftp"
)

const (
	dropQuiescence = 500 * time.Millisecond
	dropPollPeriod = 100 * time.Millisecond
)

// DropUpload uploads localPath under remoteName. Implementations must not
// overwrite an existing remote file.
type DropUpload func(localPath, remoteName string) error

// NewDropUpload adapts task 08a's verified SFTP upload for a drop watcher.
// Existing inbox names receive a timestamp suffix instead of being replaced.
func NewDropUpload(sc *sftp.Client, projectRoot string) DropUpload {
	return newDropUpload(sc, projectRoot, time.Now, nil)
}

func newDropUpload(sc *sftp.Client, projectRoot string, now func() time.Time, beforePush func(string)) DropUpload {
	return func(localPath, remoteName string) error {
		for attempts := 0; attempts < 100; attempts++ {
			candidate, err := collisionName(remoteName, now(), func(candidate string) (bool, error) {
				_, err := sc.Lstat(filepath.ToSlash(filepath.Join(projectRoot, DefaultInboxDir, candidate)))
				if os.IsNotExist(err) {
					return false, nil
				}
				return err == nil, err
			})
			if err != nil {
				return fmt.Errorf("checking remote collision: %w", err)
			}
			if beforePush != nil {
				beforePush(candidate)
			}
			file, err := os.Open(localPath)
			if err != nil {
				return err
			}
			_, pushErr := PushToInboxNoClobber(sc, projectRoot, "", candidate, file)
			closeErr := file.Close()
			if errors.Is(pushErr, ErrDestinationExists) {
				continue
			}
			if pushErr != nil {
				return pushErr
			}
			if closeErr != nil {
				return closeErr
			}
			return nil
		}
		return fmt.Errorf("sshclient: syncing %s: too many remote name collisions", remoteName)
	}
}

// DropWatcher synchronizes settled regular files from one local directory.
// It uses fsnotify for prompt delivery and also polls file metadata. The poll
// is intentional: notifications on WSL's /mnt/c 9P mounts are unreliable.
type DropWatcher struct {
	dir    string
	notify func(string)

	mu     sync.RWMutex
	upload DropUpload

	cancel context.CancelFunc
	done   chan struct{}
}

// NewDropWatcher creates and starts a watcher. Existing regular files are
// treated like newly created files and uploaded once they are quiescent.
func NewDropWatcher(dir string, upload DropUpload, notify func(string)) (*DropWatcher, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("sshclient: creating drop directory: %w", err)
	}
	fw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("sshclient: creating drop watcher: %w", err)
	}
	if err := fw.Add(dir); err != nil {
		fw.Close()
		return nil, fmt.Errorf("sshclient: watching drop directory: %w", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	w := &DropWatcher{dir: dir, upload: upload, notify: notify, cancel: cancel, done: make(chan struct{})}
	go w.run(ctx, fw)
	return w, nil
}

// SetUpload replaces the transport-dependent upload function. A nil function
// pauses uploads while local observation continues (for SSH reconnects).
func (w *DropWatcher) SetUpload(upload DropUpload) {
	w.mu.Lock()
	w.upload = upload
	w.mu.Unlock()
}

// Close stops the watcher and waits for its sole goroutine to exit.
func (w *DropWatcher) Close() error {
	w.cancel()
	<-w.done
	return nil
}

type dropState struct {
	size     int64
	modTime  time.Time
	changed  time.Time
	uploaded bool
}

func (w *DropWatcher) run(ctx context.Context, fw *fsnotify.Watcher) {
	defer close(w.done)
	defer fw.Close()
	ticker := time.NewTicker(dropPollPeriod)
	defer ticker.Stop()

	files := make(map[string]dropState)
	w.scan(files, time.Now())
	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-fw.Events:
			if !ok {
				return
			}
			if event.Op&(fsnotify.Create|fsnotify.Write|fsnotify.Rename) != 0 {
				w.observe(event.Name, files, time.Now())
			}
		case err, ok := <-fw.Errors:
			if ok {
				w.report(fmt.Sprintf("agnt ssh: drop-folder watch error: %v", err))
			}
		case now := <-ticker.C:
			w.scan(files, now)
			w.uploadSettled(files, now)
		}
	}
}

func (w *DropWatcher) scan(files map[string]dropState, now time.Time) {
	entries, err := os.ReadDir(w.dir)
	if err != nil {
		w.report(fmt.Sprintf("agnt ssh: scanning drop folder: %v", err))
		return
	}
	for _, entry := range entries {
		w.observe(filepath.Join(w.dir, entry.Name()), files, now)
	}
}

func (w *DropWatcher) observe(name string, files map[string]dropState, now time.Time) {
	info, err := os.Stat(name)
	if err != nil || !info.Mode().IsRegular() {
		delete(files, name)
		return
	}
	previous, known := files[name]
	if !known || previous.size != info.Size() || !previous.modTime.Equal(info.ModTime()) {
		files[name] = dropState{size: info.Size(), modTime: info.ModTime(), changed: now}
	}
}

func (w *DropWatcher) uploadSettled(files map[string]dropState, now time.Time) {
	w.mu.RLock()
	upload := w.upload
	w.mu.RUnlock()
	if upload == nil {
		return
	}
	for name, state := range files {
		if state.uploaded || now.Sub(state.changed) < dropQuiescence {
			continue
		}
		if err := upload(name, filepath.Base(name)); err != nil {
			state.changed = now
			files[name] = state
			w.report(fmt.Sprintf("agnt ssh: syncing %s: %v", filepath.Base(name), err))
			continue
		}
		state.uploaded = true
		files[name] = state
		w.report(fmt.Sprintf("agnt ssh: synced %s to %s/", filepath.Base(name), DefaultInboxDir))
	}
}

func (w *DropWatcher) report(message string) {
	if w.notify != nil {
		w.notify(message)
	}
}

// collisionName returns base unless it exists, then adds a UTC timestamp
// before the extension. A numeric suffix handles same-nanosecond collisions.
func collisionName(base string, now time.Time, exists func(string) (bool, error)) (string, error) {
	used, err := exists(base)
	if err != nil || !used {
		return base, err
	}
	ext := filepath.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	stamp := now.UTC().Format("20060102T150405.000000000Z")
	for i := 0; ; i++ {
		candidate := fmt.Sprintf("%s-%s%s", stem, stamp, ext)
		if i > 0 {
			candidate = fmt.Sprintf("%s-%s-%d%s", stem, stamp, i, ext)
		}
		used, err = exists(candidate)
		if err != nil || !used {
			return candidate, err
		}
	}
}
