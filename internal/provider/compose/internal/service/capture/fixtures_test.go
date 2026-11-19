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
	"github.com/stretchr/testify/mock"

	"github.com/greenmaskio/arcave/internal/model"
	composeconfig "github.com/greenmaskio/arcave/internal/provider/compose/config"
	composemodel "github.com/greenmaskio/arcave/internal/provider/compose/internal/model"
	"github.com/greenmaskio/arcave/internal/provider/compose/internal/utils/composetest"
)

// demoVolumeName is the named volume of composetest.DemoService every capture test takes.
const demoVolumeName = "demo_db"

// demoSpecBytes is the artifact every capture test writes.
var demoSpecBytes = []byte("name: demo\n")

// demoVolume is the unit, in the form the mock tests address it by.
func demoVolume() composemodel.Volume {
	return composemodel.Volume{
		ObjectID: "volume-named-" + demoVolumeName,
		Backend:  composeconfig.SnapshotBackendFullCopy,
		Named:    &composemodel.VolumePropertiesNamed{VolumeName: demoVolumeName},
	}
}

// demoImage is what the backend reports for demoImageCaptureRequest.
func demoImage() composemodel.Image {
	image := composetest.DemoImageCaptureRequest().Image
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
