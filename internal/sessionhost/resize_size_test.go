package sessionhost

import (
	"errors"
	"testing"
)

// SESSION-HOST RESIZE refuses a request only when BOTH dimensions are
// non-positive, so {cols:120, rows:0} and negative values reached Resize. A
// zero dimension handed the child a 0-line terminal, and a negative or
// oversized one wrapped through the 16-bit winsize field. Resize must refuse
// them before touching the PTY.
func TestResize_RejectsDimensionsOutsideWinsizeRange(t *testing.T) {
	s := &Session{} // no PTY: a refused size must never reach it
	for _, size := range [][2]int{{120, 0}, {0, 40}, {-1, 40}, {120, -5}, {0x10000, 40}, {120, 0x10000}} {
		if err := s.Resize(size[0], size[1]); !errors.Is(err, ErrInvalidSize) {
			t.Errorf("Resize(%d, %d) = %v, want ErrInvalidSize", size[0], size[1], err)
		}
	}
}
