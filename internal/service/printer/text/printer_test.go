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

package text

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/utils"
)

var update = flag.Bool("update", false, "rewrite the golden files under testdata")

const (
	savepointID = model.SavepointID("01a188c4-1650-7000-8000-000000000001")
	safetyID    = model.SavepointID("01a188c9-0000-7000-8000-000000000002")
)

var (
	createdAt        = time.Date(2026, 10, 29, 19, 45, 35, 0, time.UTC)
	sessionID        = model.SessionID{0x01, 0x9a, 0x2b, 0x3c, 0x4d, 0x5e, 0x70, 0x00, 0x80, 0x00, 0, 0, 0, 0, 0, 0x07}
	removedSessionID = model.SessionID{0x01, 0x9a, 0x2b, 0x3c, 0x4d, 0x5e, 0x70, 0x00, 0x80, 0x00, 0, 0, 0, 0, 0, 0x09}
	failedSessionID  = model.SessionID{0x01, 0x9a, 0x2b, 0x3c, 0x4d, 0x5e, 0x70, 0x00, 0x80, 0x00, 0, 0, 0, 0, 0, 0x0a}
)

func TestPrinter_Print(t *testing.T) {
	tests := []struct {
		name   string
		opts   Options
		result any
	}{
		{
			name:   "savepoint_create",
			opts:   Options{ComposeProject: "arcave-demo"},
			result: newSavepoint(),
		},
		{
			name: "savepoint_create_unnamed_in_session",
			opts: Options{ComposeProject: "arcave-demo", Session: "feature x"},
			result: model.Savepoint{
				ID:        savepointID,
				CreatedAt: createdAt,
				Stats: []model.SnapshotStat{
					{Volume: namedVolume("demo_db"), SizeBytes: 1_000, StoredBytes: 1_000},
				},
			},
		},
		{
			name:   "savepoint_create_capture_drift",
			opts:   Options{ComposeProject: "arcave-demo"},
			result: newDriftedSavepoint(),
		},
		{
			name: "savepoint_list",
			result: []model.Savepoint{
				newSavepoint(),
				{
					ID:        safetyID,
					Name:      "before-rollback",
					Labels:    map[string]string{model.SavepointLabelSafetySnapshot: model.SavepointLabelSafetySnapshotValue},
					CreatedAt: createdAt.Add(time.Hour),
				},
			},
		},
		{
			name:   "savepoint_list_empty",
			result: []model.Savepoint{},
		},
		{
			name: "rollback",
			opts: Options{ComposeProject: "arcave-demo"},
			result: model.RollbackResult{
				RolledBackTo: newSavepoint(),
				Restored: []model.Volume{
					namedVolume("demo_db"), namedVolume("demo_minio"), namedVolume("demo_redis"), namedVolume("demo_cache"),
				},
				WritableLayers: []model.WritableLayerRestoreResult{
					{Service: "app", Replica: 1},
					{Service: "app", Replica: 2},
				},
				SafetySnapshot: safetyID,
			},
		},
		{
			name: "rollback_no_safety_snapshot",
			opts: Options{ComposeProject: "arcave-demo"},
			result: model.RollbackResult{
				RolledBackTo: newSavepoint(),
				Restored:     []model.Volume{namedVolume("demo_db")},
			},
		},
		{
			name: "rollback_leftover_volumes",
			opts: Options{ComposeProject: "arcave-demo"},
			result: model.RollbackResult{
				RolledBackTo:    newSavepoint(),
				Restored:        []model.Volume{namedVolume("demo_db"), namedVolume("demo_minio")},
				WritableLayers:  []model.WritableLayerRestoreResult{{Service: "app", Replica: 1}},
				LeftoverVolumes: []string{"demo_db", "demo_minio"},
			},
		},
		{
			name: "savepoint_remove_dry_run",
			result: model.RemoveResult{
				DryRun:  true,
				Removed: []model.Savepoint{newSavepoint()},
			},
		},
		{
			name:   "savepoint_remove_nothing",
			result: model.RemoveResult{},
		},
		{
			name: "gc_dry_run",
			opts: Options{Session: "feature"},
			result: model.GarbageCollectResult{
				DryRun:  true,
				Session: sessionID,
				Reports: []model.GarbageReport{
					{
						DataKind:       model.GarbageDataKindSavepointData,
						ComposeProject: "demo",
						Summary:        model.GarbageSummary{Found: 2, Skipped: 1, Bytes: utils.New[int64](12_400_000)},
					},
					{
						DataKind:       model.GarbageDataKindDeduplicatedVolumes,
						ComposeProject: "demo",
						Summary:        model.GarbageSummary{Aborted: "backend-unreadable"},
					},
					{
						DataKind: model.GarbageDataKindRestoredLayers,
						Summary:  model.GarbageSummary{Found: 1, Unjudged: 2},
					},
				},
				Total: model.GarbageTotal{
					Found:    3,
					Bytes:    12_400_000,
					Unjudged: 2,
					Aborted:  1,
				},
				Warnings: []string{"docker-layer-tags: the daemon cannot be reached"},
			},
		},
		{
			name: "gc",
			result: model.GarbageCollectResult{
				Session: sessionID,
				Reports: []model.GarbageReport{
					{
						DataKind:       model.GarbageDataKindSavepointData,
						ComposeProject: "demo",
						Summary:        model.GarbageSummary{Found: 2, Bytes: utils.New[int64](12_400_000)},
						Deletion: &model.GarbageDeletion{
							Deleted: []model.GarbageDeleted{{Target: "a", Bytes: utils.New[int64](12_400_000)}},
							Failed:  []model.GarbageDeleteFailure{{Target: "b", Error: "denied"}},
						},
					},
					{
						DataKind:       model.GarbageDataKindSafetySnapshots,
						ComposeProject: "demo",
						Summary:        model.GarbageSummary{Found: 1, Bytes: utils.New[int64](2_000_000)},
						Deletion: &model.GarbageDeletion{
							Deleted: []model.GarbageDeleted{{Target: "c", Bytes: utils.New[int64](2_000_000)}},
						},
					},
				},
				Total: model.GarbageTotal{
					Found:         3,
					Bytes:         14_400_000,
					BytesComplete: true,
					Deleted:       2,
					DeleteFailed:  1,
					FreedBytes:    utils.New[int64](14_400_000),
				},
			},
		},
		{
			name: "gc_freed_partial",
			result: model.GarbageCollectResult{
				Session: sessionID,
				Reports: []model.GarbageReport{
					{
						DataKind:       model.GarbageDataKindSavepointData,
						ComposeProject: "demo",
						Summary:        model.GarbageSummary{Found: 2, Bytes: utils.New[int64](12_400_000)},
						Deletion: &model.GarbageDeletion{
							Deleted: []model.GarbageDeleted{{Target: "a", Bytes: utils.New[int64](12_400_000)}, {Target: "b"}},
						},
					},
				},
				Total: model.GarbageTotal{Found: 2, Bytes: 12_400_000, BytesComplete: true, Deleted: 2},
			},
		},
		{
			name: "gc_freed_unknown",
			result: model.GarbageCollectResult{
				Session: sessionID,
				Reports: []model.GarbageReport{
					{
						DataKind:       model.GarbageDataKindDeduplicatedVolumes,
						ComposeProject: "demo",
						Summary:        model.GarbageSummary{Found: 1},
						Deletion: &model.GarbageDeletion{
							Deleted:   []model.GarbageDeleted{{Target: "a"}},
							Reclaimed: []model.GarbageReclaimed{{Target: "repo"}},
						},
					},
				},
				Total: model.GarbageTotal{Found: 1, Deleted: 1},
			},
		},
		{
			name: "gc_nothing_deleted",
			result: model.GarbageCollectResult{
				Session: sessionID,
				Reports: []model.GarbageReport{
					{
						DataKind:       model.GarbageDataKindSavepointData,
						ComposeProject: "demo",
						Summary:        model.GarbageSummary{Bytes: utils.New[int64](0)},
						Deletion:       &model.GarbageDeletion{},
					},
				},
				Total: model.GarbageTotal{BytesComplete: true, FreedBytes: utils.New[int64](0)},
			},
		},
		{
			name: "session",
			result: model.Session{
				ID:        sessionID,
				Name:      "feature",
				CreatedAt: createdAt,
				From:      &model.SessionOrigin{Session: model.DefaultSession},
			},
		},
		{
			name: "session_current",
			result: model.CurrentSession{
				Session: model.Session{
					ID:        sessionID,
					Name:      "feature",
					CreatedAt: createdAt,
				},
				CurrentSource: model.CurrentSessionSource{
					Source:    model.SessionRefSourceFlag,
					Overrides: &model.SessionRef{ID: model.DefaultSession, Name: model.DefaultSessionName},
				},
			},
		},
		{
			name: "session_current_default",
			result: model.CurrentSession{
				Session:       model.DefaultSessionRecord,
				CurrentSource: model.CurrentSessionSource{Source: model.SessionRefSourceDefault},
			},
		},
		{
			name: "session_list",
			result: []model.SessionSummary{
				{
					Session:             model.DefaultSessionRecord,
					Status:              model.SessionStatusReady,
					SavepointCount:      3,
					SafetySnapshotCount: 1,
					SizeBytes:           1_250_000_000,
					ComposeProjects:     []model.DockerComposeProjectName{"blog", "shop", "tools"},
				},
				{
					Session: model.Session{
						ID:        sessionID,
						Name:      "feature",
						CreatedAt: createdAt,
					},
					Status: model.SessionStatusReady,
					CurrentSource: &model.CurrentSessionSource{
						Source:    model.SessionRefSourceEnv,
						Overrides: &model.SessionRef{ID: model.DefaultSession, Name: model.DefaultSessionName},
					},
					SavepointCount:  1,
					SizeBytes:       48_800_000,
					ComposeProjects: []model.DockerComposeProjectName{"shop"},
				},
				{
					Session: model.Session{
						ID:        sessionID,
						Name:      "empty",
						CreatedAt: createdAt.Add(time.Hour),
					},
					Status:          model.SessionStatusReady,
					ComposeProjects: []model.DockerComposeProjectName{},
				},
			},
		},
		{
			name: "session_list_all",
			result: []model.SessionSummary{
				{
					Session:         model.Session{ID: removedSessionID},
					Status:          model.SessionStatusCloning,
					SizeBytes:       12_000_000,
					ComposeProjects: []model.DockerComposeProjectName{},
				},
				{
					Session:         model.Session{ID: failedSessionID},
					Status:          model.SessionStatusFailed,
					ComposeProjects: []model.DockerComposeProjectName{},
				},
				{
					Session:         model.Session{ID: sessionID, Name: "feature", CreatedAt: createdAt},
					Status:          model.SessionStatusReady,
					CurrentSource:   &model.CurrentSessionSource{Source: model.SessionRefSourcePointer},
					SavepointCount:  1,
					SizeBytes:       48_800_000,
					ComposeProjects: []model.DockerComposeProjectName{"shop"},
				},
			},
		},
		{
			name: "session_list_default",
			result: []model.SessionSummary{{
				Session:         model.DefaultSessionRecord,
				Status:          model.SessionStatusReady,
				CurrentSource:   &model.CurrentSessionSource{Source: model.SessionRefSourceDefault},
				ComposeProjects: []model.DockerComposeProjectName{},
			}},
		},
		{
			// --session names what the pointer names: nothing is hidden, so no footer.
			name: "session_list_override_hides_none",
			result: []model.SessionSummary{{
				Session:         model.DefaultSessionRecord,
				Status:          model.SessionStatusReady,
				CurrentSource:   &model.CurrentSessionSource{Source: model.SessionRefSourceFlag},
				ComposeProjects: []model.DockerComposeProjectName{},
			}},
		},
		{
			name: "session_show",
			opts: Options{Session: "baseline"},
			result: model.SessionDetail{
				Session: model.Session{
					ID:        sessionID,
					Name:      "feature x",
					CreatedAt: createdAt,
					From:      &model.SessionOrigin{Session: model.DefaultSession},
				},
				From:                &model.SessionRef{ID: model.DefaultSession, Name: model.DefaultSessionName},
				SavepointCount:      2,
				SafetySnapshotCount: 1,
				ComposeProjects: []model.ComposeProjectSavepoints{
					{
						ComposeProject: "blog",
						Savepoints: []model.Savepoint{{
							ID:        safetyID,
							Name:      "init",
							CreatedAt: createdAt.Add(time.Hour),
							Stats: []model.SnapshotStat{
								{Volume: namedVolume("blog_db"), SizeBytes: 48_000_000, StoredBytes: 48_000_000},
							},
						}},
					},
					{
						ComposeProject: "shop",
						Savepoints: []model.Savepoint{
							{
								ID:        safetyID,
								Labels:    map[string]string{model.SavepointLabelSafetySnapshot: model.SavepointLabelSafetySnapshotValue},
								CreatedAt: createdAt.Add(2 * time.Hour),
							},
							newSavepoint(),
						},
					},
				},
				CheckedAt: createdAt.Add(3 * time.Hour),
			},
		},
		{
			name: "session_show_empty",
			result: model.SessionDetail{
				Session:         model.DefaultSessionRecord,
				CurrentSource:   &model.CurrentSessionSource{Source: model.SessionRefSourceDefault},
				ComposeProjects: []model.ComposeProjectSavepoints{},
				CheckedAt:       createdAt,
			},
		},
		{
			name: "session_show_unnamed",
			result: model.SessionDetail{
				Session:       model.Session{ID: sessionID, From: &model.SessionOrigin{Session: removedSessionID}},
				CurrentSource: &model.CurrentSessionSource{
					Source:    model.SessionRefSourceEnv,
					Overrides: &model.SessionRef{ID: model.DefaultSession, Name: model.DefaultSessionName},
				},
				// The session it was forked from is gone: its id alone.
				From:           &model.SessionRef{ID: removedSessionID},
				SavepointCount: 1,
				ComposeProjects: []model.ComposeProjectSavepoints{{
					ComposeProject: "shop",
					Savepoints:     []model.Savepoint{newSavepoint()},
				}},
				CheckedAt: createdAt.Add(time.Minute),
			},
		},
		{
			name: "session_remove",
			result: model.RemoveSessionResponse{
				Session:    sessionID,
				Removed:    true,
				FreedBytes: 48_800_000,
			},
		},
		{
			name:   "unknown_type_as_json",
			result: map[string]string{"path": "compose.yaml"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			var out bytes.Buffer
			tt.opts.Location = time.UTC
			printer := New(&out, tt.opts)

			// Act
			err := printer.Print(tt.result)

			// Assert
			require.NoError(t, err)
			assertGolden(t, tt.name+".golden", out.String())
		})
	}
}

