package cdc

import (
	"context"
	"fmt"
	"sync"

	"github.com/stretchr/testify/mock"

	"github.com/greenmaskio/arcave/internal/model"
	resticmodel "github.com/greenmaskio/arcave/internal/service/backend/restic/rest/model"
)

// Every mock forwards all of its seam's arguments to Called, ctx included;
// tests match ctx with mock.Anything unless they assert on it.

// mockRunner doubles ContainerRunner and records every spec it was handed: a
// OneShotSpec carries the stdio writers this package creates, so no literal can
// name one — the expectations match the argv, and the assertions read the
// recording.
type mockRunner struct {
	mock.Mock
	mu    sync.Mutex
	specs []model.OneShotSpec
}

func (m *mockRunner) RunOneShot(ctx context.Context, spec model.OneShotSpec) error {
	m.mu.Lock()
	m.specs = append(m.specs, spec)
	m.mu.Unlock()
	return m.Called(ctx, spec).Error(0)
}

func (m *mockRunner) ResolveMountSource(ctx context.Context, project, service, target string) (string, error) {
	args := m.Called(ctx, project, service, target)
	return args.String(0), args.Error(1)
}

func (m *mockRunner) WipeMount(ctx context.Context, project, service, target string) error {
	return m.Called(ctx, project, service, target).Error(0)
}

// Specs returns the runs in the order they happened.
func (m *mockRunner) Specs() []model.OneShotSpec {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]model.OneShotSpec(nil), m.specs...)
}

// mockPublisher doubles the registry both halves of the seam: a withdrawal is a
// method of its own now, so it is an expectation like any other rather than a
// counter.
type mockPublisher struct {
	mock.Mock
}

func (m *mockPublisher) Register(name string, repo resticmodel.Repository) error {
	return m.Called(name, repo).Error(0)
}

func (m *mockPublisher) Unregister(name string) {
	m.Called(name)
}

// matchRestic matches a one-shot run by the restic subcommand it invokes, which
// is the one stable thing about a spec carrying live stdio writers.
func matchRestic(verb string) any {
	return mock.MatchedBy(func(spec model.OneShotSpec) bool {
		return len(spec.Cmd) > 0 && spec.Cmd[0] == verb
	})
}

// respondWith writes lines to a run's stdout, standing in for what restic prints
// on --json.
func respondWith(lines ...string) func(mock.Arguments) {
	return func(args mock.Arguments) {
		spec := args.Get(1).(model.OneShotSpec)
		for _, line := range lines {
			fmt.Fprintln(spec.Stdout, line)
		}
	}
}
