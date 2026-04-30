# gzipr

Package `gzipr` provides sequential gzip scanning and random-access decompressed reading via DEFLATE block-boundary checkpoints.

## The problem it solves

A gzip stream must normally be decompressed from the beginning to reach any given byte. For large OCI layers this makes random file access prohibitively expensive. `gzipr` solves this by recording the full DEFLATE decompressor state (bit buffer + 32 KB sliding-window history) at periodic block boundaries during an initial scan. Later, any byte range of the decompressed stream can be reached by seeking to the nearest checkpoint in the compressed stream and decompressing only forward from there.

## Two-phase usage

### Phase 1 — Scan (once per blob)

`Scan` makes a single sequential pass, decompressing to an `io.Writer` and emitting checkpoints at a configurable decompressed-byte interval (default: 1 MiB).

```go
import "github.com/docker/oci/ocifs/gzipr"

idx, err := gzipr.Scan(compressedReader, io.Discard)
// idx.Checkpoints holds the checkpoint sequence.
// idx.Size is the total decompressed length.
```

The `Index` is JSON-serializable; persist it alongside the blob to avoid re-scanning on every start.

### Phase 2 — Random-access reads

```go
// Build from a fresh scan:
reader, err := gzipr.NewReader(blobReaderAt, blobSize)

// Or rebuild from a persisted index (no I/O at construction time):
reader, err := gzipr.NewReaderWithIndex(blobReaderAt, idx, blobSize)
defer reader.Close()

buf := make([]byte, 4096)
n, err := reader.ReadAt(buf, decompressedOffset)
```

`ReadAt` finds the highest checkpoint at or before `decompressedOffset`, fetches only the compressed bytes between that checkpoint and the next, resumes the DEFLATE decompressor from the saved state, and discards forward to the exact byte.

## Key types

| Type | Purpose |
|------|---------|
| `Index` | Checkpoint sequence + total decompressed size. JSON-serializable. |
| `Checkpoint` | DEFLATE decompressor state at a block boundary: compressed offset (`In`), decompressed offset (`Out`), bit buffer (`B`, `NB`), sliding-window history (`Hist`). |
| `Reader` | `io.ReaderAt` + `io.Closer` over the decompressed stream. |

## Options

```go
gzipr.WithSpan(2 << 20)   // checkpoint every 2 MiB (default: 1 MiB)
gzipr.WithMaxReaders(4)   // allow up to 4 concurrent ReadAt calls (default: 8)
```

Smaller spans improve seek latency at the cost of more checkpoint memory (~32 KiB of sliding-window history per checkpoint).

## Concurrency

`Reader.ReadAt` is safe for concurrent use. Concurrent decompressions are bounded by `WithMaxReaders`; callers that exceed the cap block until a slot is available. `Close` unblocks any waiting callers with `ErrClosed`.

## Internal DEFLATE fork

`gzipr/internal/flate` is a fork of the Go standard library's `compress/flate` package extended with two entry points:

- `NewReaderCallback` — invokes a callback at each DEFLATE block boundary during `Scan`.
- `NewReaderResume` — resumes decompression from a saved `(Hist, B, NB)` state for `ReadAt`.

These additions are the only changes from the upstream stdlib code.
