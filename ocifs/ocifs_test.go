package ocifs

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/klauspost/compress/zstd"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/docker/oci"
	"github.com/docker/oci/ocimem"
)

// tarFile describes a single entry to write into a synthetic test layer.
type tarFile struct {
	Name     string
	Linkname string
	Mode     int64
	Type     byte
	Body     string
}

// buildLayerTar serializes the supplied entries into a tar bytestream.
func buildLayerTar(t *testing.T, files []tarFile) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, f := range files {
		mode := f.Mode
		if mode == 0 {
			switch f.Type {
			case tar.TypeDir:
				mode = 0o755
			case tar.TypeSymlink:
				mode = 0o777
			default:
				mode = 0o644
			}
		}
		hdr := &tar.Header{
			Name:     f.Name,
			Linkname: f.Linkname,
			Mode:     mode,
			Typeflag: f.Type,
			Size:     int64(len(f.Body)),
			ModTime:  time.Unix(1700000000, 0).UTC(),
		}
		if f.Type == tar.TypeDir || f.Type == tar.TypeSymlink || f.Type == tar.TypeLink {
			hdr.Size = 0
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("tar header %q: %v", f.Name, err)
		}
		if hdr.Size > 0 {
			if _, err := tw.Write([]byte(f.Body)); err != nil {
				t.Fatalf("tar body %q: %v", f.Name, err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}
	return buf.Bytes()
}

// gzipBytes gzips data with default compression.
func gzipBytes(t *testing.T, data []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	if _, err := gw.Write(data); err != nil {
		t.Fatalf("gzip write: %v", err)
	}
	if err := gw.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	return buf.Bytes()
}

// zstdBytes encodes data as a zstd stream.
func zstdBytes(t *testing.T, data []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw, err := zstd.NewWriter(&buf)
	if err != nil {
		t.Fatalf("zstd writer: %v", err)
	}
	if _, err := zw.Write(data); err != nil {
		t.Fatalf("zstd write: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zstd close: %v", err)
	}
	return buf.Bytes()
}

// pushBlob pushes data to the registry as a blob and returns the descriptor.
func pushBlob(t *testing.T, ctx context.Context, reg oci.Interface, repo, mediaType string, data []byte) oci.Descriptor {
	t.Helper()
	desc := oci.Descriptor{
		MediaType: mediaType,
		Digest:    digest.FromBytes(data),
		Size:      int64(len(data)),
	}
	out, err := reg.PushBlob(ctx, repo, desc, bytes.NewReader(data))
	if err != nil {
		t.Fatalf("push blob: %v", err)
	}
	return out
}

// pushImage assembles a multi-layer image, pushes the layers, config and
// manifest, and returns the manifest digest.
func pushImage(t *testing.T, ctx context.Context, reg oci.Interface, repo, tag string, layers [][]tarFile) oci.Digest {
	t.Helper()
	return pushImageWithMediaType(t, ctx, reg, repo, tag, MediaTypeLayerOCIGzip, layers)
}

// pushImageWithMediaType is like pushImage but uses the supplied layer
// media type (and matching compressor) for every layer.
func pushImageWithMediaType(t *testing.T, ctx context.Context, reg oci.Interface, repo, tag, mediaType string, layers [][]tarFile) oci.Digest {
	t.Helper()
	layerDescs := make([]oci.Descriptor, len(layers))
	for i, files := range layers {
		raw := buildLayerTar(t, files)
		var compressed []byte
		switch mediaType {
		case MediaTypeLayerOCIGzip, MediaTypeLayerDockerGzip:
			compressed = gzipBytes(t, raw)
		case MediaTypeLayerOCIZstd:
			compressed = zstdBytes(t, raw)
		default:
			compressed = raw
		}
		layerDescs[i] = pushBlob(t, ctx, reg, repo, mediaType, compressed)
	}

	cfg := ocispec.Image{}
	cfgBytes, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	cfgDesc := pushBlob(t, ctx, reg, repo, ocispec.MediaTypeImageConfig, cfgBytes)

	mf := ocispec.Manifest{
		MediaType: MediaTypeOCIManifest,
		Config:    cfgDesc,
		Layers:    layerDescs,
	}
	mf.SchemaVersion = 2
	mfBytes, err := json.Marshal(mf)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	mDesc, err := reg.PushManifest(ctx, repo, mfBytes, MediaTypeOCIManifest, &oci.PushManifestParameters{Tags: []string{tag}})
	if err != nil {
		t.Fatalf("push manifest: %v", err)
	}
	return mDesc.Digest
}

func TestNew_SingleLayer(t *testing.T) {
	ctx := context.Background()
	reg := ocimem.New()
	pushImage(t, ctx, reg, "test/img", "v1", [][]tarFile{
		{
			{Name: "hello.txt", Type: tar.TypeReg, Body: "hello world"},
			{Name: "etc/", Type: tar.TypeDir},
			{Name: "etc/version", Type: tar.TypeReg, Body: "1.0"},
		},
	})

	f, err := New(ctx, reg, "test/img", "v1")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer f.Close()

	data, err := f.ReadFile("hello.txt")
	if err != nil {
		t.Fatalf("ReadFile hello.txt: %v", err)
	}
	if string(data) != "hello world" {
		t.Errorf("hello.txt = %q, want %q", data, "hello world")
	}

	data, err = f.ReadFile("etc/version")
	if err != nil {
		t.Fatalf("ReadFile etc/version: %v", err)
	}
	if string(data) != "1.0" {
		t.Errorf("etc/version = %q, want %q", data, "1.0")
	}
}

func TestNew_MultiLayerOverwrite(t *testing.T) {
	ctx := context.Background()
	reg := ocimem.New()
	pushImage(t, ctx, reg, "test/img", "v1", [][]tarFile{
		{
			{Name: "a.txt", Type: tar.TypeReg, Body: "lower"},
			{Name: "b.txt", Type: tar.TypeReg, Body: "lower-b"},
		},
		{
			{Name: "a.txt", Type: tar.TypeReg, Body: "upper"},
		},
	})
	f, err := New(ctx, reg, "test/img", "v1")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer f.Close()

	got, _ := f.ReadFile("a.txt")
	if string(got) != "upper" {
		t.Errorf("a.txt = %q, want upper", got)
	}
	got, _ = f.ReadFile("b.txt")
	if string(got) != "lower-b" {
		t.Errorf("b.txt = %q, want lower-b", got)
	}
}

func TestNew_Whiteout(t *testing.T) {
	ctx := context.Background()
	reg := ocimem.New()
	pushImage(t, ctx, reg, "test/img", "v1", [][]tarFile{
		{
			{Name: "delete-me.txt", Type: tar.TypeReg, Body: "old"},
			{Name: "keep.txt", Type: tar.TypeReg, Body: "kept"},
		},
		{
			{Name: ".wh.delete-me.txt", Type: tar.TypeReg, Body: ""},
		},
	})
	f, err := New(ctx, reg, "test/img", "v1")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer f.Close()

	_, err = f.Open("delete-me.txt")
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Open(delete-me.txt) err = %v, want ErrNotExist", err)
	}
	if _, err := f.ReadFile("keep.txt"); err != nil {
		t.Errorf("ReadFile(keep.txt): %v", err)
	}

	// ReadDir should not return the deleted file.
	entries, err := f.ReadDir(".")
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if e.Name() == "delete-me.txt" {
			t.Errorf("delete-me.txt should not appear in root listing")
		}
		if strings.HasPrefix(e.Name(), ".wh.") {
			t.Errorf("whiteout marker leaked into ReadDir: %q", e.Name())
		}
	}
}

