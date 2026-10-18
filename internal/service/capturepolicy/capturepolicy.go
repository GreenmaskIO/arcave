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

// Package capturepolicy reads a compose project's arcave.* labels and the config's
// defaults into the capture decision for every resource, and spells the decisions back
// out as labels for the spec a savepoint records. Pure computation.
package capturepolicy

import (
	"context"
	"fmt"
	"maps"
	"path"
	"slices"
	"strings"

	"github.com/rs/zerolog"

	"github.com/greenmaskio/arcave/internal/model"
)

// serviceLabelKeys are the labels a service may carry; volumeLabelKeys those a named or
// anonymous volume may.
var (
	serviceLabelKeys = []string{
		model.LabelCapture, model.LabelVolumeBackend, model.LabelMountsExclude,
		model.LabelMountsInclude, model.LabelMountsFullCopy, model.LabelMountsCDC,
		model.LabelWritableLayerCapture, model.LabelWritableLayerBackend, model.LabelImageCapture,
	}
	volumeLabelKeys = []string{model.LabelCapture, model.LabelBackend}
)

// CaptureDefaults are what the config says of a resource no label names: its backend,
// and whether its kind is captured.
type CaptureDefaults struct {
	VolumeBackend        model.SnapshotBackendKind
	WritableLayerBackend model.ImageBackendKind
	SavepointCapture     SavepointCaptureSettings
}

// SavepointCaptureSettings says which kinds of resource a savepoint captures when no
// label says otherwise: the config's capture section, one field per key.
type SavepointCaptureSettings struct {
	WritableLayers   bool
	Images           bool
	NamedVolumes     bool
	BindVolumes      bool
	AnonymousVolumes bool
}

// serviceLabels is one service's arcave.* labels, parsed; a nil field is unset.
type serviceLabels struct {
	name string
	// rawLabels are the service's labels as set, for the sources that name them.
	rawLabels            map[string]string
	capture              *model.CaptureDecision
	volumeBackend        *model.SnapshotBackendKind
	excludedTargets      []string
	includedTargets      []string
	backendByTarget      map[string]model.SnapshotBackendKind
	writableLayerCapture *model.CaptureDecision
	writableLayerBackend *model.ImageBackendKind
	imageCapture         *model.CaptureDecision
}

// volumeLabels is one volume's arcave.* labels, parsed; a nil field is unset.
type volumeLabels struct {
	capture *model.CaptureDecision
	backend *model.SnapshotBackendKind
}

// Resolve decides every resource of the introspected compose project from its labels and
// defaults; a label arcave cannot act on is model.ErrInvalidCapturePolicy. arcave's own
// io.arcave.* stamps are never read.
func Resolve(
	ctx context.Context, introspection model.IntrospectionResult, defaults CaptureDefaults,
) (model.CapturePolicy, error) {
	capturePolicy := model.CapturePolicy{
		VolumeBackend:        defaults.VolumeBackend,
		WritableLayerBackend: defaults.WritableLayerBackend,
		Services:             map[string]model.ServiceDecision{},
		NamedVolumes:         map[string]model.VolumeDecision{},
		Mounts:               map[model.MountRef]model.VolumeDecision{},
	}

	services := map[string]serviceLabels{}
	for _, service := range introspection.Project.Services {
		labels, err := parseServiceLabels(ctx, service, findImageLabels(introspection, service))
		if err != nil {
			return model.CapturePolicy{}, err
		}
		services[service.Name] = labels
		capturePolicy.Services[service.Name] = labels.decideWritableLayerAndImage(defaults)
		for _, mount := range service.Volumes {
			if !isMountOwnedByService(mount) {
				continue
			}
			decision, err := labels.decideMount(ctx, mount, defaults)
			if err != nil {
				return model.CapturePolicy{}, err
			}
			ref := model.MountRef{Service: service.Name, Replica: mount.Replica, Target: mount.Target}
			capturePolicy.Mounts[ref] = decision
		}
	}

	for _, named := range introspection.Project.Volumes {
		decision, err := decideNamed(ctx, named, services, defaults)
		if err != nil {
			return model.CapturePolicy{}, err
		}
		capturePolicy.NamedVolumes[named.Name] = decision
	}
	return capturePolicy, nil
}

