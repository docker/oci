// Package ocifs provides an io/fs.FS implementation backed by an OCI image.
//
// FS composes the per-layer io.ReaderAt readers built by the blobra, gzipr,
// zstdr, and tarfs sub-packages into a single overlay filesystem reflecting
// the merged contents of all image layers (with whiteout semantics).
package ocifs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync/atomic"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/docker/oci"
	"github.com/docker/oci/ocifs/blobra"
	"github.com/docker/oci/ocifs/gzipr"
	"github.com/docker/oci/ocifs/tarfs"
	"github.com/docker/oci/ocifs/zstdr"
	"github.com/docker/oci/ociref"
)

// ErrFSClosed is returned (wrapped in *fs.PathError) by Open/ReadDir/Stat/
// ReadFile after Close has been called on the FS.
var ErrFSClosed = errors.New("ocifs: filesystem has been closed")

// ErrSymlinkLoop is returned (wrapped in *fs.PathError) when a path
// resolution exceeds the per-call symlink hop budget (255).
var ErrSymlinkLoop = errors.New("ocifs: too many levels of symbolic links")

// LayerReader is the interface satisfied by both *gzipr.Reader and
// *zstdr.Reader. It is the only surface tarfs.FS and the overlay need
// from the decompressor.
type LayerReader interface {
	io.ReaderAt
	io.Closer
	Size() int64
}

// layerState bundles the per-layer readers built during New.
type layerState struct {
	desc      oci.Descriptor
	decompRA  LayerReader
	tarFS     *tarfs.FS
	gzipIndex *gzipr.Index
	zstdIndex *zstdr.Index
	tarIndex  []tarfs.Entry
}

// FS is an io/fs.FS over an OCI image's merged layer contents. It is
// constructed by [New] or [NewWithIndex] and is safe for concurrent use
// after construction. Call [FS.Close] to release decompressor pools.
type FS struct {
	layers []layerState
	index  overlayIndex
	dirs   overlayDirs
	config []byte
	closed atomic.Bool
}

// ConfigBlob returns the raw image config JSON. It is always available on
// a successfully constructed FS, including after Close. The returned slice
// is owned by the FS; callers must not modify it.
func (f *FS) ConfigBlob() []byte { return f.config }

// ImageIndex returns a snapshot of the per-layer indices that can be
// persisted via [ImageIndex.Encode] and reused with [NewWithIndex].
// The returned struct is a deep copy; callers may mutate it without
// affecting the FS.
func (f *FS) ImageIndex() *ImageIndex {
	out := &ImageIndex{Layers: make([]LayerIndex, len(f.layers))}
	for i, l := range f.layers {
		out.Layers[i] = cloneLayerIndex(LayerIndex{
			Digest:    l.desc.Digest,
			MediaType: l.desc.MediaType,
			Size:      l.desc.Size,
			GzipIndex: l.gzipIndex,
			ZstdIndex: l.zstdIndex,
			TarIndex:  l.tarIndex,
		})
	}
	return out
}

// Close releases decompressor reader pools. After Close, all Open/ReadDir/
// Stat/ReadFile calls return *fs.PathError wrapping ErrFSClosed. Close is
// idempotent and always returns nil; ConfigBlob and ImageIndex remain safe
// to call.
func (f *FS) Close() error {
	if !f.closed.CompareAndSwap(false, true) {
		return nil
	}
	for i := range f.layers {
		if f.layers[i].decompRA != nil {
			_ = f.layers[i].decompRA.Close()
		}
	}
	return nil
}

// New resolves the image at name@ref, downloads each layer once to build
// its checkpoint and tar entry indices, and returns a ready-to-use FS.
// The context is captured by the underlying blob readers and governs all
// subsequent registry range requests issued by file Read/ReadAt calls on
// the returned FS; cancel it to abort in-flight reads.
func New(ctx context.Context, reg oci.Interface, name string, ref string) (*FS, error) {
	manifestBytes, manifestMT, err := fetchManifest(ctx, reg, name, ref)
	if err != nil {
		return nil, err
	}
	manifest, err := decodeManifest(manifestBytes, manifestMT)
	if err != nil {
		return nil, err
	}
	configBytes, err := fetchConfig(ctx, reg, name, manifest.Config.Digest)
	if err != nil {
		return nil, err
	}

	layers, err := buildLayers(ctx, reg, name, manifest.Layers)
	if err != nil {
		return nil, err
	}

	return finalizeFS(configBytes, layers), nil
}

