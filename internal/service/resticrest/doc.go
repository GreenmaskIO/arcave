// Package resticrest serves the restic REST backend protocol over a
// storages.Storager, so a restic process can back up into whatever store arcave has
// configured — a local directory today, an object store tomorrow — without
// arcave shipping a restic server or a filesystem for it to write to.
//
// The handler serves exactly ONE repository. There is no {repo} segment in the
// URL: the binding is made before the handler exists, by handing New the
// Storager the repository lives in (SubStorage("restic/<svc>/<vol>"), an
// overlay, a scratch store), and inside the container the repository is always
// addressed as rest:http+unix:///run/arcave.sock:/. That is the invariant a
// shared, multi-repo instance would break, which is why one instance serves one
// tunnel session.
//
// # The two layers
//
// The package is cut in two along the `repository` interface (resticrest.go),
// and the cut is what keeps either half readable on its own:
//
//	http.Handler  →  handler        the protocol: which paths exist, which
//	                 (handler.go)   method each takes, what Accept negotiates,
//	                                which status each outcome earns
//	              →  repository     the objects: which key each lands under,
//	                 (service.go)   which store failure means what
//
// Nothing above the line names a storage key, and nothing below it names an
// HTTP status. What crosses it is a (type, name) pair and, in the failing
// direction, one of two sentinels — errObjectNotFound and errConfigExists —
// which handler.fail is the whole of the mapping for. The interface is the
// consumer's, declared with the handler that calls it; storeRepository is its
// one implementation, and a double stands in for it in handler_test.go.
//
// # The log
//
// A third file, logging.go, wraps both: withRequestLog resolves the logger once
// per request — the context's, or the global one when the context carries none
// — stamps it with the method and path, and puts it back in the context, so
// every layer below simply reads zerolog.Ctx(r.Context()) and no line has to
// name the request again. It logs the start and the end of each request at
// DEBUG, including the status, which is the one thing an http.Handler reports
// to nobody.
//
// The levels are chosen by how often a condition is a *fault*, not by the
// severity of the status it earns, because a healthy backup must be silent
// above DEBUG: a 404 is restic asking whether an object exists (DEBUG, and
// there are thousands), while a 405, a path outside the grammar, a POST / that
// is not an init and an init refused against an existing config are all things
// no client of ours does (WARN), and a store failure is the only ERROR.
//
// The Range protocol stays on the HTTP side of that line by design: the
// repository hands back an io.ReadSeekCloser (file.go, a lazy seeker over
// GetObjectRange) and http.ServeContent does the rest, so 206, Content-Range
// and 416 are stdlib's semantics — which is exactly what rest-server relies on
// too.
//
// Semantics are mirrored from restic/rest-server's repo package — status codes,
// the Content-Length on HEAD, the v1/v2 listing content types, the order the
// path is validated in — because that is the server real restic clients are
// tested against. What is deliberately NOT mirrored:
//
//   - flat keys. rest-server shards data/ over 256 sub-directories (data/xx/id);
//     that is filesystem hygiene, invisible to the protocol, and pointless on an
//     object store. Keys here are "<type>/<name>", and "config".
//   - no temp-file + rename, no fsync, no sha256 verification of an upload. The
//     Storager has no Rename to build atomicity out of, and arcave does not buy
//     atomicity anywhere (CLAUDE.md: the record is the commit) — a half-written
//     repository from a crashed create is dropped wholesale and re-created.
//   - POST of an existing blob overwrites (200) rather than refusing (403).
//     Blob names are content-addressed, so a retry writes the same bytes.
//   - a listing of a type that holds nothing is [], not 404. rest-server answers
//     404 because init made the directory and a missing one means "no repo";
//     here init creates nothing (an object store has no directories), so a fresh
//     repository would answer 404 to every list.
//   - DELETE is idempotent, as in rest-server: restic retries deletes.
//   - a listing answers HEAD (200) rather than refusing it, and names HEAD in
//     its Allow. GET-implies-HEAD is http.ServeMux semantics, and honouring HEAD
//     is the more correct HTTP. Allow is likewise written in the mux's sorted
//     order rather than rest-server's literal "HEAD, GET, POST, DELETE"; RFC 9110
//     gives the order no meaning.
//   - no auth, no quotas, no metrics: the transport is a private stdio
//     connection to one container, and byte counts come from restic's --json.
//
// Every difference above is pinned from both sides by the differential oracle in
// oracle_test.go, so one that quietly disappears — or one that quietly appears —
// fails the tests.
//
// Append-only mode is not implemented (MVP). It is a policy over the objects,
// not over the protocol, so its insertion points are SaveBlob and DeleteBlob in
// service.go, where they are marked.
//
// Neither layer holds state of its own beyond the store, so the handler is safe
// to serve concurrently: per-key atomicity is the Storager's job. Production runs restic
// with rest.connections=1, but nothing here relies on that.
package resticrest