// ResolveWithoutLabels decides every resource by the defaults alone, every label ignored.
func ResolveWithoutLabels(
	ctx context.Context, introspection model.IntrospectionResult, defaults CaptureDefaults,
) (model.CapturePolicy, error) {
	unlabelled := introspection
	unlabelled.Project.Services = make([]model.Service, len(introspection.Project.Services))
	for serviceIdx, service := range introspection.Project.Services {
		service.Labels = nil
		service.Volumes = slices.Clone(service.Volumes)
		for volumeIdx, mount := range service.Volumes {
			if mount.Volume != nil {
				options := *mount.Volume
				options.Labels = nil
				service.Volumes[volumeIdx].Volume = &options
			}
		}
		unlabelled.Project.Services[serviceIdx] = service
	}
	unlabelled.Project.Volumes = make([]model.NamedVolume, len(introspection.Project.Volumes))
	for volumeIdx, named := range introspection.Project.Volumes {
		named.Labels = nil
		unlabelled.Project.Volumes[volumeIdx] = named
	}
	unlabelled.Images = nil

	return Resolve(ctx, unlabelled, defaults)
}

// findImageLabels are the labels of the image the service's first replica runs on.
func findImageLabels(introspection model.IntrospectionResult, service model.Service) map[string]string {
	if len(service.Containers) == 0 {
		return nil
	}
	inspect, ok := introspection.Images[service.Containers[0].ImageID()]
	if !ok || inspect.Config == nil {
		return nil
	}
	return inspect.Config.Labels
}

// isMountOwnedByService reports whether a mount is decided per service: a bind or an
// anonymous volume, not inherited through volumes_from. A named volume is decided once
// for every service mounting it.
func isMountOwnedByService(mount model.ServiceVolume) bool {
	isOwnType := mount.Type == model.VolumeTypeBind || mount.Type == model.VolumeTypeAnon
	return isOwnType && mount.InheritedFrom == ""
}

// parseServiceLabels reads and checks a service's labels, its mount targets against
// the mounts it has.
func parseServiceLabels(
	ctx context.Context,
	service model.Service,
	imageLabels map[string]string,
) (serviceLabels, error) {
	where := "service " + service.Name
	raw := service.Labels
	if err := refuseUnknownLabels(ctx, raw, serviceLabelKeys, imageLabels, where); err != nil {
		return serviceLabels{}, err
	}

	labels := serviceLabels{name: service.Name, rawLabels: raw, backendByTarget: map[string]model.SnapshotBackendKind{}}
	var err error
	if labels.capture, err = parseCaptureLabel(raw, model.LabelCapture, where); err != nil {
		return serviceLabels{}, err
	}
	if labels.writableLayerCapture, err = parseCaptureLabel(raw, model.LabelWritableLayerCapture, where); err != nil {
		return serviceLabels{}, err
	}
	if labels.imageCapture, err = parseCaptureLabel(raw, model.LabelImageCapture, where); err != nil {
		return serviceLabels{}, err
	}
	if labels.volumeBackend, err = parseVolumeBackend(raw, model.LabelVolumeBackend, where); err != nil {
		return serviceLabels{}, err
	}
	if value, ok := raw[model.LabelWritableLayerBackend]; ok {
		backend, ok := model.ParseImageBackend(value)
		if !ok {
			return serviceLabels{}, newInvalidLabelError(
				where, model.LabelWritableLayerBackend, value, "want dedupcopy or fullcopy")
		}
		labels.writableLayerBackend = &backend
	}

	if labels.excludedTargets, err = parseTargets(service, raw, model.LabelMountsExclude); err != nil {
		return serviceLabels{}, err
	}
	if labels.includedTargets, err = parseTargets(service, raw, model.LabelMountsInclude); err != nil {
		return serviceLabels{}, err
	}
	for _, target := range labels.includedTargets {
		if slices.Contains(labels.excludedTargets, target) {
			return serviceLabels{}, newInvalidLabelError(where, model.LabelMountsInclude, raw[model.LabelMountsInclude],
				fmt.Sprintf("target %s is also in %s", target, model.LabelMountsExclude))
		}
	}
	for _, targetList := range []struct {
		key     string
		backend model.SnapshotBackendKind
	}{
		{model.LabelMountsFullCopy, model.SnapshotBackendFullCopy},
		{model.LabelMountsCDC, model.SnapshotBackendCDC},
	} {
		targets, err := parseTargets(service, raw, targetList.key)
		if err != nil {
			return serviceLabels{}, err
		}
		for _, target := range targets {
			if _, taken := labels.backendByTarget[target]; taken {
				return serviceLabels{}, newInvalidLabelError(where, targetList.key, raw[targetList.key],
					fmt.Sprintf("target %s is listed for both %s and %s",
						target, model.LabelMountsFullCopy, model.LabelMountsCDC))
			}
			labels.backendByTarget[target] = targetList.backend
		}
	}
	return labels, nil
}