// NewWithIndex re-uses a previously persisted ImageIndex to skip layer
// scanning. The manifest is still re-fetched so the layer digests can be
// verified; if the digests do not match the persisted index, ErrIndexStale
// is returned and the caller should fall back to [New].
func NewWithIndex(ctx context.Context, reg oci.Interface, name string, ref string, idx *ImageIndex) (*FS, error) {
	if idx == nil {
		return nil, errors.New("ocifs: NewWithIndex: idx is nil")
	}
	idx = idx.Clone()
	manifestBytes, manifestMT, err := fetchManifest(ctx, reg, name, ref)
	if err != nil {
		return nil, err
	}
	manifest, err := decodeManifest(manifestBytes, manifestMT)
	if err != nil {
		return nil, err
	}
	if len(manifest.Layers) != len(idx.Layers) {
		return nil, ErrIndexStale
	}
	for i, ml := range manifest.Layers {
		li := idx.Layers[i]
		if ml.Digest != li.Digest {
			return nil, ErrIndexStale
		}
	}
	configBytes, err := fetchConfig(ctx, reg, name, manifest.Config.Digest)
	if err != nil {
		return nil, err
	}

	layers := make([]layerState, len(manifest.Layers))
	for i, ml := range manifest.Layers {
		li := idx.Layers[i]
		comp, err := classifyLayerMediaType(li.MediaType)
		if err != nil {
			return nil, err
		}
		if li.Size != ml.Size {
			return nil, ErrIndexStale
		}
		if li.MediaType != ml.MediaType {
			return nil, ErrIndexStale
		}
		if li.TarIndex == nil {
			return nil, ErrIndexStale
		}
		switch comp {
		case compressionGzip:
			if li.GzipIndex == nil || li.ZstdIndex != nil {
				return nil, ErrIndexStale
			}
			if li.GzipIndex.Size <= 0 {
				return nil, ErrIndexStale
			}
			if li.GzipIndex.Checkpoints == nil {
				return nil, ErrIndexStale
			}
			// Validate individual checkpoint fields so a tampered index
			// cannot trigger unbounded allocations or silent data corruption.
			for _, cp := range li.GzipIndex.Checkpoints {
				if cp == nil || cp.In < 0 || cp.In > ml.Size || cp.Out < 0 || cp.NB > 32 {
					return nil, ErrIndexStale
				}
			}
		case compressionZstd:
			if li.ZstdIndex == nil || li.GzipIndex != nil {
				return nil, ErrIndexStale
			}
			if li.ZstdIndex.Size <= 0 {
				return nil, ErrIndexStale
			}
			if li.ZstdIndex.Frames == nil {
				return nil, ErrIndexStale
			}
			for _, fr := range li.ZstdIndex.Frames {
				if fr == nil || fr.In < 0 || fr.In > ml.Size || fr.Out < 0 {
					return nil, ErrIndexStale
				}
			}
		}
		// Validate TarIndex entry fields to guard against tampered persisted data.
		for _, e := range li.TarIndex {
			if e.Offset < 0 || e.Header.Size < 0 {
				return nil, ErrIndexStale
			}
		}
		desc := oci.Descriptor{
			Digest:    li.Digest,
			MediaType: li.MediaType,
			Size:      ml.Size,
		}
		blobRA := blobra.New(ctx, reg, name, desc)
		var decompRA LayerReader
		switch comp {
		case compressionGzip:
			decompRA = gzipr.NewReaderWithIndex(blobRA, li.GzipIndex, ml.Size)
		case compressionZstd:
			decompRA = zstdr.NewReaderWithIndex(blobRA, li.ZstdIndex, ml.Size)
		default:
			// classifyLayerMediaType already returned an error for unknown
			// types, so this branch is unreachable in correct usage.
			return nil, fmt.Errorf("ocifs: layer %d: unsupported compression type", i)
		}
		tarFS, err := tarfs.NewFromEntries(decompRA, li.TarIndex)
		if err != nil {
			// Close the just-allocated decompressor and all prior ones
			// before returning to avoid leaking readers.
			if decompRA != nil {
				_ = decompRA.Close()
			}
			for j := 0; j < i; j++ {
				if layers[j].decompRA != nil {
					_ = layers[j].decompRA.Close()
				}
			}
			return nil, fmt.Errorf("ocifs: layer %d: %w", i, err)
		}
		layers[i] = layerState{
			desc:      desc,
			decompRA:  decompRA,
			tarFS:     tarFS,
			gzipIndex: li.GzipIndex,
			zstdIndex: li.ZstdIndex,
			tarIndex:  li.TarIndex,
		}
	}

	return finalizeFS(configBytes, layers), nil
}

