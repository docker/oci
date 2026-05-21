package ocidigest

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"hash"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const (
	helloWorldSHA256 = "sha256:b94d27b9934d3e08a52e52d7da7dabfac484efe37a5380ee9088f7ace2efcde9"
	helloWorldSHA512 = "sha512:309ecc489c12d6eb4cc40f50c902f2b4d0ed77ee511a7c7a9bcd3ca86d4cd86f989dd35bc5ff499670da34255b45b0cfd830e81f605dcf7dc5542e93ae9cd76f"
)

func TestDigestParseValidateAndComparable(t *testing.T) {
	d, err := Parse(helloWorldSHA256)
	require.NoError(t, err)

	require.Equal(t, "sha256", d.Algorithm().String())
	require.Equal(t, strings.TrimPrefix(helloWorldSHA256, "sha256:"), d.Encoded())
	require.Equal(t, helloWorldSHA256, d.String())
	require.NoError(t, d.Validate())

	byDigest := map[Digest]string{d: "hello"}
	require.Equal(t, "hello", byDigest[d])
	require.True(t, d.Equal(d))

	_, err = Parse("sha256:B94D27B9934D3E08A52E52D7DA7DABFADEB100F1FDDC5EF7A679E9C58EE68F")
	require.ErrorIs(t, err, ErrEncodingInvalid)

	_, err = Parse("not-a-digest")
	require.ErrorIs(t, err, ErrDigestInvalid)

	_, err = Parse("")
	require.ErrorIs(t, err, ErrDigestInvalid)

	_, err = Parse("bad/alg:abcdef")
	require.ErrorIs(t, err, ErrAlgorithmInvalidName)

	var zero Digest
	require.Empty(t, zero)
	require.Empty(t, zero.String())
	require.ErrorIs(t, zero.Validate(), ErrDigestInvalid)
}

func TestDigestJSON(t *testing.T) {
	d := FromBytes([]byte("hello world"))

	data, err := json.Marshal(d)
	require.NoError(t, err)
	require.JSONEq(t, `"`+helloWorldSHA256+`"`, string(data))

	var fromJSON Digest
	require.NoError(t, json.Unmarshal(data, &fromJSON))
	require.Equal(t, d, fromJSON)

	var zero Digest
	data, err = json.Marshal(zero)
	require.NoError(t, err)
	require.JSONEq(t, `""`, string(data))

	require.NoError(t, json.Unmarshal([]byte(`"sha256:bad"`), &fromJSON))
	require.Equal(t, Digest("sha256:bad"), fromJSON)
	require.ErrorIs(t, fromJSON.Validate(), ErrEncodingInvalid)
}

func TestDigestCreationAPIs(t *testing.T) {
	data := []byte("hello world")

	require.Equal(t, helloWorldSHA256, FromBytes(data).String())
	require.Equal(t, helloWorldSHA256, SHA256.FromBytes(data).String())
	require.Equal(t, helloWorldSHA512, SHA512.FromBytes(data).String())

	fromReader, err := FromReader(bytes.NewReader(data))
	require.NoError(t, err)
	require.Equal(t, FromBytes(data), fromReader)

	sha512FromReader, err := SHA512.FromReader(strings.NewReader("hello world"))
	require.NoError(t, err)
	require.Equal(t, helloWorldSHA512, sha512FromReader.String())

	dr, err := NewReader(bytes.NewReader(data), SHA256)
	require.NoError(t, err)
	read, err := io.ReadAll(dr)
	require.NoError(t, err)
	require.Equal(t, data, read)
	got, err := dr.Digest()
	require.NoError(t, err)
	require.Equal(t, FromBytes(data), got)
	require.Equal(t, int64(len(data)), dr.Size())

	var buf bytes.Buffer
	w, err := NewWriter(&buf, SHA256)
	require.NoError(t, err)
	n, err := w.Write(data)
	require.NoError(t, err)
	require.Equal(t, len(data), n)
	require.Equal(t, string(data), buf.String())
	got, err = w.Digest()
	require.NoError(t, err)
	require.Equal(t, FromBytes(data), got)
	require.Equal(t, int64(len(data)), w.Size())
}

