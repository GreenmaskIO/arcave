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

//go:build fidelity

package test

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/compose-spec/compose-go/v2/cli"
	"github.com/compose-spec/compose-go/v2/types"
	cerrdefs "github.com/containerd/errdefs"
	"github.com/docker/cli/cli/command"
	"github.com/docker/cli/cli/flags"
	"github.com/docker/compose/v2/pkg/api"
	dockercompose "github.com/docker/compose/v2/pkg/compose"
	"github.com/docker/compose/v2/pkg/progress"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/volume"
	"github.com/docker/docker/client"
	gocmp "github.com/google/go-cmp/cmp"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	composeclient "github.com/greenmaskio/arcave/internal/client/compose"
	dockerclient "github.com/greenmaskio/arcave/internal/client/docker"
	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/service/composespec"
	"github.com/greenmaskio/arcave/internal/service/introspect"
)

// The images every case runs on, pinned so their environment and command hold still under the test.
const (
	fidelityBusybox = "busybox:1.37@sha256:bdf57e528e45e4433820e045b29b4597825a1c9e38353532d90a01445013f82e"
	fidelityAlpine  = "alpine:3.22@sha256:5291449c3df73caf6ed85e649dec1b9e818b39a5d8c871e97afc13e9cd5e8fa8"
	fidelityRedis   = "redis:7-alpine@sha256:858f009f9709ce576febc734aa78b8f6d624b82571f9ddb6bda4377c833b3499"
)

// fidelityCase is one compose capability: the file a user wrote, and the spec arcave must
// assemble from the compose project that file created.
type fidelityCase struct {
	name string
	// compose is compose.yaml; files are written beside it, and composeFiles name the files
	// loaded when compose.yaml alone is not.
	compose      string
	files        map[string]string
	composeFiles []string
	want         string
	// warns are the warnings Assemble logs, one JSON record each.
	warns []string
	// setup runs before the compose project is created; afterUp once it runs.
	setup   func(t *testing.T, e *fidelityEnv)
	afterUp func(t *testing.T, e *fidelityEnv)
	// drift applies to R1 what pass 2 is expected to change, ahead of the comparison with R2.
	drift func(r *model.IntrospectionResult)
	// noWait starts the compose project without waiting for it to be healthy.
	noWait bool
	// knownDefect names the BACKLOG.md row a pass is known to fail on; the pass then asserts
	// the failure, so the case turns red once the defect is fixed.
	knownDefect map[string]string
}

func TestIntrospectAssemble(t *testing.T) {
	groups := []struct {
		name  string
		cases []fidelityCase
	}{
		{name: "replicas", cases: replicaCases()},
		{name: "namespace", cases: namespaceCases()},
		{name: "networks", cases: networkCases()},
		{name: "ports", cases: portCases()},
		{name: "runtime", cases: runtimeCases()},
		{name: "volumes", cases: volumeCases()},
		{name: "dependencies", cases: dependencyCases()},
		{name: "build", cases: buildCases()},
		{name: "provenance", cases: provenanceCases()},
	}
	for _, group := range groups {
		t.Run(group.name, func(t *testing.T) {
			for _, tc := range group.cases {
				t.Run(tc.name, func(t *testing.T) { runFidelityCase(t, tc) })
			}
		})
	}
}

