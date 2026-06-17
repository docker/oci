// Copyright 2023 CUE Labs AG
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package ociclient

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"

	"github.com/docker/oci"

	"github.com/docker/oci/ociauth"
	"github.com/docker/oci/ocidigest"
)

// PushManifest pushes a manifest with the given media type and contents.
func (c *Client) PushManifest(ctx context.Context, repo string, contents []byte, mediaType string, params *oci.PushManifestParameters) (oci.Descriptor, error) {
	if mediaType == "" {
		return oci.Descriptor{}, fmt.Errorf("PushManifest called with empty mediaType")
	}
	var dig oci.Digest
	if params != nil && params.Digest != "" {
		dig = params.Digest
	} else {
		dig = ocidigest.FromBytes(contents)
	}
	desc := oci.Descriptor{
		Digest:    dig,
		Size:      int64(len(contents)),
		MediaType: mediaType,
	}

	var tags []string
	if params != nil {
		tags = params.Tags
	}

	// If there are no tags, push once by digest.
	// If there are tags, push once per tag (all referencing the same contents).
	if len(tags) == 0 {
		_, err := c.putManifest(ctx, repo, desc.Digest.String(), nil, desc, contents)
		return desc, err
	} else {
		createdTags, err := c.putManifest(ctx, repo, desc.Digest.String(), tags, desc, contents)
		if err != nil || len(createdTags) != len(tags) {
			// bulk send failed, fallback to sending one at a time
			for _, tag := range tags {
				_, err = c.putManifest(ctx, repo, tag, nil, desc, contents)
				if err != nil {
					return oci.Descriptor{}, fmt.Errorf("creating tag %s failed: %w", tag, err)
				}
			}
		}
	}
	return desc, nil
}

func (c *Client) putManifest(ctx context.Context, repo string, tagOrDigest string, tags []string, desc oci.Descriptor, contents []byte) ([]string, error) {
	u := manifestURLWithTags(repo, tagOrDigest, tags)
	req, err := newRequest(ctx, http.MethodPut, u, bytes.NewReader(contents), pushScope(repo))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", desc.MediaType)
	req.ContentLength = desc.Size
	resp, err := c.do(req, http.StatusCreated)
	if err != nil {
		return nil, err
	}
	resp.Body.Close()

	var createdTags []string
	if vs := resp.Header.Values("OCI-Tag"); len(vs) > 0 {
		for _, v := range vs {
			createdTags = append(createdTags, strings.Split(v, ",")...)
		}
	}
	return createdTags, nil
}

func manifestURLWithTags(repo string, tagOrDigest string, tags []string) string {
	u := manifestURL(repo, tagOrDigest)
	if len(tags) == 0 {
		return u
	}
	q := make(url.Values)
	for _, tag := range tags {
		q.Add("tag", tag)
	}
	return u + "?" + q.Encode()
}

// CopyImage copies an image from fromRepo into toRepo using reference as the
// source tag or digest.
//
// Experimental: CopyImage uses a registry extension and is not part of
// [oci.Interface] or the OCI distribution specification.
func (c *Client) CopyImage(ctx context.Context, fromRepo, toRepo, reference string) (oci.Descriptor, error) {
	q := url.Values{}
	q.Set("from", fromRepo)
	q.Set("reference", reference)
	req, err := newRequest(ctx, http.MethodPost, copyImageURL(toRepo, q), nil, mountScope(fromRepo, toRepo))
	if err != nil {
		return oci.Descriptor{}, err
	}
	resp, err := c.do(req, http.StatusCreated)
	if err != nil {
		return oci.Descriptor{}, err
	}
	resp.Body.Close()
	return descriptorFromResponse(resp, "", requireDigest)
}

func copyImageURL(repo string, q url.Values) string {
	return "/v2/" + repo + "/_docker/copy/image?" + q.Encode()
}

