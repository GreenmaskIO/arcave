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
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"filippo.io/age"
	"github.com/docker/compose/v5/pkg/api"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/config"
	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/repository/encryptionmarker"
)

// statusSpec is a compose project with a healthchecked service publishing a port, a scaled one, and
// a job another waits on to complete.
const statusSpec = `
name: %[1]s
services:
  db:
    image: ` + busyboxImage + `
    command: ["sleep", "3600"]
    stop_signal: SIGKILL
    ports: ["127.0.0.1::5432"]
    healthcheck:
      test: ["CMD", "true"]
      interval: 1s
      retries: 3
    volumes: ["data:/data"]
  web:
    image: ` + busyboxImage + `
    command: ["sleep", "3600"]
    stop_signal: SIGKILL
    deploy:
      replicas: 2
  migrate:
    image: ` + busyboxImage + `
    command: ["true"]
  app:
    image: ` + busyboxImage + `
    command: ["sleep", "3600"]
    stop_signal: SIGKILL
    depends_on:
      migrate:
        condition: service_completed_successfully
volumes:
  data: {}
`

const statusArcaveProject = "arcave-status-it"

// statusEnv is a compose project run from statusSpec and an arcave project over it.
type statusEnv struct {
	*imageRollbackEnv
	projectRoot string
	storeDir    string
	document    string
	env         []string
}

func newStatusEnv(t *testing.T, env ...string) *statusEnv {
	t.Helper()

	compose := newImageRollbackEnv(t)
	storage := startDirectoryStorage(t).file
	statusEnv := &statusEnv{
		imageRollbackEnv: compose,
		projectRoot:      t.TempDir(),
		storeDir:         storage.Directory.Path,
		document:         fmt.Sprintf(statusSpec, compose.project),
		env:              env,
	}
	runArcave(t, statusEnv.projectRoot, env, "init", "--name", statusArcaveProject)
	writeStorageConfigWith(t, statusEnv.projectRoot, storage,
		storageE2EBackend{name: config.SnapshotBackendFullCopy, backend: config.SnapshotBackendFullCopy}, len(env) > 0)
	compose.upDocument(t, statusEnv.document)
	return statusEnv
}

func (e *statusEnv) run(t *testing.T, args ...string) []byte {
	t.Helper()

	return runArcave(t, e.projectRoot, e.env, append([]string{"-p", e.project}, args...)...)
}

func (e *statusEnv) status(t *testing.T) model.ComposeProjectStatus {
	t.Helper()

	return decode[model.ComposeProjectStatus](t, e.run(t, "status", "--format", "json"))
}

func TestStatus_AfterRollback(t *testing.T) {
	t.Parallel()

	// Arrange
	env := newStatusEnv(t)
	savepoint := decode[model.Savepoint](t, env.run(t, "savepoint", "create", "sp1"))

	// Act
	env.run(t, "rollback", "sp1")
	status := env.status(t)

	// Assert
	assert.Equal(t, model.ComposeProjectStateRunning, status.State, "notices: %+v", status.Notices)
	require.NotNil(t, status.Deployment)
	assert.Equal(t, model.DeploymentSourceSavepoint, status.Deployment.Source)
	assert.Equal(t, model.DeploymentRecordFound, status.Deployment.Record)
	require.NotNil(t, status.Deployment.Savepoint)
	assert.Equal(t, savepoint.ID, status.Deployment.Savepoint.ID)
	assert.Equal(t, "sp1", status.Deployment.Savepoint.Name)
	assert.Equal(t, &model.RestoredCounts{
		Volumes:        len(savepoint.Stats),
		WritableLayers: len(savepoint.WritableLayers),
	}, status.Deployment.Savepoint.Restored, "the record says what was restored")
	byName := map[string]model.ServiceStatus{}
	for _, service := range status.Services {
		byName[service.Name] = service
		for _, replica := range service.Replicas {
			assert.True(t, replica.InDeployment, "%s-%d is the rollback's", service.Name, replica.Number)
			assert.True(t, replica.InSavepoint, "%s-%d is the savepoint's", service.Name, replica.Number)
		}
	}
	require.Len(t, byName["migrate"].Replicas, 1)
	assert.Equal(t, model.ReplicaStateCompleted, byName["migrate"].State, "a job another waits on completed")
	assert.Equal(t, 0, byName["migrate"].Replicas[0].ExitCode)
	assert.Equal(t, 1, byName["migrate"].ReplicasUp)
	assert.Equal(t, 2, byName["web"].ReplicasExpected)
	assert.Equal(t, 2, byName["web"].ReplicasUp)
	require.Len(t, byName["db"].Replicas, 1)
	db := byName["db"].Replicas[0]
	assert.Equal(t, model.ReplicaHealthHealthy, db.Health)
	inspected, err := env.daemon.ContainerInspect(env.ctx, db.ContainerID, client.ContainerInspectOptions{})
	require.NoError(t, err)
	var published []string
	for port, bindings := range inspected.Container.NetworkSettings.Ports {
		for _, binding := range bindings {
			published = append(published, fmt.Sprintf("%s:%s->%d", binding.HostIP, binding.HostPort, port.Num()))
		}
	}
	var reported []string
	for _, port := range db.Ports {
		reported = append(reported, fmt.Sprintf("%s:%d->%d", port.HostIP, port.HostPort, port.ContainerPort))
	}
	assert.ElementsMatch(t, published, reported, "the ports are the daemon's")
	assertDeploymentLabelsOnContainersAlone(t, env, status)
}

