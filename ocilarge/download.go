package ocilarge

import (
	"context"
	"fmt"
	"hash"
	"io"
	"sync"
	"time"

	"github.com/docker/oci"
	"github.com/opencontainers/go-digest"
)

// Tuning constants for the adaptive parallel download pipeline.
const (
	// probeSize is the size of the initial probe request used to measure
	// throughput and derive an optimal chunk size.
	probeSize int64 = 4 * 1024 * 1024 // 4 MB

	// targetChunkDuration is the ideal wall-clock time for a single chunk
	// download. Chunks are sized so that, at the observed bandwidth, each
	// one takes approximately this long. Short enough to get good
	// parallelism; long enough to amortise per-request overhead.
	targetChunkDuration = 2 * time.Second

	// minChunkSize / maxChunkSize clamp the adaptive chunk size so we
	// never create absurdly small or large requests.
	minChunkSize int64 = 4 * 1024 * 1024   // 4 MB
	maxChunkSize int64 = 256 * 1024 * 1024 // 256 MB

	// maxConcurrent is the number of parallel range-request goroutines.
	// This is also the prefetch depth: while the consumer reads chunk N,
	// chunks N+1 … N+maxConcurrent are already downloading.
	maxConcurrent = 6

	// maxRetries is the number of times a single chunk download is
	// retried before the whole operation is failed.
	maxRetries = 3
)

// DownloadLargeBlob downloads a blob using multiple concurrent HTTP range
// requests to saturate the available bandwidth. It returns a BlobReader
// whose Read calls yield the bytes in the correct order with full digest
// verification at EOF.
//
// Internally the function:
//  1. Resolves the blob to obtain its size and digest.
//  2. Issues a small "probe" range request and measures the throughput.
//  3. Derives an optimal chunk size from the observed bandwidth.
//  4. Launches a pipeline of concurrent fetchers that prefetch chunks into
//     an ordered cache so the next chunks are always ready when the caller
//     reads.
func DownloadLargeBlob(ctx context.Context, reg oci.Interface, repo string, dgst oci.Digest) (oci.BlobReader, error) {
	desc, err := reg.ResolveBlob(ctx, repo, dgst)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve blob: %w", err)
	}

	// Trivial blobs: just do a single GET.
	if desc.Size <= probeSize {
		return downloadSingle(ctx, reg, repo, dgst, desc)
	}

	// --- Phase 1: bandwidth probe -------------------------------------------
	probeEnd := probeSize
	if probeEnd > desc.Size {
		probeEnd = desc.Size
	}
	probeData, elapsed, err := timedRangeGet(ctx, reg, repo, dgst, 0, probeEnd)
	if err != nil {
		return nil, fmt.Errorf("bandwidth probe failed: %w", err)
	}

	// --- Phase 2: compute optimal chunk size --------------------------------
	chunkSize := deriveChunkSize(int64(len(probeData)), elapsed)

	// --- Phase 3: launch pipeline -------------------------------------------
	pr, pw := io.Pipe()

	go runPipeline(ctx, reg, repo, dgst, desc.Size, chunkSize, probeData, pw)

	return &blobReader{
		r:        pr,
		digester: desc.Digest.Algorithm().Hash(),
		desc:     desc,
		verify:   true,
	}, nil
}

// deriveChunkSize returns a chunk size (clamped) that should make each range
// request take roughly targetChunkDuration at the observed bandwidth.
func deriveChunkSize(probeBytes int64, elapsed time.Duration) int64 {
	if elapsed <= 0 {
		return maxChunkSize
	}
	bytesPerSec := float64(probeBytes) / elapsed.Seconds()
	cs := int64(bytesPerSec * targetChunkDuration.Seconds())

	if cs < minChunkSize {
		cs = minChunkSize
	}
	if cs > maxChunkSize {
		cs = maxChunkSize
	}
	return cs
}

// timedRangeGet fetches [start, end) and returns the data plus the wall-clock
// duration of the transfer (excluding connection setup overhead as much as
// possible by timing from first byte read to completion, but in practice we
// time the whole call for simplicity).
func timedRangeGet(ctx context.Context, reg oci.Interface, repo string, dgst oci.Digest, start, end int64) ([]byte, time.Duration, error) {
	t0 := time.Now()
	data, err := fetchRange(ctx, reg, repo, dgst, start, end)
	return data, time.Since(t0), err
}

// fetchRange downloads [start, end) from the registry with up to maxRetries
// attempts. It returns the raw bytes.
func fetchRange(ctx context.Context, reg oci.Interface, repo string, dgst oci.Digest, start, end int64) ([]byte, error) {
	size := end - start
	var lastErr error
	for attempt := range maxRetries {
		_ = attempt
		br, err := reg.GetBlobRange(ctx, repo, dgst, start, end)
		if err != nil {
			lastErr = err
			continue
		}
		data, err := io.ReadAll(io.LimitReader(br, size))
		closeErr := br.Close()
		if err != nil {
			lastErr = err
			continue
		}
		if closeErr != nil {
			lastErr = closeErr
			continue
		}
		if int64(len(data)) != size {
			lastErr = fmt.Errorf("short read: got %d bytes, want %d", len(data), size)
			continue
		}
		return data, nil
	}
	return nil, fmt.Errorf("failed after %d attempts: %w", maxRetries, lastErr)
}

// chunkResult holds the downloaded data for a single chunk.
type chunkResult struct {
	data []byte
	err  error
}