func TestNew_OpaqueWhiteout(t *testing.T) {
	ctx := context.Background()
	reg := ocimem.New()
	pushImage(t, ctx, reg, "test/img", "v1", [][]tarFile{
		{
			{Name: "etc/", Type: tar.TypeDir},
			{Name: "etc/old1", Type: tar.TypeReg, Body: "1"},
			{Name: "etc/old2", Type: tar.TypeReg, Body: "2"},
		},
		{
			{Name: "etc/.wh..wh..opq", Type: tar.TypeReg},
			{Name: "etc/new", Type: tar.TypeReg, Body: "fresh"},
		},
	})
	f, err := New(ctx, reg, "test/img", "v1")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer f.Close()

	for _, p := range []string{"etc/old1", "etc/old2"} {
		_, err := f.Open(p)
		if !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("Open(%q) err = %v, want ErrNotExist", p, err)
		}
	}
	got, _ := f.ReadFile("etc/new")
	if string(got) != "fresh" {
		t.Errorf("etc/new = %q, want fresh", got)
	}
}

func TestNew_Symlink(t *testing.T) {
	ctx := context.Background()
	reg := ocimem.New()
	pushImage(t, ctx, reg, "test/img", "v1", [][]tarFile{
		{
			{Name: "usr/", Type: tar.TypeDir},
			{Name: "usr/lib/", Type: tar.TypeDir},
			{Name: "usr/lib/foo.so", Type: tar.TypeReg, Body: "binary"},
			{Name: "lib", Type: tar.TypeSymlink, Linkname: "usr/lib"},
		},
	})
	f, err := New(ctx, reg, "test/img", "v1")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer f.Close()

	got, err := f.ReadFile("lib/foo.so")
	if err != nil {
		t.Fatalf("ReadFile(lib/foo.so): %v", err)
	}
	if string(got) != "binary" {
		t.Errorf("lib/foo.so = %q, want binary", got)
	}
	// Lstat on the symlink itself should preserve ModeSymlink.
	fi, err := f.Lstat("lib")
	if err != nil {
		t.Fatalf("Lstat(lib): %v", err)
	}
	if fi.Mode()&fs.ModeSymlink == 0 {
		t.Errorf("Lstat(lib).Mode = %v, want ModeSymlink set", fi.Mode())
	}
}

