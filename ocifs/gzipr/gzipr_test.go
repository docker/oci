package gzipr

import (
	"bytes"
	"compress/gzip"
	"crypto/rand"
	"errors"
	"io"
	mrand "math/rand"
	"sync"
	"sync/atomic"
	"testing"
)

func makeGzip(t *testing.T, payload []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	if _, err := gw.Write(payload); err != nil {
		t.Fatalf("gzip write: %v", err)
	}
	if err := gw.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	return buf.Bytes()
}

// makeGzipFlush writes payload in chunks separated by Flush calls so
// the stream contains many DEFLATE block boundaries — an exercise of
// the checkpoint emission path.
func makeGzipFlush(t *testing.T, payload []byte, chunk int) []byte {
	t.Helper()
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	for off := 0; off < len(payload); off += chunk {
		end := off + chunk
		if end > len(payload) {
			end = len(payload)
		}
		if _, err := gw.Write(payload[off:end]); err != nil {
			t.Fatalf("gzip write: %v", err)
		}
		if err := gw.Flush(); err != nil {
			t.Fatalf("gzip flush: %v", err)
		}
	}
	if err := gw.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	return buf.Bytes()
}

func randPayload(n int) []byte {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return b
}

func TestScanRoundTrip(t *testing.T) {
	payload := randPayload(64 * 1024)
	gz := makeGzip(t, payload)

	var out bytes.Buffer
	idx, err := Scan(bytes.NewReader(gz), &out)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if !bytes.Equal(out.Bytes(), payload) {
		t.Fatal("decompressed output mismatch")
	}
	if idx.Size != int64(len(payload)) {
		t.Fatalf("idx.Size = %d; want %d", idx.Size, len(payload))
	}
	if idx.Checkpoints == nil {
		t.Fatal("Checkpoints must be non-nil even when empty")
	}
}

func TestScanShortStreamHasEmptyCheckpoints(t *testing.T) {
	payload := []byte("hello world")
	gz := makeGzip(t, payload)
	idx, err := Scan(bytes.NewReader(gz), io.Discard)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if idx.Checkpoints == nil {
		t.Fatal("Checkpoints must be non-nil empty slice")
	}
	if len(idx.Checkpoints) != 0 {
		t.Fatalf("len(Checkpoints) = %d; want 0", len(idx.Checkpoints))
	}
}

func TestScanCheckpointsEmittedAtSpan(t *testing.T) {
	payload := randPayload(2 * 1024 * 1024)
	gz := makeGzipFlush(t, payload, 64*1024)
	idx, err := Scan(bytes.NewReader(gz), io.Discard, WithSpan(256*1024))
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(idx.Checkpoints) == 0 {
		t.Fatal("expected checkpoints with frequent flush + small span")
	}
	for i := 1; i < len(idx.Checkpoints); i++ {
		if idx.Checkpoints[i].Out <= idx.Checkpoints[i-1].Out {
			t.Fatalf("checkpoints not Out-sorted at %d", i)
		}
		if idx.Checkpoints[i].Out-idx.Checkpoints[i-1].Out < 256*1024 {
			t.Fatalf("checkpoint span violation at %d: %d -> %d",
				i, idx.Checkpoints[i-1].Out, idx.Checkpoints[i].Out)
		}
	}
}

func TestScanWriteErrorUnwrapped(t *testing.T) {
	payload := randPayload(8 * 1024)
	gz := makeGzip(t, payload)

	sentinel := errors.New("boom")
	w := &errWriter{err: sentinel, after: 10}
	_, err := Scan(bytes.NewReader(gz), w)
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected sentinel write error, got %v", err)
	}
	if errors.Is(err, ErrInvalidFormat) {
		t.Fatalf("write error must not be wrapped as ErrInvalidFormat: %v", err)
	}
}

func TestScanInvalidFormat(t *testing.T) {
	bad := []byte("not gzip at all")
	_, err := Scan(bytes.NewReader(bad), io.Discard)
	if !errors.Is(err, ErrInvalidFormat) {
		t.Fatalf("expected ErrInvalidFormat, got %v", err)
	}
}