// runFidelityCase brings the case's compose project up from its file, assembles a spec from
// the running containers, compares it with want, then brings it up again from that spec.
func runFidelityCase(t *testing.T, tc fidelityCase) {
	e := newFidelityEnv(t)
	e.writeFiles(t, tc)
	if tc.setup != nil {
		tc.setup(t, e)
	}
	source := e.load(t, tc)
	e.build(t, source)
	e.up(t, tc, source)
	if tc.afterUp != nil {
		tc.afterUp(t, e)
	}

	r1 := e.introspect(t)
	specA, warns := e.assemble(t, r1)

	t.Run("pass1", func(t *testing.T) {
		defer func() {
			if t.Failed() {
				t.Logf("spec A:\n%s", e.marshal(t, specA))
			}
		}()
		// The project is named per run; a want names it only through the placeholder.
		want, err := e.codec.Parse(e.ctx, []byte(e.render("name: {{project}}\n"+tc.want)))
		require.NoError(t, err, "want must parse")
		delete(want.Extensions, "x-base")
		if want.Extensions[originExtension] == nil {
			assert.Equal(t, source.WorkingDir, specA.WorkingDir, "working_dir of the origin")
			assert.Equal(t, source.ComposeFiles, specA.ComposeFiles, "compose_files of the origin")
			want.WorkingDir, want.ComposeFiles = specA.WorkingDir, specA.ComposeFiles
			want.Extensions = specA.Extensions
		}

		diff := gocmp.Diff(want, specA, specOptions())

		e.expect(t, tc, "pass1", diff, "spec A against want (-want +A)")
		assert.Equal(t, normalizeWarnings(t, tc.warns), warns, "warnings")
	})

	t.Run("pass2", func(t *testing.T) {
		e.down(t, source)
		// compose writes into the project it creates, so it gets a copy of A.
		e.up(t, tc, e.reparse(t, specA))
		r2 := e.introspect(t)
		specB, _ := e.assemble(t, r2)
		if tc.drift != nil {
			tc.drift(&r1)
		}

		specDiff := gocmp.Diff(specA, specB, specOptions())
		resDiff := gocmp.Diff(r1, r2, introspectionOptions(declaredMACs(source)))

		e.expect(t, tc, "pass2", specDiff+resDiff, "spec A against spec B, then R1 against R2 (-1 +2)")
	})
}

// fidelityEnv is one compose project, driven the way cmd/arcave drives it.
type fidelityEnv struct {
	ctx       context.Context
	logs      *syncBuffer
	daemon    *client.Client
	compose   api.Compose
	runner    *composeclient.Client
	inspector *introspect.Inspector
	assembler *composespec.Assembler
	codec     *composespec.Codec
	project   string
	dir       string
	// volumes are every volume an introspection saw: a Down leaves the anonymous ones behind.
	volumes map[string]bool
}

// originExtension is where a spec carries the directory and files it was loaded from.
const originExtension = "x-arcave-origin"

func newFidelityEnv(t *testing.T) *fidelityEnv {
	t.Helper()

	daemon := newDaemonClient(t)
	project := "arcave-fidelity-" + randomSuffix(t)

	dockerCLI, err := command.NewDockerCli(
		command.WithInputStream(nil),
		command.WithOutputStream(io.Discard),
		command.WithErrorStream(io.Discard),
	)
	require.NoError(t, err)
	require.NoError(t, dockerCLI.Initialize(flags.NewClientOptions()))
	composeService := dockercompose.NewComposeService(dockerCLI)
	progress.Mode = progress.ModeQuiet

	logs := &syncBuffer{}
	e := &fidelityEnv{
		ctx:     zerolog.New(logs).WithContext(context.Background()),
		logs:    logs,
		daemon:  daemon,
		compose: composeService,
		runner: composeclient.NewClient(composeService, composeclient.Options{
			WaitTimeout: 90 * time.Second, StopTimeout: 2 * time.Second,
		}),
		inspector: introspect.NewInspector(daemon, model.DockerComposeProjectName(project)),
		assembler: composespec.NewAssembler(),
		codec:     composespec.NewCodec(),
		project:   project,
		dir:       t.TempDir(),
		volumes:   map[string]bool{},
	}
	t.Cleanup(func() { e.cleanup(t) })
	return e
}

// cleanup removes the compose project with every network and volume it ever had.
func (e *fidelityEnv) cleanup(t *testing.T) {
	ctx := context.Background()
	assert.NoError(t, e.compose.Down(ctx, e.project, api.DownOptions{RemoveOrphans: true, Volumes: true}))
	removeProjectNetworks(t, e.daemon, e.project)
	volumes, err := e.daemon.VolumeList(ctx, volume.ListOptions{
		Filters: filters.NewArgs(filters.Arg("label", api.ProjectLabel+"="+e.project)),
	})
	if assert.NoError(t, err) {
		for _, v := range volumes.Volumes {
			e.volumes[v.Name] = true
		}
	}
	for name := range e.volumes {
		if err := e.daemon.VolumeRemove(ctx, name, true); err != nil && !cerrdefs.IsNotFound(err) {
			assert.NoError(t, err, "remove volume %s", name)
		}
	}
}