func TestNew_FsTestPasses(t *testing.T) {
	ctx := context.Background()
	reg := ocimem.New()
	pushImage(t, ctx, reg, "test/img", "v1", [][]tarFile{
		{
			{Name: "a/", Type: tar.TypeDir},
			{Name: "a/b/", Type: tar.TypeDir},
			{Name: "a/b/c.txt", Type: tar.TypeReg, Body: "deep"},
			{Name: "top.txt", Type: tar.TypeReg, Body: "top"},
		},
		{
			{Name: "extra.txt", Type: tar.TypeReg, Body: "extra body"},
		},
	})
	f, err := New(ctx, reg, "test/img", "v1")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer f.Close()

	if err := fstest.TestFS(f, "a/b/c.txt", "top.txt", "extra.txt"); err != nil {
		t.Fatalf("fstest.TestFS: %v", err)
	}
}

func TestNew_UnsupportedMediaType(t *testing.T) {
	ctx := context.Background()
	reg := ocimem.New()
	repo := "test/img"
	tag := "v1"

	// Push a layer with an unsupported media type.
	raw := buildLayerTar(t, []tarFile{{Name: "x", Type: tar.TypeReg, Body: "x"}})
	layerDesc := pushBlob(t, ctx, reg, repo, "application/vnd.oci.image.layer.v1.tar", raw)
	cfg := ocispec.Image{}
	cfgBytes, _ := json.Marshal(cfg)
	cfgDesc := pushBlob(t, ctx, reg, repo, ocispec.MediaTypeImageConfig, cfgBytes)
	mf := ocispec.Manifest{
		MediaType: MediaTypeOCIManifest,
		Config:    cfgDesc,
		Layers:    []oci.Descriptor{layerDesc},
	}
	mf.SchemaVersion = 2
	mfBytes, _ := json.Marshal(mf)
	if _, err := reg.PushManifest(ctx, repo, mfBytes, MediaTypeOCIManifest, &oci.PushManifestParameters{Tags: []string{tag}}); err != nil {
		t.Fatalf("push manifest: %v", err)
	}

	if _, err := New(ctx, reg, repo, tag); !errors.Is(err, ErrUnsupportedMediaType) {
		t.Errorf("New err = %v, want ErrUnsupportedMediaType", err)
	}
}

func TestNew_BadTag(t *testing.T) {
	ctx := context.Background()
	reg := ocimem.New()
	pushImage(t, ctx, reg, "test/img", "v1", [][]tarFile{
		{{Name: "x", Type: tar.TypeReg, Body: "x"}},
	})
	if _, err := New(ctx, reg, "test/img", "no-such-tag"); err == nil {
		t.Errorf("New with bad tag should fail")
	}
}

func TestNew_ConfigBlob(t *testing.T) {
	ctx := context.Background()
	reg := ocimem.New()
	pushImage(t, ctx, reg, "test/img", "v1", [][]tarFile{
		{{Name: "x", Type: tar.TypeReg, Body: "x"}},
	})
	f, err := New(ctx, reg, "test/img", "v1")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer f.Close()

	cfg := f.ConfigBlob()
	if len(cfg) == 0 {
		t.Errorf("ConfigBlob is empty")
	}
	var m map[string]any
	if err := json.Unmarshal(cfg, &m); err != nil {
		t.Errorf("ConfigBlob is not valid JSON: %v", err)
	}
}