// decideWritableLayerAndImage decides a service's writable layer and image: its own
// label, else the service's arcave.capture, else the config.
func (sl serviceLabels) decideWritableLayerAndImage(defaults CaptureDefaults) model.ServiceDecision {
	writableLayerConfig := newConfigDecision(defaults.SavepointCapture.WritableLayers, "writable_layers")
	writableLayer := model.WritableLayerDecision{
		CaptureDecision: sl.decideWithFallback(sl.writableLayerCapture, writableLayerConfig),
		Backend:         defaults.WritableLayerBackend,
	}
	if sl.writableLayerBackend != nil {
		writableLayer.Backend = *sl.writableLayerBackend
	}
	image := sl.decideWithFallback(sl.imageCapture, newConfigDecision(defaults.SavepointCapture.Images, "images"))
	return model.ServiceDecision{WritableLayer: writableLayer, Image: image}
}

// decideMount decides one bind or anonymous mount of the service, most specific first:
// the anonymous volume's own label, the target lists, the service's defaults, the config.
func (sl serviceLabels) decideMount(
	ctx context.Context, mount model.ServiceVolume, defaults CaptureDefaults,
) (model.VolumeDecision, error) {
	var ownLabels volumeLabels
	if mount.Type == model.VolumeTypeAnon && mount.Volume != nil {
		var err error
		where := "the anonymous volume at " + mount.Target + " of service " + sl.name
		ownLabels, err = parseVolumeLabels(ctx, mount.Volume.Labels, where)
		if err != nil {
			return model.VolumeDecision{}, err
		}
	}

	configDecision := newConfigDecision(defaults.SavepointCapture.BindVolumes, "volumes.bind")
	if mount.Type == model.VolumeTypeAnon {
		configDecision = newConfigDecision(defaults.SavepointCapture.AnonymousVolumes, "volumes.anonymous")
	}
	decision := model.VolumeDecision{CaptureDecision: sl.decideWithFallback(ownLabels.capture, configDecision)}
	if ownLabels.capture == nil {
		if listed, ok := sl.decideByTargetLists(mount.Target); ok {
			decision.CaptureDecision = listed
		}
	}

	decision.Backend = defaults.VolumeBackend
	switch backend, listed := sl.findListedBackend(mount.Target); {
	case ownLabels.backend != nil:
		decision.Backend = *ownLabels.backend
	case listed:
		decision.Backend = backend
	case sl.volumeBackend != nil:
		decision.Backend = *sl.volumeBackend
	}
	return decision, nil
}

// decideByTargetLists is what arcave.mounts.exclude and .include say of a target, if
// either does.
func (sl serviceLabels) decideByTargetLists(target string) (model.CaptureDecision, bool) {
	switch {
	case slices.Contains(sl.excludedTargets, target):
		return newLabelDecision(false, sl.formatListSource(model.LabelMountsExclude)), true
	case slices.Contains(sl.includedTargets, target):
		return newLabelDecision(true, sl.formatListSource(model.LabelMountsInclude)), true
	default:
		return model.CaptureDecision{}, false
	}
}

// findListedBackend is the backend the target lists name for a target.
func (sl serviceLabels) findListedBackend(target string) (model.SnapshotBackendKind, bool) {
	backend, ok := sl.backendByTarget[target]
	return backend, ok
}

// formatListSource names a target list label of the service as a decision's source.
func (sl serviceLabels) formatListSource(key string) string {
	return formatLabelSource(key, sl.rawLabels[key], "service "+sl.name)
}

