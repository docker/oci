package ocidigest

import (
	"fmt"
	"io"
)

// VerifierReader wraps an io.Reader and verifies the stream at EOF.
type VerifierReader struct {
	r        *Reader
	expected Digest
	final    bool
	verified bool
}

// NewVerifierReader returns a reader that verifies bytes read from r.
func NewVerifierReader(r io.Reader, expected Digest) (*VerifierReader, error) {
	if r == nil {
		return nil, ErrReaderInvalid
	}
	if err := expected.Validate(); err != nil {
		return nil, err
	}
	dr, err := NewReader(r, expected.Algorithm())
	if err != nil {
		return nil, err
	}
	return &VerifierReader{r: dr, expected: expected}, nil
}

// Read reads from the underlying reader and updates the verifier.
func (r *VerifierReader) Read(p []byte) (int, error) {
	if r == nil || r.r == nil {
		return 0, ErrReaderInvalid
	}
	if r.final {
		return 0, r.finalErr()
	}
	n, err := r.r.Read(p)
	if n > 0 {
		if err == io.EOF {
			r.final = true
			return n, nil
		}
		return n, err
	}
	if err == io.EOF {
		r.final = true
		return 0, r.finalErr()
	}
	return n, err
}

// Digest returns the expected digest.
func (r *VerifierReader) Digest() Digest {
	if r == nil {
		return ""
	}
	return r.expected
}

// Calculated returns the digest calculated from bytes read so far.
func (r *VerifierReader) Calculated() (Digest, error) {
	if r == nil || r.r == nil {
		return "", ErrReaderInvalid
	}
	return r.r.Digest()
}

// Verified reports whether EOF has been reached and the digest matched.
func (r *VerifierReader) Verified() bool {
	if r == nil {
		return false
	}
	return r.verified
}

func (r *VerifierReader) finalErr() error {
	got, err := r.Calculated()
	if err != nil {
		return err
	}
	if r.expected != "" && got == r.expected {
		r.verified = true
		return io.EOF
	}
	r.verified = false
	return fmt.Errorf("%w: expected %s, got %s", ErrDigestMismatch, r.expected, got)
}
