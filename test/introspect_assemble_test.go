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
	"bufio"
	"context"
	"encoding/json"
	"io"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/compose-spec/compose-go/v2/cli"
	"github.com/compose-spec/compose-go/v2/types"
	cerrdefs "github.com/containerd/errdefs"
	"github.com/docker/cli/cli/command"
	"github.com/docker/cli/cli/flags"
	"github.com/docker/compose/v5/pkg/api"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
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
	busyboxDigest = "@sha256:bdf57e528e45e4433820e045b29b4597825a1c9e38353532d90a01445013f82e"
	alpineDigest  = "@sha256:5291449c3df73caf6ed85e649dec1b9e818b39a5d8c871e97afc13e9cd5e8fa8"
	redisDigest   = "@sha256:858f009f9709ce576febc734aa78b8f6d624b82571f9ddb6bda4377c833b3499"

	fidelityBusybox = "busybox:1.37" + busyboxDigest
	fidelityAlpine  = "alpine:3.22" + alpineDigest
	fidelityRedis   = "redis:7-alpine" + redisDigest
)

// fidelityEngine is what a want says of the engine it runs on: the images as that engine
// stores them, and facts, the *engine anchor every service of a want merges.
type fidelityEngine struct {
	busybox, alpine, redis string
	facts                  string
}

// fidelityEngines holds each engine's own truth, stated whole; no comparison relaxes it.
var fidelityEngines = map[model.ContainerRuntimeKind]fidelityEngine{
	model.ContainerRuntimeDocker: {
		busybox: fidelityBusybox,
		alpine:  fidelityAlpine,
		redis:   fidelityRedis,
		facts: `
x-engine: &engine
  ipc: private
  cgroup: private
`,
	},
	// Podman stores no tag beside a digest, and stamps each container started with the
	// oom_score_adj and rlimits of its API process, read from a probe (readEngineStamps).
	model.ContainerRuntimePodman: {
		busybox: "busybox" + busyboxDigest,
		alpine:  "alpine" + alpineDigest,
		redis:   "redis" + redisDigest,
		facts: `
x-engine: &engine
  cgroup: private
  oom_score_adj: {{oom_score_adj}}
  ulimits:
    nofile: {soft: {{nofile_soft}}, hard: {{nofile_hard}}}
    nproc: {soft: {{nproc_soft}}, hard: {{nproc_hard}}}
`,
	},
}

// fidelityCase is one compose capability: the file a user wrote, and the spec arcave must
// assemble from the compose project that file created.
type fidelityCase struct {
	name string
	// compose is compose.yaml; files are written beside it, and composeFiles name the files
	// loaded when compose.yaml alone is not.
	compose      string
	files        map[string]string
	composeFiles []string
	// want is the spec on every engine; wants, one per engine, where their specs differ in
	// more than fidelityEngines says. A case names exactly one of the two.
	want  string
	wants map[model.ContainerRuntimeKind]string
	// warns are the warnings Assemble logs, one JSON record each.
	warns []string
	// setup runs before the compose project is created; afterUp once it runs.
	setup   func(t *testing.T, e *fidelityEnv)
	afterUp func(t *testing.T, e *fidelityEnv)
	// drift applies to R1 what pass 2 is expected to change, ahead of the comparison with R2.
	drift func(r *model.IntrospectionResult, runtime model.ContainerRuntimeKind)
	// unsupported names, by engine, why that engine refuses to create the case's compose
	// project at all; the case is skipped there.
	unsupported map[model.ContainerRuntimeKind]string
	// knownDefect names the BACKLOG.md row a pass is known to fail on, keyed by the pass or by
	// <runtime>/<pass>; the pass asserts the failure, so the case turns red once it is fixed.
	knownDefect map[string]string
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
	// runtime is the engine behind the daemon, which decides the want a case is held to.
	runtime model.ContainerRuntimeKind
	// values fill the placeholders of a want known only once something runs.
	values  map[string]string
	project string
	dir     string
	// volumes are every volume an introspection saw: a Down leaves the anonymous ones behind.
	volumes map[string]bool
	// outsideContainers and outsideNetworks are what a case created beside the compose project.
	outsideContainers []string
	outsideNetworks   []string
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
	composeService, prompter := newComposeService(t, dockerCLI)
	logs := &syncBuffer{}
	profile := newRuntimeProfile(t, daemon)
	e := &fidelityEnv{
		ctx:     zerolog.New(logs).WithContext(context.Background()),
		logs:    logs,
		daemon:  daemon,
		compose: composeService,
		runner: composeclient.NewClient(composeService, composeclient.Options{
			WaitTimeout: 90 * time.Second, StopTimeout: 2 * time.Second, Prompter: prompter,
		}),
		inspector: introspect.NewInspector(profile.api, model.DockerComposeProjectName(project)),
		assembler: profile.assembler,
		codec:     composespec.NewCodec(),
		runtime:   profile.runtime,
		project:   project,
		dir:       t.TempDir(),
		volumes:   map[string]bool{},
		values:    map[string]string{},
	}
	t.Cleanup(func() { e.cleanup(t) })
	if e.runtime == model.ContainerRuntimePodman {
		e.values, err = readEngineStamps(e.ctx, daemon)
		require.NoError(t, err, "read what the engine stamps")
	}
	return e
}

