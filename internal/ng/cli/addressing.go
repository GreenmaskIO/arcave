// Package cli adapts arcave's commands to the control plane: a handler resolves the
// addressing, builds the request and renders the response through an injected printer.
package cli

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/google/uuid"

	"github.com/greenmaskio/arcave/internal/config"
)

// EnvSessionID is the environment variable naming the session.
const EnvSessionID = "ARCAVE_SESSION_ID"

// Addressing carries the four root addressing flags as typed.
type Addressing struct {
	Project   string
	SessionID string
	Provider  string
	Stack     string
}

// scope is the resolved addressing a request is built from.
type scope struct {
	ProjectName string
	SessionID   uuid.UUID
	ProviderID  string
	StackID     string
}

// resolveScope resolves the whole chain, session included.
func resolveScope(cfg config.Config, a Addressing) (scope, error) {
	s, err := resolveStackScope(cfg, a)
	if err != nil {
		return scope{}, err
	}
	s.SessionID, err = resolveSessionID(a.SessionID)
	if err != nil {
		return scope{}, err
	}
	return s, nil
}

// resolveStackScope resolves the chain without the session.
func resolveStackScope(cfg config.Config, a Addressing) (scope, error) {
	projectName, err := resolveProjectName(a.Project, cfg.Project.Name)
	if err != nil {
		return scope{}, err
	}
	providerID, err := resolveProviderID(a.Provider, cfg.Project.Providers.Stacks)
	if err != nil {
		return scope{}, err
	}
	stackID, err := resolveStackID(a.Stack)
	if err != nil {
		return scope{}, err
	}
	return scope{ProjectName: projectName, ProviderID: providerID, StackID: stackID}, nil
}

// resolveProjectName takes the flag over the config's project.name.
func resolveProjectName(flagValue, configured string) (string, error) {
	if flagValue != "" {
		return flagValue, nil
	}
	if configured == "" {
		return "", errors.New("no project: pass --project or set project.name in the config")
	}
	return configured, nil
}

// resolveSessionID takes the flag over ARCAVE_SESSION_ID.
func resolveSessionID(flagValue string) (uuid.UUID, error) {
	raw, source := flagValue, "--session-id"
	if raw == "" {
		raw, source = os.Getenv(EnvSessionID), EnvSessionID
	}
	if raw == "" {
		return uuid.Nil, fmt.Errorf(
			"no session: pass --session-id or set %s (run 'arcave session create' to open one)", EnvSessionID)
	}
	id, err := uuid.Parse(raw)
	if err != nil {
		return uuid.Nil, fmt.Errorf("parse session id from %s: %w", source, err)
	}
	return id, nil
}

// resolveProviderID takes the flag, then the sole declared provider.
func resolveProviderID(flagValue string, declared []string) (string, error) {
	if flagValue != "" {
		return flagValue, nil
	}
	switch len(declared) {
	case 0:
		return "", errors.New("no provider: pass --provider or declare one under project.providers.stacks")
	case 1:
		return declared[0], nil
	default:
		return "", fmt.Errorf("several providers declared (%s): pass --provider to pick one",
			strings.Join(declared, ", "))
	}
}

// resolveStackID requires the flag: the config does not name a stack.
func resolveStackID(flagValue string) (string, error) {
	if flagValue == "" {
		return "", errors.New("no stack: pass --stack with the name of the spec to address")
	}
	return flagValue, nil
}
