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

package inflightmarker

import (
	"errors"
	"fmt"
	"time"

	"github.com/greenmaskio/arcave/internal/model"
	composemodel "github.com/greenmaskio/arcave/internal/provider/compose/internal/model"
)

// labelPrefix opens every label of an inflight marker's volume.
const labelPrefix = composemodel.SystemLabelPrefix + "inflight."

// The labels of an inflight marker's volume: the arcave project it is looked up by, and the
// marker spelled out.
const (
	labelArcaveProject  composemodel.SystemLabel = labelPrefix + "arcave-project"
	labelComposeProject composemodel.SystemLabel = labelPrefix + "compose-project"
	labelSession        composemodel.SystemLabel = labelPrefix + "session"
	labelTarget         composemodel.SystemLabel = labelPrefix + "target"
	labelSafety         composemodel.SystemLabel = labelPrefix + "safety"
	labelPhase          composemodel.SystemLabel = labelPrefix + "phase"
	labelAt             composemodel.SystemLabel = labelPrefix + "at"
	// labelDescription says what the volume is to whoever finds it on the daemon.
	labelDescription composemodel.SystemLabel = labelPrefix + "description"
)

const (
	volumeNamePrefix = "arcave-inflight-"
	// maxVolumeNameProject caps the compose project in a marker volume's name, which the
	// daemon holds to a file name's length; the label keeps it whole.
	maxVolumeNameProject = 64
)

var errLabelBlank = errors.New("blank")

// inflightMarkerVolume is an inflight marker as the volume that holds it on the daemon.
type inflightMarkerVolume struct {
	ArcaveProject model.ArcaveProjectName
	Marker        model.InflightMarker
}

// Labels spells the marker out as its volume's labels.
func (m inflightMarkerVolume) Labels() map[string]string {
	return map[string]string{
		string(labelArcaveProject):  m.ArcaveProject.String(),
		string(labelComposeProject): m.Marker.ComposeProject.String(),
		string(labelSession):        m.Marker.Session.String(),
		string(labelTarget):         m.Marker.Target.String(),
		string(labelSafety):         m.Marker.Safety.String(),
		string(labelPhase):          string(m.Marker.Phase),
		string(labelAt):             m.Marker.At.UTC().Format(time.RFC3339Nano),
		string(labelDescription): fmt.Sprintf("arcave service volume, holds no data: it records "+
			"a running or interrupted rollback of compose project %s. Removing it only makes arcave forget "+
			"that the rollback was interrupted.", m.Marker.ComposeProject),
	}
}

// newInflightMarkerVolumeName is the name of a marker volume of composeProject; suffix tells one
// write from the next, since a volume's labels never change.
func newInflightMarkerVolumeName(composeProject model.DockerComposeProjectName, suffix string) string {
	project := composeProject.String()
	if len(project) > maxVolumeNameProject {
		project = project[:maxVolumeNameProject]
	}
	return volumeNamePrefix + project + "-" + suffix
}

// parseInflightMarkerVolume reads the marker a volume's labels spell out; every label but the
// safety snapshot and the description is required.
func parseInflightMarkerVolume(labels map[string]string) (inflightMarkerVolume, error) {
	get := func(key composemodel.SystemLabel) string { return labels[string(key)] }
	var (
		arcaveProject  = model.ArcaveProjectName(get(labelArcaveProject))
		composeProject = model.DockerComposeProjectName(get(labelComposeProject))
		target         = model.SavepointID(get(labelTarget))
		safety         = model.SavepointID(get(labelSafety))
		phase          = model.InflightPhase(get(labelPhase))
		session        model.SessionID
		at             time.Time
		err            error
	)
	if err = arcaveProject.Validate(); err != nil {
		return inflightMarkerVolume{}, newLabelError(labelArcaveProject, err)
	}
	if composeProject == "" {
		return inflightMarkerVolume{}, newLabelError(labelComposeProject, errLabelBlank)
	}
	if session, err = model.ParseSessionID(get(labelSession)); err != nil {
		return inflightMarkerVolume{}, newLabelError(labelSession, err)
	}
	if err = target.Validate(); err != nil {
		return inflightMarkerVolume{}, newLabelError(labelTarget, err)
	}
	if safety != "" {
		if err = safety.Validate(); err != nil {
			return inflightMarkerVolume{}, newLabelError(labelSafety, err)
		}
	}
	if err = validateInflightPhase(phase); err != nil {
		return inflightMarkerVolume{}, newLabelError(labelPhase, err)
	}
	if at, err = parseLabelTime(get(labelAt)); err != nil {
		return inflightMarkerVolume{}, newLabelError(labelAt, err)
	}
	return inflightMarkerVolume{
		ArcaveProject: arcaveProject,
		Marker: model.InflightMarker{
			Phase:          phase,
			Session:        session,
			ComposeProject: composeProject,
			Target:         target,
			Safety:         safety,
			At:             at,
		},
	}, nil
}

// validateInflightPhase refuses a phase no rollback writes.
func validateInflightPhase(phase model.InflightPhase) error {
	switch phase {
	case model.InflightRecreating, model.InflightRestoring, model.InflightRestoreDone, model.InflightInconsistent:
		return nil
	case "":
		return errLabelBlank
	default:
		return fmt.Errorf("unknown phase %q", phase)
	}
}

func parseLabelTime(value string) (time.Time, error) {
	if value == "" {
		return time.Time{}, errLabelBlank
	}
	return time.Parse(time.RFC3339Nano, value)
}

func newLabelError(key composemodel.SystemLabel, err error) error {
	return fmt.Errorf("%s: %w", key, err)
}
