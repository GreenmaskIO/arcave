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

package config

import (
	"fmt"
	"reflect"
	"slices"
)

// Keys returns every leaf of Config as a sorted dotted path (storage.s3.bucket).
func Keys() []string {
	keys := newKeys(reflect.TypeFor[Config](), "")
	slices.Sort(keys)
	return keys
}

// newKeys walks t's fields by their mapstructure tags, descending structs and pointers to them.
func newKeys(t reflect.Type, prefix string) []string {
	var keys []string
	for i := range t.NumField() {
		field := t.Field(i)
		name := field.Tag.Get("mapstructure")
		if name == "" {
			panic(fmt.Sprintf("config key of %s.%s: no mapstructure tag", t.Name(), field.Name))
		}
		key := name
		if prefix != "" {
			key = prefix + "." + name
		}
		leaf := field.Type
		if leaf.Kind() == reflect.Pointer {
			leaf = leaf.Elem()
		}
		if leaf.Kind() == reflect.Struct {
			keys = append(keys, newKeys(leaf, key)...)
			continue
		}
		keys = append(keys, key)
	}
	return keys
}
