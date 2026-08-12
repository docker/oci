package ociserver

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"

	"github.com/docker/oci"
	"github.com/docker/oci/ocidigest"
	"github.com/docker/oci/ociref"
	"github.com/docker/oci/ociserver/mux"
)

const manifestSizeLimit = 4 * 1024 * 1024 // 4 MB

func (s *Server) manifestHeadGet() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := mux.URLParam(r, "name")
		reference := mux.URLParam(r, "reference")
		var desc oci.Descriptor
		var err error
		if strings.Contains(reference, ":") { // is digest
			var dgst oci.Digest
			dgst, err = ocidigest.Parse(reference)
			if err != nil {
				returnError(w, ErrManifestInvalid("invalid digest"))
				return
			}
			desc, err = s.db.ResolveManifest(r.Context(), name, dgst)
		} else {
			if !ociref.IsValidTag(reference) {
				returnError(w, ErrManifestInvalid("invalid tag name"))
				return
			}
			desc, err = s.db.ResolveTag(r.Context(), name, reference)
		}
		if err != nil {
			if errors.Is(err, oci.ErrManifestUnknown) || errors.Is(err, oci.ErrNameUnknown) {
				returnError(w, ErrManifestUnknown(reference))
				return
			}
			s.logError(r.Context(), "resolving manifest", err, "repository", name, "reference", reference)
			returnError(w, ErrServerError())
			return
		}

		if !acceptsMediaType(r.Header.Values("Accept"), desc.MediaType) {
			returnError(w, ErrNotAcceptable(desc.MediaType))
			return
		}

		if r.Method == http.MethodHead {
			w.Header().Set("Content-Type", desc.MediaType)
			w.Header().Set("Content-Length", fmt.Sprintf("%d", desc.Size))
			w.Header().Set("Docker-Content-Digest", desc.Digest.String())
			// TODO: solve eventing.
			return
		}

		b, err := s.db.GetManifest(r.Context(), name, desc.Digest)
		if err != nil {
			s.logError(r.Context(), "getting manifest", err, "repository", name, "reference", reference)
			returnError(w, ErrServerError())
			return
		}
		defer func() {
			if err := b.Close(); err != nil {
				s.logError(r.Context(), "closing manifest reader", err, "repository", name, "reference", reference)
			}
		}()
		w.Header().Set("Content-Type", desc.MediaType)
		w.Header().Set("Content-Length", fmt.Sprintf("%d", desc.Size))
		w.Header().Set("Docker-Content-Digest", desc.Digest.String())
		if i, err := io.Copy(w, b); err != nil {
			s.logError(r.Context(), "writing manifest response", err, "repository", name, "reference", reference, "bytesWritten", i, "size", desc.Size)
		}
		// TODO: solve eventing.
	}
}

func acceptsMediaType(acceptHeaders []string, mediaType string) bool {
	if len(acceptHeaders) == 0 || mediaType == "" {
		return true
	}
	for _, acceptHeader := range acceptHeaders {
		if acceptHeader == "" {
			continue
		}
		for _, part := range strings.Split(acceptHeader, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			acceptedType, params, err := mime.ParseMediaType(part)
			if err != nil {
				acceptedType, _, _ = strings.Cut(part, ";")
				acceptedType = strings.TrimSpace(acceptedType)
				params = nil
			}
			if acceptedType == "" {
				continue
			}
			if q, ok := params["q"]; ok {
				qv, err := strconv.ParseFloat(q, 64)
				if err == nil && qv <= 0 {
					continue
				}
			}
			if mediaTypeMatches(acceptedType, mediaType) {
				return true
			}
		}
	}
	return false
}

func mediaTypeMatches(acceptedType, mediaType string) bool {
	if acceptedType == "*/*" || acceptedType == mediaType {
		return true
	}
	acceptedKind, acceptedSubType, ok := strings.Cut(acceptedType, "/")
	if !ok {
		return false
	}
	mediaKind, _, ok := strings.Cut(mediaType, "/")
	if !ok {
		return false
	}
	return acceptedSubType == "*" && acceptedKind == mediaKind
}

