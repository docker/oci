// Package zstdr provides random-access reading of zstd-compressed blobs.
//
// The package mirrors gzipr but uses zstd frame boundaries (rather than
// DEFLATE block checkpoints) as resume points: each zstd frame is
// independently decompressible, so a frame index recording each frame's
// (compressed, decompressed) offset pair is sufficient to seek into the
// decompressed stream.
package zstdr

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"sync"

	"github.com/klauspost/compress/zstd"
)

// ErrInvalidFormat is returned when the compressed stream does not have
// the structure the index predicts (truncated frame, premature EOF, etc.).
var ErrInvalidFormat = errors.New("zstdr: invalid compressed format")

// ErrClosed is returned by [Reader.ReadAt] after [Reader.Close] has been
// called.
var ErrClosed = errors.New("zstdr: reader has been closed")

// FrameCheckpoint records the start of a non-skippable zstd frame.
//
// In is the byte offset within the compressed blob where the frame's
// magic bytes (0x28 0xB5 0x2F 0xFD) begin. Out is the byte offset within
// the concatenated decompressed stream where the frame's first
// decompressed byte will appear.
type FrameCheckpoint struct {
	In  int64 `json:"in"`
	Out int64 `json:"out"`
}

// Index is the persisted frame map for a zstd-compressed blob.
//
// Frames is sorted by ascending In (and therefore Out) and contains one
// entry per non-skippable frame. Skippable frames are not represented:
// the gap between consecutive Frames[i].In and Frames[i+1].In may be
// larger than the i-th frame's compressed length when a skippable frame
// follows. Size is the total decompressed length of the stream.
type Index struct {
	Frames []*FrameCheckpoint `json:"frames"`
	Size   int64              `json:"size"`
}

// Encode writes the JSON encoding of idx to w.
func (idx *Index) Encode(w io.Writer) error {
	return json.NewEncoder(w).Encode(idx)
}

// DecodeIndex reads a JSON-encoded Index from r.
func DecodeIndex(r io.Reader) (*Index, error) {
	var idx Index
	if err := json.NewDecoder(r).Decode(&idx); err != nil {
		return nil, err
	}
	return &idx, nil
}

// Option configures a Reader (or, for API symmetry with gzipr, a Scan
// call). Options are applied in order; later values override earlier
// ones for the same setting.
type Option func(*config)

type config struct {
	maxReaders int
}

func defaultConfig() config {
	return config{maxReaders: 8}
}

// WithMaxReaders bounds the number of concurrent ReadAt operations the
// returned Reader will service in parallel. Calls beyond the cap block
// until a reader is returned to the pool. n must be >= 1; values < 1
// are silently clamped to 1.
func WithMaxReaders(n int) Option {
	return func(c *config) {
		if n < 1 {
			n = 1
		}
		c.maxReaders = n
	}
}

// Reader serves io.ReaderAt over the decompressed bytes of a
// zstd-compressed blob, given an io.ReaderAt over the compressed blob
// and a frame Index.
type Reader struct {
	ra      io.ReaderAt
	idx     *Index
	cmpSize int64

	mu          sync.Mutex
	cond        *sync.Cond
	closed      bool
	inUse       int
	maxCap      int
	decoderPool sync.Pool // pool of *zstd.Decoder; keyed by Reset before use
}

// NewReader scans the compressed blob via ra to build a frame Index and
// returns a Reader. size is the COMPRESSED blob size in bytes.
func NewReader(ra io.ReaderAt, size int64, opts ...Option) (*Reader, error) {
	idx, err := Scan(io.NewSectionReader(ra, 0, size), io.Discard, opts...)
	if err != nil {
		return nil, err
	}
	return NewReaderWithIndex(ra, idx, size), nil
}

