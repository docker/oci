# ocilayout

Package `ocilayout` provides an `oci.Interface` implementation backed by an
[OCI Image Layout](https://github.com/opencontainers/image-spec/blob/main/image-layout.md)
directory on disk.

Use it when you want registry-like reads and writes without running a registry
server. It stores manifests in `index.json`, blobs under `blobs/`, and records
named references with the standard `org.opencontainers.image.ref.name`
annotation.

## Shared Layout

`New` opens a single OCI layout directory that can hold multiple repositories.
Missing layout files are not created when the registry is opened; write
operations create `oci-layout`, `index.json`, and blob directories lazily.

```go
package main

import (
	"context"
	"fmt"

	"github.com/docker/oci"
	"github.com/docker/oci/ocilayout"
)

func main() {
	reg, err := ocilayout.New("./layout", &ocilayout.Options{})
	if err != nil {
		panic(err)
	}

	tags, err := oci.All(reg.Tags(context.Background(), "example/app", nil))
	if err != nil {
		panic(err)
	}
	fmt.Println(tags)
}
```

When reading existing layouts that use tag-only `ref.name` annotations, set
`DefaultRepo` so those entries can be associated with a repository.

```go
reg, err := ocilayout.New("./layout", &ocilayout.Options{
	DefaultRepo: "example/app",
})
```

## Per-Repository Layouts

`NewPerRepository` stores each repository in its own nested OCI layout under the
given directory. For repository `example/app`, the underlying layout lives at
`<dir>/example/app`.

```go
reg, err := ocilayout.NewPerRepository("./layouts", &ocilayout.PerRepoOptions{})
if err != nil {
	panic(err)
}
```

The per-repository constructor has its own options type so its API can evolve
independently from `New`.

## Finding Layouts From Paths

`FindLayout` splits a user-supplied path into the base directory for `New` and
the image reference suffix. It first looks for `oci-layout` marker files in path
prefixes and uses the deepest matching layout. If no marker exists, it falls
back to treating the last path component as the reference.

```go
baseDir, ref, err := ocilayout.FindLayout("./foo/bar:baz")
// baseDir == "./foo"
// ref.Repository == "bar"
// ref.Tag == "baz"
```

```go
baseDir, ref, err := ocilayout.FindLayout("./one/two/three/four:tag")
// If ./one/two/oci-layout exists:
// baseDir == "./one/two"
// ref.Repository == "three/four"
// ref.Tag == "tag"
```

References may include tags, digests, or both:

```go
baseDir, ref, err := ocilayout.FindLayout("./layout/repo:tag@sha256:...")
```
