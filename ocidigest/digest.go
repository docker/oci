package ocidigest

import (
	"fmt"
	"hash"
	"io"
	"strings"
)

// Digest is an OCI-compatible digest in the form "<algorithm>:<encoded-hash>".
type Digest string

// Parse parses and validates a digest string.
func Parse(s string) (Digest, error) {
	if s == "" {
		return "", ErrDigestInvalid
	}
	alg, enc, ok := strings.Cut(s, ":")
	if !ok || alg == "" || enc == "" {
		return "", fmt.Errorf("%w: %q", ErrDigestInvalid, s)
	}
	return newDigestFromEncoded(Algorithm{name: alg}, enc)
}

// FromBytes returns the canonical digest of p.
func FromBytes(p []byte) Digest {
	return Canonical.FromBytes(p)
}

// FromReader reads r to EOF and returns its canonical digest.
func FromReader(r io.Reader) (Digest, error) {
	return Canonical.FromReader(r)
}

func newDigest(alg Algorithm, h hash.Hash) (Digest, error) {
	if h == nil {
		return "", ErrHashInvalid
	}
	info, err := lookupAlgorithm(alg.name)
	if err != nil {
		return "", err
	}
	if h.Size() != info.size {
		return "", fmt.Errorf("%w: %q has hash size %d, want %d", ErrHashInvalid, alg.name, h.Size(), info.size)
	}
	enc, err := info.encoding.Encode(h.Sum(nil))
	if err != nil {
		return "", err
	}
	return Digest(info.name + ":" + enc), nil
}

func newDigestFromEncoded(alg Algorithm, encoded string) (Digest, error) {
	info, err := lookupAlgorithm(alg.name)
	if err != nil {
		return "", err
	}
	if !info.encoding.Validate(encoded) {
		return "", fmt.Errorf("%w: %q", ErrEncodingInvalid, encoded)
	}
	return Digest(info.name + ":" + encoded), nil
}

// Algorithm returns the digest algorithm.
func (d Digest) Algorithm() Algorithm {
	alg, _, _ := strings.Cut(string(d), ":")
	return Algorithm{name: alg}
}

// Encoded returns the encoded hash portion of the digest.
func (d Digest) Encoded() string {
	_, enc, ok := strings.Cut(string(d), ":")
	if !ok {
		return ""
	}
	return enc
}

// String returns the string form of the digest.
func (d Digest) String() string {
	return string(d)
}

// Equal reports whether d and cmp are identical.
func (d Digest) Equal(cmp Digest) bool {
	return d == cmp
}

// Validate checks whether d is a valid non-zero digest.
func (d Digest) Validate() error {
	alg, enc, ok := strings.Cut(string(d), ":")
	if !ok || alg == "" || enc == "" {
		return ErrDigestInvalid
	}
	info, err := lookupAlgorithm(alg)
	if err != nil {
		return err
	}
	if !info.encoding.Validate(enc) {
		return fmt.Errorf("%w: %q", ErrEncodingInvalid, enc)
	}
	return nil
}
