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

package model

import (
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func demoDeployment() SavepointDeployment {
	return SavepointDeployment{
		ID:            "019ea000-0000-7000-8000-000000000000",
		At:            time.Date(2026, 11, 7, 14, 2, 11, 0, time.UTC),
		Origin:        DeploymentOriginRollback,
		ArcaveProject: "app",
		Session: SessionRef{
			ID:   DefaultSession,
			Name: DefaultSessionName,
		},
		Savepoint: DeployedSavepoint{
			ID:          "019e843e-1650-7000-8000-000000000000",
			Name:        "before-migration",
			Description: "the schema before 0042",
			CreatedAt:   time.Date(2026, 11, 6, 19, 41, 0, 0, time.UTC),
		},
		Manifest: DeploymentManifest{
			Services: map[string]DeployedService{
				"db":      {Replicas: []int{1}, Layers: []int{1}},
				"migrate": {Replicas: []int{1}},
			},
			Restored: DeploymentCounts{Volumes: 1, WritableLayers: 1},
			LeftOut:  DeploymentCounts{Volumes: 0, WritableLayers: 0},
		},
		ArcaveVersion: "v0.1.0",
		Host:          "laptop",
	}
}

func TestSavepointDeployment_Labels(t *testing.T) {
	revert := demoDeployment()
	revert.Origin = DeploymentOriginRevert
	revert.RevertedFrom = SavepointRef{ID: "019e9000-0000-7000-8000-000000000000", Name: "after-fix"}
	revert.Savepoint.SafetySnapshotFor = "019e9000-0000-7000-8000-000000000000"
	tests := []struct {
		name       string
		deployment SavepointDeployment
	}{
		{
			name:       "a rollback reads back as it was written",
			deployment: demoDeployment(),
		},
		{
			name:       "a revert carries the rollback it undid and the one its safety snapshot was for",
			deployment: revert,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			labels := tt.deployment.Labels()
			parsed, ok, err := ParseSavepointDeployment(labels)

			// Assert
			require.NoError(t, err)
			require.True(t, ok)
			assert.Equal(t, tt.deployment, parsed)
			assert.Len(t, labels, len(deploymentLabels), "every key is written, blank or not")
		})
	}
}

func TestSavepointDeployment_Labels_Bounds(t *testing.T) {
	// Arrange: every value past its bound.
	deployment := demoDeployment()
	deployment.Savepoint.Description = "first line\n\tsecond line " + strings.Repeat("ж", 300)
	for i := range 40 {
		deployment.Manifest.NotRestored.WritableLayers = append(deployment.Manifest.NotRestored.WritableLayers,
			SkippedWritableLayer{Service: "web", Replica: i + 1, Container: "demo-web", Reason: SkipReasonExcluded})
	}

	// Act
	parsed, ok, err := ParseSavepointDeployment(deployment.Labels())

	// Assert
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, maxLabelDescriptionRunes, utf8.RuneCountInString(parsed.Savepoint.Description))
	assert.True(t, strings.HasPrefix(parsed.Savepoint.Description, "first line second line жж"),
		"the description is one line: %q", parsed.Savepoint.Description)
	assert.True(t, strings.HasSuffix(parsed.Savepoint.Description, "…"))
	assert.Len(t, parsed.Manifest.NotRestored.WritableLayers, maxLabelNotRestoredEntries)
	assert.True(t, parsed.Manifest.NotRestored.Truncated)
}

func TestSavepointDeployment_Labels_ManifestPastItsBound(t *testing.T) {
	// Arrange: a detail long enough that even the cut lists overflow the manifest's bound.
	deployment := demoDeployment()
	for i := range maxLabelNotRestoredEntries {
		deployment.Manifest.NotRestored.Mounts = append(deployment.Manifest.NotRestored.Mounts,
			SkippedVolume{Service: "web", Replica: i + 1, Target: "/cache", Reason: SkipReasonExcluded,
				Detail: strings.Repeat("d", 1000)})
	}
	deployment.Manifest.LeftOut = DeploymentCounts{Volumes: maxLabelNotRestoredEntries}

	// Act
	parsed, ok, err := ParseSavepointDeployment(deployment.Labels())

	// Assert
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, DeploymentNotRestored{Truncated: true}, parsed.Manifest.NotRestored)
	assert.Equal(t, deployment.Manifest.Services, parsed.Manifest.Services, "the replicas stay whatever is dropped")
	assert.Equal(t, deployment.Manifest.LeftOut, parsed.Manifest.LeftOut, "and so do the counts of what is")
}

