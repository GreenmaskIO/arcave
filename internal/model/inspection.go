package model

// Inspection is the resolved, arcave-relevant view of a compose project: the name
// docker compose knows the stack by, the persistent mounts arcave snapshots, and the
// mounts it had to skip. The compose client produces it; the service consumes it
// — it is data, so it lives here and neither layer needs to import the other.
//
// ComposeProject, not ProjectName: this is a Docker addressing name, used to
// locate containers. The project as a *storage* tenant is a different concept,
// and it is the Storager root a backend is bound to, never a field.
type Inspection struct {
	ComposeProject string
	Mounts         []Mount
	Skipped        []SkippedMount
}
