package incident

import (
	"container/list"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sync"
)

// ErrBlobEvicted is returned by Read when the requested blob was evicted.
var ErrBlobEvicted = errors.New("blob evicted from store")

const (
	defaultBudgetBytes = 16 * 1024 * 1024 // 16MB
	writeQueueCap      = 256
)

type blobEntry struct {
	ref     BlobRef
	content []byte
	elem    *list.Element // position in lruList
}

type writeReq struct {
	content []byte
	mime    string
	result  chan writeResult
}

type writeResult struct {
	ref BlobRef
	err error
}

// BlobStore is a bytes-bounded content-addressed in-memory store with LRU
// eviction. Write computes the sha256 hash, deduplicates identical payloads,
// and evicts the least-recently-used blob when the budget is exceeded.
//
// Writes are dispatched via a bounded channel (256 entries) to a background
// goroutine so callers never block on lock contention. Close drains the queue
// and stops the goroutine.
type BlobStore struct {
	maxBytes  int64
	mu        sync.Mutex
	entries   map[string]*blobEntry
	lruList   *list.List
	usedBytes int64
	writeCh   chan writeReq
	done      chan struct{}
	wg        sync.WaitGroup
}

// NewBlobStore creates a BlobStore limited to maxBytes of payload storage.
// Pass 0 for the default (16MB).
func NewBlobStore(maxBytes int64) *BlobStore {
	if maxBytes <= 0 {
		maxBytes = defaultBudgetBytes
	}
	bs := &BlobStore{
		maxBytes: maxBytes,
		entries:  make(map[string]*blobEntry),
		lruList:  list.New(),
		writeCh:  make(chan writeReq, writeQueueCap),
		done:     make(chan struct{}),
	}
	bs.wg.Add(1)
	go bs.drain()
	return bs
}

// Write stores content and returns a BlobRef. Identical content (same sha256)
// is stored only once. If the store is over budget the oldest entry is evicted.
// Write never blocks the caller: it is enqueued to a background goroutine.
// The returned channel receives the result when the write completes.
func (bs *BlobStore) Write(content []byte, mime string) (BlobRef, error) {
	req := writeReq{
		content: content,
		mime:    mime,
		result:  make(chan writeResult, 1),
	}
	select {
	case bs.writeCh <- req:
	case <-bs.done:
		return BlobRef{}, errors.New("blob store closed")
	}
	res := <-req.result
	return res.ref, res.err
}

// Read retrieves the payload for hash. Returns ErrBlobEvicted if the blob was
// evicted since writing.
func (bs *BlobStore) Read(hash string) ([]byte, string, error) {
	bs.mu.Lock()
	defer bs.mu.Unlock()
	entry, ok := bs.entries[hash]
	if !ok {
		return nil, "", ErrBlobEvicted
	}
	// Touch: move to front (most recently used)
	bs.lruList.MoveToFront(entry.elem)
	return entry.content, entry.ref.MIME, nil
}

// Close stops the background goroutine after draining pending writes.
func (bs *BlobStore) Close() {
	close(bs.done)
	bs.wg.Wait()
}

// Stats returns current usage for observability.
func (bs *BlobStore) Stats() (used, max int64, count int) {
	bs.mu.Lock()
	defer bs.mu.Unlock()
	return bs.usedBytes, bs.maxBytes, len(bs.entries)
}

func (bs *BlobStore) drain() {
	defer bs.wg.Done()
	for {
		select {
		case req := <-bs.writeCh:
			ref, err := bs.writeSync(req.content, req.mime)
			req.result <- writeResult{ref: ref, err: err}
		case <-bs.done:
			// Drain remaining writes before exit
			for {
				select {
				case req := <-bs.writeCh:
					ref, err := bs.writeSync(req.content, req.mime)
					req.result <- writeResult{ref: ref, err: err}
				default:
					return
				}
			}
		}
	}
}

func (bs *BlobStore) writeSync(content []byte, mime string) (BlobRef, error) {
	sum := sha256.Sum256(content)
	hash := hex.EncodeToString(sum[:])
	ref := BlobRef{Hash: hash, Size: len(content), MIME: mime}

	bs.mu.Lock()
	defer bs.mu.Unlock()

	if existing, ok := bs.entries[hash]; ok {
		// Dedup: touch LRU position
		bs.lruList.MoveToFront(existing.elem)
		return ref, nil
	}

	entry := &blobEntry{ref: ref, content: content}
	entry.elem = bs.lruList.PushFront(entry)
	bs.entries[hash] = entry
	bs.usedBytes += int64(len(content))

	// Evict least-recently-used until under budget
	for bs.usedBytes > bs.maxBytes {
		back := bs.lruList.Back()
		if back == nil {
			break
		}
		evict := back.Value.(*blobEntry)
		bs.lruList.Remove(back)
		delete(bs.entries, evict.ref.Hash)
		bs.usedBytes -= int64(len(evict.content))
	}

	return ref, nil
}