func TestNew_Close(t *testing.T) {
	ctx := context.Background()
	reg := ocimem.New()
	pushImage(t, ctx, reg, "test/img", "v1", [][]tarFile{
		{{Name: "x.txt", Type: tar.TypeReg, Body: "data"}},
	})
	f, err := New(ctx, reg, "test/img", "v1")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// A second Close is a no-op.
	if err := f.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}

	// Operations after Close return ErrFSClosed.
	_, err = f.Open("x.txt")
	if !errors.Is(err, ErrFSClosed) {
		t.Errorf("Open after Close err = %v, want ErrFSClosed", err)
	}
	_, err = f.ReadDir(".")
	if !errors.Is(err, ErrFSClosed) {
		t.Errorf("ReadDir after Close err = %v, want ErrFSClosed", err)
	}
	_, err = f.Stat("x.txt")
	if !errors.Is(err, ErrFSClosed) {
		t.Errorf("Stat after Close err = %v, want ErrFSClosed", err)
	}
	_, err = f.ReadFile("x.txt")
	if !errors.Is(err, ErrFSClosed) {
		t.Errorf("ReadFile after Close err = %v, want ErrFSClosed", err)
	}
	// ConfigBlob still works.
	if len(f.ConfigBlob()) == 0 {
		t.Errorf("ConfigBlob after Close should still work")
	}
}

func TestNewWithIndex_RoundTrip(t *testing.T) {
	ctx := context.Background()
	reg := ocimem.New()
	pushImage(t, ctx, reg, "test/img", "v1", [][]tarFile{
		{
			{Name: "a/", Type: tar.TypeDir},
			{Name: "a/b.txt", Type: tar.TypeReg, Body: "bee"},
		},
		{
			{Name: "c.txt", Type: tar.TypeReg, Body: "cee"},
		},
	})

	f, err := New(ctx, reg, "test/img", "v1")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	idx := f.ImageIndex()
	if len(idx.Layers) != 2 {
		t.Fatalf("idx.Layers = %d, want 2", len(idx.Layers))
	}
	for i, li := range idx.Layers {
		if li.GzipIndex == nil {
			t.Errorf("layer %d GzipIndex is nil", i)
		}
		if li.TarIndex == nil {
			t.Errorf("layer %d TarIndex is nil", i)
		}
	}
	f.Close()

	// Encode/decode round-trip.
	var buf bytes.Buffer
	if err := idx.Encode(&buf); err != nil {
		t.Fatalf("Encode: %v", err)
	}
	dec, err := DecodeImageIndex(&buf)
	if err != nil {
		t.Fatalf("DecodeImageIndex: %v", err)
	}

	// Wrap registry to count GetBlob calls.
	rec := &recordingReg{Interface: reg}
	f2, err := NewWithIndex(ctx, rec, "test/img", "v1", dec)
	if err != nil {
		t.Fatalf("NewWithIndex: %v", err)
	}
	defer f2.Close()

	// NewWithIndex must NOT call GetBlob for layers (only for the config).
	if rec.layerBlobReads != 0 {
		t.Errorf("NewWithIndex made %d layer GetBlob calls, want 0", rec.layerBlobReads)
	}

	got, err := f2.ReadFile("a/b.txt")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != "bee" {
		t.Errorf("a/b.txt = %q, want bee", got)
	}
	got, err = f2.ReadFile("c.txt")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != "cee" {
		t.Errorf("c.txt = %q, want cee", got)
	}
}

func TestImageIndex_DefensiveCopies(t *testing.T) {
	ctx := context.Background()
	reg := ocimem.New()
	pushImage(t, ctx, reg, "test/img", "v1", [][]tarFile{
		{{Name: "a.txt", Type: tar.TypeReg, Body: "unchanged"}},
	})

	f, err := New(ctx, reg, "test/img", "v1")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer f.Close()

	idx := f.ImageIndex()
	idx.Layers[0].TarIndex[0].Header.Size = 1
	idx.Layers[0].GzipIndex.Size = 1
	if len(idx.Layers[0].GzipIndex.Checkpoints) > 0 {
		idx.Layers[0].GzipIndex.Checkpoints[0].Out = 12345
	}

	got, err := f.ReadFile("a.txt")
	if err != nil {
		t.Fatalf("ReadFile after mutating ImageIndex result: %v", err)
	}
	if string(got) != "unchanged" {
		t.Fatalf("a.txt after mutating ImageIndex result = %q, want unchanged", got)
	}

	fresh := f.ImageIndex()
	if fresh.Layers[0].TarIndex[0].Header.Size != int64(len("unchanged")) {
		t.Fatalf("fresh ImageIndex tar size = %d, want %d", fresh.Layers[0].TarIndex[0].Header.Size, len("unchanged"))
	}
	if fresh.Layers[0].GzipIndex.Size == 1 {
		t.Fatalf("fresh ImageIndex reused mutated gzip index")
	}
}

