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
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

// The bounds the deployment labels keep to: every container of a deployment carries them, and
// `docker ps --format '{{.Labels}}'` prints them whole.
const (
	maxLabelDescriptionRunes   = 256
	maxLabelNotRestoredEntries = 32
	maxLabelManifestBytes      = 16 << 10
)

// DeploymentID names one build of a compose project from a savepoint: a UUIDv7, fresh on every
// rollback and revert, so the containers of two rollbacks to one savepoint tell apart.
type DeploymentID string

// NewDeploymentID returns a fresh UUIDv7; a later deployment's id sorts after an earlier one's.
func NewDeploymentID() DeploymentID {
	return DeploymentID(uuid.Must(uuid.NewV7()).String())
}

// Validate accepts a canonical UUID only.
func (m DeploymentID) Validate() error {
	parsed, err := uuid.Parse(string(m))
	if err != nil || parsed.String() != string(m) {
		return fmt.Errorf("invalid deployment id %q: must be a canonical UUID", string(m))
	}
	return nil
}

func (m DeploymentID) String() string { return string(m) }

// DeploymentOrigin says what built a deployment.
type DeploymentOrigin string

const (
	// DeploymentOriginRollback is a rollback to the savepoint deployed.
	DeploymentOriginRollback DeploymentOrigin = "rollback"
	// DeploymentOriginRevert is a failed rollback putting its safety snapshot back.
	DeploymentOriginRevert DeploymentOrigin = "revert"
)

// SessionRef names a session by id and by the name it carried.
type SessionRef struct {
	ID   SessionID `json:"id"`
	Name string    `json:"name"`
}

// String names the session by its name, by its id when it has none.
func (m SessionRef) String() string {
	if m.Name != "" {
		return m.Name
	}
	return m.ID.String()
}

// SavepointRef names a savepoint by id and by the name it carried, if any.
type SavepointRef struct {
	ID   SavepointID `json:"id"`
	Name string      `json:"name,omitempty"`
}

// DeployedSavepoint is what a deployment says of the savepoint it was built from, as the record
// read when it was built.
type DeployedSavepoint struct {
	ID          SavepointID
	Name        string
	Description string
	CreatedAt   time.Time
	// SafetySnapshotFor is, for a rollback's safety snapshot, the savepoint that rollback restored;
	// empty for a savepoint a user took.
	SafetySnapshotFor SavepointID
}

// IsSafetySnapshot reports whether the savepoint is a rollback's undo.
func (m DeployedSavepoint) IsSafetySnapshot() bool {
	return m.SafetySnapshotFor != ""
}

// DeploymentManifest is what a deployment built: the replicas each service runs as, and what the
// savepoint did not put back.
type DeploymentManifest struct {
	Services map[string]DeployedService `json:"services"`
	Restored DeploymentCounts           `json:"restored"`
	// LeftOut counts what NotRestored lists, whatever the labels cut of it.
	LeftOut     DeploymentCounts      `json:"left_out"`
	NotRestored DeploymentNotRestored `json:"not_restored"`
}

// DeployedService is one service of a deployment.
type DeployedService struct {
	// Replicas are the replica numbers the savepoint recorded, a gap included.
	Replicas []int `json:"replicas"`
	// Layers are the replicas a writable layer was put back into.
	Layers []int `json:"layers,omitempty"`
}

// DeploymentCounts counts the volumes and the writable layers of a deployment.
type DeploymentCounts struct {
	Volumes        int `json:"volumes"`
	WritableLayers int `json:"writable_layers"`
}

// DeploymentNotRestored is what the deployed savepoint left out, so a deployment did not put back.
type DeploymentNotRestored struct {
	Mounts         []SkippedVolume        `json:"mounts,omitempty"`
	WritableLayers []SkippedWritableLayer `json:"writable_layers,omitempty"`
	// Truncated marks lists the labels carry only part of.
	Truncated bool `json:"truncated,omitempty"`
}

// SavepointDeployment is one build of a compose project from a savepoint, as the labels of every
// container it built spell it: what a status reads with no store.
type SavepointDeployment struct {
	ID            DeploymentID
	At            time.Time
	Origin        DeploymentOrigin
	ArcaveProject ArcaveProjectName
	Session       SessionRef
	Savepoint     DeployedSavepoint
	// RevertedFrom is the savepoint a failed rollback was restoring; zero for a rollback.
	RevertedFrom  SavepointRef
	Manifest      DeploymentManifest
	ArcaveVersion string
	Host          string
}

