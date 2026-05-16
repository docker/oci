package zstdr_test

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"math/rand"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/klauspost/compress/zstd"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/docker/oci/ocifs/zstdr"
)

// encodeFrames encodes the given byte slices as a sequence of
// concatenated zstd frames (one frame per slice). Useful to produce
// multi-frame fixtures with predictable boundaries.
func encodeFrames(t *testing.T, parts ...[]byte) []byte {
	t.Helper()
	var out bytes.Buffer
	enc, err := zstd.NewWriter(&out)
	require.NoError(t, err)
	for i, p := range parts {
		_, err := enc.Write(p)
		require.NoError(t, err)
		if i < len(parts)-1 {
			require.NoError(t, enc.Close())
			enc.Reset(&out)
		}
	}
	require.NoError(t, enc.Close())
	return out.Bytes()
}

// encodeSingleFrame encodes data as a single zstd frame.
func encodeSingleFrame(t *testing.T, data []byte) []byte {
	t.Helper()
	var out bytes.Buffer
	enc, err := zstd.NewWriter(&out)
	require.NoError(t, err)
	_, err = enc.Write(data)
	require.NoError(t, err)
	require.NoError(t, enc.Close())
	return out.Bytes()
}

// makeSkippableFrame returns a skippable frame (magic + size + payload)
// with the given user ID (0..15).
func makeSkippableFrame(userID byte, payload []byte) []byte {
	if userID > 15 {
		panic("userID out of range")
	}
	magic := uint32(0x184D2A50) | uint32(userID)
	hdr := make([]byte, 8)
	binary.LittleEndian.PutUint32(hdr[0:4], magic)
	binary.LittleEndian.PutUint32(hdr[4:8], uint32(len(payload)))
	return append(hdr, payload...)
}

func TestScanSingleFrameRoundTrip(t *testing.T) {
	data := bytes.Repeat([]byte("hello, zstd! "), 4096)
	compressed := encodeSingleFrame(t, data)

	var out bytes.Buffer
	idx, err := zstdr.Scan(bytes.NewReader(compressed), &out)
	require.NoError(t, err)
	assert.Equal(t, data, out.Bytes())
	require.NotNil(t, idx.Frames)
	require.Len(t, idx.Frames, 1)
	assert.Equal(t, int64(0), idx.Frames[0].In)
	assert.Equal(t, int64(0), idx.Frames[0].Out)
	assert.Equal(t, int64(len(data)), idx.Size)
}

func TestScanMultiFrame(t *testing.T) {
	parts := [][]byte{
		bytes.Repeat([]byte("aaaa"), 1024),
		bytes.Repeat([]byte("bbbb"), 2048),
		bytes.Repeat([]byte("cccc"), 512),
	}
	compressed := encodeFrames(t, parts...)

	var out bytes.Buffer
	idx, err := zstdr.Scan(bytes.NewReader(compressed), &out)
	require.NoError(t, err)

	var expected []byte
	for _, p := range parts {
		expected = append(expected, p...)
	}
	assert.Equal(t, expected, out.Bytes())
	require.Len(t, idx.Frames, 3)

	// Frame Out offsets should partition the decompressed stream by
	// part length.
	var outOff int64
	for i, p := range parts {
		assert.Equal(t, outOff, idx.Frames[i].Out, "frame %d", i)
		outOff += int64(len(p))
	}
	assert.Equal(t, int64(len(expected)), idx.Size)

	// In offsets must be strictly increasing and start at 0.
	assert.Equal(t, int64(0), idx.Frames[0].In)
	for i := 1; i < len(idx.Frames); i++ {
		assert.Greater(t, idx.Frames[i].In, idx.Frames[i-1].In, "frame %d", i)
	}
}

func TestScanEmptyInput(t *testing.T) {
	var out bytes.Buffer
	idx, err := zstdr.Scan(bytes.NewReader(nil), &out)
	require.NoError(t, err)
	require.NotNil(t, idx.Frames)
	assert.Len(t, idx.Frames, 0)
	assert.Equal(t, int64(0), idx.Size)
	assert.Equal(t, 0, out.Len())
}

