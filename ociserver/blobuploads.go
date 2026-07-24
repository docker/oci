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
	"github.com/docker/oci/ociref"
	"github.com/docker/oci/ociserver/mux"
)

func (s *Server) blobUploadGet() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := mux.URLParam(r, "name")
		session := mux.URLParam(r, "session")
		if session == "" {
			returnError(w, ErrBlobUploadInvalid("missing session"))
			return
		}
		bw, err := s.db.PushBlobChunkedResume(r.Context(), name, session, -1, 0)
		if err != nil {
			if errors.Is(err, oci.ErrBlobUploadUnknown) {
				returnError(w, ErrBlobUploadInvalid("session not found"))
				return
			}
			s.logError(r.Context(), "getting upload session", err, "repository", name, "session", session)
			returnError(w, ErrServerError())
			return
		}
		u := fmt.Sprintf("/v2/%s/blobs/uploads/%s", name, session)
		w.Header().Set("Location", u)
		w.Header().Set("Range", fmt.Sprintf("%d-%d", 0, bw.Size()-1)) // adjust size to 0-based
		w.WriteHeader(http.StatusNoContent)
	}
}

func (s *Server) blobUploadDelete() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := mux.URLParam(r, "name")
		session := mux.URLParam(r, "session")
		if session == "" {
			returnError(w, ErrBlobUploadInvalid("missing session"))
			return
		}
		bw, err := s.db.PushBlobChunkedResume(r.Context(), name, session, -1, 0)
		if err != nil {
			if errors.Is(err, oci.ErrBlobUploadUnknown) {
				returnError(w, ErrBlobUploadInvalid("session not found"))
				return
			}
			s.logError(r.Context(), "getting upload session", err, "repository", name, "session", session)
			returnError(w, ErrServerError())
			return
		}
		err = bw.Cancel()
		if err != nil {
			s.logError(r.Context(), "canceling upload session", err, "repository", name, "session", session)
			returnError(w, ErrServerError())
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func (s *Server) blobUploadPost() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := mux.URLParam(r, "name")
		dgstString := r.URL.Query().Get("digest")
		mount := r.URL.Query().Get("mount")
		from := r.URL.Query().Get("from")

		if mount != "" && from != "" {
			if !ociref.IsValidRepository(from) {
				returnError(w, ErrBlobUploadInvalid("invalid from parameter"))
				return
			}
			dgst, err := ocidigest.Parse(mount)
			if err != nil {
				returnError(w, ErrBlobUploadInvalid("invalid digest"))
				return
			}
			blob, err := s.db.MountBlob(r.Context(), from, name, dgst)
			if err != nil {
				goto FALLBACK
			}
			// TODO: solve eventing.
			u := fmt.Sprintf("/v2/%s/blobs/%s", name, blob.Digest.String())
			w.Header().Set("Location", u)
			w.Header().Set("Docker-Content-Digest", blob.Digest.String())
			w.WriteHeader(http.StatusCreated)
			return
		} else if dgstString != "" {
			dgst, err := ocidigest.Parse(dgstString)
			if err != nil {
				returnError(w, ErrBlobUploadInvalid("invalid digest"))
				return
			}
			contentLength := r.Header.Get("Content-Length")
			if contentLength == "" {
				contentLength = "0"
			}
			length, err := strconv.Atoi(contentLength)
			if err != nil || length < 0 {
				returnError(w, ErrBlobUploadInvalid("unable to parse Content-Length"))
				return
			}
			bw, err := s.db.PushBlobChunked(r.Context(), name, length)
			if err != nil {
				s.logError(r.Context(), "starting blob upload", err, "repository", name)
				returnError(w, ErrServerError())
				return
			}
			_, err = io.Copy(bw, r.Body)
			if err != nil {
				s.logError(r.Context(), "writing blob upload", err, "repository", name)
				returnError(w, ErrServerError())
				return
			}
			err = r.Body.Close()
			if err != nil {
				s.logError(r.Context(), "closing blob upload request body", err, "repository", name)
			}
			if err = bw.Close(); err != nil {
				s.logError(r.Context(), "closing blob writer", err, "repository", name)
				returnError(w, ErrServerError())
				return
			}
			desc, err := bw.Commit(dgst)
			if err != nil {
				if errors.Is(err, oci.ErrDigestInvalid) {
					returnError(w, ErrDigestInvalid("digest does not match contents"))
					return
				}
				s.logError(r.Context(), "committing blob upload", err, "repository", name, "digest", dgst)
				returnError(w, ErrServerError())
				return
			}
			// TODO: solve eventing.
			u := fmt.Sprintf("/v2/%s/blobs/%s", name, desc.Digest.String())
			w.Header().Set("Location", u)
			w.Header().Set("Docker-Content-Digest", desc.Digest.String())
			w.WriteHeader(http.StatusCreated)
			return
		}
	FALLBACK:
		bw, err := s.db.PushBlobChunked(r.Context(), name, 0)
		if err != nil {
			s.logError(r.Context(), "creating blob upload session", err, "repository", name)
			returnError(w, ErrServerError())
			return
		}
		u := fmt.Sprintf("/v2/%s/blobs/uploads/%s", name, bw.ID())
		w.Header().Set("Location", u)
		w.Header().Set("Range", "0-0")
		w.WriteHeader(http.StatusAccepted)
	}
}

