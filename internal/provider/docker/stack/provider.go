// Package stack is the docker-compose stack provider: the stack family on the
// docker runtime, ProviderKind compose.
package stack

import (
	"context"
	"fmt"
	"io"

	"github.com/docker/cli/cli/command"
	"github.com/docker/compose/v2/pkg/api"
	dockercompose "github.com/docker/compose/v2/pkg/compose"
	"github.com/docker/docker/client"
	"github.com/rs/zerolog/log"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/provider"
	"github.com/greenmaskio/arcave/internal/provider/docker/internal/client/docker"
)

var _ provider.StackProvider = (*Provider)(nil)

// Provider is one configured docker-compose runtime: a daemon connection, the
// compose service, and its settings. It holds no stack — a call names one.
type Provider struct {
	settings       Settings
	progressWriter io.WriteCloser
	dockerCli      *command.DockerCli
	dockerClient   client.APIClient
	composeService api.Compose
	engine         *docker.Engine
}

// New wires a compose provider from its own settings, which arrive as the blob
// the control plane carried. Everything that can fail on configuration alone —
// an unknown key included — fails here.
func New(ctx context.Context, raw model.RawSettings) (*Provider, error) {
	var settings Settings
	if err := raw.Decode(&settings); err != nil {
		return nil, fmt.Errorf("decode compose provider settings: %w", err)
	}
	settings.ApplyDefaults()
	if err := settings.Validate(); err != nil {
		return nil, fmt.Errorf("validate compose provider settings: %w", err)
	}

	var opened partial
	defer func() { opened.releaseOnFailure(ctx) }()

	progressWriter := composeProgressWriter(settings.Compose.Progress, *log.Ctx(ctx))
	opened.add("compose progress writer", progressWriter)

	// One docker/cli instance, so both halves of client/docker share a daemon.
	dockerCli, dockerClientAPI, err := initDockerClient(settings.Connection, progressWriter)
	if err != nil {
		return nil, fmt.Errorf("initialise docker client: %w", err)
	}
	engine := docker.NewEngine(dockerClientAPI)
	opened.add("docker engine", engine)

	opened.keep()
	return &Provider{
		settings:       settings,
		progressWriter: progressWriter,
		dockerCli:      dockerCli,
		dockerClient:   dockerClientAPI,
		composeService: dockercompose.NewComposeService(dockerCli),
		engine:         engine,
	}, nil
}

// Kind is what a record and a store layout name this provider by.
func (p *Provider) Kind() model.ProviderKind { return model.ProviderCompose }

// Capture is not implemented; the engine it will be built from is in legacy.go.
func (p *Provider) Capture(context.Context, provider.CaptureRequest) (provider.CaptureResult, error) {
	return provider.CaptureResult{}, fmt.Errorf("compose provider: capture is not implemented")
}

// Restore is not implemented — see Capture.
func (p *Provider) Restore(context.Context, provider.RestoreRequest) error {
	return fmt.Errorf("compose provider: restore is not implemented")
}

// Discard is not implemented — see Capture.
func (p *Provider) Discard(context.Context, provider.DiscardRequest) error {
	return fmt.Errorf("compose provider: discard is not implemented")
}

// Close releases the engine, then the progress writer, so a line compose left
// unterminated is still emitted.
func (p *Provider) Close(ctx context.Context) error {
	closeQuietly(ctx, "docker engine", p.engine)
	closeQuietly(ctx, "compose progress writer", p.progressWriter)
	return nil
}

// partial tracks what a half-finished wiring has opened; keep disarms the unwind.
type partial struct {
	closers []namedCloser
	kept    bool
}

type namedCloser struct {
	name string
	c    io.Closer
}

func (p *partial) add(name string, c io.Closer) { p.closers = append(p.closers, namedCloser{name, c}) }

func (p *partial) keep() { p.kept = true }

func (p *partial) releaseOnFailure(ctx context.Context) {
	if p.kept {
		return
	}
	for i := len(p.closers) - 1; i >= 0; i-- {
		closeQuietly(ctx, p.closers[i].name, p.closers[i].c)
	}
}

// closeQuietly closes c and logs any error without failing the caller.
func closeQuietly(ctx context.Context, name string, c io.Closer) {
	if err := c.Close(); err != nil {
		log.Ctx(ctx).Error().Err(err).Msg("close " + name)
	}
}
