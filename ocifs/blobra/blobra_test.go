package blobra_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"

	"github.com/opencontainers/go-digest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/docker/oci"
	"github.com/docker/oci/ocifs/blobra"
	"github.com/docker/oci/ocimem"
)

const (
	testRepo      = "example/blob"
	testMediaType = "application/octet-stream"
)

func pushBlob(t *testing.T, reg *ocimem.Registry, data []byte) oci.Descriptor {
	t.Helper()
	desc := oci.Descriptor{
		MediaType: testMediaType,
		Size:      int64(len(data)),
		Digest:    digest.FromBytes(data),
	}
	out, err := reg.PushBlob(context.Background(), testRepo, desc, bytes.NewReader(data))
	require.NoError(t, err)
	return out
}

func TestNewAndAccessors(t *testing.T) {
	reg := ocimem.New()
	data := []byte("hello, world!")
	desc := pushBlob(t, reg, data)

	r := blobra.New(context.Background(), reg, testRepo, desc)
	assert.Equal(t, desc, r.Descriptor())
	assert.Equal(t, int64(len(data)), r.Size())
}

func TestReadAtAgainstOCIMem(t *testing.T) {
	reg := ocimem.New()
	data := []byte("0123456789ABCDEF")
	desc := pushBlob(t, reg, data)
	r := blobra.New(context.Background(), reg, testRepo, desc)

	tests := []struct {
		name    string
		bufLen  int
		off     int64
		wantN   int
		wantErr error
		wantBuf []byte
	}{
		{name: "EmptyBuffer", bufLen: 0, off: 0, wantN: 0, wantErr: nil, wantBuf: []byte{}},
		{name: "EmptyBufferAtEnd", bufLen: 0, off: int64(len(data)), wantN: 0, wantErr: nil, wantBuf: []byte{}},
		{name: "FullReadFromStart", bufLen: len(data), off: 0, wantN: len(data), wantErr: nil, wantBuf: data},
		{name: "PartialFromStart", bufLen: 4, off: 0, wantN: 4, wantErr: nil, wantBuf: data[:4]},
		{name: "InteriorRead", bufLen: 5, off: 3, wantN: 5, wantErr: nil, wantBuf: data[3:8]},
		{name: "ReadEndingExactlyAtBoundary", bufLen: 6, off: 10, wantN: 6, wantErr: nil, wantBuf: data[10:]},
		{name: "ReadPastEndShortFill", bufLen: 8, off: 10, wantN: 6, wantErr: io.EOF, wantBuf: data[10:]},
		{name: "OffsetAtSize", bufLen: 4, off: int64(len(data)), wantN: 0, wantErr: io.EOF, wantBuf: []byte{}},
		{name: "OffsetBeyondSize", bufLen: 4, off: int64(len(data) + 100), wantN: 0, wantErr: io.EOF, wantBuf: []byte{}},
		{name: "SingleByteAtEnd", bufLen: 1, off: int64(len(data) - 1), wantN: 1, wantErr: nil, wantBuf: data[len(data)-1:]},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			buf := make([]byte, tc.bufLen)
			n, err := r.ReadAt(buf, tc.off)
			assert.Equal(t, tc.wantN, n)
			assert.Equal(t, tc.wantErr, err)
			assert.Equal(t, tc.wantBuf, buf[:n])
		})
	}
}

// fakeRanger is a minimal BlobRanger used to exercise paths that ocimem
// cannot produce, such as a truncated or empty range response.
type fakeRanger struct {
	data []byte
	desc oci.Descriptor

	err           error
	truncateTo    int   // if > 0 and < requested, return only this many bytes
	emptyResponse bool  // return zero-byte body regardless of range
	gotOff0       int64 // recorded for assertions
	gotOff1       int64
	gotRepo       string
	gotDigest     oci.Digest
	calls         int
}

func (f *fakeRanger) GetBlobRange(ctx context.Context, repo string, dig oci.Digest, o0, o1 int64) (oci.BlobReader, error) {
	f.calls++
	f.gotOff0 = o0
	f.gotOff1 = o1
	f.gotRepo = repo
	f.gotDigest = dig
	if f.err != nil {
		return nil, f.err
	}
	var body []byte
	switch {
	case f.emptyResponse:
		body = nil
	case f.truncateTo > 0 && f.truncateTo < int(o1-o0):
		body = f.data[o0 : o0+int64(f.truncateTo)]
	default:
		body = f.data[o0:o1]
	}
	return ocimem.NewBytesReader(body, f.desc), nil
}

func newFake(data []byte) *fakeRanger {
	return &fakeRanger{
		data: data,
		desc: oci.Descriptor{
			MediaType: testMediaType,
			Size:      int64(len(data)),
			Digest:    digest.FromBytes(data),
		},
	}
}

