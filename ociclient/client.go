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

// Package ociclient provides an implementation of oci.Interface that
// uses HTTP to talk to the remote registry.
package ociclient

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/docker/oci"

	"github.com/docker/oci/ociauth"
	"github.com/docker/oci/ocidigest"
	"github.com/docker/oci/ociref"
)

// debug enables logging.
// TODO this should be configurable in the API.
const debug = false

// Options holds configuration for creating a new OCI registry client.
type Options struct {
	// DebugID is used to prefix any log messages printed by the client.
	DebugID string

	// Transport is used to make HTTP requests. The context passed
	// to its RoundTrip method will have an appropriate
	// [ociauth.RequestInfo] value added, suitable for consumption
	// by the transport created by [ociauth.NewStdTransport]. If
	// Transport is nil, [http.DefaultTransport] will be used.
	Transport http.RoundTripper

	// Insecure specifies whether an http scheme will be used to
	// address the host instead of https.
	Insecure bool

	// Specifies a user agent string to use when making requests. Defaults to "docker/oci"
	UserAgent string
}

// See https://github.com/google/go-containerregistry/issues/1091
// for an early report of the issue alluded to below.

var debugID int32

// New returns a client implementation that uses the OCI HTTP API. A nil opts
// parameter is equivalent to a pointer to zero Options.
//
// The host specifies the host name to talk to; it may
// optionally be a host:port pair.
func New(host string, opts0 *Options) (*Client, error) {
	var opts Options
	if opts0 != nil {
		opts = *opts0
	}
	if opts.DebugID == "" {
		opts.DebugID = fmt.Sprintf("id%d", atomic.AddInt32(&debugID, 1))
	}
	if opts.Transport == nil {
		opts.Transport = http.DefaultTransport
	}
	if opts.UserAgent == "" {
		opts.UserAgent = "docker/oci"
	}
	if host == "docker.io" {
		host = "registry-1.docker.io"
	}
	// Check that it's a valid host by forming a URL from it and checking that it matches.
	u, err := url.Parse("https://" + host + "/path")
	if err != nil {
		return nil, fmt.Errorf("invalid host %q", host)
	}
	if u.Host != host {
		return nil, fmt.Errorf("invalid host %q (does not correctly form a host part of a URL)", host)
	}
	if opts.Insecure {
		u.Scheme = "http"
	}
	return &Client{
		httpHost:   host,
		httpScheme: u.Scheme,
		httpClient: &http.Client{
			Transport: opts.Transport,
		},
		userAgent: opts.UserAgent,
		debugID:   opts.DebugID,
	}, nil
}

// Client is an OCI registry client that uses HTTP to talk to the remote
// registry.
type Client struct {
	*oci.Funcs
	httpScheme   string
	httpHost     string
	httpClient   *http.Client
	userAgent    string
	debugID      string
	listPageSize int
}

var _ oci.Interface = (*Client)(nil)

// RequestOptions holds options for [Client.Do].
type RequestOptions struct {
	// Scope is the authorization scope required by the request. It is attached
	// to the request context for use by transports created by
	// [ociauth.NewStdTransport].
	Scope ociauth.Scope
}

type descriptorRequired byte

const (
	requireSize descriptorRequired = 1 << iota
	requireDigest
)

// descriptorFromResponse tries to form a descriptor from an HTTP response,
// filling in the Digest field using knownDigest if it's not present.
//
// Note: this implies that the Digest field will be empty if there is no
// digest in the response and knownDigest is empty.
func descriptorFromResponse(resp *http.Response, knownDigest oci.Digest, require descriptorRequired) (oci.Descriptor, error) {
	contentType := resp.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	size := int64(0)
	if (require & requireSize) != 0 {
		if resp.StatusCode == http.StatusPartialContent {
			contentRange := resp.Header.Get("Content-Range")
			if contentRange == "" {
				return oci.Descriptor{}, fmt.Errorf("no Content-Range in partial content response")
			}
			i := strings.LastIndex(contentRange, "/")
			if i == -1 {
				return oci.Descriptor{}, fmt.Errorf("malformed Content-Range %q", contentRange)
			}
			contentSize, err := strconv.ParseInt(contentRange[i+1:], 10, 64)
			if err != nil {
				return oci.Descriptor{}, fmt.Errorf("malformed Content-Range %q", contentRange)
			}
			size = contentSize
		} else {
			if resp.ContentLength < 0 {
				return oci.Descriptor{}, fmt.Errorf("unknown content length")
			}
			size = resp.ContentLength
		}
	}
	var digest oci.Digest
	if digestHeader := resp.Header.Get("Docker-Content-Digest"); digestHeader != "" {
		var err error
		digest, err = ocidigest.Parse(digestHeader)
		if err != nil {
			return oci.Descriptor{}, fmt.Errorf("bad digest %q found in response: %v", digestHeader, err)
		}
		if !ociref.IsValidDigest(digest.String()) {
			return oci.Descriptor{}, fmt.Errorf("bad digest %q found in response", digest)
		}
	} else {
		digest = knownDigest
	}
	if (require&requireDigest) != 0 && digest == "" {
		return oci.Descriptor{}, fmt.Errorf("no digest found in response")
	}
	return oci.Descriptor{
		Digest:    digest,
		MediaType: contentType,
		Size:      size,
	}, nil
}