func TestScanSkippableFramesArePassedThrough(t *testing.T) {
	dataA := bytes.Repeat([]byte("AAAA"), 256)
	dataB := bytes.Repeat([]byte("BBBB"), 256)
	frameA := encodeSingleFrame(t, dataA)
	frameB := encodeSingleFrame(t, dataB)
	skip := makeSkippableFrame(3, []byte("hello-skippable-payload"))
	skipEmpty := makeSkippableFrame(0, nil)

	// Stream layout:
	//   skipEmpty | frameA | skip | frameB
	var compressed bytes.Buffer
	compressed.Write(skipEmpty)
	compressed.Write(frameA)
	compressed.Write(skip)
	compressed.Write(frameB)

	var out bytes.Buffer
	idx, err := zstdr.Scan(bytes.NewReader(compressed.Bytes()), &out)
	require.NoError(t, err)
	assert.Equal(t, append(append([]byte{}, dataA...), dataB...), out.Bytes())
	require.Len(t, idx.Frames, 2)
	assert.Equal(t, int64(len(skipEmpty)), idx.Frames[0].In)
	assert.Equal(t, int64(len(skipEmpty)+len(frameA)+len(skip)), idx.Frames[1].In)
	assert.Equal(t, int64(0), idx.Frames[0].Out)
	assert.Equal(t, int64(len(dataA)), idx.Frames[1].Out)
	assert.Equal(t, int64(len(dataA)+len(dataB)), idx.Size)
}

func TestScanRejectsInvalidMagic(t *testing.T) {
	// Four bytes that are neither zstd magic nor skippable.
	var out bytes.Buffer
	_, err := zstdr.Scan(bytes.NewReader([]byte{0x00, 0x01, 0x02, 0x03}), &out)
	assert.ErrorIs(t, err, zstdr.ErrInvalidFormat)
}

func TestScanRejectsTruncatedFrame(t *testing.T) {
	data := bytes.Repeat([]byte("payload"), 100)
	compressed := encodeSingleFrame(t, data)

	// Truncate halfway through the frame body.
	truncated := compressed[:len(compressed)/2]
	var out bytes.Buffer
	_, err := zstdr.Scan(bytes.NewReader(truncated), &out)
	assert.ErrorIs(t, err, zstdr.ErrInvalidFormat)
}

func TestIndexEncodeDecodeRoundTrip(t *testing.T) {
	idx := &zstdr.Index{
		Frames: []*zstdr.FrameCheckpoint{
			{In: 0, Out: 0},
			{In: 1024, Out: 4096},
			{In: 2048, Out: 8192},
		},
		Size: 16384,
	}
	var buf bytes.Buffer
	require.NoError(t, idx.Encode(&buf))
	got, err := zstdr.DecodeIndex(&buf)
	require.NoError(t, err)
	assert.Equal(t, idx, got)
}

func TestIndexEncodeEmptyFramesIsArrayNotNull(t *testing.T) {
	idx := &zstdr.Index{Frames: []*zstdr.FrameCheckpoint{}, Size: 1}
	var buf bytes.Buffer
	require.NoError(t, idx.Encode(&buf))
	assert.Contains(t, buf.String(), `"frames":[]`)
	assert.NotContains(t, buf.String(), `"frames":null`)
}

// countingReaderAt records the byte ranges fetched, so tests can
// verify that ReadAt only fetches the expected compressed span.
type countingReaderAt struct {
	data       []byte
	mu         sync.Mutex
	calls      int
	totalBytes int64
	ranges     []offRange
}

type offRange struct {
	off int64
	n   int
}

