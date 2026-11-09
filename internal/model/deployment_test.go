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
	"maps"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func demoDeployment() SavepointDeployment {
	return SavepointDeployment{
		DeployedAt:    time.Date(2026, 11, 7, 14, 2, 11, 0, time.UTC),
		ArcaveProject: "demo",
		Session: SessionRef{
			ID:   DefaultSession,
			Name: DefaultSessionName,
		},
		Savepoint: DeployedSavepoint{
			ID:        "019e843e-1650-7000-8000-000000000000",
			Name:      "before-migration",
			CreatedAt: time.Date(2026, 11, 6, 19, 41, 0, 0, time.UTC),
		},
		ServiceReplicas: map[string][]int{
			"db":      {1, 3},
			"migrate": {1},
		},
	}
}

func TestSavepointDeployment_Labels(t *testing.T) {
	safetySnapshot := demoDeployment()
	safetySnapshot.Savepoint.Name = "safety-snapshot-019e843e-1650-7000-8000-000000000000"
	safetySnapshot.Savepoint.IsSafetySnapshot = true
	tests := []struct {
		name       string
		deployment SavepointDeployment
		want       map[string]string
	}{
		{
			name:       "a savepoint a user took",
			deployment: demoDeployment(),
			want: map[string]string{
				"io.arcave.deployment.deployed-at":                  "2026-11-07T14:02:11Z",
				"io.arcave.deployment.arcave-project":               "demo",
				"io.arcave.deployment.session-id":                   DefaultSession.String(),
				"io.arcave.deployment.session-name":                 DefaultSessionName,
				"io.arcave.deployment.savepoint-id":                 "019e843e-1650-7000-8000-000000000000",
				"io.arcave.deployment.savepoint-name":               "before-migration",
				"io.arcave.deployment.savepoint-created-at":         "2026-11-06T19:41:00Z",
				"io.arcave.deployment.savepoint-is-safety-snapshot": "",
				"io.arcave.deployment.service-replicas":             "db=1,3;migrate=1",
			},
		},
		{
			name:       "a safety snapshot",
			deployment: safetySnapshot,
			want: map[string]string{
				"io.arcave.deployment.deployed-at":                  "2026-11-07T14:02:11Z",
				"io.arcave.deployment.arcave-project":               "demo",
				"io.arcave.deployment.session-id":                   DefaultSession.String(),
				"io.arcave.deployment.session-name":                 DefaultSessionName,
				"io.arcave.deployment.savepoint-id":                 "019e843e-1650-7000-8000-000000000000",
				"io.arcave.deployment.savepoint-name":               "safety-snapshot-019e843e-1650-7000-8000-000000000000",
				"io.arcave.deployment.savepoint-created-at":         "2026-11-06T19:41:00Z",
				"io.arcave.deployment.savepoint-is-safety-snapshot": "true",
				"io.arcave.deployment.service-replicas":             "db=1,3;migrate=1",
			},
		},
		{
			name: "no names and no replicas",
			deployment: SavepointDeployment{
				DeployedAt:    time.Date(2026, 11, 7, 14, 2, 11, 0, time.UTC),
				ArcaveProject: "demo",
				Session:       SessionRef{ID: DefaultSession},
				Savepoint: DeployedSavepoint{
					ID:        "019e843e-1650-7000-8000-000000000000",
					CreatedAt: time.Date(2026, 11, 6, 19, 41, 0, 0, time.UTC),
				},
			},
			want: map[string]string{
				"io.arcave.deployment.deployed-at":                  "2026-11-07T14:02:11Z",
				"io.arcave.deployment.arcave-project":               "demo",
				"io.arcave.deployment.session-id":                   DefaultSession.String(),
				"io.arcave.deployment.session-name":                 "",
				"io.arcave.deployment.savepoint-id":                 "019e843e-1650-7000-8000-000000000000",
				"io.arcave.deployment.savepoint-name":               "",
				"io.arcave.deployment.savepoint-created-at":         "2026-11-06T19:41:00Z",
				"io.arcave.deployment.savepoint-is-safety-snapshot": "",
				"io.arcave.deployment.service-replicas":             "",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			labels := tt.deployment.Labels()
			parsed, ok, err := ParseSavepointDeployment(labels)

			// Assert
			assert.Equal(t, tt.want, labels, "every key is written, blank or not")
			require.NoError(t, err)
			require.True(t, ok)
			assert.Equal(t, tt.deployment, parsed, "the labels read back as they were written")
		})
	}
}

