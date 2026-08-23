package imagecopy

import (
	"github.com/greenmaskio/arcave/internal/model"
)

// manifestName is the one file in a capture arcave writes rather than copies.
const manifestName = "image.json"

// Format is the form a capture's bytes take in the store; a manifest names its own.
type Format string

const (
	FormatArchive Format = "archive"
	FormatBlobs   Format = "oci-blobs"
)

// Kind is what a member is: one whole archive, a blob shared by every capture
// naming the same digest, or metadata private to this capture.
type Kind string

const (
	KindArchive  Kind = "archive"
	KindBlob     Kind = "blob"
	KindMetadata Kind = "metadata"
)

// Manifest describes one stored image: what it reassembles into, the form it was
// stored in, and its members in the order they are to be read back.
type Manifest struct {
	ImageID model.ImageDigestID `json:"image_id"`
	Format  Format              `json:"format"`
	Members []Member            `json:"members"`
}

// Member is one part of a stored image, under the name it is stored by.
type Member struct {
	Name string `json:"name"`
	Kind Kind   `json:"kind"`
	// Digest is a blob's content digest and its key in the shared store; empty for
	// anything else.
	Digest string `json:"digest,omitempty"`
}

// StoredSize is the image's own size and the bytes of it the store did not already
// hold. The pair is what makes deduplication visible.
type StoredSize struct {
	Image   int64
	Written int64
}
