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

// ExecOutputTail is how many bytes of each of an exec's streams ExecResult keeps, the last ones.
const ExecOutputTail = 4 << 10

// ExecSpec is one command run in a running container.
type ExecSpec struct {
	Cmd []string
	// User runs it; empty is the container's user.
	User string
}

// ExecResult is how an exec that ran to its end ended; its streams are cut to their last
// ExecOutputTail bytes.
type ExecResult struct {
	ExitCode int
	Stdout   []byte
	Stderr   []byte
}
