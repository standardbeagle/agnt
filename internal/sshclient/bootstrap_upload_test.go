package sshclient

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// errAfterReader returns n bytes of data then a non-EOF error, simulating
// an aborted upload (dropped connection, local read failure) rather than a
// clean end of input.
type errAfterReader struct {
	data []byte
	n    int
	err  error
}

func (r *errAfterReader) Read(p []byte) (int, error) {
	if r.n <= 0 {
		return 0, r.err
	}
	take := r.n
	if take > len(p) {
		take = len(p)
	}
	if take > len(r.data) {
		take = len(r.data)
	}
	copy(p, r.data[:take])
	r.data = r.data[take:]
	r.n -= take
	return take, nil
}

var errSimulatedAbort = errors.New("simulated upload abort")

func TestUploadFile_SuccessfulUploadIsAtomicAndVerified(t *testing.T) {
	remoteHome := t.TempDir()
	fixture := newFixtureServer(t)
	fixture.onSession = execFixtureHandler(t, remoteHome)
	stop := fixture.serve(t)
	defer stop()

	client := dialFixtureClient(t, fixture).SSH

	content := []byte("#!/bin/sh\necho fake-agnt-binary\n")
	finalPath := filepath.Join(remoteHome, ".local", "bin", "agnt")

	if err := UploadFile(client, strings.NewReader(string(content)), finalPath, 0o755); err != nil {
		t.Fatalf("UploadFile: %v", err)
	}

	got, err := os.ReadFile(finalPath)
	if err != nil {
		t.Fatalf("reading uploaded file: %v", err)
	}
	if string(got) != string(content) {
		t.Errorf("uploaded content = %q, want %q", got, content)
	}

	info, err := os.Stat(finalPath)
	if err != nil {
		t.Fatalf("stat uploaded file: %v", err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Errorf("uploaded file mode = %o, want 0755", info.Mode().Perm())
	}

	// No leftover temp files at the final directory.
	entries, err := os.ReadDir(filepath.Dir(finalPath))
	if err != nil {
		t.Fatalf("reading dir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "agnt" {
		names := make([]string, len(entries))
		for i, e := range entries {
			names[i] = e.Name()
		}
		t.Errorf("expected exactly one file 'agnt' in target dir, got %v", names)
	}
}

func TestUploadFile_RepeatedSuccessReleasesWriteSessions(t *testing.T) {
	remoteHome := t.TempDir()
	fixture := newFixtureServer(t)
	fixture.onSession = execFixtureHandler(t, remoteHome)
	stop := fixture.serve(t)
	defer stop()

	client := dialFixtureClient(t, fixture).SSH
	for i := 0; i < 25; i++ {
		finalPath := filepath.Join(remoteHome, "uploads", fmt.Sprintf("artifact-%02d", i))
		if err := UploadFile(client, strings.NewReader("content"), finalPath, 0o644); err != nil {
			t.Fatalf("UploadFile iteration %d: %v", i, err)
		}
	}
}

// TestUploadFile_AbortedUploadLeavesNoPartialBinary pins acceptance
// criterion 3: if the source read fails partway through (simulating a
// dropped connection or local I/O error), UploadFile must return an error
// and the FINAL path must never come into existence — proving the
// temp-name-then-rename design actually avoids partial writes, not just
// that it happens to usually work.
func TestUploadFile_AbortedUploadLeavesNoPartialBinary(t *testing.T) {
	remoteHome := t.TempDir()
	fixture := newFixtureServer(t)
	fixture.onSession = execFixtureHandler(t, remoteHome)
	stop := fixture.serve(t)
	defer stop()

	client := dialFixtureClient(t, fixture).SSH

	// A reader that yields some bytes, then errors instead of hitting EOF.
	src := &errAfterReader{
		data: []byte("this looks like the start of a real binary......."),
		n:    16,
		err:  errSimulatedAbort,
	}
	finalPath := filepath.Join(remoteHome, ".local", "bin", "agnt")

	err := UploadFile(client, src, finalPath, 0o755)
	if err == nil {
		t.Fatal("expected UploadFile to fail for an aborted source read, got nil error")
	}
	if !errors.Is(err, errSimulatedAbort) {
		t.Errorf("expected error chain to include the simulated abort cause, got: %v", err)
	}

	if _, statErr := os.Stat(finalPath); !os.IsNotExist(statErr) {
		t.Fatalf("final path %s must not exist after an aborted upload, stat err: %v", finalPath, statErr)
	}

	// The parent dir may or may not exist (mkdir -p ran before the abort),
	// but if it does, it must not contain the final filename under any
	// name that would pass as the installed binary.
	if entries, readErr := os.ReadDir(filepath.Dir(finalPath)); readErr == nil {
		for _, e := range entries {
			if e.Name() == "agnt" {
				t.Errorf("found a file literally named 'agnt' after aborted upload: %v", e.Name())
			}
		}
	}
}

func TestUploadFile_RemoteWriteFailureLeavesNoPartialBinary(t *testing.T) {
	// A directory that can't be created (parent is unwritable) forces the
	// remote "mkdir -p && cat >" step itself to fail, exercising the
	// writeSession.Wait() error path distinctly from the read-abort path
	// above.
	remoteHome := t.TempDir()
	fixture := newFixtureServer(t)
	fixture.onSession = execFixtureHandler(t, remoteHome)
	stop := fixture.serve(t)
	defer stop()

	client := dialFixtureClient(t, fixture).SSH

	unwritableParent := filepath.Join(remoteHome, "unwritable")
	if err := os.Mkdir(unwritableParent, 0o500); err != nil {
		t.Fatalf("setting up unwritable dir: %v", err)
	}
	t.Cleanup(func() { os.Chmod(unwritableParent, 0o700) })

	finalPath := filepath.Join(unwritableParent, "nested", "agnt")
	err := UploadFile(client, strings.NewReader("content"), finalPath, 0o755)
	if err == nil {
		t.Fatal("expected UploadFile to fail when the remote mkdir/write step fails")
	}
	if _, statErr := os.Stat(finalPath); !os.IsNotExist(statErr) {
		t.Fatalf("final path must not exist after a failed remote write, stat err: %v", statErr)
	}
}

var _ io.Reader = (*errAfterReader)(nil)
