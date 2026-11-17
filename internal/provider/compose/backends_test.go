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
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/greenmaskio/arcave/internal/model"
	composeconfig "github.com/greenmaskio/arcave/internal/provider/compose/config"
	"github.com/greenmaskio/arcave/internal/provider/compose/internal/service/capturepolicy"
	"github.com/greenmaskio/arcave/internal/utils"
)

// The config's capture keys reach the policy as written; one left unset is captured,
// because ApplyDefaults sets it.
func TestNewCaptureDefaults(t *testing.T) {
	// Arrange
	cfg := composeconfig.Config{
		Snapshot:       composeconfig.Snapshot{Backend: composeconfig.SnapshotBackendCDC},
		WritableLayers: composeconfig.WritableLayers{Backend: "fullcopy"},
		Capture: composeconfig.Capture{
			Mode:           "pause",
			WritableLayers: utils.New(false),
			Volumes:        composeconfig.CaptureVolumes{Bind: utils.New(false), Named: utils.New(true)},
		},
	}
	cfg.ApplyDefaults()

	// Act
	got := newCaptureDefaults(cfg)

	// Assert
	assert.Equal(t, capturepolicy.CaptureDefaults{
		VolumeBackend:        model.SnapshotBackendCDC,
		WritableLayerBackend: model.ImageBackendFullCopy,
		Mode:                 model.CaptureModePause,
		SavepointCapture: capturepolicy.SavepointCaptureSettings{
			WritableLayers:   false,
			Images:           true,
			NamedVolumes:     true,
			BindVolumes:      false,
			AnonymousVolumes: true,
		},
	}, got)
}
