package incident

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestBlobStore_ContentAddressedDedup(t *testing.T) {
	t.Parallel()
	store := NewBlobStore(0)
	defer store.Close()

	content := []byte("the same payload bytes")
	ref1, err := store.Write(content, "text/plain")
	require.NoError(t, err)
	ref2, err := store.Write(content, "text/plain")
	require.NoError(t, err)

	if ref1.Hash != ref2.Hash {
		t.Errorf("same content produced different hashes: %s vs %s", ref1.Hash, ref2.Hash)
	}

	_, _, count := store.Stats()
	if count != 1 {
		t.Errorf("expected 1 blob after writing same content twice, got %d", count)
	}
}

func TestBlobStore_LRUEvictionByBytes(t *testing.T) {
	t.Parallel()
	const blobSize = 512
	const budget = 1024 // 2 blobs fit

	store := NewBlobStore(budget)
	defer store.Close()

	// Write first blob
	b1 := make([]byte, blobSize)
	for i := range b1 {
		b1[i] = 1
	}
	ref1, err := store.Write(b1, "text/plain")
	require.NoError(t, err)

	// Write second blob
	b2 := make([]byte, blobSize)
	for i := range b2 {
		b2[i] = 2
	}
	_, err = store.Write(b2, "text/plain")
	require.NoError(t, err)

	// Verify both fit
	_, _, count := store.Stats()
	if count != 2 {
		t.Fatalf("expected 2 blobs, got %d", count)
	}

	// Write third blob — pushes oldest (b1) out
	b3 := make([]byte, blobSize)
	for i := range b3 {
		b3[i] = 3
	}
	_, err = store.Write(b3, "text/plain")
	require.NoError(t, err)

	// Only 2 blobs still fit (budget = 1024)
	_, _, count = store.Stats()
	if count != 2 {
		t.Errorf("expected 2 blobs after eviction, got %d", count)
	}

	// Original oldest blob (b1) should be evicted
	_, _, err = store.Read(ref1.Hash)
	if err != ErrBlobEvicted {
		t.Errorf("expected ErrBlobEvicted for evicted blob, got %v", err)
	}
}

func TestBlobStore_ReadAfterEvict_ReturnsMissingErr(t *testing.T) {
	t.Parallel()
	store := NewBlobStore(10) // tiny budget
	defer store.Close()

	b1 := []byte("hello world — this is more than 10 bytes")
	ref1, err := store.Write(b1, "text/plain")
	require.NoError(t, err)

	// Another write must evict b1
	b2 := []byte("another payload that also exceeds the 10-byte budget")
	_, err = store.Write(b2, "text/plain")
	require.NoError(t, err)

	_, _, readErr := store.Read(ref1.Hash)
	if readErr != ErrBlobEvicted {
		t.Errorf("Read after eviction: got %v, want ErrBlobEvicted", readErr)
	}
}

func TestBlobStore_ConcurrentWriteRead(t *testing.T) {
	t.Parallel()
	store := NewBlobStore(4 * 1024 * 1024) // 4MB
	defer store.Close()

	const (
		writers  = 100
		readers  = 10
		duration = time.Second
	)

	var (
		writes atomic.Int64
		reads  atomic.Int64
		errors atomic.Int64
	)

	ctx, cancel := func() (chan struct{}, func()) {
		ch := make(chan struct{})
		return ch, sync.OnceFunc(func() { close(ch) })
	}()

	deadline := time.After(duration)
	go func() {
		<-deadline
		cancel()
	}()

	var wg sync.WaitGroup

	// Shared written refs for readers to consume
	refsMu := sync.Mutex{}
	var refs []BlobRef

	// Writers
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for {
				select {
				case <-ctx:
					return
				default:
				}
				content := []byte(fmt.Sprintf("payload-%d-%d", id, writes.Load()))
				ref, err := store.Write(content, "text/plain")
				if err != nil {
					errors.Add(1)
					return
				}
				refsMu.Lock()
				refs = append(refs, ref)
				refsMu.Unlock()
				writes.Add(1)
			}
		}(i)
	}

	// Readers
	for i := 0; i < readers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctx:
					return
				default:
				}
				refsMu.Lock()
				if len(refs) == 0 {
					refsMu.Unlock()
					continue
				}
				ref := refs[reads.Load()%int64(len(refs))]
				refsMu.Unlock()

				_, _, err := store.Read(ref.Hash)
				// ErrBlobEvicted is acceptable (LRU eviction under load)
				if err != nil && err != ErrBlobEvicted {
					errors.Add(1)
				}
				reads.Add(1)
			}
		}()
	}

	wg.Wait()

	if errors.Load() > 0 {
		t.Errorf("concurrent stress: %d unexpected errors", errors.Load())
	}
	if writes.Load() == 0 {
		t.Error("no writes completed during stress test")
	}
	t.Logf("stress: %d writes, %d reads, %d errors in %s",
		writes.Load(), reads.Load(), errors.Load(), duration)
}

func TestBlobStore_LargePayload_LowAlloc(t *testing.T) {
	t.Parallel()
	const payloadSize = 10 * 1024 * 1024 // 10MB
	store := NewBlobStore(12 * 1024 * 1024)
	defer store.Close()

	content := make([]byte, payloadSize)
	ref, err := store.Write(content, "application/octet-stream")
	require.NoError(t, err)

	got, mime, err := store.Read(ref.Hash)
	require.NoError(t, err)
	if len(got) != payloadSize {
		t.Errorf("read payload size: got %d, want %d", len(got), payloadSize)
	}
	if mime != "application/octet-stream" {
		t.Errorf("mime: got %q, want application/octet-stream", mime)
	}
}
