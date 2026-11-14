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

package compose

import (
	"fmt"
	"strings"
	"time"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/image"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/service/capturepolicy"
)

// The compose project every plan test runs against: one service mounting one named volume.
const (
	demoProject = "demo"
	demoService = "db"
	demoTarget  = "/data"
)

// The one replica demoIntrospectionWithContainers reports for demoService.
const (
	demoContainerID = "c0ffee"
	demoBaseImage   = "postgres:16"
	demoUpstreamID  = model.ImageDigestID("sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc")
)

// demoCaptureDefaults are the backends every test Service captures by, every kind captured.
var demoCaptureDefaults = capturepolicy.CaptureDefaults{
	VolumeBackend:        model.SnapshotBackendFullCopy,
	WritableLayerBackend: model.ImageBackendDedupCopy,
	SavepointCapture: capturepolicy.SavepointCaptureSettings{
		WritableLayers:   true,
		Images:           true,
		NamedVolumes:     true,
		BindVolumes:      true,
		AnonymousVolumes: true,
	},
}

// demoAssembled is what the assembler builds demoIntrospection into; demoSpecBytes is its
// artifact and demoSpec what that parses back to.
var demoAssembled = &types.Project{Name: demoProject}

// demoRecord is a committed savepoint of demoVolume.
func demoRecord() model.Savepoint {
	return model.Savepoint{
		ID:        "019e843e-1650-7000-8000-000000000000",
		Name:      "sp",
		CreatedAt: time.Date(2026, 6, 12, 11, 0, 0, 0, time.UTC),
		Stats: []model.SnapshotStat{{
			Volume:    demoVolume(),
			Type:      model.SnapshotBackendFullCopy,
			SizeBytes: 4096,
			Payload:   "sp-payload",
		}},
		Spec: &model.SpecRef{Format: model.SpecFormatComposeYAML},
	}
}

// demoIntrospection is the live compose project: one service mounting the named volume, and no
// container, so no layer is captured.
func demoIntrospection() model.IntrospectionResult {
	return model.IntrospectionResult{Project: model.Project{
		Name: demoProject,
		Services: []model.Service{{
			Name: demoService,
			Volumes: []model.ServiceVolume{{
				Type: model.VolumeTypeNamed, Source: "db", Target: demoTarget, VolumeName: demoVolumeName,
			}},
		}},
		Volumes: []model.NamedVolume{{Key: "db", Name: demoVolumeName,
			UsedBy: []model.ServiceReference{{Service: demoService, Target: demoTarget}}}},
	}}
}

// demoContainerRef is demoService's one replica, as a capture request addresses it.
func demoContainerRef() model.ContainerRef {
	return model.ContainerRef{
		Service: demoService, Replica: 1, ID: demoContainerID, Name: demoContainerName(1),
		UpstreamImageID: demoUpstreamID, UpstreamImageRef: demoBaseImage,
	}
}

// demoContainerName is the name compose gives replica n of demoService.
func demoContainerName(n int) string {
	return fmt.Sprintf("%s-%s-%d", demoProject, demoService, n)
}

// demoContainerInspect is a replica running on demoBaseImage.
func demoContainerInspect() container.InspectResponse {
	return container.InspectResponse{
		Image:  demoUpstreamID.String(),
		Config: &container.Config{Image: demoBaseImage},
	}
}

// demoImageCaptureRequest is the one upstream demoIntrospectionWithContainers names.
func demoImageCaptureRequest() model.ImageCaptureRequest {
	return model.ImageCaptureRequest{
		Image: model.Image{
			ID: demoUpstreamID, Refs: []string{demoBaseImage}, RepoDigests: []string{"postgres@sha256:dd"}, SizeBytes: 1 << 20,
		},
		ExistsInDaemon: true,
	}
}