func newBlobReader(r io.ReadCloser, desc oci.Descriptor) *blobReader {
	digester, _ := desc.Digest.Algorithm().New()
	return &blobReader{
		r:        r,
		digester: digester,
		desc:     desc,
		verify:   true,
	}
}

func newBlobReaderUnverified(r io.ReadCloser, desc oci.Descriptor) *blobReader {
	br := newBlobReader(r, desc)
	br.verify = false
	return br
}

type blobReader struct {
	r        io.ReadCloser
	n        int64
	digester ocidigest.Digester
	desc     oci.Descriptor
	verify   bool
}

func (r *blobReader) Descriptor() oci.Descriptor {
	return r.desc
}

func (r *blobReader) Read(buf []byte) (int, error) {
	n, err := r.r.Read(buf)
	r.n += int64(n)
	if _, writeErr := r.digester.Write(buf[:n]); writeErr != nil {
		return n, writeErr
	}
	if err == nil {
		if r.n > r.desc.Size {
			// Fail early when the blob is too big; we can do that even
			// when we're not verifying for other use cases.
			return n, fmt.Errorf("blob size exceeds content length %d: %w", r.desc.Size, oci.ErrSizeInvalid)
		}
		return n, nil
	}
	if err != io.EOF {
		return n, err
	}
	if !r.verify {
		return n, io.EOF
	}
	if r.n != r.desc.Size {
		return n, fmt.Errorf("blob size mismatch (%d/%d): %w", r.n, r.desc.Size, oci.ErrSizeInvalid)
	}
	gotDigest, err := r.digester.Digest()
	if err != nil {
		return n, err
	}
	if gotDigest != r.desc.Digest {
		return n, fmt.Errorf("digest mismatch when reading blob")
	}
	return n, io.EOF
}

func (r *blobReader) Close() error {
	return r.r.Close()
}

// TODO make this list configurable.
var knownManifestMediaTypes = []string{
	oci.MediaTypeImageManifest,
	oci.MediaTypeImageIndex,
	"application/vnd.oci.artifact.manifest.v1+json", // deprecated.
	"application/vnd.docker.distribution.manifest.v1+json",
	oci.MediaTypeDockerManifest,
	oci.MediaTypeDockerManifestList,
	// Technically this wildcard should be sufficient, but it isn't
	// recognized by some registries.
	"*/*",
}

// Do performs an HTTP request against the registry.
//
// If req.URL has no scheme or host, Do fills them in from the client's
// configured registry. It also sets the User-Agent header and, when the request
// has a body, sets Expect: 100-continue.
//
// Do does not interpret HTTP response statuses. Callers that want OCI error
// handling can use [CheckResponse] or [ErrorFromResponse]. On a nil error, the
// caller is responsible for closing resp.Body.
func (c *Client) Do(req *http.Request, opts *RequestOptions) (*http.Response, error) {
	if req.URL.Scheme == "" {
		req.URL.Scheme = c.httpScheme
	}
	if req.URL.Host == "" {
		req.URL.Host = c.httpHost
	}
	req.Header.Set("User-Agent", c.userAgent)
	if opts != nil {
		req = req.WithContext(ociauth.ContextWithRequestInfo(req.Context(), ociauth.RequestInfo{
			RequiredScope: opts.Scope,
		}))
	}

	if req.Body != nil {
		// Ensure that the body isn't consumed until the
		// server has responded that it will receive it.
		// This means that we can retry requests even when we've
		// got a consume-once-only io.Reader, such as
		// when pushing blobs.
		req.Header.Set("Expect", "100-continue")
	}
	var buf bytes.Buffer
	if debug {
		fmt.Fprintf(&buf, "Client.Do: %s %s {{\n", req.Method, req.URL)
		fmt.Fprintf(&buf, "\tBODY: %#v\n", req.Body)
		for k, v := range req.Header {
			fmt.Fprintf(&buf, "\t%s: %q\n", k, v)
		}
		c.logf("%s", buf.Bytes())
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cannot do HTTP request: %w", err)
	}
	if debug {
		buf.Reset()
		fmt.Fprintf(&buf, "} -> %s {\n", resp.Status)
		for k, v := range resp.Header {
			fmt.Fprintf(&buf, "\t%s: %q\n", k, v)
		}
		data, _ := io.ReadAll(resp.Body)
		if len(data) > 0 {
			fmt.Fprintf(&buf, "\tBODY: %q\n", data)
		}
		fmt.Fprintf(&buf, "}}\n")
		resp.Body.Close()
		resp.Body = io.NopCloser(bytes.NewReader(data))
		c.logf("%s", buf.Bytes())
	}
	return resp, nil
}

