package resticrest

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/rs/zerolog"
)

// blob is one entry of a v2 listing: what restic asks for when it wants sizes
// (prune and check plan their work from them) rather than just names.
type blob struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
}

// listBlobs answers GET /{type}/ from a single List of that type's prefix.
//
// The type is whatever the mux captured, which is one of the five: a listing of
// anything else ("/packs/", "/config/") never got past the path gate.
//
// Which of the two formats comes back is decided by Accept alone, exactly as in
// rest-server: the v2 media type asks for [{name,size}], anything else — including
// no Accept at all — gets the v1 [name] array. Both are answered with the
// matching Content-Type, which is how the client knows which it got.
//
// A type holding nothing is an empty array, never 404 and never null: init
// creates no directories here (see createRepo), so "absent" and "empty" are the
// same state, and restic must be able to list a fresh repository.
func (h *handler) listBlobs(w http.ResponseWriter, r *http.Request) {
	objectType := r.PathValue("type")

	stats, err := h.store.List(r.Context(), objectType)
	if err != nil {
		h.storeError(w, r, err)
		return
	}

	if r.Header.Get("Accept") == mimeTypeAPIV2 {
		blobs := make([]blob, 0, len(stats))
		for _, st := range stats {
			if !isBlobName(st.Name) {
				continue
			}
			blobs = append(blobs, blob{Name: st.Name, Size: st.Size})
		}
		writeJSON(w, r, mimeTypeAPIV2, blobs)
		return
	}

	names := make([]string, 0, len(stats))
	for _, st := range stats {
		if !isBlobName(st.Name) {
			continue
		}
		names = append(names, st.Name)
	}
	writeJSON(w, r, mimeTypeAPIV1, names)
}

// isBlobName keeps a listing to the objects this handler itself writes: keys are
// flat, so a name carrying a separator was put there by something else and is
// not a blob of this repository. rest-server does the same in the other
// direction, skipping the files it finds outside its data/xx shards.
func isBlobName(name string) bool {
	return !strings.Contains(name, "/")
}

// writeJSON marshals the listing before touching the response, so a marshal
// failure can still be reported as a 500 rather than corrupting a body that has
// already begun.
func writeJSON(w http.ResponseWriter, r *http.Request, contentType string, payload any) {
	data, err := json.Marshal(payload)
	if err != nil {
		zerolog.Ctx(r.Context()).Error().Err(err).Str("path", r.URL.Path).Msg("marshal listing")
		httpError(w, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", contentType)
	if _, err := w.Write(data); err != nil {
		zerolog.Ctx(r.Context()).Error().Err(err).Str("path", r.URL.Path).Msg("write listing")
	}
}
