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
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/docker/oci"
	"github.com/docker/oci/ociauth"
)

// Tags returns an iterator over tags in the given repository.
func (c *Client) Tags(ctx context.Context, repoName string, params *oci.TagsParameters) iter.Seq2[string, error] {
	var startAfter string
	limit := -1
	if params != nil {
		startAfter = params.StartAfter
		limit = params.Limit
	}
	return pager(ctx, c, pageRequest{
		URL:   tagsURL(repoName, limit, startAfter),
		Scope: pullScope(repoName),
		Limit: limit,
		Next: func(last any) string {
			return tagsURL(repoName, limit, fmt.Sprint(last))
		},
	}, true, func(resp *http.Response) ([]string, error) {
		data, err := io.ReadAll(resp.Body)
		if err != nil {
			return nil, err
		}
		var tagsResponse struct {
			Repo string   `json:"name"`
			Tags []string `json:"tags"`
		}
		if err := json.Unmarshal(data, &tagsResponse); err != nil {
			return nil, fmt.Errorf("cannot unmarshal tags list response: %v", err)
		}
		return tagsResponse.Tags, nil
	})
}

// Referrers returns an iterator over manifests that refer to the given digest.
func (c *Client) Referrers(ctx context.Context, repoName string, digest oci.Digest, params *oci.ReferrersParameters) iter.Seq2[oci.Descriptor, error] {
	var artifactType string
	if params != nil {
		artifactType = params.ArtifactType
	}
	return pager(ctx, c, pageRequest{
		URL:   referrersURL(repoName, digest, artifactType),
		Scope: pullScope(repoName),
		Limit: -1,
	}, false, func(resp *http.Response) ([]oci.Descriptor, error) {
		body := resp.Body
		if resp.StatusCode == http.StatusNotFound {
			body.Close()
			body = nil
			// Fall back to the referrers tag API.
			// From https://github.com/opencontainers/distribution-spec/blob/main/spec.md#unavailable-referrers-api :
			//	A client querying the referrers API and receiving a
			//	404 Not Found MUST fallback to using an image index
			//	pushed to a tag described by the referrers tag
			//	schema.
			r, err := c.GetTag(ctx, repoName, referrersTag(digest))
			if err != nil {
				if errors.Is(err, oci.ErrManifestUnknown) {
					return nil, nil
				}
				return nil, err
			}
			body = r
		}
		data, err := io.ReadAll(body)
		body.Close()
		if err != nil {
			return nil, err
		}
		var referrersResponse oci.IndexOrManifest
		if err := json.Unmarshal(data, &referrersResponse); err != nil {
			return nil, fmt.Errorf("cannot unmarshal referrers response: %v", err)
		}
		if referrersResponse.MediaType == "" {
			referrersResponse.MediaType = oci.MediaTypeImageIndex
		}
		if err := referrersResponse.Validate(); err != nil {
			return nil, fmt.Errorf("invalid referrers response: %v", err)
		}
		if artifactType == "" || resp.Header.Get("OCI-Filters-Applied") == "artifactType" {
			return referrersResponse.Manifests, nil
		}
		// The server hasn't filtered the responses, so we must.
		// TODO is it OK to assume that the index contains correctly populated
		// artifact type and attributes fields when we've fallen back to the referrer tags API?
		// If not, we might have to retrieve all the individual manifests to check that info.
		manifests := slices.DeleteFunc(referrersResponse.Manifests, func(desc oci.Descriptor) bool {
			return desc.ArtifactType != artifactType
		})
		return manifests, nil
	}, http.StatusOK, http.StatusNotFound)
}

type pageRequest struct {
	URL   string
	Scope ociauth.Scope
	Limit int
	Next  func(last any) string
}

