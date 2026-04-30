package gzipr

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"github.com/docker/oci/ocifs/gzipr/internal/flate"
)

// gzipMagic is the two-byte gzip member-start signature (RFC 1952 §2.3.1).
var gzipMagic = [2]byte{0x1f, 0x8b}

const (
	flagHCRC    = 1 << 1
	flagExtra   = 1 << 2
	flagName    = 1 << 3
	flagComment = 1 << 4
)

// readGzipHeader consumes a single gzip member header from br and
// returns the number of bytes consumed. The reader is positioned at
// the first DEFLATE byte on success.
func readGzipHeader(br *bufio.Reader) (int64, error) {
	var fixed [10]byte
	if _, err := io.ReadFull(br, fixed[:]); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return 0, fmt.Errorf("%w: short gzip header", ErrInvalidFormat)
		}
		return 0, err
	}
	if fixed[0] != gzipMagic[0] || fixed[1] != gzipMagic[1] {
		return 0, fmt.Errorf("%w: bad gzip magic", ErrInvalidFormat)
	}
	if fixed[2] != 8 {
		return 0, fmt.Errorf("%w: unknown gzip method %d", ErrInvalidFormat, fixed[2])
	}
	flag := fixed[3]
	consumed := int64(10)

	if flag&flagExtra != 0 {
		var x [2]byte
		if _, err := io.ReadFull(br, x[:]); err != nil {
			return consumed, fmt.Errorf("%w: truncated FEXTRA", ErrInvalidFormat)
		}
		consumed += 2
		xlen := int(binary.LittleEndian.Uint16(x[:]))
		if _, err := io.CopyN(io.Discard, br, int64(xlen)); err != nil {
			return consumed, fmt.Errorf("%w: truncated FEXTRA payload", ErrInvalidFormat)
		}
		consumed += int64(xlen)
	}
	if flag&flagName != 0 {
		n, err := discardNullTerminated(br)
		consumed += n
		if err != nil {
			return consumed, fmt.Errorf("%w: truncated FNAME", ErrInvalidFormat)
		}
	}
	if flag&flagComment != 0 {
		n, err := discardNullTerminated(br)
		consumed += n
		if err != nil {
			return consumed, fmt.Errorf("%w: truncated FCOMMENT", ErrInvalidFormat)
		}
	}
	if flag&flagHCRC != 0 {
		var c [2]byte
		if _, err := io.ReadFull(br, c[:]); err != nil {
			return consumed, fmt.Errorf("%w: truncated FHCRC", ErrInvalidFormat)
		}
		consumed += 2
	}
	return consumed, nil
}

// maxNullTerminatedLen caps the length of null-terminated FNAME/FCOMMENT
// fields to prevent a crafted blob from looping indefinitely.
const maxNullTerminatedLen = 65536

func discardNullTerminated(br *bufio.Reader) (int64, error) {
	var n int64
	for {
		if n >= maxNullTerminatedLen {
			return n, fmt.Errorf("%w: FNAME/FCOMMENT field exceeds %d bytes", ErrInvalidFormat, maxNullTerminatedLen)
		}
		b, err := br.ReadByte()
		if err != nil {
			return n, err
		}
		n++
		if b == 0 {
			return n, nil
		}
	}
}

// Scan performs a single sequential pass over the gzip stream r,
// writing the decompressed bytes to out and recording a [Checkpoint]
// at every DEFLATE block boundary whose decompressed offset is at
// least one configured span past the previous checkpoint.
//
// Write errors from out are returned unwrapped so callers can
// distinguish them (e.g. [io.ErrClosedPipe]) from format errors.
// Format errors wrap [ErrInvalidFormat].
//
// When the stream decompresses to fewer bytes than one span, the
// returned Index has a non-nil empty Checkpoints slice.
func Scan(r io.Reader, out io.Writer, opts ...Option) (*Index, error) {
	cfg := applyOpts(opts)

	br := bufio.NewReader(r)

	headerLen, err := readGzipHeader(br)
	if err != nil {
		return nil, err
	}
	// Compressed offset of the first DEFLATE byte. cr counts every byte
	// consumed from r; bufio.Reader may have buffered ahead, so we use
	// the explicit headerLen rather than cr.n here.
	deflateStart := headerLen

	idx := &Index{Checkpoints: []*Checkpoint{}}

	var (
		writeErr   error
		emittedOut int64 = -1 // ensures first eligible boundary qualifies
	)
	checkpoint := func(in, outOff int64, b uint32, nb uint, hist []byte) {
		if writeErr != nil {
			return
		}
		// Skip the start-of-stream boundary: ReadAt handles the
		// [0, firstCheckpoint.Out) range implicitly, so emitting a
		// checkpoint here would be redundant.
		if outOff == 0 && in == 0 {
			emittedOut = 0
			return
		}
		if outOff-emittedOut < cfg.span {
			return
		}
		histCopy := make([]byte, len(hist))
		copy(histCopy, hist)
		idx.Checkpoints = append(idx.Checkpoints, &Checkpoint{
			In:   in + deflateStart,
			Out:  outOff,
			B:    b,
			NB:   nb,
			Hist: histCopy,
		})
		emittedOut = outOff
	}

	zr := flate.NewReaderCallback(br, nil, checkpoint)
	defer zr.Close()

	buf := make([]byte, 32*1024)
	var total int64
	for {
		n, rerr := zr.Read(buf)
		if n > 0 {
			if _, werr := out.Write(buf[:n]); werr != nil {
				writeErr = werr
				return nil, werr
			}
			total += int64(n)
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return nil, fmt.Errorf("%w: %v", ErrInvalidFormat, rerr)
		}
	}

	// We intentionally do not validate the gzip trailer (CRC32 + ISIZE).
	// The DEFLATE stream may end mid-byte, leaving leftover bits in the
	// flate reader's buffer; the underlying byte stream has already been
	// advanced past the last fully-consumed DEFLATE byte. Trailer
	// validation would require careful re-alignment that is not needed
	// for our random-access use case.
	idx.Size = total
	return idx, nil
}
