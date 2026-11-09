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
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"
)

// errLabelBlank refuses a blank label a deployment cannot do without.
var errLabelBlank = errors.New("must not be blank")

// DeployedSavepoint is what a deployment says of the savepoint it was built from, as its record
// read when it was built.
type DeployedSavepoint struct {
	ID               SavepointID `json:"id"`
	Name             string      `json:"name,omitempty"`
	CreatedAt        time.Time   `json:"created_at"`
	IsSafetySnapshot bool        `json:"safety_snapshot"`
}

// String names the savepoint by its name and id, by its id when it has no name.
func (m DeployedSavepoint) String() string {
	if m.Name == "" {
		return m.ID.String()
	}
	return m.Name + " (" + m.ID.String() + ")"
}

// SavepointDeployment is one build of a compose project from a savepoint, as the labels of every
// container it built spell it: what a status reads with no store.
type SavepointDeployment struct {
	DeployedAt time.Time
	// ArcaveProject is the arcave project whose store holds Session.
	ArcaveProject ArcaveProjectName
	Session       SessionRef
	Savepoint     DeployedSavepoint
	// ServiceReplicas are the replica numbers each service was built with, a gap included.
	ServiceReplicas map[string][]int
}

// Labels spells the deployment out as labels, every key present.
func (m SavepointDeployment) Labels() map[string]string {
	return map[string]string{
		string(SystemLabelDeploymentDeployedAt):                formatLabelTime(m.DeployedAt),
		string(SystemLabelDeploymentArcaveProject):             m.ArcaveProject.String(),
		string(SystemLabelDeploymentSessionID):                 m.Session.ID.String(),
		string(SystemLabelDeploymentSessionName):               m.Session.Name,
		string(SystemLabelDeploymentSavepointID):               m.Savepoint.ID.String(),
		string(SystemLabelDeploymentSavepointName):             m.Savepoint.Name,
		string(SystemLabelDeploymentSavepointCreatedAt):        formatLabelTime(m.Savepoint.CreatedAt),
		string(SystemLabelDeploymentSavepointIsSafetySnapshot): strconv.FormatBool(m.Savepoint.IsSafetySnapshot),
		string(SystemLabelDeploymentServiceReplicas):           formatServiceReplicas(m.ServiceReplicas),
	}
}

// NewBlankDeploymentLabels is every deployment key blank: what a committed layer carries, so a
// container created from it outside arcave names no deployment.
func NewBlankDeploymentLabels() map[string]string {
	labels := make(map[string]string, len(deploymentLabels))
	for _, key := range deploymentLabels {
		labels[string(key)] = ""
	}
	return labels
}

// ParseSavepointDeployment reads the deployment the labels name; ok is false when they name no
// savepoint. Blank names and replicas are allowed, any other blank label is an error.
func ParseSavepointDeployment(labels map[string]string) (SavepointDeployment, bool, error) {
	get := func(key SystemLabel) string { return labels[string(key)] }
	if get(SystemLabelDeploymentSavepointID) == "" {
		return SavepointDeployment{}, false, nil
	}
	var (
		savepointID      = SavepointID(get(SystemLabelDeploymentSavepointID))
		deployedAt       time.Time
		arcaveProject    = ArcaveProjectName(get(SystemLabelDeploymentArcaveProject))
		createdAt        time.Time
		session          SessionID
		isSafetySnapshot bool
		replicas         map[string][]int
		err              error
	)
	if err = savepointID.Validate(); err != nil {
		return SavepointDeployment{}, false, newLabelError(SystemLabelDeploymentSavepointID, err)
	}
	if deployedAt, err = parseLabelTime(get(SystemLabelDeploymentDeployedAt)); err != nil {
		return SavepointDeployment{}, false, newLabelError(SystemLabelDeploymentDeployedAt, err)
	}
	if err = arcaveProject.Validate(); err != nil {
		return SavepointDeployment{}, false, newLabelError(SystemLabelDeploymentArcaveProject, err)
	}
	if session, err = ParseSessionID(get(SystemLabelDeploymentSessionID)); err != nil {
		return SavepointDeployment{}, false, newLabelError(SystemLabelDeploymentSessionID, err)
	}
	if createdAt, err = parseLabelTime(get(SystemLabelDeploymentSavepointCreatedAt)); err != nil {
		return SavepointDeployment{}, false, newLabelError(SystemLabelDeploymentSavepointCreatedAt, err)
	}
	if isSafetySnapshot, err = strconv.ParseBool(get(SystemLabelDeploymentSavepointIsSafetySnapshot)); err != nil {
		return SavepointDeployment{}, false, newLabelError(SystemLabelDeploymentSavepointIsSafetySnapshot, err)
	}
	if replicas, err = parseServiceReplicas(get(SystemLabelDeploymentServiceReplicas)); err != nil {
		return SavepointDeployment{}, false, newLabelError(SystemLabelDeploymentServiceReplicas, err)
	}
	return SavepointDeployment{
		DeployedAt:    deployedAt,
		ArcaveProject: arcaveProject,
		Session: SessionRef{
			ID:   session,
			Name: get(SystemLabelDeploymentSessionName),
		},
		Savepoint: DeployedSavepoint{
			ID:               savepointID,
			Name:             get(SystemLabelDeploymentSavepointName),
			CreatedAt:        createdAt,
			IsSafetySnapshot: isSafetySnapshot,
		},
		ServiceReplicas: replicas,
	}, true, nil
}

func newLabelError(key SystemLabel, err error) error {
	return fmt.Errorf("%s: %w", key, err)
}

func formatLabelTime(t time.Time) string {
	return t.UTC().Format(time.RFC3339Nano)
}

func parseLabelTime(value string) (time.Time, error) {
	if value == "" {
		return time.Time{}, errLabelBlank
	}
	return time.Parse(time.RFC3339Nano, value)
}

// formatServiceReplicas spells replicas as db=1,3;web=1, services by name, a service with none left
// out; a compose service name holds none of "=;,".
func formatServiceReplicas(replicas map[string][]int) string {
	entries := make([]string, 0, len(replicas))
	for _, service := range slices.Sorted(maps.Keys(replicas)) {
		if len(replicas[service]) == 0 {
			continue
		}
		numbers := make([]string, 0, len(replicas[service]))
		for _, number := range replicas[service] {
			numbers = append(numbers, strconv.Itoa(number))
		}
		entries = append(entries, service+"="+strings.Join(numbers, ","))
	}
	return strings.Join(entries, ";")
}

// parseServiceReplicas reads what formatServiceReplicas spells; nil for blank, a deployment of no
// service.
func parseServiceReplicas(value string) (map[string][]int, error) {
	if value == "" {
		return nil, nil
	}
	replicas := map[string][]int{}
	for entry := range strings.SplitSeq(value, ";") {
		service, list, found := strings.Cut(entry, "=")
		if !found || service == "" {
			return nil, fmt.Errorf("entry %q: want <service>=<replica>[,<replica>...]", entry)
		}
		if _, taken := replicas[service]; taken {
			return nil, fmt.Errorf("service %q named twice", service)
		}
		numbers := []int{}
		for field := range strings.SplitSeq(list, ",") {
			number, err := strconv.Atoi(field)
			if err != nil || number < 1 || strconv.Itoa(number) != field {
				return nil, fmt.Errorf("service %q: replica %q is no positive number", service, field)
			}
			if slices.Contains(numbers, number) {
				return nil, fmt.Errorf("service %q: replica %d named twice", service, number)
			}
			numbers = append(numbers, number)
		}
		replicas[service] = numbers
	}
	return replicas, nil
}