// assertDeploymentLabelsOnContainersAlone pins where a rollback puts its labels: on every replica,
// on no network or volume.
func assertDeploymentLabelsOnContainersAlone(t *testing.T, env *statusEnv, status model.ComposeProjectStatus) {
	t.Helper()

	key := string(model.SystemLabelDeploymentSavepointID)
	for _, service := range status.Services {
		for _, replica := range service.Replicas {
			inspected, err := env.daemon.ContainerInspect(env.ctx, replica.ContainerID, client.ContainerInspectOptions{})
			require.NoError(t, err)
			assert.Equal(t, status.Deployment.Savepoint.ID.String(), inspected.Container.Config.Labels[key],
				"%s carries the deployment", replica.Container)
		}
	}
	composeProjectFilter := api.ProjectLabel + "=" + env.project
	networks, err := env.daemon.NetworkList(env.ctx, client.NetworkListOptions{Filters: make(client.Filters).Add("label", composeProjectFilter)})
	require.NoError(t, err)
	for _, network := range networks.Items {
		assert.NotContains(t, network.Labels, key, "network %s", network.Name)
	}
	volumes, err := env.daemon.VolumeList(env.ctx, client.VolumeListOptions{Filters: make(client.Filters).Add("label", composeProjectFilter)})
	require.NoError(t, err)
	for _, volume := range volumes.Items {
		assert.NotContains(t, volume.Labels, key, "volume %s", volume.Name)
	}
}

func TestStatus_DetectsDrift(t *testing.T) {
	t.Parallel()

	// Arrange
	env := newStatusEnv(t)
	env.run(t, "savepoint", "create", "sp1")
	env.run(t, "rollback", "sp1")
	before := env.status(t)
	web1 := findReplica(t, before, "web", 1)
	// Killed, not stopped: an exit 0 of a service no other waits on degrades nothing.
	_, err := env.daemon.ContainerStop(env.ctx, web1.ContainerID, client.ContainerStopOptions{Signal: "SIGKILL"})
	require.NoError(t, err)

	// Act: db rebuilt outside arcave, as compose rebuilds a replica on the image its file names.
	stopped := env.status(t)
	recreateAsCompose(t, env, findReplica(t, before, "db", 1).ContainerID)
	recreated := env.status(t)

	// Assert
	assert.Equal(t, model.ComposeProjectStateDegraded, stopped.State)
	assert.Equal(t, model.ReplicaStateExited, findReplica(t, stopped, "web", 1).State)
	assert.True(t, slices.ContainsFunc(stopped.Notices, func(notice model.Notice) bool {
		return notice.Code == model.NoticeCodeReplicaExited && notice.Severe && strings.HasPrefix(notice.Message, "web-1 ")
	}), "the killed replica is warned of: %+v", stopped.Notices)
	assert.Equal(t, model.DeploymentSourceMixed, recreated.Deployment.Source)
	db := findReplica(t, recreated, "db", 1)
	assert.False(t, db.InDeployment)
	assert.True(t, db.InSavepoint)
	assert.NotEmpty(t, db.Replaces, "compose's label says what it replaced")
	assert.True(t, slices.ContainsFunc(recreated.Notices, func(notice model.Notice) bool {
		return notice.Code == model.NoticeCodeReplicaRecreated && strings.HasPrefix(notice.Message, "db-1 was recreated") &&
			strings.Contains(notice.Message, "(compose replaced "+db.Replaces+")") &&
			strings.Contains(notice.Message, "the writable layer the rollback restored is gone")
	}), "the replacement is warned of: %+v", recreated.Notices)
}

// recreateAsCompose rebuilds a container as compose recreates one: the same config and name, its
// deployment labels gone and com.docker.compose.replace naming the container it replaced.
func recreateAsCompose(t *testing.T, env *statusEnv, id string) {
	t.Helper()

	inspected, err := env.daemon.ContainerInspect(env.ctx, id, client.ContainerInspectOptions{})
	require.NoError(t, err)
	old := inspected.Container
	name := strings.TrimPrefix(old.Name, "/")
	config := *old.Config
	config.Labels = map[string]string{api.ContainerReplaceLabel: name}
	for key, value := range old.Config.Labels {
		if !strings.HasPrefix(key, model.DeploymentLabelPrefix) {
			config.Labels[key] = value
		}
	}
	endpoints := map[string]*network.EndpointSettings{}
	for networkName, endpoint := range old.NetworkSettings.Networks {
		endpoints[networkName] = &network.EndpointSettings{Aliases: endpoint.Aliases}
	}
	_, err = env.daemon.ContainerRemove(env.ctx, id, client.ContainerRemoveOptions{Force: true})
	require.NoError(t, err)
	created, err := env.daemon.ContainerCreate(env.ctx, client.ContainerCreateOptions{
		Config:           &config,
		HostConfig:       old.HostConfig,
		NetworkingConfig: &network.NetworkingConfig{EndpointsConfig: endpoints},
		Name:             name,
	})
	require.NoError(t, err)
	_, err = env.daemon.ContainerStart(env.ctx, created.ID, client.ContainerStartOptions{})
	require.NoError(t, err)
}

