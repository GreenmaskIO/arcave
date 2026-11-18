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

// Package docs carries the OpenAPI document the api package is generated from,
// embedded in the binary so that what a reader is served is the contract the
// routes were built from, with no file to keep beside the executable.
package docs

import (
	_ "embed"
	"fmt"

	"github.com/swaggo/swag"
	"sigs.k8s.io/yaml"
)

// InstanceName is the name the document is registered under in swag's registry,
// and it is swag's own default, so a reader looking one up by nothing finds
// this one.
const InstanceName = "swagger"

//go:embed openapi.yaml
var yamlSpec []byte

// Spec is the document as JSON, which is what Swagger UI reads.
var Spec string

// DocReader hands the document to swag, whose registry Swagger UI reads it
// through.
type DocReader struct{}

// ReadDoc satisfies swag.Swagger.
func (*DocReader) ReadDoc() string {
	return Spec
}

// The document is embedded at build time, so a conversion failure is not a
// runtime condition to report but a malformed file that was compiled in — every
// run would fail identically. Panicking says so at once; the alternative was
// serving an empty document and finding out in a browser.
//
// Registering here rather than at assembly is not tidiness: swag's registry is
// process-wide and it panics on a second registration under one name, so a
// server built twice — a second repository, a second test — would take the
// process down with it. The document is a fact of this package, and this is the
// only place that is true once per process. swag's own generated packages
// register the same way.
func init() {
	jsonSpec, err := yaml.YAMLToJSON(yamlSpec)
	if err != nil {
		panic(fmt.Sprintf("openapi.yaml is not valid yaml: %v", err))
	}
	Spec = string(jsonSpec)
	swag.Register(InstanceName, &DocReader{})
}