func TestSavepointDeployment_Labels_SubSecond(t *testing.T) {
	// Arrange
	deployment := demoDeployment()
	deployment.DeployedAt = deployment.DeployedAt.Add(250 * time.Millisecond)

	// Act
	labels := deployment.Labels()
	parsed, ok, err := ParseSavepointDeployment(labels)

	// Assert
	assert.Equal(t, "2026-11-07T14:02:11.25Z", labels[string(SystemLabelDeploymentDeployedAt)])
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, deployment.DeployedAt, parsed.DeployedAt, "two builds within one second are told apart")
}

func TestNewBlankDeploymentLabels(t *testing.T) {
	// Act
	labels := NewBlankDeploymentLabels()
	_, ok, err := ParseSavepointDeployment(labels)

	// Assert
	assert.Equal(t, slices.Sorted(maps.Keys(demoDeployment().Labels())), slices.Sorted(maps.Keys(labels)),
		"every key a deployment writes")
	for key, value := range labels {
		assert.Empty(t, value, key)
	}
	require.NoError(t, err)
	assert.False(t, ok, "blank labels name no deployment")
}

func TestParseSavepointDeployment(t *testing.T) {
	blank := demoDeployment().Labels()
	for key := range blank {
		blank[key] = ""
	}
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
			labels: blank,
		},
		{
			name:    "a broken savepoint",
			labels:  withLabel(SystemLabelDeploymentSavepointID, "nope"),
			wantErr: string(SystemLabelDeploymentSavepointID),
		},
		{
			name:    "a broken arcave project",
			labels:  withLabel(SystemLabelDeploymentArcaveProject, ""),
			wantErr: string(SystemLabelDeploymentArcaveProject),
		},
		{
			name:    "a broken session",
			labels:  withLabel(SystemLabelDeploymentSessionID, "nope"),
			wantErr: string(SystemLabelDeploymentSessionID),
		},
		{
			name:    "a broken time",
			labels:  withLabel(SystemLabelDeploymentDeployedAt, "yesterday"),
			wantErr: string(SystemLabelDeploymentDeployedAt),
		},
		{
			name:    "no time",
			labels:  withLabel(SystemLabelDeploymentDeployedAt, ""),
			wantErr: string(SystemLabelDeploymentDeployedAt),
		},
		{
			name:    "no savepoint time",
			labels:  withLabel(SystemLabelDeploymentSavepointCreatedAt, ""),
			wantErr: string(SystemLabelDeploymentSavepointCreatedAt),
		},
		{
			name:    "a broken savepoint time",
			labels:  withLabel(SystemLabelDeploymentSavepointCreatedAt, "yesterday"),
			wantErr: string(SystemLabelDeploymentSavepointCreatedAt),
		},
		{
			name:    "a broken flag",
			labels:  withLabel(SystemLabelDeploymentSavepointIsSafetySnapshot, "yes"),
			wantErr: string(SystemLabelDeploymentSavepointIsSafetySnapshot),
		},
		{
			name:    "broken replicas",
			labels:  withLabel(SystemLabelDeploymentServiceReplicas, "db"),
			wantErr: string(SystemLabelDeploymentServiceReplicas),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			_, ok, err := ParseSavepointDeployment(tt.labels)

			// Assert
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
			} else {
				require.NoError(t, err)
			}
			assert.False(t, ok)
		})
	}
}

func TestParseServiceReplicas(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		want    map[string][]int
		wantErr string
	}{
		{name: "blank is none"},
		{name: "one service", value: "db=1", want: map[string][]int{"db": {1}}},
		{name: "a gap", value: "db=1,3;web=2", want: map[string][]int{"db": {1, 3}, "web": {2}}},
		{name: "a service with none", value: "db=", wantErr: "no positive number"},
		{name: "no number", value: "db", wantErr: "want <service>="},
		{name: "no service", value: "=1", wantErr: "want <service>="},
		{name: "no positive number", value: "db=0", wantErr: "no positive number"},
		{name: "not a number", value: "db=one", wantErr: "no positive number"},
		{name: "a service named twice", value: "db=1;db=2", wantErr: "named twice"},
		{name: "a replica named twice", value: "db=1,1", wantErr: "named twice"},
		{name: "a number spelled otherwise than it is written", value: "db=01", wantErr: "no positive number"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			got, err := parseServiceReplicas(tt.value)

			// Assert
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

// withLabel is demoDeployment's labels with key set to value.
func withLabel(key SystemLabel, value string) map[string]string {
	labels := demoDeployment().Labels()
	labels[string(key)] = value
	return labels
}
