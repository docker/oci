# ocidigest

`ocidigest` calculates and verifies OCI-compatible content digests:

```text
<algorithm>:<encoded-hash>
```

The built-in algorithms are `sha256` and `sha512`. `sha256` is the canonical
default.

## Parse a Digest

```go
dgst, err := ocidigest.Parse("sha256:b94d27b9934d3e08a52e52d7da7dabfac484efe37a5380ee9088f7ace2efcde9")
if err != nil {
    return err
}

fmt.Println(dgst.Algorithm()) // sha256
fmt.Println(dgst.Encoded())   // b94d27...
fmt.Println(dgst.String())    // sha256:b94d27...
```

`Digest` values are comparable and can be used as map keys:

```go
seen := map[ocidigest.Digest]bool{
    dgst: true,
}
```

`Digest` is a string-backed type. Use `Parse` when accepting a digest string
that should be valid immediately:

```go
dgst, err := ocidigest.Parse(input)
if err != nil {
    return err
}
```

Use `Validate` when a digest may have arrived through a plain string path, such
as JSON decoding into a struct:

```go
if err := desc.Digest.Validate(); err != nil {
    return fmt.Errorf("invalid descriptor digest: %w", err)
}
```

Empty digests represent absent optional values. `Parse("")` and `Validate` both
reject an empty digest, so callers should check optional strings before parsing.

## Digest Bytes

Use `FromBytes` when content is already buffered, such as manifest JSON or test data:

```go
payload := []byte(`{"schemaVersion":2}`)
dgst := ocidigest.FromBytes(payload)
```

To choose a non-canonical algorithm:

```go
dgst := ocidigest.SHA512.FromBytes(payload)
```

## Digest a Reader

Use `FromReader` when content should be consumed and discarded after hashing:

```go
f, err := os.Open("layer.tar")
if err != nil {
    return err
}
defer f.Close()

dgst, err := ocidigest.FromReader(f)
if err != nil {
    return err
}
```

For a specific algorithm:

```go
dgst, err := ocidigest.SHA512.FromReader(f)
```

## Digest While Copying

Create a `Digester` and write bytes through it directly:

```go
d, err := ocidigest.SHA256.New()
if err != nil {
    return err
}

if _, err := io.Copy(d, src); err != nil {
    return err
}

dgst, err := d.Digest()
if err != nil {
    return err
}
```

Use `io.TeeReader` when bytes should be copied somewhere else while hashing:

```go
d, err := ocidigest.SHA256.New()
if err != nil {
    return err
}

tee := io.TeeReader(src, d)
if _, err := io.Copy(dst, tee); err != nil {
    return err
}

dgst, err := d.Digest()
```

## Digesting Reader Wrapper

`NewReader` passes reads through to an underlying reader and records the digest.
When no algorithm is provided, it uses `Canonical`:

```go
r, err := ocidigest.NewReader(src)
if err != nil {
    return err
}

if _, err := io.Copy(dst, r); err != nil {
    return err
}

dgst, err := r.Digest()
```

Pass one or more algorithms to choose what is calculated:

```go
r, err := ocidigest.NewReader(src, ocidigest.SHA256, ocidigest.SHA512)
if err != nil {
    return err
}

if _, err := io.Copy(dst, r); err != nil {
    return err
}

sha256Digest, err := r.DigestFor(ocidigest.SHA256)
digests, err := r.Digests()
```

The wrapper also exposes how many bytes were digested:

```go
fmt.Println(r.Size())
```

## Digesting Writer Wrapper

`NewWriter` passes writes through to an underlying writer and records the digest.
When no algorithm is provided, it uses `Canonical`:

```go
w, err := ocidigest.NewWriter(dst)
if err != nil {
    return err
}

if _, err := io.Copy(w, src); err != nil {
    return err
}

dgst, err := w.Digest()
```

Pass `nil` as the underlying writer to only calculate the digest:

```go
w, err := ocidigest.NewWriter(nil, ocidigest.SHA256, ocidigest.SHA512)
if err != nil {
    return err
}
_, err = w.Write(payload)
```

## Verify While Reading

`VerifierReader` reports a digest mismatch as a read error at EOF:

```go
vr, err := ocidigest.NewVerifierReader(src, expected)
if err != nil {
    return err
}

if _, err := io.Copy(dst, vr); err != nil {
    if errors.Is(err, ocidigest.ErrDigestMismatch) {
        return fmt.Errorf("content digest did not match: %w", err)
    }
    return err
}

if !vr.Verified() {
    return ocidigest.ErrDigestMismatch
}
```

If the final read returns bytes and EOF together, `VerifierReader` returns the final bytes
first. The EOF or mismatch is returned by the next read. This preserves normal `io.Reader`
byte-delivery behavior.

## Resume Digest State

Digest state can be exported and restored for short-lived resumable workflows, such as
chunked uploads. State is only intended for the same library version and same algorithm
implementation.

```go
w, err := ocidigest.NewWriter(uploadWriter, ocidigest.SHA256, ocidigest.SHA512)
if err != nil {
    return err
}

if _, err := w.Write(firstChunk); err != nil {
    return err
}

state, err := w.State()
if err != nil {
    return err
}

// Persist state alongside the upload ID and offset.
save(state)
```

Later:

```go
state := load()

w, err := ocidigest.NewWriterFromState(uploadWriter, state)
if err != nil {
    return err
}

if w.Size() != expectedUploadOffset {
    return fmt.Errorf("digest state offset mismatch")
}

if _, err := w.Write(nextChunk); err != nil {
    return err
}

digests, err := w.Digests()
```

## Multiple Digests from One Stream

Pass multiple algorithms when one pass should produce several digests:

```go
r, err := ocidigest.NewReader(src, ocidigest.SHA256, ocidigest.SHA512)
if err != nil {
    return err
}

if _, err := io.Copy(dst, r); err != nil {
    return err
}

digests, err := r.Digests()
if err != nil {
    return err
}

sha256Digest := digests[0]
sha512Digest := digests[1]
```

Use `NewWriter(nil, ...)` to calculate several digests without forwarding bytes:

```go
w, err := ocidigest.NewWriter(nil, ocidigest.SHA256, ocidigest.SHA512)
if err != nil {
    return err
}

if _, err := w.Write(payload); err != nil {
    return err
}

digests, err := w.Digests()
```

## Register a Custom Algorithm

Algorithms are registered globally by name:

```go
alg, err := ocidigest.RegisterAlgorithm(ocidigest.RegisterOptions{
    Name:    "mysha256",
    Size:    sha256.Size,
    NewHash: sha256.New,
})
if err != nil {
    return err
}

dgst := alg.FromBytes(payload)
fmt.Println(dgst.String()) // mysha256:<hex>
```

Custom algorithms that need resumable state can provide `MarshalState` and `NewState`.
Most callers should not need this unless the hash implementation does not support Go's
standard binary marshaling interfaces.

## JSON Encoding

`Digest` is encoded as a JSON string because it is a string-backed type:

```go
data, err := json.Marshal(dgst)
// "sha256:b94d27..."
```

JSON decoding stores the string value as-is. It does not validate the digest:

```go
var dgst ocidigest.Digest
if err := json.Unmarshal(data, &dgst); err != nil {
    return err
}

if err := dgst.Validate(); err != nil {
    return err
}
```

This keeps unmarshaling cheap and lets higher-level validation decide whether a
digest is required in that context.