func TestVerifierReaderMismatchOnEOF(t *testing.T) {
	expected := FromBytes([]byte("hello world"))
	vr, err := NewVerifierReader(strings.NewReader("goodbye"), expected)
	require.NoError(t, err)

	_, err = io.Copy(io.Discard, vr)
	require.ErrorIs(t, err, ErrDigestMismatch)
	require.False(t, vr.Verified())

	got, calcErr := vr.Calculated()
	require.NoError(t, calcErr)
	require.NotEqual(t, expected, got)
}

func TestVerifierReaderReturnsFinalBytesBeforeFinalVerification(t *testing.T) {
	expected := FromBytes([]byte("abc"))
	vr, err := NewVerifierReader(&eofWithBytesReader{data: []byte("abc")}, expected)
	require.NoError(t, err)

	buf := make([]byte, 8)
	n, err := vr.Read(buf)
	require.NoError(t, err)
	require.Equal(t, 3, n)
	require.Equal(t, "abc", string(buf[:n]))
	require.False(t, vr.Verified())

	n, err = vr.Read(buf)
	require.Equal(t, 0, n)
	require.ErrorIs(t, err, io.EOF)
	require.True(t, vr.Verified())
}

func TestStateRoundTripResetAndOffset(t *testing.T) {
	w, err := NewWriter(nil, SHA256)
	require.NoError(t, err)
	n, err := w.Write([]byte("hello "))
	require.NoError(t, err)
	require.Equal(t, len("hello "), n)

	st, err := w.State()
	require.NoError(t, err)
	require.Len(t, st.States, 1)
	require.Equal(t, "sha256", st.States[0].Algorithm)
	require.Equal(t, int64(len("hello ")), st.States[0].Offset)
	require.Equal(t, w.Size(), st.States[0].Offset)
	require.NotEmpty(t, st.States[0].Payload)

	resumed, err := NewWriterFromState(nil, st)
	require.NoError(t, err)
	require.Equal(t, st.States[0].Offset, resumed.Size())
	_, err = resumed.Write([]byte("world"))
	require.NoError(t, err)

	got, err := resumed.Digest()
	require.NoError(t, err)
	require.Equal(t, FromBytes([]byte("hello world")), got)
}

func TestDigesterReset(t *testing.T) {
	resumed, err := SHA256.New()
	require.NoError(t, err)
	_, err = resumed.Write([]byte("hello "))
	require.NoError(t, err)
	resumed.Reset()
	require.Equal(t, int64(0), resumed.Size())
	_, err = resumed.Write([]byte("hello world"))
	require.NoError(t, err)
	got, err := resumed.Digest()
	require.NoError(t, err)
	require.Equal(t, FromBytes([]byte("hello world")), got)
}

func TestWriterMultiDigest(t *testing.T) {
	w, err := NewWriter(nil, SHA256, SHA512)
	require.NoError(t, err)

	_, err = w.Write([]byte("hello world"))
	require.NoError(t, err)
	require.Equal(t, int64(len("hello world")), w.Size())

	digests, err := w.Digests()
	require.NoError(t, err)
	require.Equal(t, []Digest{
		SHA256.FromBytes([]byte("hello world")),
		SHA512.FromBytes([]byte("hello world")),
	}, digests)

	sha512Digest, err := w.DigestFor(SHA512)
	require.NoError(t, err)
	require.Equal(t, helloWorldSHA512, sha512Digest.String())

	_, err = NewWriter(nil, SHA256, SHA256)
	require.ErrorIs(t, err, ErrAlgorithmDuplicate)
}

