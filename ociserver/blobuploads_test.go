package ociserver

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/docker/oci"
	"github.com/docker/oci/ocidigest"
	"github.com/stretchr/testify/require"
)

func TestBlobUploadPostValidatesMountParameters(t *testing.T) {
	t.Parallel()

	dgst := ocidigest.FromBytes([]byte("blob"))
	tests := []struct {
		name  string
		query string
	}{
		{name: "from is not a local repository", query: "mount=" + dgst.String() + "&from=UPPERCASE"},
		{name: "from contains a tag", query: "mount=" + dgst.String() + "&from=repo%3Alatest"},
		{name: "from is too long", query: "mount=" + dgst.String() + "&from=" + strings.Repeat("a", 256)},
		{name: "mount is not a digest", query: "mount=another%2Frepository&from=repo"},
		{name: "mount without from is still validated", query: "mount=not-a-digest"},
		{name: "digest with mount is still validated", query: "digest=not-a-digest&mount=" + dgst.String() + "&from=repo"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := &Server{db: (*oci.Funcs)(nil)}
			req := httptest.NewRequest(http.MethodPost, "/v2/repo/blobs/uploads/?"+tt.query, nil)
			rec := httptest.NewRecorder()

			serveTestRoute(t, `/v2/*name/blobs/uploads/`, s.blobUploadPost(), rec, req)

			require.Equal(t, http.StatusBadRequest, rec.Code)
			require.Contains(t, rec.Body.String(), `"code":"BLOB_UPLOAD_INVALID"`)
		})
	}
}

func TestParseRange(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		contentRange     string
		wantStart        int
		wantEndExclusive int
		wantSize         int
		wantErr          bool
	}{
		{
			name:             "bare range",
			contentRange:     "0-4",
			wantStart:        0,
			wantEndExclusive: 5,
			wantSize:         5,
		},
		{
			name:             "bytes range with total",
			contentRange:     "bytes 5-9/20",
			wantStart:        5,
			wantEndExclusive: 10,
			wantSize:         5,
		},
		{
			name:         "negative start",
			contentRange: "-1-4",
			wantErr:      true,
		},
		{
			name:         "negative end",
			contentRange: "1--4",
			wantErr:      true,
		},
		{
			name:         "end before start",
			contentRange: "5-3",
			wantErr:      true,
		},
		{
			name:         "missing dash",
			contentRange: "5",
			wantErr:      true,
		},
		{
			name:         "non numeric start",
			contentRange: "a-5",
			wantErr:      true,
		},
		{
			name:         "non numeric end",
			contentRange: "5-b",
			wantErr:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			gotStart, gotEndExclusive, gotSize, err := parseRange(tt.contentRange)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseRange(%q) succeeded, want error", tt.contentRange)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseRange(%q) returned error: %v", tt.contentRange, err)
			}
			if gotStart != tt.wantStart || gotEndExclusive != tt.wantEndExclusive || gotSize != tt.wantSize {
				t.Fatalf("parseRange(%q) = (%d, %d, %d), want (%d, %d, %d)",
					tt.contentRange,
					gotStart,
					gotEndExclusive,
					gotSize,
					tt.wantStart,
					tt.wantEndExclusive,
					tt.wantSize,
				)
			}
		})
	}
}

func TestBlobHeadGetRangeOpenEnded(t *testing.T) {
	t.Parallel()

	dgst := ocidigest.FromBytes([]byte("0123456789"))
	blob := "0123456789"

	tests := []struct {
		name             string
		rangeHeader      string
		wantOffset0      int64
		wantOffset1      int64
		wantContentRange string
		wantBody         string
	}{
		{
			name:             "missing start returns suffix range",
			rangeHeader:      "bytes=-4",
			wantOffset0:      6,
			wantOffset1:      10,
			wantContentRange: "bytes 6-9/10",
			wantBody:         "6789",
		},
		{
			name:             "missing end returns to end of blob",
			rangeHeader:      "bytes=4-",
			wantOffset0:      4,
			wantOffset1:      10,
			wantContentRange: "bytes 4-9/10",
			wantBody:         "456789",
		},
		{
			name:             "explicit range passes exclusive upper bound to storage",
			rangeHeader:      "bytes=2-5",
			wantOffset0:      2,
			wantOffset1:      6,
			wantContentRange: "bytes 2-5/10",
			wantBody:         "2345",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s := &Server{
				db: &oci.Funcs{
					ResolveBlob_: func(_ context.Context, repo string, gotDigest oci.Digest) (oci.Descriptor, error) {
						if repo != "repo" {
							t.Fatalf("ResolveBlob repo = %q, want repo", repo)
						}
						if gotDigest != dgst {
							t.Fatalf("ResolveBlob digest = %q, want %q", gotDigest, dgst)
						}
						return oci.Descriptor{
							Digest: dgst,
							Size:   int64(len(blob)),
						}, nil
					},
					GetBlobRange_: func(_ context.Context, repo string, gotDigest oci.Digest, offset0, offset1 int64) (oci.BlobReader, error) {
						if repo != "repo" {
							t.Fatalf("GetBlobRange repo = %q, want repo", repo)
						}
						if gotDigest != dgst {
							t.Fatalf("GetBlobRange digest = %q, want %q", gotDigest, dgst)
						}
						if offset0 != tt.wantOffset0 || offset1 != tt.wantOffset1 {
							t.Fatalf("GetBlobRange offsets = (%d, %d), want (%d, %d)", offset0, offset1, tt.wantOffset0, tt.wantOffset1)
						}
						return &testBlobReader{
							ReadCloser: io.NopCloser(strings.NewReader(blob[offset0:offset1])),
							desc: oci.Descriptor{
								Digest: dgst,
								Size:   int64(len(blob)),
							},
						}, nil
					},
				},
			}
			req := httptest.NewRequest(http.MethodGet, "/v2/repo/blobs/"+dgst.String(), nil)
			req.Header.Set("Range", tt.rangeHeader)
			rec := httptest.NewRecorder()

			serveTestRoute(t, `/v2/*name/blobs/:digest`, s.blobHeadGet(), rec, req)

			if rec.Code != http.StatusPartialContent {
				t.Fatalf("status = %d, want %d; body: %s", rec.Code, http.StatusPartialContent, rec.Body.String())
			}
			if got := rec.Header().Get("Content-Range"); got != tt.wantContentRange {
				t.Fatalf("Content-Range = %q, want %q", got, tt.wantContentRange)
			}
			if got := rec.Body.String(); got != tt.wantBody {
				t.Fatalf("body = %q, want %q", got, tt.wantBody)
			}
		})
	}
}

