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
				if !strings.HasPrefix(rh, "bytes=") {
					returnError(w, ErrRangeNotSatisfiable("Range header must use bytes unit"))
					return
				}
				rangeStart, rangeEnd, _ := strings.Cut(strings.TrimPrefix(rh, "bytes="), "-")
				var bi, ei int64
				if rangeStart == "" {
					// Suffix range (e.g. bytes=-500): return the last N bytes.
					suffixLen, parseErr := strconv.ParseInt(rangeEnd, 10, 64)
					if parseErr != nil || suffixLen < 0 {
						returnError(w, ErrRangeNotSatisfiable("invalid range"))
						return
					}
					bi = blob.Size - suffixLen
					if bi < 0 {
						bi = 0
					}
					ei = blob.Size - 1
				} else {
					var parseErr error
					bi, parseErr = strconv.ParseInt(rangeStart, 10, 64)
					if parseErr != nil || bi < 0 {
						returnError(w, ErrRangeNotSatisfiable("invalid range start"))
						return
					}
					if rangeEnd == "" {
						ei = blob.Size - 1
					} else {
						ei, parseErr = strconv.ParseInt(rangeEnd, 10, 64)
						if parseErr != nil || ei < 0 {
							returnError(w, ErrRangeNotSatisfiable("invalid range end"))
							return
						}
					}
				}
				if ei >= blob.Size {
					ei = blob.Size - 1
				}
				if bi >= blob.Size {
					returnError(w, ErrRangeNotSatisfiable("range start is beyond end of blob"))
					return
				}
				if ei < bi {
					returnError(w, ErrRangeNotSatisfiable("range end is before range start"))
					return
				}
				br, err = s.db.GetBlobRange(r.Context(), name, dgst, bi, ei+1)
				if err != nil {
					s.logError(r.Context(), "getting blob range", err, "repository", name, "digest", dgstStr)
					returnError(w, ErrServerError())
					return
				}
				length := ei - bi + 1
				w.Header().Set("Content-Length", fmt.Sprintf("%d", length))
				w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", bi, ei, blob.Size))
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
