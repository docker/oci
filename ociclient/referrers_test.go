package ociclient_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/docker/oci"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"

	"github.com/docker/oci/ociclient"
	"github.com/docker/oci/ocidebug"
	"github.com/docker/oci/ocimem"
	"github.com/docker/oci/ociserver"
)

func TestReferrersFallback(t *testing.T) {
	ctx := context.Background()

	// Test that the client falls back to using the referrers tag API
	// when the referrers API is not enabled.
	srv := httptest.NewServer(ociserver.New(ocidebug.New(ocimem.New(), t.Logf), &ociserver.Options{
		DisableReferrersAPI: true,
	}))
	t.Cleanup(srv.Close)

	client := mustNewOCIClient(srv.URL, nil)

	const repo = "foo/bar"

	// Push a scratch config for all the manifests to refer to.
	config := pushScratchConfig(t, client, repo)

	// Push a manifest to refer to.
	subject := pushManifest(t, client, repo, "sometag", &oci.Manifest{
		MediaType: ocispec.MediaTypeImageManifest,
		Config:    withMediaType(config, "subject/mediatype"),
	}, ocispec.MediaTypeImageManifest)

	index := &ocispec.Index{
		MediaType: ocispec.MediaTypeImageIndex,
	}
	// Then push some manifests that refer to it and update the index at the same time.
	for i := range 5 {
		artifactType := fmt.Sprintf("referrer/%d", i)
		desc := pushManifest(t, client, repo, "", &oci.Manifest{
			MediaType: ocispec.MediaTypeImageManifest,
			Subject:   &subject,
			Config:    withMediaType(config, artifactType),
		}, ocispec.MediaTypeImageManifest)
		desc.ArtifactType = artifactType
		index.Manifests = append(index.Manifests, desc)
	}

	// Then push the index to the referrers tag.
	pushManifest(t, client, repo, strings.ReplaceAll(string(subject.Digest), ":", "-"), index, ocispec.MediaTypeImageIndex)

	// Then ask for the referrers.
	var got []oci.Descriptor
	for desc, err := range client.Referrers(ctx, repo, subject.Digest, nil) {
		require.NoError(t, err)
		got = append(got, desc)
	}
	require.Equal(t, index.Manifests, got)

	// Check that artifact type filtering still works OK.
	got = nil
	for desc, err := range client.Referrers(ctx, repo, subject.Digest, &oci.ReferrersParameters{ArtifactType: "referrer/2"}) {
		require.NoError(t, err)
		got = append(got, desc)
	}
	require.Equal(t, []oci.Descriptor{index.Manifests[2]}, got)
}

func withMediaType(desc oci.Descriptor, mediaType string) oci.Descriptor {
	desc.MediaType = mediaType
	return desc
}

func pushScratchConfig(t *testing.T, client oci.Interface, repo string) oci.Descriptor {
	content := []byte("{}")
	desc := ocispec.Descriptor{
		Digest: digest.FromBytes(content),
		Size:   int64(len(content)),
	}
	_, err := client.PushBlob(context.Background(), repo, desc, bytes.NewReader(content))
	require.NoError(t, err)
	return desc
}

func pushManifest(t *testing.T, client oci.Interface, repo, tag string, content any, mediaType string) oci.Descriptor {
	data, err := json.Marshal(content)
	require.NoError(t, err)
	var params *oci.PushManifestParameters
	if tag != "" {
		params = &oci.PushManifestParameters{
			Tags: []string{tag},
		}
	}
	desc, err := client.PushManifest(context.Background(), repo, data, mediaType, params)
	require.NoError(t, err)
	return desc
}

func mustNewOCIClient(srvURL string, opts *ociclient.Options) oci.Interface {
	if opts == nil {
		opts = new(ociclient.Options)
	}
	u, err := url.Parse(srvURL)
	if err != nil {
		panic(err)
	}
	opts.Insecure = u.Scheme == "http"
	client, err := ociclient.New(u.Host, opts)
	if err != nil {
		panic(err)
	}
	return client
}
