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

package composespec

import (
	"context"
	"reflect"
	"slices"
	"strings"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/rs/zerolog"

	"github.com/greenmaskio/arcave/internal/model"
)

// perReplicaFields differ between replicas by construction.
var perReplicaFields = []string{"ContainerName", "Hostname"}

// warnOnDivergentReplicas reports, once per service, the fields in which a later
// replica differs from the first one the spec was rendered off.
func (s assembly) warnOnDivergentReplicas(ctx context.Context, svc model.Service, first types.ServiceConfig) {
	var diverged []string
	for _, c := range svc.Containers[min(1, len(svc.Containers)):] {
		replica := s.baseConfig(svc)
		s.applyReplica(&replica, svc.Name, c)
		for _, field := range divergentFields(first, replica) {
			if !slices.Contains(diverged, field) {
				diverged = append(diverged, field)
			}
		}
	}
	if len(diverged) == 0 {
		return
	}
	zerolog.Ctx(ctx).Warn().Str("service", svc.Name).Strs("fields", diverged).
		Msg("replicas differ; the spec declares the first replica's")
}

// divergentFields names, by their compose key, the fields a and b differ in.
func divergentFields(a, b types.ServiceConfig) []string {
	va, vb := reflect.ValueOf(a), reflect.ValueOf(b)
	var fields []string
	for i := range va.NumField() {
		field := va.Type().Field(i)
		if slices.Contains(perReplicaFields, field.Name) {
			continue
		}
		if !reflect.DeepEqual(va.Field(i).Interface(), vb.Field(i).Interface()) {
			fields = append(fields, composeKey(field))
		}
	}
	return fields
}

// composeKey is the field's yaml key, or its Go name when it has none.
func composeKey(field reflect.StructField) string {
	key, _, _ := strings.Cut(field.Tag.Get("yaml"), ",")
	if key == "" || key == "-" {
		return field.Name
	}
	return key
}
