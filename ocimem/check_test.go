package ocimem

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/docker/oci"
	"github.com/docker/oci/ocitest"
	"github.com/stretchr/testify/require"
)

var pushManifestTests = []struct {
	testName     string
	preload      ocitest.RepoContent
	config       Config
	tag          string
	mediaType    string
	manifestData func(content ocitest.PushedRepoContent) []byte
	wantError    string
}{{
	testName:  "NonExistentConfigReference",
	mediaType: oci.MediaTypeImageManifest,
	manifestData: func(ocitest.PushedRepoContent) []byte {
		return mustJSONMarshal(oci.IndexOrManifest{
			MediaType: oci.MediaTypeImageManifest,
			Config: ref(oci.Descriptor{
				MediaType: "application/something",
				Size:      1,
				Digest:    ocitest.DigestRef("a"),
			}),
		})
	},
	wantError: `invalid manifest: blob for config not found`,
}, {
	testName: "NonExistentLayerReference",
	preload: ocitest.RepoContent{
		Blobs: map[string]string{
			"a": "{}",
		},
	},
	mediaType: oci.MediaTypeImageManifest,
	manifestData: func(content ocitest.PushedRepoContent) []byte {
		return mustJSONMarshal(oci.IndexOrManifest{
			MediaType: oci.MediaTypeImageManifest,
			Config:    ref(content.Blobs["a"]),
			Layers: []oci.Descriptor{{
				MediaType: "application/something",
				Size:      1,
				Digest:    ocitest.DigestRef("b"),
			}},
		})
	},
	wantError: `invalid manifest: blob for layers\[0\] not found`,
}, {
	testName: "NonExistentSubjectReference",
	preload: ocitest.RepoContent{
		Blobs: map[string]string{
			"a": "{}",
		},
	},
	mediaType: oci.MediaTypeImageManifest,
	manifestData: func(content ocitest.PushedRepoContent) []byte {
		return mustJSONMarshal(oci.IndexOrManifest{
			MediaType: oci.MediaTypeImageManifest,
			Config:    ref(content.Blobs["a"]),
			Subject: &oci.Descriptor{
				MediaType: "application/something",
				Size:      1,
				Digest:    ocitest.DigestRef("b"),
			},
		})
	},
	// Non-existent subject references are explicitly allowed.
}, {
	testName:  "NonExistentImageIndexManifestReference",
	mediaType: oci.MediaTypeImageIndex,
	manifestData: func(content ocitest.PushedRepoContent) []byte {
		return mustJSONMarshal(oci.IndexOrManifest{
			MediaType: oci.MediaTypeImageIndex,
			Manifests: []oci.Descriptor{{
				MediaType: oci.MediaTypeImageManifest,
				Size:      1,
				Digest:    ocitest.DigestRef("a"),
			}},
		})
	},
	wantError: `invalid manifest: manifest for manifests\[0\] not found`,
}, {
	testName: "LaxChildReferences_NonExistentReference",
	preload: ocitest.RepoContent{
		Blobs: map[string]string{
			"a": "{}",
		},
	},
	config: Config{
		LaxChildReferences: true,
	},
	mediaType: oci.MediaTypeImageManifest,
	manifestData: func(content ocitest.PushedRepoContent) []byte {
		return mustJSONMarshal(oci.IndexOrManifest{
			MediaType: oci.MediaTypeImageManifest,
			Config:    ref(content.Blobs["a"]),
			Layers: []oci.Descriptor{{
				MediaType: "application/something",
				Size:      1,
				Digest:    ocitest.DigestRef("b"),
			}},
		})
	},
}, {
	testName:  "NonExistentImageIndexSubjectReference",
	mediaType: oci.MediaTypeImageIndex,
	manifestData: func(content ocitest.PushedRepoContent) []byte {
		return mustJSONMarshal(oci.IndexOrManifest{
			MediaType: oci.MediaTypeImageIndex,
			Subject: &oci.Descriptor{
				MediaType: "application/something",
				Size:      1,
				Digest:    ocitest.DigestRef("b"),
			},
		})
	},
	// Non-existent subject references are explicitly allowed.
}, {
	testName: "CannotOverwriteTagWhenImmutabilityEnabled",
	preload: ocitest.RepoContent{
		Blobs: map[string]string{
			"a": "{}",
			"b": "other",
		},
		Manifests: map[string]oci.IndexOrManifest{
			"m": {
				MediaType: oci.MediaTypeImageManifest,
				Config: ref(oci.Descriptor{
					Digest: ocitest.DigestRef("a"),
				}),
				Layers: []oci.Descriptor{{
					Digest: ocitest.DigestRef("a"),
				}},
			},
		},
		Tags: map[string]string{
			"sometag": "m",
		},
	},
	config: Config{
		ImmutableTags: true,
	},
	mediaType: oci.MediaTypeImageManifest,
	tag:       "sometag",
	manifestData: func(content ocitest.PushedRepoContent) []byte {
		return mustJSONMarshal(oci.IndexOrManifest{
			MediaType: oci.MediaTypeImageManifest,
			Config:    ref(content.Blobs["a"]),
			Layers:    []oci.Descriptor{content.Blobs["a"]},
			Annotations: map[string]string{
				"different": "thing",
			},
		})
	},
	wantError: `denied: requested access to the resource is denied: cannot overwrite tag`,
}, {
	testName: "CanRewriteTagWithIdenticalContentsWhenImmutabilityEnabled",
	preload: ocitest.RepoContent{
		Blobs: map[string]string{
			"a": "{}",
			"b": "other",
		},
		Manifests: map[string]oci.IndexOrManifest{
			"m": {
				MediaType: oci.MediaTypeImageManifest,
				Config: ref(oci.Descriptor{
					Digest: ocitest.DigestRef("a"),
				}),
				Layers: []oci.Descriptor{{
					Digest: ocitest.DigestRef("a"),
				}},
			},
		},
		Tags: map[string]string{
			"sometag": "m",
		},
	},
	config: Config{
		ImmutableTags: true,
	},
	mediaType: oci.MediaTypeImageManifest,
	tag:       "sometag",
	manifestData: func(content ocitest.PushedRepoContent) []byte {
		return content.ManifestData["m"]
	},
}, {
	testName: "CannotRewriteTagWithIdenticalContentsButDifferentMediaTypeWhenImmutabilityEnabled",
	preload: ocitest.RepoContent{
		Blobs: map[string]string{
			"a": "{}",
			"b": "other",
		},
		Manifests: map[string]oci.IndexOrManifest{
			"m": {
				MediaType: oci.MediaTypeImageManifest,
				Config: ref(oci.Descriptor{
					Digest: ocitest.DigestRef("a"),
				}),
				Layers: []oci.Descriptor{{
					Digest: ocitest.DigestRef("a"),
				}},
			},
		},
		Tags: map[string]string{
			"sometag": "m",
		},
	},
	config: Config{
		ImmutableTags: true,
	},
	mediaType: "application/vnd.docker.container.image.v1+json",
	tag:       "sometag",
	manifestData: func(content ocitest.PushedRepoContent) []byte {
		return content.ManifestData["m"]
	},
	wantError: `denied: requested access to the resource is denied: mismatched media type`,
}, {
	testName: "CanOverwriteTagWhenImmutabilityNotEnabled",
	preload: ocitest.RepoContent{
		Blobs: map[string]string{
			"a": "{}",
			"b": "other",
		},
		Manifests: map[string]oci.IndexOrManifest{
			"m": {
				MediaType: oci.MediaTypeImageManifest,
				Config: ref(oci.Descriptor{
					Digest: ocitest.DigestRef("a"),
				}),
				Layers: []oci.Descriptor{{
					Digest: ocitest.DigestRef("a"),
				}},
			},
		},
		Tags: map[string]string{
			"sometag": "m",
		},
	},
	mediaType: oci.MediaTypeImageManifest,
	tag:       "sometag",
	manifestData: func(content ocitest.PushedRepoContent) []byte {
		return mustJSONMarshal(oci.IndexOrManifest{
			MediaType: oci.MediaTypeImageManifest,
			Config:    ref(content.Blobs["a"]),
			Layers:    []oci.Descriptor{content.Blobs["a"]},
			Annotations: map[string]string{
				"different": "thing",
			},
		})
	},
}}