// runPipeline orchestrates the concurrent download and feeds the pipe writer
// with bytes in the correct order. It always closes pw (with or without error).
//
// Memory is bounded: at most maxConcurrent chunks are ever in flight or
// buffered at a time. A sliding window of fetcher goroutines advances as
// the writer drains each chunk, so completed data is written to the pipe
// (and freed) as soon as possible.
func runPipeline(
	ctx context.Context,
	reg oci.Interface,
	repo string,
	dgst oci.Digest,
	totalSize int64,
	chunkSize int64,
	probeData []byte,
	pw *io.PipeWriter,
) {
	defer pw.Close()

	// Probe data may cover more than one "chunk" if chunkSize < probeSize,
	// but we treat it as covering exactly the first probeLen bytes.
	probeLen := int64(len(probeData))

	// Write probe data first.
	if _, err := pw.Write(probeData); err != nil {
		pw.CloseWithError(err)
		return
	}

	// If the probe covered the whole blob, we're done.
	if probeLen >= totalSize {
		return
	}

	// Build the list of remaining byte-ranges to fetch.
	type rangeSpec struct {
		start int64
		end   int64
	}

	var chunks []rangeSpec
	offset := probeLen
	for offset < totalSize {
		end := offset + chunkSize
		if end > totalSize {
			end = totalSize
		}
		chunks = append(chunks, rangeSpec{start: offset, end: end})
		offset = end
	}
	numChunks := len(chunks)

	// Sliding window: we keep exactly maxConcurrent result channels alive
	// at any time. slots[i % maxConcurrent] holds the channel for chunk i.
	// Each channel is created when its fetcher is launched and consumed
	// (then nilled) when the writer drains it, so at most maxConcurrent
	// chunks' worth of data is ever resident in memory.
	window := min(maxConcurrent, numChunks)
	slots := make([]chan chunkResult, maxConcurrent)

	var wg sync.WaitGroup

	// launchFetcher starts a goroutine that downloads chunk i and deposits
	// the result into its slot.
	launchFetcher := func(i int) {
		ch := make(chan chunkResult, 1)
		slots[i%maxConcurrent] = ch
		wg.Add(1)
		go func() {
			defer wg.Done()
			data, err := fetchRange(ctx, reg, repo, dgst, chunks[i].start, chunks[i].end)
			ch <- chunkResult{data: data, err: err}
		}()
	}

	// Seed the window: launch the first batch of fetchers.
	for i := range window {
		launchFetcher(i)
	}

	// nextLaunch tracks the index of the next chunk to launch.
	nextLaunch := window

	// Drain chunks in order. As each chunk is consumed, launch the next
	// one (if any) so the window slides forward.
	for i := range numChunks {
		ch := slots[i%maxConcurrent]
		var cr chunkResult
		select {
		case <-ctx.Done():
			pw.CloseWithError(ctx.Err())
			wg.Wait()
			return
		case cr = <-ch:
		}

		// Free the slot immediately so the GC can reclaim the channel.
		slots[i%maxConcurrent] = nil

		if cr.err != nil {
			pw.CloseWithError(fmt.Errorf("chunk %d (offset %d): %w", i, chunks[i].start, cr.err))
			wg.Wait()
			return
		}

		// Write the data into the pipe, then let it be GC'd.
		if _, err := pw.Write(cr.data); err != nil {
			pw.CloseWithError(err)
			wg.Wait()
			return
		}
		cr.data = nil

		// Advance the window: launch the next fetcher if there is one.
		if nextLaunch < numChunks {
			launchFetcher(nextLaunch)
			nextLaunch++
		}
	}

	wg.Wait()
}

// downloadSingle handles small blobs that fit in a single request.
func downloadSingle(ctx context.Context, reg oci.Interface, repo string, dgst oci.Digest, desc oci.Descriptor) (oci.BlobReader, error) {
	data, err := fetchRange(ctx, reg, repo, dgst, 0, desc.Size)
	if err != nil {
		return nil, err
	}
	pr, pw := io.Pipe()
	go func() {
		_, writeErr := pw.Write(data)
		if writeErr != nil {
			pw.CloseWithError(writeErr)
			return
		}
		pw.Close()
	}()
	return &blobReader{
		r:        pr,
		digester: desc.Digest.Algorithm().Hash(),
		desc:     desc,
		verify:   true,
	}, nil
}

type blobReader struct {
	r        io.ReadCloser
	n        int64
	digester hash.Hash
	desc     oci.Descriptor
	verify   bool
}

func (r *blobReader) Descriptor() oci.Descriptor {
	return r.desc
}

func (r *blobReader) Read(buf []byte) (int, error) {
	n, err := r.r.Read(buf)
	r.n += int64(n)
	r.digester.Write(buf[:n])
	if err == nil {
		if r.n > r.desc.Size {
			// Fail early when the blob is too big; we can do that even
			// when we're not verifying for other use cases.
			return n, fmt.Errorf("blob size %d exceeds content length %d: %w", r.n, r.desc.Size, oci.ErrSizeInvalid)
		}
		return n, nil
	}
	if err != io.EOF {
		return n, err
	}
	if !r.verify {
		return n, io.EOF
	}
	if r.n != r.desc.Size {
		return n, fmt.Errorf("blob size mismatch (%d/%d): %w", r.n, r.desc.Size, oci.ErrSizeInvalid)
	}
	gotDigest := digest.NewDigest(r.desc.Digest.Algorithm(), r.digester)
	if gotDigest != r.desc.Digest {
		return n, fmt.Errorf("digest mismatch when reading blob")
	}
	return n, io.EOF
}

func (r *blobReader) Close() error {
	return r.r.Close()
}
