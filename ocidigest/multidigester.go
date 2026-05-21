package ocidigest

import (
	"fmt"
	"io"
)

type multiDigester struct {
	order     []Algorithm
	digesters map[Algorithm]Digester
}

func newMultiDigesterWithDefault(algs ...Algorithm) (*multiDigester, error) {
	if len(algs) == 0 {
		algs = []Algorithm{Canonical}
	}
	return newMultiDigester(algs...)
}

func newMultiDigester(algs ...Algorithm) (*multiDigester, error) {
	if len(algs) == 0 {
		return nil, ErrAlgorithmInvalidName
	}
	m := &multiDigester{
		order:     make([]Algorithm, 0, len(algs)),
		digesters: make(map[Algorithm]Digester, len(algs)),
	}
	for _, alg := range algs {
		if alg.String() == "" {
			return nil, ErrAlgorithmInvalidName
		}
		if _, ok := m.digesters[alg]; ok {
			return nil, fmt.Errorf("%w: %q", ErrAlgorithmDuplicate, alg)
		}
		d, err := alg.New()
		if err != nil {
			return nil, err
		}
		m.order = append(m.order, alg)
		m.digesters[alg] = d
	}
	return m, nil
}

func newMultiDigesterFromState(st State) (*multiDigester, error) {
	if len(st.States) == 0 {
		return nil, ErrStateInvalid
	}
	m := &multiDigester{
		order:     make([]Algorithm, 0, len(st.States)),
		digesters: make(map[Algorithm]Digester, len(st.States)),
	}
	var offset int64 = -1
	for _, state := range st.States {
		d, err := newDigesterFromState(state)
		if err != nil {
			return nil, err
		}
		alg := d.Algorithm()
		if _, ok := m.digesters[alg]; ok {
			return nil, fmt.Errorf("%w: %q", ErrAlgorithmDuplicate, alg)
		}
		if offset == -1 {
			offset = d.Size()
		} else if d.Size() != offset {
			return nil, fmt.Errorf("%w: mismatched multi-state offsets", ErrStateInvalid)
		}
		m.order = append(m.order, alg)
		m.digesters[alg] = d
	}
	return m, nil
}

func (m *multiDigester) Write(p []byte) (int, error) {
	if m == nil || len(m.order) == 0 {
		return 0, ErrHashInvalid
	}
	for _, alg := range m.order {
		if n, err := m.digesters[alg].Write(p); err != nil {
			return n, err
		} else if n != len(p) {
			return n, io.ErrShortWrite
		}
	}
	return len(p), nil
}

func (m *multiDigester) Algorithms() []Algorithm {
	if m == nil {
		return nil
	}
	return append([]Algorithm(nil), m.order...)
}

func (m *multiDigester) Digest(alg Algorithm) (Digest, error) {
	if m == nil {
		return "", ErrHashInvalid
	}
	d, ok := m.digesters[alg]
	if !ok {
		return "", fmt.Errorf("%w: %q", ErrAlgorithmUnknown, alg)
	}
	return d.Digest()
}

func (m *multiDigester) defaultDigest() (Digest, error) {
	if m == nil || len(m.order) == 0 {
		return "", ErrHashInvalid
	}
	return m.Digest(m.order[0])
}

func (m *multiDigester) Digests() ([]Digest, error) {
	if m == nil {
		return nil, ErrHashInvalid
	}
	digests := make([]Digest, 0, len(m.order))
	for _, alg := range m.order {
		digest, err := m.digesters[alg].Digest()
		if err != nil {
			return nil, err
		}
		digests = append(digests, digest)
	}
	return digests, nil
}

func (m *multiDigester) Reset() {
	if m == nil {
		return
	}
	for _, alg := range m.order {
		m.digesters[alg].Reset()
	}
}

func (m *multiDigester) Size() int64 {
	if m == nil || len(m.order) == 0 {
		return 0
	}
	return m.digesters[m.order[0]].Size()
}

func (m *multiDigester) State(alg Algorithm) (DigestState, error) {
	if m == nil {
		return DigestState{}, ErrHashInvalid
	}
	d, ok := m.digesters[alg]
	if !ok {
		return DigestState{}, fmt.Errorf("%w: %q", ErrAlgorithmUnknown, alg)
	}
	return d.State()
}

func (m *multiDigester) States() (State, error) {
	if m == nil {
		return State{}, ErrHashInvalid
	}
	states := make([]DigestState, 0, len(m.order))
	for _, alg := range m.order {
		st, err := m.digesters[alg].State()
		if err != nil {
			return State{}, err
		}
		states = append(states, st)
	}
	return State{States: states}, nil
}
