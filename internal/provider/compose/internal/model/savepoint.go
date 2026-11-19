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
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/greenmaskio/arcave/internal/model"
)

// ProviderName is what a savepoint record names the compose provider by.
const ProviderName = "compose"

// ErrSavepointDetail refuses a record whose body the compose provider cannot read: another
// provider's, or one that does not decode.
var ErrSavepointDetail = errors.New("savepoint body is not the compose provider's")

// SpecFormatComposeYAML is the format of a savepoint's spec artifact: a compose
// document, loadable by compose's own loader.
const SpecFormatComposeYAML = "compose-yaml"

// SpecRef is the reference to a savepoint's compose spec artifact.
type SpecRef struct {
	Format string `json:"format"` // SpecFormatComposeYAML
}

// Savepoint is a savepoint as the compose provider holds it: arcave's header and its own body.
type Savepoint struct {
	// Header carries no Summary and no Detail: Detail is the body below, and Summary is derived.
	Header model.Savepoint
	Detail SavepointDetail
}

// SavepointDetail is the compose provider's body of a savepoint, the detail of its record. Its
// json tags are the record's.
type SavepointDetail struct {
	Stats []SnapshotStat `json:"stats,omitempty"` // one per snapshotted volume
	// WritableLayers is one capture per container whose writable layer was committed;
	// empty when a capture takes none.
	WritableLayers []WritableLayer `json:"writable_layers,omitempty"`
	// Images is one entry per distinct image the compose file named, kept
	// whether or not a writable layer was captured over it.
	Images []Image `json:"images,omitempty"`
	// Spec names the compose spec committed beside this record. A savepoint without
	// one cannot be rolled back to: the compose project is rebuilt from the spec.
	Spec *SpecRef `json:"spec,omitempty"`
	// NotCovered is what this savepoint does not hold; nil when it holds everything.
	NotCovered *NotCovered `json:"not_covered,omitempty"`
}

// NewSavepointRecord is the record m is stored as: the header naming the provider, the body
// encoded, no summary.
func NewSavepointRecord(m Savepoint) (model.Savepoint, error) {
	return newSharedSavepoint(m, model.SavepointSummary{})
}

// ToSharedSavepoint is m as arcave reads it: its record with the summary.
func ToSharedSavepoint(m Savepoint) (model.Savepoint, error) {
	return newSharedSavepoint(m, NewSavepointSummary(m.Detail))
}

// ParseSavepoint reads the compose provider's savepoint out of record; ErrSavepointDetail for
// another provider's or a body that does not decode.
func ParseSavepoint(record model.Savepoint) (Savepoint, error) {
	if record.Provider == "" {
		return Savepoint{}, fmt.Errorf("%w: savepoint %s was written by an earlier arcave version, which is not read back",
			ErrSavepointDetail, record.ID)
	}
	if record.Provider != ProviderName {
		return Savepoint{}, fmt.Errorf("%w: savepoint %s is the %q provider's", ErrSavepointDetail, record.ID, record.Provider)
	}
	var detail SavepointDetail
	if len(record.Detail) > 0 {
		if err := json.Unmarshal(record.Detail, &detail); err != nil {
			return Savepoint{}, fmt.Errorf("%w: savepoint %s: %w", ErrSavepointDetail, record.ID, err)
		}
	}
	return Savepoint{
		Header: model.Savepoint{
			ID:                    record.ID,
			Name:                  record.Name,
			Description:           record.Description,
			Labels:                record.Labels,
			TakenBeforeRollbackTo: record.TakenBeforeRollbackTo,
			CreatedAt:             record.CreatedAt,
		},
		Detail: detail,
	}, nil
}

// newSharedSavepoint is m's header naming the provider, with its body encoded and summary.
func newSharedSavepoint(m Savepoint, summary model.SavepointSummary) (model.Savepoint, error) {
	detail, err := json.Marshal(m.Detail)
	if err != nil {
		return model.Savepoint{}, fmt.Errorf("encode savepoint %s: %w", m.Header.ID, err)
	}
	return model.Savepoint{
		ID:                    m.Header.ID,
		Name:                  m.Header.Name,
		Description:           m.Header.Description,
		Labels:                m.Header.Labels,
		TakenBeforeRollbackTo: m.Header.TakenBeforeRollbackTo,
		CreatedAt:             m.Header.CreatedAt,
		Provider:              ProviderName,
		Summary:               summary,
		Detail:                detail,
	}, nil
}

