package model

// ARCEntry is one entry of the Arcave Recommender Config: the image whose
// persistent state arcave knows about, the images that store the same state in
// the same places, and the container paths that state lives at.
//
// Image and Aliases are normalized image repositories — no tag, no digest, and
// no `docker.io/library/` prefix (`postgres`, `org/pg`) — because that is the key
// a compose service's image is matched on. An alias shares the entry's Volumes
// verbatim, so an image only becomes an alias when its data directories are
// identical; a variant that relocates them (the bitnami images) gets an entry of
// its own.
//
// It carries no json/yaml tags: it is the result type crossing the repository
// seam, never the on-disk shape. The catalog file's schema is private to
// repository/arc.
type ARCEntry struct {
	Image   string
	Aliases []string
	Volumes []string
}
