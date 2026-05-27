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
	"context"
	"net/http"

	"github.com/docker/oci"
)

// DeleteBlob deletes the blob with the given digest from the repository.
func (c *Client) DeleteBlob(ctx context.Context, repoName string, digest oci.Digest) error {
	req, err := newRequest(ctx, http.MethodDelete, blobURL(repoName, digest), nil, deleteScope(repoName))
	if err != nil {
		return err
	}
	return c.delete(req)
}

// DeleteManifest deletes the manifest with the given digest from the repository.
func (c *Client) DeleteManifest(ctx context.Context, repoName string, digest oci.Digest) error {
	req, err := newRequest(ctx, http.MethodDelete, manifestURL(repoName, digest.String()), nil, deleteScope(repoName))
	if err != nil {
		return err
	}
	return c.delete(req)
}

// DeleteTag deletes the manifest with the given tag from the repository.
func (c *Client) DeleteTag(ctx context.Context, repoName string, tagName string) error {
	req, err := newRequest(ctx, http.MethodDelete, manifestURL(repoName, tagName), nil, deleteScope(repoName))
	if err != nil {
		return err
	}
	return c.delete(req)
}

func (c *Client) delete(req *http.Request) error {
	resp, err := c.do(req, http.StatusAccepted)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}
