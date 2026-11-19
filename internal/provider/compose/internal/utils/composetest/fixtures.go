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

package composetest

import (
	"fmt"
	"strings"
	"time"

	composemodel "github.com/greenmaskio/arcave/internal/provider/compose/internal/model"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/image"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	"github.com/greenmaskio/arcave/internal/model"
	composeconfig "github.com/greenmaskio/arcave/internal/provider/compose/config"
	"github.com/greenmaskio/arcave/internal/provider/compose/internal/service/capturepolicy"
)

// The compose project every plan test runs against: one service mounting one named volume.
const (
	DemoProject = "demo"
	DemoService = "db"
	DemoTarget  = "/data"
)

// The one replica DemoIntrospectionWithContainers reports for DemoService.
const (
	DemoContainerID = "c0ffee"
	DemoBaseImage   = "postgres:16"
	DemoUpstreamID  = composemodel.ImageDigestID("sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc")
)

// DemoCaptureDefaults are the backends every test Provider captures by, every kind captured.
var DemoCaptureDefaults = capturepolicy.CaptureDefaults{
	VolumeBackend:        composeconfig.SnapshotBackendFullCopy,
	WritableLayerBackend: composeconfig.ImageBackendDedupCopy,
	SavepointCapture: capturepolicy.SavepointCaptureSettings{
		WritableLayers:   true,
		Images:           true,
		NamedVolumes:     true,
		BindVolumes:      true,
		AnonymousVolumes: true,
	},
}

// DemoAssembled is what the assembler builds DemoIntrospection into; DemoSpecBytes is its
// artifact and DemoSpec what that parses back to.
var DemoAssembled = &types.Project{Name: DemoProject}

// DemoRecord is a committed savepoint of DemoVolume.
func DemoRecord() composemodel.Savepoint {
	return composemodel.Savepoint{
		Header: model.Savepoint{
			ID:        "019e843e-1650-7000-8000-000000000000",
			Name:      "sp",
			CreatedAt: time.Date(2026, 6, 12, 11, 0, 0, 0, time.UTC),
		},
		Detail: composemodel.SavepointDetail{
			Stats: []composemodel.SnapshotStat{{
				Volume:    DemoVolume(),
				Type:      composeconfig.SnapshotBackendFullCopy,
				SizeBytes: 4096,
				Payload:   "sp-payload",
			}},
			Spec: &composemodel.SpecRef{Format: composemodel.SpecFormatComposeYAML},
		},
	}
}

// DemoIntrospection is the live compose project: one service mounting the named volume, and no
// container, so no layer is captured.
func DemoIntrospection() composemodel.IntrospectionResult {
	return composemodel.IntrospectionResult{Project: composemodel.Project{
		Name: DemoProject,
		Services: []composemodel.Service{{
			Name: DemoService,
			Volumes: []composemodel.ServiceVolume{{
				Type: composemodel.VolumeTypeNamed, Source: "db", Target: DemoTarget, VolumeName: DemoVolumeName,
			}},
		}},
		Volumes: []composemodel.NamedVolume{{Key: "db", Name: DemoVolumeName,
			UsedBy: []composemodel.ServiceReference{{Service: DemoService, Target: DemoTarget}}}},
	}}
}

// DemoContainerRef is DemoService's one replica, as a capture request addresses it.
func DemoContainerRef() composemodel.ContainerRef {
	return composemodel.ContainerRef{
		Service: DemoService, Replica: 1, ID: DemoContainerID, Name: DemoContainerName(1),
		UpstreamImageID: DemoUpstreamID, UpstreamImageRef: DemoBaseImage,
	}
}

// DemoContainerName is the name compose gives replica n of DemoService.
func DemoContainerName(n int) string {
	return fmt.Sprintf("%s-%s-%d", DemoProject, DemoService, n)
}

// DemoContainerInspect is a replica running on DemoBaseImage.
func DemoContainerInspect() container.InspectResponse {
	return container.InspectResponse{
		Image:  DemoUpstreamID.String(),
		Config: &container.Config{Image: DemoBaseImage},
	}
}

// DemoImageCaptureRequest is the one upstream DemoIntrospectionWithContainers names.
func DemoImageCaptureRequest() composemodel.ImageCaptureRequest {
	return composemodel.ImageCaptureRequest{
		Image: composemodel.Image{
			ID: DemoUpstreamID, Refs: []string{DemoBaseImage}, RepoDigests: []string{"postgres@sha256:dd"}, SizeBytes: 1 << 20,
		},
		ExistsInDaemon: true,
	}
}

