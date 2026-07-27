# How restic uses this server

This document describes the restic side of the seam: what a restic repository *is* as a set of
objects, why the protocol needs ranged reads, and what each restic operation does to the store — so
that a change to `handler.go` or `service.go` can be judged against the client that will actually
talk to it.

Everything below was **observed**, not remembered: the request traces are real, captured by wrapping
`resticrest.New(store)` in a logging `http.Handler` and driving it with `restic 0.19.0` over a
directory store, repository format version 2. §11 says how to reproduce them. Where a statement is
an inference rather than an observation, it says so.

---

## 1. The mental model

restic is a **content-addressed, deduplicating, append-mostly** backup program. Three consequences
shape everything this server has to do:

1. **Every object is named by the SHA-256 of its content.** The name is not a label the client picks
   — it is a hash the client computed. Two uploads of the same name necessarily carry the same
   bytes. This is why overwriting an upload is safe (§8) and why the server never has to reconcile
   two versions of a name.
2. **Objects are immutable.** Nothing is ever modified in place. The only mutations in the whole
   protocol are *create* and *delete*. There is no PUT, no PATCH, no rename.
3. **The unit of storage is not the unit of retrieval.** restic packs many small blobs into one
   large object to keep object counts sane, then reads *back* individual blobs out of the middle of
   those objects. That mismatch is the entire reason the protocol needs HTTP Range (§5).

The REST backend is one of restic's storage backends (alongside local, S3, SFTP, …). It is
deliberately dumb: **all encryption, chunking, deduplication and integrity checking happen in the
client.** The server never sees a plaintext byte, never needs the password, and cannot tell a data
pack from an index except by the prefix it was uploaded under. Everything this package implements is
object CRUD plus a listing.

---

## 2. The repository is a flat set of objects in six namespaces

A restic repository is not a tree. It is six key spaces, five of which hold content-addressed
objects and one of which holds a single well-known object:

| Namespace | URL | What **one object** is | Typical size | At rest | Written by | Read by |
|---|---|---|---|---|---|---|
| config | `/config` | a JSON document: repo format version, repo id, chunker polynomial | ~155 B | ciphertext | `init` | every command, on startup |
| keys | `/keys/<id>` | a JSON document: KDF parameters + the **master key encrypted with your password** | ~450 B | **plaintext JSON** (only the key material inside is encrypted) | `init`, `key add` | every command, on startup |
| data | `/data/<id>` | a **pack**: many blobs concatenated + an encrypted trailer describing them. Holds file content *and* directory metadata — see §2.1 | ~16 MiB target | ciphertext | `backup`, `prune` | `restore`, `check`, `prune`, `dump`, `mount` |
| index | `/index/<id>` | a JSON document mapping **blob id → (pack id, offset, length)** for a set of packs | KB–MB | ciphertext (+zstd in v2) | `backup`, `prune`, `repair index` | every command that resolves blobs |
| snapshots | `/snapshots/<id>` | a JSON document: timestamp, paths, host, user, and the **id of the root tree blob** | ~450–500 B | ciphertext | `backup` | `snapshots`, `restore`, `forget`, `prune` |
| locks | `/locks/<id>` | a JSON document: host, pid, user, time, and an **`exclusive` flag** — see §2.1 | ~150 B | ciphertext | every command | every command |

Three properties are uniform across the table and worth stating once instead of six times:

- **The object's name is the SHA-256 of its content**, in lowercase hex, for all five `<type>/<id>`
  namespaces. `config` is the exception — its name is fixed, which is exactly why it is the one
  object whose POST must refuse to overwrite (§7.2).
- **The only mutations are create and delete.** Nothing is ever modified in place.
- **Everything is opaque to the server except the key files.** `keys/<id>` is plain JSON on disk —
  anyone who can read the store learns the KDF parameters, the creating user, host and time (the
  master key inside it is separately encrypted and useless without the password). Every other object
  is indistinguishable from random bytes, verified by hexdumping one straight off the store.

There is no directory structure inside a namespace as far as the protocol is concerned: `data/<id>`
is one key, flat. (rest-server shards `data/` into 256 sub-directories on disk — `data/ab/abcd…` —
but that is a filesystem-hygiene detail invisible to the protocol. This package stores keys flat,
see §9.)

A freshly initialised repository holds exactly **two** objects — `config` and one key — and nothing
else. This is worth stating because it is the case that breaks naive servers: four of the six
namespaces are *empty*, and restic will list them anyway.

### 2.1 The namespaces that hold more than one kind of thing

Two of the six are not homogeneous, and both surprise people.

**`data/` holds two kinds of blob — and the name lies.** A pack is a container of blobs, and a blob
is one of exactly two types:

| Blob type | What it is | Produced from | Referenced by |
|---|---|---|---|
| `data` | one chunk of **file content**, typically ~1 MiB | splitting a file by content-defined chunking | the `content` array of a file node in a tree blob |
| `tree` | one **directory listing** as JSON: for each entry its name, type, mode, mtime/atime/ctime, uid/gid, size, xattrs, and — for a file — the ordered list of data-blob ids that make it up | serialising one directory | its parent tree blob, or a snapshot's `tree` field |

