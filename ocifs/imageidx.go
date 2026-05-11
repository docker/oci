package ocifs

import (
	"encoding/json"
	"errors"
	"io"

	"github.com/docker/oci"
	"github.com/docker/oci/ocifs/gzipr"
	"github.com/docker/oci/ocifs/tarfs"
	"github.com/docker/oci/ocifs/zstdr"
)

// ErrIndexStale is returned by [NewWithIndex] when the manifest currently
// served by the registry no longer matches the persisted index (e.g.
// because the tag was re-pushed or a layer's digest changed). Callers are
// expected to fall back to [New] to rebuild the index.
var ErrIndexStale = errors.New("ocifs: image has changed since index was built")

// LayerIndex captures everything required to reconstruct one layer's
// random-access reader from the registry without re-scanning the
// compressed blob. Exactly one of GzipIndex or ZstdIndex is populated,
// matching the layer's MediaType.
type LayerIndex struct {
	Digest    oci.Digest    `json:"digest"`
	MediaType string        `json:"mediaType"`
	Size      int64         `json:"size"`
	GzipIndex *gzipr.Index  `json:"gzip,omitempty"`
	ZstdIndex *zstdr.Index  `json:"zstd,omitempty"`
	TarIndex  []tarfs.Entry `json:"tar"`
}

// ImageIndex bundles per-layer indices for a single OCI image.
//
// The index does NOT include the image config blob; that is re-fetched
// from the registry by [NewWithIndex].
type ImageIndex struct {
	Layers []LayerIndex `json:"layers"`
}

// Clone returns a deep copy of idx. Mutating the result does not affect idx
// or any FS that produced idx via ImageIndex.
func (idx *ImageIndex) Clone() *ImageIndex {
	if idx == nil {
		return nil
	}
	out := &ImageIndex{Layers: make([]LayerIndex, len(idx.Layers))}
	for i := range idx.Layers {
		out.Layers[i] = cloneLayerIndex(idx.Layers[i])
	}
	return out
}

// Encode writes the JSON encoding of idx to w.
func (idx *ImageIndex) Encode(w io.Writer) error {
	return json.NewEncoder(w).Encode(idx)
}

// DecodeImageIndex reads a JSON-encoded [ImageIndex] from r.
func DecodeImageIndex(r io.Reader) (*ImageIndex, error) {
	var idx ImageIndex
	if err := json.NewDecoder(r).Decode(&idx); err != nil {
		return nil, err
	}
	return &idx, nil
}

func cloneLayerIndex(in LayerIndex) LayerIndex {
	out := LayerIndex{
		Digest:    in.Digest,
		MediaType: in.MediaType,
		Size:      in.Size,
		GzipIndex: cloneGzipIndex(in.GzipIndex),
		ZstdIndex: cloneZstdIndex(in.ZstdIndex),
	}
	if in.TarIndex != nil {
		out.TarIndex = make([]tarfs.Entry, len(in.TarIndex))
		copy(out.TarIndex, in.TarIndex)
	}
	return out
}

func cloneGzipIndex(in *gzipr.Index) *gzipr.Index {
	if in == nil {
		return nil
	}
	out := &gzipr.Index{
		Size:        in.Size,
		Checkpoints: make([]*gzipr.Checkpoint, len(in.Checkpoints)),
	}
	for i, cp := range in.Checkpoints {
		if cp == nil {
			continue
		}
		cpCopy := *cp
		if cp.Hist != nil {
			cpCopy.Hist = make([]byte, len(cp.Hist))
			copy(cpCopy.Hist, cp.Hist)
		}
		out.Checkpoints[i] = &cpCopy
	}
	return out
}

func cloneZstdIndex(in *zstdr.Index) *zstdr.Index {
	if in == nil {
		return nil
	}
	out := &zstdr.Index{
		Size:   in.Size,
		Frames: make([]*zstdr.FrameCheckpoint, len(in.Frames)),
	}
	for i, fr := range in.Frames {
		if fr == nil {
			continue
		}
		frCopy := *fr
		out.Frames[i] = &frCopy
	}
	return out
}
