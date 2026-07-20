package incident

import (
	"container/list"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sync"
	"sync/atomic"
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

// asyncWriteReq is used by WriteAsync. The hash is pre-computed by the caller
// so the drain goroutine only needs to store the bytes.
type asyncWriteReq struct {
	hash    string
	content []byte
	mime    string
}

type blobDrainGate struct {
	release     chan struct{}
	parked      chan struct{}
	writeQueued chan struct{}
	parkOnce    sync.Once
	writeOnce   sync.Once
}

// BlobStore is a bytes-bounded content-addressed in-memory store with LRU
// eviction. Write computes the sha256 hash, deduplicates identical payloads,
// and evicts the least-recently-used blob when the budget is exceeded.
//
// Writes are dispatched via a bounded channel (256 entries) to a background
// goroutine so callers never block on lock contention. Close drains the queue
// and stops the goroutine.
//
// WriteAsync returns a BlobRef immediately (hash computed synchronously) and
// defers the actual storage. If the async queue is full the content may never
// land — readers fall back to IncidentEvent.Summary in that case.
type BlobStore struct {
	maxBytes  int64
	mu        sync.Mutex
	entries   map[string]*blobEntry
	lruList   *list.List
	usedBytes int64
	writeCh   chan writeReq
	asyncCh   chan asyncWriteReq // for WriteAsync; separate so sync writes can't starve
	done      chan struct{}
	closeOnce sync.Once
	wg        sync.WaitGroup

	// drainGate/gateWake are test-only. pauseDrain wakes the worker and waits for
	// an acknowledgement that it is parked before returning.
	drainGate atomic.Pointer[blobDrainGate]
	gateWake  chan struct{}
}

// NewBlobStore creates a BlobStore limited to maxBytes of payload storage.
// Pass 0 for the default (16MB).
func NewBlobStore(maxBytes int64) *BlobStore {
	return newBlobStoreInternal(maxBytes, writeQueueCap)
}

// newBlobStoreWithQueueCap creates a BlobStore with a custom async queue
// capacity. Used by tests to exercise overflow paths.
func newBlobStoreWithQueueCap(asyncCap int) *BlobStore {
	return newBlobStoreInternal(defaultBudgetBytes, asyncCap)
}

func newBlobStoreInternal(maxBytes int64, asyncCap int) *BlobStore {
	if maxBytes <= 0 {
		maxBytes = defaultBudgetBytes
	}
	bs := &BlobStore{
		maxBytes: maxBytes,
		entries:  make(map[string]*blobEntry),
		lruList:  list.New(),
		writeCh:  make(chan writeReq, writeQueueCap),
		asyncCh:  make(chan asyncWriteReq, asyncCap),
		done:     make(chan struct{}),
		gateWake: make(chan struct{}, 1),
	}
	bs.wg.Add(1)
	go bs.drain()
	return bs
}

// Write stores content and returns a BlobRef. Identical content (same sha256)
// is stored only once. If the store is over budget the oldest entry is evicted.
// The write is enqueued to a background goroutine so the caller never contends
// on the store lock; Write then blocks on the result channel until the
// background goroutine reports completion (or the store is closed).
func (bs *BlobStore) Write(content []byte, mime string) (BlobRef, error) {
	req := writeReq{
		content: content,
		mime:    mime,
		result:  make(chan writeResult, 1),
	}
	select {
	case bs.writeCh <- req:
		if gate := bs.drainGate.Load(); gate != nil {
			gate.writeOnce.Do(func() { close(gate.writeQueued) })
		}
	case <-bs.done:
		return BlobRef{}, errors.New("blob store closed")
	}
	res := <-req.result
	return res.ref, res.err
}

// WriteAsync computes the sha256 hash synchronously and returns a BlobRef
// immediately, then enqueues the actual content write to a background goroutine.
// If the async queue is full the ref is still returned but the content may
// never land — readers should fall back to IncidentEvent.Summary in that case.
func (bs *BlobStore) WriteAsync(content []byte, mime string) BlobRef {
	sum := sha256.Sum256(content)
	hash := hex.EncodeToString(sum[:])
	ref := BlobRef{Hash: hash, Size: len(content), MIME: mime}

	req := asyncWriteReq{hash: hash, content: content, mime: mime}
	select {
	case bs.asyncCh <- req:
	default:
		// Queue full: ref returned without content being stored.
	}
	return ref
}

// pauseDrain blocks the drain goroutine from processing writes. Used only by
// tests to control timing of WriteAsync. Must be paired with resumeDrain.
func (bs *BlobStore) pauseDrain() {
	gate := &blobDrainGate{
		release:     make(chan struct{}),
		parked:      make(chan struct{}),
		writeQueued: make(chan struct{}),
	}
	bs.drainGate.Store(gate)
	select {
	case bs.gateWake <- struct{}{}:
	default:
	}
	select {
	case <-gate.parked:
	case <-bs.done:
	}
}

// pausedWriteQueued reports when a synchronous Write has enqueued its request
// behind the acknowledged drain gate. Used only by tests.
func (bs *BlobStore) pausedWriteQueued() <-chan struct{} {
	gate := bs.drainGate.Load()
	if gate == nil {
		closed := make(chan struct{})
		close(closed)
		return closed
	}
	return gate.writeQueued
}

// resumeDrain unblocks the drain goroutine. Paired with pauseDrain.
func (bs *BlobStore) resumeDrain() {
	ptr := bs.drainGate.Swap(nil)
	if ptr != nil {
		close(ptr.release)
	}
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
// Idempotent: a second Close is a no-op (a bare close(bs.done) would panic).
func (bs *BlobStore) Close() {
	bs.closeOnce.Do(func() {
		close(bs.done)
		bs.wg.Wait()
	})
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
		// Honor the test-only drain gate.
		if ptr := bs.drainGate.Load(); ptr != nil {
			ptr.parkOnce.Do(func() { close(ptr.parked) })
			select {
			case <-ptr.release:
			case <-bs.done:
				bs.drainRemaining()
				return
			}
		}

		select {
		case req := <-bs.writeCh:
			ref, err := bs.writeSync(req.content, req.mime)
			req.result <- writeResult{ref: ref, err: err}
		case req := <-bs.asyncCh:
			bs.writeAsyncSync(req)
		case <-bs.gateWake:
			continue
		case <-bs.done:
			bs.drainRemaining()
			return
		}
	}
}

func (bs *BlobStore) drainRemaining() {
	for {
		select {
		case req := <-bs.writeCh:
			ref, err := bs.writeSync(req.content, req.mime)
			req.result <- writeResult{ref: ref, err: err}
		case req := <-bs.asyncCh:
			bs.writeAsyncSync(req)
		default:
			return
		}
	}
}

// writeAsyncSync stores a pre-hashed async write request. Deduplicates by hash.
func (bs *BlobStore) writeAsyncSync(req asyncWriteReq) {
	bs.mu.Lock()
	defer bs.mu.Unlock()

	if existing, ok := bs.entries[req.hash]; ok {
		bs.lruList.MoveToFront(existing.elem)
		return
	}

	ref := BlobRef{Hash: req.hash, Size: len(req.content), MIME: req.mime}
	entry := &blobEntry{ref: ref, content: req.content}
	entry.elem = bs.lruList.PushFront(entry)
	bs.entries[req.hash] = entry
	bs.usedBytes += int64(len(req.content))

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
