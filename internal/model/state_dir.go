package model

// StateDir is the per-project directory arcave keeps all of its local state in,
// relative to the project root: the config file, the project lock, and the
// default savepoint store.
//
// It lives in model — the base every layer imports — so the config, lock, and
// safety-snapshot paths all render the same segment from one source. They were
// three independent literals once; renaming the tool then meant editing three
// files in lockstep, and missing one split the state silently across two
// directories. This is the single source; a future rename is a one-liner here.
const StateDir = ".arcave"
