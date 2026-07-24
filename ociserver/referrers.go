package ociserver

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/docker/oci"
	"github.com/docker/oci/ocidigest"
	"github.com/docker/oci/ociserver/mux"
)

func marshalReferrersResponse(descs []oci.Descriptor) ([]byte, error) {
	return json.Marshal(map[string]any{
		"schemaVersion": 2,
		"mediaType":     oci.MediaTypeImageIndex,
		"manifests":     descs,
	})
}

func (s *Server) referrersGet() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := mux.URLParam(r, "name")
		dgstString := mux.URLParam(r, "digest")
		artifactType := r.URL.Query().Get("artifactType")

		dgst, err := ocidigest.Parse(dgstString)
		if err != nil {
			returnError(w, ErrManifestInvalid("invalid digest"))
			return
		}
		descSeq := s.db.Referrers(r.Context(), name, dgst, &oci.ReferrersParameters{ArtifactType: artifactType})
		descs, err := oci.All(descSeq)
		if err != nil {
			if errors.Is(err, oci.ErrManifestUnknown) || errors.Is(err, oci.ErrNameUnknown) {
				body, encErr := marshalReferrersResponse([]oci.Descriptor{})
				if encErr != nil {
					s.logError(r.Context(), "encoding empty referrers response", encErr, "repository", name, "digest", dgst, "artifactType", artifactType)
					returnError(w, ErrServerError())
					return
				}
				w.Header().Set("Content-Type", "application/vnd.oci.image.index.v1+json")
				if artifactType != "" {
					w.Header().Set("OCI-Filters-Applied", "artifactType")
				}
				if _, err := w.Write(body); err != nil {
					s.logError(r.Context(), "writing empty referrers response", err, "repository", name, "digest", dgst, "artifactType", artifactType)
				}
				return
			}
			s.logError(r.Context(), "listing referrers", err, "repository", name, "digest", dgst, "artifactType", artifactType)
			returnError(w, ErrServerError())
			return
		}

		if descs == nil {
			descs = []oci.Descriptor{}
		}
		body, err := marshalReferrersResponse(descs)
		if err != nil {
			s.logError(r.Context(), "encoding referrers response", err, "repository", name, "digest", dgst, "artifactType", artifactType)
			returnError(w, ErrServerError())
			return
		}
		w.Header().Set("Content-Type", "application/vnd.oci.image.index.v1+json")
		if artifactType != "" {
			w.Header().Set("OCI-Filters-Applied", "artifactType")
		}
		if _, err := w.Write(body); err != nil {
			s.logError(r.Context(), "writing referrers response", err, "repository", name, "digest", dgst, "artifactType", artifactType)
		}
	}
}
