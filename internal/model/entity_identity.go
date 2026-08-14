package model

import (
	"fmt"
	"strings"
)

type ObjectIdentity struct {
	Kind string `json:"kind"`
	// Ordered logical scope keys.
	NameParts []string `json:"name_parts"`
	// Scope values by logical key.
	NameValues map[string]string `json:"name_values,omitempty"`
}

func (i ObjectIdentity) Name() (string, error) {
	parts := make([]string, 0, len(i.NameParts))

	for _, key := range i.NameParts {
		value, ok := i.NameValues[key]
		if !ok {
			return "", fmt.Errorf("missing name value for key %q", key)
		}
		if value == "" {
			return "", fmt.Errorf("empty name value for key %q", key)
		}

		parts = append(parts, value)
	}

	if len(parts) == 0 {
		return "", fmt.Errorf("object identity has no name parts")
	}

	return strings.Join(parts, "."), nil
}
