package ocilayout

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"iter"
	"os"
	"path/filepath"
	"testing"

	"github.com/docker/oci"
	"github.com/docker/oci/ocidigest"
	"github.com/stretchr/testify/require"
)

func TestSharedLayoutPushAndRead(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	reg, err := New(dir, &Options{})
	require.NoError(t, err)
	require.NoFileExists(t, filepath.Join(dir, "oci-layout"))

	config := pushTestBlob(ctx, t, reg, "example/app", []byte("{}"))
	layer := pushTestBlob(ctx, t, reg, "example/app", []byte("layer"))
	manifest := testManifest(t, config, layer)
	desc, err := reg.PushManifest(ctx, "example/app", manifest, oci.MediaTypeImageManifest, &oci.PushManifestParameters{
		Tags: []string{"latest", "v1"},
	})
	require.NoError(t, err)

	require.FileExists(t, filepath.Join(dir, "oci-layout"))
	require.FileExists(t, filepath.Join(dir, "index.json"))
	require.FileExists(t, filepath.Join(dir, "blobs", desc.Digest.Algorithm().String(), desc.Digest.Encoded()))
	require.ElementsMatch(t, []string{"latest", "v1"}, all(t, reg.Tags(ctx, "example/app", nil)))
	require.Equal(t, []string{"example/app"}, all(t, reg.Repositories(ctx, "")))

	got, err := reg.ResolveTag(ctx, "example/app", "latest")
	require.NoError(t, err)
	require.Equal(t, desc.Digest, got.Digest)

	br, err := reg.GetBlob(ctx, "example/app", layer.Digest)
	require.NoError(t, err)
	data, err := io.ReadAll(br)
	require.NoError(t, err)
	require.NoError(t, br.Close())
	require.Equal(t, []byte("layer"), data)

	refs := indexRefs(t, filepath.Join(dir, "index.json"))
	require.ElementsMatch(t, []string{"example/app:latest", "example/app:v1"}, refs)
}

func TestSharedLayoutUntaggedManifest(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	reg, err := New(dir, &Options{})
	require.NoError(t, err)

	config := pushTestBlob(ctx, t, reg, "example/app", []byte("{}"))
	layer := pushTestBlob(ctx, t, reg, "example/app", []byte("layer"))
	desc, err := reg.PushManifest(ctx, "example/app", testManifest(t, config, layer), oci.MediaTypeImageManifest, nil)
	require.NoError(t, err)

	got, err := reg.ResolveManifest(ctx, "example/app", desc.Digest)
	require.NoError(t, err)
	require.Equal(t, desc.Digest, got.Digest)
	require.Equal(t, []string{"example/app@" + desc.Digest.String()}, indexRefs(t, filepath.Join(dir, "index.json")))
}

func TestSharedLayoutMultipleUntaggedManifests(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	reg, err := New(dir, &Options{})
	require.NoError(t, err)

	config := pushTestBlob(ctx, t, reg, "example/app", []byte("{}"))
	layer1 := pushTestBlob(ctx, t, reg, "example/app", []byte("layer1"))
	layer2 := pushTestBlob(ctx, t, reg, "example/app", []byte("layer2"))
	desc1, err := reg.PushManifest(ctx, "example/app", testManifest(t, config, layer1), oci.MediaTypeImageManifest, nil)
	require.NoError(t, err)
	desc2, err := reg.PushManifest(ctx, "example/app", testManifest(t, config, layer2), oci.MediaTypeImageManifest, nil)
	require.NoError(t, err)

	got, err := reg.ResolveManifest(ctx, "example/app", desc1.Digest)
	require.NoError(t, err)
	require.Equal(t, desc1.Digest, got.Digest)
	got, err = reg.ResolveManifest(ctx, "example/app", desc2.Digest)
	require.NoError(t, err)
	require.Equal(t, desc2.Digest, got.Digest)
	require.ElementsMatch(t, []string{
		"example/app@" + desc1.Digest.String(),
		"example/app@" + desc2.Digest.String(),
	}, indexRefs(t, filepath.Join(dir, "index.json")))
	require.Equal(t, []string{"example/app"}, all(t, reg.Repositories(ctx, "")))
}

