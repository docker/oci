package ocilayout

import (
	"path/filepath"
	"testing"

	"github.com/docker/oci/ocidigest"
	"github.com/docker/oci/ociref"
	"github.com/stretchr/testify/require"
)

func TestFindLayoutFallbackSplitsLastPathComponent(t *testing.T) {
	tests := []struct {
		name     string
		path     string
		baseDir  string
		wantRepo string
		wantTag  string
	}{{
		name:     "nested path",
		path:     "./foo/bar:baz",
		baseDir:  "./foo",
		wantRepo: "bar",
		wantTag:  "baz",
	}, {
		name:     "single component relative dot slash",
		path:     "./foo:bar",
		baseDir:  "./",
		wantRepo: "foo",
		wantTag:  "bar",
	}, {
		name:     "single component",
		path:     "foo:bar",
		baseDir:  ".",
		wantRepo: "foo",
		wantTag:  "bar",
	}}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			baseDir, ref, err := FindLayout(test.path)
			require.NoError(t, err)
			require.Equal(t, test.baseDir, baseDir)
			requireRef(t, ref, test.wantRepo, test.wantTag)
		})
	}
}

func TestFindLayoutUsesDeepestLayoutMarker(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	require.NoError(t, ensureLayout(filepath.Join("one", "two")))

	baseDir, ref, err := FindLayout("./one/two/three/four:tag")
	require.NoError(t, err)
	require.Equal(t, "./one/two", baseDir)
	requireRef(t, ref, "three/four", "tag")
}

func TestFindLayoutPrefersDeepestLayoutMarker(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	require.NoError(t, ensureLayout("one"))
	require.NoError(t, ensureLayout(filepath.Join("one", "two")))

	baseDir, ref, err := FindLayout("./one/two/three/four:tag")
	require.NoError(t, err)
	require.Equal(t, "./one/two", baseDir)
	requireRef(t, ref, "three/four", "tag")
}

func TestFindLayoutUsesCurrentLayoutMarker(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	require.NoError(t, ensureLayout("."))

	baseDir, ref, err := FindLayout("one/two:tag")
	require.NoError(t, err)
	require.Equal(t, ".", baseDir)
	requireRef(t, ref, "one/two", "tag")
}

func TestFindLayoutParsesDigestReference(t *testing.T) {
	digest := ocidigest.FromBytes([]byte("manifest"))

	baseDir, ref, err := FindLayout("./layout/repo:tag@" + digest.String())
	require.NoError(t, err)
	require.Equal(t, "./layout", baseDir)
	require.Empty(t, ref.Host)
	require.Equal(t, "repo", ref.Repository)
	require.Equal(t, "tag", ref.Tag)
	require.Equal(t, digest, ref.Digest)
}

func TestFindLayoutParsesDigestReferenceWithLayoutMarker(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	require.NoError(t, ensureLayout(filepath.Join("layout", "base")))
	digest := ocidigest.FromBytes([]byte("manifest"))

	baseDir, ref, err := FindLayout("./layout/base/repo/name:tag@" + digest.String())
	require.NoError(t, err)
	require.Equal(t, "./layout/base", baseDir)
	require.Empty(t, ref.Host)
	require.Equal(t, "repo/name", ref.Repository)
	require.Equal(t, "tag", ref.Tag)
	require.Equal(t, digest, ref.Digest)
}

func TestFindLayoutParsesDigestOnlyReference(t *testing.T) {
	digest := ocidigest.FromBytes([]byte("manifest"))

	baseDir, ref, err := FindLayout("./layout/repo@" + digest.String())
	require.NoError(t, err)
	require.Equal(t, "./layout", baseDir)
	require.Empty(t, ref.Host)
	require.Equal(t, "repo", ref.Repository)
	require.Empty(t, ref.Tag)
	require.Equal(t, digest, ref.Digest)
}

func TestFindLayoutReturnsReferenceErrors(t *testing.T) {
	_, _, err := FindLayout("./foo/Bad:tag")
	require.ErrorContains(t, err, "invalid reference syntax")

	_, _, err = FindLayout("")
	require.ErrorContains(t, err, "path must not be empty")
}

func requireRef(t *testing.T, ref ociref.Reference, repo, tag string) {
	t.Helper()
	require.Empty(t, ref.Host)
	require.Equal(t, repo, ref.Repository)
	require.Equal(t, tag, ref.Tag)
	require.Empty(t, ref.Digest)
}