// NewReaderWithIndex constructs a Reader from a pre-built Index without
// any blob scanning. size is the COMPRESSED blob size; idx.Size is the
// DECOMPRESSED size (returned by Reader.Size).
//
// Precondition: idx.Size > 0 and idx.Frames != nil.
func NewReaderWithIndex(ra io.ReaderAt, idx *Index, size int64, opts ...Option) *Reader {
	if idx == nil {
		panic("zstdr: NewReaderWithIndex: idx is nil")
	}
	if idx.Size <= 0 {
		panic("zstdr: NewReaderWithIndex: idx.Size must be > 0")
	}
	if idx.Frames == nil {
		panic("zstdr: NewReaderWithIndex: idx.Frames must not be nil")
	}
	cfg := defaultConfig()
	for _, o := range opts {
		o(&cfg)
	}
	r := &Reader{
		ra:      ra,
		idx:     idx,
		cmpSize: size,
		maxCap:  cfg.maxReaders,
	}
	r.cond = sync.NewCond(&r.mu)
	r.decoderPool.New = func() any {
		dec, err := zstd.NewReader(bytes.NewReader(nil), zstd.WithDecoderConcurrency(1))
		if err != nil {
			panic("zstdr: failed to allocate decoder: " + err.Error())
		}
		return dec
	}
	return r
}

// Size returns the total decompressed length of the stream.
func (r *Reader) Size() int64 {
	return r.idx.Size
}

// Index returns the Reader's frame index. The returned pointer is the
// same value held by the Reader; callers must not mutate it.
func (r *Reader) Index() *Index {
	return r.idx
}

// Close drains the bounded reader pool. Subsequent ReadAt calls return
// ErrClosed; in-flight callers blocked on a pool slot also receive
// ErrClosed.
func (r *Reader) Close() error {
	r.mu.Lock()
	r.closed = true
	r.cond.Broadcast()
	for r.inUse > 0 {
		r.cond.Wait()
	}
	r.mu.Unlock()
	return nil
}

// acquire reserves a pool slot. Returns ErrClosed if the Reader has
// been closed (either before the call or while waiting for a slot).
func (r *Reader) acquire() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for {
		if r.closed {
			return ErrClosed
		}
		if r.inUse < r.maxCap {
			r.inUse++
			return nil
		}
		r.cond.Wait()
	}
}

// release returns a pool slot.
func (r *Reader) release() {
	r.mu.Lock()
	r.inUse--
	r.cond.Broadcast()
	r.mu.Unlock()
}

// ReadAt implements io.ReaderAt over the decompressed stream.
//
// Returns (0, nil) for len(p) == 0 and (0, io.EOF) for off >= r.Size().
// Otherwise returns (n, io.EOF) when n < len(p) (clamped read crossing
// end-of-stream) or (n, nil) when n == len(p) (full read, including a
// full read that ends exactly at end-of-stream). Format corruption
// surfaces as ErrInvalidFormat; underlying ReaderAt errors are returned
// as-is.
func (r *Reader) ReadAt(p []byte, off int64) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if off < 0 {
		return 0, fmt.Errorf("zstdr: negative offset %d", off)
	}
	if off >= r.idx.Size {
		return 0, io.EOF
	}
	if err := r.acquire(); err != nil {
		return 0, err
	}
	defer r.release()
	return r.readAtLocked(p, off)
}