// Labels spells the deployment out as labels: every key, blank where there is no value, so the
// labels of an earlier deployment a layer carries are all overwritten.
func (m SavepointDeployment) Labels() map[string]string {
	labels := NewBlankDeploymentLabels()
	set := func(key SystemLabel, value string) { labels[string(key)] = value }
	set(SystemLabelDeploymentID, m.ID.String())
	set(SystemLabelDeploymentAt, formatLabelTime(m.At))
	set(SystemLabelDeploymentOrigin, string(m.Origin))
	set(SystemLabelDeploymentArcaveProject, m.ArcaveProject.String())
	set(SystemLabelDeploymentSession, m.Session.ID.String())
	set(SystemLabelDeploymentSessionName, m.Session.Name)
	set(SystemLabelDeploymentSavepoint, m.Savepoint.ID.String())
	set(SystemLabelDeploymentSavepointName, m.Savepoint.Name)
	set(SystemLabelDeploymentSavepointDescription, newLabelDescription(m.Savepoint.Description))
	set(SystemLabelDeploymentSavepointCreatedAt, formatLabelTime(m.Savepoint.CreatedAt))
	set(SystemLabelDeploymentSafetySnapshotFor, m.Savepoint.SafetySnapshotFor.String())
	set(SystemLabelDeploymentRevertedFrom, m.RevertedFrom.ID.String())
	set(SystemLabelDeploymentRevertedFromName, m.RevertedFrom.Name)
	set(SystemLabelDeploymentManifest, newLabelManifest(m.Manifest))
	set(SystemLabelDeploymentArcaveVersion, m.ArcaveVersion)
	set(SystemLabelDeploymentHost, m.Host)
	return labels
}

// NewBlankDeploymentLabels are the deployment labels, every one blank: what a layer committed
// off a deployed container carries, so a container run from it reads as deployed by none.
func NewBlankDeploymentLabels() map[string]string {
	labels := make(map[string]string, len(deploymentLabels))
	for _, key := range deploymentLabels {
		labels[string(key)] = ""
	}
	return labels
}

// ParseSavepointDeployment reads the deployment labels name; ok is false when they name none.
func ParseSavepointDeployment(labels map[string]string) (SavepointDeployment, bool, error) {
	get := func(key SystemLabel) string { return labels[string(key)] }
	if get(SystemLabelDeploymentID) == "" {
		return SavepointDeployment{}, false, nil
	}
	var (
		id            = DeploymentID(get(SystemLabelDeploymentID))
		origin        = DeploymentOrigin(get(SystemLabelDeploymentOrigin))
		arcaveProject = ArcaveProjectName(get(SystemLabelDeploymentArcaveProject))
		savepointID   = SavepointID(get(SystemLabelDeploymentSavepoint))
		safetyFor     = SavepointID(get(SystemLabelDeploymentSafetySnapshotFor))
		revertedFrom  = SavepointID(get(SystemLabelDeploymentRevertedFrom))
		at, createdAt time.Time
		session       SessionID
		manifest      DeploymentManifest
		err           error
	)
	if err = id.Validate(); err != nil {
		return SavepointDeployment{}, false, err
	}
	if at, err = parseLabelTime(get(SystemLabelDeploymentAt)); err != nil {
		return SavepointDeployment{}, false, fmt.Errorf("%s: %w", SystemLabelDeploymentAt, err)
	}
	if origin != DeploymentOriginRollback && origin != DeploymentOriginRevert {
		return SavepointDeployment{}, false, fmt.Errorf("%s: unknown origin %q", SystemLabelDeploymentOrigin, origin)
	}
	if err = arcaveProject.Validate(); err != nil {
		return SavepointDeployment{}, false, fmt.Errorf("%s: %w", SystemLabelDeploymentArcaveProject, err)
	}
	if session, err = ParseSessionID(get(SystemLabelDeploymentSession)); err != nil {
		return SavepointDeployment{}, false, fmt.Errorf("%s: %w", SystemLabelDeploymentSession, err)
	}
	if err = savepointID.Validate(); err != nil {
		return SavepointDeployment{}, false, fmt.Errorf("%s: %w", SystemLabelDeploymentSavepoint, err)
	}
	if createdAt, err = parseLabelTime(get(SystemLabelDeploymentSavepointCreatedAt)); err != nil {
		return SavepointDeployment{}, false, fmt.Errorf("%s: %w", SystemLabelDeploymentSavepointCreatedAt, err)
	}
	if safetyFor != "" {
		if err = safetyFor.Validate(); err != nil {
			return SavepointDeployment{}, false, fmt.Errorf("%s: %w", SystemLabelDeploymentSafetySnapshotFor, err)
		}
	}
	if origin == DeploymentOriginRevert {
		if err = revertedFrom.Validate(); err != nil {
			return SavepointDeployment{}, false, fmt.Errorf("%s: %w", SystemLabelDeploymentRevertedFrom, err)
		}
	}
	if manifest, err = parseLabelJSON[DeploymentManifest](get(SystemLabelDeploymentManifest)); err != nil {
		return SavepointDeployment{}, false, fmt.Errorf("%s: %w", SystemLabelDeploymentManifest, err)
	}
	return SavepointDeployment{
		ID:            id,
		At:            at,
		Origin:        origin,
		ArcaveProject: arcaveProject,
		Session: SessionRef{
			ID:   session,
			Name: get(SystemLabelDeploymentSessionName),
		},
		Savepoint: DeployedSavepoint{
			ID:                savepointID,
			Name:              get(SystemLabelDeploymentSavepointName),
			Description:       get(SystemLabelDeploymentSavepointDescription),
			CreatedAt:         createdAt,
			SafetySnapshotFor: safetyFor,
		},
		RevertedFrom: SavepointRef{
			ID:   revertedFrom,
			Name: get(SystemLabelDeploymentRevertedFromName),
		},
		Manifest:      manifest,
		ArcaveVersion: get(SystemLabelDeploymentArcaveVersion),
		Host:          get(SystemLabelDeploymentHost),
	}, true, nil
}

func formatLabelTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

func parseLabelTime(value string) (time.Time, error) {
	if value == "" {
		return time.Time{}, nil
	}
	return time.Parse(time.RFC3339, value)
}

// newLabelDescription is description on one line, cut to maxLabelDescriptionRunes with an ellipsis.
func newLabelDescription(description string) string {
	line := strings.Join(strings.Fields(description), " ")
	if utf8.RuneCountInString(line) <= maxLabelDescriptionRunes {
		return line
	}
	return string([]rune(line)[:maxLabelDescriptionRunes-1]) + "…"
}

// newLabelManifest is the manifest as JSON, its lists cut to maxLabelNotRestoredEntries; past
// maxLabelManifestBytes the lists go, then each service's layers, and a cut marks them truncated.
// Each service's replicas always stay: they are what a status counts a replica missing by.
func newLabelManifest(manifest DeploymentManifest) string {
	var (
		mounts, mountsCut = cutLabelList(manifest.NotRestored.Mounts)
		layers, layersCut = cutLabelList(manifest.NotRestored.WritableLayers)
		encoded           = marshalLabelJSON(DeploymentManifest{
			Services: manifest.Services,
			Restored: manifest.Restored,
			LeftOut:  manifest.LeftOut,
			NotRestored: DeploymentNotRestored{
				Mounts:         mounts,
				WritableLayers: layers,
				Truncated:      manifest.NotRestored.Truncated || mountsCut || layersCut,
			},
		})
	)
	if len(encoded) <= maxLabelManifestBytes {
		return encoded
	}
	withoutLists := marshalLabelJSON(DeploymentManifest{
		Services:    manifest.Services,
		Restored:    manifest.Restored,
		LeftOut:     manifest.LeftOut,
		NotRestored: DeploymentNotRestored{Truncated: true},
	})
	if len(withoutLists) <= maxLabelManifestBytes {
		return withoutLists
	}
	return marshalLabelJSON(DeploymentManifest{
		Services:    newReplicasOnly(manifest.Services),
		Restored:    manifest.Restored,
		LeftOut:     manifest.LeftOut,
		NotRestored: DeploymentNotRestored{Truncated: true},
	})
}

// newReplicasOnly is services with each one's replicas alone, its layers left out.
func newReplicasOnly(services map[string]DeployedService) map[string]DeployedService {
	replicas := make(map[string]DeployedService, len(services))
	for name, service := range services {
		replicas[name] = DeployedService{Replicas: service.Replicas}
	}
	return replicas
}

// cutLabelList keeps the first maxLabelNotRestoredEntries of entries; cut reports a loss.
func cutLabelList[T any](entries []T) (_ []T, cut bool) {
	if len(entries) <= maxLabelNotRestoredEntries {
		return entries, false
	}
	return entries[:maxLabelNotRestoredEntries], true
}

// marshalLabelJSON encodes v compactly; v is a type of this file, which always encodes.
func marshalLabelJSON(v any) string {
	encoded, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("encode a deployment label: %v", err))
	}
	return string(encoded)
}

func parseLabelJSON[T any](value string) (T, error) {
	var v T
	if value == "" {
		return v, nil
	}
	err := json.Unmarshal([]byte(value), &v)
	return v, err
}