// NewSavepointSummary sums detail up: its units, what its capture left out, what a rollback to it
// does not put back, and the writable layers it holds by replica.
func NewSavepointSummary(detail SavepointDetail) model.SavepointSummary {
	return model.SavepointSummary{
		Units:    newUnitTotals(detail),
		Notices:  newSavepointNotices(detail),
		Gaps:     newNotRestoredNotices(detail.NotCovered),
		Replicas: newReplicaHoldings(detail.WritableLayers),
	}
}

// newUnitTotals totals volumes always, layers and images when the savepoint holds any.
func newUnitTotals(detail SavepointDetail) []model.UnitTotal {
	var volumeSize, volumeStored int64
	for _, stat := range detail.Stats {
		volumeSize += stat.SizeBytes
		volumeStored += stat.StoredBytes
	}
	units := []model.UnitTotal{newUnitTotal(model.UnitKindVolumes, len(detail.Stats), volumeSize, volumeStored)}
	if len(detail.WritableLayers) > 0 {
		var layerSize, layerStored int64
		for _, layer := range detail.WritableLayers {
			layerSize += layer.SizeBytes
			layerStored += layer.StoredBytes
		}
		units = append(units, newUnitTotal(model.UnitKindLayers, len(detail.WritableLayers), layerSize, layerStored))
	}
	if len(detail.Images) > 0 {
		var imageSize, imageStored int64
		for _, image := range detail.Images {
			imageSize += image.SizeBytes
			imageStored += image.StoredBytes
		}
		units = append(units, newUnitTotal(model.UnitKindImages, len(detail.Images), imageSize, imageStored))
	}
	return units
}

// newUnitTotal is one kind's total.
func newUnitTotal(kind model.UnitKind, count int, size, stored int64) model.UnitTotal {
	return model.UnitTotal{
		Kind:        kind,
		Count:       count,
		SizeBytes:   size,
		StoredBytes: stored,
	}
}

// newSavepointNotices spells out what the savepoint leaves out and every image it names but does
// not store, the severe first.
func newSavepointNotices(detail SavepointDetail) []model.Notice {
	var notices []model.Notice
	if detail.NotCovered != nil {
		notices = detail.NotCovered.Notices()
	}
	for _, image := range detail.Images {
		if image.Stored {
			continue
		}
		notices = append(notices, model.Notice{
			Message: fmt.Sprintf("image %s is not stored, only its identity", newImageName(image)),
			Hint:    "a rollback needs it pullable to rebuild a container on it",
			Severe:  true,
		})
	}
	return model.SortSevereFirst(notices)
}

// newNotRestoredNotices say what the savepoint left out, so a rollback to it did not put back: a
// mount a label or the config excluded, and a writable layer.
func newNotRestoredNotices(notCovered *NotCovered) []model.Notice {
	if notCovered == nil {
		return nil
	}
	var notices []model.Notice
	for _, mount := range notCovered.Mounts {
		if mount.Reason != SkipReasonExcluded {
			continue
		}
		var message string
		switch {
		case mount.Volume != "":
			message = "volume " + mount.Volume + " was not in the savepoint: it holds what it held before the rollback"
		case mount.RawType == string(VolumeTypeBind):
			message = fmt.Sprintf("%s %s was not in the savepoint: the host folder holds what it held before the rollback",
				mount.Service, mount.Target)
		default:
			message = fmt.Sprintf("%s %s was not in the savepoint: it came back empty",
				formatReplicaName(mount.Service, mount.Replica), mount.Target)
		}
		notices = append(notices, model.Notice{
			Code:    model.NoticeCodeVolumeNotRestored,
			Message: message,
		})
	}
	for _, layer := range notCovered.WritableLayers {
		notices = append(notices, model.Notice{
			Code: model.NoticeCodeLayerNotRestored,
			Message: fmt.Sprintf("%s's writable layer was not in the savepoint: it started fresh from its image",
				formatReplicaName(layer.Service, layer.Replica)),
		})
	}
	return notices
}

// newReplicaHoldings names, by replica, the writable layers the savepoint holds.
func newReplicaHoldings(layers []WritableLayer) []model.ReplicaHolding {
	var holdings []model.ReplicaHolding
	for _, layer := range layers {
		holdings = append(holdings, model.ReplicaHolding{
			Service: layer.Service,
			Replica: layer.Replica,
			Held:    []string{model.ReplicaHeldLayer},
		})
	}
	return holdings
}

// newImageName is an image's first reference, else its short id.
func newImageName(image Image) string {
	if len(image.Refs) > 0 {
		return image.Refs[0]
	}
	return formatShortID(strings.TrimPrefix(image.ID.String(), "sha256:"))
}

// formatReplicaName names a replica as compose numbers it: service-n.
func formatReplicaName(service string, number int) string {
	return service + "-" + strconv.Itoa(number)
}
