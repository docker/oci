# ocifs

Package `ocifs` provides an [`io/fs.FS`](https://pkg.go.dev/io/fs#FS) backed by an OCI image. It downloads each layer once, builds compressed-stream and tar indices, and then serves any file in the merged image as a random-access read — without ever unpacking layers to disk.

## How it works

An OCI image is a stack of compressed tar layers. `ocifs` composes four sub-packages into a single overlay filesystem:

```
Registry
  └─ blobra    — range-request io.ReaderAt over each compressed layer blob
       └─ gzipr / zstdr  — checkpoint index → random-access decompressed io.ReaderAt
            └─ tarfs     — tar entry index → io/fs.FS over the decompressed stream
                 └─ ocifs (overlay)  — merges all layers with OCI whiteout semantics
```

On the first call to `New`, every layer blob is streamed once: the decompressor builds its checkpoint index while the tar scanner builds its entry index. Subsequent `Open` / `ReadAt` calls fetch only the compressed bytes covering the requested file — no full-layer downloads.

## Quick start

```go
import (
    "context"
    "io/fs"

    "github.com/docker/oci"
    "github.com/docker/oci/ocifs"
)

reg := oci.New(/* ... */)
ociFS, err := ocifs.New(ctx, reg, "library/alpine", "latest")
if err != nil {
    return err
}
defer ociFS.Close()

// Walk the entire filesystem (root is always ".", never "/").
fs.WalkDir(ociFS, ".", func(path string, d fs.DirEntry, err error) error {
    fmt.Println(path)
    return err
})

// Read a single file.
data, err := ociFS.ReadFile("etc/os-release")
```

## Persisting the index

Scanning large layers on every startup is expensive. Save the index after the first `New` call and reuse it with `NewWithIndex`:

```go
// First run: build and persist.
ociFS, _ := ocifs.New(ctx, reg, repo, ref)
idx := ociFS.ImageIndex()
f, _ := os.Create("index.json")
idx.Encode(f)

// Subsequent runs: restore from disk.
f, _ := os.Open("index.json")
idx, _ := ocifs.DecodeImageIndex(f)
ociFS, err := ocifs.NewWithIndex(ctx, reg, repo, ref, idx)
if errors.Is(err, ocifs.ErrIndexStale) {
    // Image was re-pushed; fall back to full scan.
    ociFS, err = ocifs.New(ctx, reg, repo, ref)
}
```

`NewWithIndex` re-fetches the manifest to verify that layer digests still match the persisted index before accepting it. If the tag was re-pushed or a layer changed, `ErrIndexStale` is returned.

## Key types

| Type | Purpose |
|------|---------|
| `FS` | The `io/fs.FS` implementation. Also implements `ReadDirFS`, `StatFS`, `ReadFileFS`, and `Lstat`. |
| `ImageIndex` | Serializable bundle of per-layer checkpoint and tar indices. |
| `LayerIndex` | Per-layer index: digest, media type, compressed size, decompressor index, tar entry list. |

## Overlay semantics

Layers are merged bottom-to-top following the [OCI image layer spec](https://github.com/opencontainers/image-spec/blob/main/layer.md):

- **Whiteout** (`.wh.<name>`): deletes `<name>` from lower layers.
- **Opaque whiteout** (`.wh..wh..opq`): hides the entire directory contents from lower layers, keeping only what the current layer adds.
- **Hardlinks**: resolved to the target entry's content at open time.
- **Symlinks**: followed up to 255 hops; circular chains return `ErrSymlinkLoop`.

Whiteout markers are excluded from `ReadDir` results. Whiteout targets that are `"."`, `".."`, or contain `/` are silently ignored.

## Supported layer types

| Media type | Decompressor |
|-----------|-------------|
| `application/vnd.oci.image.layer.v1.tar+gzip` | `gzipr` |
| `application/vnd.docker.image.rootfs.diff.tar.gzip` | `gzipr` |
| `application/vnd.oci.image.layer.v1.tar+zstd` | `zstdr` |

## Concurrency

`FS` is safe for concurrent use after construction. Each `Open` call may issue range requests to the registry; the context passed to `New` governs all such requests. Call `Close` to release decompressor pools when the `FS` is no longer needed.
