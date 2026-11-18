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

package composetest

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	composemodel "github.com/greenmaskio/arcave/internal/provider/compose/internal/model"
	"github.com/greenmaskio/arcave/internal/utils/testutils/daemontest"
)

// oneShotRunner runs one helper container to completion.
type oneShotRunner interface {
	RunOneShot(ctx context.Context, spec composemodel.OneShotSpec) error
}

// Busybox runs script in a busybox container with volume mounted at /data and returns its stdout.
func Busybox(t *testing.T, runner oneShotRunner, volume, script string) string {
	t.Helper()

	var stdout, stderr bytes.Buffer
	err := runner.RunOneShot(context.Background(), composemodel.OneShotSpec{
		Image:  daemontest.BusyboxImage,
		Cmd:    []string{"sh", "-c", script},
		Binds:  []string{volume + ":/data"},
		Stdout: &stdout,
		Stderr: &stderr,
	})
	require.NoErrorf(t, err, "busybox %q failed:\nstdout:\n%s\nstderr:\n%s", script, stdout.String(), stderr.String())
	return stdout.String()
}
