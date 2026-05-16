package zstdr

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"

	"github.com/klauspost/compress/zstd"
)

const (
	// zstdFrameMagic is the little-endian uint32 form of the zstd
	// standard frame magic (0x28 0xB5 0x2F 0xFD).
	zstdFrameMagic = 0xFD2FB528

	// skippableMagicMask and skippableMagicBase together identify
	// skippable frames: (v & mask) == base.
	skippableMagicMask = 0xFFFFFFF0
	skippableMagicBase = 0x184D2A50
)

// Scan performs a single sequential pass over the zstd stream r,
// writing all decompressed bytes to out and recording one
// FrameCheckpoint per non-skippable frame. Skippable frames are
// passed over (their bytes are read from r but not written to out and
// no checkpoint is emitted).
//
// opts are accepted for API symmetry with gzipr.Scan; no current
// option affects scanning behaviour.
//
// When the stream contains no non-skippable frames, the returned
// Index has a non-nil empty Frames slice (rather than nil), so JSON
// round-trip produces "frames":[] not "frames":null.
func Scan(r io.Reader, out io.Writer, opts ...Option) (*Index, error) {
	// opts is accepted for API symmetry with gzipr; no option currently
	// affects scanning behaviour.

	idx := &Index{
		Frames: []*FrameCheckpoint{},
	}

	// Reuse a single Decoder across frames.
	dec, err := zstd.NewReader(nil, zstd.WithDecoderConcurrency(1))
	if err != nil {
		return nil, fmt.Errorf("zstdr: %w", err)
	}
	defer dec.Close()

	// We read frames one at a time. For each frame we accumulate the
	// raw frame bytes into frameBuf and then DecodeAll them. This
	// keeps the implementation independent of the decoder's
	// multi-frame stream handling.
	var (
		inOff     int64
		outOff    int64
		frameBuf  bytes.Buffer
		decompBuf []byte
	)

	for {
		// Peek at the first 4 bytes to identify the frame type.
		var magicBuf [4]byte
		_, err := io.ReadFull(r, magicBuf[:])
		if err == io.EOF {
			// Clean end of stream at a frame boundary.
			idx.Size = outOff
			return idx, nil
		}
		if err == io.ErrUnexpectedEOF {
			return nil, ErrInvalidFormat
		}
		if err != nil {
			return nil, err
		}

		magic := binary.LittleEndian.Uint32(magicBuf[:])

		switch {
		case (magic & skippableMagicMask) == skippableMagicBase:
			// Skippable frame: 4-byte magic + 4-byte size + size bytes.
			var sizeBuf [4]byte
			if _, err := io.ReadFull(r, sizeBuf[:]); err != nil {
				return nil, mapTrunc(err)
			}
			size := int64(binary.LittleEndian.Uint32(sizeBuf[:]))
			if size > 0 {
				if _, err := io.CopyN(io.Discard, r, size); err != nil {
					return nil, mapTrunc(err)
				}
			}
			inOff += 8 + size

		case magic == zstdFrameMagic:
			// Standard frame: record checkpoint, then read the
			// entire frame body using a frame walker.
			idx.Frames = append(idx.Frames, &FrameCheckpoint{
				In:  inOff,
				Out: outOff,
			})

			frameBuf.Reset()
			frameBuf.Write(magicBuf[:])
			frameLen, err := readFrameBody(r, &frameBuf)
			if err != nil {
				return nil, err
			}

			// Decompress and stream out.
			decompBuf, err = dec.DecodeAll(frameBuf.Bytes(), decompBuf[:0])
			if err != nil {
				return nil, fmt.Errorf("%w: %v", ErrInvalidFormat, err)
			}
			if _, err := out.Write(decompBuf); err != nil {
				return nil, err
			}

			inOff += 4 + frameLen
			outOff += int64(len(decompBuf))

		default:
			return nil, fmt.Errorf("%w: unrecognised frame magic 0x%08x at offset %d", ErrInvalidFormat, magic, inOff)
		}
	}
}