func TestScanClosedPipe(t *testing.T) {
	payload := randPayload(8 * 1024)
	gz := makeGzip(t, payload)
	pr, pw := io.Pipe()
	pr.CloseWithError(io.ErrClosedPipe)
	_, err := Scan(bytes.NewReader(gz), pw)
	if !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("expected io.ErrClosedPipe, got %v", err)
	}
}

type errWriter struct {
	w     bytes.Buffer
	err   error
	after int
}

func (e *errWriter) Write(p []byte) (int, error) {
	if e.w.Len()+len(p) > e.after {
		_, _ = e.w.Write(p)
		return len(p), e.err
	}
	return e.w.Write(p)
}

func TestReaderRandomAccess(t *testing.T) {
	payload := randPayload(2 * 1024 * 1024)
	gz := makeGzipFlush(t, payload, 32*1024)

	idx, err := Scan(bytes.NewReader(gz), io.Discard, WithSpan(64*1024))
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(idx.Checkpoints) < 8 {
		t.Fatalf("expected several checkpoints, got %d", len(idx.Checkpoints))
	}

	r := NewReaderWithIndex(bytes.NewReader(gz), idx, int64(len(gz)))
	defer r.Close()

	if r.Size() != int64(len(payload)) {
		t.Fatalf("Size() = %d; want %d", r.Size(), len(payload))
	}

	rng := mrand.New(mrand.NewSource(0xC0FFEE))
	for i := 0; i < 100; i++ {
		off := rng.Int63n(int64(len(payload)))
		max := int64(len(payload)) - off
		if max > 64*1024 {
			max = 64 * 1024
		}
		n := rng.Int63n(max) + 1
		got := make([]byte, n)
		nn, err := r.ReadAt(got, off)
		if err != nil && err != io.EOF {
			t.Fatalf("ReadAt(%d, %d): %v", n, off, err)
		}
		if int64(nn) != n {
			t.Fatalf("ReadAt(%d, %d) returned %d", n, off, nn)
		}
		want := payload[off : off+n]
		if !bytes.Equal(got, want) {
			t.Fatalf("ReadAt(%d, %d) mismatch", n, off)
		}
	}
}

func TestReaderClampedReadEOF(t *testing.T) {
	payload := randPayload(100 * 1024)
	gz := makeGzipFlush(t, payload, 16*1024)
	idx, err := Scan(bytes.NewReader(gz), io.Discard, WithSpan(16*1024))
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	r := NewReaderWithIndex(bytes.NewReader(gz), idx, int64(len(gz)))
	defer r.Close()

	want := payload[len(payload)-100:]
	got := make([]byte, 200)
	n, err := r.ReadAt(got, int64(len(payload)-100))
	if err != io.EOF {
		t.Fatalf("expected io.EOF, got %v", err)
	}
	if n != 100 {
		t.Fatalf("n = %d; want 100", n)
	}
	if !bytes.Equal(got[:n], want) {
		t.Fatal("clamped tail data mismatch")
	}
}

func TestReaderExactBoundaryFullRead(t *testing.T) {
	payload := randPayload(100 * 1024)
	gz := makeGzipFlush(t, payload, 16*1024)
	idx, err := Scan(bytes.NewReader(gz), io.Discard, WithSpan(16*1024))
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	r := NewReaderWithIndex(bytes.NewReader(gz), idx, int64(len(gz)))
	defer r.Close()

	got := make([]byte, 100)
	n, err := r.ReadAt(got, int64(len(payload)-100))
	if err != nil {
		t.Fatalf("expected nil err, got %v", err)
	}
	if n != 100 {
		t.Fatalf("n = %d; want 100", n)
	}
}

func TestReaderEmpty(t *testing.T) {
	payload := randPayload(8 * 1024)
	gz := makeGzip(t, payload)
	idx, err := Scan(bytes.NewReader(gz), io.Discard)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	r := NewReaderWithIndex(bytes.NewReader(gz), idx, int64(len(gz)))
	defer r.Close()

	n, err := r.ReadAt(nil, 0)
	if err != nil || n != 0 {
		t.Fatalf("ReadAt(empty) = (%d, %v); want (0, nil)", n, err)
	}
	n, err = r.ReadAt(make([]byte, 1), int64(len(payload)))
	if err != io.EOF || n != 0 {
		t.Fatalf("ReadAt past end = (%d, %v); want (0, io.EOF)", n, err)
	}
}

