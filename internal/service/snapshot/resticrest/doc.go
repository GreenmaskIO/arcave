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
// Append-only mode is not implemented (MVP). Its insertion points are saveBlob
// and deleteObject, marked in object.go.
//
// The handler holds no state of its own beyond the store, so it is safe to serve
// concurrently: per-key atomicity is the Storager's job. Production runs restic
// with rest.connections=1, but nothing here relies on that.
package resticrest
