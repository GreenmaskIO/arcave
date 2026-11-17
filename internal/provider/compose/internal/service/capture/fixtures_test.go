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

package capture

import (
	"fmt"

	"github.com/stretchr/testify/mock"

	"github.com/greenmaskio/arcave/internal/model"
	"github.com/greenmaskio/arcave/internal/provider/compose/utils/composetest"
)

// The compose project every capture test runs against: one service with one replica.
const (
	demoProject     = "demo"
	demoService     = "db"
	demoVolumeName  = "demo_db"
	demoContainerID = "c0ffee"
	demoBaseImage   = "postgres:16"
	demoUpstreamID  = model.ImageDigestID("sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc")
)

// demoSpecBytes is the artifact every capture test writes.
var demoSpecBytes = []byte("name: demo\n")

// demoVolume is the unit, in the form the mock tests address it by.
func demoVolume() model.Volume {
	return model.Volume{
		ObjectID: "volume-named-" + demoVolumeName,
		Backend:  model.SnapshotBackendFullCopy,
		Named:    &model.VolumePropertiesNamed{VolumeName: demoVolumeName},
	}
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

// demoImageCaptureRequest is the one upstream demoContainerRef runs on.
func demoImageCaptureRequest() model.ImageCaptureRequest {
	return model.ImageCaptureRequest{
		Image: model.Image{
			ID: demoUpstreamID, Refs: []string{demoBaseImage}, RepoDigests: []string{"postgres@sha256:dd"}, SizeBytes: 1 << 20,
		},
		ExistsInDaemon: true,
	}
}

// demoImage is what the backend reports for demoImageCaptureRequest.
func demoImage() model.Image {
	image := demoImageCaptureRequest().Image
	image.Stored = true
	image.StoredBytes = 512
	return image
}

// expectSpecCommit declares the artifact a capture writes before its record, under an id
// off the clock that passes the gate.
func expectSpecCommit(repo *composetest.MockSavepointRepo) *mock.Call {
	validID := mock.MatchedBy(func(id model.SavepointID) bool { return id.Validate() == nil })
	return repo.On("SaveSpec", mock.Anything, validID, demoSpecBytes).Return(nil).Once()
}
