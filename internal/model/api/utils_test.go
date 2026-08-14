package api

import (
	"github.com/google/uuid"
)

// The ids every operation's table addresses its fixtures by.
var (
	testSessionID   = uuid.MustParse("6f8f2f0e-1a5c-4a2e-9d4b-0f1f2a3b4c5d")
	testSavepointID = uuid.MustParse("11111111-2222-3333-4444-555555555555")
)
