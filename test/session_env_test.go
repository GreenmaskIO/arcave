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
	"path/filepath"
	"testing"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/volume"
	"github.com/moby/moby/client"
	"github.com/rs/zerolog"

	"github.com/greenmaskio/arcave/internal/config"
	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/utils/testutils/daemontest"
)

// inflightLabelPrefix opens every label of an inflight marker's volume.
const inflightLabelPrefix = "io.arcave.inflight."

// sessionArcaveProject opens the name of the arcave project a session test stores under; each env
// suffixes it, as the inflight marker is the arcave project's on the shared daemon.
const sessionArcaveProject = "arcave-session-tenant"

// sessionEnv is one arcave project directory the built binary works in, and the compose project
// withComposeProject brings up beside it.
type sessionEnv struct {
	ctx           context.Context
	projectRoot   string
	arcaveProject string
	storage       config.Storage
	inflight      daemonMarkers
	// composeProject is the compose project, project its name; nil and empty until
	// withComposeProject.
	composeProject *composeProject
	project        string
}

func newSessionEnv(t *testing.T) *sessionEnv {
	t.Helper()

	arcaveProject := sessionArcaveProject + "-" + daemontest.RandomSuffix(t)
	return &sessionEnv{
		ctx:           zerolog.New(io.Discard).WithContext(context.Background()),
		projectRoot:   t.TempDir(),
		arcaveProject: arcaveProject,
		storage: config.Storage{
			Type:      config.StorageTypeDirectory,
			Directory: &config.DirectoryStorage{Path: filepath.Join(".arcave", "storage")},
		},
		inflight: openInflightMarkers(t, arcaveProject),
	}
}

// openInflightMarkers is arcaveProject's inflight marker on the test daemon; whatever marker the
// test leaves is cleared when it ends.
func openInflightMarkers(t *testing.T, arcaveProject string) daemonMarkers {
	t.Helper()

	markers := daemonMarkers{
		daemon:        daemontest.NewClient(t),
		arcaveProject: arcaveProject,
	}
	t.Cleanup(func() {
		if err := markers.Clear(context.Background()); err != nil {
			t.Logf("clearing the inflight marker of %s: %v", arcaveProject, err)
		}
	})
	return markers
}

// daemonMarkers reads and writes an arcave project's inflight marker the way the binary keeps it,
// spelled out apart from it: a volume per write on the daemon, the marker in its labels.
type daemonMarkers struct {
	daemon        *client.Client
	arcaveProject string
}

// Get reads the newest marker; found is false when none stands.
func (m daemonMarkers) Get(ctx context.Context) (marker model.InflightMarker, found bool, err error) {
	volumes, err := m.list(ctx)
	if err != nil {
		return model.InflightMarker{}, false, err
	}
	for _, v := range volumes {
		label := func(key string) string { return v.Labels[inflightLabelPrefix+key] }
		session, err := model.ParseSessionID(label("session"))
		if err != nil {
			return model.InflightMarker{}, false, fmt.Errorf("volume %s: %w", v.Name, err)
		}
		at, err := time.Parse(time.RFC3339Nano, label("at"))
		if err != nil {
			return model.InflightMarker{}, false, fmt.Errorf("volume %s: %w", v.Name, err)
		}
		if found && !at.After(marker.At) {
			continue
		}
		marker = model.InflightMarker{
			Phase:          model.InflightPhase(label("phase")),
			Session:        session,
			ComposeProject: model.DockerComposeProjectName(label("compose-project")),
			Target:         model.SavepointID(label("target")),
			Safety:         model.SavepointID(label("safety")),
			At:             at,
		}
		found = true
	}
	return marker, found, nil
}

// Put writes marker as the binary does, in a volume of its own beside any earlier one.
func (m daemonMarkers) Put(ctx context.Context, marker model.InflightMarker) error {
	_, err := m.daemon.VolumeCreate(ctx, client.VolumeCreateOptions{
		Name: "arcave-inflight-" + marker.ComposeProject.String() + "-" + model.NewSavepointID().String(),
		Labels: map[string]string{
			inflightLabelPrefix + "arcave-project":  m.arcaveProject,
			inflightLabelPrefix + "compose-project": marker.ComposeProject.String(),
			inflightLabelPrefix + "session":         marker.Session.String(),
			inflightLabelPrefix + "target":          marker.Target.String(),
			inflightLabelPrefix + "safety":          marker.Safety.String(),
			inflightLabelPrefix + "phase":           string(marker.Phase),
			inflightLabelPrefix + "at":              marker.At.UTC().Format(time.RFC3339Nano),
		},
	})
	return err
}

// Clear removes every marker volume of the arcave project.
func (m daemonMarkers) Clear(ctx context.Context) error {
	volumes, err := m.list(ctx)
	if err != nil {
		return err
	}
	for _, v := range volumes {
		if _, err := m.daemon.VolumeRemove(ctx, v.Name, client.VolumeRemoveOptions{}); err != nil && !cerrdefs.IsNotFound(err) {
			return err
		}
	}
	return nil
}

// list lists the arcave project's marker volumes.
func (m daemonMarkers) list(ctx context.Context) ([]volume.Volume, error) {
	listed, err := m.daemon.VolumeList(ctx, client.VolumeListOptions{
		Filters: make(client.Filters).Add("label", inflightLabelPrefix+"arcave-project="+m.arcaveProject),
	})
	if err != nil {
		return nil, err
	}
	return listed.Items, nil
}

// storeDir is the store root on disk, every arcave project beneath it.
func (e *sessionEnv) storeDir() string {
	return filepath.Join(e.projectRoot, e.storage.Directory.Path, "arcave")
}

// sessionDir is where a session's tree lives on disk.
func (e *sessionEnv) sessionDir(id model.SessionID) string {
	return filepath.Join(e.storeDir(), "projects", e.arcaveProject, "sessions", id.String())
}

// create makes a session named name through the binary and fails the test if it cannot.
func (e *sessionEnv) create(t *testing.T, name string) model.Session {
	t.Helper()

	return decode[model.Session](t, runArcave(t, e.projectRoot, nil, "session", "create", "--name", name))
}

// withComposeProject brings a compose project of one replica up beside the arcave project.
func (e *sessionEnv) withComposeProject(t *testing.T) {
	t.Helper()

	e.composeProject = newComposeProject(t, "session-it")
	e.project = e.composeProject.project
	e.composeProject.up(t, 1)
}

// listSavepoints lists the compose project's savepoints in session through the binary, newest
// first; includeSafety lists the safety snapshots too.
func (e *sessionEnv) listSavepoints(t *testing.T, session string, includeSafety bool) []model.Savepoint {
	t.Helper()

	args := []string{"-p", e.project, "--session", session, "savepoint", "list"}
	if includeSafety {
		args = append(args, "--include-safety-snapshots")
	}
	return decode[[]model.Savepoint](t, runArcave(t, e.projectRoot, nil, args...))
}