func TestPushManifestAllowsMissingLayerWithURLs(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	reg, err := New(dir, &Options{})
	require.NoError(t, err)

	config := pushTestBlob(ctx, t, reg, "example/app", []byte("{}"))
	layer := oci.Descriptor{
		MediaType: "application/vnd.docker.image.rootfs.foreign.diff.tar.gzip",
		Digest:    ocidigest.FromBytes([]byte("foreign layer")),
		Size:      int64(len("foreign layer")),
		URLs:      []string{"https://example.com/foreign-layer"},
	}
	_, err = reg.PushManifest(ctx, "example/app", testManifest(t, config, layer), oci.MediaTypeImageManifest, nil)
	require.NoError(t, err)
}

func TestPushManifestRejectsMissingLayerWithoutURLs(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	reg, err := New(dir, &Options{})
	require.NoError(t, err)

	config := pushTestBlob(ctx, t, reg, "example/app", []byte("{}"))
	layer := oci.Descriptor{
		MediaType: "application/vnd.oci.image.layer.v1.tar+gzip",
		Digest:    ocidigest.FromBytes([]byte("missing layer")),
		Size:      int64(len("missing layer")),
	}
	_, err = reg.PushManifest(ctx, "example/app", testManifest(t, config, layer), oci.MediaTypeImageManifest, nil)
	require.ErrorIs(t, err, oci.ErrManifestInvalid)
	require.ErrorContains(t, err, "blob for layers[0] not found")
}

func TestDefaultRepoReadsTagOnlyRefs(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	reg, err := New(dir, &Options{DefaultRepo: "example/app"})
	require.NoError(t, err)

	config := pushTestBlob(ctx, t, reg, "example/app", []byte("{}"))
	layer := pushTestBlob(ctx, t, reg, "example/app", []byte("layer"))
	desc, err := reg.PushManifest(ctx, "example/app", testManifest(t, config, layer), oci.MediaTypeImageManifest, &oci.PushManifestParameters{
		Tags: []string{"latest"},
	})
	require.NoError(t, err)

	indexPath := filepath.Join(dir, "index.json")
	require.Equal(t, []string{"example/app:latest"}, indexRefs(t, indexPath))

	index := readIndex(t, indexPath)
	index.Manifests[0].Annotations[refNameAnnotation] = "latest"
	writeIndex(t, indexPath, index)

	reg, err = New(dir, &Options{DefaultRepo: "example/app"})
	require.NoError(t, err)
	got, err := reg.ResolveTag(ctx, "example/app", "latest")
	require.NoError(t, err)
	require.Equal(t, desc.Digest, got.Digest)
}

func TestPerRepositoryHelperUsesRepoDirectories(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	reg, err := NewPerRepository(dir, &PerRepoOptions{})
	require.NoError(t, err)

	config := pushTestBlob(ctx, t, reg, "example/app", []byte("{}"))
	layer := pushTestBlob(ctx, t, reg, "example/app", []byte("layer"))
	desc, err := reg.PushManifest(ctx, "example/app", testManifest(t, config, layer), oci.MediaTypeImageManifest, &oci.PushManifestParameters{
		Tags: []string{"latest"},
	})
	require.NoError(t, err)

	indexPath := filepath.Join(dir, "example", "app", "index.json")
	require.Equal(t, []string{"example/app:latest"}, indexRefs(t, indexPath))
	require.Equal(t, []string{"example/app"}, all(t, reg.Repositories(ctx, "")))

	index := readIndex(t, indexPath)
	index.Manifests[0].Annotations[refNameAnnotation] = "latest"
	writeIndex(t, indexPath, index)

	reg, err = NewPerRepository(dir, &PerRepoOptions{})
	require.NoError(t, err)
	got, err := reg.ResolveTag(ctx, "example/app", "latest")
	require.NoError(t, err)
	require.Equal(t, desc.Digest, got.Digest)
}