// finalizeFS builds the overlay maps and assembles the final *FS.
func finalizeFS(configBytes []byte, layers []layerState) *FS {
	entriesByLayer := make([][]tarfs.Entry, len(layers))
	for i, l := range layers {
		entriesByLayer[i] = l.tarFS.Entries()
	}
	idx, dirs := buildOverlay(entriesByLayer)
	return &FS{
		layers: layers,
		index:  idx,
		dirs:   dirs,
		config: configBytes,
	}
}

// fetchManifest fetches the manifest by tag or digest and returns the raw
// JSON, the manifest's media type, and any error.
func fetchManifest(ctx context.Context, reg oci.Interface, name string, ref string) ([]byte, string, error) {
	var br oci.BlobReader
	var err error
	if ociref.IsValidDigest(ref) {
		br, err = reg.GetManifest(ctx, name, oci.Digest(ref))
	} else {
		br, err = reg.GetTag(ctx, name, ref)
	}
	if err != nil {
		return nil, "", err
	}
	defer br.Close()
	body, err := io.ReadAll(br)
	if err != nil {
		return nil, "", err
	}
	mt := br.Descriptor().MediaType
	return body, mt, nil
}

// decodeManifest parses the manifest payload and verifies that it is a
// supported single-image manifest.
func decodeManifest(body []byte, mt string) (*ocispec.Manifest, error) {
	// If the descriptor lacks a media type (e.g. some implementations don't
	// set it on tag responses), peek at the JSON itself.
	if mt == "" {
		var probe struct {
			MediaType string `json:"mediaType"`
		}
		_ = json.Unmarshal(body, &probe)
		mt = probe.MediaType
	}
	switch mt {
	case MediaTypeOCIIndex, MediaTypeDockerManifestList:
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedManifest, mt)
	case MediaTypeOCIManifest, MediaTypeDockerManifest, "":
		// Fall through: many registries omit the embedded mediaType field;
		// allow empty if the descriptor was also empty.
	default:
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedManifest, mt)
	}
	var m ocispec.Manifest
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, fmt.Errorf("ocifs: decode manifest: %w", err)
	}
	if m.Config.Digest == "" {
		return nil, fmt.Errorf("ocifs: manifest is missing config")
	}
	return &m, nil
}

// fetchConfig retrieves and buffers the image config blob.
func fetchConfig(ctx context.Context, reg oci.Interface, name string, dgst oci.Digest) ([]byte, error) {
	br, err := reg.GetBlob(ctx, name, dgst)
	if err != nil {
		return nil, err
	}
	defer br.Close()
	return io.ReadAll(br)
}

// buildLayers performs the single-pass layer construction loop.
func buildLayers(ctx context.Context, reg oci.Interface, name string, descs []oci.Descriptor) ([]layerState, error) {
	out := make([]layerState, 0, len(descs))
	for i, desc := range descs {
		layer, err := buildLayer(ctx, reg, name, desc)
		if err != nil {
			// Close any partial layers built so far.
			for _, l := range out {
				if l.decompRA != nil {
					_ = l.decompRA.Close()
				}
			}
			return nil, fmt.Errorf("ocifs: layer %d: %w", i, err)
		}
		out = append(out, layer)
	}
	return out, nil
}

