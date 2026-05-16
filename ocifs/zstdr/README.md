# zstdr

Package `zstdr` provides sequential zstd scanning and random-access decompressed reading via frame-boundary checkpoints.

## The problem it solves

A zstd stream is a sequence of independently decompressible frames. Each frame can be decompressed from its magic bytes without any prior context, making random access straightforward: record the compressed and decompressed byte offset of every frame, and any byte range of the decompressed stream can be reached by seeking to the nearest frame start and decompressing only that frame.

## Two-phase usage

### Phase 1 — Scan (once per blob)

`Scan` makes a single sequential pass over the zstd stream, decompressing to an `io.Writer` and recording one `FrameCheckpoint` per non-skippable frame.

```go
import "github.com/docker/oci/ocifs/zstdr"

idx, err := zstdr.Scan(compressedReader, io.Discard)
// idx.Frames holds one entry per frame.
// idx.Size is the total decompressed length.
```

Skippable frames (magic `0x184D2A5x`) are consumed and discarded; they do not appear in the index. The `Index` is JSON-serializable; persist it to avoid re-scanning.

### Phase 2 — Random-access reads

```go
// Build from a fresh scan:
reader, err := zstdr.NewReader(blobReaderAt, blobSize)

// Or rebuild from a persisted index (no I/O at construction time):
reader := zstdr.NewReaderWithIndex(blobReaderAt, idx, blobSize)
defer reader.Close()

buf := make([]byte, 4096)
n, err := reader.ReadAt(buf, decompressedOffset)
```

`ReadAt` finds the highest frame whose decompressed offset is at or before the requested position, fetches the compressed bytes for that frame, decompresses the whole frame into memory, then copies the requested slice out.

## Key types

| Type | Purpose |
|------|---------|
| `Index` | Frame checkpoint sequence + total decompressed size. JSON-serializable. |
| `FrameCheckpoint` | Compressed offset (`In`) and decompressed offset (`Out`) of a frame's first byte. |
| `Reader` | `io.ReaderAt` + `io.Closer` over the decompressed stream. |

## Options

```go
zstdr.WithMaxReaders(4) // allow up to 4 concurrent ReadAt calls (default: 8)
```

Unlike gzip checkpoints, zstd frame checkpoints carry no per-checkpoint memory overhead (no sliding-window history). The trade-off between seek granularity and memory is governed by how the zstd encoder chose frame sizes when the blob was created.

## Concurrency

`Reader.ReadAt` is safe for concurrent use. Each in-flight `ReadAt` acquires a slot from a bounded pool (size = `WithMaxReaders`) and obtains a `*zstd.Decoder` from a `sync.Pool`. Callers that exceed the concurrency cap block until a slot is returned. `Close` unblocks waiting callers with `ErrClosed`.

## Comparison with gzipr

| | `gzipr` | `zstdr` |
|--|---------|---------|
| Resume granularity | Configurable (default 1 MiB) | One frame (encoder-defined) |
| Per-checkpoint cost | ~32 KiB (sliding window) | 16 bytes |
| Decompression on ReadAt | Partial frame only | Whole frame |
| Standard | RFC 1952 + RFC 1951 | RFC 8878 |