func TestSharedLayoutAmbiguousRefErrors(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	require.NoError(t, ensureLayout(dir))
	index := emptyIndex()
	index.Manifests = []oci.Descriptor{{
		MediaType: oci.MediaTypeImageManifest,
		Digest:    ocidigest.FromBytes([]byte("manifest")),
		Size:      int64(len("manifest")),
		Annotations: map[string]string{
			refNameAnnotation: "latest",
		},
	}}
	require.NoError(t, saveIndex(dir, index))

	reg, err := New(dir, &Options{})
	require.NoError(t, err)
	_, err = oci.All(reg.Repositories(ctx, ""))
	require.ErrorContains(t, err, "ambiguous ref.name")
}

func TestBlobCanBeReadWithoutManifestReachability(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	reg, err := New(dir, &Options{})
	require.NoError(t, err)

	config := pushTestBlob(ctx, t, reg, "example/app", []byte("{}"))
	reachable := pushTestBlob(ctx, t, reg, "example/app", []byte("reachable"))
	unreachable := pushTestBlob(ctx, t, reg, "example/app", []byte("unreachable"))
	_, err = reg.PushManifest(ctx, "example/app", testManifest(t, config, reachable), oci.MediaTypeImageManifest, &oci.PushManifestParameters{
		Tags: []string{"latest"},
	})
	require.NoError(t, err)

	br, err := reg.GetBlob(ctx, "example/app", unreachable.Digest)
	require.NoError(t, err)
	data, err := io.ReadAll(br)
	require.NoError(t, err)
	require.NoError(t, br.Close())
	require.Equal(t, []byte("unreachable"), data)
	require.Equal(t, unreachable.Digest, br.Descriptor().Digest)
	require.Equal(t, unreachable.Size, br.Descriptor().Size)
}

func TestChunkedUploadUsesUploadsDirectory(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	reg, err := New(dir, &Options{})
	require.NoError(t, err)

	w, err := reg.PushBlobChunked(ctx, "example/app", 0)
	require.NoError(t, err)
	_, err = w.Write([]byte("content"))
	require.NoError(t, err)
	id := w.ID()
	require.FileExists(t, filepath.Join(dir, "blobs", "uploads", id))
	desc, err := w.Commit(ocidigest.FromBytes([]byte("content")))
	require.NoError(t, err)
	require.NoFileExists(t, filepath.Join(dir, "blobs", "uploads", id))
	require.FileExists(t, filepath.Join(dir, "blobs", desc.Digest.Algorithm().String(), desc.Digest.Encoded()))
}

func pushTestBlob(ctx context.Context, t *testing.T, reg oci.Interface, repo string, data []byte) oci.Descriptor {
	t.Helper()
	desc := oci.Descriptor{
		MediaType: "application/octet-stream",
		Digest:    ocidigest.FromBytes(data),
		Size:      int64(len(data)),
	}
	got, err := reg.PushBlob(ctx, repo, desc, bytes.NewReader(data))
	require.NoError(t, err)
	return got
}

func testManifest(t *testing.T, config oci.Descriptor, layer oci.Descriptor) []byte {
	t.Helper()
	config.MediaType = oci.MediaTypeImageConfig
	m := oci.IndexOrManifest{
		SchemaVersion: 2,
		MediaType:     oci.MediaTypeImageManifest,
		Config:        &config,
		Layers:        []oci.Descriptor{layer},
	}
	data, err := json.Marshal(m)
	require.NoError(t, err)
	return data
}

func indexRefs(t *testing.T, path string) []string {
	t.Helper()
	index := readIndex(t, path)
	refs := make([]string, 0, len(index.Manifests))
	for _, desc := range index.Manifests {
		refs = append(refs, desc.Annotations[refNameAnnotation])
	}
	return refs
}

func readIndex(t *testing.T, path string) oci.IndexOrManifest {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var index oci.IndexOrManifest
	require.NoError(t, json.Unmarshal(data, &index))
	return index
}

func writeIndex(t *testing.T, path string, index oci.IndexOrManifest) {
	t.Helper()
	data, err := json.MarshalIndent(index, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, append(data, '\n'), 0o644))
}

func all[T any](t *testing.T, seq iter.Seq2[T, error]) []T {
	t.Helper()
	xs, err := oci.All(seq)
	require.NoError(t, err)
	return xs
}
