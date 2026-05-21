package ocilarge

import (
	"context"
	"fmt"
	"io"

	"github.com/docker/oci"
	"github.com/docker/oci/ocidigest"
)

const defaultUploadChunkSize = 100 * 1024 * 1024 // 100 MB

// UploadLargeBlobParameters holds optional parameters for UploadLargeBlob.
type UploadLargeBlobParameters struct {
	// ChunkSize is the maximum number of bytes to upload at a time.
	// If zero or negative, a default of 100 MB is used.
	ChunkSize int

	// Algorithm is the digest algorithm used to commit the blob.
	// If zero, ocidigest.Canonical is used.
	Algorithm ocidigest.Algorithm
}

// UploadLargeBlob uploads a large blob in chunks with retries so that uploads can be resumed in case of network error
func UploadLargeBlob(ctx context.Context, reg oci.Interface, repo string, f io.ReadCloser, params *UploadLargeBlobParameters) (oci.Descriptor, error) {
	defer f.Close()
	chunkSize := defaultUploadChunkSize
	algorithm := ocidigest.Canonical
	if params != nil {
		if params.ChunkSize > 0 {
			chunkSize = params.ChunkSize
		}
		if params.Algorithm.String() != "" {
			algorithm = params.Algorithm
		}
	}

	bw, err := reg.PushBlobChunked(ctx, repo, chunkSize)
	if err != nil {
		return oci.Descriptor{}, fmt.Errorf("starting chunked upload: %w", err)
	}
	defer func() { _ = bw.Cancel() }() // no-op after a successful Commit

	buf := make([]byte, chunkSize)
	dgstr, err := algorithm.New()
	if err != nil {
		return oci.Descriptor{}, err
	}
	for {
		n, readErr := io.ReadFull(f, buf)
		if n > 0 {
			if _, err := dgstr.Write(buf[:n]); err != nil {
				return oci.Descriptor{}, err
			}

			var writeErr error
			for range 3 { // try writing each chunk three times
				_, writeErr = bw.Write(buf[:n])
				if writeErr == nil {
					break
				}
			}
			if writeErr != nil {
				return oci.Descriptor{}, fmt.Errorf("writing chunk: %w", writeErr)
			}
		}
		// io.ReadFull returns io.EOF when zero bytes were read (stream
		// already at EOF) and io.ErrUnexpectedEOF when it read some bytes
		// but fewer than len(buf) — i.e. the final short chunk.  In both
		// cases we are done reading.
		if readErr != nil {
			if readErr == io.EOF || readErr == io.ErrUnexpectedEOF {
				break
			}
			return oci.Descriptor{}, fmt.Errorf("reading source: %w", readErr)
		}
	}
	dgst, err := dgstr.Digest()
	if err != nil {
		return oci.Descriptor{}, err
	}
	return bw.Commit(dgst)
}
