//go:build windows

package sessionhost

import (
	"os"

	"github.com/creack/pty"
)

func setPTYSize(ptmx *os.File, cols, rows int, before func()) error {
	if before != nil {
		before()
	}
	return pty.Setsize(ptmx, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
}
