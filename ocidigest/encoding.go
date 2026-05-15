package ocidigest

import (
	"encoding/hex"
	"fmt"
)

type digestEncoding interface {
	Encode([]byte) (string, error)
	Validate(string) bool
}

type encodeHex struct {
	Len int
}

func (e encodeHex) Encode(p []byte) (string, error) {
	if len(p)*2 != e.Len {
		return "", ErrEncodingInvalid
	}
	return hex.EncodeToString(p), nil
}

func (e encodeHex) Validate(s string) bool {
	if len(s) != e.Len {
		return false
	}
	for i := range len(s) {
		c := s[i]
		if ('0' <= c && c <= '9') || ('a' <= c && c <= 'f') {
			continue
		}
		return false
	}
	return true
}

func validateEncoding(enc digestEncoding, size int) error {
	if enc == nil {
		return fmt.Errorf("%w: nil encoding", ErrEncodingInvalid)
	}
	if size <= 0 {
		return fmt.Errorf("%w: invalid size %d", ErrHashInvalid, size)
	}
	zero := make([]byte, size)
	encoded, err := enc.Encode(zero)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrEncodingInvalid, err)
	}
	if !enc.Validate(encoded) {
		return fmt.Errorf("%w: encoded test value did not validate", ErrEncodingInvalid)
	}
	return nil
}
