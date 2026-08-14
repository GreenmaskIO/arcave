package compose

import (
	"context"
	"fmt"

	"github.com/compose-spec/compose-go/v2/cli"
	"github.com/compose-spec/compose-go/v2/types"
)

type Loader struct{}

func NewLoader() *Loader {
	return &Loader{}
}

func (*Loader) Load(ctx context.Context, files ...string) (*types.Project, error) {
	opts, err := cli.NewProjectOptions(
		files,
		cli.WithOsEnv,
		cli.WithDotEnv,
		cli.WithConfigFileEnv, // COMPOSE_FILE
		cli.WithDefaultConfigPath,
	)
	if err != nil {
		return nil, fmt.Errorf("build compose project options: %w", err)
	}

	project, err := opts.LoadProject(ctx)
	if err != nil {
		return nil, fmt.Errorf("load compose project: %w", err)
	}

	if len(project.Services) == 0 {
		return nil, fmt.Errorf("compose project %q defines no services", project.Name)
	}

	return project, nil
}
