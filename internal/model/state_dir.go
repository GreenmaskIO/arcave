package model

// StateDir is the per-project directory arcave keeps its state in, relative to
// the project root: the config file, the lock, the safety-backup scratch space,
// and — unless the user configures a store elsewhere — the savepoints.
//
// It lives in model because three layers that cannot import one another each
// need to name it: internal/config renders config.yaml and the default savepoint
// store under it, internal/lock puts the flock target under it, and cmd/arcave
// roots the safety-backup store there. Each used to carry its own ".gm" literal,
// so the rename to ".arcave" meant editing three files that no compiler or test
// tied together — miss one and the state silently splits across two directories,
// with a green build the whole way. One constant makes the next rename a
// one-liner.
const StateDir = ".arcave"
