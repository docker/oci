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
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/docker/oci"
	"github.com/docker/oci/ocidigest"
)

func blobPath(dir string, digest oci.Digest) (string, error) {
	if err := digest.Validate(); err != nil {
		return "", fmt.Errorf("%w: %v", oci.ErrDigestInvalid, err)
	}
	alg := digest.Algorithm().String()
	enc := digest.Encoded()
	if alg == "" || enc == "" {
		return "", oci.ErrDigestInvalid
	}
	return filepath.Join(dir, "blobs", alg, enc), nil
}

func ensureBlobExists(dir string, digest oci.Digest) error {
	path, err := blobPath(dir, digest)
	if err != nil {
		return err
	}
	_, err = os.Stat(path) // #nosec G703 -- path is derived from a validated OCI digest.
	return err
}

func writeBlob(dir string, desc oci.Descriptor, r io.Reader) (oci.Descriptor, error) {
	if desc.Size < 0 {
		return oci.Descriptor{}, oci.ErrSizeInvalid
	}
	final, err := blobPath(dir, desc.Digest)
	if err != nil {
		return oci.Descriptor{}, err
	}
	if existing, err := os.Stat(final); err == nil { // #nosec G703 -- path is derived from a validated OCI digest.
		if existing.Size() != desc.Size {
			return oci.Descriptor{}, oci.ErrSizeInvalid
		}
		return desc, nil
	} else if !os.IsNotExist(err) {
		return oci.Descriptor{}, err
	}
	if err := os.MkdirAll(filepath.Join(dir, "blobs", "uploads"), 0o700); err != nil {
		return oci.Descriptor{}, err
	}
	tmp := filepath.Join(dir, "blobs", "uploads", newUploadID())
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return oci.Descriptor{}, err
	}
	cleanup := true
	defer func() {
		f.Close()
		if cleanup {
			os.Remove(tmp)
		}
	}()
	dw, err := ocidigest.NewWriter(f, desc.Digest.Algorithm())
	if err != nil {
		return oci.Descriptor{}, err
	}
	if _, err := io.Copy(dw, r); err != nil {
		return oci.Descriptor{}, err
	}
	got, err := dw.Digest()
	if err != nil {
		return oci.Descriptor{}, err
	}
	if got != desc.Digest {
		return oci.Descriptor{}, fmt.Errorf("digest mismatch: %w", oci.ErrDigestInvalid)
	}
	if dw.Size() != desc.Size {
		return oci.Descriptor{}, fmt.Errorf("size mismatch: %w", oci.ErrSizeInvalid)
	}
	if err := f.Close(); err != nil {
		return oci.Descriptor{}, err
	}
	if err := os.MkdirAll(filepath.Dir(final), 0o700); err != nil { // #nosec G703 -- path is derived from a validated OCI digest.
		return oci.Descriptor{}, err
	}
	if err := os.Rename(tmp, final); err != nil { // #nosec G703 -- final path is derived from a validated OCI digest.
		return oci.Descriptor{}, err
	}
	cleanup = false
	return desc, nil
}

func writeBlobBytes(dir string, desc oci.Descriptor, data []byte) (oci.Descriptor, error) {
	return writeBlob(dir, desc, bytes.NewReader(data))
}

type layoutReader struct {
	*os.File
	desc oci.Descriptor
}

func (r *layoutReader) Descriptor() oci.Descriptor {
	return r.desc
}

type sectionReadCloser struct {
	*io.SectionReader
	f    *os.File
	desc oci.Descriptor
}

func (r *sectionReadCloser) Close() error {
	return r.f.Close()
}

func (r *sectionReadCloser) Descriptor() oci.Descriptor {
	return r.desc
}

func openBlob(dir string, desc oci.Descriptor) (oci.BlobReader, error) {
	path, err := blobPath(dir, desc.Digest)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	return &layoutReader{File: f, desc: desc}, nil
}

func openBlobRange(dir string, desc oci.Descriptor, o0, o1 int64) (oci.BlobReader, error) {
	if o1 < 0 || o1 > desc.Size {
		o1 = desc.Size
	}
	if o0 < 0 || o0 > o1 {
		return nil, fmt.Errorf("invalid range [%d, %d]; have [%d, %d]: %w", o0, o1, 0, desc.Size, oci.ErrRangeInvalid)
	}
	path, err := blobPath(dir, desc.Digest)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	return &sectionReadCloser{
		SectionReader: io.NewSectionReader(f, o0, o1-o0),
		f:             f,
		desc:          desc,
	}, nil
}

func newUploadID() string {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(buf[:])
}