func TestPushManifest(t *testing.T) {
	for _, test := range pushManifestTests {
		t.Run(test.testName, func(t *testing.T) {
			ctx := context.Background()
			r := ocitest.NewRegistry(t, NewWithConfig(&test.config))
			content := r.MustPushContent(ocitest.RegistryContent{
				"test": test.preload,
			})["test"]
			data := test.manifestData(content)
			var params *oci.PushManifestParameters
			if test.tag != "" {
				params = &oci.PushManifestParameters{
					Tags: []string{test.tag},
				}
			}
			_, err := r.R.PushManifest(ctx, "test", data, test.mediaType, params)
			if test.wantError != "" {
				require.Error(t, err)
				require.Regexp(t, test.wantError, err.Error())
			} else {
				require.NoError(t, err)
			}
		})
	}
}

var deleteBlobTests = []struct {
	testName  string
	config    Config
	preload   ocitest.RepoContent
	getDigest func(content ocitest.PushedRepoContent) oci.Digest
	wantError string
}{{
	testName: "NonExistentRepo",
	getDigest: func(content ocitest.PushedRepoContent) oci.Digest {
		return ocitest.DigestRef("blshdfsvg")
	},
	wantError: "name unknown: repository name not known to registry",
}, {
	testName: "NonExistentBlob",
	preload: ocitest.RepoContent{
		Blobs: map[string]string{
			"a": "{}",
		},
	},
	getDigest: func(content ocitest.PushedRepoContent) oci.Digest {
		return ocitest.DigestRef("blshdfsvg")
	},
	wantError: "blob unknown: blob unknown to registry",
}, {
	testName: "TaggedBlobWithImmutableTags",
	config: Config{
		ImmutableTags: true,
	},
	preload: ocitest.RepoContent{
		Blobs: map[string]string{
			"a": "{}",
		},
		Manifests: map[string]oci.IndexOrManifest{
			"m": {
				MediaType: oci.MediaTypeImageManifest,
				Config: ref(oci.Descriptor{
					Digest: ocitest.DigestRef("a"),
				}),
				Layers: []oci.Descriptor{{
					Digest: ocitest.DigestRef("a"),
				}},
			},
		},
		Tags: map[string]string{
			"sometag": "m",
		},
	},
	getDigest: func(content ocitest.PushedRepoContent) oci.Digest {
		return content.Blobs["a"].Digest
	},
	wantError: "denied: requested access to the resource is denied: deletion of tagged blob not permitted",
}, {
	testName: "IndirectlyTaggedBlobWithImmutableTags",
	config: Config{
		ImmutableTags: true,
	},
	preload: ocitest.RepoContent{
		Blobs: map[string]string{
			"a": "{}",
			"b": "other",
		},
		Manifests: map[string]oci.IndexOrManifest{
			"m0": {
				MediaType: oci.MediaTypeImageManifest,
				Config: ref(oci.Descriptor{
					Digest: ocitest.DigestRef("a"),
				}),
			},
			"m1": {
				MediaType: oci.MediaTypeImageManifest,
				Config: ref(oci.Descriptor{
					Digest: ocitest.DigestRef("b"),
				}),
				Subject: &oci.Descriptor{
					Digest: ocitest.DigestRef("m0"),
				},
			},
		},
		Tags: map[string]string{
			"sometag": "m1",
		},
	},
	getDigest: func(content ocitest.PushedRepoContent) oci.Digest {
		return content.Blobs["a"].Digest
	},
	wantError: "denied: requested access to the resource is denied: deletion of tagged blob not permitted",
}}

