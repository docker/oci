// Package gzipr provides sequential gzip scanning and random-access
// decompressed reading via DEFLATE block-boundary checkpoints.
//
// A checkpoint captures the full DEFLATE decompressor state (bit buffer
// plus 32 KB sliding-window history) at a block boundary, which allows
// resuming decompression at that exact point. Scan walks a gzip stream
// once, decompressing to an [io.Writer] and emitting checkpoints at a
// configurable decompressed-byte interval. The resulting [Index] is
// then used by [NewReaderWithIndex] to back an [io.ReaderAt] whose
// ReadAt fetches only the compressed span needed for the requested
// decompressed range.
package gzipr

import (
	"errors"
)

// ErrInvalidFormat is returned by [Scan], [NewReader], and
// [Reader.ReadAt] when the underlying gzip or DEFLATE stream cannot be
// parsed.
var ErrInvalidFormat = errors.New("gzipr: invalid compressed format")

// ErrClosed is returned by [Reader.ReadAt] after [Reader.Close] has
// been called.
var ErrClosed = errors.New("gzipr: reader has been closed")

// Checkpoint records the DEFLATE decompressor state at a block boundary.
//
// In is the compressed-byte offset to seek to for resume. Out is the
// total decompressed bytes produced before the captured boundary.
// B and NB hold the bit buffer (up to 32 bits, NB ≤ 32). Hist contains
// the in-order sliding-window history (at most 32 KB) that primes the
// LZ77 dictionary on resume.
type Checkpoint struct {
	In   int64  `json:"in"`
	Out  int64  `json:"out"`
	B    uint32 `json:"b"`
	NB   uint   `json:"nb"`
	Hist []byte `json:"hist"`
}

// Index holds the checkpoint sequence for one gzip stream plus the
// total decompressed size.
//
// Checkpoints is sorted by Out ascending. It may be empty (non-nil
// zero-length slice) when the stream decompresses to fewer bytes than
// the configured checkpoint span. Size is the total decompressed
// length of the stream.
type Index struct {
	Checkpoints []*Checkpoint `json:"checkpoints"`
	Size        int64         `json:"size"`
}

// Option configures [Scan], [NewReader], and [NewReaderWithIndex].
type Option func(*config)

type config struct {
	span       int64
	maxReaders int
}

func defaultConfig() *config {
	return &config{
		span:       1 << 20,
		maxReaders: 8,
	}
}

func applyOpts(opts []Option) *config {
	c := defaultConfig()
	for _, o := range opts {
		o(c)
	}
	if c.span <= 0 {
		c.span = 1 << 20
	}
	if c.maxReaders <= 0 {
		c.maxReaders = 8
	}
	return c
}

// WithSpan sets the minimum decompressed-byte interval between
// checkpoints emitted by [Scan] and [NewReader]. The default is 1 MiB.
// Smaller spans yield finer-grained random access at the cost of
// proportionally more checkpoint memory (~32 KiB per checkpoint).
func WithSpan(decompressedBytes int64) Option {
	return func(c *config) { c.span = decompressedBytes }
}

// WithMaxReaders sets the maximum number of concurrent decompression
// streams a [Reader] will run for [Reader.ReadAt]. Calls beyond the cap
// block until a slot is returned. The default is 8.
func WithMaxReaders(n int) Option {
	return func(c *config) { c.maxReaders = n }
}