// The colors are the profile's: none for the zero one, a terminal's when it names one.
func TestPrinter_Print_Profile(t *testing.T) {
	tests := []struct {
		name      string
		profile   colorprofile.Profile
		wantColor bool
	}{
		{name: "the zero profile writes no color"},
		{name: "a file writes no color", profile: colorprofile.NoTTY},
		{name: "a terminal is colored", profile: colorprofile.ANSI, wantColor: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out, plain bytes.Buffer
			printer := New(&out, Options{
				Location: time.UTC,
				Profile:  tt.profile,
			})
			require.NoError(t, New(&plain, Options{Location: time.UTC}).Print(newSavepoint()))

			err := printer.Print(newSavepoint())

			require.NoError(t, err)
			assert.Equal(t, tt.wantColor, strings.Contains(out.String(), "\x1b["))
			assert.Equal(t, plain.String(), ansi.Strip(out.String()))
		})
	}
}

func TestFormatSize(t *testing.T) {
	tests := []struct {
		size int64
		want string
	}{
		{size: 0, want: "0 B"},
		{size: 999, want: "999 B"},
		{size: 53_000, want: "53.0 KB"},
		{size: 48_812_000, want: "48.8 MB"},
		{size: 274_600_000, want: "275 MB"},
		{size: 1_100_000_000, want: "1.1 GB"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			// Act
			got := formatSize(tt.size)

			// Assert
			assert.Equal(t, tt.want, got)
		})
	}
}