// readFrameBody reads the bytes of a single zstd standard frame
// following the 4-byte magic that has already been consumed by the
// caller. The bytes are appended to dst. Returns the number of bytes
// appended (i.e. the size of the frame excluding its 4-byte magic).
//
// The walker parses the frame header (variable length per RFC 8878
// §3.1.1.1) followed by a sequence of Block_Header (3 bytes) +
// block content tuples, terminating after the block whose Last_Block
// flag is set. If the frame's Content_Checksum_Flag is set, a 4-byte
// trailing checksum is also consumed.
func readFrameBody(r io.Reader, dst *bytes.Buffer) (int64, error) {
	startLen := dst.Len()

	// Frame_Header_Descriptor (1 byte).
	var fhd [1]byte
	if _, err := io.ReadFull(r, fhd[:]); err != nil {
		return 0, mapTrunc(err)
	}
	dst.Write(fhd[:])

	dictIDFlag := fhd[0] & 0x03
	checksumFlag := (fhd[0] >> 2) & 0x01
	reservedBit := (fhd[0] >> 3) & 0x01
	singleSegment := (fhd[0]>>5)&0x01 != 0
	fcsFlag := fhd[0] >> 6

	if reservedBit != 0 {
		return 0, fmt.Errorf("%w: reserved bit set in frame header", ErrInvalidFormat)
	}

	// Window_Descriptor (1 byte) — present iff Single_Segment_Flag is 0.
	if !singleSegment {
		var wd [1]byte
		if _, err := io.ReadFull(r, wd[:]); err != nil {
			return 0, mapTrunc(err)
		}
		dst.Write(wd[:])
	}

	// Dictionary_ID — 0/1/2/4 bytes per dictIDFlag.
	dictIDSize := dictIDFieldSize(dictIDFlag)
	if dictIDSize > 0 {
		buf := make([]byte, dictIDSize)
		if _, err := io.ReadFull(r, buf); err != nil {
			return 0, mapTrunc(err)
		}
		dst.Write(buf)
	}

	// Frame_Content_Size — variable per fcsFlag and singleSegment.
	fcsSize := frameContentSizeFieldSize(fcsFlag, singleSegment)
	if fcsSize > 0 {
		buf := make([]byte, fcsSize)
		if _, err := io.ReadFull(r, buf); err != nil {
			return 0, mapTrunc(err)
		}
		dst.Write(buf)
	}

	// Block sequence: read Block_Header (3 bytes) + content until
	// Last_Block flag is set.
	for {
		var bh [3]byte
		if _, err := io.ReadFull(r, bh[:]); err != nil {
			return 0, mapTrunc(err)
		}
		dst.Write(bh[:])

		// Block_Header is 24 bits, little-endian.
		bhVal := uint32(bh[0]) | uint32(bh[1])<<8 | uint32(bh[2])<<16
		lastBlock := bhVal & 0x01
		blockType := (bhVal >> 1) & 0x03
		blockSize := bhVal >> 3

		var contentLen int64
		switch blockType {
		case 0: // Raw_Block
			contentLen = int64(blockSize)
		case 1: // RLE_Block — single byte repeated Block_Size times.
			contentLen = 1
		case 2: // Compressed_Block
			contentLen = int64(blockSize)
		case 3:
			return 0, fmt.Errorf("%w: reserved block type", ErrInvalidFormat)
		}

		if contentLen > 0 {
			if _, err := io.CopyN(dst, r, contentLen); err != nil {
				return 0, mapTrunc(err)
			}
		}

		if lastBlock != 0 {
			break
		}
	}

	// Optional 4-byte content checksum.
	if checksumFlag != 0 {
		var ck [4]byte
		if _, err := io.ReadFull(r, ck[:]); err != nil {
			return 0, mapTrunc(err)
		}
		dst.Write(ck[:])
	}

	return int64(dst.Len() - startLen), nil
}

// dictIDFieldSize returns the size of the Dictionary_ID field in bytes
// for the two-bit Dictionary_ID_Flag.
func dictIDFieldSize(flag byte) int {
	switch flag {
	case 0:
		return 0
	case 1:
		return 1
	case 2:
		return 2
	case 3:
		return 4
	}
	return 0
}

// frameContentSizeFieldSize returns the size of the Frame_Content_Size
// field in bytes given the FCS flag (top 2 bits of FHD) and the
// Single_Segment_Flag.
func frameContentSizeFieldSize(flag byte, singleSegment bool) int {
	switch flag {
	case 0:
		if singleSegment {
			return 1
		}
		return 0
	case 1:
		return 2
	case 2:
		return 4
	case 3:
		return 8
	}
	return 0
}

// mapTrunc maps a truncation-style error from a Read into the Scan
// function's error contract: a partial frame surfaces as
// ErrInvalidFormat; other errors propagate.
func mapTrunc(err error) error {
	if err == io.EOF || err == io.ErrUnexpectedEOF {
		return ErrInvalidFormat
	}
	return err
}
