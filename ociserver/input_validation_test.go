package ociserver

import (
	"context"
	"iter"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/docker/oci"
	"github.com/docker/oci/ocidigest"
	"github.com/stretchr/testify/require"
)

func TestTagsGetValidatesLast(t *testing.T) {
	t.Parallel()

	s := &Server{db: &oci.Funcs{
		Tags_: func(context.Context, string, *oci.TagsParameters) iter.Seq2[string, error] {
			t.Fatal("invalid last parameter reached storage")
			return nil
		},
	}}
	req := httptest.NewRequest(http.MethodGet, "/v2/repo/tags/list?last=-bad", nil)
	rec := httptest.NewRecorder()

	serveTestRoute(t, `/v2/*name/tags/list`, s.tagsGet(), rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), `"code":"BAD_REQUEST"`)
}

func TestReferrersGetValidatesArtifactType(t *testing.T) {
	t.Parallel()

	dgst := ocidigest.FromBytes([]byte("manifest"))
	s := &Server{db: &oci.Funcs{
		Referrers_: func(context.Context, string, oci.Digest, *oci.ReferrersParameters) iter.Seq2[oci.Descriptor, error] {
			t.Fatal("invalid artifactType reached storage")
			return nil
		},
	}}
	tests := []string{
		"not a media type",
		"*/*",
		"application/example; charset=utf-8",
		strings.Repeat("a", oci.MaxArtifactTypeLen+1) + "/x",
	}
	for _, artifactType := range tests {
		req := httptest.NewRequest(http.MethodGet, "/v2/repo/referrers/"+dgst.String()+"?artifactType="+url.QueryEscape(artifactType), nil)
		rec := httptest.NewRecorder()

		serveTestRoute(t, `/v2/*name/referrers/:digest`, s.referrersGet(), rec, req)

		require.Equal(t, http.StatusBadRequest, rec.Code)
		require.Contains(t, rec.Body.String(), `"code":"BAD_REQUEST"`)
	}
}