func TestMultiStateRoundTrip(t *testing.T) {
	w, err := NewWriter(nil, SHA256, SHA512)
	require.NoError(t, err)
	_, err = w.Write([]byte("hello "))
	require.NoError(t, err)

	st, err := w.State()
	require.NoError(t, err)
	require.Len(t, st.States, 2)
	require.Equal(t, int64(len("hello ")), st.States[0].Offset)
	require.Equal(t, st.States[0].Offset, st.States[1].Offset)

	resumed, err := NewWriterFromState(nil, st)
	require.NoError(t, err)
	require.Equal(t, int64(len("hello ")), resumed.Size())
	_, err = resumed.Write([]byte("world"))
	require.NoError(t, err)

	digests, err := resumed.Digests()
	require.NoError(t, err)
	require.Equal(t, []Digest{
		SHA256.FromBytes([]byte("hello world")),
		SHA512.FromBytes([]byte("hello world")),
	}, digests)
}

func TestReaderFromState(t *testing.T) {
	w, err := NewWriter(nil, SHA256, SHA512)
	require.NoError(t, err)
	_, err = w.Write([]byte("hello "))
	require.NoError(t, err)
	st, err := w.State()
	require.NoError(t, err)

	r, err := NewReaderFromState(strings.NewReader("world"), st)
	require.NoError(t, err)
	_, err = io.Copy(io.Discard, r)
	require.NoError(t, err)

	digests, err := r.Digests()
	require.NoError(t, err)
	require.Equal(t, []Digest{
		SHA256.FromBytes([]byte("hello world")),
		SHA512.FromBytes([]byte("hello world")),
	}, digests)
}

func TestReaderDefaultAndMultiDigest(t *testing.T) {
	r, err := NewReader(strings.NewReader("hello world"))
	require.NoError(t, err)
	_, err = io.Copy(io.Discard, r)
	require.NoError(t, err)

	digest, err := r.Digest()
	require.NoError(t, err)
	require.Equal(t, helloWorldSHA256, digest.String())
	require.Equal(t, []Algorithm{Canonical}, r.Algorithms())

	r, err = NewReader(strings.NewReader("hello world"), SHA256, SHA512)
	require.NoError(t, err)
	_, err = io.Copy(io.Discard, r)
	require.NoError(t, err)

	digests, err := r.Digests()
	require.NoError(t, err)
	require.Equal(t, helloWorldSHA256, digests[0].String())
	require.Equal(t, helloWorldSHA512, digests[1].String())

	digest, err = r.Digest()
	require.NoError(t, err)
	require.Equal(t, helloWorldSHA256, digest.String())

	sha512Digest, err := r.DigestFor(SHA512)
	require.NoError(t, err)
	require.Equal(t, helloWorldSHA512, sha512Digest.String())
}

func TestWriterDefaultAndMultiDigest(t *testing.T) {
	var buf bytes.Buffer
	w, err := NewWriter(&buf)
	require.NoError(t, err)
	_, err = w.Write([]byte("hello world"))
	require.NoError(t, err)
	require.Equal(t, "hello world", buf.String())

	digest, err := w.Digest()
	require.NoError(t, err)
	require.Equal(t, helloWorldSHA256, digest.String())

	w, err = NewWriter(nil, SHA512, SHA256)
	require.NoError(t, err)
	_, err = w.Write([]byte("hello world"))
	require.NoError(t, err)

	digest, err = w.Digest()
	require.NoError(t, err)
	require.Equal(t, helloWorldSHA512, digest.String())

	sha256Digest, err := w.DigestFor(SHA256)
	require.NoError(t, err)
	require.Equal(t, helloWorldSHA256, sha256Digest.String())
}

func TestAlgorithmRegistrationErrors(t *testing.T) {
	_, err := RegisterAlgorithm(RegisterOptions{
		Name:    "sha256",
		Size:    32,
		NewHash: nil,
	})
	require.ErrorIs(t, err, ErrAlgorithmExists)

	_, err = RegisterAlgorithm(RegisterOptions{
		Name:    "testnilhash",
		Size:    32,
		NewHash: nil,
	})
	require.ErrorIs(t, err, ErrHashInvalid)

	_, err = RegisterAlgorithm(RegisterOptions{
		Name:    "sha256",
		Size:    32,
		NewHash: sha256.New,
	})
	require.ErrorIs(t, err, ErrAlgorithmExists)

	_, err = RegisterAlgorithm(RegisterOptions{
		Name:    "bad/alg",
		Size:    32,
		NewHash: sha256.New,
	})
	require.ErrorIs(t, err, ErrAlgorithmInvalidName)

	_, err = LookupAlgorithm("missing")
	require.ErrorIs(t, err, ErrAlgorithmUnknown)
}

