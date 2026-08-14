package cdc

import (
	"context"
	"fmt"
	"sync"

	"github.com/stretchr/testify/mock"

	dockermodel "github.com/greenmaskio/arcave/internal/provider/docker/internal/model"
)

// Every mock forwards all of its seam's arguments to Called, ctx included;
// tests match ctx with mock.Anything unless they assert on it.

// mockRunner doubles VolumeRunner and records every spec it was handed: a
// OneShotSpec carries the stdio writers this package creates, so no literal can
// name one — the expectations match the argv, and the assertions read the
// recording.
type mockRunner struct {
	mock.Mock
	mu    sync.Mutex
	specs []dockermodel.OneShotSpec
}

func (m *mockRunner) RunOneShot(ctx context.Context, spec dockermodel.OneShotSpec) error {
	m.mu.Lock()
	m.specs = append(m.specs, spec)
	m.mu.Unlock()
	return m.Called(ctx, spec).Error(0)
}

func (m *mockRunner) ResolveMountSource(ctx context.Context, v dockermodel.Volume) (dockermodel.MountSource, error) {
	args := m.Called(ctx, v)
	src, _ := args.Get(0).(dockermodel.MountSource)
	return src, args.Error(1)
}

func (m *mockRunner) WipeVolume(ctx context.Context, src dockermodel.MountSource) error {
	return m.Called(ctx, src).Error(0)
}

// Specs returns the runs in the order they happened.
func (m *mockRunner) Specs() []dockermodel.OneShotSpec {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]dockermodel.OneShotSpec(nil), m.specs...)
}

// matchRestic matches a one-shot run by the restic subcommand it invokes, which
// is the one stable thing about a spec carrying live stdio writers.
func matchRestic(verb string) any {
	return mock.MatchedBy(func(spec dockermodel.OneShotSpec) bool {
		return len(spec.Cmd) > 0 && spec.Cmd[0] == verb
	})
}

// respondWith writes lines to a run's stdout, standing in for what restic prints
// on --json.
func respondWith(lines ...string) func(mock.Arguments) {
	return func(args mock.Arguments) {
		spec := args.Get(1).(dockermodel.OneShotSpec)
		for _, line := range lines {
			fmt.Fprintln(spec.Stdout, line)
		}
	}
}
