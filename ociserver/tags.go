package ociserver

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"github.com/docker/oci"
	"github.com/docker/oci/ociref"
	"github.com/docker/oci/ociserver/mux"
)

func (s *Server) tagsGet() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := mux.URLParam(r, "name")
		last := r.URL.Query().Get("last")
		if last != "" && !ociref.IsValidTag(last) {
			returnError(w, ErrBadRequest("invalid last"))
			return
		}
		limit := 0
		if n := r.URL.Query().Get("n"); n != "" {
			i, err := strconv.Atoi(n)
			if err != nil || i < 0 {
				returnError(w, ErrBadRequest("invalid n"))
				return
			}
			limit = i
		}

		// Fetch one extra tag to determine whether a next page exists without
		// relying on len(tags)==limit, which can give a false positive when the
		// total tag count is an exact multiple of the page size.
		fetchLimit := limit
		if limit > 0 {
			fetchLimit = limit + 1
		}
		tagSeq := s.db.Tags(r.Context(), name, &oci.TagsParameters{StartAfter: last, Limit: fetchLimit})
		tags, err := oci.All[string](tagSeq)
		if err != nil {
			if errors.Is(err, oci.ErrNameUnknown) {
				returnError(w, ErrNameUnknown(name))
				return
			}
			s.logError(r.Context(), "listing tags", err, "repository", name)
			returnError(w, ErrServerError())
			return
		}
		if limit > 0 && len(tags) > limit {
			tags = tags[:limit]
			lastTag := tags[len(tags)-1]
			link := fmt.Sprintf(`</v2/%s/tags/list?last=%s&n=%d>; rel="next"`, name, url.QueryEscape(lastTag), limit)
			w.Header().Set("Link", link)
		}
		w.Header().Set("Content-Type", "application/json")
		err = json.NewEncoder(w).Encode(map[string]any{
			"name": name,
			"tags": tags,
		})
		if err != nil {
			s.logError(r.Context(), "writing tags response", err, "repository", name)
		}
	}
}
