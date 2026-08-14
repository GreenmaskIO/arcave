package api

import (
	"github.com/greenmaskio/arcave/internal/model"
)

// ProjectApplyRequest carries the whole `project` section of the config.
type ProjectApplyRequest struct {
	Project model.Project
}

func (r ProjectApplyRequest) Validate() error {
	if r.Project.Name == "" {
		return ErrProjectNameRequired
	}
	return nil
}

// ProjectApplyStatus is what applying the declaration did to the project.
type ProjectApplyStatus string

const (
	ProjectApplyStatusCreated   ProjectApplyStatus = "created"
	ProjectApplyStatusChanged   ProjectApplyStatus = "changed"
	ProjectApplyStatusNoChanges ProjectApplyStatus = "no_changes"
)

type ProjectApplyResponse struct {
	Name   string
	Status ProjectApplyStatus
}
