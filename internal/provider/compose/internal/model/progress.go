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

import "github.com/greenmaskio/arcave/pkg/progress"

// NewRestoreSample is the sample a restored unit ends with: its size.
func NewRestoreSample(size int64) progress.Sample {
	return progress.Sample{Bytes: &progress.Bytes{
		Done:  size,
		Total: size,
	}}
}
