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

package ocilayout

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/docker/oci/ocidigest"
	"github.com/docker/oci/ociref"
)

// FindLayout splits path into an OCI image layout directory and image
// reference.
//
// It first looks for OCI layout marker files in directory prefixes of path. If
// one or more marker files are found, the deepest directory whose remaining
// suffix parses as a reference is used as the base directory. If no marker file
// matches, FindLayout falls back to treating the last path component as the
// reference and the preceding path as the base directory.
func FindLayout(path string) (baseDir string, ref ociref.Reference, err error) {
	if path == "" {
		return "", ociref.Reference{}, fmt.Errorf("path must not be empty")
	}

	candidates := layoutCandidates(path)
	for i := len(candidates) - 1; i >= 0; i-- {
		c := candidates[i]
		ok, err := hasLayoutFile(c.baseDir)
		if err != nil {
			return "", ociref.Reference{}, err
		}
		if !ok {
			continue
		}
		ref, err := parseLayoutReference(filepath.ToSlash(c.ref))
		if err != nil {
			continue
		}
		return c.baseDir, ref, nil
	}

	fallback := candidates[len(candidates)-1]
	ref, err = parseLayoutReference(filepath.ToSlash(fallback.ref))
	if err != nil {
		return "", ociref.Reference{}, err
	}
	return fallback.baseDir, ref, nil
}

func parseLayoutReference(refStr string) (ociref.Reference, error) {
	var ref ociref.Reference
	repoAndTag, digestStr, hasDigest := strings.Cut(refStr, "@")
	if hasDigest {
		if digestStr == "" {
			return ociref.Reference{}, fmt.Errorf("invalid reference syntax (%q)", refStr)
		}
		digest, err := ocidigest.Parse(digestStr)
		if err != nil {
			return ociref.Reference{}, fmt.Errorf("invalid digest %q: %v", digestStr, err)
		}
		if err := digest.Validate(); err != nil {
			return ociref.Reference{}, fmt.Errorf("invalid digest %q: %v", digest, err)
		}
		ref.Digest = digest
	}
	repo, tag, hasTag := strings.Cut(repoAndTag, ":")
	if hasTag {
		if strings.Contains(tag, ":") || strings.Contains(tag, "/") {
			return ociref.Reference{}, fmt.Errorf("invalid reference syntax (%q)", refStr)
		}
		if !ociref.IsValidTag(tag) {
			return ociref.Reference{}, fmt.Errorf("invalid tag %q", tag)
		}
		ref.Tag = tag
	}
	if !ociref.IsValidRepository(repo) {
		return ociref.Reference{}, fmt.Errorf("invalid reference syntax (%q)", refStr)
	}
	if len(repo) > 255 {
		return ociref.Reference{}, fmt.Errorf("repository name too long")
	}
	ref.Repository = repo
	return ref, nil
}

type layoutCandidate struct {
	baseDir string
	ref     string
}

func layoutCandidates(path string) []layoutCandidate {
	var candidates []layoutCandidate
	if !filepath.IsAbs(path) {
		candidates = append(candidates, layoutCandidate{
			baseDir: ".",
			ref:     path,
		})
	}
	for i := range len(path) {
		if !isPathSeparator(path[i]) || i == len(path)-1 {
			continue
		}
		candidates = append(candidates, layoutCandidate{
			baseDir: baseDirForSplit(path, i),
			ref:     path[i+1:],
		})
	}
	if len(candidates) == 0 {
		return []layoutCandidate{{
			baseDir: ".",
			ref:     path,
		}}
	}
	return candidates
}

func isPathSeparator(c byte) bool {
	return c == '/' || c == filepath.Separator
}

func baseDirForSplit(path string, sep int) string {
	if sep == 0 {
		return path[:1]
	}
	base := path[:sep]
	if base == "." || base == ".." {
		return path[:sep+1]
	}
	return base
}

func hasLayoutFile(dir string) (bool, error) {
	info, err := os.Stat(filepath.Join(dir, "oci-layout"))
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return !info.IsDir(), nil
}