func (c *countingReaderAt) ReadAt(p []byte, off int64) (int, error) {
	c.mu.Lock()
	c.calls++
	c.ranges = append(c.ranges, offRange{off: off, n: len(p)})
	c.totalBytes += int64(len(p))
	c.mu.Unlock()
	if off < 0 || off >= int64(len(c.data)) {
		return 0, io.EOF
	}
	n := copy(p, c.data[off:])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

func TestNewReaderAndReadAt(t *testing.T) {
	parts := [][]byte{
		bytes.Repeat([]byte("alpha-"), 1000),
		bytes.Repeat([]byte("beta--"), 1500),
		bytes.Repeat([]byte("gamma!"), 2000),
	}
	var expected []byte
	for _, p := range parts {
		expected = append(expected, p...)
	}
	compressed := encodeFrames(t, parts...)

	cra := &countingReaderAt{data: compressed}
	r, err := zstdr.NewReader(cra, int64(len(compressed)))
	require.NoError(t, err)
	t.Cleanup(func() { r.Close() })

	assert.Equal(t, int64(len(expected)), r.Size())
	require.Len(t, r.Index().Frames, 3)

	t.Run("FullReadFromZero", func(t *testing.T) {
		buf := make([]byte, len(expected))
		n, err := r.ReadAt(buf, 0)
		assert.NoError(t, err)
		assert.Equal(t, len(expected), n)
		assert.Equal(t, expected, buf)
	})

	t.Run("EmptyBuffer", func(t *testing.T) {
		n, err := r.ReadAt(nil, 0)
		assert.NoError(t, err)
		assert.Equal(t, 0, n)
	})

	t.Run("OffsetAtEnd", func(t *testing.T) {
		buf := make([]byte, 8)
		n, err := r.ReadAt(buf, r.Size())
		assert.Equal(t, 0, n)
		assert.Equal(t, io.EOF, err)
	})

	t.Run("OffsetBeyondEnd", func(t *testing.T) {
		buf := make([]byte, 8)
		n, err := r.ReadAt(buf, r.Size()+100)
		assert.Equal(t, 0, n)
		assert.Equal(t, io.EOF, err)
	})

	t.Run("ClampedRead", func(t *testing.T) {
		buf := make([]byte, 1024)
		off := r.Size() - 100
		n, err := r.ReadAt(buf, off)
		assert.Equal(t, 100, n)
		assert.Equal(t, io.EOF, err)
		assert.Equal(t, expected[off:], buf[:n])
	})

	t.Run("FullReadEndingExactlyAtBoundary", func(t *testing.T) {
		buf := make([]byte, 100)
		off := r.Size() - 100
		n, err := r.ReadAt(buf, off)
		assert.NoError(t, err)
		assert.Equal(t, 100, n)
		assert.Equal(t, expected[off:], buf)
	})
}

func TestReadAtMatchesSequentialDecompress(t *testing.T) {
	// Build a fixture with several frames.
	rng := rand.New(rand.NewSource(42))
	var parts [][]byte
	for i := 0; i < 5; i++ {
		size := 1024 + rng.Intn(8192)
		p := make([]byte, size)
		// Mix of compressible and incompressible-ish data.
		for j := range p {
			p[j] = byte(rng.Intn(256))
		}
		// Inject a repeating pattern in part of the buffer to make
		// blocks compressible.
		if i%2 == 0 {
			pat := []byte("repeating-pattern-")
			for j := 0; j+len(pat) <= len(p)/2; j += len(pat) {
				copy(p[j:], pat)
			}
		}
		parts = append(parts, p)
	}
	var expected []byte
	for _, p := range parts {
		expected = append(expected, p...)
	}
	compressed := encodeFrames(t, parts...)

	r, err := zstdr.NewReader(bytes.NewReader(compressed), int64(len(compressed)))
	require.NoError(t, err)
	t.Cleanup(func() { r.Close() })
	require.Equal(t, int64(len(expected)), r.Size())

	// 100 random ReadAt calls.
	for i := 0; i < 100; i++ {
		off := int64(rng.Intn(len(expected) + 100))
		size := 1 + rng.Intn(len(expected)/4)

		buf := make([]byte, size)
		n, err := r.ReadAt(buf, off)

		switch {
		case off >= int64(len(expected)):
			assert.Equal(t, 0, n, "i=%d off=%d", i, off)
			assert.Equal(t, io.EOF, err, "i=%d off=%d", i, off)
		case off+int64(size) > int64(len(expected)):
			expN := int(int64(len(expected)) - off)
			assert.Equal(t, expN, n, "i=%d off=%d", i, off)
			assert.Equal(t, io.EOF, err, "i=%d off=%d", i, off)
			assert.Equal(t, expected[off:off+int64(expN)], buf[:n], "i=%d off=%d", i, off)
		default:
			assert.NoError(t, err, "i=%d off=%d size=%d", i, off, size)
			assert.Equal(t, size, n, "i=%d off=%d", i, off)
			assert.Equal(t, expected[off:off+int64(size)], buf[:n], "i=%d off=%d", i, off)
		}
	}
}

func TestReadAtRangeIsBoundedByFrameWindow(t *testing.T) {
	// Use predictable per-frame data so the index has well-known
	// frame boundaries; assert that reading from inside one frame
	// only fetches that frame's compressed span (or a contiguous
	// span ending at the next frame's start).
	parts := [][]byte{
		bytes.Repeat([]byte("AAAAAAAA"), 4096),
		bytes.Repeat([]byte("BBBBBBBB"), 4096),
		bytes.Repeat([]byte("CCCCCCCC"), 4096),
	}
	compressed := encodeFrames(t, parts...)
	cra := &countingReaderAt{data: compressed}
	r, err := zstdr.NewReader(cra, int64(len(compressed)))
	require.NoError(t, err)
	t.Cleanup(func() { r.Close() })

	idx := r.Index()
	require.Len(t, idx.Frames, 3)

	// Reset call counter — NewReader scans which uses the
	// underlying io.Reader, but our cra is io.ReaderAt; scan goes
	// through io.NewSectionReader so it still hits ReadAt.
	cra.mu.Lock()
	cra.ranges = nil
	cra.calls = 0
	cra.totalBytes = 0
	cra.mu.Unlock()

	// Read 8 bytes from inside the second frame. Only the second
	// frame's compressed span should be fetched.
	off := idx.Frames[1].Out + 16
	buf := make([]byte, 8)
	n, err := r.ReadAt(buf, off)
	require.NoError(t, err)
	assert.Equal(t, 8, n)

	// Exactly one ReadAt should have hit the underlying ra, with
	// off == idx.Frames[1].In and length == idx.Frames[2].In - idx.Frames[1].In.
	cra.mu.Lock()
	defer cra.mu.Unlock()
	require.Len(t, cra.ranges, 1)
	assert.Equal(t, idx.Frames[1].In, cra.ranges[0].off)
	assert.Equal(t, int(idx.Frames[2].In-idx.Frames[1].In), cra.ranges[0].n)
}

func TestNewReaderWithIndexRoundTrip(t *testing.T) {
	parts := [][]byte{
		bytes.Repeat([]byte("xxx"), 1000),
		bytes.Repeat([]byte("yyy"), 1000),
	}
	compressed := encodeFrames(t, parts...)

	// Build via Scan.
	var sink bytes.Buffer
	idx, err := zstdr.Scan(bytes.NewReader(compressed), &sink)
	require.NoError(t, err)

	// Round-trip the index via JSON.
	var jsonBuf bytes.Buffer
	require.NoError(t, idx.Encode(&jsonBuf))
	idx2, err := zstdr.DecodeIndex(&jsonBuf)
	require.NoError(t, err)

	r := zstdr.NewReaderWithIndex(bytes.NewReader(compressed), idx2, int64(len(compressed)))
	t.Cleanup(func() { r.Close() })

	expected := append(append([]byte{}, parts[0]...), parts[1]...)
	buf := make([]byte, len(expected))
	n, err := r.ReadAt(buf, 0)
	require.NoError(t, err)
	assert.Equal(t, len(expected), n)
	assert.Equal(t, expected, buf)
}

func TestNewReaderWithIndexNilFramesPanics(t *testing.T) {
	idx := &zstdr.Index{Size: 1}
	assert.Panics(t, func() {
		zstdr.NewReaderWithIndex(bytes.NewReader(nil), idx, 0)
	})
}

func TestNewReaderWithIndexZeroSizePanics(t *testing.T) {
	idx := &zstdr.Index{Frames: []*zstdr.FrameCheckpoint{}, Size: 0}
	assert.Panics(t, func() {
		zstdr.NewReaderWithIndex(bytes.NewReader(nil), idx, 0)
	})
}

func TestCloseReturnsErrClosed(t *testing.T) {
	parts := [][]byte{bytes.Repeat([]byte("z"), 1000)}
	compressed := encodeFrames(t, parts...)

	r, err := zstdr.NewReader(bytes.NewReader(compressed), int64(len(compressed)))
	require.NoError(t, err)
	require.NoError(t, r.Close())

	buf := make([]byte, 4)
	n, err := r.ReadAt(buf, 0)
	assert.Equal(t, 0, n)
	assert.ErrorIs(t, err, zstdr.ErrClosed)
}

func TestNewReaderRejectsCorrupt(t *testing.T) {
	junk := []byte{0xDE, 0xAD, 0xBE, 0xEF, 0xDE, 0xAD, 0xBE, 0xEF}
	_, err := zstdr.NewReader(bytes.NewReader(junk), int64(len(junk)))
	assert.ErrorIs(t, err, zstdr.ErrInvalidFormat)
}

func TestConcurrentReadAt(t *testing.T) {
	parts := [][]byte{
		bytes.Repeat([]byte("AAAA"), 4096),
		bytes.Repeat([]byte("BBBB"), 4096),
		bytes.Repeat([]byte("CCCC"), 4096),
		bytes.Repeat([]byte("DDDD"), 4096),
	}
	var expected []byte
	for _, p := range parts {
		expected = append(expected, p...)
	}
	compressed := encodeFrames(t, parts...)

	r, err := zstdr.NewReader(bytes.NewReader(compressed), int64(len(compressed)), zstdr.WithMaxReaders(4))
	require.NoError(t, err)
	t.Cleanup(func() { r.Close() })

	const goroutines = 16
	const reads = 50
	var failures int64

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func(seed int) {
			defer wg.Done()
			rng := rand.New(rand.NewSource(int64(seed)))
			for i := 0; i < reads; i++ {
				size := 1 + rng.Intn(len(expected)/2)
				off := int64(rng.Intn(len(expected) - size + 1))
				buf := make([]byte, size)
				n, err := r.ReadAt(buf, off)
				if err != nil || n != size || !bytes.Equal(buf, expected[off:off+int64(size)]) {
					atomic.AddInt64(&failures, 1)
				}
			}
		}(g)
	}
	wg.Wait()
	assert.Equal(t, int64(0), failures)
}