// CheckResponse checks that resp has an acceptable status.
//
// If okStatuses is empty, any 2xx response is accepted. Otherwise, only the
// supplied statuses are accepted. Non-2xx responses are returned as OCI errors.
// CheckResponse reads but does not close resp.Body when constructing an error.
func CheckResponse(resp *http.Response, okStatuses ...int) error {
	if len(okStatuses) == 0 {
		if isOKStatus(resp.StatusCode) {
			return nil
		}
		return ErrorFromResponse(resp)
	}
	if slices.Contains(okStatuses, resp.StatusCode) {
		return nil
	}
	if !isOKStatus(resp.StatusCode) {
		return ErrorFromResponse(resp)
	}
	return unexpectedStatusError(resp.StatusCode)
}

func (c *Client) do(req *http.Request, okStatuses ...int) (*http.Response, error) {
	resp, err := c.Do(req, nil)
	if err != nil {
		return nil, err
	}
	if err := CheckResponse(resp, okStatuses...); err != nil {
		resp.Body.Close()
		return nil, err
	}
	return resp, nil
}

func (c *Client) logf(f string, a ...any) {
	log.Printf("ociclient %s: %s", c.debugID, fmt.Sprintf(f, a...))
}

func locationFromResponse(resp *http.Response) (*url.URL, error) {
	location := resp.Header.Get("Location")
	if location == "" {
		return nil, fmt.Errorf("no Location found in response")
	}
	u, err := url.Parse(location)
	if err != nil {
		return nil, fmt.Errorf("invalid Location URL found in response")
	}
	return resp.Request.URL.ResolveReference(u), nil
}

func isOKStatus(code int) bool {
	return code/100 == 2
}

func closeOnError(err *error, r io.Closer) {
	if *err != nil {
		r.Close()
	}
}

func unexpectedStatusError(code int) error {
	return fmt.Errorf("unexpected HTTP response code %d", code)
}

func newRequest(ctx context.Context, method string, u string, body io.Reader, scope ociauth.Scope) (*http.Request, error) {
	ctx = ociauth.ContextWithRequestInfo(ctx, ociauth.RequestInfo{
		RequiredScope: scope,
	})
	return http.NewRequestWithContext(ctx, method, u, body)
}

func newManifestRequest(ctx context.Context, method string, repo string, tagOrDigest string, scope ociauth.Scope) (*http.Request, error) {
	req, err := newRequest(ctx, method, manifestURL(repo, tagOrDigest), nil, scope)
	if err != nil {
		return nil, err
	}
	// When getting manifests, some servers won't return the content unless
	// there's an Accept header, so add all manifest kinds that we know about.
	if method == http.MethodGet || method == http.MethodHead {
		req.Header["Accept"] = knownManifestMediaTypes
	}
	return req, nil
}

func pullScope(repo string) ociauth.Scope {
	return ociauth.NewScope(ociauth.ResourceScope{
		ResourceType: ociauth.TypeRepository,
		Resource:     repo,
		Action:       ociauth.ActionPull,
	})
}

func pushScope(repo string) ociauth.Scope {
	return ociauth.NewScope(ociauth.ResourceScope{
		ResourceType: ociauth.TypeRepository,
		Resource:     repo,
		Action:       ociauth.ActionPull,
	}, ociauth.ResourceScope{
		ResourceType: ociauth.TypeRepository,
		Resource:     repo,
		Action:       ociauth.ActionPush,
	})
}

func deleteScope(repo string) ociauth.Scope {
	return ociauth.NewScope(ociauth.ResourceScope{
		ResourceType: ociauth.TypeRepository,
		Resource:     repo,
		Action:       ociauth.ActionDelete,
	})
}

func mountScope(fromRepo, toRepo string) ociauth.Scope {
	return ociauth.NewScope(ociauth.ResourceScope{
		ResourceType: ociauth.TypeRepository,
		Resource:     toRepo,
		Action:       ociauth.ActionPull,
	}, ociauth.ResourceScope{
		ResourceType: ociauth.TypeRepository,
		Resource:     toRepo,
		Action:       ociauth.ActionPush,
	}, ociauth.ResourceScope{
		ResourceType: ociauth.TypeRepository,
		Resource:     fromRepo,
		Action:       ociauth.ActionPull,
	})
}

func catalogScope() ociauth.Scope {
	return ociauth.NewScope(ociauth.CatalogScope)
}
