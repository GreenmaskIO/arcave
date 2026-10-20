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

// GenerateComposeRequest asks for the compose file of a live compose project.
type GenerateComposeRequest struct {
	// AbsolutePaths keeps every bind source absolute.
	AbsolutePaths bool
	// Dir is the directory the file will live in, which bind sources are made relative
	// to; empty is the compose project's working directory.
	Dir string
}

// GenerateComposeResponse is the generated compose file and what it declares; Path is set
// once the file is written.
type GenerateComposeResponse struct {
	ComposeProject DockerComposeProjectName `json:"compose_project"`
	Services       []string                 `json:"services"`
	Path           string                   `json:"path,omitempty"`
	Spec           []byte                   `json:"-"`
}
