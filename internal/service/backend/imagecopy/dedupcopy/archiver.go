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
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/greenmaskio/storages"
	"github.com/rs/zerolog"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/service/backend/imagecopy"
)

// NamespaceBlobs is the blob store's key under the compose project store, beside the savepoints;
// a savepoint id is a canonical UUID, so it cannot collide with one.
const NamespaceBlobs = "shared/oci/blobs"

// MinDaemonMajor is the first Docker whose `docker save` writes the OCI layout this
// archiver splits by digest.
const MinDaemonMajor = 25

// Archiver splits an archive between the blob store its captures share and each
// capture's own metadata.
type Archiver struct {
	blobs *blobStore
}

// New binds the archiver to the blob store every capture under it shares.
func New(blobStore storages.Storager) *Archiver {
	return &Archiver{blobs: newBlobStore(blobStore)}
}

// Store walks the archive once, writing every blob under the digest its member
// name carries and the metadata beside the savepoint. Nothing is decompressed: the
// member name is the key, and that is what makes one pass enough. The OCI layout is
// therefore the gate — see requiredMetadata.
func (s *Archiver) Store(
	ctx context.Context, dst storages.Writer, imageID model.ImageDigestID, archive io.Reader,
) (imagecopy.StoredSize, error) {
	var size imagecopy.StoredSize
	manifest := imagecopy.Manifest{ImageID: imageID, Format: imagecopy.FormatBlobs}
	seen := map[string]bool{}

	reader := tar.NewReader(archive)
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return imagecopy.StoredSize{}, fmt.Errorf("read tar member: %w", err)
		}
		// An OCI export carries its blob directories as members of their own.
		if header.Typeflag == tar.TypeDir {
			continue
		}

		member, written, err := s.storeMember(ctx, dst, reader, header.Name, header.Size)
		if err != nil {
			return imagecopy.StoredSize{}, err
		}
		if member.Kind == imagecopy.KindMetadata {
			seen[member.Name] = true
		}
		size.Image += header.Size
		size.Written += written
		manifest.Members = append(manifest.Members, member)
	}

	for _, required := range requiredMetadata {
		if !seen[required] {
			return imagecopy.StoredSize{}, fmt.Errorf("%w: the archive carries no %s",
				model.ErrUnsupportedImageArchive, required)
		}
	}
	if err := imagecopy.WriteManifest(ctx, dst, manifest); err != nil {
		return imagecopy.StoredSize{}, err
	}
	return size, nil
}

// storeMember puts one archive member where its kind belongs and reports how many
// bytes that added. An unaddressable name must never become a storage key.
func (s *Archiver) storeMember(
	ctx context.Context, dst storages.Writer, body io.Reader, name string, size int64,
) (imagecopy.Member, int64, error) {
	if digest, isBlob := parseBlobName(name); isBlob {
		written, err := s.blobs.put(ctx, digest, body, size)
		if err != nil {
			return imagecopy.Member{}, 0, err
		}
		return imagecopy.Member{Name: name, Kind: imagecopy.KindBlob, Digest: digest}, written, nil
	}
	if isMetadataName(name) {
		if err := dst.PutObject(ctx, name, body); err != nil {
			return imagecopy.Member{}, 0, fmt.Errorf("write metadata %s: %w", name, err)
		}
		return imagecopy.Member{Name: name, Kind: imagecopy.KindMetadata}, size, nil
	}
	return imagecopy.Member{}, 0, fmt.Errorf(
		"%w: member %q is neither a blob nor metadata; a daemon older than Docker 25 exports this shape",
		model.ErrUnsupportedImageArchive, name)
}

// Load renders the archive back out of the blob store and the capture's metadata.
// Members go back under the names and in the order the capture recorded, their bytes
// verbatim: an image's id is the digest of those, never of the tar headers, which are
// written anew and which a load ignores.
func (s *Archiver) Load(
	ctx context.Context, src storages.Storager, imageID model.ImageDigestID, archive io.Writer,
) error {
	manifest, err := imagecopy.ReadManifest(ctx, src, imagecopy.FormatBlobs)
	if err != nil {
		return err
	}
	if manifest.ImageID != imageID {
		return fmt.Errorf("%w: the capture holds image %s, the record names %s",
			model.ErrSavepointImageMissing, manifest.ImageID, imageID)
	}

	tarWriter := tar.NewWriter(archive)
	for _, member := range manifest.Members {
		if err := s.writeMember(ctx, tarWriter, src, member); err != nil {
			return err
		}
	}
	return tarWriter.Close()
}

// writeMember copies one stored member into the archive under its original name.
func (s *Archiver) writeMember(
	ctx context.Context, tarWriter *tar.Writer, src storages.Storager, member imagecopy.Member,
) error {
	body, size, err := s.openMember(ctx, src, member)
	if err != nil {
		return err
	}
	defer func() {
		if err := body.Close(); err != nil {
			zerolog.Ctx(ctx).Error().Err(err).Str("member", member.Name).Msg("close member reader")
		}
	}()

	header := &tar.Header{Name: member.Name, Mode: 0o644, Size: size, Typeflag: tar.TypeReg}
	if err := tarWriter.WriteHeader(header); err != nil {
		return fmt.Errorf("write tar header for %s: %w", member.Name, err)
	}
	if _, err := io.Copy(tarWriter, body); err != nil {
		return fmt.Errorf("copy member %s into the archive: %w", member.Name, err)
	}
	return nil
}

// openMember resolves one member to the store that holds it.
func (s *Archiver) openMember(
	ctx context.Context, src storages.Storager, member imagecopy.Member,
) (io.ReadCloser, int64, error) {
	switch member.Kind {
	case imagecopy.KindBlob:
		if !validDigest(member.Digest) {
			return nil, 0, fmt.Errorf("%w: member %q names digest %q",
				model.ErrUnsupportedImageArchive, member.Name, member.Digest)
		}
		body, size, err := s.blobs.open(ctx, member.Digest)
		if err != nil {
			return nil, 0, fmt.Errorf("%w: %w", model.ErrSavepointImageMissing, err)
		}
		return body, size, nil
	case imagecopy.KindMetadata:
		if !isMetadataName(member.Name) {
			return nil, 0, fmt.Errorf("%w: %q is not a metadata member",
				model.ErrUnsupportedImageArchive, member.Name)
		}
		body, size, err := openSized(ctx, src, member.Name)
		if err != nil {
			return nil, 0, fmt.Errorf("%w: %w", model.ErrSavepointImageMissing, err)
		}
		return body, size, nil
	default:
		return nil, 0, fmt.Errorf("%w: member %q is of unknown kind %q",
			model.ErrUnsupportedImageArchive, member.Name, member.Kind)
	}
}

// openSized opens an object and reports its recorded size. A tar header carries the
// size and is written ahead of the body, while GetObject reports no length — so the
// size is asked of the store, never learned by buffering a layer to measure it.
func openSized(ctx context.Context, store storages.Storager, key string) (io.ReadCloser, int64, error) {
	stat, err := store.Stat(key)
	if err != nil {
		return nil, 0, fmt.Errorf("stat %s: %w", key, err)
	}
	if stat == nil || !stat.Exist {
		return nil, 0, fmt.Errorf("%s is not in the store", key)
	}
	body, err := store.GetObject(ctx, key)
	if err != nil {
		return nil, 0, fmt.Errorf("read %s: %w", key, err)
	}
	return body, stat.Size, nil
}