// pager returns an iterator for a list entry point. It starts by sending the given
// initial request and parses each response into its component items using
// parseResponse. It tries to use the Link header in each response to continue
// the iteration, falling back to using the "last" query parameter if
// canUseLast is true.
func pager[T any](ctx context.Context, c *Client, initialReq pageRequest, canUseLast bool, parseResponse func(*http.Response) ([]T, error), okStatuses ...int) iter.Seq2[T, error] {
	if len(okStatuses) == 0 {
		okStatuses = []int{http.StatusOK}
	}
	return func(yield func(T, error) bool) {
		// We assume that the same auth scope is applicable to all page requests.
		req, err := newRequest(ctx, http.MethodGet, initialReq.URL, nil, initialReq.Scope)
		if err != nil {
			yield(*new(T), err)
			return
		}
		for {
			resp, err := c.do(req, okStatuses...)
			if err != nil {
				yield(*new(T), err)
				return
			}
			items, err := parseResponse(resp)
			resp.Body.Close()
			if err != nil {
				yield(*new(T), err)
				return
			}
			for _, item := range items {
				if !yield(item, nil) {
					return
				}
			}
			if len(items) == 0 || (initialReq.Limit > 0 && len(items) < initialReq.Limit) {
				// From the distribution spec:
				//     The response to such a request MAY return fewer than <int> results,
				//     but only when the total number of tags attached to the repository
				//     is less than <int>.
				return
			}
			req, err = nextLink(ctx, resp, initialReq, canUseLast, items[len(items)-1])
			if err != nil {
				yield(*new(T), fmt.Errorf("invalid Link header in response: %v", err))
				return
			}
			if req == nil {
				// No link found; assume there are no more items.
				return
			}
		}
	}
}

// nextLink ttries to form a request that can be sent to obtain the next page
// in a set of list results.
// The given response holds the response received from the previous
// list request; initialReq holds the request that initiated the listing,
// and last holds the final item returned in the previous response.
func nextLink[T any](ctx context.Context, resp *http.Response, initialReq pageRequest, canUseLast bool, last T) (*http.Request, error) {
	link0 := resp.Header.Get("Link")
	if link0 == "" {
		if !canUseLast || initialReq.Next == nil {
			return nil, nil
		}
		// This is beyond the first page and there was no Link
		// in the previous response (the standard doesn't mandate
		// one), so add a "last" parameter to the initial request.
		req, err := newRequest(ctx, http.MethodGet, initialReq.Next(last), nil, initialReq.Scope)
		if err != nil {
			// Given that we could form the initial request, this should
			// never happen.
			return nil, fmt.Errorf("cannot form next request: %v", err)
		}
		return req, nil
	}
	// Parse the link header according to RFC 5988.
	// TODO perhaps we shouldn't ignore the relation type?
	link, ok := strings.CutPrefix(link0, "<")
	if !ok {
		return nil, fmt.Errorf("no initial < character in Link=%q", link0)
	}
	link, _, ok = strings.Cut(link, ">")
	if !ok {
		return nil, fmt.Errorf("no > character in Link=%q", link0)
	}
	// Parse it with respect to the originating request, as it's probably relative.
	linkURL, err := resp.Request.URL.Parse(link)
	if err != nil {
		return nil, fmt.Errorf("invalid URL in Link=%q", link0)
	}
	return newRequest(ctx, http.MethodGet, linkURL.String(), nil, initialReq.Scope)
}

// referrersTag returns the referrers tag for the given digest, as described
// in https://github.com/opencontainers/distribution-spec/blob/main/spec.md#referrers-tag-schema
func referrersTag(digest oci.Digest) string {
	// It's hard to know what the spec means by "with any characters not allowed by <reference> tags replaced with -",
	// because different characters are allowed in different contexts (for example, a dot character
	// is allowed except when it's at the start.
	// In practice, however, the set of characters is very limited, and the only
	// disallowed character in common use is :, so just use a naive algorithm.
	return truncateAndMap(digest.Algorithm().String(), 32) + "-" + truncateAndMap(digest.Encoded(), 64)
}

func truncateAndMap(s string, n int) string {
	// regexp: [a-zA-Z0-9_][a-zA-Z0-9._-]{0,127}

	s = strings.Map(func(r rune) rune {
		switch {
		case 'a' <= r && r <= 'z':
			return r
		case 'A' <= r && r <= 'Z':
			return r
		case '0' <= r && r <= '9':
			return r
		case r == '.' || r == '_' || r == '-':
			return r
		}
		return '-'
	}, s)
	// Note: it's OK to use n as a byte index because the
	// above Map has eliminated all non-ASCII characters.
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func tagsURL(repo string, limit int, last string) string {
	return "/v2/" + repo + "/tags/list" + listQuery(limit, last)
}

func referrersURL(repo string, digest oci.Digest, artifactType string) string {
	u := "/v2/" + repo + "/referrers/" + digest.String()
	if artifactType != "" {
		u += "?" + url.Values{"artifactType": {artifactType}}.Encode()
	}
	return u
}

func listQuery(limit int, last string) string {
	q := make(url.Values)
	if limit >= 0 {
		q.Set("n", fmt.Sprint(limit))
	}
	if last != "" {
		q.Set("last", last)
	}
	if len(q) == 0 {
		return ""
	}
	return "?" + q.Encode()
}
