package model

import "errors"

// ErrProjectLocked is returned when another arcave process is already running a
// mutating workflow in this project. It crosses a seam inside the provider — the
// lock service returns it and a workflow matches it with errors.Is — so it lives
// with the other types they share.
var ErrProjectLocked = errors.New("another arcave command is already running in this project")