// MountBlob makes a blob from fromRepo available in toRepo without uploading it again.
func (c *Client) MountBlob(ctx context.Context, fromRepo, toRepo string, dig oci.Digest) (oci.Descriptor, error) {
	q := url.Values{}
	q.Set("mount", dig.String())
	q.Set("from", fromRepo)
	req, err := newRequest(ctx, http.MethodPost, blobUploadURL(toRepo, q), nil, mountScope(fromRepo, toRepo))
	if err != nil {
		return oci.Descriptor{}, err
	}
	resp, err := c.do(req, http.StatusCreated, http.StatusAccepted)
	if err != nil {
		return oci.Descriptor{}, err
	}
	resp.Body.Close()
	if resp.StatusCode == http.StatusAccepted {
		// Mount isn't supported and technically the upload session has begun,
		// but we aren't in a great position to be able to continue it, so let's just
		// return Unsupported.
		return oci.Descriptor{}, fmt.Errorf("registry does not support mounts: %w", oci.ErrUnsupported)
	}
	// TODO: is it OK to omit the size from the returned descriptor here?
	return descriptorFromResponse(resp, dig, requireDigest)
}

// PushBlob pushes a blob described by desc to the given repository.
func (c *Client) PushBlob(ctx context.Context, repo string, desc oci.Descriptor, r io.Reader) (_ oci.Descriptor, _err error) {
	// TODO use the single-post blob-upload method.
	// See:
	//	https://github.com/distribution/distribution/issues/4065
	//	https://github.com/golang/go/issues/63152
	scope := pushScope(repo)
	req, err := newRequest(ctx, http.MethodPost, blobUploadURL(repo, nil), nil, scope)
	if err != nil {
		return oci.Descriptor{}, err
	}
	resp, err := c.do(req, http.StatusAccepted)
	if err != nil {
		return oci.Descriptor{}, err
	}
	resp.Body.Close()
	location, err := locationFromResponse(resp)
	if err != nil {
		return oci.Descriptor{}, err
	}

	// We've got the upload location. Now PUT the content.

	ctx = ociauth.ContextWithRequestInfo(ctx, ociauth.RequestInfo{
		RequiredScope: scope,
	})
	req, err = http.NewRequestWithContext(ctx, "PUT", "", r)
	if err != nil {
		return oci.Descriptor{}, err
	}
	req.URL = urlWithDigest(location, desc.Digest.String())
	req.ContentLength = desc.Size
	req.Header.Set("Content-Type", "application/octet-stream")
	// TODO: per the spec, the content-range header here is unnecessary.
	req.Header.Set("Content-Range", contentRange(0, desc.Size))
	resp, err = c.do(req, http.StatusCreated)
	if err != nil {
		return oci.Descriptor{}, err
	}
	defer closeOnError(&_err, resp.Body)
	resp.Body.Close()
	return desc, nil
}

// TODO is this a reasonable default? We have to
// weigh up in-memory cost vs round-trip overhead.
// TODO: make this default configurable.
const defaultChunkSize = 64 * 1024

// PushBlobChunked starts a chunked blob upload to the given repository.
func (c *Client) PushBlobChunked(ctx context.Context, repo string, chunkSize int) (oci.BlobWriter, error) {
	if chunkSize <= 0 {
		chunkSize = defaultChunkSize
	}
	req, err := newRequest(ctx, http.MethodPost, blobUploadURL(repo, nil), nil, pushScope(repo))
	if err != nil {
		return nil, err
	}
	resp, err := c.do(req, http.StatusAccepted)
	if err != nil {
		return nil, err
	}
	resp.Body.Close()
	location, err := locationFromResponse(resp)
	if err != nil {
		return nil, err
	}
	ctx = ociauth.ContextWithRequestInfo(ctx, ociauth.RequestInfo{
		RequiredScope: pushScope(repo),
	})
	return &blobWriter{
		ctx:       ctx,
		client:    c,
		chunkSize: chunkSizeFromResponse(resp, chunkSize),
		chunk:     make([]byte, 0, chunkSize),
		location:  location,
	}, nil
}