func newSavepoint() model.Savepoint {
	return model.Savepoint{
		ID:        savepointID,
		Name:      "test3",
		CreatedAt: createdAt,
		Stats: []model.SnapshotStat{
			{Volume: namedVolume("demo_postgres"), SizeBytes: 48_800_000, StoredBytes: 48_800_000},
			{Volume: namedVolume("demo_minio"), SizeBytes: 53_000, StoredBytes: 53_000},
			{Volume: namedVolume("demo_redis"), SizeBytes: 6_000, StoredBytes: 6_000},
			{
				Volume: model.Volume{
					ObjectID: "volume-anon-app-1-_tmp",
					Anon:     &model.VolumePropertiesAnon{Service: "app", Replica: 1, Target: "/tmp"},
				},
				SizeBytes:   1_000,
				StoredBytes: 1_000,
			},
		},
		WritableLayers: []model.WritableLayer{
			{Service: "app", Replica: 1, SizeBytes: 274_600_000, StoredBytes: 44_000},
		},
		Images: []model.Image{
			{ID: "sha256:aa", Refs: []string{"postgres:16"}, SizeBytes: 1_100_000_000, Stored: true},
			{ID: "sha256:0123456789abcdef", Stored: false},
		},
		NotCovered: &model.NotCovered{
			ServicesWithoutVolumes: []string{"worker"},
			KilledContainers:       []model.KilledContainer{{Service: "db", Replica: 1, Container: "0123456789abcdef"}},
		},
	}
}

