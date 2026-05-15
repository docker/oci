# OCI Go modules

This repository holds functionality related to OCI (Open Container Initiative).

The top-level package (`oci`) defines a [Go interface](./interface.go) that encapsulates the operations provided by an OCI registry — reading blobs and manifests, pushing content, listing tags, and more.

Full reference documentation can be found at [pkg.go.dev/github.com/docker/oci](https://pkg.go.dev/github.com/docker/oci).

The aim is to provide an ergonomic interface for defining and layering OCI registry implementations.

Although the API is fairly stable, it's still in v0 currently, so incompatible changes can't be ruled out.

The code was originally derived from [cue-labs/oci](https://github.com/cue-labs/oci) which was originally derived from
the [go-containerregistry](https://pkg.go.dev/github.com/google/go-containerregistry/pkg/registry) package, but has
considerably diverged since then.

## Purpose

There are other libraries that exist for working with OCI registries, and they are great! However, as we look to invest 
in Docker Hub and add new features ahead of them getting standardized into the OCI spec, we need a library that can be 
used to interact with those features.

## Packages

| Package | Description |
|---------|-------------|
| `oci` | Core interface (`oci.Interface`) and types shared across all packages. |
| `ociclient` | HTTP client that implements `oci.Interface` against a remote OCI registry. |
| `ociserver` | HTTP server that serves the OCI distribution protocol on top of any `oci.Interface`. |
| `ocimem` | Lightweight in-memory `oci.Interface` implementation, useful for testing and caching. |
| `ociauth` | Authentication transport implementing the Docker/OCI token flow, plus helpers for loading credentials from Docker config files. |
| `ocifilter` | Wrappers that expose restricted or transformed views of a registry (read-only, immutable, namespace prefix, custom access control). |
| `ociunify` | Combines two registries into a single unified `oci.Interface`, with configurable read policy. |
| `ocilarge` | Parallel multi-range download (and upload) for large blobs, automatically tuning chunk size to available bandwidth. |
| `ocidebug` | Registry wrapper that logs every operation — useful for tracing and debugging. |
| `ociref` | Reference and digest parsing/validation utilities. |

The server currently passes the [OCI distribution conformance tests](https://pkg.go.dev/github.com/opencontainers/distribution-spec/conformance).

## Usage

### List tags on Docker Hub

```go
package main

import (
	"context"
	"fmt"

	"github.com/docker/oci/ociauth"
	"github.com/docker/oci/ociclient"
)

func main() {
	cf, err := ociauth.Load(nil)
	if err != nil {
		panic(err)
	}

	ocl, err := ociclient.New("index.docker.io", &ociclient.Options{
		Transport: ociauth.NewStdTransport(ociauth.StdTransportParams{
			Config: cf,
		}),
	})
	if err != nil {
		panic(err)
	}

	tags := ocl.Tags(context.Background(), "library/alpine", nil)
	for tag, err := range tags {
		if err != nil {
			panic(err)
		}
		fmt.Printf("%s\n", tag)
	}
}
```

### Fetch a manifest by tag

```go
package main

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/docker/oci"
	"github.com/docker/oci/ociauth"
	"github.com/docker/oci/ociclient"
)

func main() {
	cf, err := ociauth.Load(nil)
	if err != nil {
		panic(err)
	}

	ocl, err := ociclient.New("index.docker.io", &ociclient.Options{
		Transport: ociauth.NewStdTransport(ociauth.StdTransportParams{
			Config: cf,
		}),
	})
	if err != nil {
		panic(err)
	}

	// Fetch the manifest for alpine:latest.
	r, err := ocl.GetTag(context.Background(), "library/alpine", "latest")
	if err != nil {
		panic(err)
	}
	defer r.Close()

	fmt.Printf("media type: %s\n", r.Descriptor().MediaType)
	fmt.Printf("digest:     %s\n", r.Descriptor().Digest)

	var manifest oci.IndexOrManifest
	if err := json.NewDecoder(r).Decode(&manifest); err != nil {
		panic(err)
	}
	fmt.Printf("config digest: %s\n", manifest.Config.Digest)
	for i, layer := range manifest.Layers {
		fmt.Printf("layer[%d]: %s (%d bytes)\n", i, layer.Digest, layer.Size)
	}
}
```

### Pull a blob (image layer)

```go
package main

import (
	"context"
	"io"
	"os"

	"github.com/docker/oci/ociauth"
	"github.com/docker/oci/ociclient"
)

func main() {
	cf, err := ociauth.Load(nil)
	if err != nil {
		panic(err)
	}

	ocl, err := ociclient.New("index.docker.io", &ociclient.Options{
		Transport: ociauth.NewStdTransport(ociauth.StdTransportParams{
			Config: cf,
		}),
	})
	if err != nil {
		panic(err)
	}

	// Pull a specific blob by digest.
	const repo = "library/alpine"
	const layerDigest = "sha256:bca4290a96390d7a6fc6f2f9929370d06f8dfcacba591c76e3d5c5044e7f420c"

	blob, err := ocl.GetBlob(context.Background(), repo, layerDigest)
	if err != nil {
		panic(err)
	}
	defer blob.Close()

	f, err := os.Create("layer.tar.gz")
	if err != nil {
		panic(err)
	}
	defer f.Close()

	if _, err := io.Copy(f, blob); err != nil {
		panic(err)
	}
}
```

### Serve a local in-memory registry over HTTP

```go
package main

import (
	"net/http"

	"github.com/docker/oci/ocimem"
	"github.com/docker/oci/ociserver"
)

func main() {
	backend := ocimem.New()
	handler := ociserver.New(backend, nil)
	http.ListenAndServe(":5000", handler)
}
```
