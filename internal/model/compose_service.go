package model

// ComposeService is one service of a compose project, as declared by the files —
// what it runs and where it already mounts something persistent. The compose
// inspector produces it; the recommend service consumes it, matching ImageRepo
// against the ARC catalog and treating MountTargets as the paths already covered.
//
// Image is verbatim from the compose file (tag, digest, registry and all) and is
// what the user sees and what a recommendation hashes over. ImageRepo is the
// normalized repository that same value resolves to — the match key — so the
// image-reference parsing stays inside the inspector and never leaks into the
// service.
//
// MountTargets holds container paths only, never sources: whether a path is
// covered by a named volume, a bind or an anonymous volume makes no difference to
// the advice, and a tmpfs mount is not in the list at all — it persists nothing.
type ComposeService struct {
	Name         string
	Image        string
	ImageRepo    string
	MountTargets []string
}
