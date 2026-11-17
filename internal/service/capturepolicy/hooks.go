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

package capturepolicy

import (
	"cmp"
	"fmt"
	"slices"
	"time"

	"github.com/mattn/go-shellwords"

	"github.com/greenmaskio/arcave/internal/model"
)

// hookLabelKeys are the labels of one hook: its command and the settings beside it.
type hookLabelKeys struct {
	command model.CaptureLabel
	user    model.CaptureLabel
	timeout model.CaptureLabel
	onError model.CaptureLabel
}

// newHookLabelKeys are the labels of the hook of phase.
func newHookLabelKeys(phase model.HookPhase) hookLabelKeys {
	if phase == model.HookPhasePostCapture {
		return hookLabelKeys{
			command: model.CaptureLabelHookPostCapture,
			user:    model.CaptureLabelHookPostCaptureUser,
			timeout: model.CaptureLabelHookPostCaptureTimeout,
			onError: model.CaptureLabelHookPostCaptureOnError,
		}
	}
	return hookLabelKeys{
		command: model.CaptureLabelHookPreCapture,
		user:    model.CaptureLabelHookPreCaptureUser,
		timeout: model.CaptureLabelHookPreCaptureTimeout,
		onError: model.CaptureLabelHookPreCaptureOnError,
	}
}

// withHookDefaults is hook with the defaults filling the settings its labels leave unset; nil
// for nil.
func withHookDefaults(hook *model.Hook, defaults CaptureDefaults) *model.Hook {
	if hook == nil {
		return nil
	}
	return &model.Hook{
		Command: hook.Command,
		User:    hook.User,
		Timeout: cmp.Or(hook.Timeout, defaults.HookTimeout),
		OnError: cmp.Or(hook.OnError, defaults.HookOnError),
	}
}

// parseHookLabels reads the hook of phase off a service's labels, a zero Timeout or OnError being
// unset; nil when it has none.
func parseHookLabels(labels map[string]string, phase model.HookPhase, where string) (*model.Hook, error) {
	keys := newHookLabelKeys(phase)
	value, ok := labels[string(keys.command)]
	if !ok {
		for _, key := range []model.CaptureLabel{keys.user, keys.timeout, keys.onError} {
			if setting, set := labels[string(key)]; set {
				return nil, newInvalidLabelError(where, key, setting, "no "+keys.command.String()+" beside it")
			}
		}
		return nil, nil
	}

	var (
		command []string
		timeout time.Duration
		onError model.HookOnError
		err     error
	)
	if command, err = parseHookCommand(value); err != nil {
		return nil, newInvalidLabelError(where, keys.command, value, err.Error())
	}
	if setting, set := labels[string(keys.timeout)]; set {
		if timeout, err = time.ParseDuration(setting); err == nil && timeout <= 0 {
			err = fmt.Errorf("%s is not positive", timeout)
		}
		if err != nil {
			return nil, newInvalidLabelError(where, keys.timeout, setting, "want a positive Go duration, e.g. 30s: "+err.Error())
		}
	}
	if setting, set := labels[string(keys.onError)]; set {
		if onError, ok = model.ParseHookOnError(setting); !ok {
			return nil, newInvalidLabelError(where, keys.onError, setting, "want fail or continue")
		}
	}
	return &model.Hook{
		Command: command,
		User:    labels[string(keys.user)],
		Timeout: timeout,
		OnError: onError,
	}, nil
}

// parseHookCommand splits a hook's command into its argv as a shell would, expanding nothing.
func parseHookCommand(value string) ([]string, error) {
	parser := shellwords.NewParser()
	parser.ParseEnv = false
	parser.ParseBacktick = false
	parser.ParseComment = false
	argv, err := parser.Parse(value)
	switch {
	case err != nil:
		return nil, fmt.Errorf("%w: %w", errUnparsableCommand, err)
	case parser.Position >= 0:
		// The parser stops at an unquoted ;, &, |, < or >, returns only the words before it and
		// sets Position to that character, or to the fd of a redirect such as 2>; run as is, the
		// rest of the command would be dropped.
		return nil, fmt.Errorf("%w: %q at character %d; run the command through sh -c",
			errShellOperator, newShellOperator(value, parser.Position), parser.Position+1)
	case len(argv) == 0:
		return nil, errEmptyCommand
	}
	return argv, nil
}

// newShellOperator is the operator at rune pos of value, a redirect's fd included.
func newShellOperator(value string, pos int) string {
	rest := []rune(value)[pos:]
	end := slices.IndexFunc(rest, func(r rune) bool { return r < '0' || r > '9' })
	return string(rest[:end+1])
}
