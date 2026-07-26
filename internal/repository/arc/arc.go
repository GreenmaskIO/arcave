// Package arc reads the Arcave Recommender Config — the catalog of images whose
// persistent state arcave knows about, and the container paths that state lives
// at. `arcave recommend` matches a compose service's image against it.
//
// It is CRUD over one thing and owns no client, so it is a repository. Like
// repository/config it reads through os rather than a Storager: the catalog is
// user input naming what to look for, not data of the savepoint store — and the
// default half of it is not on disk at all.
package arc

import (
	"bytes"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"os"

	"gopkg.in/yaml.v3"

	"github.com/greenmaskio/arcave/internal/model"
)

// defaultCatalog is the catalog arcave ships with, compiled into the binary so
// `arcave recommend` works in a project that configured nothing. It is embedded
// rather than installed alongside the binary because a missing data file would
// turn the command into a no-op that still exits 0.
//
//go:embed default.yaml
var defaultCatalog []byte

// defaultSource names the embedded catalog in an error message, where a user
// file would name its path.
const defaultSource = "built-in catalog"

// entry is the catalog's on-disk shape, private to this package: the domain type
// crossing the seam is model.ARCEntry, so no consumer depends on the yaml schema.
type entry struct {
	Image   string   `yaml:"image"`
	Aliases []string `yaml:"aliases"`
	Volumes []string `yaml:"volumes"`
}

// Repository reads the recommendation catalog: the built-in one, with each
// configured user file merged over it in turn.
type Repository struct {
	// userFiles are the --recommender-file targets, in the order given; empty
	// when the caller supplied none.
	userFiles []string
}

// New returns a catalog reader. userFiles may be empty, in which case only the
// built-in catalog is read. Several are merged left to right, so a personal
// catalog can layer over a team one.
func NewARC(userFiles ...string) *Repository {
	return &Repository{userFiles: userFiles}
}

// List returns the merged catalog. Entries keep the built-in order, with images
// a user file introduces appended after them.
//
// It takes no context, unlike the savepoint repository's List: every half is
// local bytes, one of them already in the binary, so there is no call here that
// could be cancelled.
func (r *Repository) List() ([]model.ARCEntry, error) {
	merged, err := parseCatalog(defaultCatalog, defaultSource)
	if err != nil {
		// The embedded catalog is arcave's own, so this is a build-time mistake —
		// but it is still returned rather than panicked on: a corrupt catalog must
		// not take down a process that could have reported it.
		return nil, err
	}

	for _, file := range r.userFiles {
		data, err := os.ReadFile(file)
		if err != nil {
			return nil, fmt.Errorf("read recommender catalog %s: %w", file, err)
		}
		overlay, err := parseCatalog(data, file)
		if err != nil {
			return nil, err
		}
		merged = mergeCatalogs(merged, overlay)
	}
	return merged, nil
}

// parseCatalog decodes one catalog document. source names the file in errors, so
// a user who passed several --recommender-file catalogs learns which one is
// malformed.
//
// Unknown keys are rejected, as everywhere else arcave reads user-authored YAML:
// a mistyped `volume:` that parsed silently would produce an entry recommending
// nothing at all.
func parseCatalog(data []byte, source string) ([]model.ARCEntry, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)

	var entries []entry
	if err := dec.Decode(&entries); err != nil {
		// An empty document is an empty catalog, not a failure — a user file that
		// currently lists nothing is a legitimate state.
		if errors.Is(err, io.EOF) {
			return nil, nil
		}
		return nil, fmt.Errorf("parse recommender catalog %s: %w", source, err)
	}

	out := make([]model.ARCEntry, 0, len(entries))
	for i, e := range entries {
		if e.Image == "" {
			return nil, fmt.Errorf("parse recommender catalog %s: entry %d has no image", source, i)
		}
		out = append(out, model.ARCEntry{Image: e.Image, Aliases: e.Aliases, Volumes: e.Volumes})
	}
	return out, nil
}

// mergeCatalogs layers overlay over base, keyed on the exact image string. An
// image both halves name keeps its position and gains the overlay's aliases and
// volumes, deduplicated, base order first; an image only overlay names is
// appended. Nothing is ever removed: the catalog is additive, so a user file
// extends arcave's knowledge rather than replacing it.
func mergeCatalogs(base, overlay []model.ARCEntry) []model.ARCEntry {
	merged := make([]model.ARCEntry, len(base))
	copy(merged, base)

	index := make(map[string]int, len(merged))
	for i, e := range merged {
		index[e.Image] = i
	}

	for _, e := range overlay {
		i, ok := index[e.Image]
		if !ok {
			index[e.Image] = len(merged)
			merged = append(merged, e)
			continue
		}
		merged[i].Aliases = union(merged[i].Aliases, e.Aliases)
		merged[i].Volumes = union(merged[i].Volumes, e.Volumes)
	}
	return merged
}

// union appends the members of b that a does not already hold, preserving a's
// order and dropping duplicates within b itself.
func union(a, b []string) []string {
	if len(b) == 0 {
		return a
	}
	seen := make(map[string]struct{}, len(a)+len(b))
	out := make([]string, 0, len(a)+len(b))
	for _, vs := range [2][]string{a, b} {
		for _, v := range vs {
			if _, ok := seen[v]; ok {
				continue
			}
			seen[v] = struct{}{}
			out = append(out, v)
		}
	}
	return out
}