func (s *Server) blobUploadPatch() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		session := mux.URLParam(r, "session")
		if session == "" {
			returnError(w, ErrBlobUploadInvalid("missing session"))
			return
		}
		contentLength := r.Header.Get("Content-Length")
		streaming := contentLength == ""
		if streaming {
			contentLength = "0"
		}
		length, err := strconv.Atoi(contentLength)
		if err != nil || length < 0 {
			returnError(w, ErrBlobUploadInvalid("unable to parse Content-Length"))
			return
		}
		contentRange := r.Header.Get("Content-Range")
		var rangeStart, rangeEnd, size int
		if contentRange == "" {
			rangeStart = 0
			rangeEnd = length
			size = length
		} else {
			rangeStart, rangeEnd, size, err = parseRange(contentRange)
			if err != nil {
				returnError(w, ErrBlobUploadInvalid("unable to parse range"))
				return
			}
		}

		if length > 0 && length != size {
			returnError(w, ErrBlobUploadInvalid("Content-Length does not match Content-Range"))
			return
		}
		name := mux.URLParam(r, "name")

		bw, err := s.db.PushBlobChunkedResume(r.Context(), name, session, int64(rangeStart), size)
		if err != nil {
			if errors.Is(err, oci.ErrRangeInvalid) {
				returnError(w, ErrBlobUploadOutOfOrder())
				return
			}
			s.logError(r.Context(), "resuming blob upload", err, "repository", name, "session", session)
			returnError(w, ErrServerError())
			return
		}
		if size > 0 || streaming {
			n, err := io.Copy(bw, r.Body)
			if err != nil {
				if errors.Is(err, oci.ErrRangeInvalid) {
					returnError(w, ErrBlobUploadOutOfOrder())
					return
				}
				s.logError(r.Context(), "writing blob upload chunk", err, "repository", name, "session", session)
				if cancelErr := bw.Cancel(); cancelErr != nil {
					s.logError(r.Context(), "canceling blob upload after write failure", cancelErr, "repository", name, "session", session)
				}
				returnError(w, ErrServerError())
				return
			}
			if streaming {
				rangeEnd = rangeStart + int(n)
			}
		}
		err = r.Body.Close()
		if err != nil {
			s.logError(r.Context(), "closing blob upload request body", err, "repository", name, "session", session)
		}
		if err = bw.Close(); err != nil {
			s.logError(r.Context(), "closing blob writer", err, "repository", name, "session", session)
			returnError(w, ErrServerError())
			return
		}
		u := fmt.Sprintf("/v2/%s/blobs/uploads/%s", name, session)
		w.Header().Set("Location", u)
		w.Header().Set("Range", fmt.Sprintf("0-%d", rangeEnd-1))
		w.WriteHeader(http.StatusAccepted)
	}
}

