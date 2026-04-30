# tarfs

Package `tarfs` builds a read-only [`io/fs.FS`](https://pkg.go.dev/io/fs#FS) over a tar archive accessed via [`io.ReaderAt`](https://pkg.go.dev/io#ReaderAt). The filesystem is immutable after construction and safe for concurrent use.

## Two-phase usage

### Phase 1 — Index (once per archive)

`Index` makes a single sequential pass over the tar stream and records each entry's byte offset, normalized path, and header metadata. No file content is read.

```go
import "github.com/docker/oci/ocifs/tarfs"

entries, err := tarfs.Index(decompressedReader)
```

The `[]Entry` slice is JSON-serializable; persist it to avoid re-scanning on every start.

### Phase 2 — Serve reads

```go
// From a fresh scan (combines Index + NewFromEntries):
tfs, err := tarfs.New(decompressedReaderAt, -1)

// From a persisted index (no I/O at construction time):
tfs, err := tarfs.NewFromEntries(decompressedReaderAt, entries)

// Use as any io/fs.FS:
f, err := tfs.Open("etc/passwd")
data, err := fs.ReadFile(tfs, "usr/lib/libz.so.1")
entries, err := tfs.ReadDir("usr/bin")
```

File content is fetched on demand via `io.SectionReader` over the `io.ReaderAt`; the entry index records where in the decompressed stream each file's data begins.

## Key types

| Type | Purpose |
|------|---------|
| `FS` | The `io/fs.FS` implementation. Also implements `ReadDirFS`, `StatFS`, and `Lstat`. |
| `Entry` | One tar entry: normalized `Filename`, byte `Offset` in the decompressed stream, and a `Header` with metadata. |
| `Header` | Subset of `tar.Header` that is JSON-serializable with stable round-trip semantics. `ModTime` is stored as Unix nanoseconds. |

## Symlinks and hardlinks

- **Symlinks** are followed by `Open` and `Stat` up to 255 hops. Circular chains return `ErrSymlinkLoop`. `Lstat` does not follow a final-component symlink.
- **Hardlinks** are resolved to the target entry at construction time so that `DirEntry.Info` reports the real size and mode. A hardlink whose target is absent is excluded from directory listings.

## Synthetic directories

Tar archives do not always include explicit entries for every ancestor directory. `tarfs` synthesizes a minimal directory entry (`Mode: fs.ModeDir|0755`) for any ancestor implied by a file path but not present in the archive. Synthesized entries are excluded from the `Entries()` output used for index persistence.

## Security

Tar entries whose normalized path contains a `".."` component are silently dropped during both `Index` and `NewFromEntries`. This prevents [TarSlip/ZipSlip](https://security.snyk.io/research/zip-slip-vulnerability) path traversal: a crafted archive cannot inject `".."` entries into directory listings or cause reads outside the archive root.

## OCI whiteout markers

Entries whose base name is `.wh..wh..opq` (opaque whiteout) or starts with `.wh.` (per-file whiteout) are included in the path index so that `Open` succeeds on them, but they are excluded from `ReadDir` results. The higher-level `ocifs` overlay layer interprets these markers and removes the appropriate lower-layer entries.

## `io/fs` path conventions

Following the `io/fs` spec, all paths use `"."` as the root, never `"/"`. Pass `"."` to `Open`, `ReadDir`, or `fs.WalkDir` to address the root directory.
