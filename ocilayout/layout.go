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

// Package ocilayout provides an [oci.Interface] implementation backed by an
// OCI Image Layout directory.
package ocilayout

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/docker/oci"
	"github.com/docker/oci/ociref"
)

const (
	layoutVersion     = "1.0.0"
	refNameAnnotation = "org.opencontainers.image.ref.name"
)

// Options holds configuration for opening a layout registry.
type Options struct {
	// DefaultRepo is used when reading layout entries whose
	// org.opencontainers.image.ref.name annotation is empty or tag-only.
	DefaultRepo string
}

// Registry is an OCI Image Layout backed implementation of [oci.Interface].
type Registry struct {
	*oci.Funcs
	mu    sync.Mutex
	dir   string
	opts  Options
	state *layoutState
}

var _ oci.Interface = (*Registry)(nil)

type layoutState struct {
	dir   string
	index oci.IndexOrManifest
}

type layoutVersionFile struct {
	ImageLayoutVersion string `json:"imageLayoutVersion"`
}

// New opens an OCI Image Layout registry rooted at dir.
//
// Missing layout files are not created by New. Write operations create the
// required layout files and directories lazily.
func New(dir string, opts *Options) (*Registry, error) {
	if opts == nil {
		opts = &Options{}
	}
	if dir == "" {
		return nil, fmt.Errorf("directory must not be empty")
	}
	if opts.DefaultRepo != "" && !ociref.IsValidRepository(opts.DefaultRepo) {
		return nil, oci.ErrNameInvalid
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	r := &Registry{
		dir:  abs,
		opts: *opts,
	}
	if _, err := r.loadLayoutLocked(false); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *Registry) layoutForRepoLocked(repo string, forWrite bool) (*layoutState, error) {
	if !ociref.IsValidRepository(repo) {
		return nil, oci.ErrNameInvalid
	}
	return r.loadLayoutLocked(forWrite)
}

func (r *Registry) loadLayoutLocked(forWrite bool) (*layoutState, error) {
	if r.state != nil {
		if forWrite {
			if err := ensureLayout(r.state.dir); err != nil {
				return nil, err
			}
		}
		return r.state, nil
	}
	if forWrite {
		if err := ensureLayout(r.dir); err != nil {
			return nil, err
		}
	}
	index, err := loadIndex(r.dir)
	if err != nil {
		if !forWrite && errors.Is(err, os.ErrNotExist) {
			index = emptyIndex()
		} else {
			return nil, err
		}
	}
	st := &layoutState{
		dir:   r.dir,
		index: index,
	}
	r.state = st
	return st, nil
}

func ensureLayout(dir string) error {
	if err := os.MkdirAll(filepath.Join(dir, "blobs", "uploads"), 0o700); err != nil {
		return err
	}
	layoutPath := filepath.Join(dir, "oci-layout")
	if _, err := os.Stat(layoutPath); errors.Is(err, os.ErrNotExist) {
		data, err := json.Marshal(layoutVersionFile{ImageLayoutVersion: layoutVersion})
		if err != nil {
			return err
		}
		if err := os.WriteFile(layoutPath, append(data, '\n'), 0o600); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	indexPath := filepath.Join(dir, "index.json")
	if _, err := os.Stat(indexPath); errors.Is(err, os.ErrNotExist) {
		return saveIndex(dir, emptyIndex())
	} else if err != nil {
		return err
	}
	return validateLayoutFile(dir)
}

func loadIndex(dir string) (oci.IndexOrManifest, error) {
	if err := validateLayoutFile(dir); err != nil {
		return oci.IndexOrManifest{}, err
	}
	blobDir := filepath.Join(dir, "blobs")
	if info, err := os.Stat(blobDir); err != nil {
		return oci.IndexOrManifest{}, err
	} else if !info.IsDir() {
		return oci.IndexOrManifest{}, fmt.Errorf("%s is not a directory", blobDir)
	}
	data, err := os.ReadFile(filepath.Join(dir, "index.json"))
	if err != nil {
		return oci.IndexOrManifest{}, err
	}
	var index oci.IndexOrManifest
	if err := json.Unmarshal(data, &index); err != nil {
		return oci.IndexOrManifest{}, fmt.Errorf("cannot unmarshal index.json: %v", err)
	}
	if index.MediaType == "" {
		index.MediaType = oci.MediaTypeImageIndex
	}
	if index.SchemaVersion == 0 {
		index.SchemaVersion = 2
	}
	if index.MediaType != oci.MediaTypeImageIndex && index.MediaType != oci.MediaTypeDockerManifestList {
		return oci.IndexOrManifest{}, fmt.Errorf("index.json has unexpected media type %q", index.MediaType)
	}
	if err := index.Validate(); err != nil {
		return oci.IndexOrManifest{}, fmt.Errorf("invalid index.json: %v", err)
	}
	return index, nil
}

func validateLayoutFile(dir string) error {
	data, err := os.ReadFile(filepath.Join(dir, "oci-layout"))
	if err != nil {
		return err
	}
	var layout layoutVersionFile
	if err := json.Unmarshal(data, &layout); err != nil {
		return fmt.Errorf("cannot unmarshal oci-layout: %v", err)
	}
	if layout.ImageLayoutVersion != layoutVersion {
		return fmt.Errorf("unsupported OCI image layout version %q", layout.ImageLayoutVersion)
	}
	return nil
}

func saveIndex(dir string, index oci.IndexOrManifest) error {
	if index.SchemaVersion == 0 {
		index.SchemaVersion = 2
	}
	if index.MediaType == "" {
		index.MediaType = oci.MediaTypeImageIndex
	}
	data, err := json.MarshalIndent(index, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, "index.json.tmp")
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, "index.json"))
}

func emptyIndex() oci.IndexOrManifest {
	return oci.IndexOrManifest{
		SchemaVersion: 2,
		MediaType:     oci.MediaTypeImageIndex,
	}
}

func (r *Registry) refFor(repo string, tag string, digest oci.Digest) string {
	if tag == "" {
		return repo + "@" + digest.String()
	}
	return repo + ":" + tag
}

func (r *Registry) parseRef(ref string) (repo string, tag string, err error) {
	if ref == "" {
		if r.opts.DefaultRepo == "" {
			return "", "", fmt.Errorf("empty ref.name")
		}
		return r.opts.DefaultRepo, "", nil
	}
	if ociref.IsValidTag(ref) && !strings.ContainsAny(ref, "/:") {
		if r.opts.DefaultRepo != "" {
			return r.opts.DefaultRepo, ref, nil
		}
		return "", "", fmt.Errorf("ambiguous ref.name %q", ref)
	}
	if parsed, ok := parseFullRef(ref); ok {
		return parsed.repo, parsed.tag, nil
	}
	return "", "", fmt.Errorf("invalid ref.name %q", ref)
}

type parsedRef struct {
	repo string
	tag  string
}

func parseFullRef(ref string) (parsedRef, bool) {
	name, digest, hasDigest := strings.Cut(ref, "@")
	if hasDigest && !ociref.IsValidDigest(digest) {
		return parsedRef{}, false
	}
	if ociref.IsValidRepository(name) {
		return parsedRef{repo: name}, true
	}
	i := strings.LastIndex(name, ":")
	if i <= 0 {
		return parsedRef{}, false
	}
	repo, tag := name[:i], name[i+1:]
	if !ociref.IsValidRepository(repo) || !ociref.IsValidTag(tag) {
		return parsedRef{}, false
	}
	return parsedRef{repo: repo, tag: tag}, true
}

func contextErr(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}