func TestDeleteBlob(t *testing.T) {
	for _, test := range deleteBlobTests {
		t.Run(test.testName, func(t *testing.T) {
			ctx := context.Background()
			r := ocitest.NewRegistry(t, NewWithConfig(&test.config))
			content := r.MustPushContent(ocitest.RegistryContent{
				"test": test.preload,
			})["test"]
			digest := test.getDigest(content)
			err := r.R.DeleteBlob(ctx, "test", digest)
			if test.wantError != "" {
				require.Error(t, err)
				require.Regexp(t, test.wantError, err.Error())
			} else {
				require.NoError(t, err)
			}
			// Regardless of the result, the blob shouldn't be there afterwards
			// unless the operation was denied.
			if !errors.Is(err, oci.ErrDenied) {
				_, err := r.R.ResolveBlob(ctx, "test", digest)
				require.Error(t, err)
			}
		})
	}
}

var deleteManifestTests = []struct {
	testName  string
	config    Config
	preload   ocitest.RepoContent
	getDigest func(content ocitest.PushedRepoContent) oci.Digest
	wantError string
}{{
	testName: "NonExistentRepo",
	getDigest: func(content ocitest.PushedRepoContent) oci.Digest {
		return ocitest.DigestRef("blshdfsvg")
	},
	wantError: "name unknown: repository name not known to registry",
}, {
	testName: "NonExistentManifest",
	preload: ocitest.RepoContent{
		Blobs: map[string]string{
			"a": "{}",
		},
	},
	getDigest: func(content ocitest.PushedRepoContent) oci.Digest {
		return ocitest.DigestRef("blshdfsvg")
	},
	wantError: "manifest unknown: manifest unknown to registry",
}, {
	testName: "TaggedManifestWithImmutableTags",
	config: Config{
		ImmutableTags: true,
	},
	preload: ocitest.RepoContent{
		Blobs: map[string]string{
			"a": "{}",
		},
		Manifests: map[string]oci.IndexOrManifest{
			"m": {
				MediaType: oci.MediaTypeImageManifest,
				Config: ref(oci.Descriptor{
					Digest: ocitest.DigestRef("a"),
				}),
			},
		},
		Tags: map[string]string{
			"sometag": "m",
		},
	},
	getDigest: func(content ocitest.PushedRepoContent) oci.Digest {
		return content.Manifests["m"].Digest
	},
	wantError: "denied: requested access to the resource is denied: deletion of tagged manifest not permitted",
}, {
	testName: "IndirectlyTaggedManifestWithImmutableTags",
	config: Config{
		ImmutableTags: true,
	},
	preload: ocitest.RepoContent{
		Blobs: map[string]string{
			"a": "{}",
			"b": "other",
		},
		Manifests: map[string]oci.IndexOrManifest{
			"m0": {
				MediaType: oci.MediaTypeImageManifest,
				Config: ref(oci.Descriptor{
					Digest: ocitest.DigestRef("a"),
				}),
			},
			"m1": {
				MediaType: oci.MediaTypeImageManifest,
				Config: ref(oci.Descriptor{
					Digest: ocitest.DigestRef("b"),
				}),
				Subject: &oci.Descriptor{
					Digest: ocitest.DigestRef("m0"),
				},
			},
		},
		Tags: map[string]string{
			"sometag": "m1",
		},
	},
	getDigest: func(content ocitest.PushedRepoContent) oci.Digest {
		return content.Manifests["m0"].Digest
	},
	wantError: "denied: requested access to the resource is denied: deletion of tagged manifest not permitted",
}}