// decideWithFallback is decision when set, else the service's arcave.capture, else configDecision.
func (sl serviceLabels) decideWithFallback(
	decision *model.CaptureDecision, configDecision model.CaptureDecision,
) model.CaptureDecision {
	switch {
	case decision != nil:
		return *decision
	case sl.capture != nil:
		return *sl.capture
	default:
		return configDecision
	}
}

// decideNamed decides a named volume, most specific first: its own label, what the
// services mounting it say of its target, their arcave.capture, the config.
func decideNamed(
	ctx context.Context, named model.NamedVolume, services map[string]serviceLabels, defaults CaptureDefaults,
) (model.VolumeDecision, error) {
	ownLabels, err := parseVolumeLabels(ctx, named.Labels, "volume "+named.Name)
	if err != nil {
		return model.VolumeDecision{}, err
	}
	listedDecision, listedBackend, err := decideNamedByTargetLists(named, services)
	if err != nil {
		return model.VolumeDecision{}, err
	}

	decision := model.VolumeDecision{
		CaptureDecision: newConfigDecision(defaults.SavepointCapture.NamedVolumes, "volumes.named"),
		Backend:         defaults.VolumeBackend,
	}
	switch {
	case ownLabels.backend != nil:
		decision.Backend = *ownLabels.backend
	case listedBackend != nil:
		decision.Backend = *listedBackend
	}
	switch {
	case ownLabels.capture != nil:
		decision.CaptureDecision = *ownLabels.capture
	case listedDecision != nil:
		decision.CaptureDecision = *listedDecision
	default:
		if byServices, ok := decideByServicesCapture(named.UsedBy, services); ok {
			decision.CaptureDecision = byServices
		}
	}
	return decision, nil
}

// decideNamedByTargetLists is what the services mounting a named volume say of its
// target: left out if any leaves it out, else taken if any takes it, and the backend
// they name, which they must agree on.
func decideNamedByTargetLists(
	named model.NamedVolume, services map[string]serviceLabels,
) (*model.CaptureDecision, *model.SnapshotBackendKind, error) {
	var (
		capture       *model.CaptureDecision
		agreedBackend *model.SnapshotBackendKind
		agreedBy      string
	)
	for _, ref := range named.UsedBy {
		labels := services[ref.Service]
		if decision, ok := labels.decideByTargetLists(ref.Target); ok && (capture == nil || capture.Capture) {
			capture = &decision
		}
		backend, ok := labels.findListedBackend(ref.Target)
		if !ok {
			continue
		}
		if agreedBackend != nil && *agreedBackend != backend {
			return nil, nil, fmt.Errorf("%w: services %s and %s name different backends for volume %s",
				model.ErrInvalidCapturePolicy, agreedBy, ref.Service, named.Name)
		}
		agreedBackend, agreedBy = &backend, ref.Service
	}
	return capture, agreedBackend, nil
}

// decideByServicesCapture is what the arcave.capture of the services mounting a volume
// says of it: taken if any takes it, left out if all leave it out.
func decideByServicesCapture(
	refs []model.ServiceReference, services map[string]serviceLabels,
) (model.CaptureDecision, bool) {
	excluded := len(refs) > 0
	for _, ref := range refs {
		capture := services[ref.Service].capture
		if capture != nil && capture.Capture {
			return *capture, true
		}
		if capture == nil {
			excluded = false
		}
	}
	if excluded {
		return newLabelDecision(false, formatLabelSource(model.LabelCapture, "false", "every service mounting it")), true
	}
	return model.CaptureDecision{}, false
}

// newConfigDecision is the config's decision for a kind: left out when off, naming the
// key that says so.
func newConfigDecision(capture bool, key string) model.CaptureDecision {
	if !capture {
		return model.NewConfigDecision(false, fmt.Sprintf("capture.%s=false in the config", key))
	}
	return model.NewConfigDecision(true, "")
}

// newLabelDecision is a decision a label made.
func newLabelDecision(capture bool, source string) model.CaptureDecision {
	return model.CaptureDecision{Capture: capture, Origin: model.OriginLabel, Source: source}
}

// formatLabelSource is how every decision a label made names it: "<key>=<value> on <where>".
func formatLabelSource(key, value, where string) string {
	return fmt.Sprintf("%s=%s on %s", key, value, where)
}

