//go:build windows

package publish

import (
	"fmt"

	"golang.org/x/sys/windows"
)

// renameDurable atomically replaces path with tmp and makes the rename itself
// durable. Windows cannot fsync a directory: os.Open returns a read-only
// handle and FlushFileBuffers on it fails with "Access is denied". The
// documented durability barrier for a rename is MOVEFILE_WRITE_THROUGH, which
// does not return until the move is flushed to disk.
func renameDurable(tmp, path string) error {
	from, err := windows.UTF16PtrFromString(tmp)
	if err != nil {
		return fmt.Errorf("rename: %w", err)
	}
	to, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return fmt.Errorf("rename: %w", err)
	}
	if err := windows.MoveFileEx(from, to, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH); err != nil {
		return fmt.Errorf("rename: %w", err)
	}
	return nil
}