func TestReadAtClosedDuringWait(t *testing.T) {
	// A maxReaders=1 reader holds its slot while we artificially
	// block in another goroutine.
	parts := [][]byte{bytes.Repeat([]byte("a"), 1000)}
	compressed := encodeFrames(t, parts...)
	r, err := zstdr.NewReader(bytes.NewReader(compressed), int64(len(compressed)), zstdr.WithMaxReaders(1))
	require.NoError(t, err)

	hold := &slowReaderAt{ReaderAt: bytes.NewReader(compressed)}
	idx := r.Index()
	r2 := zstdr.NewReaderWithIndex(hold, idx, int64(len(compressed)), zstdr.WithMaxReaders(1))
	t.Cleanup(func() { r2.Close() })

	hold.gate = make(chan struct{})

	// Goroutine 1 holds the only pool slot.
	doneG1 := make(chan struct{})
	go func() {
		defer close(doneG1)
		buf := make([]byte, 16)
		_, _ = r2.ReadAt(buf, 0)
	}()

	// Wait for goroutine 1 to be inside ReadAt and blocked on the
	// gate.
	hold.waitEntered()

	// Goroutine 2 will block on the pool slot.
	type res struct {
		n   int
		err error
	}
	doneG2 := make(chan res, 1)
	go func() {
		buf := make([]byte, 16)
		n, err := r2.ReadAt(buf, 0)
		doneG2 <- res{n: n, err: err}
	}()

	// Close should unblock goroutine 2 with ErrClosed while it waits
	// for goroutine 1's in-flight call to drain. Run Close in the
	// background: calling it synchronously before releasing hold.gate
	// would deadlock by construction.
	closeDone := make(chan error, 1)
	go func() {
		closeDone <- r2.Close()
	}()

	got := <-doneG2
	assert.Equal(t, 0, got.n)
	assert.ErrorIs(t, got.err, zstdr.ErrClosed)

	close(hold.gate)
	require.NoError(t, <-closeDone)
	<-doneG1
}

