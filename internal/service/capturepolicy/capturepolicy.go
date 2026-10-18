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
	ctx context.Context, res model.IntrospectionResult, defaults CaptureDefaults,
) (model.CapturePolicy, error) {
	capturePolicy := model.CapturePolicy{
		VolumeBackend:        defaults.VolumeBackend,
		WritableLayerBackend: defaults.WritableLayerBackend,
		Services:             map[string]model.ServiceDecision{},
		NamedVolumes:         map[string]model.VolumeDecision{},
		Mounts:               map[model.MountRef]model.VolumeDecision{},
	}

	services := map[string]serviceLabels{}
	for _, service := range res.Project.Services {
		labels, err := parseServiceLabels(ctx, service, findImageLabels(res, service))
		if err != nil {
			return model.CapturePolicy{}, err
		}
		services[service.Name] = labels
		capturePolicy.Services[service.Name] = labels.decideWritableLayerAndImage(defaults)
		for _, serviceVolume := range service.Volumes {
			if !isMountOwnedByService(serviceVolume) {
				continue
			}
			decision, err := labels.decideMount(ctx, serviceVolume, defaults)
			if err != nil {
				return model.CapturePolicy{}, err
			}
			ref := model.MountRef{Service: service.Name, Replica: serviceVolume.Replica, Target: serviceVolume.Target}
			capturePolicy.Mounts[ref] = decision
		}
	}

	for _, named := range res.Project.Volumes {
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
	ctx context.Context, res model.IntrospectionResult, defaults CaptureDefaults,
) (model.CapturePolicy, error) {
	unlabelled := res
	unlabelled.Project.Services = make([]model.Service, len(res.Project.Services))
	for serviceIdx, service := range res.Project.Services {
		service.Labels = nil
		service.Volumes = slices.Clone(service.Volumes)
		for volumeIdx, serviceVolume := range service.Volumes {
			if serviceVolume.Volume != nil {
				mount := *serviceVolume.Volume
				mount.Labels = nil
				service.Volumes[volumeIdx].Volume = &mount
			}
		}
		unlabelled.Project.Services[serviceIdx] = service
	}
	unlabelled.Project.Volumes = make([]model.NamedVolume, len(res.Project.Volumes))
	for volumeIdx, named := range res.Project.Volumes {
		named.Labels = nil
		unlabelled.Project.Volumes[volumeIdx] = named
	}
	unlabelled.Images = nil

	return Resolve(ctx, unlabelled, defaults)
}

// findImageLabels are the labels of the image the service's first replica runs on.
func findImageLabels(res model.IntrospectionResult, service model.Service) map[string]string {
	if len(service.Containers) == 0 {
		return nil
	}
	inspect, ok := res.Images[service.Containers[0].ImageID()]
	if !ok || inspect.Config == nil {
		return nil
	}
	return inspect.Config.Labels
}

// isMountOwnedByService reports whether a mount is decided per service: a bind or an
// anonymous volume, not inherited through volumes_from. A named volume is decided once
// for every service mounting it.
func isMountOwnedByService(serviceVolume model.ServiceVolume) bool {
	isOwnType := serviceVolume.Type == model.VolumeTypeBind || serviceVolume.Type == model.VolumeTypeAnon
	return isOwnType && serviceVolume.InheritedFrom == ""
}

// parseServiceLabels reads and checks a service's labels, its mount targets against
// the mounts it has.
func parseServiceLabels(ctx context.Context, service model.Service, image map[string]string) (serviceLabels, error) {
	where := "service " + service.Name
	live := service.Labels
	if err := refuseUnknownLabels(ctx, live, serviceLabelKeys, image, where); err != nil {
		return serviceLabels{}, err
	}

	labels := serviceLabels{name: service.Name, rawLabels: live, backendByTarget: map[string]model.SnapshotBackendKind{}}
	var err error
	if labels.capture, err = parseCaptureLabel(live, model.LabelCapture, where); err != nil {
		return serviceLabels{}, err
	}
	if labels.writableLayerCapture, err = parseCaptureLabel(live, model.LabelWritableLayerCapture, where); err != nil {
		return serviceLabels{}, err
	}
	if labels.imageCapture, err = parseCaptureLabel(live, model.LabelImageCapture, where); err != nil {
		return serviceLabels{}, err
	}
	if labels.volumeBackend, err = parseVolumeBackend(live, model.LabelVolumeBackend, where); err != nil {
		return serviceLabels{}, err
	}
	if value, ok := live[model.LabelWritableLayerBackend]; ok {
		backend, ok := model.ParseImageBackend(value)
		if !ok {
			return serviceLabels{}, newInvalidLabelError(
				where, model.LabelWritableLayerBackend, value, "want dedupcopy or fullcopy")
		}
		labels.writableLayerBackend = &backend
	}

	if labels.excludedTargets, err = parseTargets(service, live, model.LabelMountsExclude); err != nil {
		return serviceLabels{}, err
	}
	if labels.includedTargets, err = parseTargets(service, live, model.LabelMountsInclude); err != nil {
		return serviceLabels{}, err
	}
	for _, target := range labels.includedTargets {
		if slices.Contains(labels.excludedTargets, target) {
			return serviceLabels{}, newInvalidLabelError(where, model.LabelMountsInclude, live[model.LabelMountsInclude],
				fmt.Sprintf("target %s is also in %s", target, model.LabelMountsExclude))
		}
	}
	for _, list := range []struct {
		key     string
		backend model.SnapshotBackendKind
	}{
		{model.LabelMountsFullCopy, model.SnapshotBackendFullCopy},
		{model.LabelMountsCDC, model.SnapshotBackendCDC},
	} {
		targets, err := parseTargets(service, live, list.key)
		if err != nil {
			return serviceLabels{}, err
		}
		for _, target := range targets {
			if _, taken := labels.backendByTarget[target]; taken {
				return serviceLabels{}, newInvalidLabelError(where, list.key, live[list.key],
					fmt.Sprintf("target %s is listed for both %s and %s",
						target, model.LabelMountsFullCopy, model.LabelMountsCDC))
			}
			labels.backendByTarget[target] = list.backend
		}
	}
	return labels, nil
}

// decideWritableLayerAndImage decides a service's writable layer and image: its own
// label, else the service's arcave.capture, else the config.
func (l serviceLabels) decideWritableLayerAndImage(defaults CaptureDefaults) model.ServiceDecision {
	writableLayerConfig := newConfigDecision(defaults.SavepointCapture.WritableLayers, "writable_layers")
	writableLayer := model.WritableLayerDecision{
		CaptureDecision: l.decideWithFallback(l.writableLayerCapture, writableLayerConfig),
		Backend:         defaults.WritableLayerBackend,
	}
	if l.writableLayerBackend != nil {
		writableLayer.Backend = *l.writableLayerBackend
	}
	image := l.decideWithFallback(l.imageCapture, newConfigDecision(defaults.SavepointCapture.Images, "images"))
	return model.ServiceDecision{WritableLayer: writableLayer, Image: image}
}

// decideMount decides one bind or anonymous mount of the service, most specific first:
// the anonymous volume's own label, the target lists, the service's defaults, the config.
func (l serviceLabels) decideMount(
	ctx context.Context, serviceVolume model.ServiceVolume, defaults CaptureDefaults,
) (model.VolumeDecision, error) {
	var ownLabels volumeLabels
	if serviceVolume.Type == model.VolumeTypeAnon && serviceVolume.Volume != nil {
		var err error
		where := "the anonymous volume at " + serviceVolume.Target + " of service " + l.name
		ownLabels, err = parseVolumeLabels(ctx, serviceVolume.Volume.Labels, where)
		if err != nil {
			return model.VolumeDecision{}, err
		}
	}

	config := newConfigDecision(defaults.SavepointCapture.BindVolumes, "volumes.bind")
	if serviceVolume.Type == model.VolumeTypeAnon {
		config = newConfigDecision(defaults.SavepointCapture.AnonymousVolumes, "volumes.anonymous")
	}
	decision := model.VolumeDecision{CaptureDecision: l.decideWithFallback(ownLabels.capture, config)}
	if ownLabels.capture == nil {
		if listed, ok := l.decideByTargetLists(serviceVolume.Target); ok {
			decision.CaptureDecision = listed
		}
	}

	decision.Backend = defaults.VolumeBackend
	switch backend, listed := l.findListedBackend(serviceVolume.Target); {
	case ownLabels.backend != nil:
		decision.Backend = *ownLabels.backend
	case listed:
		decision.Backend = backend
	case l.volumeBackend != nil:
		decision.Backend = *l.volumeBackend
	}
	return decision, nil
}

// decideByTargetLists is what arcave.mounts.exclude and .include say of a target, if
// either does.
func (l serviceLabels) decideByTargetLists(target string) (model.CaptureDecision, bool) {
	switch {
	case slices.Contains(l.excludedTargets, target):
		return newLabelDecision(false, l.formatListSource(model.LabelMountsExclude)), true
	case slices.Contains(l.includedTargets, target):
		return newLabelDecision(true, l.formatListSource(model.LabelMountsInclude)), true
	default:
		return model.CaptureDecision{}, false
	}
}

// findListedBackend is the backend the target lists name for a target.
func (l serviceLabels) findListedBackend(target string) (model.SnapshotBackendKind, bool) {
	backend, ok := l.backendByTarget[target]
	return backend, ok
}

// formatListSource names a target list label of the service as a decision's source.
func (l serviceLabels) formatListSource(key string) string {
	return formatLabelSource(key, l.rawLabels[key], "service "+l.name)
}

// decideWithFallback is decision when set, else the service's arcave.capture, else config.
func (l serviceLabels) decideWithFallback(
	decision *model.CaptureDecision, config model.CaptureDecision,
) model.CaptureDecision {
	switch {
	case decision != nil:
		return *decision
	case l.capture != nil:
		return *l.capture
	default:
		return config
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
		return model.ConfigDecision(false, fmt.Sprintf("capture.%s=false in the config", key))
	}
	return model.ConfigDecision(true, "")
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
	ctx context.Context, labels map[string]string, allowed []string, image map[string]string, where string,
) error {
	// Sorted, so of several unknown keys the same one is named every run.
	for _, key := range slices.Sorted(maps.Keys(labels)) {
		if !strings.HasPrefix(key, model.LabelPrefix) || slices.Contains(allowed, key) {
			continue
		}
		if value, ok := image[key]; ok && value == labels[key] {
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
	volumeIdx := slices.IndexFunc(service.Volumes, func(serviceVolume model.ServiceVolume) bool {
		return serviceVolume.Target == target
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
