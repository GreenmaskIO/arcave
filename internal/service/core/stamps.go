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

package core

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/rs/zerolog"

	"github.com/greenmaskio/arcave/internal/model"
)

// errSpecRecordMismatch refuses a savepoint whose spec says it captured a unit its
// record does not hold.
var errSpecRecordMismatch = errors.New("the savepoint's spec names a capture its record does not hold")

// checkCaptureStamps logs what the rollback puts back, as the savepoint's capture stamps
// say, and refuses a capture a stamp names and the record does not hold.
func checkCaptureStamps(ctx context.Context, record model.Savepoint, project *types.Project) error {
	for _, name := range slices.Sorted(maps.Keys(project.Services)) {
		svc := project.Services[name]
		if err := checkLayerStamp(ctx, record, name, svc); err != nil {
			return err
		}
		for _, mount := range svc.Volumes {
			stamp, ok, err := readStamp(svc, model.MountStamp(mount.Target))
			if err != nil {
				return err
			}
			if !ok {
				continue
			}
			event := stampEvent(ctx, name, stamp).Str("target", mount.Target)
			if !stamp.Capture {
				event.Msg(notRestoredMessage(mount))
				continue
			}
			if !recordHolds(record, name, mount.Target) {
				return fmt.Errorf("%w: the mount at %s of service %s", errSpecRecordMismatch, mount.Target, name)
			}
			event.Msg("mount restored from the savepoint")
		}
	}
	return nil
}

// checkLayerStamp logs what the rollback puts back of a service's writable layers, and
// refuses a layer its stamp names and the record does not hold.
func checkLayerStamp(ctx context.Context, record model.Savepoint, name string, svc types.ServiceConfig) error {
	stamp, ok, err := readStamp(svc, model.StampWritableLayer)
	if err != nil || !ok {
		return err
	}
	event := stampEvent(ctx, name, stamp)
	if !stamp.Capture {
		event.Str("image", svc.Image).Msg("writable layer not in the savepoint: created fresh from its image")
		return nil
	}
	if !slices.ContainsFunc(record.WritableLayers, func(c model.WritableLayer) bool { return c.Service == name }) {
		return fmt.Errorf("%w: the writable layer of service %s", errSpecRecordMismatch, name)
	}
	event.Msg("writable layer restored from the savepoint")
	return nil
}

// readStamp reads one capture stamp of a service; ok is false when it carries none.
func readStamp(svc types.ServiceConfig, key string) (model.CaptureStamp, bool, error) {
	value, ok := svc.Labels[key]
	if !ok {
		return model.CaptureStamp{}, false, nil
	}
	stamp, err := model.ParseCaptureStamp(value)
	if err != nil {
		return model.CaptureStamp{}, false, fmt.Errorf("service %s: %w", svc.Name, err)
	}
	return stamp, true, nil
}

// stampEvent is the log line of one stamp, its backend and why named.
func stampEvent(ctx context.Context, service string, stamp model.CaptureStamp) *zerolog.Event {
	event := zerolog.Ctx(ctx).Info().Str("service", service).Str("origin", string(stamp.Origin))
	if stamp.Backend != "" {
		event = event.Str("backend", stamp.Backend)
	}
	if stamp.Source != "" {
		event = event.Str("reason", stamp.Source)
	}
	return event
}

// notRestoredMessage says what a rollback leaves of a mount the savepoint does not hold.
func notRestoredMessage(mount types.ServiceVolumeConfig) string {
	if mount.Type == types.VolumeTypeVolume && mount.Source == "" {
		return "mount not in the savepoint: created empty"
	}
	return "mount not in the savepoint: left as it is"
}

// recordHolds reports whether record captured the volume or bind service mounts at target.
func recordHolds(record model.Savepoint, service, target string) bool {
	return slices.ContainsFunc(volumesOf(record), func(v model.Volume) bool {
		switch {
		case v.Bind != nil:
			return v.Bind.Service == service && v.Bind.Target == target
		case v.Anon != nil:
			return v.Anon.Service == service && v.Anon.Target == target
		case v.Named != nil:
			return slices.ContainsFunc(v.Named.UsedBy, func(ref model.ServiceReference) bool {
				return ref.Service == service && ref.Target == target
			})
		}
		return false
	})
}
