package ocidigest

import (
	"crypto/sha256"
	"crypto/sha512"
	"fmt"
	"hash"
	"io"
	"regexp"
	"sync"
)

// Algorithm identifies a digest algorithm.
type Algorithm struct {
	name string
}

// RegisterOptions defines a digest algorithm implementation.
type RegisterOptions struct {
	Name         string
	Size         int
	NewHash      func() hash.Hash
	MarshalState func(hash.Hash) (encoding string, payload []byte, err error)
	NewState     func(DigestState) (hash.Hash, error)
}

type algorithmInfo struct {
	name         string
	size         int
	encoding     digestEncoding
	newHash      func() hash.Hash
	marshalState func(hash.Hash) (encoding string, payload []byte, err error)
	newState     func(DigestState) (hash.Hash, error)
}

var (
	// SHA256 is the OCI sha256 digest algorithm.
	SHA256 Algorithm
	// SHA512 is the OCI sha512 digest algorithm.
	SHA512 Algorithm
	// Canonical is the default digest algorithm.
	Canonical Algorithm

	algorithmsMu sync.RWMutex
	algorithms   = map[string]algorithmInfo{}

	algorithmNameRegexp = regexp.MustCompile(`^[a-z0-9]+([+._-][a-z0-9]+)*$`)
)

func init() {
	var err error
	SHA256, err = RegisterAlgorithm(RegisterOptions{
		Name:    "sha256",
		Size:    sha256.Size,
		NewHash: sha256.New,
	})
	if err != nil {
		panic(err)
	}
	SHA512, err = RegisterAlgorithm(RegisterOptions{
		Name:    "sha512",
		Size:    sha512.Size,
		NewHash: sha512.New,
	})
	if err != nil {
		panic(err)
	}
	Canonical = SHA256
}

// LookupAlgorithm returns a previously registered algorithm by name.
func LookupAlgorithm(name string) (Algorithm, error) {
	info, err := lookupAlgorithm(name)
	if err != nil {
		return Algorithm{}, err
	}
	return Algorithm{name: info.name}, nil
}

// RegisterAlgorithm registers a digest algorithm.
func RegisterAlgorithm(opts RegisterOptions) (Algorithm, error) {
	if !algorithmNameRegexp.MatchString(opts.Name) {
		return Algorithm{}, fmt.Errorf("%w: %q", ErrAlgorithmInvalidName, opts.Name)
	}
	if algorithmRegistered(opts.Name) {
		return Algorithm{}, fmt.Errorf("%w: %q", ErrAlgorithmExists, opts.Name)
	}
	if opts.NewHash == nil {
		return Algorithm{}, fmt.Errorf("%w: %q", ErrHashInvalid, opts.Name)
	}
	h := opts.NewHash()
	if h == nil {
		return Algorithm{}, fmt.Errorf("%w: %q", ErrHashInvalid, opts.Name)
	}
	if h.Size() <= 0 {
		return Algorithm{}, fmt.Errorf("%w: %q", ErrHashInvalid, opts.Name)
	}
	if n, err := h.Write([]byte("ocidigest")); err != nil || n != len("ocidigest") {
		return Algorithm{}, fmt.Errorf("%w: %q write failed", ErrHashInvalid, opts.Name)
	}
	h.Reset()
	size := opts.Size
	if size == 0 {
		size = h.Size()
	}
	if h.Size() != size {
		return Algorithm{}, fmt.Errorf("%w: %q has hash size %d, want %d", ErrHashInvalid, opts.Name, h.Size(), size)
	}
	enc := encodeHex{Len: size * 2}
	if err := validateEncoding(enc, size); err != nil {
		return Algorithm{}, err
	}

	algorithmsMu.Lock()
	defer algorithmsMu.Unlock()
	if _, ok := algorithms[opts.Name]; ok {
		return Algorithm{}, fmt.Errorf("%w: %q", ErrAlgorithmExists, opts.Name)
	}
	algorithms[opts.Name] = algorithmInfo{
		name:         opts.Name,
		size:         size,
		encoding:     enc,
		newHash:      opts.NewHash,
		marshalState: opts.MarshalState,
		newState:     opts.NewState,
	}
	return Algorithm{name: opts.Name}, nil
}

func algorithmRegistered(name string) bool {
	algorithmsMu.RLock()
	defer algorithmsMu.RUnlock()
	_, ok := algorithms[name]
	return ok
}

// String returns the algorithm name.
func (a Algorithm) String() string {
	return a.name
}

// Size returns the digest size in bytes.
func (a Algorithm) Size() int {
	info, err := lookupAlgorithm(a.name)
	if err != nil {
		return 0
	}
	return info.size
}

// New returns a new digester for the algorithm.
func (a Algorithm) New() (Digester, error) {
	info, err := lookupAlgorithm(a.name)
	if err != nil {
		return nil, err
	}
	return newDigester(info, 0), nil
}

// FromBytes returns the digest of p.
func (a Algorithm) FromBytes(p []byte) Digest {
	d, err := a.New()
	if err != nil {
		return ""
	}
	_, _ = d.Write(p)
	digest, err := d.Digest()
	if err != nil {
		return ""
	}
	return digest
}

// FromReader reads r to EOF and returns its digest.
func (a Algorithm) FromReader(r io.Reader) (Digest, error) {
	d, err := a.New()
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(d, r); err != nil {
		return "", err
	}
	return d.Digest()
}

func lookupAlgorithm(name string) (algorithmInfo, error) {
	if name == "" {
		return algorithmInfo{}, ErrAlgorithmInvalidName
	}
	if !algorithmNameRegexp.MatchString(name) {
		return algorithmInfo{}, fmt.Errorf("%w: %q", ErrAlgorithmInvalidName, name)
	}
	algorithmsMu.RLock()
	defer algorithmsMu.RUnlock()
	info, ok := algorithms[name]
	if !ok {
		return algorithmInfo{}, fmt.Errorf("%w: %q", ErrAlgorithmUnknown, name)
	}
	return info, nil
}
