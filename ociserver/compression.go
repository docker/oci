package ociserver

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"io"
	"net/http"
	"strings"
)

const acceptEncodingHeader = "Accept-Encoding"

// compressedFormatMagic holds the leading bytes ("magic numbers") of file
// formats that are already compressed. A response body starting with one of
// these prefixes gains nothing -- and can even grow slightly -- from an
// additional gzip/deflate pass, so compressHandler skips re-compressing it.
//
// Detection works on the blob's actual bytes, not any declared media type:
// a blob referenced only by digest has no media type of its own (that's a
// property of a manifest's reference to it, and this registry never even
// records one more specific than "application/octet-stream" -- see
// registry/storage/blobwriter.go), so media type isn't a usable signal here.
// Sniffing real bytes instead also means this keeps working for any artifact
// type: if the ecosystem ever stops shipping gzip-compressed tarballs by
// convention and leans on transport compression instead, an uncompressed tar
// simply won't match any of these prefixes and will still get gzip'd on the
// wire like any other compressible content.
var compressedFormatMagic = [][]byte{
	{0x1f, 0x8b},                         // gzip
	{0x28, 0xb5, 0x2f, 0xfd},             // zstd
	{0x42, 0x5a, 0x68},                   // bzip2
	{0xfd, 0x37, 0x7a, 0x58, 0x5a, 0x00}, // xz
	{0x50, 0x4b, 0x03, 0x04},             // zip
	{0x04, 0x22, 0x4d, 0x18},             // lz4
}

func looksAlreadyCompressed(head []byte) bool {
	for _, magic := range compressedFormatMagic {
		if bytes.HasPrefix(head, magic) {
			return true
		}
	}
	return false
}

// negotiateEncoding picks gzip or deflate from the client's Accept-Encoding
// header. It intentionally doesn't parse quality values, matching the
// gorilla/handlers.CompressHandler behavior this replaced.
func negotiateEncoding(r *http.Request) string {
	for enc := range strings.SplitSeq(r.Header.Get(acceptEncodingHeader), ",") {
		enc = strings.TrimSpace(enc)
		if enc == "gzip" || enc == "deflate" {
			return enc
		}
	}
	return ""
}

// compressHandler gzip/deflate-compresses eligible responses for clients
// that advertise support via Accept-Encoding.
//
// A HEAD request is excluded outright: it has no body, and some handlers in
// this codebase write their body payload unconditionally, relying on
// net/http to discard it for HEAD at the transport level. Compressing here
// would still spin up a compressor whose lone Close() trailer corrupts the
// Content-Length net/http would otherwise report accurately. The
// compressingResponseWriter below also defends against this more generally,
// for any response that ends up writing no body at all, but excluding HEAD
// up front avoids the sniffing overhead for a class of request that can
// never benefit from it anyway.
func compressHandler(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Always add Accept-Encoding to Vary, to prevent intermediate caches
		// from serving a compressed response to a client that can't decode
		// it (or vice versa), regardless of what this particular request negotiates.
		w.Header().Add("Vary", acceptEncodingHeader)

		encoding := negotiateEncoding(r)
		if encoding == "" || r.Method == http.MethodHead || r.Header.Get("Upgrade") != "" {
			next.ServeHTTP(w, r)
			return
		}

		cw := &compressingResponseWriter{ResponseWriter: w, encoding: encoding, statusCode: http.StatusOK}
		next.ServeHTTP(cw, r)
		cw.finish()
	}
}

// compressingResponseWriter defers the compress-or-not decision until the
// handler's first Write, so it can peek at the bytes actually being served:
//
//   - A Range/206 response is never compressed. http.ServeContent computes
//     Content-Range and (per its own documented "always set Content-Length
//     [for range requests]" rule) Content-Length from the uncompressed byte
//     offsets; silently gzip'ing the body out from under that would make
//     both headers describe bytes that were never sent.
//   - Content that's already in a recognized compressed format (typically
//     an OCI layer tarball) is passed through untouched.
//
// Because the decision -- and the resulting Content-Encoding/Content-Length
// header changes -- only happens once real body bytes exist to inspect, a
// response that never calls Write (including, redundantly with the HEAD
// check above, any handler that legitimately writes nothing) never spins up
// a compressor. That's what starves the bug described in compressHandler's
// doc comment: gzip/deflate's harmless empty end-of-stream trailer has
// nothing to masquerade as the whole response, because it's never written.
type compressingResponseWriter struct {
	http.ResponseWriter
	encoding   string
	statusCode int

	wroteHeader bool
	decided     bool
	enc         io.WriteCloser // non-nil once compressing
}

func (cw *compressingResponseWriter) WriteHeader(code int) {
	if cw.wroteHeader {
		return
	}
	cw.wroteHeader = true
	cw.statusCode = code
}

func (cw *compressingResponseWriter) Write(p []byte) (int, error) {
	if !cw.decided {
		cw.decided = true
		if cw.statusCode != http.StatusPartialContent && !looksAlreadyCompressed(p) {
			// Whatever Content-Length a handler (or http.ServeContent, for
			// the uncompressed byte count) already set no longer applies
			// once the body is compressed, and the final compressed size
			// isn't known up front.
			cw.Header().Del("Content-Length")
			cw.Header().Set("Content-Encoding", cw.encoding)
			if cw.encoding == "deflate" {
				cw.enc, _ = flate.NewWriter(cw.ResponseWriter, gzip.DefaultCompression)
			} else {
				cw.enc, _ = gzip.NewWriterLevel(cw.ResponseWriter, gzip.DefaultCompression)
			}
		}
		cw.ResponseWriter.WriteHeader(cw.statusCode)
	}

	if cw.enc != nil {
		return cw.enc.Write(p)
	}
	return cw.ResponseWriter.Write(p)
}

// finish must be called after the wrapped handler returns, to flush a
// still-pending WriteHeader call for a response that wrote no body (a
// decision was never made, so there's nothing to compress), or to close out
// the compressor -- emitting its end-of-stream trailer -- for one that did.
func (cw *compressingResponseWriter) finish() {
	if cw.enc != nil {
		cw.enc.Close()
		return
	}
	if cw.wroteHeader && !cw.decided {
		cw.ResponseWriter.WriteHeader(cw.statusCode)
	}
}