func TestStatus_Absent(t *testing.T) {
	t.Parallel()

	// Arrange
	env := newStatusEnv(t)
	env.run(t, "savepoint", "create", "sp1")
	env.down(t)

	// Act
	status := env.status(t)
	text := string(runArcave(t, env.projectRoot, nil, "-p", env.project, "status", "--format", "text"))

	// Assert
	assert.Equal(t, model.ComposeProjectStateAbsent, status.State)
	assert.Nil(t, status.Deployment)
	assert.Empty(t, status.Services)
	assert.Equal(t, &model.SessionRef{ID: model.DefaultSession, Name: model.DefaultSessionName}, status.Session)
	assert.Contains(t, text, env.project+" · not running")
	assert.Contains(t, text, "session    "+model.DefaultSessionName)
	assert.NotContains(t, text, "arcave ", "a status prints no command to run")
}

func TestStatus_SessionNamingNone(t *testing.T) {
	t.Parallel()

	// Arrange
	env := newStatusEnv(t)
	env.run(t, "savepoint", "create", "sp1")

	// Act
	cmd := arcaveCommandIn(t, env.projectRoot, "-p", env.project, "--session", "nope", "status", "--format", "json")
	out, err := cmd.Output()

	// Assert: the user's typo is the command's error, not a partial status.
	var exitErr *exec.ExitError
	require.True(t, errors.As(err, &exitErr), "a session naming none fails status: %v", err)
	assert.Empty(t, out, "and prints no status")
	assert.Contains(t, string(exitErr.Stderr), model.ErrSessionNotFound.Error())
}

func TestStatus_StoreUnreadable(t *testing.T) {
	t.Parallel()

	// Arrange: the project is marked for one key, and status runs with another.
	marking, err := age.GenerateX25519Identity()
	require.NoError(t, err)
	reading, err := age.GenerateX25519Identity()
	require.NoError(t, err)
	env := newStatusEnv(t, "ARCAVE_ENCRYPTION_KEY="+marking.String())
	env.run(t, "savepoint", "create", "sp1")
	env.run(t, "rollback", "sp1")
	markerBefore := readEncryptionMarker(t, env)

	// Act
	cmd := arcaveCommandIn(t, env.projectRoot, "-p", env.project, "status", "--format", "json")
	cmd.Env = append(cmd.Env, "ARCAVE_ENCRYPTION_KEY="+reading.String())
	out, err := cmd.Output()

	// Assert
	var exitErr *exec.ExitError
	require.True(t, errors.As(err, &exitErr), "a partial status exits non-zero: %v", err)
	assert.Equal(t, 1, exitErr.ExitCode())
	status := decode[model.ComposeProjectStatus](t, out)
	require.Len(t, status.Unreadable, 1)
	assert.Equal(t, model.StatusUnreadableStore, status.Unreadable[0].Source)
	assert.Contains(t, status.Unreadable[0].Error, model.ErrWrongStoreKey.Error())
	assert.Equal(t, model.ComposeProjectStateRunning, status.State, "the daemon's part is whole")
	assert.Equal(t, "sp1", status.Deployment.Savepoint.Name, "and the labels name the savepoint")
	assert.Len(t, status.Services, 4)
	assert.Equal(t, markerBefore, readEncryptionMarker(t, env), "the marker is read, never written")
}

func findReplica(t *testing.T, status model.ComposeProjectStatus, service string, number int) model.ReplicaStatus {
	t.Helper()

	for _, candidate := range status.Services {
		for _, replica := range candidate.Replicas {
			if candidate.Name == service && replica.Number == number {
				return replica
			}
		}
	}
	require.Failf(t, "replica not found", "%s-%d in %+v", service, number, status.Services)
	return model.ReplicaStatus{}
}

// readEncryptionMarker reads the arcave project's encryption marker, and when it was last written.
func readEncryptionMarker(t *testing.T, env *statusEnv) string {
	t.Helper()

	file := filepath.Join(env.storeDir, "arcave", "projects", statusArcaveProject, encryptionmarker.FileName)
	data, err := os.ReadFile(file)
	require.NoError(t, err)
	info, err := os.Stat(file)
	require.NoError(t, err)
	return string(data) + " @ " + info.ModTime().String()
}