func TestBlobUploadPutStreamsBodyWithoutContentLength(t *testing.T) {
	t.Parallel()

	body := []byte("sha512 final put body")
	dgst := ocidigest.SHA512.FromBytes(body)
	bw := &testBlobWriter{id: "session"}
	var resumeOffsets []int64
	s := &Server{
		db: &oci.Funcs{
			// docker/oci currently checks PushBlobChunked_ before dispatching
			// PushBlobChunkedResume_, so keep this non-nil in the test double.
			PushBlobChunked_: func(context.Context, string, int) (oci.BlobWriter, error) {
				return nil, nil
			},
			PushBlobChunkedResume_: func(_ context.Context, repo, id string, offset int64, _ int) (oci.BlobWriter, error) {
				if repo != "repo" {
					t.Fatalf("repo = %q, want repo", repo)
				}
				if id != "session" {
					t.Fatalf("session = %q, want session", id)
				}
				resumeOffsets = append(resumeOffsets, offset)
				return bw, nil
			},
		},
	}
	req := httptest.NewRequest(http.MethodPut, "/v2/repo/blobs/uploads/session?digest="+dgst.String(), bytes.NewReader(body))
	req.Header.Del("Content-Length")
	req.ContentLength = -1
	rec := httptest.NewRecorder()

	serveTestRoute(t, `/v2/*name/blobs/uploads/:session`, s.blobUploadPut(), rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d; body: %s", rec.Code, http.StatusCreated, rec.Body.String())
	}
	if got := bw.String(); got != string(body) {
		t.Fatalf("written body = %q, want %q", got, body)
	}
	if bw.committedDigest != dgst {
		t.Fatalf("committed digest = %s, want %s", bw.committedDigest, dgst)
	}
	if len(resumeOffsets) != 2 || resumeOffsets[0] != 0 || resumeOffsets[1] != -1 {
		t.Fatalf("resume offsets = %v, want [0 -1]", resumeOffsets)
	}
}

func TestBlobUploadPatchMapsWriterRangeInvalidToOutOfOrder(t *testing.T) {
	t.Parallel()

	s := &Server{
		db: &oci.Funcs{
			PushBlobChunked_: func(context.Context, string, int) (oci.BlobWriter, error) {
				return nil, nil
			},
			PushBlobChunkedResume_: func(context.Context, string, string, int64, int) (oci.BlobWriter, error) {
				return &errorBlobWriter{err: oci.ErrRangeInvalid}, nil
			},
		},
	}
	req := httptest.NewRequest(http.MethodPatch, "/v2/repo/blobs/uploads/session", strings.NewReader("abc"))
	req.Header.Set("Content-Length", "3")
	req.Header.Set("Content-Range", "3-5")
	rec := httptest.NewRecorder()

	serveTestRoute(t, `/v2/*name/blobs/uploads/:session`, s.blobUploadPatch(), rec, req)

	if rec.Code != http.StatusRequestedRangeNotSatisfiable {
		t.Fatalf("status = %d, want %d; body: %s", rec.Code, http.StatusRequestedRangeNotSatisfiable, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "BLOB_UPLOAD_INVALID") {
		t.Fatalf("body = %q, want BLOB_UPLOAD_INVALID", rec.Body.String())
	}
}

type testBlobReader struct {
	io.ReadCloser
	desc oci.Descriptor
}

func (r *testBlobReader) Descriptor() oci.Descriptor {
	return r.desc
}

type testBlobWriter struct {
	bytes.Buffer
	id              string
	committedDigest oci.Digest
}

func (w *testBlobWriter) Close() error { return nil }

func (w *testBlobWriter) Cancel() error { return nil }

func (w *testBlobWriter) Size() int64 { return int64(w.Len()) }

func (w *testBlobWriter) ChunkSize() int { return 0 }

func (w *testBlobWriter) ID() string { return w.id }

func (w *testBlobWriter) Commit(dgst oci.Digest) (oci.Descriptor, error) {
	if dgst.Algorithm().FromBytes(w.Bytes()) != dgst {
		return oci.Descriptor{}, oci.ErrDigestInvalid
	}
	w.committedDigest = dgst
	return oci.Descriptor{
		Digest: dgst,
		Size:   int64(w.Len()),
	}, nil
}

type errorBlobWriter struct {
	err error
}

func (w *errorBlobWriter) Write([]byte) (int, error) {
	return 0, w.err
}

func (w *errorBlobWriter) Close() error { return nil }

func (w *errorBlobWriter) Cancel() error { return nil }

func (w *errorBlobWriter) Size() int64 { return 0 }

func (w *errorBlobWriter) ChunkSize() int { return 0 }

func (w *errorBlobWriter) ID() string { return "session" }

func (w *errorBlobWriter) Commit(oci.Digest) (oci.Descriptor, error) {
	return oci.Descriptor{}, errors.New("unexpected commit")
}