// DemoIntrospectionWithContainers is DemoIntrospection with the daemon holding one container
// for DemoService: what a capture of layers sees.
func DemoIntrospectionWithContainers() composemodel.IntrospectionResult {
	introspection := DemoIntrospection()
	introspection.Project.Services = []composemodel.Service{{
		Name: DemoService,
		Containers: []composemodel.Container{{
			ID:      DemoContainerID,
			Name:    DemoContainerName(1),
			Number:  1,
			Inspect: DemoContainerInspect(),
		}},
	}}
	introspection.Images = map[composemodel.ImageDigestID]image.InspectResponse{
		DemoUpstreamID: {
			ID: DemoUpstreamID.String(), RepoTags: []string{DemoBaseImage},
			RepoDigests: []string{"postgres@sha256:dd"}, Size: 1 << 20,
		},
	}
	return introspection
}

// LabelledIntrospection is DemoIntrospectionWithContainers, its one service carrying labels.
func LabelledIntrospection(labels map[string]string) composemodel.IntrospectionResult {
	introspection := DemoIntrospectionWithContainers()
	introspection.Project.Services[0].Labels = labels
	return introspection
}

// AssemblerFor assembles introspection into DemoAssembled.
func AssemblerFor(introspection composemodel.IntrospectionResult) *MockAssembler {
	assembler := &MockAssembler{}
	assembler.On("Assemble", mock.Anything, introspection).Return(DemoAssembled, nil)
	return assembler
}

// DemoCodec renders DemoAssembled and parses its artifact back.
func DemoCodec() *MockCodec {
	codec := &MockCodec{}
	codec.On("Marshal", DemoAssembled).Return(DemoSpecBytes, nil)
	codec.On("Parse", mock.Anything, DemoSpecBytes).Return(DemoSpec, nil)
	return codec
}

// DemoAssembler assembles DemoIntrospection into DemoAssembled.
func DemoAssembler() *MockAssembler { return AssemblerFor(DemoIntrospection()) }

// DemoRecordWithImages is DemoRecord carrying the writable layer of its one replica.
func DemoRecordWithImages() composemodel.Savepoint {
	record := DemoRecord()
	record.Detail.WritableLayers = []composemodel.WritableLayer{DemoWritableLayer()}
	return record
}

// DemoRestoredTag is the tag a rollback of DemoRecord puts on one replica's layer;
// DemoBaseImage's colon is what a docker repository path cannot carry.
func DemoRestoredTag(replica int) string {
	return fmt.Sprintf("arcave/%s-%s-%d-postgres-16:sp-%s", DemoProject, DemoService, replica, DemoRecord().Header.ID)
}

// DemoWritableLayerRestoreResult is what the copier reports for DemoWritableLayer.
func DemoWritableLayerRestoreResult() composemodel.WritableLayerRestoreResult {
	return composemodel.WritableLayerRestoreResult{
		Service: DemoService, Replica: 1, ContainerName: DemoContainerName(1),
		ImageID: DemoWritableLayer().CommittedImageID, Tag: DemoRestoredTag(1),
	}
}