So the `data/` namespace holds the *whole* backup: the file bytes and all the directory metadata.
There is no separate "metadata" namespace — a snapshot object points at a root **tree** blob, that
tree blob lives inside some pack in `data/`, and following it downward yields the rest.

A real tree blob, for a directory holding one 3-byte file (`restic cat tree <snap>:/src`):

```json
{"nodes":[{"name":"a.txt","type":"file","mode":420,"mtime":"…","uid":501,"gid":20,
  "user":"vadim","group":"staff","size":3,"links":1,
  "content":["98ea6e4f216f2fb4b69fff9b3a44842c38686ca685f3f55dc48c5d3fb1107be4"]}]}
```

That `content` array is the join back into `data` blobs, and the index is what turns each of those
ids into a byte range in a pack.

**Packs are homogeneous even though the namespace is not.** restic fills data blobs and tree blobs
into *separate* packs — verified by dumping an index after backing up three files across a directory
and one subdirectory:

```
pack 1158a22b…  →  5 blobs, all "data"   (500 KB … 1.6 MB each)
pack c9de1a5a…  →  3 blobs, all "tree"   (317 … 573 bytes each)
```

This is not cosmetic. It is what lets restic cache tree packs locally while never caching file
content (visible in §7.5 as the difference between a cold and a warm restore), and it is what
`prune --repack-cacheable-only` selects on. From the server's side it changes nothing — a pack is a
pack — but it explains why a "cold" restore fetches one whole small pack plus a few narrow ranges.

**`locks/` holds two kinds of lock**, distinguished by a field rather than by a key shape:

```json
{"time":"2026-07-25T21:05:30+03:00","exclusive":false,
 "hostname":"compound","username":"vadim","pid":62294,"uid":501,"gid":20}
```

`exclusive: false` is the shared lock every ordinary command takes; `prune` takes the exclusive one.
Enforcement is entirely client-side — each client lists `/locks/`, reads the others, and decides for
itself whether to proceed. **The server must not treat locks specially**: to it they are objects
like any other, and any cleverness here (refusing a second lock, expiring one) would break the
clients that are doing the actual coordinating.

---

## 3. What is inside each object type

### 3.1 `config` — the repository's identity

A small JSON document (155 bytes on the wire in the trace), encrypted with the master key —
`restic cat config` decrypts it for you:

```json
{"version": 2,
 "id": "21e1842c0518a221d8ff7da89ba83f7852cd89cb94ecbd29c917b2def3011d2d",
 "chunker_polynomial": "39a0178e2dcb05"}
```

It carries:

- the repository **format version** (1 = original, 2 = adds zstd compression; restic 0.19 creates v2
  by default — verified with `restic cat config`, which is also how to read this object yourself),
- a repository **id**,
- the **chunker polynomial** — the per-repository random polynomial that parameterises
  content-defined chunking. This is why a repository's deduplication is self-consistent and why the
  config must exist before anything else can be written.

**Its role in the protocol is a probe.** `HEAD /config` is how restic asks "is there a repository
here?" — it is the *first request of every single command* (see every trace below). A 404 means "not
a repository"; a 200 means "yes, and it is this many bytes".

`POST /config` is therefore the commit point of `init`, and it is the one write in the whole protocol
that **must not overwrite** (§7.1).

### 3.2 `keys/<id>` — password → master key

Each key object is a JSON document holding the KDF parameters and the master key *encrypted with the
password-derived key*. It is the only object **not** encrypted with the master key — it could not
be, since it is what yields the master key. Read straight off the store, with no password at all:

```json
{"created":"2026-07-25T21:05:29+03:00","username":"vadim","hostname":"compound",
 "kdf":"scrypt","N":32768,"r":8,"p":13,
 "salt":"RTTvnhsMsCaLFcqhDnr/…","data":"hw2Wlj36FXbTskiDY1A/p46rdCX3Fk…"}
```

`data` is the encrypted master key and is useless without the password, but everything around it is
in the clear — this is the one object that tells whoever holds the store *something*: when the
repository was keyed, by which user, on which host, with which KDF cost parameters. Worth knowing
when deciding where a store may live; it changes nothing about how this server treats the object.

Consequence for the server: none, beyond CRUD. Consequence for the trace: every command opens with
`GET /keys/` (list) followed by `GET /keys/<id>` (fetch), then `GET /config` — that four-request
prelude is the client unlocking the repository. Multiple keys may exist (`restic key add`); restic
tries them in turn against the supplied password.

### 3.3 `data/<id>` — pack files, the bulk of the repository

A **pack** is a container. Its layout is:

```
┌───────────┬───────────┬─────┬───────────┬──────────────────┬───────────────┐
│  blob 1   │  blob 2   │ ... │  blob N   │  header (encr.)  │ header length │
│ (encr.)   │ (encr.)   │     │ (encr.)   │  one entry/blob  │  (4 bytes)    │
└───────────┴───────────┴─────┴───────────┴──────────────────┴───────────────┘
 ◄──────────── the blobs, back to back ────────────►◄──── the trailer ──────►
```

