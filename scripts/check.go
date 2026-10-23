//go:build ignore

// check runs the repo's portable verification steps without make or a POSIX
// shell, so they behave the same on Windows, macOS and Linux.
//
// Usage (from anywhere in the repo):
//
//	go run ./scripts/check.go vendor   # vendor/ must equal what go.mod resolves to
//	go run ./scripts/check.go cross    # windows/amd64 and darwin/arm64 build
//
// Both are also tman aliases: `tman vendor-check`, `tman cross-compile-check`.
package main

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

func main() {
	if len(os.Args) != 2 {
		fail("usage: go run ./scripts/check.go vendor|cross")
	}
	root, err := output("git", "rev-parse", "--show-toplevel")
	if err != nil {
		fail("not in a git repository: %v", err)
	}
	if err := os.Chdir(strings.TrimSpace(root)); err != nil {
		fail("%v", err)
	}
	switch os.Args[1] {
	case "vendor":
		vendorCheck()
	case "cross":
		crossCheck()
	default:
		fail("unknown check %q", os.Args[1])
	}
}

// crossCheck mirrors `make cross-compile-check`.
func crossCheck() {
	for _, t := range [][2]string{{"windows", "amd64"}, {"darwin", "arm64"}} {
		fmt.Printf("==> Checking %s/%s build...\n", t[0], t[1])
		cmd := exec.Command("go", "build", "./...")
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS="+t[0], "GOARCH="+t[1])
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil {
			fail("%s/%s build failed: %v", t[0], t[1], err)
		}
	}
	fmt.Println("Cross-compile check passed")
}

// vendorCheck mirrors `make vendor-check`: vendor into a temp directory,
// drop files the repo's .gitignore excludes (go mod vendor writes
// vendor/cloud.google.com/go/go.work, which a clean checkout never has), and
// compare against the committed vendor/. GOWORK=off because a go.work puts go
// in workspace mode, where `go mod vendor` refuses to run.
func vendorCheck() {
	tmp, err := os.MkdirTemp("", "vendor-check")
	if err != nil {
		fail("%v", err)
	}
	defer os.RemoveAll(tmp)

	cmd := exec.Command("go", "mod", "vendor", "-o", filepath.Join(tmp, "vendor"))
	cmd.Env = append(os.Environ(), "GOWORK=off")
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		fail("go mod vendor failed: %v", err)
	}

	fresh := listFiles(filepath.Join(tmp, "vendor"))
	ignore := exec.Command("git", "check-ignore", "--stdin")
	ignore.Stdin = strings.NewReader(strings.Join(prefixed("vendor/", fresh), "\n") + "\n")
	ignored, _ := ignore.Output() // exit 1 means nothing is ignored
	for _, p := range strings.Fields(string(ignored)) {
		os.Remove(filepath.Join(tmp, filepath.FromSlash(p)))
	}

	want := listFiles(filepath.Join(tmp, "vendor"))
	have := listFiles("vendor")
	var diffs []string
	wantSet := map[string]bool{}
	for _, f := range want {
		wantSet[f] = true
	}
	haveSet := map[string]bool{}
	for _, f := range have {
		haveSet[f] = true
		if !wantSet[f] {
			diffs = append(diffs, "only in vendor/: "+f)
		}
	}
	for _, f := range want {
		if !haveSet[f] {
			diffs = append(diffs, "missing from vendor/: "+f)
			continue
		}
		a, _ := os.ReadFile(filepath.Join("vendor", filepath.FromSlash(f)))
		b, _ := os.ReadFile(filepath.Join(tmp, "vendor", filepath.FromSlash(f)))
		if !bytes.Equal(a, b) {
			diffs = append(diffs, "differs: "+f)
		}
	}
	if len(diffs) == 0 {
		fmt.Println("vendor-check passed: vendor/ matches go.mod")
		return
	}
	fmt.Println("FAIL: vendor/ differs from what go.mod resolves to:")
	for i, d := range diffs {
		if i == 40 {
			fmt.Printf("... and %d more\n", len(diffs)-40)
			break
		}
		fmt.Println(d)
	}
	fmt.Println("A patch must not live only in vendor/ (it never reaches go install")
	fmt.Println("builds and the next go mod vendor drops it). Send it upstream and")
	fmt.Println("bump the version, or run make vendor if go.mod changed.")
	os.Exit(1)
}

// listFiles returns the slash-separated paths of regular files under dir,
// relative to dir, sorted.
func listFiles(dir string) []string {
	var out []string
	filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			rel, _ := filepath.Rel(dir, p)
			out = append(out, filepath.ToSlash(rel))
		}
		return nil
	})
	sort.Strings(out)
	return out
}

func prefixed(prefix string, in []string) []string {
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = prefix + s
	}
	return out
}

func output(name string, args ...string) (string, error) {
	b, err := exec.Command(name, args...).Output()
	return string(b), err
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
