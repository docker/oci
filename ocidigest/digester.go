package ocidigest

import (
	"hash"
	"io"
)

// Digester calculates a digest incrementally.
type Digester interface {
	io.Writer
	Algorithm() Algorithm
	Digest() (Digest, error)
	Reset()
	Size() int64
	State() (DigestState, error)
}

type digester struct {
	info algorithmInfo
	hash hash.Hash
	size int64
}

func newDigester(info algorithmInfo, size int64) *digester {
	return &digester{
		info: info,
		hash: info.newHash(),
		size: size,
	}
}

func (d *digester) Write(p []byte) (int, error) {
	if d == nil || d.hash == nil {
		return 0, ErrHashInvalid
	}
	n, err := d.hash.Write(p)
	d.size += int64(n)
	return n, err
}

func (d *digester) Algorithm() Algorithm {
	if d == nil {
		return Algorithm{}
	}
	return Algorithm{name: d.info.name}
}

func (d *digester) Digest() (Digest, error) {
	if d == nil || d.hash == nil {
		return "", ErrHashInvalid
	}
	return newDigest(Algorithm{name: d.info.name}, d.hash)
}

func (d *digester) Reset() {
	if d == nil || d.hash == nil {
		return
	}
	d.hash.Reset()
	d.size = 0
}

func (d *digester) Size() int64 {
	if d == nil {
		return 0
	}
	return d.size
}

func (d *digester) State() (DigestState, error) {
	if d == nil || d.hash == nil {
		return DigestState{}, ErrHashInvalid
	}
	encoding, payload, err := marshalHashState(d.info, d.hash)
	if err != nil {
		return DigestState{}, err
	}
	return DigestState{
		Algorithm: d.info.name,
		Encoding:  encoding,
		Offset:    d.size,
		Payload:   payload,
	}, nil
}
