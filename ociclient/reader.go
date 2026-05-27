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

	"github.com/docker/oci"
	"github.com/docker/oci/ocidigest"
)

// GetBlob returns the content of the blob with the given digest.
func (c *Client) GetBlob(ctx context.Context, repo string, digest oci.Digest) (oci.BlobReader, error) {
	req, err := newRequest(ctx, http.MethodGet, blobURL(repo, digest), nil, pullScope(repo))
	if err != nil {
		return nil, err
	}
	return c.read(req, digest, false)
}

// GetBlobRange is like GetBlob but asks for only the given byte range.
func (c *Client) GetBlobRange(ctx context.Context, repo string, digest oci.Digest, o0, o1 int64) (_ oci.BlobReader, _err error) {
	if o0 == 0 && o1 < 0 {
		return c.GetBlob(ctx, repo, digest)
	}
	req, err := newRequest(ctx, http.MethodGet, blobURL(repo, digest), nil, pullScope(repo))
	if err != nil {
		return nil, err
	}
	if o1 < 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", o0))
	} else {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", o0, o1-1))
	}
	resp, err := c.do(req, http.StatusOK, http.StatusPartialContent)
	if err != nil {
		return nil, err
	}
	// TODO this is wrong when the server returns a 200 response.
	// Fix that either by returning ErrUnsupported or by reading the whole
	// blob and returning only the required portion.
	defer closeOnError(&_err, resp.Body)
	desc, err := descriptorFromResponse(resp, digest, requireSize)
	if err != nil {
		return nil, fmt.Errorf("invalid descriptor in response: %v", err)
	}
	return newBlobReaderUnverified(resp.Body, desc), nil
}

// ResolveBlob returns the descriptor for the blob with the given digest.
func (c *Client) ResolveBlob(ctx context.Context, repo string, digest oci.Digest) (oci.Descriptor, error) {
	req, err := newRequest(ctx, http.MethodHead, blobURL(repo, digest), nil, pullScope(repo))
	if err != nil {
		return oci.Descriptor{}, err
	}
	return c.resolve(req, digest)
}

// ResolveManifest returns the descriptor for the manifest with the given digest.
func (c *Client) ResolveManifest(ctx context.Context, repo string, digest oci.Digest) (oci.Descriptor, error) {
	req, err := newManifestRequest(ctx, http.MethodHead, repo, digest.String(), pullScope(repo))
	if err != nil {
		return oci.Descriptor{}, err
	}
	return c.resolve(req, digest)
}

// ResolveTag returns the descriptor for the manifest with the given tag.
func (c *Client) ResolveTag(ctx context.Context, repo string, tag string) (oci.Descriptor, error) {
	req, err := newManifestRequest(ctx, http.MethodHead, repo, tag, pullScope(repo))
	if err != nil {
		return oci.Descriptor{}, err
	}
	return c.resolve(req, "")
}

func (c *Client) resolve(req *http.Request, knownDigest oci.Digest) (oci.Descriptor, error) {
	resp, err := c.do(req)
	if err != nil {
		return oci.Descriptor{}, err
	}
	resp.Body.Close()
	desc, err := descriptorFromResponse(resp, knownDigest, requireSize|requireDigest)
	if err != nil {
		return oci.Descriptor{}, fmt.Errorf("invalid descriptor in response: %v", err)
	}
	return desc, nil
}

// GetManifest returns the content of the manifest with the given digest.
func (c *Client) GetManifest(ctx context.Context, repo string, digest oci.Digest) (oci.BlobReader, error) {
	req, err := newManifestRequest(ctx, http.MethodGet, repo, digest.String(), pullScope(repo))
	if err != nil {
		return nil, err
	}
	return c.read(req, digest, true)
}

// GetTag returns the content of the manifest with the given tag.
func (c *Client) GetTag(ctx context.Context, repo string, tagName string) (oci.BlobReader, error) {
	req, err := newManifestRequest(ctx, http.MethodGet, repo, tagName, pullScope(repo))
	if err != nil {
		return nil, err
	}
	return c.read(req, "", true)
}

const maxManifestSize = 4 * 1024 * 1024

func (c *Client) read(req *http.Request, knownDigest oci.Digest, isManifest bool) (_ oci.BlobReader, _err error) {
	resp, err := c.do(req)
	if err != nil {
		return nil, err
	}
	defer closeOnError(&_err, resp.Body)
	desc, err := descriptorFromResponse(resp, knownDigest, requireSize)
	if err != nil {
		return nil, fmt.Errorf("invalid descriptor in response: %v", err)
	}
	if desc.Digest == "" {
		// Returning a digest isn't mandatory according to the spec, and
		// at least one registry (AWS's ECR) fails to return a digest
		// when doing a GET of a tag.
		// We know the request must be a tag-getting
		// request because all other requests take a digest not a tag
		// but sanity check anyway.
		if !isManifest {
			return nil, fmt.Errorf("internal error: no digest available for non-tag request")
		}

		if desc.Size > maxManifestSize {
			return nil, fmt.Errorf("manifest size %d exceeds maximum size %d", desc.Size, maxManifestSize)
		}

		data, err := io.ReadAll(io.LimitReader(resp.Body, maxManifestSize+1))
		if err != nil {
			return nil, fmt.Errorf("failed to read body to determine digest: %v", err)
		}
		if int64(len(data)) != desc.Size {
			return nil, fmt.Errorf("body size mismatch")
		}
		desc.Digest = ocidigest.FromBytes(data)
		resp.Body.Close()
		resp.Body = io.NopCloser(bytes.NewReader(data))
	}
	return newBlobReader(resp.Body, desc), nil
}

func blobURL(repo string, digest oci.Digest) string {
	return "/v2/" + repo + "/blobs/" + digest.String()
}

func manifestURL(repo string, tagOrDigest string) string {
	return "/v2/" + repo + "/manifests/" + tagOrDigest
}
