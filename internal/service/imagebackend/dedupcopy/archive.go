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

package dedupcopy

import (
	"crypto/sha256"
	"strings"
)

// The vocabulary of a save archive as Docker 25 and newer export it: blobs under
// one content-addressed prefix, and metadata members naming what they make up.
const (
	blobPrefix    = "blobs/sha256/"
	ociLayoutName = "oci-layout"
	indexName     = "index.json"
	manifestName  = "manifest.json"
)

// metadataNames are the archive members that are not blobs; they are stored beside
// the savepoint rather than shared.
var metadataNames = map[string]bool{
	indexName:     true,
	manifestName:  true,
	ociLayoutName: true,
}

// requiredMetadata is what an archive must carry to be an OCI one at all. Docker
// writes it last and a tar cut at a member boundary reads as a clean EOF, so its
// absence is how a truncated save stream is caught before the capture is committed.
// A pre-Docker-25 layout is refused earlier, by the member names themselves.
var requiredMetadata = []string{ociLayoutName, indexName}

// sha256HexLen is how long a sha256 digest is once written in hex.
const sha256HexLen = sha256.Size * 2

// validDigest reports whether s may become a blob's storage key. It gates both
// directions — a name arriving from the archive, and a digest read back out of a
// manifest, which is a file on disk and so is untrusted input too.
func validDigest(s string) bool {
	if len(s) != sha256HexLen {
		return false
	}
	for i := range len(s) {
		if c := s[i]; (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// parseBlobName reports whether a member is a blob and the digest its name carries.
func parseBlobName(name string) (digest string, isBlob bool) {
	rest, ok := strings.CutPrefix(name, blobPrefix)
	if !ok || !validDigest(rest) {
		return "", false
	}
	return rest, true
}

func isMetadataName(name string) bool {
	return metadataNames[name]
}
