//go:build !windows

package publish

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// renameDurable atomically replaces path with tmp and makes the rename itself
// durable: it fsyncs the parent directory, because without that a power loss
// after a successful rename() can still lose the new dir entry, leaving the
// record absent despite the temp-file fsync before it.
func renameDurable(tmp, path string) error {
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("rename: %w", err)
	}
	if err := fsyncDir(filepath.Dir(path)); err != nil {
		return fmt.Errorf("fsync dir: %w", err)
	}
	return nil
}

// fsyncDir flushes a directory entry to disk. A filesystem that does not
// support syncing a directory handle reports errors.ErrUnsupported (or EINVAL);
// those are non-fatal — the rename is still atomic, only the extra durability
// barrier is unavailable. Any other error is a real I/O fault and is returned.
func fsyncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	err = d.Sync()
	if cerr := d.Close(); err == nil {
		err = cerr
	}
	if err != nil && (errors.Is(err, errors.ErrUnsupported) || errors.Is(err, syscall.EINVAL)) {
		return nil
	}
	return err
}
