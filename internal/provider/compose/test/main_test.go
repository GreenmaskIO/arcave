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
	"os"
	"testing"

	"github.com/docker/cli/cli/command"
	"github.com/docker/compose/v5/cmd/display"
	"github.com/docker/compose/v5/pkg/api"
	dockercompose "github.com/docker/compose/v5/pkg/compose"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	composeclient "github.com/greenmaskio/arcave/internal/provider/compose/internal/client/compose"
	"github.com/greenmaskio/arcave/internal/utils/testutils/daemontest"
)

// TestMain fails the run when a test leaves anything of its own in the daemon.
func TestMain(m *testing.M) {
	os.Exit(daemontest.Run(m))
}

// newComposeService is compose over cli, its progress silent and its questions answered by the
// returned prompter, which a runner's Options carry: unanswered, compose asks on stdout.
func newComposeService(t *testing.T, cli command.Cli) (api.Compose, *composeclient.Prompter) {
	t.Helper()
	prompter := composeclient.NewPrompter(zerolog.Nop())
	service, err := dockercompose.NewComposeService(cli,
		dockercompose.WithEventProcessor(display.Quiet()),
		dockercompose.WithPrompt(prompter.Prompt),
	)
	require.NoError(t, err)
	return service, prompter
}