func TestDeleteManifest(t *testing.T) {
	for _, test := range deleteManifestTests {
		t.Run(test.testName, func(t *testing.T) {
			ctx := context.Background()
			r := ocitest.NewRegistry(t, NewWithConfig(&test.config))
			content := r.MustPushContent(ocitest.RegistryContent{
				"test": test.preload,
			})["test"]
			digest := test.getDigest(content)
			err := r.R.DeleteManifest(ctx, "test", digest)
			if test.wantError != "" {
				require.Error(t, err)
				require.Regexp(t, test.wantError, err.Error())
			} else {
				require.NoError(t, err)
			}
			// Regardless of the result, the manifest shouldn't be there afterwards
			// unless the operation was denied.
			if !errors.Is(err, oci.ErrDenied) {
				_, err := r.R.ResolveManifest(ctx, "test", digest)
				require.Error(t, err)
			}
		})
	}
}

var deleteTagTests = []struct {
	testName  string
	config    Config
	preload   ocitest.RepoContent
	tag       string
	wantError string
}{{
	testName:  "NonExistentRepo",
	tag:       "foo",
	wantError: "name unknown: repository name not known to registry",
}, {
	testName: "NonExistentTag",
	preload: ocitest.RepoContent{
		Blobs: map[string]string{
			"a": "{}",
		},
	},
	tag:       "foo",
	wantError: "manifest unknown: manifest unknown to registry: tag does not exist",
}, {
	testName: "WithImmutableTags",
	config: Config{
		ImmutableTags: true,
	},
	preload: ocitest.RepoContent{
		Blobs: map[string]string{
			"a": "{}",
		},
		Manifests: map[string]oci.IndexOrManifest{
			"m": {
				MediaType: oci.MediaTypeImageManifest,
				Config: ref(oci.Descriptor{
					Digest: ocitest.DigestRef("a"),
				}),
			},
		},
		Tags: map[string]string{
			"sometag": "m",
		},
	},
	tag:       "sometag",
	wantError: "denied: requested access to the resource is denied: tag deletion not permitted",
}, {
	testName: "Success",
	preload: ocitest.RepoContent{
		Blobs: map[string]string{
			"a": "{}",
			"b": "other",
		},
		Manifests: map[string]oci.IndexOrManifest{
			"m0": {
				MediaType: oci.MediaTypeImageManifest,
				Config: ref(oci.Descriptor{
					Digest: ocitest.DigestRef("a"),
				}),
			},
			"m1": {
				MediaType: oci.MediaTypeImageManifest,
				Config: ref(oci.Descriptor{
					Digest: ocitest.DigestRef("b"),
				}),
				Subject: &oci.Descriptor{
					Digest: ocitest.DigestRef("m0"),
				},
			},
		},
		Tags: map[string]string{
			"sometag": "m1",
		},
	},
	tag: "sometag",
}}