// The second replica of DemoService, and the layer it carries of its own — what a
// compose service's one image cannot stand for.
const (
	DemoSecondContainerID = "cafe"
	DemoSecondImageID     = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

// DemoScaledIntrospection is DemoIntrospectionWithContainers with DemoService
// scaled to two replicas.
func DemoScaledIntrospection() composemodel.IntrospectionResult {
	introspection := DemoIntrospectionWithContainers()
	introspection.Project.Services[0].Containers = append(introspection.Project.Services[0].Containers, composemodel.Container{
		ID:      DemoSecondContainerID,
		Name:    DemoContainerName(2),
		Number:  2,
		Inspect: DemoContainerInspect(),
	})
	return introspection
}

// DemoSecondWritableLayer is the second replica's own layer.
func DemoSecondWritableLayer() composemodel.WritableLayer {
	capture := DemoWritableLayer()
	capture.Replica = 2
	capture.ContainerID = DemoSecondContainerID
	capture.ContainerName = DemoContainerName(2)
	capture.CommittedImageID = DemoSecondImageID
	return capture
}

// DemoScaledRecord is a savepoint of the scaled compose project: one capture per replica.
func DemoScaledRecord() composemodel.Savepoint {
	record := DemoRecord()
	record.Detail.WritableLayers = []composemodel.WritableLayer{DemoWritableLayer(), DemoSecondWritableLayer()}
	return record
}

// DemoAbsentIntrospection is what the inspector reports once the compose project was
// taken down: no service, its named volume left behind.
func DemoAbsentIntrospection() composemodel.IntrospectionResult {
	introspection := DemoIntrospection()
	introspection.Project.Services = nil
	introspection.Project.Volumes[0].UsedBy = nil
	return introspection
}

// DefaultSessionRef is the default session as the deployment labels name it.
var DefaultSessionRef = model.SessionRef{
	ID:   model.DefaultSession,
	Name: model.DefaultSessionName,
}

// DeployedAs matches what deployed does, every service's labels read back into a deployment want
// accepts.
func DeployedAs(spec *types.Project, want func(composemodel.Deployment) bool) any {
	return mock.MatchedBy(func(project *types.Project) bool {
		for _, service := range project.Services {
			deployment, ok, err := composemodel.ParseDeploymentLabels(service.Labels)
			if err != nil || !ok || !want(deployment) {
				return false
			}
		}
		return assert.ObjectsAreEqual(spec, WithoutDeploymentLabels(project))
	})
}

// WithoutDeploymentLabels is project with the deployment labels a rollback adds taken off again.
func WithoutDeploymentLabels(project *types.Project) *types.Project {
	stripped := *project
	stripped.Services = nil
	if len(project.Services) > 0 {
		stripped.Services = make(types.Services, len(project.Services))
	}
	for name, service := range project.Services {
		labels := types.Labels{}
		for key, value := range service.Labels {
			if !strings.HasPrefix(key, composemodel.DeploymentLabelPrefix) {
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

// DemoTargetSpecBytes is a savepoint's artifact and DemoTargetSpec what it parses back to: a project
// distinct by identity from the live DemoSpec, so a call handed the live one instead fails.
var (
	DemoTargetSpec = &types.Project{Name: DemoProject, Services: types.Services{
		DemoService: {Name: DemoService, Image: DemoBaseImage},
	}}
	DemoTargetSpecBytes = []byte("name: demo\nservices: {db: {image: postgres:16}}\n")
)

// DemoArcaveProject is the arcave project a deployment names.
const DemoArcaveProject model.ArcaveProjectName = "demo-arcave"

// DemoDeployment is a build of savepoint in the default session, as core hands it.
func DemoDeployment(savepoint composemodel.Savepoint) model.SavepointDeployment {
	return model.SavepointDeployment{
		ArcaveProject: DemoArcaveProject,
		Session:       DefaultSessionRef,
		Savepoint: model.DeployedSavepoint{
			ID:        savepoint.Header.ID,
			Name:      savepoint.Header.Name,
			CreatedAt: savepoint.Header.CreatedAt,
		},
	}
}

// DeployedFrom matches spec labelled with deployment as core gave it: the arcave project, the
// session and the savepoint stamped as they were.
func DeployedFrom(spec *types.Project, deployment model.SavepointDeployment) any {
	return DeployedAs(spec, func(stamped composemodel.Deployment) bool {
		return stamped.ArcaveProject == deployment.ArcaveProject && stamped.Session == deployment.Session &&
			stamped.Savepoint == deployment.Savepoint
	})
}

// DemoWritableLayer is what the backend reports for DemoContainerRef's layer.
func DemoWritableLayer() composemodel.WritableLayer {
	return composemodel.WritableLayer{
		Service:          DemoService,
		Replica:          1,
		ContainerID:      DemoContainerID,
		ContainerName:    DemoContainerName(1),
		CommittedImageID: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		ImageBackend:     composeconfig.ImageBackendDedupCopy,
		UpstreamImageID:  DemoUpstreamID,
		UpstreamImageRef: DemoBaseImage,
		SizeBytes:        4096,
	}
}

// MatchDeploymentOf matches target with its every service labelled with a deployment, and its
// volumes and networks with none: their labels are in compose's config hash.
func MatchDeploymentOf(target *types.Project) func(*types.Project) bool {
	return func(project *types.Project) bool {
		for _, volume := range project.Volumes {
			if hasDeploymentLabel(volume.Labels) {
				return false
			}
		}
		for _, network := range project.Networks {
			if hasDeploymentLabel(network.Labels) {
				return false
			}
		}
		return len(project.Services) == len(target.Services) &&
			assert.ObjectsAreEqual(target, WithoutDeploymentLabels(project))
	}
}

func hasDeploymentLabel(labels types.Labels) bool {
	for key := range labels {
		if strings.HasPrefix(key, composemodel.DeploymentLabelPrefix) {
			return true
		}
	}
	return false
}
