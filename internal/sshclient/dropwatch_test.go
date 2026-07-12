package sshclient

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"
)

func TestDropWatcher_SyncsWithinOneSecondOfWriteQuiescence(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())
	dir := t.TempDir()

	type uploadedFile struct {
		name string
		body string
		at   time.Time
	}
	var mu sync.Mutex
	var uploaded []uploadedFile
	w, err := NewDropWatcher(dir, func(localPath, remoteName string) error {
		body, err := os.ReadFile(localPath)
		if err != nil {
			return err
		}
		mu.Lock()
		uploaded = append(uploaded, uploadedFile{name: remoteName, body: string(body), at: time.Now()})
		mu.Unlock()
		return nil
	}, nil)
	require.NoError(t, err)

	file, err := os.Create(filepath.Join(dir, "notes.txt"))
	require.NoError(t, err)
	_, err = file.WriteString("first")
	require.NoError(t, err)
	require.Never(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(uploaded) != 0
	}, 250*time.Millisecond, 10*time.Millisecond)
	_, err = file.WriteString("-complete")
	require.NoError(t, err)
	require.NoError(t, file.Close())
	quiescentAt := time.Now()

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(uploaded) == 1
	}, time.Second, 10*time.Millisecond)
	mu.Lock()
	got := uploaded[0]
	mu.Unlock()
	require.Equal(t, "notes.txt", got.name)
	require.Equal(t, "first-complete", got.body)
	require.LessOrEqual(t, got.at.Sub(quiescentAt), time.Second)
	require.Never(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(uploaded) > 1
	}, 700*time.Millisecond, 10*time.Millisecond)
	require.NoError(t, w.Close())
}

func TestDropWatcher_PollFallbackFindsExistingFile(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "from-9p.txt"), []byte("ready"), 0o600))
	uploaded := make(chan string, 1)
	w, err := NewDropWatcher(dir, func(_ string, remoteName string) error {
		uploaded <- remoteName
		return nil
	}, nil)
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		select {
		case name := <-uploaded:
			return name == "from-9p.txt"
		default:
			return false
		}
	}, time.Second, 10*time.Millisecond)
	require.NoError(t, w.Close())
}

func TestCollisionName_AddsTimestampAndNeverChoosesExistingName(t *testing.T) {
	now := time.Date(2026, time.July, 12, 9, 30, 45, 123, time.FixedZone("CDT", -5*60*60))
	existing := map[string]bool{
		"report.txt":                              true,
		"report-20260712T143045.000000123Z.txt":   true,
		"report-20260712T143045.000000123Z-1.txt": true,
	}
	got, err := collisionName("report.txt", now, func(name string) (bool, error) {
		return existing[name], nil
	})
	require.NoError(t, err)
	require.Equal(t, "report-20260712T143045.000000123Z-2.txt", got)
	require.False(t, existing[got])
}

func TestDropUpload_CollisionAtActivationRetriesWithoutOverwriting(t *testing.T) {
	root, sc := newSFTPFixture(t)
	localPath := filepath.Join(t.TempDir(), "report.txt")
	require.NoError(t, os.WriteFile(localPath, []byte("new bytes"), 0o600))

	inbox := filepath.Join(root, DefaultInboxDir)
	var once sync.Once
	upload := newDropUpload(sc, root, func() time.Time {
		return time.Date(2026, time.July, 12, 14, 30, 45, 123, time.UTC)
	}, func(candidate string) {
		once.Do(func() {
			require.Equal(t, "report.txt", candidate)
			require.NoError(t, os.MkdirAll(inbox, 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(inbox, candidate), []byte("existing bytes"), 0o600))
		})
	})

	require.NoError(t, upload(localPath, "report.txt"))
	existing, err := os.ReadFile(filepath.Join(inbox, "report.txt"))
	require.NoError(t, err)
	require.Equal(t, "existing bytes", string(existing))

	entries, err := os.ReadDir(inbox)
	require.NoError(t, err)
	require.Len(t, entries, 2)
	var suffixed string
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "report-20260712T143045.000000123Z") {
			suffixed = entry.Name()
		}
	}
	require.NotEmpty(t, suffixed)
	got, err := os.ReadFile(filepath.Join(inbox, suffixed))
	require.NoError(t, err)
	require.Equal(t, "new bytes", string(got))
}

func TestDropWatcher_CloseWithoutFilesLeaksNoGoroutines(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())
	w, err := NewDropWatcher(t.TempDir(), nil, nil)
	require.NoError(t, err)
	require.NoError(t, w.Close())
}