// slowReaderAt blocks the first ReadAt call on a gate channel so a
// second goroutine can be observed waiting on the bounded pool.
type slowReaderAt struct {
	io.ReaderAt
	gate    chan struct{}
	entered chan struct{}
	once    sync.Once
}

func (s *slowReaderAt) ReadAt(p []byte, off int64) (int, error) {
	s.once.Do(func() {
		if s.entered == nil {
			s.entered = make(chan struct{})
		}
		close(s.entered)
		<-s.gate
	})
	return s.ReaderAt.ReadAt(p, off)
}

func (s *slowReaderAt) waitEntered() {
	if s.entered == nil {
		s.entered = make(chan struct{})
	}
	<-s.entered
}

func TestScanFrameContentSizeFastPath(t *testing.T) {
	// The klauspost zstd writer sets Frame_Content_Size when the
	// stream is a single Write call (it knows the size up front).
	// We exercise both paths by scanning a file with FCS set
	// (single-Write) and one without FCS (multi-Write streaming).
	// In both cases the resulting Index must be identical apart
	// from compressed-frame layout details we can't fully control.
	data := bytes.Repeat([]byte("data-with-fcs-"), 2048)

	// Write with FCS set: single Write before Close.
	var withFCS bytes.Buffer
	enc, err := zstd.NewWriter(&withFCS)
	require.NoError(t, err)
	_, err = enc.Write(data)
	require.NoError(t, err)
	require.NoError(t, enc.Close())

	// Write without FCS: many small Writes; encoder may not know
	// total size at frame-header time.
	var withoutFCS bytes.Buffer
	enc2, err := zstd.NewWriter(&withoutFCS)
	require.NoError(t, err)
	const chunk = 17
	for i := 0; i < len(data); i += chunk {
		j := i + chunk
		if j > len(data) {
			j = len(data)
		}
		_, err := enc2.Write(data[i:j])
		require.NoError(t, err)
	}
	require.NoError(t, enc2.Close())

	for _, tc := range []struct {
		name string
		buf  []byte
	}{
		{"WithFCS", withFCS.Bytes()},
		{"WithoutFCS", withoutFCS.Bytes()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			idx, err := zstdr.Scan(bytes.NewReader(tc.buf), &out)
			require.NoError(t, err)
			assert.Equal(t, data, out.Bytes())
			assert.Equal(t, int64(len(data)), idx.Size)
			require.GreaterOrEqual(t, len(idx.Frames), 1)
			assert.Equal(t, int64(0), idx.Frames[0].In)
			assert.Equal(t, int64(0), idx.Frames[0].Out)
		})
	}
}

func TestReadAtRangerErrorPropagated(t *testing.T) {
	parts := [][]byte{
		bytes.Repeat([]byte("xx"), 2048),
		bytes.Repeat([]byte("yy"), 2048),
	}
	compressed := encodeFrames(t, parts...)
	idx, err := zstdr.Scan(bytes.NewReader(compressed), io.Discard)
	require.NoError(t, err)

	sentinel := errors.New("network down")
	er := &erroringReaderAt{err: sentinel}
	r := zstdr.NewReaderWithIndex(er, idx, int64(len(compressed)))
	t.Cleanup(func() { r.Close() })

	buf := make([]byte, 16)
	n, err := r.ReadAt(buf, 0)
	assert.Equal(t, 0, n)
	assert.ErrorIs(t, err, sentinel)
}

type erroringReaderAt struct{ err error }

func (e *erroringReaderAt) ReadAt(p []byte, off int64) (int, error) {
	return 0, e.err
}

// Compile-time assertion: *Reader is an io.ReaderAt.
var _ io.ReaderAt = (*zstdr.Reader)(nil)
