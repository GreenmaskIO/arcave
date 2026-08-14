package cli

import (
	"context"
	"errors"
	"fmt"

	"github.com/rs/zerolog/log"

	"github.com/greenmaskio/arcave/internal/config"
)

// ErrProjectInitialized refuses a project that already carries a config.
var ErrProjectInitialized = errors.New("project already initialized use --override")

// configRepository is .arcave/config.yaml as this handler needs it.
type configRepository interface {
	Exists() (bool, error)
	Save(cfg config.Config) error
	Path() string
}

// InitHandler writes the project's config.
type InitHandler struct {
	repo configRepository
}

func NewInitHandler(repo configRepository) *InitHandler {
	return &InitHandler{repo: repo}
}

// Init writes cfg to the project's config file, refusing an initialized project
// unless override says otherwise.
func (h *InitHandler) Init(ctx context.Context, cfg config.Config, override bool) error {
	exists, err := h.repo.Exists()
	if err != nil {
		return fmt.Errorf("check config: %w", err)
	}
	if exists && !override {
		return ErrProjectInitialized
	}
	if err := h.repo.Save(cfg); err != nil {
		return err
	}

	log.Ctx(ctx).Info().
		Str("name", cfg.Project.Name).
		Str("path", h.repo.Path()).
		Msg("initialized arcave project")
	return nil
}
