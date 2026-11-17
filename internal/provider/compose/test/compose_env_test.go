// Copyright 2026 Greenmask Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package test

import (
	"context"
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/docker/cli/cli/command"
	"github.com/docker/cli/cli/flags"
	"github.com/greenmaskio/storages"
	"github.com/moby/moby/client"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	composeclient "github.com/greenmaskio/arcave/internal/client/compose"
	dockerclient "github.com/greenmaskio/arcave/internal/client/docker"
	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/provider/compose"
	composeconfig "github.com/greenmaskio/arcave/internal/provider/compose/config"
	"github.com/greenmaskio/arcave/internal/provider/compose/internal/service/composespec"
	"github.com/greenmaskio/arcave/internal/utils/testutils/daemontest"
)

// composeEnv is one compose project named after the test, with a compose client and an engine
// over the test daemon, logging into a buffer.
type composeEnv struct {
	ctx     context.Context
	compose *composeclient.Client
	engine  *dockerclient.Engine
	daemon  *client.Client
	logs    *syncBuffer
	project string
}

// newComposeEnv names the compose project arcave-<kind>-it-<suffix>, stops it within stopTimeout and
// removes it when the test ends.
func newComposeEnv(t *testing.T, kind string, stopTimeout time.Duration) *composeEnv {
	t.Helper()

	daemonAPI := daemontest.NewClient(t)
	project := "arcave-" + kind + "-it-" + daemontest.RandomSuffix(t)

	cli, err := command.NewDockerCli(
		command.WithInputStream(nil),
		command.WithOutputStream(io.Discard),
		command.WithErrorStream(io.Discard),
	)
	require.NoError(t, err)
	require.NoError(t, cli.Initialize(flags.NewClientOptions()))
	composeService, prompter := newComposeService(t, cli)
	logs := &syncBuffer{}
	env := &composeEnv{
		ctx: zerolog.New(logs).WithContext(context.Background()),
		compose: composeclient.NewClient(composeService, composeclient.Options{
			WaitTimeout: 90 * time.Second, StopTimeout: stopTimeout, Prompter: prompter,
		}),
		engine:  dockerclient.NewEngine(daemonAPI),
		daemon:  daemonAPI,
		logs:    logs,
		project: project,
	}
	t.Cleanup(func() { daemontest.RemoveComposeProject(t, env.daemon, env.project) })
	return env
}

// up creates and starts the compose project document describes, its name the first verb, then args.
func (e *composeEnv) up(t *testing.T, document string, args ...any) *types.Project {
	t.Helper()

	project, err := composespec.NewCodec().Parse(e.ctx, fmt.Appendf(nil, document, append([]any{e.project}, args...)...))
	require.NoError(t, err)
	require.NoError(t, e.compose.Create(e.ctx, project, nil))
	require.NoError(t, e.compose.Start(e.ctx, project, 90*time.Second))
	return project
}

// openComposeWorkload opens composeProject's workload over store the way the DI root does: the
// config's defaults, with the timeouts this suite waits on and compose's own progress muted.
func openComposeWorkload(t *testing.T, store storages.Storager, arcaveProject, composeProject string) compose.Workload {
	t.Helper()

	settings := composeconfig.Config{
		Compose: composeconfig.Compose{
			WaitTimeout: 90 * time.Second,
			StopTimeout: 5 * time.Second,
		},
	}
	settings.ApplyDefaults()
	workload, err := compose.Open(t.Context(), compose.Config{
		Settings:        settings,
		ArcaveProject:   model.ArcaveProjectName(arcaveProject),
		ComposeProgress: composeconfig.ProgressQuiet,
	}, store, model.DockerComposeProjectName(composeProject))
	require.NoError(t, err)
	t.Cleanup(func() { workload.Close(context.Background()) })
	return workload
}