// parseVolumeLabels reads and checks a named or anonymous volume's labels.
func parseVolumeLabels(ctx context.Context, labels map[string]string, where string) (volumeLabels, error) {
	if err := refuseUnknownLabels(ctx, labels, volumeLabelKeys, nil, where); err != nil {
		return volumeLabels{}, err
	}
	capture, err := parseCaptureLabel(labels, model.LabelCapture, where)
	if err != nil {
		return volumeLabels{}, err
	}
	backend, err := parseVolumeBackend(labels, model.LabelBackend, where)
	if err != nil {
		return volumeLabels{}, err
	}
	return volumeLabels{capture: capture, backend: backend}, nil
}

// refuseUnknownLabels refuses an arcave.* label not among allowed. One the image carries
// with the same value is the image's, not the user's: it is skipped with a warning.
func refuseUnknownLabels(
	ctx context.Context, labels map[string]string, allowed []string, imageLabels map[string]string, where string,
) error {
	// Sorted, so of several unknown keys the same one is named every run.
	for _, key := range slices.Sorted(maps.Keys(labels)) {
		if !strings.HasPrefix(key, model.LabelPrefix) || slices.Contains(allowed, key) {
			continue
		}
		if value, ok := imageLabels[key]; ok && value == labels[key] {
			zerolog.Ctx(ctx).Warn().Str("label", key).Str("where", where).
				Msg("the image carries a label arcave does not read; it is ignored")
			continue
		}
		return newInvalidLabelError(where, key, labels[key],
			"not a label arcave reads here; it reads "+strings.Join(allowed, ", "))
	}
	return nil
}

// parseCaptureLabel reads a "true"/"false" label into a decision naming it.
func parseCaptureLabel(labels map[string]string, key, where string) (*model.CaptureDecision, error) {
	value, ok := labels[key]
	if !ok {
		return nil, nil
	}
	var capture bool
	switch value {
	case "true":
		capture = true
	case "false":
	default:
		return nil, newInvalidLabelError(where, key, value, "want true or false")
	}
	decision := newLabelDecision(capture, formatLabelSource(key, value, where))
	return &decision, nil
}

// parseVolumeBackend reads a volume backend token label.
func parseVolumeBackend(labels map[string]string, key, where string) (*model.SnapshotBackendKind, error) {
	value, ok := labels[key]
	if !ok {
		return nil, nil
	}
	backend, ok := model.ParseVolumeBackend(value)
	if !ok {
		return nil, newInvalidLabelError(where, key, value, "want fullcopy or cdc")
	}
	return &backend, nil
}

// parseTargets reads a comma-separated target list.
func parseTargets(service model.Service, labels map[string]string, key string) ([]string, error) {
	value, ok := labels[key]
	if !ok {
		return nil, nil
	}
	var targets []string
	for _, field := range strings.Split(value, ",") {
		field = strings.TrimSpace(field)
		if field == "" {
			continue
		}
		target := path.Clean(field)
		if err := requireMountedTarget(service, key, value, target); err != nil {
			return nil, err
		}
		targets = append(targets, target)
	}
	return targets, nil
}

// requireMountedTarget refuses a target that is not one of the service's binds,
// anonymous volumes or named volume mounts.
func requireMountedTarget(service model.Service, key, value, target string) error {
	where := "service " + service.Name
	volumeIdx := slices.IndexFunc(service.Volumes, func(mount model.ServiceVolume) bool {
		return mount.Target == target
	})
	switch {
	case volumeIdx < 0:
		return newInvalidLabelError(where, key, value, "the service mounts nothing at "+target)
	case service.Volumes[volumeIdx].Type != model.VolumeTypeNamed && !isMountOwnedByService(service.Volumes[volumeIdx]):
		return newInvalidLabelError(where, key, value, target+" is not a volume or bind of the service's own")
	}
	return nil
}

// newInvalidLabelError is model.ErrInvalidCapturePolicy naming the label and why.
func newInvalidLabelError(where, key, value, why string) error {
	return fmt.Errorf("%w: %s=%q on %s: %s", model.ErrInvalidCapturePolicy, key, value, where, why)
}