Each header entry names one blob: its type (data or tree), its length, its uncompressed length (v2
only), and its 32-byte id. The header is at the **end** because a pack is written streaming — the
client does not know what will fit until it has finished filling it.

The blobs inside are of two types — `data` and `tree` — and a given pack holds only one of them;
§2.1 has the breakdown and a real example of each.

Packs target **~16 MiB** (`--pack-size`, default 16); the observed run produced four packs totalling
40 MiB from 40 MiB of incompressible input, the largest 17.2 MiB. A file is split into chunks by
**content-defined chunking** (a rolling hash over the content, so an insertion in the middle shifts
only the chunk it lands in, not every chunk after it), which is what makes incremental backups
cheap. Chunk sizes are roughly 512 KiB – 8 MiB, averaging ~1 MiB.

**This is the object that makes Range mandatory.** A single 8 MiB file's chunks may sit anywhere
inside a 17 MiB pack, and restic will ask for exactly that byte span.

### 3.4 `index/<id>` — the blob → pack map

An encrypted (and in v2, compressed) JSON document answering the only question restic asks
constantly: *"blob `abc…` — which pack is it in, at what offset, and how long is it?"* One real
index, covering the two packs of §2.1, abbreviated to two entries:

```json
{"packs":[
  {"id":"1158a22b…","blobs":[
    {"id":"5891b5b5…","type":"data","offset":0,"length":47,"uncompressed_length":6},
    {"id":"b311dc9b…","type":"data","offset":47,"length":500053,"uncompressed_length":500000}]},
  {"id":"c9de1a5a…","blobs":[
    {"id":"e86d39be…","type":"tree","offset":0,"length":317,"uncompressed_length":459}]}]}
```

`offset` and `length` are the byte span **inside the pack** — they are literally the `Range` header
of the next request (§5), which is the clearest statement of why the index and ranged reads are one
design and not two.

Without the index, finding a blob would mean reading every pack's header. That is precisely what
`restic repair index` does when the index is lost, and §5.2 shows what it costs.

Indexes are **append-only in effect**: each backup writes a new index object covering the packs it
just uploaded. `prune` is what consolidates them (in the trace: 3 index objects → 1).

### 3.5 `snapshots/<id>` — the backup itself

A small JSON document (446–502 bytes in the trace):

```json
{"time":"2026-07-25T21:05:12+03:00",
 "tree":"fb87281558583296ce9e4e2f2b775c58cbd2ae7f9a5ed90fe66c6383f50994d8",
 "paths":["/srv/data"],"hostname":"compound","username":"vadim","uid":501,"gid":20,
 "program_version":"restic 0.19.0",
 "summary":{"backup_start":"…","backup_end":"…","files_new":1,"files_changed":0,
            "files_unmodified":0,"dirs_new":1,"…":"…"}}
```