// readAtLocked performs the seeking algorithm described in the design
// document. The caller must hold a pool slot.
func (r *Reader) readAtLocked(p []byte, off int64) (int, error) {
	target64 := int64(len(p))
	if remaining := r.idx.Size - off; target64 > remaining {
		target64 = remaining
	}
	target := int(target64)

	first := r.findFirst(off)
	lastReal, lastIdx := r.findLast(off + target64 - 1)

	// Determine the upper bound of the compressed range to fetch.
	var nextIn int64
	switch {
	case lastReal && lastIdx < len(r.idx.Frames)-1:
		nextIn = r.idx.Frames[lastIdx+1].In
	case lastReal && lastIdx == len(r.idx.Frames)-1:
		nextIn = r.cmpSize
	case !lastReal && len(r.idx.Frames) > 0:
		// Virtual start-of-stream as F_last AND real frames exist:
		// upper bound is the start of the first real frame.
		nextIn = r.idx.Frames[0].In
	default:
		// Virtual start-of-stream AND no real frames.
		nextIn = r.cmpSize
	}

	if first.In >= nextIn {
		return 0, ErrInvalidFormat
	}

	// Fetch the compressed span.
	cmpLen := nextIn - first.In
	if cmpLen <= 0 {
		return 0, ErrInvalidFormat
	}
	cmp := make([]byte, cmpLen)
	n, err := r.ra.ReadAt(cmp, first.In)
	if int64(n) < cmpLen {
		// Underlying ReadAt returned fewer bytes than requested. Use the
		// error it provided; synthesize one if it returned nil despite
		// the short read. io.EOF is valid for an exact-length ReadAt
		// (io.ReaderAt contract), so only treat it as an error here.
		if err == nil {
			err = io.ErrUnexpectedEOF
		}
		return 0, err
	}
	// n == cmpLen; swallow io.EOF (valid exact-length io.ReaderAt form).

	// Obtain a decoder from the pool and reset it to read from cmp.
	dec := r.decoderPool.Get().(*zstd.Decoder)
	if resetErr := dec.Reset(bytes.NewReader(cmp)); resetErr != nil {
		r.decoderPool.Put(dec)
		return 0, fmt.Errorf("zstdr: %w", resetErr)
	}
	defer r.decoderPool.Put(dec)

	// Discard phase: drop (off - first.Out) bytes from the decompressor.
	discardRemaining := off - first.Out
	if discardRemaining < 0 {
		// Index is corrupt.
		return 0, ErrInvalidFormat
	}
	if err := discardN(dec, discardRemaining); err != nil {
		return 0, err
	}

	// Read phase: pull `target` bytes into p, applying the design's
	// 4-rule loop (post-update accumulation).
	accumulated := 0
	for accumulated < target {
		nr, rerr := dec.Read(p[accumulated:target])
		accumulated += nr
		if accumulated >= target {
			// Rule 1: success regardless of rerr.
			break
		}
		switch rerr {
		case nil:
			// Rule 4: partial read, loop.
			continue
		case io.EOF, io.ErrUnexpectedEOF:
			// Rule 2: stream ended before target.
			return 0, ErrInvalidFormat
		default:
			// Rule 3: other non-nil error.
			return 0, rerr
		}
	}

	if target < len(p) {
		return target, io.EOF
	}
	return target, nil
}

// findFirst returns the highest-Out frame with Out <= off, or a virtual
// start-of-stream frame {In: 0, Out: 0} when no real frame qualifies.
func (r *Reader) findFirst(off int64) *FrameCheckpoint {
	frames := r.idx.Frames
	// Binary search for the largest i with frames[i].Out <= off.
	i := sort.Search(len(frames), func(i int) bool {
		return frames[i].Out > off
	}) - 1
	if i < 0 {
		return &FrameCheckpoint{In: 0, Out: 0}
	}
	return frames[i]
}

// findLast returns the index in r.idx.Frames of the highest-Out frame
// with Out <= bound, or (-1, false) when no real frame qualifies (the
// caller should treat this as the virtual start-of-stream frame).
func (r *Reader) findLast(bound int64) (bool, int) {
	frames := r.idx.Frames
	i := sort.Search(len(frames), func(i int) bool {
		return frames[i].Out > bound
	}) - 1
	if i < 0 {
		return false, -1
	}
	return true, i
}

// Compile-time interface checks.
var (
	_ io.ReaderAt = (*Reader)(nil)
	_ io.Closer   = (*Reader)(nil)
)

// discardN reads and discards n bytes from dec, applying the design's
// discard-phase rules: io.EOF or io.ErrUnexpectedEOF before n bytes
// have been consumed map to ErrInvalidFormat; other errors propagate.
func discardN(dec io.Reader, n int64) error {
	if n == 0 {
		return nil
	}
	const chunk = 32 * 1024
	buf := make([]byte, chunk)
	for n > 0 {
		want := int64(len(buf))
		if want > n {
			want = n
		}
		got, err := dec.Read(buf[:want])
		n -= int64(got)
		if n == 0 {
			// Done. A trailing io.EOF is fine here — the read phase
			// will surface ErrInvalidFormat if it cannot make further
			// progress.
			return nil
		}
		switch err {
		case nil:
			// Forward progress assumed (see design note); loop.
			continue
		case io.EOF, io.ErrUnexpectedEOF:
			return ErrInvalidFormat
		default:
			return err
		}
	}
	return nil
}