func (s *Server) blobUploadPut() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		session := mux.URLParam(r, "session")
		if session == "" {
			returnError(w, ErrBlobUploadInvalid("missing session"))
			return
		}
		dgstString := r.URL.Query().Get("digest")
		if dgstString == "" {
			returnError(w, ErrDigestInvalid("missing digest"))
			return
		}
		dgst, err := ocidigest.Parse(dgstString)
		if err != nil {
			returnError(w, ErrBlobUploadInvalid("invalid digest"))
			return
		}
		name := mux.URLParam(r, "name")
		contentLength := r.Header.Get("Content-Length")

		streaming := contentLength == ""
		if streaming {
			contentLength = "0"
		}
		if streaming || contentLength != "0" {
			length, err := strconv.Atoi(contentLength)
			if err != nil || length < 0 {
				returnError(w, ErrBlobUploadInvalid("unable to parse Content-Length"))
				return
			}
			contentRange := r.Header.Get("Content-Range")
			var rangeStart, size int
			if contentRange == "" {
				rangeStart = 0
				size = length
			} else {
				rangeStart, _, size, err = parseRange(contentRange)
				if err != nil {
					returnError(w, ErrBlobUploadInvalid("unable to parse range"))
					return
				}
			}
			if length > 0 && length != size {
				returnError(w, ErrBlobUploadInvalid("Content-Length does not match Content-Range"))
				return
			}

			bw, err := s.db.PushBlobChunkedResume(r.Context(), name, session, int64(rangeStart), size)
			if err != nil {
				if errors.Is(err, oci.ErrRangeInvalid) {
					returnError(w, ErrBlobUploadOutOfOrder())
					return
				}
				s.logError(r.Context(), "resuming blob upload", err, "repository", name, "session", session)
				returnError(w, ErrServerError())
				return
			}
			_, err = io.Copy(bw, r.Body)
			if err != nil {
				if errors.Is(err, oci.ErrRangeInvalid) {
					returnError(w, ErrBlobUploadOutOfOrder())
					return
				}
				s.logError(r.Context(), "writing blob upload chunk", err, "repository", name, "session", session)
				returnError(w, ErrServerError())
				return
			}
			err = r.Body.Close()
			if err != nil {
				s.logError(r.Context(), "closing blob upload request body", err, "repository", name, "session", session)
			}
			if err = bw.Close(); err != nil {
				s.logError(r.Context(), "closing blob writer", err, "repository", name, "session", session)
				returnError(w, ErrServerError())
				return
			}
		}
		bw, err := s.db.PushBlobChunkedResume(r.Context(), name, session, -1, 0)
		if err != nil {
			if !errors.Is(err, oci.ErrBlobUploadUnknown) {
				s.logError(r.Context(), "getting blob upload session for commit", err, "repository", name, "session", session)
			}
			returnError(w, ErrBlobUploadUnknown(session))
			return
		}
		_, err = bw.Commit(dgst)
		if err != nil {
			if errors.Is(err, oci.ErrDigestInvalid) {
				returnError(w, ErrDigestInvalid("digest does not match contents"))
				return
			}
			s.logError(r.Context(), "committing blob upload", err, "repository", name, "session", session, "digest", dgst)
			returnError(w, ErrServerError())
			return
		}
		// TODO: solve eventing.
		u := fmt.Sprintf("/v2/%s/blobs/%s", name, dgst.String())
		w.Header().Set("Location", u)
		w.Header().Set("Docker-Content-Digest", dgst.String())
		w.WriteHeader(http.StatusCreated)
	}
}

// parseRange parses a Content-Range value of the form [bytes ]start-end[/total].
// It returns (start, end+1, size, nil) where end+1 is the exclusive upper bound,
// so callers can use rangeEnd-1 consistently for the Range response header.
func parseRange(contentRange string) (int, int, int, error) {
	contentRange = strings.TrimPrefix(contentRange, "bytes ")
	if i := strings.IndexByte(contentRange, '/'); i >= 0 {
		contentRange = contentRange[:i]
	}
	rangeStart, rangeEnd, ok := strings.Cut(contentRange, "-")
	if !ok {
		return 0, 0, 0, errors.New("unable to parse range header")
	}
	start, err := strconv.Atoi(rangeStart)
	if err != nil {
		return 0, 0, 0, errors.New("unable to parse range start")
	}
	end, err := strconv.Atoi(rangeEnd)
	if err != nil {
		return 0, 0, 0, errors.New("unable to parse range end")
	}
	if start < 0 || end < 0 {
		return 0, 0, 0, errors.New("range values must be non-negative")
	}
	if end < start {
		return 0, 0, 0, errors.New("range end must be greater than or equal to range start")
	}
	return start, end + 1, end - start + 1, nil
}