func TestDeleteTag(t *testing.T) {
	for _, test := range deleteTagTests {
		t.Run(test.testName, func(t *testing.T) {
			ctx := context.Background()
			r := ocitest.NewRegistry(t, NewWithConfig(&test.config))
			content := r.MustPushContent(ocitest.RegistryContent{
				"test": test.preload,
			})["test"]
			err := r.R.DeleteTag(ctx, "test", test.tag)
			if test.wantError != "" {
				require.Error(t, err)
				require.Regexp(t, test.wantError, err.Error())
			} else {
				require.NoError(t, err)
			}
			// Regardless of the result, the tag shouldn't be there afterwards
			// unless the operation was denied.
			if !errors.Is(err, oci.ErrDenied) {
				_, err := r.R.ResolveTag(ctx, "test", test.tag)
				require.Error(t, err)
			}
			// The manifest should remain present even though the tag
			// itself has been deleted.
			if tagDesc, ok := content.Manifests[test.preload.Tags[test.tag]]; ok {
				_, err := r.R.ResolveManifest(ctx, "test", tagDesc.Digest)
				require.NoError(t, err)
			}
		})
	}
}

func mustJSONMarshal(x any) []byte {
	data, err := json.Marshal(x)
	if err != nil {
		panic(err)
	}
	return data
}

func ref[T any](x T) *T {
	return &x
}

func TestTagsLimit(t *testing.T) {
	ctx := context.Background()
	r := ocitest.NewRegistry(t, New())
	r.MustPushContent(ocitest.RegistryContent{
		"test": {
			Blobs: map[string]string{
				"a": "{}",
			},
			Manifests: map[string]oci.IndexOrManifest{
				"m": {
					MediaType: oci.MediaTypeImageManifest,
					Config: ref(oci.Descriptor{
						Digest: ocitest.DigestRef("a"),
					}),
				},
			},
			Tags: map[string]string{
				"alpha":   "m",
				"bravo":   "m",
				"charlie": "m",
				"delta":   "m",
				"echo":    "m",
			},
		},
	})

	t.Run("ZeroLimitReturnsAll", func(t *testing.T) {
		tags, err := oci.All(r.R.Tags(ctx, "test", nil))
		require.NoError(t, err)
		require.Equal(t, []string{"alpha", "bravo", "charlie", "delta", "echo"}, tags)
	})

	t.Run("NegativeLimitReturnsAll", func(t *testing.T) {
		tags, err := oci.All(r.R.Tags(ctx, "test", &oci.TagsParameters{Limit: -1}))
		require.NoError(t, err)
		require.Equal(t, []string{"alpha", "bravo", "charlie", "delta", "echo"}, tags)
	})

	t.Run("LimitSmallerThanTotal", func(t *testing.T) {
		tags, err := oci.All(r.R.Tags(ctx, "test", &oci.TagsParameters{Limit: 3}))
		require.NoError(t, err)
		require.Equal(t, []string{"alpha", "bravo", "charlie"}, tags)
	})

	t.Run("LimitOfOne", func(t *testing.T) {
		tags, err := oci.All(r.R.Tags(ctx, "test", &oci.TagsParameters{Limit: 1}))
		require.NoError(t, err)
		require.Equal(t, []string{"alpha"}, tags)
	})

	t.Run("LimitLargerThanTotal", func(t *testing.T) {
		tags, err := oci.All(r.R.Tags(ctx, "test", &oci.TagsParameters{Limit: 100}))
		require.NoError(t, err)
		require.Equal(t, []string{"alpha", "bravo", "charlie", "delta", "echo"}, tags)
	})

	t.Run("LimitEqualToTotal", func(t *testing.T) {
		tags, err := oci.All(r.R.Tags(ctx, "test", &oci.TagsParameters{Limit: 5}))
		require.NoError(t, err)
		require.Equal(t, []string{"alpha", "bravo", "charlie", "delta", "echo"}, tags)
	})

	t.Run("LimitWithStartAfter", func(t *testing.T) {
		tags, err := oci.All(r.R.Tags(ctx, "test", &oci.TagsParameters{StartAfter: "bravo", Limit: 2}))
		require.NoError(t, err)
		require.Equal(t, []string{"charlie", "delta"}, tags)
	})

	t.Run("LimitWithStartAfterReturnsAll", func(t *testing.T) {
		tags, err := oci.All(r.R.Tags(ctx, "test", &oci.TagsParameters{StartAfter: "bravo"}))
		require.NoError(t, err)
		require.Equal(t, []string{"charlie", "delta", "echo"}, tags)
	})

	t.Run("NonExistentRepo", func(t *testing.T) {
		_, err := oci.All(r.R.Tags(ctx, "nonexistent", &oci.TagsParameters{Limit: 3}))
		require.Error(t, err)
	})
}