// render fills the placeholders a case's documents share with the harness.
func (e *fidelityEnv) render(document string) string {
	return strings.NewReplacer(
		"{{project}}", e.project,
		"{{dir}}", e.dir,
		"{{busybox}}", fidelityBusybox,
		"{{alpine}}", fidelityAlpine,
		"{{redis}}", fidelityRedis,
	).Replace(document)
}

func (e *fidelityEnv) writeFiles(t *testing.T, tc fidelityCase) {
	t.Helper()

	files := map[string]string{"compose.yaml": tc.compose}
	for name, content := range tc.files {
		files[name] = content
	}
	for name, content := range files {
		path := filepath.Join(e.dir, name)
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(e.render(content)), 0o644))
	}
}

// load reads the case's files the way `docker compose config` does.
func (e *fidelityEnv) load(t *testing.T, tc fidelityCase) *types.Project {
	t.Helper()

	names := tc.composeFiles
	if len(names) == 0 {
		names = []string{"compose.yaml"}
	}
	paths := make([]string, 0, len(names))
	for _, name := range names {
		paths = append(paths, filepath.Join(e.dir, name))
	}
	options, err := cli.NewProjectOptions(paths,
		cli.WithWorkingDirectory(e.dir),
		cli.WithOsEnv,
		cli.WithDotEnv,
		cli.WithName(e.project),
	)
	require.NoError(t, err)
	project, err := options.LoadProject(e.ctx)
	require.NoError(t, err, "load the case's compose file")
	return project
}

// build builds the images the compose project builds, the way `docker compose up` does first.
func (e *fidelityEnv) build(t *testing.T, project *types.Project) {
	t.Helper()

	for _, svc := range project.Services {
		if svc.Build != nil {
			require.NoError(t, e.compose.Build(e.ctx, project, api.BuildOptions{Quiet: true}), "build")
			return
		}
	}
}

func (e *fidelityEnv) up(t *testing.T, tc fidelityCase, project *types.Project) {
	t.Helper()

	require.NoError(t, e.runner.Create(e.ctx, project), "create")
	if tc.noWait {
		require.NoError(t, e.compose.Start(e.ctx, project.Name, api.StartOptions{Project: project}), "start")
		return
	}
	require.NoError(t, e.runner.Start(e.ctx, project, 90*time.Second), "start")
}

// down takes the compose project down with its networks; volumes stay, as a rollback keeps them.
func (e *fidelityEnv) down(t *testing.T, project *types.Project) {
	t.Helper()

	require.NoError(t, e.runner.Down(e.ctx, project), "down")
	removeProjectNetworks(t, e.daemon, e.project)
}

func (e *fidelityEnv) introspect(t *testing.T) model.IntrospectionResult {
	t.Helper()

	res, err := e.inspector.Introspect(e.ctx)
	require.NoError(t, err, "introspect")
	for name := range res.Volumes {
		e.volumes[name] = true
	}
	return res
}

// assemble builds the spec and carries it through its artifact, returning it with the
// warnings Assemble logged.
func (e *fidelityEnv) assemble(t *testing.T, res model.IntrospectionResult) (*types.Project, []string) {
	t.Helper()

	before := len(e.logs.String())
	assembled, err := e.assembler.Assemble(e.ctx, res)
	require.NoError(t, err, "assemble")
	warns := warnings(t, e.logs.String()[before:])

	return e.reparse(t, assembled), warns
}

// reparse is a copy of the spec, carried through its artifact once more.
func (e *fidelityEnv) reparse(t *testing.T, project *types.Project) *types.Project {
	t.Helper()

	parsed, err := e.codec.Parse(e.ctx, e.marshal(t, project))
	require.NoError(t, err, "parse the rendered spec")
	return parsed
}

