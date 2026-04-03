package ocilarge

import (
	"context"
	"fmt"
	"io"

	"github.com/docker/oci"
	"github.com/opencontainers/go-digest"
)

// UploadLargeBlob uploads a large blob in chunks with retries so that uploads can be resumed in case of network error
func UploadLargeBlob(ctx context.Context, reg oci.Interface, repo string, f io.ReadCloser, chunkSize int) (oci.Descriptor, error) {
	defer f.Close()
	if chunkSize <= 0 {
		chunkSize = 100 * 1024 * 1024 // 100 MB
	}
	bw, err := reg.PushBlobChunked(ctx, repo, chunkSize)
	if err != nil {
		return oci.Descriptor{}, fmt.Errorf("starting chunked upload: %w", err)
	}
	defer func() { _ = bw.Cancel() }() // no-op after a successful Commit

	buf := make([]byte, chunkSize)
	dgstr := digest.Canonical.Digester()
	for {
		n, readErr := io.ReadFull(f, buf)
		if n > 0 {
			dgstr.Hash().Write(buf[:n])

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
	return bw.Commit(dgstr.Digest())
}