// cleanup removes the compose project with every network and volume it ever had.
// cleanup removes in an order that frees each thing before it is removed: the containers
// outside the project, the project, the outside networks, then every volume.
func (e *fidelityEnv) cleanup(t *testing.T) {
	ctx := context.Background()
	// One a replica joins goes after the project on Podman, which refuses to remove it first.
	var joined []string
	for _, id := range e.outsideContainers {
		if _, err := e.daemon.ContainerRemove(ctx, id, client.ContainerRemoveOptions{Force: true}); err != nil {
			joined = append(joined, id)
		}
	}
	removeComposeProject(t, e.daemon, e.project)
	for _, id := range joined {
		_, err := e.daemon.ContainerRemove(ctx, id, client.ContainerRemoveOptions{Force: true})
		assert.NoError(t, err, "remove container %s", id)
	}
	for _, id := range e.outsideNetworks {
		_, err := e.daemon.NetworkRemove(ctx, id, client.NetworkRemoveOptions{})
		assert.NoError(t, err, "remove network %s", id)
	}
	for name := range e.volumes {
		if _, err := e.daemon.VolumeRemove(ctx, name, client.VolumeRemoveOptions{Force: true}); err != nil && !cerrdefs.IsNotFound(err) {
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

// renderWant is the spec the case holds this engine to, with the engine's facts ahead of it.
func (e *fidelityEnv) renderWant(tc fidelityCase) string {
	engine := fidelityEngines[e.runtime]
	want, ok := tc.wants[e.runtime]
	if !ok {
		want = tc.want
	}
	pairs := []string{
		"{{project}}", e.project,
		"{{dir}}", e.dir,
		"{{busybox}}", engine.busybox,
		"{{alpine}}", engine.alpine,
		"{{redis}}", engine.redis,
	}
	for name, value := range e.values {
		pairs = append(pairs, "{{"+name+"}}", value)
	}
	return strings.NewReplacer(pairs...).Replace("name: {{project}}\n" + engine.facts + want)
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

	require.NoError(t, e.runner.Create(e.ctx, project, nil), "create")
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

	row, ok := tc.knownDefect[string(e.runtime)+"/"+pass]
	if !ok {
		row, ok = tc.knownDefect[pass]
	}
	if ok {
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
	_, err := e.daemon.ContainerUpdate(e.ctx, name, client.ContainerUpdateOptions{
		Resources: &container.Resources{Memory: bytes, MemorySwap: -1},
	})
	require.NoError(t, err, "update %s", name)
}

// readPublishedPort is the host port the daemon gave service's first replica for port.
func (e *fidelityEnv) readPublishedPort(t *testing.T, service string, port string) string {
	t.Helper()

	inspect, err := e.daemon.ContainerInspect(e.ctx, strings.Join([]string{e.project, service, "1"}, "-"),
		client.ContainerInspectOptions{})
	require.NoError(t, err)
	bindings := inspect.Container.NetworkSettings.Ports[network.MustParsePort(port)]
	require.NotEmpty(t, bindings, "no binding of %s", port)
	return bindings[0].HostPort
}

// runOutsideContainer starts a container outside the compose project, named <project>-<name>,
// on network when one is named.
func (e *fidelityEnv) runOutsideContainer(t *testing.T, name, network string) {
	t.Helper()

	hostConfig := &container.HostConfig{}
	if network != "" {
		hostConfig.NetworkMode = container.NetworkMode(network)
	}
	created, err := e.daemon.ContainerCreate(e.ctx, client.ContainerCreateOptions{
		Config:     &container.Config{Image: fidelityBusybox, Cmd: []string{"sleep", "infinity"}},
		HostConfig: hostConfig,
		Name:       e.project + "-" + name,
	})
	require.NoError(t, err)
	e.outsideContainers = append(e.outsideContainers, created.ID)
	_, err = e.daemon.ContainerStart(e.ctx, created.ID, client.ContainerStartOptions{})
	require.NoError(t, err)
}

// createOutsideVolume creates a volume outside the compose project, named <project>-<name>,
// and runs script over it at /data when one is given.
func (e *fidelityEnv) createOutsideVolume(t *testing.T, name, script string) {
	t.Helper()

	volumeName := e.project + "-" + name
	_, err := e.daemon.VolumeCreate(e.ctx, client.VolumeCreateOptions{Name: volumeName})
	require.NoError(t, err)
	e.volumes[volumeName] = true
	if script != "" {
		busybox(t, dockerclient.NewEngine(e.daemon), volumeName, script)
	}
}

// createOutsideNetwork creates a network outside the compose project, named <project>-<name>.
func (e *fidelityEnv) createOutsideNetwork(t *testing.T, name string) {
	t.Helper()

	created, err := e.daemon.NetworkCreate(e.ctx, e.project+"-"+name, client.NetworkCreateOptions{})
	require.NoError(t, err)
	e.outsideNetworks = append(e.outsideNetworks, created.ID)
}

// engineStamps caches readEngineStamps: every container of a run gets the same.
var engineStamps struct {
	once   sync.Once
	values map[string]string
	err    error
}

// readEngineStamps reads, off a probe it starts, the oom_score_adj and rlimits the engine
// stamps on a container asking for none: its API process's, so the host's. Only a started
// container reports them.
func readEngineStamps(ctx context.Context, daemon *client.Client) (map[string]string, error) {
	engineStamps.once.Do(func() {
		engineStamps.values, engineStamps.err = runStampProbe(ctx, daemon)
	})
	return maps.Clone(engineStamps.values), engineStamps.err
}

func runStampProbe(ctx context.Context, daemon *client.Client) (map[string]string, error) {
	pull, err := daemon.ImagePull(ctx, fidelityBusybox, client.ImagePullOptions{})
	if err != nil {
		return nil, err
	}
	//nolint:errcheck // the pull is over once the stream is read; the create below is the check
	defer pull.Close()
	if _, err := io.Copy(io.Discard, pull); err != nil {
		return nil, err
	}
	created, err := daemon.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config: &container.Config{Image: fidelityBusybox, Cmd: []string{"sleep", "infinity"}},
	})
	if err != nil {
		return nil, err
	}
	//nolint:errcheck // a probe left behind fails nothing the run checks
	defer daemon.ContainerRemove(context.WithoutCancel(ctx), created.ID, client.ContainerRemoveOptions{Force: true})
	if _, err := daemon.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		return nil, err
	}
	inspect, err := daemon.ContainerInspect(ctx, created.ID, client.ContainerInspectOptions{})
	if err != nil {
		return nil, err
	}
	hostConfig := inspect.Container.HostConfig
	values := map[string]string{"oom_score_adj": strconv.Itoa(hostConfig.OomScoreAdj)}
	for _, limit := range hostConfig.Ulimits {
		name := strings.ToLower(strings.TrimPrefix(limit.Name, "RLIMIT_"))
		values[name+"_soft"] = strconv.FormatInt(limit.Soft, 10)
		values[name+"_hard"] = strconv.FormatInt(limit.Hard, 10)
	}
	return values, nil
}

// fidelityGroups are the catalog's cases by the capability they exercise.
func fidelityGroups() []fidelityGroup {
	return []fidelityGroup{
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
}

// fidelityGroup is one capability's cases.
type fidelityGroup struct {
	name  string
	cases []fidelityCase
}

// wantPlaceholders are what a want may name; composePlaceholders what its compose project may.
var (
	composePlaceholders = []string{"project", "dir", "busybox", "alpine", "redis"}
	wantPlaceholders    = []string{
		"project", "dir", "busybox", "alpine", "redis", "published",
		"oom_score_adj", "nofile_soft", "nofile_hard", "nproc_soft", "nproc_hard",
	}
	placeholder = regexp.MustCompile(`{{(\w+)}}`)
)

// Every case states what every engine it runs on must assemble, so none is one engine's by
// omission. Needs no daemon, so it runs with the rest of ./test/, outside the fidelity tag.
func TestFidelityCases_Complete(t *testing.T) {
	for _, group := range fidelityGroups() {
		t.Run(group.name, func(t *testing.T) {
			for _, tc := range group.cases {
				t.Run(tc.name, func(t *testing.T) {
					assert.NotEqual(t, tc.want == "", len(tc.wants) == 0, "exactly one of want and wants")
					assertPlaceholders(t, composePlaceholders, tc.compose)
					assertPlaceholders(t, wantPlaceholders, tc.want)
					for runtime := range fidelityEngines {
						want, stated := tc.wants[runtime]
						_, unsupported := tc.unsupported[runtime]
						if len(tc.wants) > 0 {
							assert.NotEqual(t, stated, unsupported, "%s: a want, or why the engine refuses the case", runtime)
						}
						assertPlaceholders(t, wantPlaceholders, want)
					}
				})
			}
		})
	}
}

// assertPlaceholders asserts document names only the known placeholders.
func assertPlaceholders(t *testing.T, known []string, document string) {
	t.Helper()

	for _, match := range placeholder.FindAllStringSubmatch(document, -1) {
		assert.Contains(t, known, match[1], "unknown placeholder")
	}
}
