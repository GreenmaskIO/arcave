package api

import (
	"errors"

	"github.com/greenmaskio/arcave/internal/model"
)

// The sentinels a refused request returns.
var (
	ErrProjectNameRequired = errors.New("project name is required")
	ErrSessionIDRequired   = errors.New("session id is required")
	ErrProviderIDRequired  = errors.New("provider id is required")
	ErrStackIDRequired     = errors.New("stack id is required")
	ErrSavepointIDRequired = errors.New("savepoint id is required")

	// Aliases, not copies: errors.Is matches whichever side refused the request.
	ErrRemoveSelectorConflict = model.ErrRemoveSelectorConflict
	ErrRemoveNoSelector       = model.ErrRemoveNoSelector
	ErrRetainRecentNegative   = model.ErrRetainRecentNegative
)
