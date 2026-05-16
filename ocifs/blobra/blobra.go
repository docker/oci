// Package blobra adapts a range-capable OCI blob source to [io.ReaderAt].
//
// The package depends on [oci.Digest] and [oci.BlobReader] from the parent
// oci package, but intentionally does not depend on the sealed
// [oci.Interface] type. Instead it consumes the narrow [BlobRanger]
// interface, which any [oci.Interface] implementation satisfies and which
// test fakes can implement directly.
package blobra

import (
	"context"
	"fmt"
	"io"

	"github.com/docker/oci"
)

// Compile-time interface check.
var _ io.ReaderAt = (*Reader)(nil)

// BlobRanger is the subset of [oci.Interface] that [Reader] requires.
// Any [oci.Interface] implementation satisfies it.
//
// The range is half-open: [offset0, offset1). The response body contains
// exactly offset1-offset0 bytes when both endpoints are within the blob.
type BlobRanger interface {
	GetBlobRange(ctx context.Context, repo string, digest oci.Digest, offset0, offset1 int64) (oci.BlobReader, error)
}

// Reader serves [io.ReaderAt] calls against a single OCI blob using
// range requests against an underlying [BlobRanger].
type Reader struct {
	ctx    context.Context
	ranger BlobRanger
	repo   string
	desc   oci.Descriptor
}

// New returns a Reader that serves ReadAt calls via range requests on
// ranger. desc.Size is the blob size in bytes; it is used to detect
// end-of-blob without issuing a probe request. No I/O is performed by
// this constructor.
func New(ctx context.Context, ranger BlobRanger, repo string, desc oci.Descriptor) *Reader {
	return &Reader{
		ctx:    ctx,
		ranger: ranger,
		repo:   repo,
		desc:   desc,
	}
}

// Descriptor returns the descriptor that the Reader was constructed with.
func (r *Reader) Descriptor() oci.Descriptor {
	return r.desc
}

// Size returns the size of the underlying blob in bytes.
func (r *Reader) Size() int64 {
	return r.desc.Size
}

// ReadAt implements [io.ReaderAt]. See the package documentation for the
// full contract; in particular, a server-side truncation of a clamped
// range request surfaces as [io.ErrUnexpectedEOF] rather than [io.EOF]
// so that downstream consumers (gzipr, zstdr) cannot mistake a protocol
// violation for legitimate end-of-blob.
func (r *Reader) ReadAt(p []byte, off int64) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if off < 0 {
		return 0, fmt.Errorf("blobra: negative offset %d", off)
	}
	if off >= r.desc.Size {
		return 0, io.EOF
	}

	n := int64(len(p))
	if remaining := r.desc.Size - off; n > remaining {
		n = remaining
	}

	br, err := r.ranger.GetBlobRange(r.ctx, r.repo, r.desc.Digest, off, off+n)
	if err != nil {
		return 0, err
	}
	defer br.Close()

	if _, err := io.ReadFull(br, p[:n]); err != nil {
		// io.ReadFull maps a fully empty stream to io.EOF and a partial
		// stream to io.ErrUnexpectedEOF. Both indicate the registry
		// returned fewer bytes than the clamped range demanded — a
		// protocol violation. Surface it uniformly as
		// io.ErrUnexpectedEOF; never propagate io.EOF here, which is
		// reserved for off >= desc.Size.
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			return 0, io.ErrUnexpectedEOF
		}
		return 0, err
	}

	if int64(len(p)) > n {
		return int(n), io.EOF
	}
	return int(n), nil
}
