//go:build windows

package browser

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

var versionDirPattern = regexp.MustCompile(`^\d+\.\d+\.\d+\.\d+$`)

// readVersion reads the version from the install layout, never by running
// the binary. Chromium on Windows has no `--version` that prints and exits:
// every spelling (`--version`, `--headless=new --version`,
// `--product-version`) starts a real browser that stays running, so a probe
// built on it hangs the caller and leaves a stray Chrome behind. The
// installer instead keeps each version's payload in a directory named after
// it, next to the executable (Application\<version>\); the highest one is
// the version the launcher runs.
func readVersion(path string) (string, error) {
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		return "", err
	}
	best := ""
	for _, e := range entries {
		if e.IsDir() && versionDirPattern.MatchString(e.Name()) && versionLess(best, e.Name()) {
			best = e.Name()
		}
	}
	if best == "" {
		return "", fmt.Errorf("no version directory next to %s", path)
	}
	return best, nil
}

// versionLess reports whether dotted version a sorts before b, comparing
// components numerically ("9.0.0.0" < "10.0.0.0"). An empty a is lowest.
func versionLess(a, b string) bool {
	if a == "" {
		return true
	}
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(as) && i < len(bs); i++ {
		an, _ := strconv.Atoi(as[i])
		bn, _ := strconv.Atoi(bs[i])
		if an != bn {
			return an < bn
		}
	}
	return len(as) < len(bs)
}