func TestReaderClosed(t *testing.T) {
	payload := randPayload(8 * 1024)
	gz := makeGzip(t, payload)
	idx, err := Scan(bytes.NewReader(gz), io.Discard)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	r := NewReaderWithIndex(bytes.NewReader(gz), idx, int64(len(gz)))
	r.Close()

	_, err = r.ReadAt(make([]byte, 8), 0)
	if !errors.Is(err, ErrClosed) {
		t.Fatalf("expected ErrClosed, got %v", err)
	}
	// Idempotent close
	if err := r.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

func TestNewReader(t *testing.T) {
	payload := randPayload(512 * 1024)
	gz := makeGzipFlush(t, payload, 16*1024)
	r, err := NewReader(bytes.NewReader(gz), int64(len(gz)), WithSpan(64*1024))
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	defer r.Close()

	if r.Size() != int64(len(payload)) {
		t.Fatalf("Size = %d; want %d", r.Size(), len(payload))
	}
	got := make([]byte, len(payload))
	n, err := r.ReadAt(got, 0)
	if err != nil {
		t.Fatalf("ReadAt: %v", err)
	}
	if n != len(payload) || !bytes.Equal(got, payload) {
		t.Fatal("data mismatch")
	}
}

func TestIndexEncodeDecode(t *testing.T) {
	payload := randPayload(256 * 1024)
	gz := makeGzipFlush(t, payload, 16*1024)
	idx, err := Scan(bytes.NewReader(gz), io.Discard, WithSpan(64*1024))
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	var buf bytes.Buffer
	if err := idx.Encode(&buf); err != nil {
		t.Fatalf("Encode: %v", err)
	}
	got, err := DecodeIndex(&buf)
	if err != nil {
		t.Fatalf("DecodeIndex: %v", err)
	}
	if got.Size != idx.Size {
		t.Fatalf("Size mismatch")
	}
	if len(got.Checkpoints) != len(idx.Checkpoints) {
		t.Fatalf("Checkpoints length mismatch")
	}
	for i, c := range got.Checkpoints {
		o := idx.Checkpoints[i]
		if c.In != o.In || c.Out != o.Out || c.B != o.B || c.NB != o.NB || !bytes.Equal(c.Hist, o.Hist) {
			t.Fatalf("Checkpoint[%d] mismatch", i)
		}
	}

	r := NewReaderWithIndex(bytes.NewReader(gz), got, int64(len(gz)))
	defer r.Close()
	out := make([]byte, 1024)
	n, err := r.ReadAt(out, 100*1024)
	if err != nil || n != 1024 {
		t.Fatalf("ReadAt after decode = (%d, %v)", n, err)
	}
	if !bytes.Equal(out, payload[100*1024:100*1024+1024]) {
		t.Fatal("data mismatch after Encode/Decode round-trip")
	}
}

func TestReaderConcurrent(t *testing.T) {
	payload := randPayload(1024 * 1024)
	gz := makeGzipFlush(t, payload, 16*1024)
	idx, err := Scan(bytes.NewReader(gz), io.Discard, WithSpan(32*1024))
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	r := NewReaderWithIndex(bytes.NewReader(gz), idx, int64(len(gz)))
	defer r.Close()

	const goroutines = 16
	var wg sync.WaitGroup
	wg.Add(goroutines)
	errCh := make(chan error, goroutines)
	for g := 0; g < goroutines; g++ {
		seed := int64(g)
		go func() {
			defer wg.Done()
			rng := mrand.New(mrand.NewSource(seed))
			for i := 0; i < 50; i++ {
				off := rng.Int63n(int64(len(payload)))
				max := int64(len(payload)) - off
				if max > 4096 {
					max = 4096
				}
				n := rng.Int63n(max) + 1
				got := make([]byte, n)
				_, err := r.ReadAt(got, off)
				if err != nil && err != io.EOF {
					errCh <- err
					return
				}
				if !bytes.Equal(got, payload[off:off+n]) {
					errCh <- errors.New("data mismatch in goroutine")
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for e := range errCh {
		t.Fatal(e)
	}
}

// countingReaderAt records the byte ranges fetched.
type countingReaderAt struct {
	ra     io.ReaderAt
	mu     sync.Mutex
	calls  int32
	ranges [][2]int64
	bytes  int64
}

func (c *countingReaderAt) ReadAt(p []byte, off int64) (int, error) {
	atomic.AddInt32(&c.calls, 1)
	c.mu.Lock()
	c.ranges = append(c.ranges, [2]int64{off, int64(len(p))})
	c.bytes += int64(len(p))
	c.mu.Unlock()
	return c.ra.ReadAt(p, off)
}

func TestReaderRandomAccessNoFlush(t *testing.T) {
	// Use default gzip (no Flush) with a large repeating-pattern payload
	// so the encoder is forced to split into multiple non-byte-aligned
	// DEFLATE blocks, exercising the (nb > 0) checkpoint path.
	const N = 4 * 1024 * 1024
	payload := make([]byte, N)
	for i := range payload {
		payload[i] = byte(i*17 + 13)
	}
	// Make one large random burst in the middle to defeat compression
	// homogeneity but keep stream non-trivially compressible.
	rng := mrand.New(mrand.NewSource(42))
	for i := N / 4; i < N/2; i++ {
		payload[i] = byte(rng.Intn(256))
	}

	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	if _, err := gw.Write(payload); err != nil {
		t.Fatalf("gzip write: %v", err)
	}
	if err := gw.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	gz := buf.Bytes()

	idx, err := Scan(bytes.NewReader(gz), io.Discard, WithSpan(64*1024))
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	t.Logf("emitted %d checkpoints over %d byte payload", len(idx.Checkpoints), N)

	r := NewReaderWithIndex(bytes.NewReader(gz), idx, int64(len(gz)))
	defer r.Close()

	rng2 := mrand.New(mrand.NewSource(7))
	for i := 0; i < 100; i++ {
		off := rng2.Int63n(int64(N))
		max := int64(N) - off
		if max > 64*1024 {
			max = 64 * 1024
		}
		n := rng2.Int63n(max) + 1
		got := make([]byte, n)
		nn, err := r.ReadAt(got, off)
		if err != nil && err != io.EOF {
			t.Fatalf("ReadAt(%d, %d): %v", n, off, err)
		}
		if int64(nn) != n {
			t.Fatalf("ReadAt(%d, %d) returned %d", n, off, nn)
		}
		want := payload[off : off+n]
		if !bytes.Equal(got, want) {
			// Find first diff for a useful error message.
			for k := range got {
				if got[k] != want[k] {
					t.Fatalf("ReadAt(%d, %d) mismatch at +%d: got %02x want %02x",
						n, off, k, got[k], want[k])
				}
			}
		}
	}
}

func TestScanWithFNAMEHeader(t *testing.T) {
	payload := randPayload(64 * 1024)
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	gw.Name = "data.bin"
	gw.Comment = "a comment"
	if _, err := gw.Write(payload); err != nil {
		t.Fatalf("gzip write: %v", err)
	}
	if err := gw.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	gz := buf.Bytes()

	var out bytes.Buffer
	idx, err := Scan(bytes.NewReader(gz), &out)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if !bytes.Equal(out.Bytes(), payload) {
		t.Fatal("data mismatch with FNAME/FCOMMENT")
	}
	if idx.Size != int64(len(payload)) {
		t.Fatal("Size mismatch")
	}
}

func TestReaderFetchesOnlyNeededSpan(t *testing.T) {
	payload := randPayload(512 * 1024)
	gz := makeGzipFlush(t, payload, 16*1024)
	idx, err := Scan(bytes.NewReader(gz), io.Discard, WithSpan(32*1024))
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(idx.Checkpoints) < 4 {
		t.Skipf("not enough checkpoints (%d) to verify range narrowing", len(idx.Checkpoints))
	}
	cra := &countingReaderAt{ra: bytes.NewReader(gz)}
	r := NewReaderWithIndex(cra, idx, int64(len(gz)))
	defer r.Close()

	c := idx.Checkpoints[1]
	got := make([]byte, 64)
	if _, err := r.ReadAt(got, c.Out); err != nil {
		t.Fatalf("ReadAt: %v", err)
	}
	if cra.bytes >= int64(len(gz)) {
		t.Fatalf("fetched %d bytes; whole blob is %d — range not narrowed", cra.bytes, len(gz))
	}
}