// demoIntrospectionWithContainers is demoIntrospection with the daemon holding one container
// for demoService: what a capture of layers sees.
func demoIntrospectionWithContainers() model.IntrospectionResult {
	introspection := demoIntrospection()
	introspection.Project.Services = []model.Service{{
		Name: demoService,
		Containers: []model.Container{{
			ID:      demoContainerID,
			Name:    demoContainerName(1),
			Number:  1,
			Inspect: demoContainerInspect(),
		}},
	}}
	introspection.Images = map[model.ImageDigestID]image.InspectResponse{
		demoUpstreamID: {
			ID: demoUpstreamID.String(), RepoTags: []string{demoBaseImage},
			RepoDigests: []string{"postgres@sha256:dd"}, Size: 1 << 20,
		},
	}
	return introspection
}

// labelledIntrospection is demoIntrospectionWithContainers, its one service carrying labels.
func labelledIntrospection(labels map[string]string) model.IntrospectionResult {
	introspection := demoIntrospectionWithContainers()
	introspection.Project.Services[0].Labels = labels
	return introspection
}

// assemblerFor assembles introspection into demoAssembled.
func assemblerFor(introspection model.IntrospectionResult) *mockAssembler {
	assembler := &mockAssembler{}
	assembler.On("Assemble", mock.Anything, introspection).Return(demoAssembled, nil)
	return assembler
}

// demoCodec renders demoAssembled and parses its artifact back.
func demoCodec() *mockCodec {
	codec := &mockCodec{}
	codec.On("Marshal", demoAssembled).Return(demoSpecBytes, nil)
	codec.On("Parse", mock.Anything, demoSpecBytes).Return(demoSpec, nil)
	return codec
}

// demoAssembler assembles demoIntrospection into demoAssembled.
func demoAssembler() *mockAssembler { return assemblerFor(demoIntrospection()) }

// demoRecordWithImages is demoRecord carrying the writable layer of its one replica.
func demoRecordWithImages() model.Savepoint {
	record := demoRecord()
	record.WritableLayers = []model.WritableLayer{demoWritableLayer()}
	return record
}

// demoRestoredTag is the tag a rollback of demoRecord puts on one replica's layer;
// demoBaseImage's colon is what a docker repository path cannot carry.
func demoRestoredTag(replica int) string {
	return fmt.Sprintf("arcave/%s-%s-%d-postgres-16:sp-%s", demoProject, demoService, replica, demoRecord().ID)
}

// demoWritableLayerRestoreResult is what the copier reports for demoWritableLayer.
func demoWritableLayerRestoreResult() model.WritableLayerRestoreResult {
	return model.WritableLayerRestoreResult{
		Service: demoService, Replica: 1, ContainerName: demoContainerName(1),
		ImageID: demoWritableLayer().CommittedImageID, Tag: demoRestoredTag(1),
	}
}

