package ocifs

import (
	"errors"
	"fmt"
	"strings"
)

// Manifest media types recognised by ocifs.
const (
	// MediaTypeOCIManifest is the OCI image manifest media type.
	MediaTypeOCIManifest = "application/vnd.oci.image.manifest.v1+json"
	// MediaTypeOCIIndex is the OCI image index media type. ocifs returns
	// ErrUnsupportedManifest when this is encountered (multi-platform
	// resolution is not implemented in v1).
	MediaTypeOCIIndex = "application/vnd.oci.image.index.v1+json"
	// MediaTypeDockerManifest is the Docker v2 manifest media type.
	MediaTypeDockerManifest = "application/vnd.docker.distribution.manifest.v2+json"
	// MediaTypeDockerManifestList is the Docker v2 manifest list media type.
	// ocifs returns ErrUnsupportedManifest when this is encountered.
	MediaTypeDockerManifestList = "application/vnd.docker.distribution.manifest.list.v2+json"
)

// Layer media types recognised by ocifs.
const (
	// MediaTypeLayerOCIGzip is the OCI gzipped layer media type.
	MediaTypeLayerOCIGzip = "application/vnd.oci.image.layer.v1.tar+gzip"
	// MediaTypeLayerDockerGzip is the Docker rootfs diff (gzipped) layer media type.
	MediaTypeLayerDockerGzip = "application/vnd.docker.image.rootfs.diff.tar.gzip"
	// MediaTypeLayerOCIZstd is the OCI zstd-compressed layer media type.
	MediaTypeLayerOCIZstd = "application/vnd.oci.image.layer.v1.tar+zstd"
)

// ErrUnsupportedMediaType is returned when a layer's MediaType is not one of
// the gzip/zstd variants supported by ocifs. Callers can test with
// [errors.Is].
var ErrUnsupportedMediaType = errors.New("ocifs: unsupported layer media type")

// ErrUnsupportedManifest is returned when the resolved manifest's MediaType
// is not a supported single-image manifest (e.g. an OCI image index).
var ErrUnsupportedManifest = errors.New("ocifs: unsupported manifest type")

// layerCompression identifies the compression algorithm used by a layer.
type layerCompression int

const (
	compressionNone layerCompression = iota
	compressionGzip
	compressionZstd
)

// classifyLayerMediaType returns the compression algorithm associated with
// mt, or compressionNone (and a wrapped ErrUnsupportedMediaType) if mt is
// not recognized.
func classifyLayerMediaType(mt string) (layerCompression, error) {
	switch mt {
	case MediaTypeLayerOCIGzip, MediaTypeLayerDockerGzip:
		return compressionGzip, nil
	case MediaTypeLayerOCIZstd:
		return compressionZstd, nil
	}
	return compressionNone, fmt.Errorf("%w: %s", ErrUnsupportedMediaType, mt)
}

const (
	// whiteoutPrefix is the prefix attached to the deleted file's name in an
	// OCI overlay whiteout entry.
	whiteoutPrefix = ".wh."
	// whiteoutOpaque is the special whiteout filename that hides every entry
	// in the parent directory below this layer.
	whiteoutOpaque = ".wh..wh..opq"
)

// whiteoutTarget returns the name of the entry being deleted by a whiteout
// marker. It assumes base has the whiteoutPrefix ".wh." prefix and is not
// the opaque marker.
func whiteoutTarget(base string) string {
	return strings.TrimPrefix(base, whiteoutPrefix)
}