// newDriftedSavepoint is newSavepoint taken with two units of app and one of db captured otherwise
// than the savepoint before it.
func newDriftedSavepoint() model.Savepoint {
	savepoint := newSavepoint()
	savepoint.CaptureDrift = &model.CaptureDrift{
		Since: model.SavepointRef{ID: safetyID, Name: "test2"},
		Changes: []model.CaptureChange{
			{
				Service: "app", Stamp: "mount./var/lib/data",
				Was: model.CaptureStampDecision{Capture: true, Backend: "fullcopy"},
				Now: model.CaptureStampDecision{Capture: true, Backend: "cdc"},
			},
			{Service: "app", Stamp: "image", Was: model.CaptureStampDecision{Capture: true}},
			{
				Service: "db", Stamp: "writable-layer",
				Was: model.CaptureStampDecision{},
				Now: model.CaptureStampDecision{Capture: true, Backend: "dedupcopy"},
			},
		},
	}
	return savepoint
}

func namedVolume(name string) model.Volume {
	return model.Volume{
		ObjectID: "volume-named-" + name,
		Named:    &model.VolumePropertiesNamed{VolumeName: name},
	}
}

// assertGolden compares got with testdata/name, rewriting it under -update.
func assertGolden(t *testing.T, name string, got string) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		require.NoError(t, os.MkdirAll("testdata", 0o755))
		require.NoError(t, os.WriteFile(path, []byte(got), 0o644))
	}
	want, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, string(want), got)
}