func TestSavepointDeployment_Labels_ServicesPastTheBound(t *testing.T) {
	// Arrange: so many services with layers that only their replicas fit.
	deployment := demoDeployment()
	deployment.Manifest.Services = map[string]DeployedService{}
	for i := range 300 {
		deployment.Manifest.Services[fmt.Sprintf("service-with-a-long-name-%03d", i)] = DeployedService{
			Replicas: []int{1, 2},
			Layers:   []int{1, 2},
		}
	}

	// Act
	labels := deployment.Labels()
	parsed, ok, err := ParseSavepointDeployment(labels)

	// Assert
	require.NoError(t, err)
	require.True(t, ok)
	assert.LessOrEqual(t, len(labels[string(SystemLabelDeploymentManifest)]), maxLabelManifestBytes)
	assert.True(t, parsed.Manifest.NotRestored.Truncated)
	for name, service := range parsed.Manifest.Services {
		assert.Equal(t, DeployedService{Replicas: []int{1, 2}}, service, "%s keeps its replicas alone", name)
	}
	assert.Len(t, parsed.Manifest.Services, 300)
}

func TestParseSavepointDeployment(t *testing.T) {
	tests := []struct {
		name    string
		labels  map[string]string
		wantErr string
	}{
		{
			name:   "no labels are no deployment",
			labels: map[string]string{"com.docker.compose.service": "db"},
		},
		{
			name:   "blank labels are no deployment",
			labels: NewBlankDeploymentLabels(),
		},
		{
			name:    "a broken id",
			labels:  withLabel(SystemLabelDeploymentID, "not-a-uuid"),
			wantErr: "invalid deployment id",
		},
		{
			name:    "a broken session",
			labels:  withLabel(SystemLabelDeploymentSession, "nope"),
			wantErr: string(SystemLabelDeploymentSession),
		},
		{
			name:    "a broken time",
			labels:  withLabel(SystemLabelDeploymentAt, "yesterday"),
			wantErr: string(SystemLabelDeploymentAt),
		},
		{
			name:    "an unknown origin",
			labels:  withLabel(SystemLabelDeploymentOrigin, "deploy"),
			wantErr: "unknown origin",
		},
		{
			name:    "a revert naming no rollback",
			labels:  withLabel(SystemLabelDeploymentOrigin, string(DeploymentOriginRevert)),
			wantErr: string(SystemLabelDeploymentRevertedFrom),
		},
		{
			name:    "a broken arcave project",
			labels:  withLabel(SystemLabelDeploymentArcaveProject, "../x"),
			wantErr: string(SystemLabelDeploymentArcaveProject),
		},
		{
			name:    "a broken savepoint",
			labels:  withLabel(SystemLabelDeploymentSavepoint, "nope"),
			wantErr: string(SystemLabelDeploymentSavepoint),
		},
		{
			name:    "a broken savepoint time",
			labels:  withLabel(SystemLabelDeploymentSavepointCreatedAt, "yesterday"),
			wantErr: string(SystemLabelDeploymentSavepointCreatedAt),
		},
		{
			name:    "a broken safety snapshot's savepoint",
			labels:  withLabel(SystemLabelDeploymentSafetySnapshotFor, "nope"),
			wantErr: string(SystemLabelDeploymentSafetySnapshotFor),
		},
		{
			name:    "a broken manifest",
			labels:  withLabel(SystemLabelDeploymentManifest, "{"),
			wantErr: string(SystemLabelDeploymentManifest),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			_, ok, err := ParseSavepointDeployment(tt.labels)

			// Assert
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.False(t, ok)
		})
	}
}

// withLabel is demoDeployment's labels with key set to value.
func withLabel(key SystemLabel, value string) map[string]string {
	labels := demoDeployment().Labels()
	labels[string(key)] = value
	return labels
}