func TestRegisterAlgorithmExtensibility(t *testing.T) {
	alg, err := RegisterAlgorithm(RegisterOptions{
		Name:    "testsha256",
		Size:    sha256.Size,
		NewHash: sha256.New,
	})
	require.NoError(t, err)

	d := alg.FromBytes([]byte("hello world"))
	require.Equal(t, "testsha256:"+strings.TrimPrefix(helloWorldSHA256, "sha256:"), d.String())

	parsed, err := Parse(d.String())
	require.NoError(t, err)
	require.Equal(t, d, parsed)
}

func TestRegisterAlgorithmCustomState(t *testing.T) {
	alg, err := RegisterAlgorithm(RegisterOptions{
		Name: "teststatehash",
		Size: sha256.Size,
		NewHash: func() hash.Hash {
			return &bufferHash{}
		},
		MarshalState: func(h hash.Hash) (string, []byte, error) {
			bh, ok := h.(*bufferHash)
			if !ok {
				return "", nil, ErrHashInvalid
			}
			return "buffer-v1", append([]byte(nil), bh.buf...), nil
		},
		NewState: func(st DigestState) (hash.Hash, error) {
			if st.Encoding != "buffer-v1" {
				return nil, ErrStateInvalid
			}
			return &bufferHash{buf: append([]byte(nil), st.Payload...)}, nil
		},
	})
	require.NoError(t, err)

	w, err := NewWriter(nil, alg)
	require.NoError(t, err)
	_, err = w.Write([]byte("hello "))
	require.NoError(t, err)
	st, err := w.State()
	require.NoError(t, err)
	require.Equal(t, "buffer-v1", st.States[0].Encoding)
	require.Equal(t, int64(len("hello ")), st.States[0].Offset)

	resumed, err := NewWriterFromState(nil, st)
	require.NoError(t, err)
	_, err = resumed.Write([]byte("world"))
	require.NoError(t, err)
	got, err := resumed.Digest()
	require.NoError(t, err)
	require.Equal(t, alg.FromBytes([]byte("hello world")), got)
}

func TestInvalidState(t *testing.T) {
	_, err := NewWriterFromState(nil, State{
		States: []DigestState{{
			Algorithm: "sha256",
			Encoding:  "unknown",
		}},
	})
	require.ErrorIs(t, err, ErrStateUnsupported)

	_, err = NewWriterFromState(nil, State{
		States: []DigestState{{
			Algorithm: "sha256",
			Encoding:  "binary-marshaler-v1",
			Offset:    -1,
		}},
	})
	require.ErrorIs(t, err, ErrStateInvalid)

	_, err = NewWriterFromState(nil, State{
		States: []DigestState{{
			Algorithm: "sha256",
			Encoding:  "binary-marshaler-v1",
			Payload:   []byte("not a valid sha256 state"),
		}},
	})
	require.ErrorIs(t, err, ErrStateInvalid)
}

type eofWithBytesReader struct {
	data []byte
	done bool
}

type bufferHash struct {
	buf []byte
}

func (h *bufferHash) Write(p []byte) (int, error) {
	h.buf = append(h.buf, p...)
	return len(p), nil
}

func (h *bufferHash) Sum(b []byte) []byte {
	sum := sha256.Sum256(h.buf)
	return append(b, sum[:]...)
}

func (h *bufferHash) Reset() {
	h.buf = nil
}

func (h *bufferHash) Size() int {
	return sha256.Size
}

func (h *bufferHash) BlockSize() int {
	return sha256.BlockSize
}

func (r *eofWithBytesReader) Read(p []byte) (int, error) {
	if r.done {
		return 0, io.EOF
	}
	copy(p, r.data)
	r.done = true
	return len(r.data), io.EOF
}
