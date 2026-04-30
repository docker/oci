package gzipr

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"

	"github.com/docker/oci/ocifs/gzipr/internal/flate"
)

// Reader provides random-access reads over a gzip stream backed by an
// [io.ReaderAt] and a previously-built [Index]. ReadAt is safe for
// concurrent use; the number of in-flight decompressions is capped via
// [WithMaxReaders].
type Reader struct {
	ra   io.ReaderAt
	idx  *Index
	size int64 // compressed blob size

	pool   chan struct{} // bounded slot pool; receive = acquire, send = release
	done   chan struct{} // closed by Close to unblock waiting acquires
	closed atomic.Bool

	closeOnce sync.Once
}

// NewReader scans the entire gzip blob at ra (using sequential
// random-access reads) to build a checkpoint [Index], then returns a
// [Reader] backed by the same ra. size is the compressed blob size.
func NewReader(ra io.ReaderAt, size int64, opts ...Option) (*Reader, error) {
	idx, err := Scan(io.NewSectionReader(ra, 0, size), io.Discard, opts...)
	if err != nil {
		return nil, err
	}
	return newReaderFromIndex(ra, idx, size, opts...), nil
}

// NewReaderWithIndex returns a [Reader] backed by ra and the
// pre-computed [Index] idx. size is the compressed blob size; idx.Size
// is the decompressed size and is what [Reader.Size] returns. No I/O
// is performed.
//
// Panics if idx is nil, idx.Size <= 0, or idx.Checkpoints is nil.
// Callers who construct an Index from a persisted source (rather than
// from [Scan] or [NewReader]) should validate it before invoking this
// constructor.
func NewReaderWithIndex(ra io.ReaderAt, idx *Index, size int64, opts ...Option) *Reader {
	if idx == nil {
		panic("gzipr: NewReaderWithIndex: idx is nil")
	}
	if idx.Size <= 0 {
		panic("gzipr: NewReaderWithIndex: idx.Size must be > 0")
	}
	if idx.Checkpoints == nil {
		panic("gzipr: NewReaderWithIndex: idx.Checkpoints must not be nil")
	}
	return newReaderFromIndex(ra, idx, size, opts...)
}

func newReaderFromIndex(ra io.ReaderAt, idx *Index, size int64, opts ...Option) *Reader {
	cfg := applyOpts(opts)
	r := &Reader{
		ra:   ra,
		idx:  idx,
		size: size,
		pool: make(chan struct{}, cfg.maxReaders),
		done: make(chan struct{}),
	}
	for i := 0; i < cfg.maxReaders; i++ {
		r.pool <- struct{}{}
	}
	return r
}

// Size returns the total decompressed size of the gzip stream.
func (r *Reader) Size() int64 {
	if r.idx == nil {
		return 0
	}
	return r.idx.Size
}

// Index returns the underlying [Index]. Callers must not mutate it.
func (r *Reader) Index() *Index {
	return r.idx
}

// Close releases the bounded reader pool. After Close, [Reader.ReadAt]
// returns [ErrClosed]; callers blocked acquiring a pool slot also
// receive [ErrClosed]. Close is idempotent.
func (r *Reader) Close() error {
	r.closeOnce.Do(func() {
		r.closed.Store(true)
		close(r.done)
	})
	return nil
}

// acquire blocks until a pool slot is available or the reader is
// closed. It returns ErrClosed if the reader was closed before a slot
// was obtained.
func (r *Reader) acquire() error {
	if r.closed.Load() {
		return ErrClosed
	}
	select {
	case <-r.pool:
		if r.closed.Load() {
			r.release()
			return ErrClosed
		}
		return nil
	case <-r.done:
		return ErrClosed
	}
}

func (r *Reader) release() {
	select {
	case r.pool <- struct{}{}:
	default:
		panic("gzipr: pool over-release: release called without a matching acquire")
	}
}

