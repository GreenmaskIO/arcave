package resticrest

import (
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/rs/zerolog"

	"github.com/greenmaskio/storages"
)

// The content types restic negotiates a listing with. v2 carries sizes, v1 only
// names; the client asks by Accept and the server answers in kind.
const (
	mimeTypeAPIV1 = "application/vnd.x.restic.rest.v1"
	mimeTypeAPIV2 = "application/vnd.x.restic.rest.v2"
)

// configKey is the repository config, the one object that lives at the store
// root rather than under a type.
const configKey = "config"

// objectIDLen is the length of an object's name: restic names an object by the
// SHA-256 of its content, rendered as lowercase hex.
const objectIDLen = 64

// objectTypes is the white list of the five types an object may live under,
// mirroring rest-server's repo.ObjectTypes. A sixth would be a protocol change,
// never a configuration knob, which is why this is a literal and not an option.
var objectTypes = []string{"data", "index", "keys", "locks", "snapshots"}

// handler serves one repository over one store. It is unexported because New
// hands out an http.Handler: there is nothing to configure after construction.
type handler struct {
	store storages.Storager
}

// New returns the http.Handler serving the restic repository held in store,
// which must already be re-rooted at that repository.
//
// The routing is split in two, and the split is load-bearing: withPathGate
// decides which *paths* exist and the mux dispatches the *method* on them. A
// bare mux left to judge the path would answer three things this protocol must
// not — a 307 to the cleaned path ("/data/../config"), a 307 appending a
// trailing slash ("/nope", "/data"), and its own "404 page not found" body — and
// every one of those shapes is a flat 404 in rest-server. Behind the gate the
// mux only ever sees a path it has a route for, so none of the three is
// reachable and no route here exists to absorb them.
//
// This is why the table below carries no catch-all: adding one would give the
// mux a trailing-slash target and turn "PUT /data/<id>" from a 405 into a 307.
func New(store storages.Storager) http.Handler {
	h := &handler{store: store}
	mux := http.NewServeMux()

	mux.HandleFunc("POST /{$}", h.createRepo)

	mux.HandleFunc("HEAD /config", func(w http.ResponseWriter, r *http.Request) { h.statObject(w, r, configKey) })
	mux.HandleFunc("GET /config", h.getConfig)
	mux.HandleFunc("POST /config", h.saveConfig)
	mux.HandleFunc("DELETE /config", func(w http.ResponseWriter, r *http.Request) { h.deleteObject(w, r, configKey) })

	mux.HandleFunc("GET /{type}/{$}", h.listBlobs)
	mux.Handle("HEAD /{type}/{name}", h.blob(h.statObject))
	mux.Handle("GET /{type}/{name}", h.blob(h.getBlob))
	mux.Handle("POST /{type}/{name}", h.blob(h.saveBlob))
	mux.Handle("DELETE /{type}/{name}", h.blob(h.deleteObject))

	return withPathGate(mux)
}

// withPathGate refuses every path validPath does not define, so the mux behind
// it routes only shapes the protocol defines.
//
// It also hands the mux the very path it validated. The mux routes on the
// *escaped* path, so without this "/data%2f" — one segment to the mux, the
// listing "/data/" to the gate and to rest-server — would be judged twice, on
// two different strings. Clearing RawPath makes EscapedPath re-derive from Path,
// which is lossless: nothing in the grammar needs percent-encoding. The request
// is copied rather than mutated, as http.StripPrefix does, because a handler
// does not own the caller's request.
func withPathGate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !validPath(r.URL.Path) {
			httpError(w, http.StatusNotFound)
			return
		}
		if r.URL.RawPath != "" {
			routed := new(url.URL)
			*routed = *r.URL
			routed.RawPath = ""
			copied := new(http.Request)
			*copied = *r
			copied.URL = routed
			r = copied
		}
		next.ServeHTTP(w, r)
	})
}

// validPath reports whether path is one of the four shapes the protocol
// defines, and is the whole of its URL grammar:
//
//	/               the repository root — POST ?create=true initialises it
//	/config         the repository config
//	/<type>/        a listing of one object type
//	/<type>/<id>    one object
//
// The last two are rest-server's repo.BlobPathRE, spelled out: the type comes
// from a white list and the id from objectIDLen hex characters, optional so the
// same shape covers both a listing and an object.
//
// It is also the path-safety gate, and it is fed the *unescaped* r.URL.Path,
// exactly as rest-server matches its own regexp. A key rendered from a path this
// accepts can hold no separator beyond the one this handler writes, no "..", and
// no absolute path, so nothing that reaches the Storager was ever attacker-shaped
// — "/data/aa%2fbb" is a 404 here, not a key with a separator in it. The store's
// own key guard (storages.Guard) stays behind it as a second line, and should be
// unreachable from here.
func validPath(path string) bool {
	switch path {
	case "/", "/" + configKey:
		return true
	}

	rest, ok := strings.CutPrefix(path, "/")
	if !ok {
		return false
	}
	// Cut splits at the *first* separator, so anything nested below a type
	// ("data/aa/<id>") arrives here whole and fails isObjectID: no path deeper
	// than one segment under a type is a path at all.
	objectType, id, ok := strings.Cut(rest, "/")
	if !ok || !slices.Contains(objectTypes, objectType) {
		return false
	}
	return id == "" || isObjectID(id)
}

// isObjectID reports whether id names an object: exactly objectIDLen lowercase
// hex characters. Upper case is refused, as in rest-server — a name is a hash
// restic rendered, not one a client may spell how it likes.
//
// This is stricter than list.go's isBlobName, and deliberately not applied to a
// listing: a listing reports what the store holds, and both servers report a
// stray object under a type rather than hiding it.
func isObjectID(id string) bool {
	if len(id) != objectIDLen {
		return false
	}
	for i := range len(id) {
		if c := id[i]; (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// blob renders the "<type>/<name>" key the object handlers take out of what the
// mux captured, and is the only reason those handlers never learn there is a
// router in front of them. It validates nothing: the gate already did, and a
// second copy of the grammar here would be one that could drift from it.
func (h *handler) blob(serve func(w http.ResponseWriter, r *http.Request, key string)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		serve(w, r, r.PathValue("type")+"/"+r.PathValue("name"))
	}
}

// storeError answers a failed Storager call: a missing object is the client's
// 404, anything else is a 500 whose cause goes to the log and never to the body
// — a restic client can do nothing with it, and it may name the store's layout.
func (h *handler) storeError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, storages.ErrFileNotFound) {
		httpError(w, http.StatusNotFound)
		return
	}
	zerolog.Ctx(r.Context()).Error().Err(err).Str("method", r.Method).Str("path", r.URL.Path).
		Msg("restic rest request failed")
	httpError(w, http.StatusInternalServerError)
}

// httpError writes the status with its default description as the body, exactly
// as rest-server does — and as the mux's own 405 does — so the oracle test can
// compare bodies too.
func httpError(w http.ResponseWriter, code int) {
	http.Error(w, http.StatusText(code), code)
}
