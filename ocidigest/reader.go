package ocidigest

import (
	"errors"
	"io"
)

// Reader wraps an io.Reader and records the digest of bytes read through it.
type Reader struct {
	r io.Reader
	m *multiDigester
}

// NewReader returns a digesting reader for r.
func NewReader(r io.Reader, algs ...Algorithm) (*Reader, error) {
	if r == nil {
		return nil, ErrReaderInvalid
	}
	m, err := newMultiDigesterWithDefault(algs...)
	if err != nil {
		return nil, err
	}
	return &Reader{r: r, m: m}, nil
}

// NewReaderFromState returns a digesting reader restored from state.
func NewReaderFromState(r io.Reader, st State) (*Reader, error) {
	if r == nil {
		return nil, ErrReaderInvalid
	}
	m, err := newMultiDigesterFromState(st)
	if err != nil {
		return nil, err
	}
	return &Reader{r: r, m: m}, nil
}

// Read reads from the underlying reader and updates the digest.
func (r *Reader) Read(p []byte) (int, error) {
	if r == nil || r.r == nil || r.m == nil {
		return 0, ErrReaderInvalid
	}
	n, err := r.r.Read(p)
	if n > 0 {
		if _, writeErr := r.m.Write(p[:n]); writeErr != nil {
			err = errors.Join(err, writeErr)
		}
	}
	return n, err
}

// Algorithms returns the algorithms in digest output order.
func (r *Reader) Algorithms() []Algorithm {
	if r == nil || r.m == nil {
		return nil
	}
	return r.m.Algorithms()
}

// Digest returns the default digest of bytes read so far.
func (r *Reader) Digest() (Digest, error) {
	if r == nil || r.m == nil {
		return "", ErrReaderInvalid
	}
	return r.m.defaultDigest()
}

// DigestFor returns the current digest for alg.
func (r *Reader) DigestFor(alg Algorithm) (Digest, error) {
	if r == nil || r.m == nil {
		return "", ErrReaderInvalid
	}
	return r.m.Digest(alg)
}

// Digests returns all current digests in algorithm order.
func (r *Reader) Digests() ([]Digest, error) {
	if r == nil || r.m == nil {
		return nil, ErrReaderInvalid
	}
	return r.m.Digests()
}

// State returns the current digest state.
func (r *Reader) State() (State, error) {
	if r == nil || r.m == nil {
		return State{}, ErrReaderInvalid
	}
	return r.m.States()
}

// Size returns the number of bytes read and digested.
func (r *Reader) Size() int64 {
	if r == nil || r.m == nil {
		return 0
	}
	return r.m.Size()
}