func TestReadAtProtocolViolations(t *testing.T) {
	data := []byte("abcdefghijklmnop")

	t.Run("EmptyResponseBecomesUnexpectedEOF", func(t *testing.T) {
		fr := newFake(data)
		fr.emptyResponse = true
		r := blobra.New(context.Background(), fr, testRepo, fr.desc)

		buf := make([]byte, 4)
		n, err := r.ReadAt(buf, 0)
		assert.Equal(t, 0, n)
		assert.Equal(t, io.ErrUnexpectedEOF, err)
		assert.Equal(t, 1, fr.calls)
	})

	t.Run("TruncatedResponseBecomesUnexpectedEOF", func(t *testing.T) {
		fr := newFake(data)
		fr.truncateTo = 2
		r := blobra.New(context.Background(), fr, testRepo, fr.desc)

		buf := make([]byte, 8)
		n, err := r.ReadAt(buf, 0)
		assert.Equal(t, 0, n)
		assert.Equal(t, io.ErrUnexpectedEOF, err)
	})

	t.Run("TruncatedNearEndStillUnexpectedEOF", func(t *testing.T) {
		// Even when the request would be clamped at end-of-blob, a short
		// response from the server must surface as ErrUnexpectedEOF, not EOF.
		fr := newFake(data)
		fr.truncateTo = 1
		r := blobra.New(context.Background(), fr, testRepo, fr.desc)

		buf := make([]byte, 8)
		n, err := r.ReadAt(buf, int64(len(data)-4))
		assert.Equal(t, 0, n)
		assert.Equal(t, io.ErrUnexpectedEOF, err)
	})
}

func TestReadAtRangerErrorPropagated(t *testing.T) {
	data := []byte("abcdefgh")
	sentinel := errors.New("network down")

	fr := newFake(data)
	fr.err = sentinel
	r := blobra.New(context.Background(), fr, testRepo, fr.desc)

	buf := make([]byte, 4)
	n, err := r.ReadAt(buf, 0)
	assert.Equal(t, 0, n)
	assert.ErrorIs(t, err, sentinel)
}

func TestReadAtContextCanceled(t *testing.T) {
	// A non-EOF, non-ErrUnexpectedEOF error from the body reader path can
	// only arrive in ReadFull. We synthesise this with a reader whose Read
	// returns context.Canceled.
	data := []byte("abcdefgh")
	fr := &cancelingRanger{
		desc: oci.Descriptor{
			MediaType: testMediaType,
			Size:      int64(len(data)),
			Digest:    digest.FromBytes(data),
		},
	}
	r := blobra.New(context.Background(), fr, testRepo, fr.desc)
	buf := make([]byte, 4)
	n, err := r.ReadAt(buf, 0)
	assert.Equal(t, 0, n)
	assert.ErrorIs(t, err, context.Canceled)
	assert.True(t, fr.closed, "underlying BlobReader must be closed on error")
}

func TestReadAtDoesNotCallRangerForTrivialCases(t *testing.T) {
	data := []byte("abcd")
	fr := newFake(data)
	r := blobra.New(context.Background(), fr, testRepo, fr.desc)

	// len(p) == 0 must not issue a range request.
	n, err := r.ReadAt(nil, 0)
	require.NoError(t, err)
	assert.Equal(t, 0, n)

	// off >= size must not issue a range request.
	n, err = r.ReadAt(make([]byte, 4), int64(len(data)))
	assert.Equal(t, 0, n)
	assert.Equal(t, io.EOF, err)

	assert.Equal(t, 0, fr.calls, "no range request should be issued for trivial cases")
}

func TestReadAtClampsRangeUpperBound(t *testing.T) {
	// When the read crosses the end-of-blob, the ranger must be called
	// with offset1 == desc.Size, never beyond it.
	data := []byte("abcdefghij")
	fr := newFake(data)
	r := blobra.New(context.Background(), fr, testRepo, fr.desc)

	buf := make([]byte, 100)
	n, err := r.ReadAt(buf, 7)
	assert.Equal(t, 3, n)
	assert.Equal(t, io.EOF, err)
	assert.Equal(t, []byte("hij"), buf[:n])
	assert.Equal(t, int64(7), fr.gotOff0)
	assert.Equal(t, int64(10), fr.gotOff1)
}

func TestReadAtPassesCorrectArgsToRanger(t *testing.T) {
	data := []byte("abcdefghij")
	fr := newFake(data)
	r := blobra.New(context.Background(), fr, "some/repo", fr.desc)

	buf := make([]byte, 4)
	_, err := r.ReadAt(buf, 2)
	require.NoError(t, err)
	assert.Equal(t, "some/repo", fr.gotRepo)
	assert.Equal(t, fr.desc.Digest, fr.gotDigest)
	assert.Equal(t, int64(2), fr.gotOff0)
	assert.Equal(t, int64(6), fr.gotOff1)
}

// cancelingRanger returns a BlobReader whose Read returns context.Canceled
// on first call. Used to exercise the "other error" branch in ReadAt.
type cancelingRanger struct {
	desc   oci.Descriptor
	closed bool
}

func (c *cancelingRanger) GetBlobRange(ctx context.Context, repo string, dig oci.Digest, o0, o1 int64) (oci.BlobReader, error) {
	return &cancelingReader{parent: c, desc: c.desc}, nil
}

type cancelingReader struct {
	parent *cancelingRanger
	desc   oci.Descriptor
}

func (c *cancelingReader) Read(p []byte) (int, error) {
	return 0, context.Canceled
}

func (c *cancelingReader) Close() error {
	c.parent.closed = true
	return nil
}

func (c *cancelingReader) Descriptor() oci.Descriptor {
	return c.desc
}

// Compile-time checks: *Reader is an io.ReaderAt, and oci.Interface
// satisfies blobra.BlobRanger directly (no adapter needed).
var (
	_ io.ReaderAt       = (*blobra.Reader)(nil)
	_ blobra.BlobRanger = (oci.Interface)(nil)
)