// buildLayer performs single-pass construction for a single layer
// descriptor. It downloads the compressed blob exactly once via GetBlob,
// and pipes the decompressed stream to tarfs.Index while gzipr.Scan or
// zstdr.Scan emits checkpoints. On success it returns a fully-wired
// layerState whose decompRA is backed by blobra (random-access).
func buildLayer(ctx context.Context, reg oci.Interface, name string, desc oci.Descriptor) (layerState, error) {
	comp, err := classifyLayerMediaType(desc.MediaType)
	if err != nil {
		return layerState{}, err
	}

	br, err := reg.GetBlob(ctx, name, desc.Digest)
	if err != nil {
		return layerState{}, err
	}
	defer br.Close()

	pr, pw := io.Pipe()

	scanErr := make(chan error, 1)
	var (
		gzipIdx *gzipr.Index
		zstdIdx *zstdr.Index
	)
	go func() {
		var serr error
		switch comp {
		case compressionGzip:
			gzipIdx, serr = gzipr.Scan(br, pw)
		case compressionZstd:
			zstdIdx, serr = zstdr.Scan(br, pw)
		}
		pw.CloseWithError(serr)
		scanErr <- serr
	}()

	entries, indexErr := tarfs.Index(pr)
	if indexErr != nil {
		pr.CloseWithError(indexErr)
	} else {
		go func() {
			// Drain the pipe so the scanner goroutine is never
			// blocked. Errors are ignored: all outcomes are delivered
			// via the scanErr channel below.
			io.Copy(io.Discard, pr) //nolint:errcheck
			pr.Close()
		}()
	}
	scanErrVal := <-scanErr

	switch {
	case indexErr == nil && scanErrVal == nil:
		// success
	case indexErr == nil:
		return layerState{}, scanErrVal
	case scanErrVal == nil:
		return layerState{}, indexErr
	case errors.Is(scanErrVal, indexErr):
		return layerState{}, indexErr
	default:
		// Both sides failed independently. Prefer indexErr: the tar parse
		// error is typically the root cause (the scan error is often a
		// pipe-closed propagation of indexErr).
		return layerState{}, indexErr
	}

	// Build the random-access readers from the freshly-built indices.
	blobRA := blobra.New(ctx, reg, name, desc)
	var decompRA LayerReader
	switch comp {
	case compressionGzip:
		// Guard against an invalid/corrupt gzip index that would
		// otherwise panic inside NewReaderWithIndex.
		if gzipIdx == nil || gzipIdx.Size <= 0 || gzipIdx.Checkpoints == nil {
			return layerState{}, fmt.Errorf("ocifs: gzip layer has invalid decompression index")
		}
		decompRA = gzipr.NewReaderWithIndex(blobRA, gzipIdx, desc.Size)
	case compressionZstd:
		// Guard against an invalid/corrupt zstd index.
		if zstdIdx == nil || zstdIdx.Size <= 0 || zstdIdx.Frames == nil {
			return layerState{}, fmt.Errorf("ocifs: zstd layer has invalid decompression index")
		}
		decompRA = zstdr.NewReaderWithIndex(blobRA, zstdIdx, desc.Size)
	default:
		// Unreachable: classifyLayerMediaType already rejected unknown types.
		return layerState{}, fmt.Errorf("ocifs: unsupported compression type %d", comp)
	}
	tarFS, err := tarfs.NewFromEntries(decompRA, entries)
	if err != nil {
		_ = decompRA.Close()
		return layerState{}, err
	}
	return layerState{
		desc:      desc,
		decompRA:  decompRA,
		tarFS:     tarFS,
		gzipIndex: gzipIdx,
		zstdIndex: zstdIdx,
		tarIndex:  entries,
	}, nil
}