`tree` is the id of the **root tree blob** — the single thread from which the entire backup hangs.
A `parent` field (the previous snapshot's id) and `tags` appear when applicable.

This is the entire "backup" as an entity. Everything else — trees, chunks, packs — is shared
content. Hence the single most important operational fact in this document:

> **Deleting a backup is deleting one small JSON object.** It frees no space by itself. Space is
> reclaimed later, by `prune`. See §7.7 and §7.8.

### 3.6 `locks/<id>` — cooperative mutual exclusion

A small JSON document naming the host, PID, user, creation time, and whether the lock is
**exclusive** — §2.1 has a real one. Locking is entirely cooperative and lives in the client: the
server has no idea a lock is a lock, and must not treat it specially.

The protocol shape (visible in every trace) is:

```
GET  /locks/          →  list existing locks
POST /locks/<id>      →  create my own
GET  /locks/          →  list again, and check nobody conflicts
   ... the actual work ...
DELETE /locks/<id>    →  release
```

Two consequences for us:

- **Every command locks, including read-only ones.** `restic snapshots` and `restic restore` both
  create and delete a lock. A server that treated `/locks/` as optional would break everything.
- **Long operations refresh their lock** — restic re-uploads it periodically so that a crashed
  client's lock can be recognised as stale. So a `POST /locks/<id>` can arrive at any moment during
  a long backup, not just at its start. Stale locks are cleared by `restic unlock`.

---

## 4. How one file becomes objects

Read bottom-up, this is also how a restore works in reverse.

```
  /fixture/b.txt  (1.2 MB)
        │
        │  content-defined chunking (polynomial from config)
        ▼
  chunk₁ chunk₂ chunk₃ …            ← content-addressed data blobs
        │
        │  each blob: compress (v2) → encrypt → append to the current pack
        ▼
  data/<pack-id>                     ← POST /data/<id>   (~16 MiB objects)
        │
        │  where each blob landed: (blob-id → pack-id, offset, length)
        ▼
  index/<index-id>                   ← POST /index/<id>
        │
        │  the directory listing referencing the data blobs, itself a blob
        ▼
  tree blob → also packed into data/<pack-id>
        │
        ▼
  snapshots/<snapshot-id>            ← POST /snapshots/<id>   (the commit)
```

The **snapshot object is written last, and it is the commit marker** — exactly the ordering arcave
uses for its own `savepoint.json` (see the root `CLAUDE.md`). A backup that dies halfway leaves
packs and indexes that no snapshot references: wasted space, but not a corrupt repository, and
`prune` collects them.

---

## 5. Why Range read exists, and what it must do

### 5.1 The measurement

A repository of 5 × 8 MiB incompressible files → 4 packs, 40 MiB total, largest 17.2 MiB. Restoring
**one** of the five files, cold target:

```
SELECTIVE RESTORE: 2 requests to /data/, 8.55 MiB transferred (of 40.0 MiB stored)
  Range: bytes=0-1958115           →  206, 1.87 MiB   (a chunk at the head of one pack)
  Range: bytes=7738163-14745557    →  206, 6.68 MiB   (a span in the MIDDLE of a 17.2 MiB pack)
```

The second range is the whole argument. Without ranged reads, retrieving that blob means
transferring the pack that contains it — and, in general, every pack that holds any chunk of the
wanted file. The cost is not a constant factor: it is *(bytes you want)* versus *(bytes of every
pack any of your chunks happen to live in)*.

For comparison, a full restore of the same repository is 3 requests and 40.00 MiB — restic asks for
whole packs there, as `bytes=0-`, so the same code path serves both.

### 5.2 The extreme case: header reads

When the index is gone, restic rebuilds it by reading **only the trailer of every pack**:

```
$ rm -rf <store>/index && restic repair index
REPAIR INDEX: 4 ranged reads of /data/, 0.00 MiB transferred (of 40.0 MiB stored)
  Range: bytes=6537385-6538035      →  651 bytes
  Range: bytes=3917-4567            →  651 bytes
  Range: bytes=17386526-17387176    →  651 bytes
  Range: bytes=18021205-18021855    →  651 bytes
```

40 MiB of packs indexed by transferring 2.6 KiB. Note that the read size is a *constant* 651 bytes
regardless of pack size — restic eagerly reads a fixed number of header entries and issues a second,
larger read only if the header proves bigger. (The constant is consistent with 4 bytes of header
length + 32 bytes of crypto overhead + 15 entries × 41 bytes; the arithmetic is an inference, the
651 is measured.)

### 5.3 What the server must therefore support

| Requirement | Observed as | Our implementation |
|---|---|---|
| Open-ended range (`bytes=N-`) | every full object read: `bytes=0-` | `http.ServeContent` |
| Explicit span (`bytes=N-M`), including mid-object | selective restore, header reads | `http.ServeContent` |
| `206 Partial Content` + `Content-Range` | all of the above | `http.ServeContent` |
| `416` on an unsatisfiable range | not issued by restic; pinned by the oracle | `http.ServeContent` |
| Transfer **only** the requested bytes | the 8.55-of-40 MiB result | `storagerFile` + `GetObjectRange` |

The last row is the one that is ours to get wrong, and it is not something `ServeContent` can do for
us. `ServeContent` needs an `io.ReadSeeker`, and the naive way to produce one from an object store —
download the object, then seek within it — satisfies the *protocol* perfectly while defeating its
entire purpose. `file.go` exists to prevent that:

- `Seek` is **arithmetic over the size**, opening nothing. This matters because `ServeContent`'s
  first act is to seek to the end and back, to measure the object.
- The stream is opened by the **first `Read` after a seek**, as a single `GetObjectRange(pos, -1)`.
- A subsequent seek **discards** the open stream, so the next read re-opens at the new offset.

So a plain `GET` costs one ranged fetch covering the whole object, and a `Range` request costs one
ranged fetch starting at the offset — the bytes before it never move.
`TestStoragerFile_Read_RangedGetFetchesOnlyTheRange` asserts exactly one `GetObjectRange` at exactly
the right offset, and it is the test that fails if anyone "simplifies" this into a download.

One more subtlety worth keeping: `getBlob` sets `Content-Type` explicitly. Without it,
`ServeContent` sniffs — a 512-byte read from offset 0 followed by a seek back — which is an *extra
ranged fetch on every GET*, including the ranged ones this machinery exists to keep cheap.

> **Not used, but supported:** suffix ranges (`bytes=-N`). Zero were observed across the whole trace
> — including the pack-header reads, where a suffix range would be the natural spelling and restic
> instead computed an explicit `bytes=start-end` from the size it already knew. We answer them
> correctly anyway because `ServeContent` does, and the oracle test pins the behaviour, but nothing
> here should be *designed* around them.

---

## 6. The HTTP surface

The complete grammar — four path shapes, nothing else exists:

| Path | Methods | Meaning |
|---|---|---|
| `/` | `POST ?create=true` | initialise the repository |
| `/config` | `HEAD` `GET` `POST` `DELETE` | the repository config |
| `/<type>/` | `GET` (+`HEAD`) | list one namespace |
| `/<type>/<id>` | `HEAD` `GET` `POST` `DELETE` | one object |

where `<type>` ∈ {`data`, `index`, `keys`, `locks`, `snapshots`} and `<id>` is exactly 64 lowercase
hex characters. Anything else is a flat `404` — decided by `validPath` in `resticrest.go`, *before*
the mux, which is also the path-safety gate: a key rendered from a path that passes it cannot contain
a separator, a `..`, or an absolute path.

Listings are content-negotiated by `Accept`:

| `Accept` | Response `Content-Type` | Body |
|---|---|---|
| `application/vnd.x.restic.rest.v2` | same | `[{"name":"<id>","size":N}, …]` |
| anything else, or absent | `application/vnd.x.restic.rest.v1` | `["<id>", …]` |

**restic 0.19 sends `Accept: …v2` on every request** — including `HEAD`, `POST` and `DELETE`, where
it means nothing. The server must therefore treat `Accept` as meaningful *only* on the listing path,
which is exactly where `handler.listBlobs` reads it. Sizes matter to `prune` and `check`, which plan
their work from them without fetching anything.

---

## 7. The operations

Each subsection gives: what restic is doing, the observed request stream, the behaviour the server
must exhibit, and where that lands in this package.

Common prelude — **every** command starts with these four requests (the repository probe and the
unlock), and they are elided from the traces below:

```
HEAD /config                          → 200        "is this a repository?"
GET  /keys/           Accept: v2      → 200 [ids]  "which keys exist?"
GET  /keys/<id>       Range: bytes=0- → 206        "give me one"
GET  /config          Range: bytes=0- → 200        "the format version and chunker polynomial"
```

Note the last line: restic sends a `Range` on `/config` too, and gets a **200, not a 206** — this
server does not serve the config through `ServeContent`, so the range is ignored. That is deliberate
parity with rest-server, and restic does not care.

### 7.1 Creating a repository — `restic init`

Creates the namespace, the first key, and the config.

```
 1. HEAD   /config                     → 404       is something already here?
 2. POST   /?create=true               → 200       create the repository
 3. HEAD   /config                     → 404       re-check after creating
 4. GET    /keys/            Accept:v2 → 200  []   must be empty
 5. GET    /snapshots/       Accept:v2 → 200  []   must be empty
 6. POST   /keys/<id>                  → 200       the password-derived key
 7. POST   /config                     → 200       the commit
```

Layout afterwards: `config=1 keys=1`. Nothing else exists.

**Expected behaviour:**

- `POST /` **requires** `?create=true`; a bare `POST /` is a `400`. Answering it 200 would report a
  repository nobody asked for.
- Steps 4–5 list namespaces that have never been written. They must answer **`200 []`**, not 404.
  This is our one deliberate deviation from rest-server that a real client exercises on its very
  first command: rest-server's `init` creates directories on disk so a missing one means "no repo",
  while here `Create` creates nothing (an object store has no directories), so *absent* and *empty*
  are the same state. Had we copied rest-server's 404, `restic init` would fail at step 4.
- `POST /config` must be **exclusive**: if a config already exists, refuse with `403` and leave the
  existing bytes untouched. This is what stops a second `init` from silently orphaning a repository
  full of data — restic surfaces the 403 to the user as "repository master key and config already
  initialized".

**In this package:** `handler.createRepo` (the `create=true` gate) → `storeRepository.Create` (a
logged no-op). `handler.saveConfig` → `storeRepository.SaveConfig`, whose `Exists`-then-`PutObject`
pair returns `errConfigExists`, which `handler.fail` maps to 403. `TestStoreRepository_SaveConfig`
pins both the refusal and that the refused write did not touch the stored bytes.

### 7.2 Creating the config on its own

There is no separate "create config" operation in restic — `POST /config` happens only inside
`init`, and only once in a repository's lifetime. `DELETE /config` exists and is what effectively
destroys a repository (restic itself never issues it; rest-server supports it and so do we).

The important property is the asymmetry: **config is the only object whose POST refuses to
overwrite.** Every other namespace is content-addressed, so overwriting is a no-op by construction;
the config is *not* content-addressed — its name is fixed — so an overwrite would silently replace
the repository's identity and chunker polynomial, making every existing pack undecryptable.

### 7.3 Creating a backup — `restic backup <path>` (first run)

```
 5. GET    /locks/           Accept:v2 → 200  []      no locks
 6. POST   /locks/<id>                 → 200          take one
 7. GET    /locks/           Accept:v2 → 200 [id]     verify no conflict
 8. GET    /snapshots/       Accept:v2 → 200  []      find a parent snapshot (none)
 9. GET    /index/           Accept:v2 → 200  []      load the blob→pack map (empty)
10. POST   /data/<id>                  → 200          pack #1
11. POST   /data/<id>                  → 200          pack #2
12. POST   /index/<id>                 → 200          where those blobs went
13. POST   /snapshots/<id>             → 200          THE COMMIT
14. DELETE /locks/<id>                 → 200          release
```

Layout afterwards: `config=1 data=2 index=1 keys=1 snapshots=1`.

**Expected behaviour:**

- Uploads are **streamed**: a pack is up to ~16 MiB (and `--pack-size` can raise that a lot). The
  server must never buffer a body to measure it. `handler.saveBlob` hands `r.Body` to the store
  unread.
- An upload of a name that already exists must **succeed** (see §9). restic retries uploads after a
  network failure, and the retry carries identical bytes.
- Order is the crash-safety story and it is the client's: data → index → snapshot. The server
  guarantees nothing about it and must not try to.

**In this package:** `handler.saveBlob` → `storeRepository.SaveBlob` → `PutObject`. The whole path
is four lines of code, which is the point.

### 7.4 Creating a backup — second run (incremental)

The request *shape* is identical (14 requests). What changes is what is inside them:

```
 8. GET    /snapshots/  → 200 [1 id]   ← the parent snapshot is found
 9. GET    /index/      → 200 [1 id]   ← the existing index is loaded
10-11. POST /data/<id>  x2             ← only the packs holding NEW chunks
```

restic uses the parent snapshot to skip re-reading files whose metadata is unchanged, and the index
to skip re-uploading chunks it already has. Unchanged files cost **zero** requests. In the trace the
second backup changed one file and added another, and uploaded two packs.

**Server-side implication: none.** Incrementality is entirely a client-side decision made from the
index and the parent snapshot. The server's only contribution is answering the listings fast and
correctly — an over-eager listing (one that hides objects, or invents them) directly corrupts that
decision.

This is worth stating plainly because it constrains one of our behaviours: `isBlobName` filters
listing entries that contain a `/`. That filter must never drop a real object, or restic would
conclude a chunk is missing and re-upload it — or, worse, `prune` would conclude a pack is
unreferenced.

### 7.5 Restoring from scratch — `restic restore latest --target <empty dir>`

Cold cache (nothing local):

```
 5-7.  locks
 8. GET /snapshots/                        → 200 [2 ids]
 9. GET /snapshots/<id>  Range: bytes=0-   → 206   resolve "latest": read both
10. GET /snapshots/<id>  Range: bytes=0-   → 206
11. GET /index/                            → 200 [2 ids]
12. GET /index/<id>      Range: bytes=0-   → 206   the blob→pack map
13. GET /index/<id>      Range: bytes=0-   → 206
14. GET /data/<id>       Range: bytes=0-   → 206   the pack holding the TREE blobs
15. GET /data/<id>       Range: bytes=157-388  → 206   file content, mid-pack
16. GET /data/<id>       Range: bytes=0-301    → 206   file content
17. DELETE /locks/<id>
```

Warm cache — the same restore into a different empty target — is 12 requests: steps 9–10, 12–14
disappear. restic caches indexes, snapshots and **tree-bearing packs** locally (`--cache-dir`,
`--no-cache` to disable); **file content is never cached.** That is exactly the difference visible
between the two runs: the full pack read at step 14 vanishes, the two content ranges remain.

**Expected behaviour:** correct `206`/`Content-Range` on arbitrary spans, and only the requested
bytes transferred. Resolving `latest` reads *every* snapshot object, so a repository with many
snapshots issues many small GETs — cheap, but not zero.

**In this package:** `handler.getBlob` → `storeRepository.OpenBlob` → `statObject` (size, or
`errObjectNotFound` → 404 *before* any byte is opened) → `newStoragerFile` → `http.ServeContent`.

### 7.6 Restoring incrementally — restore over a populated target

restic compares the target's existing files against the snapshot before fetching anything.

Restoring the *same* snapshot over a target that already holds it:

```
10 requests total — the prelude, the locks, GET /snapshots/, GET /index/ — and
ZERO requests to /data/.
```

Restoring a *different* (older) snapshot over that populated target:

```
12 requests — the same, plus exactly two /data/ reads:
  GET /data/<id>  Range: bytes=0-      → 206   (the tree pack)
  GET /data/<id>  Range: bytes=0-156   → 206   (the one differing chunk)
```

So an incremental restore is *the same operation* as a full one; the difference is entirely in how
many blobs turn out to be needed. This is the property that makes restic attractive for arcave's
rollback: rolling a volume back to a savepoint that differs in a few pages moves a few pages.

**Server-side implication:** none beyond §5 — but it raises the value of Range considerably, because
the ratio of "bytes wanted" to "bytes stored" gets small precisely in the case one runs most often.

### 7.7 Deleting a backup — `restic forget <id>`

```
 8. GET    /snapshots/       → 200 [2 ids]
 9. DELETE /snapshots/<id>   → 200            ← the entire operation
10. DELETE /locks/<id>
```

Layout before: `data=4 index=2 snapshots=2`. Layout after: `data=4 index=2 snapshots=1`.

**Not one byte of data was freed.** `forget` removes the *reference*; the packs it referenced are
still there, now possibly unreferenced. This is the operation the question "how do I delete a backup"
usually means, and it is only half the answer.

**When to call it:** whenever a backup is no longer wanted. It is cheap (one DELETE), safe, and
takes a shared lock. It accepts retention policy flags rather than ids —
`--keep-last N`, `--keep-daily N`, `--keep-within 7d`, `--tag`, and `--dry-run` — which is how it is
normally driven:

```bash
restic forget --keep-last 10 --prune          # policy + immediate reclaim
restic forget <snapshot-id>                    # one specific backup, no reclaim
```

**Expected behaviour:** `DELETE` must be **idempotent**. restic retries deletes, and a retry landing
after the first one succeeded must not fail the command. Note this inverts the underlying store's
semantics: `storages.Storager.Delete` reports a missing object as an error, deliberately, so a caller
that meant to delete something learns it did not. `storeRepository.deleteObject` absorbs the
difference by swallowing `storages.ErrFileNotFound` — in the repository, not in the store, and not in
the handler.

### 7.8 Reclaiming space — `restic prune` (the repack operation)

This is the operation that actually frees bytes, and the most complex thing the server sees.

```
 8. GET    /index/          Accept:v2 → 200 [3 ids]     what the client thinks exists
 9. GET    /snapshots/      Accept:v2 → 200 [1 id]      what is still referenced
10. GET    /data/           Accept:v2 → 200 [4 entries] what ACTUALLY exists, WITH SIZES
11. GET    /data/<id>  Range: bytes=0-231 → 206         read the blobs worth keeping
12. POST   /data/<id>                     → 200         write them into a NEW pack
13. POST   /index/<id>                    → 200         the new index
14. POST   /index/<id>                    → 200
15. DELETE /index/<id>     x3                           drop the old indexes
18. DELETE /data/<id>      x2                           drop the now-dead packs
20. DELETE /locks/<id>
```

Layout before: `data=4 index=2`. After: `data=3 index=1`.

**What is happening.** After `forget`, some packs are fully unreferenced (delete them outright) and
some are *partially* referenced — a 16 MiB pack in which 2 MiB is still live. Deleting it would lose
live data; keeping it wastes 14 MiB. So restic **repacks**: it ranged-reads the surviving blobs out
of such packs, writes them into fresh packs, rewrites the index, and only then deletes the old packs.
Order matters and is the client's: new data → new index → delete old index → delete old data.

Note step 10: `prune` is the operation that **lists `/data/` with sizes**, and it is why the v2
listing carries them. It cross-checks the index against reality — a pack in the index but not in the
listing is a missing file; one in the listing but not in the index is unreferenced; a size mismatch
is corruption. Getting `size` wrong in a v2 listing therefore misleads `prune` into deleting or
repacking the wrong things.

**When to call it:** after `forget`, when space actually needs reclaiming. It is expensive — it may
read and rewrite a large fraction of the repository — and restic documents it as the operation that
takes an **exclusive** lock, so nothing else may run against the repository meanwhile. Useful knobs:

```bash
restic prune                          # full
restic prune --dry-run                # report what would happen
restic prune --max-repack-size 1G     # bound the rewriting work
restic prune --max-unused 10%         # tolerate some waste instead of repacking
restic forget --keep-last 10 --prune  # the usual combined form
```

**Expected behaviour from the server:** nothing new — but everything at once, and under an
assumption of correctness that the other operations do not stress. Listings with accurate sizes,
ranged reads at arbitrary offsets, uploads, and idempotent deletes. If our listing hides an object
(§7.4), `prune` will consider it garbage; if our `size` is wrong, it will consider it corrupt.

### 7.9 Verifying — `restic check [--read-data]`

```
 9. GET /index/<id>   Range: bytes=0-  → 206      the index, in full
10. GET /data/        Accept: v2       → 200      every pack, with sizes
11. GET /snapshots/<id> Range: bytes=0- → 206
12. GET /data/<id>   Range: bytes=0-   → 206      each pack, IN FULL (--read-data only)
```

Without `--read-data`, `check` verifies structure: every blob the index claims exists in a pack of
the size claimed, and every snapshot's trees resolve. With `--read-data` it downloads and re-hashes
every pack — in the measured repository, 40.01 MiB across 5 requests, i.e. the whole repository.

This is our cheapest end-to-end proof: `conformance_test.go` runs `check --read-data` against a real
restic binary, so if this server ever corrupts, truncates or mis-ranges anything, that test fails.

---

## 8. The behaviour contract, in one table

Everything the client depends on, and where it is pinned.

| # | Behaviour | Why restic needs it | Pinned by |
|---|---|---|---|
| 1 | `HEAD /config` → 404 on a fresh store, 200 + `Content-Length` afterwards | the "is this a repository?" probe opening every command | oracle, `TestStoreRepository_StatConfig` |
| 2 | `POST /config` refuses an existing config with 403 | a second `init` must not orphan live data | oracle, `TestStoreRepository_SaveConfig` |
| 3 | `POST /?create=true` required; bare `POST /` → 400 | a stray POST must not report a repository | `TestHandler_createRepo` |
| 4 | Empty namespace → `200 []`, never 404, never `null` | `init` lists `/keys/` and `/snapshots/` before writing anything | `TestStoreRepository_ListBlobs`, oracle |
| 5 | `POST /<type>/<id>` overwrites an existing object (200) | retried uploads after a failure | oracle (pinned deviation), `TestStoreRepository_SaveBlob` |
| 6 | `DELETE` of a missing object → 200 | restic retries deletes during `prune`/`unlock` | oracle, `TestStoreRepository_Delete` |
| 7 | Ranged `GET` → 206 + `Content-Range`, only the requested bytes moved | reading one blob out of a pack | oracle, `TestStoragerFile_Read_RangedGetFetchesOnlyTheRange` |
| 8 | v1/v2 listing chosen by `Accept`, answered with the matching `Content-Type` | `prune`/`check` plan from sizes | `TestHandler_listBlobs`, oracle |
| 9 | Listing reports only flat keys of that type | a hidden object reads as "missing"; a stray one as garbage | `TestStoreRepository_ListBlobs_SkipsNestedKeys` |
| 10 | Any path outside the grammar → flat 404 (no redirect, no stdlib body) | rest-server parity; also the path-safety gate | `TestHandler_ServeHTTP_PathValidation`, oracle |
| 11 | Wrong method on a valid path → 405 with `Allow` | restic uses a general HTTP client | `TestHandler_ServeHTTP_MethodNotAllowed` |

---

## 9. Where this server deliberately differs from rest-server

`restic/rest-server` is the reference implementation and is imported as a **test-only** oracle:
`oracle_test.go` replays the same request stream against both and compares status, headers and body.
Each difference below is pinned from *both* sides, so one that quietly disappears — or one that
quietly appears — fails the test.

| Difference | Why |
|---|---|
| **Flat keys** (`data/<id>`), no `data/xx/` sharding | filesystem hygiene, invisible to the protocol, pointless on an object store |
| **No temp-file + rename, no fsync, no upload hash check** | `Storager` has no `Rename` to build atomicity from, and arcave buys atomicity nowhere (the record is the commit); a half-written repository is dropped and re-created |
| **Overwrite on re-POST (200) instead of 403** | names are content-addressed, so a retry carries the same bytes; refusing it would fail a retry after a half-written object |
| **Empty namespace → `[]`, not 404** | `init` creates no directories here, so *absent* and *empty* are one state — and §7.1 shows `restic init` depends on it |
| **`HEAD` on a listing → 200, not 405** | GET-implies-HEAD is `http.ServeMux` semantics and the more correct HTTP |
| **No auth, no quotas, no metrics, no append-only** | the transport is a private connection to one container; see §10 |

---

## 10. Not implemented

- **Append-only mode.** rest-server can refuse deletes and overwrites so that a compromised client
  cannot destroy history. It is a policy over the *objects*, not over the protocol, so its insertion
  points are `SaveBlob` and `DeleteBlob` in `service.go`, where they are marked. Note that it must
  exempt `locks/`, which every command deletes.
- **Authentication, quotas, metrics.** The intended transport is a private stdio/unix-socket
  connection to a single container, and byte counts come from `restic --json`.
- **Multi-repository serving.** One instance serves exactly one repository — there is no `{repo}`
  segment in the URL; the binding is the `Storager` handed to `New`.

---

## 11. Reproducing the traces

The traces above were produced by a throwaway test that wraps the handler in a logging
`http.Handler`, drives a real `restic` binary against it over `httptest.NewServer`, and prints one
line per request. The wrapper is no longer needed — `logging.go` logs both ends of every request,
with the range, the Accept, the status and the bytes written — so regenerating them is:

1. Point the logger at stderr at DEBUG instead of silencing it, by editing `TestMain` in
   `logging_test.go` to `log.Logger = zerolog.New(os.Stderr).Level(zerolog.DebugLevel)`, and read the
   `restic rest request` / `restic rest request served` pairs. (The whole conformance run is ~270
   lines.)
2. Drive it with `exec.Command("restic", "-r", "rest:"+srv.URL+"/", "--cache-dir", <tmp>, …)` and
   `RESTIC_PASSWORD` in the environment. Use a dedicated cache dir — the default `~/.cache/restic`
   makes runs non-reproducible.
3. For range measurements, use **incompressible** fixtures (`rand`), or v2 compression will shrink
   the packs to nothing and the numbers will not mean anything.

`conformance_test.go` already does the driving half of this and is the permanent version of the same
idea: it runs `init → backup → check --read-data → restore → forget → prune → check` against a real
binary, and is skipped by `-short` and on hosts without `restic` on `PATH`.

---

## 12. Relevance to arcave

This is forward-looking: arcave's savepoint/rollback flows use `snapshot.CopyBackend` (a whole-tar
copy per mount) today, and **nothing in arcave calls restic yet**. The mapping this server exists to
enable is:

| arcave operation | restic operation | what the object model buys |
|---|---|---|
| `savepoint create` | `backup` | unchanged blocks are not re-stored across savepoints |
| `rollback` | `restore` | only the differing blocks move (§7.6) |
| `savepoint remove` | `forget` | one small DELETE |
| a future `savepoint gc` | `prune` | the only operation that reclaims, and the expensive one |

The last row is the operational catch worth designing for now: with restic, *deleting a savepoint
does not free space*. Something has to run `prune`, it is expensive, and it wants exclusive access to
the repository — so it belongs to a deliberate, schedulable maintenance operation, not to the tail of
`arcave savepoint remove`.
