# blobra

Package `blobra` adapts an OCI registry's range-request API to [`io.ReaderAt`](https://pkg.go.dev/io#ReaderAt), allowing decompressors (`gzipr`, `zstdr`) to fetch arbitrary byte ranges of a compressed layer blob without downloading the entire blob.

## How it works

Each `ReadAt(p, off)` call translates to a single `GetBlobRange` registry request for the half-open byte range `[off, off+len(p))`. The response body is read in full into `p`. No buffering or caching is performed — callers are responsible for requesting only the ranges they need.

## Usage

```go
import (
    "context"
    "github.com/docker/oci"
    "github.com/docker/oci/ocifs/blobra"
)

// desc is an oci.Descriptor with Digest, MediaType, and Size set.
ra := blobra.New(ctx, registry, "library/alpine", desc)

// ra now satisfies io.ReaderAt over the compressed blob.
buf := make([]byte, 512)
n, err := ra.ReadAt(buf, 1024) // fetches bytes [1024, 1536) from the registry
```

`blobra.New` performs no I/O. Registry requests are only issued by `ReadAt`.

## Key types

### `BlobRanger`

The narrow interface `blobra` requires from the registry. Any `oci.Interface` implementation satisfies it:

```go
type BlobRanger interface {
    GetBlobRange(ctx context.Context, repo string, digest oci.Digest, offset0, offset1 int64) (oci.BlobReader, error)
}
```

Using this interface rather than the full `oci.Interface` makes `blobra.Reader` easy to test with a small fake.

### `Reader`

`*Reader` implements `io.ReaderAt`. It also exposes `Size() int64` (from the descriptor) and `Descriptor() oci.Descriptor`.

## `io.ReaderAt` contract notes

- `ReadAt(p, off)` where `off >= desc.Size` returns `(0, io.EOF)` without issuing a request.
- A read that is clamped by end-of-blob (i.e. `off + len(p) > desc.Size`) returns `(n, io.EOF)` where `n < len(p)`. This signals a legitimate short read at end-of-blob.
- If the registry returns fewer bytes than the requested range, the error is surfaced as `io.ErrUnexpectedEOF` (never `io.EOF`), distinguishing a protocol violation from a legitimate end-of-blob.
