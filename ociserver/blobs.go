package ociserver

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/docker/oci"
	"github.com/docker/oci/ocidigest"
	"github.com/docker/oci/ociserver/mux"
)

func (s *Server) blobHeadGet() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := mux.URLParam(r, "name")
		dgstStr := mux.URLParam(r, "digest")

		dgst, err := ocidigest.Parse(dgstStr)
		if err != nil {
			returnError(w, ErrBlobUnknown("invalid digest"))
			return
		}
		blob, err := s.db.ResolveBlob(r.Context(), name, dgst)
		if err != nil {
			if errors.Is(err, oci.ErrBlobUnknown) || errors.Is(err, oci.ErrNameUnknown) {
				returnError(w, ErrBlobUnknown(dgst.String()))
				return
			}
			s.logError(r.Context(), "resolving blob", err, "repository", name, "digest", dgstStr)
			returnError(w, ErrServerError())
			return
		}

		switch r.Method {
		case http.MethodHead:
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Header().Set("Content-Length", fmt.Sprintf("%d", blob.Size))
			w.Header().Set("Docker-Content-Digest", blob.Digest.String())
			// TODO: solve eventing.
		case http.MethodGet:
			if s.redirect != nil {
				redirect, redirectURL, err := s.redirect.Redirect(r, blob, name)
				if err != nil {
					s.logError(r.Context(), "getting blob redirect", err, "repository", name, "digest", dgstStr)
					returnError(w, ErrServerError())
					return
				}
				if redirect {
					// TODO: solve eventing.
					http.Redirect(w, r, redirectURL, http.StatusTemporaryRedirect)
					return
				}
			}
			rh := r.Header.Get("Range")

			var br io.ReadCloser
			if rh != "" {
				bi, ei, parseErr := parseBlobRange(rh, blob.Size)
				if parseErr != nil {
					returnError(w, ErrRangeNotSatisfiable(parseErr.Error()))
					return
				}
				br, err = s.db.GetBlobRange(r.Context(), name, dgst, bi, ei)
				if err != nil {
					s.logError(r.Context(), "getting blob range", err, "repository", name, "digest", dgstStr)
					returnError(w, ErrServerError())
					return
				}
				length := ei - bi
				w.Header().Set("Content-Length", fmt.Sprintf("%d", length))
				w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", bi, ei-1, blob.Size))
				w.Header().Set("Content-Type", "application/octet-stream")
				w.Header().Set("Docker-Content-Digest", blob.Digest.String())
				w.WriteHeader(http.StatusPartialContent)
			} else {
				br, err = s.db.GetBlob(r.Context(), name, dgst)
				if err != nil {
					s.logError(r.Context(), "getting blob", err, "repository", name, "digest", dgstStr)
					returnError(w, ErrServerError())
					return
				}
				w.Header().Set("Content-Length", fmt.Sprintf("%d", blob.Size))
				w.Header().Set("Content-Type", "application/octet-stream")
				w.Header().Set("Docker-Content-Digest", blob.Digest.String())
			}

			defer func() {
				err = br.Close()
				if err != nil {
					s.logError(r.Context(), "closing blob reader", err, "repository", name, "digest", dgstStr)
				}
			}()
			_, err = io.Copy(w, br)
			if err != nil {
				s.logError(r.Context(), "writing blob response", err, "repository", name, "digest", dgstStr)
				return
			}
			// TODO: solve eventing.
		}
	}
}

// parseBlobRange parses a single HTTP byte range and returns its half-open
// bounds within a blob of the given size.
func parseBlobRange(rangeHeader string, blobSize int64) (int64, int64, error) {
	const bytesPrefix = "bytes="
	if !strings.HasPrefix(rangeHeader, bytesPrefix) {
		return 0, 0, errors.New("range header must use bytes unit")
	}

	rangeStart, rangeEnd, ok := strings.Cut(strings.TrimPrefix(rangeHeader, bytesPrefix), "-")
	if !ok {
		return 0, 0, errors.New("invalid range")
	}

	var start, end int64
	if rangeStart == "" {
		// Suffix range (e.g. bytes=-500): return the last N bytes.
		suffixLen, err := strconv.ParseInt(rangeEnd, 10, 64)
		if err != nil || suffixLen < 0 {
			return 0, 0, errors.New("invalid range")
		}
		start = max(blobSize-suffixLen, 0)
		end = blobSize - 1
	} else {
		var err error
		start, err = strconv.ParseInt(rangeStart, 10, 64)
		if err != nil || start < 0 {
			return 0, 0, errors.New("invalid range start")
		}
		if rangeEnd == "" {
			end = blobSize - 1
		} else {
			end, err = strconv.ParseInt(rangeEnd, 10, 64)
			if err != nil || end < 0 {
				return 0, 0, errors.New("invalid range end")
			}
		}
	}

	if end >= blobSize {
		end = blobSize - 1
	}
	if start >= blobSize {
		return 0, 0, errors.New("range start is beyond end of blob")
	}
	if end < start {
		return 0, 0, errors.New("range end is before range start")
	}
	return start, end + 1, nil
}

func (s *Server) blobDelete() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := mux.URLParam(r, "name")
		dgstStr := mux.URLParam(r, "digest")

		dgst, err := ocidigest.Parse(dgstStr)
		if err != nil {
			returnError(w, ErrDigestInvalid("invalid digest"))
			return
		}

		err = s.db.DeleteBlob(r.Context(), name, dgst)
		if err != nil {
			if errors.Is(err, oci.ErrBlobUnknown) || errors.Is(err, oci.ErrNameUnknown) {
				returnError(w, ErrBlobUnknown(dgst.String()))
				return
			}
			if errors.Is(err, oci.ErrReferenced) || errors.Is(err, oci.ErrDenied) {
				returnError(w, ErrMethodNotAllowed("blob is referenced by a manifest"))
				return
			}
			s.logError(r.Context(), "deleting blob", err, "repository", name, "digest", dgstStr)
			returnError(w, ErrServerError())
			return
		}
		w.WriteHeader(http.StatusAccepted)
	}
}