func (e *fidelityEnv) marshal(t *testing.T, project *types.Project) []byte {
	t.Helper()

	document, err := e.codec.Marshal(project)
	require.NoError(t, err, "marshal")
	return document
}

// expect asserts an empty diff, or a non-empty one for a pass the case knows to be defective.
func (e *fidelityEnv) expect(t *testing.T, tc fidelityCase, pass, diff, what string) {
	t.Helper()

	if row, ok := tc.knownDefect[pass]; ok {
		assert.NotEmpty(t, diff, "%s: the known defect (BACKLOG.md: %s) no longer shows; flip the case", what, row)
		t.Logf("known defect, BACKLOG.md: %s\n%s", row, diff)
		return
	}
	assert.Empty(t, diff, what)
}

// warnings are the warn records of a JSON log, normalized.
func warnings(t *testing.T, log string) []string {
	t.Helper()

	var warns []string
	scanner := bufio.NewScanner(strings.NewReader(log))
	for scanner.Scan() {
		var record map[string]any
		require.NoError(t, json.Unmarshal(scanner.Bytes(), &record))
		if record["level"] == "warn" {
			line, err := json.Marshal(record)
			require.NoError(t, err)
			warns = append(warns, string(line))
		}
	}
	return warns
}

// normalizeWarnings renders the expected warnings the way warnings renders the logged ones.
func normalizeWarnings(t *testing.T, warns []string) []string {
	t.Helper()

	var out []string
	for _, w := range warns {
		var record map[string]any
		require.NoError(t, json.Unmarshal([]byte(w), &record))
		line, err := json.Marshal(record)
		require.NoError(t, err)
		out = append(out, string(line))
	}
	return out
}

// updateMemory sets one replica's memory limit behind compose's back, the way `docker update` does.
func (e *fidelityEnv) updateMemory(t *testing.T, service string, replica int, bytes int64) {
	t.Helper()

	name := strings.Join([]string{e.project, service, strconv.Itoa(replica)}, "-")
	_, err := e.daemon.ContainerUpdate(e.ctx, name, container.UpdateConfig{
		Resources: container.Resources{Memory: bytes, MemorySwap: -1},
	})
	require.NoError(t, err, "update %s", name)
}

// runOutsideContainer starts a container outside the compose project, named <project>-<name>,
// on network when one is named.
func (e *fidelityEnv) runOutsideContainer(t *testing.T, name, network string) {
	t.Helper()

	hostConfig := &container.HostConfig{}
	if network != "" {
		hostConfig.NetworkMode = container.NetworkMode(network)
	}
	created, err := e.daemon.ContainerCreate(e.ctx,
		&container.Config{Image: fidelityBusybox, Cmd: []string{"sleep", "infinity"}},
		hostConfig, nil, nil, e.project+"-"+name)
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = e.daemon.ContainerRemove(context.Background(), created.ID, container.RemoveOptions{Force: true})
	})
	require.NoError(t, e.daemon.ContainerStart(e.ctx, created.ID, container.StartOptions{}))
}

// createOutsideVolume creates a volume outside the compose project, named <project>-<name>,
// and runs script over it at /data when one is given.
func (e *fidelityEnv) createOutsideVolume(t *testing.T, name, script string) {
	t.Helper()

	volumeName := e.project + "-" + name
	_, err := e.daemon.VolumeCreate(e.ctx, volume.CreateOptions{Name: volumeName})
	require.NoError(t, err)
	e.volumes[volumeName] = true
	if script != "" {
		busybox(t, dockerclient.NewEngine(e.daemon), volumeName, script)
	}
}

// createOutsideNetwork creates a network outside the compose project, named <project>-<name>.
func (e *fidelityEnv) createOutsideNetwork(t *testing.T, name string) {
	t.Helper()

	created, err := e.daemon.NetworkCreate(e.ctx, e.project+"-"+name, network.CreateOptions{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = e.daemon.NetworkRemove(context.Background(), created.ID) })
}
