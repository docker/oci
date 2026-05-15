package ocidigest

import (
	"errors"
	"io"
)

// Writer wraps an io.Writer and records the digest of bytes written through it.
type Writer struct {
	w io.Writer
	m *multiDigester
}

// NewWriter returns a digesting writer. If w is nil, writes are only digested.
func NewWriter(w io.Writer, algs ...Algorithm) (*Writer, error) {
	m, err := newMultiDigesterWithDefault(algs...)
	if err != nil {
		return nil, err
	}
	return &Writer{w: w, m: m}, nil
}

// NewWriterFromState returns a digesting writer restored from state.
func NewWriterFromState(w io.Writer, st State) (*Writer, error) {
	m, err := newMultiDigesterFromState(st)
	if err != nil {
		return nil, err
	}
	return &Writer{w: w, m: m}, nil
}

// Write writes p to the underlying writer, if any, and updates the digest.
func (w *Writer) Write(p []byte) (int, error) {
	if w == nil || w.m == nil {
		return 0, ErrWriterInvalid
	}
	n := len(p)
	var err error
	if w.w != nil {
		n, err = w.w.Write(p)
	}
	if n > 0 {
		if _, writeErr := w.m.Write(p[:n]); writeErr != nil {
			err = errors.Join(err, writeErr)
		}
	}
	return n, err
}

// Algorithms returns the algorithms in digest output order.
func (w *Writer) Algorithms() []Algorithm {
	if w == nil || w.m == nil {
		return nil
	}
	return w.m.Algorithms()
}

// Digest returns the default digest of bytes written so far.
func (w *Writer) Digest() (Digest, error) {
	if w == nil || w.m == nil {
		return "", ErrWriterInvalid
	}
	return w.m.defaultDigest()
}

// DigestFor returns the current digest for alg.
func (w *Writer) DigestFor(alg Algorithm) (Digest, error) {
	if w == nil || w.m == nil {
		return "", ErrWriterInvalid
	}
	return w.m.Digest(alg)
}

// Digests returns all current digests in algorithm order.
func (w *Writer) Digests() ([]Digest, error) {
	if w == nil || w.m == nil {
		return nil, ErrWriterInvalid
	}
	return w.m.Digests()
}

// State returns the current digest state.
func (w *Writer) State() (State, error) {
	if w == nil || w.m == nil {
		return State{}, ErrWriterInvalid
	}
	return w.m.States()
}

// Size returns the number of bytes written and digested.
func (w *Writer) Size() int64 {
	if w == nil || w.m == nil {
		return 0
	}
	return w.m.Size()
}
