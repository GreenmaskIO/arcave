// Copyright 2026 Greenmask Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

/*
Package dedupcopy splits a container's committed image between a
content-addressed blob store shared by every capture and that capture's own
metadata, so a base layer every savepoint carries is written once.

Its restore re-tars the stored members byte for byte, in the recorded order,
rather than reassembling the image: an image's id is the digest of bytes any
reassembly would rewrite.

It needs a daemon whose `docker save` writes an OCI layout — Docker 25 and newer.
An older one exports member names carrying no digest, and a capture against it is
refused rather than guessed at.

# What the daemon hands over

`docker save` of a committed image is one tar. Its members, in stream order:

	blobs/                        a directory member, skipped
	blobs/sha256/                 a directory member, skipped
	blobs/sha256/025fe19496…      a layer: a tar of one filesystem diff, gzipped
	blobs/sha256/2cbc9a9102…      another layer
	blobs/sha256/ccc80d1f1f…      the image config, JSON
	blobs/sha256/b54cd6fb26…      the OCI manifest, JSON — and the image's own id
	index.json                    names the manifest blob
	manifest.json                 the legacy docker view, kept for compatibility
	oci-layout                    the format marker

Not every blob is a layer: layers, config and manifest are all blobs, and only
the layers are themselves tars. Nothing here reads any of them. A blob is opaque
bytes under a name that is already the digest of those bytes, which is what makes
one pass over the stream enough — no decompression, no hashing to find a key.

# Where it goes

Blobs are shared by every capture under one compose project; the three metadata members and
the manifest belong to the capture that wrote them.

	<compose project root>/
	  shared/oci/blobs/sha256/<digest>   every layer, config and manifest, once
	  <savepoint-id>/
	    savepoint.json
	    project.yaml
	    images/<service>/replicas/<n>/
	      index.json                     verbatim from the archive
	      manifest.json                  verbatim from the archive
	      oci-layout                     verbatim from the archive
	      image.json                     what to rebuild from — written last

`image.json` is the capture's commit marker: a capture that died halfway leaves
none, and nothing reads a capture without one.

# A capture, then a second

The first savepoint of a service writes every blob. Paths are under the same compose project
root as above:

	<compose project root>/
	  shared/oci/blobs/sha256/025fe19496…   4.1 MB   the base layer
	  shared/oci/blobs/sha256/2cbc9a9102…   2.0 KB   what the container wrote
	  shared/oci/blobs/sha256/ccc80d1f1f…   1.4 KB   the config
	  shared/oci/blobs/sha256/b54cd6fb26…   0.5 KB   the manifest
	  1780335122/images/db/replicas/1/…     its four small files

The second savepoint of the same service re-streams the same archive, but the
base layer's digest is already a key in the store with the size the archive
declares, so those bytes are read past and not written again. What it adds is the
new writable layer, a new config, a new manifest — kilobytes against megabytes.
That is the whole of the saving: measured at 63.9% over three savepoints of
postgres:16.

# What it does not do

Nothing collects blobs. `savepoint remove` frees a savepoint's own namespace and
leaves the shared store alone, so removing every savepoint of a compose project frees no
image bytes. A gc is its own command and its own decision; `image.json` exists in
part so that command has something to mark from.
*/
package dedupcopy