// findFirst returns the index of the highest-Out checkpoint with
// Out <= off, or -1 if none exists.
func (r *Reader) findFirst(off int64) int {
	cps := r.idx.Checkpoints
	lo, hi := 0, len(cps)
	for lo < hi {
		mid := (lo + hi) / 2
		if cps[mid].Out <= off {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	return lo - 1
}

// ReadAt implements [io.ReaderAt]. See the package design notes for the
// full algorithm.
func (r *Reader) ReadAt(p []byte, off int64) (int, error) {
	if r.closed.Load() {
		return 0, ErrClosed
	}
	if len(p) == 0 {
		return 0, nil
	}
	if off < 0 {
		return 0, fmt.Errorf("gzipr: negative offset %d", off)
	}
	size := r.Size()
	if off >= size {
		return 0, io.EOF
	}

	target := int64(len(p))
	if remaining := size - off; target > remaining {
		target = remaining
	}

	if err := r.acquire(); err != nil {
		return 0, err
	}
	defer r.release()

	cps := r.idx.Checkpoints
	firstIdx := r.findFirst(off)
	lastIdx := r.findFirst(off + target - 1)

	var (
		firstIn   int64
		firstOut  int64
		firstB    uint32
		firstNB   uint
		firstHist []byte
	)
	if firstIdx >= 0 {
		c := cps[firstIdx]
		firstIn = c.In
		firstOut = c.Out
		firstB = c.B
		firstNB = c.NB
		firstHist = c.Hist
	}

	var lastNextIn int64
	switch {
	case lastIdx >= 0 && lastIdx+1 < len(cps):
		lastNextIn = cps[lastIdx+1].In
	case lastIdx >= 0 && lastIdx+1 == len(cps):
		lastNextIn = r.size
	case lastIdx < 0 && len(cps) > 0:
		lastNextIn = cps[0].In
	default:
		lastNextIn = r.size
	}

	if firstIn >= lastNextIn {
		return 0, ErrInvalidFormat
	}

	compressed := make([]byte, lastNextIn-firstIn)
	n, err := r.ra.ReadAt(compressed, firstIn)
	if n < len(compressed) {
		// Underlying ReadAt returned fewer bytes than requested. Use the
		// error it provided; if it returned nil despite the short read,
		// synthesize one. io.EOF is a valid io.ReaderAt return for an
		// exact-length read (contract §ReadAt), so we only treat it as
		// an error when the read is short.
		if err == nil {
			err = io.ErrUnexpectedEOF
		}
		return 0, err
	}
	// n == len(compressed); io.EOF here is the valid "exact-length" form
	// of the io.ReaderAt contract — swallow it.

	// If we're starting from the virtual start-of-stream, we must skip
	// the gzip header before handing bytes to the DEFLATE decompressor.
	br := bufio.NewReader(bytes.NewReader(compressed))
	if firstIdx < 0 {
		if _, err := readGzipHeader(br); err != nil {
			return 0, err
		}
	}

	zr := flate.NewReaderResume(br, firstHist, firstB, firstNB, nil)
	defer zr.Close()

	discard := off - firstOut
	if discard < 0 {
		return 0, fmt.Errorf("%w: negative discard %d", ErrInvalidFormat, discard)
	}
	if err := discardBytes(zr, discard); err != nil {
		return 0, err
	}

	accumulated := int64(0)
	dst := p[:target]
	for accumulated < target {
		n, err := zr.Read(dst[accumulated:])
		accumulated += int64(n)
		if accumulated >= target {
			break
		}
		switch {
		case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
			return 0, ErrInvalidFormat
		case err != nil:
			return 0, err
		}
	}

	if target < int64(len(p)) {
		return int(target), io.EOF
	}
	return int(target), nil
}

// Compile-time interface checks.
var (
	_ io.ReaderAt = (*Reader)(nil)
	_ io.Closer   = (*Reader)(nil)
)

// discardBytes advances zr past n bytes, applying the discard-phase
// rules from the package design.
func discardBytes(zr io.Reader, n int64) error {
	if n == 0 {
		return nil
	}
	buf := make([]byte, 32*1024)
	remaining := n
	for remaining > 0 {
		want := int64(len(buf))
		if want > remaining {
			want = remaining
		}
		k, err := zr.Read(buf[:want])
		remaining -= int64(k)
		if remaining == 0 {
			return nil
		}
		switch {
		case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
			return ErrInvalidFormat
		case err != nil:
			return err
		}
	}
	return nil
}