// PushBlobChunkedResume resumes a previous chunked blob upload.
func (c *Client) PushBlobChunkedResume(ctx context.Context, repo string, id string, offset int64, chunkSize int) (oci.BlobWriter, error) {
	if id == "" {
		return nil, fmt.Errorf("id must be non-empty to resume a chunked upload")
	}
	if chunkSize <= 0 {
		chunkSize = defaultChunkSize
	}
	var location *url.URL
	switch {
	case offset == -1:
		// Try to find what offset we're meant to be writing at
		// by doing a GET to the location.
		ctx := ociauth.ContextWithRequestInfo(ctx, ociauth.RequestInfo{
			RequiredScope: pushScope(repo),
		})
		req, err := http.NewRequestWithContext(ctx, "GET", id, nil)
		if err != nil {
			return nil, err
		}
		resp, err := c.do(req, http.StatusNoContent)
		if err != nil {
			return nil, fmt.Errorf("cannot recover chunk offset: %v", err)
		}
		location, err = locationFromResponse(resp)
		if err != nil {
			return nil, fmt.Errorf("cannot get location from response: %v", err)
		}
		rangeStr := resp.Header.Get("Range")
		p0, p1, ok := parseContentRange(rangeStr)
		if !ok {
			return nil, fmt.Errorf("invalid range %q in response", rangeStr)
		}
		if p0 != 0 {
			return nil, fmt.Errorf("range %q does not start with 0", rangeStr)
		}
		chunkSize = chunkSizeFromResponse(resp, chunkSize)
		offset = p1
	case offset < 0:
		return nil, fmt.Errorf("invalid offset; must be -1 or non-negative")
	default:
		var err error
		location, err = url.Parse(id) // Note that this mirrors [BlobWriter.ID].
		if err != nil {
			return nil, fmt.Errorf("provided ID is not a valid location URL")
		}
		if !strings.HasPrefix(location.Path, "/") {
			// Our BlobWriter.ID method always returns a fully
			// qualified absolute URL, so this must be a mistake
			// on the part of the caller.
			// We allow a relative URL even though we don't
			// ever return one to make things a bit easier for tests.
			return nil, fmt.Errorf("provided upload ID %q has unexpected relative URL path", id)
		}
	}
	ctx = ociauth.ContextWithRequestInfo(ctx, ociauth.RequestInfo{
		RequiredScope: pushScope(repo),
	})
	return &blobWriter{
		ctx:       ctx,
		client:    c,
		chunkSize: chunkSize,
		size:      offset,
		flushed:   offset,
		location:  location,
	}, nil
}

type blobWriter struct {
	client    *Client
	chunkSize int
	ctx       context.Context

	// mu guards the fields below it.
	mu       sync.Mutex
	closed   bool
	chunk    []byte
	closeErr error

	// size holds the size of the entire upload as seen from the
	// client perspective. Each call to Write increases this immediately.
	size int64

	// flushed holds the size of the upload as flushed to the server.
	// Each successfully flushed chunk increases this.
	flushed  int64
	location *url.URL
}

func (w *blobWriter) Write(buf []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	// We use > rather than >= here so that using a chunk size of 100
	// and writing 100 bytes does not actually flush, which would result in a PATCH
	// then followed by an empty-bodied PUT with the call to Commit.
	// Instead, we want the writes to not flush at all, and Commit to PUT the entire chunk.
	if len(w.chunk)+len(buf) > w.chunkSize {
		if err := w.flush(buf, ""); err != nil {
			return 0, err
		}
	} else {
		if w.chunk == nil {
			w.chunk = make([]byte, 0, w.chunkSize)
		}
		w.chunk = append(w.chunk, buf...)
	}
	w.size += int64(len(buf))
	return len(buf), nil
}

