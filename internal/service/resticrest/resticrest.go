package resticrest

import (
	"context"
	"errors"
	"io"
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

// configPath is the URL the repository config is addressed at. It is the
// protocol's word, not the storage key of the same spelling (configKey): this
// layer never names a key.
const configPath = "/config"

// objectIDLen is the length of an object's name: restic names an object by the
// SHA-256 of its content, rendered as lowercase hex.
const objectIDLen = 64

// objectTypes is the white list of the five types an object may live under,
// mirroring rest-server's repo.ObjectTypes. A sixth would be a protocol change,
// never a configuration knob, which is why this is a literal and not an option.
var objectTypes = []string{"data", "index", "keys", "locks", "snapshots"}

// repository is the seam this package is cut along: the handler above it owns
// the protocol — what a request means, which status says so, how a listing is
// rendered — and the repository below it owns the objects. Nothing above this
// line names a storage key, and nothing below it names a status.
//
// It is the consumer's interface, declared here with the handler that calls it
// and satisfied by storeRepository. Every method takes a ctx because reaching
// the objects may cross a network, even where today's implementation does not.
//
// Two conditions cross it as sentinels — errObjectNotFound and errConfigExists
// — and fail is the whole of the mapping from them to a status.
type repository interface {
	Create(ctx context.Context) error

	StatConfig(ctx context.Context) (int64, error)
	OpenConfig(ctx context.Context) (io.ReadCloser, error)
	SaveConfig(ctx context.Context, body io.Reader) error
	DeleteConfig(ctx context.Context) error

	ListBlobs(ctx context.Context, objectType string) ([]blob, error)
	StatBlob(ctx context.Context, objectType, name string) (int64, error)
	OpenBlob(ctx context.Context, objectType, name string) (io.ReadSeekCloser, error)
	SaveBlob(ctx context.Context, objectType, name string, body io.Reader) error
	DeleteBlob(ctx context.Context, objectType, name string) error
}

// handler serves one repository over the restic REST protocol. It is unexported
// because New hands out an http.Handler: there is nothing to configure after
// construction.
type handler struct {
	repo repository
}

// New returns the http.Handler serving the restic repository held in store,
// which must already be re-rooted at that repository.
func New(store storages.Storager) http.Handler {
	return newHandler(newStoreRepository(store))
}

// newHandler routes the protocol onto repo, and is where the URL grammar lives.
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
//
// withRequestLog wraps the pair rather than sitting between them: it is what
// puts the request's logger in the context, so the gate's own 404 — and the
// mux's 405, which reaches no handler here — are logged like everything else.
func newHandler(repo repository) http.Handler {
	h := &handler{repo: repo}
	mux := http.NewServeMux()

	mux.HandleFunc("POST /{$}", h.createRepo)

	mux.HandleFunc("HEAD /config", h.statConfig)
	mux.HandleFunc("GET /config", h.getConfig)
	mux.HandleFunc("POST /config", h.saveConfig)
	mux.HandleFunc("DELETE /config", h.deleteConfig)

	mux.HandleFunc("GET /{type}/{$}", h.listBlobs)
	mux.HandleFunc("HEAD /{type}/{name}", h.statBlob)
	mux.HandleFunc("GET /{type}/{name}", h.getBlob)
	mux.HandleFunc("POST /{type}/{name}", h.saveBlob)
	mux.HandleFunc("DELETE /{type}/{name}", h.deleteBlob)

	return withRequestLog(withPathGate(mux))
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
			// WARN: restic never asks for a path outside this grammar, so one
			// that arrives is a client that is not restic, a proxy that rewrote
			// the URL, or a repository bound at the wrong root — and the flat
			// 404 on the wire lets nobody tell those apart.
			zerolog.Ctx(r.Context()).Warn().Msg("path outside the restic REST grammar")
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
// exactly as rest-server matches its own regexp. A key the repository renders
// from a path this accepts can hold no separator beyond the one it writes
// itself, no "..", and no absolute path, so nothing that reaches the Storager
// was ever attacker-shaped — "/data/aa%2fbb" is a 404 here, not a key with a
// separator in it. The store's own key guard (storages.Guard) stays behind it
// as a second line, and should be unreachable from here.
func validPath(path string) bool {
	switch path {
	case "/", configPath:
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
// This is stricter than the repository's isBlobName, and deliberately not
// applied to a listing: a listing reports what the store holds, and both servers
// report a stray object under a type rather than hiding it.
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

// fail answers a repository failure, and is the whole of the mapping from what
// the repository promises to what the protocol says: a missing object is the
// client's 404, an existing config is init's 403, and anything else is a 500
// whose cause goes to the log and never to the body — a restic client can do
// nothing with it, and it may name the store's layout.
//
// The three statuses carry three levels, and the level is not the status's
// severity: it is how likely this condition is to be the thing someone is
// trying to diagnose. A 404 is the protocol working (DEBUG), a 403 is a run
// that stopped for a reason worth naming (WARN), a 500 is ours (ERROR).
func (h *handler) fail(w http.ResponseWriter, r *http.Request, err error) {
	logger := zerolog.Ctx(r.Context())
	switch {
	case errors.Is(err, errObjectNotFound):
		// DEBUG, not WARN: restic asks for objects to find out whether they are
		// there — HEAD /config against a fresh repository, HEAD /data/<id>
		// before an upload — so a 404 is an answer and not a fault, and one
		// backup produces thousands of them.
		logger.Debug().Err(err).Msg("object not found")
		httpError(w, http.StatusNotFound)
	case errors.Is(err, errConfigExists):
		// WARN: an init against a repository that already holds a config. The
		// refusal is correct and restic reports it, but this is the line that
		// says the store was not empty, which is what the user is wondering.
		logger.Warn().Err(err).Msg("refusing to re-initialise a repository that already has a config")
		httpError(w, http.StatusForbidden)
	default:
		logger.Error().Err(err).Msg("restic rest request failed")
		httpError(w, http.StatusInternalServerError)
	}
}

// httpError writes the status with its default description as the body, exactly
// as rest-server does — and as the mux's own 405 does — so the oracle test can
// compare bodies too.
func httpError(w http.ResponseWriter, code int) {
	http.Error(w, http.StatusText(code), code)
}