func (s *Server) manifestPut() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := mux.URLParam(r, "name")
		reference := mux.URLParam(r, "reference")
		tags := r.URL.Query()["tag"]
		for _, tag := range tags {
			if !ociref.IsValidTag(tag) {
				returnError(w, ErrManifestInvalid("invalid tag name"))
				return
			}
		}

		defer func() {
			err := r.Body.Close()
			if err != nil {
				s.logError(r.Context(), "closing manifest request body", err, "repository", name, "reference", reference)
			}
		}()
		lr := io.LimitReader(r.Body, manifestSizeLimit+1)
		b, err := io.ReadAll(lr)
		if err != nil {
			s.logError(r.Context(), "reading manifest request body", err, "repository", name, "reference", reference)
			returnError(w, ErrServerError())
			return
		}
		if len(b) > manifestSizeLimit {
			returnError(w, ErrManifestTooLarge())
			return
		}
		var dgst oci.Digest
		var tag string
		if alg, _, ok := strings.Cut(reference, ":"); ok { // is digest
			algo, err := ocidigest.LookupAlgorithm(alg)
			if err != nil {
				returnError(w, ErrDigestInvalid("unavailable algorithm"))
				return
			}
			dgst = algo.FromBytes(b)
			if dgst.String() != reference {
				returnError(w, ErrDigestInvalid(fmt.Sprintf("provided digest (%s) does not match content (%s)", reference, dgst.String())))
				return
			}
		} else {
			if !ociref.IsValidTag(reference) {
				returnError(w, ErrManifestInvalid("invalid tag name"))
				return
			}
			tag = reference
			dgst = ocidigest.FromBytes(b)
		}
		var mani oci.IndexOrManifest
		err = json.Unmarshal(b, &mani)
		if err != nil {
			returnError(w, ErrManifestInvalid("unable to parse manifest"))
			return
		}
		contentType := r.Header.Get("Content-Type")
		if contentType != "" {
			var err error
			contentType, _, err = mime.ParseMediaType(contentType)
			if err != nil {
				returnError(w, ErrManifestInvalid("invalid Content-Type"))
				return
			}
		}
		if mani.MediaType != "" && contentType != "" && mani.MediaType != contentType {
			returnError(w, ErrManifestInvalid("mediaType does not match Content-Type"))
			return
		} else if contentType == "" && mani.MediaType != "" {
			contentType = mani.MediaType
		} else if contentType == "" {
			contentType = oci.MediaTypeImageManifest // default to OCI manifest
		}
		switch contentType {
		case oci.MediaTypeImageIndex, oci.MediaTypeDockerManifestList,
			oci.MediaTypeImageManifest, oci.MediaTypeDockerManifest:
		default:
			returnError(w, ErrMediaTypeUnsupported(contentType))
			return
		}

		if mani.MediaType != contentType {
			returnError(w, ErrManifestInvalid("contentType does not match"))
		}

		if err = mani.Validate(); err != nil {
			returnError(w, ErrManifestInvalid(err.Error()))
			return
		}

		if tag != "" {
			tags = append(tags, tag)
		}
		params := &oci.PushManifestParameters{
			Digest: dgst,
			Tags:   tags,
		}
		_, err = s.db.PushManifest(r.Context(), name, b, contentType, params)
		if err != nil {
			if errors.Is(err, oci.ErrManifestBlobUnknown) {
				returnError(w, ErrManifestBlobUnknown(err.Error()))
				return
			}
			if errors.Is(err, oci.ErrSizeInvalid) || errors.Is(err, oci.ErrManifestInvalid) {
				returnError(w, ErrManifestInvalid(err.Error()))
				return
			}
			if errors.Is(err, oci.ErrDigestInvalid) {
				returnError(w, ErrDigestInvalid("digest does not match contents"))
				return
			}
			s.logError(r.Context(), "pushing manifest", err, "repository", name, "reference", reference, "digest", dgst)
			returnError(w, ErrServerError())
			return
		}
		// TODO: solve eventing.
		if mani.Subject != nil {
			// Per OCI spec, a subject manifest need not exist at push time; set the header unconditionally.
			w.Header().Set("OCI-Subject", mani.Subject.Digest.String())
		}
		u := fmt.Sprintf("/v2/%s/manifests/%s", name, reference)
		w.Header().Set("Location", u)
		w.Header().Set("Docker-Content-Digest", dgst.String())
		for _, t := range tags {
			if strings.ContainsAny(t, "\r\n") {
				continue
			}
			w.Header().Add("OCI-Tag", t)
		}
		w.WriteHeader(http.StatusCreated)
	}
}

func (s *Server) manifestDelete() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := mux.URLParam(r, "name")
		reference := mux.URLParam(r, "reference")
		var err error
		if strings.Contains(reference, ":") {
			var dgst oci.Digest
			dgst, err = ocidigest.Parse(reference)
			if err != nil {
				returnError(w, ErrManifestInvalid("invalid digest"))
				return
			}
			err = s.db.DeleteManifest(r.Context(), name, dgst)
		} else {
			if !ociref.IsValidTag(reference) {
				returnError(w, ErrManifestInvalid("invalid tag name"))
				return
			}
			err = s.db.DeleteTag(r.Context(), name, reference)
		}
		if err != nil {
			if errors.Is(err, oci.ErrManifestUnknown) || errors.Is(err, oci.ErrNameUnknown) {
				returnError(w, ErrManifestUnknown(reference))
				return
			}
			if errors.Is(err, oci.ErrReferenced) {
				returnError(w, ErrMethodNotAllowed("manifest is referenced by another manifest"))
				return
			}
			s.logError(r.Context(), "deleting manifest", err, "repository", name, "reference", reference)
			returnError(w, ErrServerError())
			return
		}
		// TODO: solve eventing.
		w.WriteHeader(http.StatusAccepted)
	}
}