// flush flushes any outstanding upload data to the server.
// If commitDigest is non-empty, this is the final segment of data in the blob:
// the blob is being committed and the digest should hold the digest of the entire blob content.
func (w *blobWriter) flush(buf []byte, commitDigest oci.Digest) error {
	if commitDigest == "" && len(buf)+len(w.chunk) == 0 {
		return nil
	}
	// Start a new PATCH request to send the currently outstanding data.
	method := "PATCH"
	expect := http.StatusAccepted
	reqURL := w.location
	if commitDigest != "" {
		// This is the final piece of data, so send it as the final PUT request
		// (committing the whole blob) which avoids an extra round trip.
		method = "PUT"
		expect = http.StatusCreated
		reqURL = urlWithDigest(reqURL, commitDigest.String())
	}
	req, err := http.NewRequestWithContext(w.ctx, method, "", concatBody(w.chunk, buf))
	if err != nil {
		return fmt.Errorf("cannot make PATCH request: %v", err)
	}
	req.URL = reqURL
	req.ContentLength = int64(len(w.chunk) + len(buf))
	// TODO: per the spec, the content-range header here is unnecessary
	// if we are doing a final PUT without a body.
	req.Header.Set("Content-Range", contentRange(w.flushed, w.flushed+req.ContentLength))
	resp, err := w.client.do(req, expect)
	if err != nil {
		return err
	}
	resp.Body.Close()
	location, err := locationFromResponse(resp)
	if err != nil {
		return fmt.Errorf("bad Location in response: %v", err)
	}
	// TODO is there something we could be doing with the Range header in the response?
	w.location = location
	w.flushed += req.ContentLength
	w.chunk = w.chunk[:0]
	return nil
}

func concatBody(b1, b2 []byte) io.Reader {
	if len(b1)+len(b2) == 0 {
		return nil // note that net/http treats a nil request body differently
	}
	if len(b1) == 0 {
		return bytes.NewReader(b2)
	}
	if len(b2) == 0 {
		return bytes.NewReader(b1)
	}
	return io.MultiReader(
		bytes.NewReader(b1),
		bytes.NewReader(b2),
	)
}

func (w *blobWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return w.closeErr
	}
	err := w.flush(nil, "")
	w.closed = true
	w.closeErr = err
	return err
}

func (w *blobWriter) Size() int64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.size
}

func (w *blobWriter) ChunkSize() int {
	return w.chunkSize
}

func (w *blobWriter) ID() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.location.String()
}

func (w *blobWriter) Commit(digest oci.Digest) (oci.Descriptor, error) {
	if digest == "" {
		return oci.Descriptor{}, fmt.Errorf("cannot commit with an empty digest")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.flush(nil, digest); err != nil {
		return oci.Descriptor{}, fmt.Errorf("cannot flush data before commit: %w", err)
	}
	return oci.Descriptor{
		MediaType: "application/octet-stream",
		Size:      w.size,
		Digest:    digest,
	}, nil
}

func (w *blobWriter) Cancel() error {
	return nil
}

// urlWithDigest returns u with the digest query parameter set, taking care not
// to disrupt the initial URL (thus avoiding the charge of "manually
// assembing the location; see [here].
//
// [here]: https://github.com/opencontainers/distribution-spec/blob/main/spec.md#post-then-put
func urlWithDigest(u0 *url.URL, digest string) *url.URL {
	u := *u0
	digest = url.QueryEscape(digest)
	switch {
	case u.ForceQuery:
		// The URL already ended in a "?" with no actual query parameters.
		u.RawQuery = "digest=" + digest
		u.ForceQuery = false
	case u.RawQuery != "":
		// There's already a query parameter present.
		u.RawQuery += "&digest=" + digest
	default:
		u.RawQuery = "digest=" + digest
	}
	return &u
}

// See https://github.com/opencontainers/distribution-spec/blob/main/spec.md#pushing-a-blob-in-chunks
func chunkSizeFromResponse(resp *http.Response, chunkSize int) int {
	minChunkSize, err := strconv.Atoi(resp.Header.Get("OCI-Chunk-Min-Length"))
	if err == nil && minChunkSize > chunkSize {
		return minChunkSize
	}
	return chunkSize
}

func blobUploadURL(repo string, query url.Values) string {
	u := "/v2/" + repo + "/blobs/uploads/"
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	return u
}

func contentRange(start, end int64) string {
	end--
	if end < 0 {
		end = 0
	}
	return fmt.Sprintf("%d-%d", start, end)
}

func parseContentRange(s string) (start, end int64, ok bool) {
	p0s, p1s, ok := strings.Cut(s, "-")
	if !ok {
		return 0, 0, false
	}
	p0, err0 := strconv.ParseInt(p0s, 10, 64)
	p1, err1 := strconv.ParseInt(p1s, 10, 64)
	if p1 > 0 {
		p1++
	}
	return p0, p1, err0 == nil && err1 == nil
}
