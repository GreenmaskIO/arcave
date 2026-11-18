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

import "time"

// HookPhase names a hook by when it runs, as its labels and its messages spell it.
type HookPhase string

const (
	// HookPhasePreCapture runs before a capture holds the project still.
	HookPhasePreCapture HookPhase = "pre-capture"
	// HookPhasePostCapture runs once the project is brought back.
	HookPhasePostCapture HookPhase = "post-capture"
)

// HookOnError is what a failed hook does to the capture.
type HookOnError string

const (
	// HookOnErrorFail fails the workflow.
	HookOnErrorFail HookOnError = "fail"
	// HookOnErrorContinue warns and goes on.
	HookOnErrorContinue HookOnError = "continue"
)

// ParseHookOnError validates an on-error token, as config and labels spell it.
func ParseHookOnError(token string) (HookOnError, bool) {
	switch onError := HookOnError(token); onError {
	case HookOnErrorFail, HookOnErrorContinue:
		return onError, true
	default:
		return "", false
	}
}

// Hook is one command run in each replica of a service, its defaults applied.
type Hook struct {
	// Command is the argv, run with no shell.
	Command []string
	// User runs it; empty is the container's user.
	User    string
	Timeout time.Duration
	OnError HookOnError
}