func TestNewWithIndex_DefensivelyCopiesInput(t *testing.T) {
	ctx := context.Background()
	reg := ocimem.New()
	pushImage(t, ctx, reg, "test/img", "v1", [][]tarFile{
		{{Name: "a.txt", Type: tar.TypeReg, Body: "from-index"}},
	})

	f, err := New(ctx, reg, "test/img", "v1")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	idx := f.ImageIndex()
	f.Close()

	f2, err := NewWithIndex(ctx, reg, "test/img", "v1", idx)
	if err != nil {
		t.Fatalf("NewWithIndex: %v", err)
	}
	defer f2.Close()

	idx.Layers[0].TarIndex[0].Header.Size = 1
	idx.Layers[0].GzipIndex.Size = 1
	if len(idx.Layers[0].GzipIndex.Checkpoints) > 0 {
		idx.Layers[0].GzipIndex.Checkpoints[0].In = 12345
	}

	got, err := f2.ReadFile("a.txt")
	if err != nil {
		t.Fatalf("ReadFile after mutating NewWithIndex input: %v", err)
	}
	if string(got) != "from-index" {
		t.Fatalf("a.txt after mutating NewWithIndex input = %q, want from-index", got)
	}
}

func TestNewWithIndex_Stale(t *testing.T) {
	ctx := context.Background()
	reg := ocimem.New()
	pushImage(t, ctx, reg, "test/img", "v1", [][]tarFile{
		{{Name: "a.txt", Type: tar.TypeReg, Body: "first"}},
	})
	f, err := New(ctx, reg, "test/img", "v1")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	idx := f.ImageIndex()
	f.Close()

	// Re-push with different content under the same tag.
	pushImage(t, ctx, reg, "test/img", "v1", [][]tarFile{
		{{Name: "a.txt", Type: tar.TypeReg, Body: "second"}},
	})

	if _, err := NewWithIndex(ctx, reg, "test/img", "v1", idx); !errors.Is(err, ErrIndexStale) {
		t.Errorf("NewWithIndex err = %v, want ErrIndexStale", err)
	}
}

// recordingReg wraps an oci.Interface and counts GetBlob calls used to
// fetch any layer (i.e. anything with a tar+gzip or tar+zstd media type).
type recordingReg struct {
	oci.Interface
	layerBlobReads int
}

func (r *recordingReg) GetBlob(ctx context.Context, repo string, dgst oci.Digest) (oci.BlobReader, error) {
	br, err := r.Interface.GetBlob(ctx, repo, dgst)
	if err != nil {
		return nil, err
	}
	mt := br.Descriptor().MediaType
	switch mt {
	case MediaTypeLayerOCIGzip, MediaTypeLayerDockerGzip, MediaTypeLayerOCIZstd:
		r.layerBlobReads++
	}
	return br, nil
}

// TestNew_ZstdLayer pushes a single-layer image with a zstd-compressed
// layer and verifies that ocifs reads it correctly.
func TestNew_ZstdLayer(t *testing.T) {
	ctx := context.Background()
	reg := ocimem.New()
	pushImageWithMediaType(t, ctx, reg, "test/img", "v1", MediaTypeLayerOCIZstd, [][]tarFile{
		{
			{Name: "hello.txt", Type: tar.TypeReg, Body: "hello zstd"},
		},
	})
	f, err := New(ctx, reg, "test/img", "v1")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer f.Close()
	got, err := f.ReadFile("hello.txt")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(got) != "hello zstd" {
		t.Errorf("hello.txt = %q, want %q", got, "hello zstd")
	}
	// ImageIndex should record a ZstdIndex, not a GzipIndex.
	idx := f.ImageIndex()
	if len(idx.Layers) != 1 {
		t.Fatalf("layers = %d, want 1", len(idx.Layers))
	}
	if idx.Layers[0].ZstdIndex == nil {
		t.Errorf("ZstdIndex is nil")
	}
	if idx.Layers[0].GzipIndex != nil {
		t.Errorf("GzipIndex should be nil for a zstd layer")
	}
}

// SinglePassConstruction verifies that New makes exactly one GetBlob call
// per layer (in addition to the config blob fetch).
func TestNew_SinglePassConstruction(t *testing.T) {
	ctx := context.Background()
	reg := ocimem.New()
	pushImage(t, ctx, reg, "test/img", "v1", [][]tarFile{
		{{Name: "a", Type: tar.TypeReg, Body: "1"}},
		{{Name: "b", Type: tar.TypeReg, Body: "2"}},
		{{Name: "c", Type: tar.TypeReg, Body: "3"}},
	})
	rec := &recordingReg{Interface: reg}
	f, err := New(ctx, rec, "test/img", "v1")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer f.Close()
	if rec.layerBlobReads != 3 {
		t.Errorf("layer GetBlob calls = %d, want 3", rec.layerBlobReads)
	}
}
