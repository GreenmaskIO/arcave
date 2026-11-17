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
	"github.com/greenmaskio/arcave/internal/model"
)

// LayerOrigin is the arcave project and compose project a writable layer was committed under.
type LayerOrigin struct {
	ArcaveProject  model.ArcaveProjectName
	ComposeProject model.DockerComposeProjectName
}

// Labels spells the origin out as the labels a commit stamps.
func (m LayerOrigin) Labels() map[string]string {
	return map[string]string{
		string(model.SystemLabelArcaveProject):  m.ArcaveProject.String(),
		string(model.SystemLabelComposeProject): m.ComposeProject.String(),
	}
}

// ParseLayerOrigin reads the origin an image's labels name; ok is false unless both are set.
func ParseLayerOrigin(labels map[string]string) (LayerOrigin, bool) {
	origin := LayerOrigin{
		ArcaveProject:  model.ArcaveProjectName(labels[string(model.SystemLabelArcaveProject)]),
		ComposeProject: model.DockerComposeProjectName(labels[string(model.SystemLabelComposeProject)]),
	}
	if origin.ArcaveProject == "" || origin.ComposeProject == "" {
		return LayerOrigin{}, false
	}
	return origin, true
}

// ContainerRef names one replica of one service. Replica is
// com.docker.compose.container-number, counted from 1.
type ContainerRef struct {
	Service string // compose service name
	Replica int    // replica number, from 1
	ID      string // container id
	Name    string // container name, without the leading slash
	// The upstream: the image the compose file named; empty when it can no longer
	// be traced.
	UpstreamImageID  model.ImageDigestID // sha256 digest of the upstream's config
	UpstreamImageRef string              // the upstream's name, e.g. postgres:16; never a digest
}

// WritableLayerCaptureRequest is one commit-and-store, addressed by savepoint and container.
type WritableLayerCaptureRequest struct {
	SnapshotID model.SavepointID
	Container  ContainerRef
	Backend    model.ImageBackendKind
}

// WritableLayerRestoreRequest is one load-and-tag, addressed by the savepoint that holds
// the layer. Tag is the docker reference the committed image is to answer to in the
// local daemon.
type WritableLayerRestoreRequest struct {
	SnapshotID model.SavepointID
	Layer      model.WritableLayer
	Tag        string
}

// ReplicaIdentity is who a rebuilt container is to be: the name and the
// com.docker.compose.container-number a savepoint recorded for it.
type ReplicaIdentity struct {
	Name   string
	Number int
}
