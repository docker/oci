package ocidigest

import "errors"

var (
	// ErrAlgorithmExists indicates that an algorithm is already registered.
	ErrAlgorithmExists = errors.New("ocidigest: algorithm is already registered")
	// ErrAlgorithmInvalidName indicates that an algorithm name is syntactically invalid.
	ErrAlgorithmInvalidName = errors.New("ocidigest: invalid algorithm name")
	// ErrAlgorithmUnknown indicates that an algorithm has not been registered.
	ErrAlgorithmUnknown = errors.New("ocidigest: algorithm is not registered")
	// ErrAlgorithmDuplicate indicates that an algorithm was specified more than once.
	ErrAlgorithmDuplicate = errors.New("ocidigest: duplicate algorithm")
	// ErrDigestInvalid indicates that a digest is invalid.
	ErrDigestInvalid = errors.New("ocidigest: digest is invalid")
	// ErrDigestMismatch indicates that calculated content did not match the expected digest.
	ErrDigestMismatch = errors.New("ocidigest: digest mismatch")
	// ErrEncodingInvalid indicates that a digest encoding is invalid.
	ErrEncodingInvalid = errors.New("ocidigest: encoded digest is invalid")
	// ErrHashInvalid indicates that a hash is invalid.
	ErrHashInvalid = errors.New("ocidigest: invalid hash")
	// ErrReaderInvalid indicates that a reader is invalid.
	ErrReaderInvalid = errors.New("ocidigest: invalid reader")
	// ErrStateInvalid indicates that digest state is invalid.
	ErrStateInvalid = errors.New("ocidigest: state is invalid")
	// ErrStateUnsupported indicates that digest state cannot be restored.
	ErrStateUnsupported = errors.New("ocidigest: state is not supported")
	// ErrWriterInvalid indicates that a writer is invalid.
	ErrWriterInvalid = errors.New("ocidigest: invalid writer")
)