// The second replica of demoService, and the layer it carries of its own — what a
// compose service's one image cannot stand for.
const (
	demoSecondContainerID = "cafe"
	demoSecondImageID     = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

// demoScaledIntrospection is demoIntrospectionWithContainers with demoService
// scaled to two replicas.
func demoScaledIntrospection() model.IntrospectionResult {
	introspection := demoIntrospectionWithContainers()
	introspection.Project.Services[0].Containers = append(introspection.Project.Services[0].Containers, model.Container{
		ID:      demoSecondContainerID,
		Name:    demoContainerName(2),
		Number:  2,
		Inspect: demoContainerInspect(),
	})
	return introspection
}

// demoSecondWritableLayer is the second replica's own layer.
func demoSecondWritableLayer() model.WritableLayer {
	capture := demoWritableLayer()
	capture.Replica = 2
	capture.ContainerID = demoSecondContainerID
	capture.ContainerName = demoContainerName(2)
	capture.CommittedImageID = demoSecondImageID
	return capture
}

// demoScaledRecord is a savepoint of the scaled compose project: one capture per replica.
func demoScaledRecord() model.Savepoint {
	record := demoRecord()
	record.WritableLayers = []model.WritableLayer{demoWritableLayer(), demoSecondWritableLayer()}
	return record
}

// demoAbsentIntrospection is what the inspector reports once the compose project was
// taken down: no service, its named volume left behind.
func demoAbsentIntrospection() model.IntrospectionResult {
	introspection := demoIntrospection()
	introspection.Project.Services = nil
	introspection.Project.Volumes[0].UsedBy = nil
	return introspection
}

// defaultSessionRef is the default session as the deployment labels name it.
var defaultSessionRef = model.SessionRef{
	ID:   model.DefaultSession,
	Name: model.DefaultSessionName,
}

// deployedAs matches what deployed does, every service's labels read back into a deployment want
// accepts.
func deployedAs(spec *types.Project, want func(model.SavepointDeployment) bool) any {
	return mock.MatchedBy(func(project *types.Project) bool {
		for _, service := range project.Services {
			deployment, ok, err := model.ParseSavepointDeployment(service.Labels)
			if err != nil || !ok || !want(deployment) {
				return false
			}
		}
		return assert.ObjectsAreEqual(spec, withoutDeploymentLabels(project))
	})
}

// withoutDeploymentLabels is project with the deployment labels a rollback adds taken off again.
func withoutDeploymentLabels(project *types.Project) *types.Project {
	stripped := *project
	stripped.Services = nil
	if len(project.Services) > 0 {
		stripped.Services = make(types.Services, len(project.Services))
	}
	for name, service := range project.Services {
		labels := types.Labels{}
		for key, value := range service.Labels {
			if !strings.HasPrefix(key, model.DeploymentLabelPrefix) {
				labels[key] = value
			}
		}
		if len(labels) == 0 {
			labels = nil
		}
		service.Labels = labels
		stripped.Services[name] = service
	}
	return &stripped
}

// demoTargetSpecBytes is a savepoint's artifact and demoTargetSpec what it parses back to: a project
// distinct by identity from the live demoSpec, so a call handed the live one instead fails.
var (
	demoTargetSpec = &types.Project{Name: demoProject, Services: types.Services{
		demoService: {Name: demoService, Image: demoBaseImage},
	}}
	demoTargetSpecBytes = []byte("name: demo\nservices: {db: {image: postgres:16}}\n")
)

// demoArcaveProject is the arcave project a deployment names.
const demoArcaveProject model.ArcaveProjectName = "demo-arcave"

// demoDeployment is a build of savepoint in the default session, as core hands it.
func demoDeployment(savepoint model.Savepoint) model.SavepointDeployment {
	return model.SavepointDeployment{
		ArcaveProject: demoArcaveProject,
		Session:       defaultSessionRef,
		Savepoint: model.DeployedSavepoint{
			ID:        savepoint.ID,
			Name:      savepoint.Name,
			CreatedAt: savepoint.CreatedAt,
		},
	}
}

// deployedFrom matches spec labelled with deployment as core gave it: the arcave project, the
// session and the savepoint stamped as they were.
func deployedFrom(spec *types.Project, deployment model.SavepointDeployment) any {
	return deployedAs(spec, func(stamped model.SavepointDeployment) bool {
		return stamped.ArcaveProject == deployment.ArcaveProject && stamped.Session == deployment.Session &&
			stamped.Savepoint == deployment.Savepoint
	})
}

// demoWritableLayer is what the backend reports for demoContainerRef's layer.
func demoWritableLayer() model.WritableLayer {
	return model.WritableLayer{
		Service:          demoService,
		Replica:          1,
		ContainerID:      demoContainerID,
		ContainerName:    demoContainerName(1),
		CommittedImageID: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		ImageBackend:     model.ImageBackendDedupCopy,
		UpstreamImageID:  demoUpstreamID,
		UpstreamImageRef: demoBaseImage,
		SizeBytes:        4096,
	}
}
