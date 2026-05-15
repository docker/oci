package ocidigest

import (
	"encoding"
	"fmt"
	"hash"
)

const stateEncodingBinaryMarshaler = "binary-marshaler-v1"

// State is serialized digest state for short-term resume.
type State struct {
	States []DigestState `json:"states"`
}

// DigestState is a serialized digest state for one algorithm.
type DigestState struct {
	Algorithm string `json:"algorithm"`
	Encoding  string `json:"encoding"`
	Offset    int64  `json:"offset"`
	Payload   []byte `json:"payload"`
}

func newDigesterFromState(st DigestState) (Digester, error) {
	info, err := lookupAlgorithm(st.Algorithm)
	if err != nil {
		return nil, err
	}
	if st.Offset < 0 {
		return nil, fmt.Errorf("%w: negative offset %d", ErrStateInvalid, st.Offset)
	}
	if info.newState != nil {
		h, err := info.newState(st)
		if err != nil {
			return nil, err
		}
		if err := validateRestoredHash(info, h); err != nil {
			return nil, err
		}
		return &digester{info: info, hash: h, size: st.Offset}, nil
	}
	if st.Encoding != stateEncodingBinaryMarshaler {
		return nil, fmt.Errorf("%w: %q", ErrStateUnsupported, st.Encoding)
	}
	h := info.newHash()
	u, ok := h.(encoding.BinaryUnmarshaler)
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrStateUnsupported, info.name)
	}
	if err := u.UnmarshalBinary(st.Payload); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrStateInvalid, err)
	}
	return &digester{info: info, hash: h, size: st.Offset}, nil
}

func marshalHashState(info algorithmInfo, h hash.Hash) (string, []byte, error) {
	if info.marshalState != nil {
		encoding, payload, err := info.marshalState(h)
		if err != nil {
			return "", nil, err
		}
		if encoding == "" {
			return "", nil, fmt.Errorf("%w: empty state encoding", ErrStateInvalid)
		}
		return encoding, payload, nil
	}
	m, ok := h.(encoding.BinaryMarshaler)
	if !ok {
		return "", nil, ErrStateUnsupported
	}
	payload, err := m.MarshalBinary()
	if err != nil {
		return "", nil, fmt.Errorf("%w: %v", ErrStateInvalid, err)
	}
	return stateEncodingBinaryMarshaler, payload, nil
}

func validateRestoredHash(info algorithmInfo, h hash.Hash) error {
	if h == nil {
		return ErrHashInvalid
	}
	if h.Size() != info.size {
		return fmt.Errorf("%w: %q has hash size %d, want %d", ErrHashInvalid, info.name, h.Size(), info.size)
	}
	return nil
}
