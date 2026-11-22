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

package hold

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/rs/zerolog"

	composeconfig "github.com/greenmaskio/arcave/internal/provider/compose/config"
	composemodel "github.com/greenmaskio/arcave/internal/provider/compose/internal/model"
	"github.com/greenmaskio/arcave/pkg/progress"
)

// maxOutputLine is how much of a failed hook's last line of output its error quotes.
const maxOutputLine = 200

// The warnings of a hook not run in a replica.
const (
	warnPreHookSkipped = "%s was %s: pre-capture hook skipped"
	warnHookNotRunning = "%s is not running: %s hook skipped"
)

// hookTarget is one hook in one replica.
type hookTarget struct {
	ref  composemodel.ContainerRef
	hook composemodel.Hook
}

// hookTrigger runs the pre-capture hooks before a capture and the post-capture ones after it.
type hookTrigger struct {
	control ContainerControl
	pre     []hookTarget
	// skippedPre are the replicas of the services with a pre-capture hook found not running.
	skippedPre []composemodel.IdleReplica
	post       []hookTarget
}

// newHookTrigger lays out the hooks of services: a pre-capture hook in each running replica, a
// post-capture one in each replica, which all run after the capture.
func newHookTrigger(control ContainerControl, services []composemodel.ServiceHold) hookTrigger {
	var (
		pre        []hookTarget
		skippedPre []composemodel.IdleReplica
		post       []hookTarget
	)
	for _, service := range services {
		if service.PreCapture != nil {
			pre = append(pre, newHookTargets(service.Running, *service.PreCapture)...)
			skippedPre = append(skippedPre, service.Idle...)
		}
		if service.PostCapture != nil {
			replicas := slices.Clone(service.Running)
			for _, idle := range service.Idle {
				replicas = append(replicas, idle.Ref)
			}
			slices.SortStableFunc(replicas, func(a, b composemodel.ContainerRef) int { return a.Replica - b.Replica })
			post = append(post, newHookTargets(replicas, *service.PostCapture)...)
		}
	}
	return hookTrigger{
		control:    control,
		pre:        pre,
		skippedPre: skippedPre,
		post:       post,
	}
}

// beforeCapture runs the pre-capture hooks and returns the failures let through; a replica not
// running is skipped with a warning.
func (t hookTrigger) beforeCapture(ctx context.Context) (Outcome, error) {
	step := progress.Current(ctx)
	for _, idle := range t.skippedPre {
		step.Warn(fmt.Sprintf(warnPreHookSkipped, newReplicaName(idle.Ref), idle.State))
	}
	failedHooks, err := runHooks(ctx, t.control, composemodel.HookPhasePreCapture, t.pre)
	if err != nil {
		return Outcome{}, err
	}
	return Outcome{FailedHooks: failedHooks}, nil
}

// afterCapture runs the post-capture hooks; the failures let through are only warned of, the
// record being committed already.
func (t hookTrigger) afterCapture(revertCtx context.Context) error {
	_, err := runHooks(revertCtx, t.control, composemodel.HookPhasePostCapture, t.post)
	return err
}

// newHookLogger is the logger of ctx naming the hook of phase in the replica.
func newHookLogger(ctx context.Context, phase composemodel.HookPhase, ref composemodel.ContainerRef) zerolog.Logger {
	return zerolog.Ctx(ctx).With().
		Str("service", ref.Service).
		Int("replica", ref.Replica).
		Str("container", ref.Name).
		Str("hook", string(phase)).
		Logger()
}

// newHookTargets is hook in each of replicas.
func newHookTargets(replicas []composemodel.ContainerRef, hook composemodel.Hook) []hookTarget {
	targets := make([]hookTarget, 0, len(replicas))
	for _, ref := range replicas {
		targets = append(targets, hookTarget{
			ref:  ref,
			hook: hook,
		})
	}
	return targets
}

// runHooks runs every target at once, a failure cancelling none of the others. It returns the
// failures on-error continue let through, and the first by the targets' order of those fail did
// not, naming how many more; an interrupt outranks them.
func runHooks(
	ctx context.Context, control ContainerControl, phase composemodel.HookPhase, targets []hookTarget,
) (_ []composemodel.FailedHook, err error) {
	if len(targets) == 0 {
		return nil, nil
	}
	ctx, batch := progress.Batch(ctx, progress.Step{Kind: newHookBatchKind(phase)}, len(targets))
	defer func() { batch.End(err) }()

	var (
		errs = make([]error, len(targets))
		wg   sync.WaitGroup
	)
	for i, target := range targets {
		wg.Go(func() {
			errs[i] = runHook(ctx, control, phase, target)
		})
	}
	wg.Wait()

	var (
		failedHooks []composemodel.FailedHook
		failures    []*hookError
	)
	for _, runErr := range errs {
		if runErr == nil {
			continue
		}
		hookErr, isHookErr := errors.AsType[*hookError](runErr)
		switch {
		case !isHookErr:
			// An interrupt: the command is being stopped, whatever the hooks did.
			return nil, runErr
		case hookErr.target.hook.OnError == composeconfig.HookOnErrorContinue:
			failedHooks = append(failedHooks, hookErr.newFailedHook())
		default:
			failures = append(failures, hookErr)
		}
	}
	if len(failures) == 0 {
		return failedHooks, nil
	}
	return nil, joinHookFailures(failures)
}

// runHook runs one hook in one replica, under its timeout, and reports how it went. It returns
// the hook's failure, a *hookError, whatever its on-error; a replica not running is skipped with a
// warning, and an interrupt is returned as it is.
func runHook(ctx context.Context, control ContainerControl, phase composemodel.HookPhase, target hookTarget) error {
	var (
		ref  = target.ref
		hook = target.hook
		name = newReplicaName(ref)
		log  = newHookLogger(ctx, phase, ref)
	)
	ctx, step := progress.Run(log.WithContext(ctx), progress.Step{
		Kind: kindHook,
		ID:   ref.Name,
		Name: name,
	})
	log.Debug().Strs("command", hook.Command).Str("user", hook.User).Dur("timeout", hook.Timeout).Msg("hook started")

	hookCtx, cancel := context.WithTimeout(ctx, hook.Timeout)
	defer cancel()
	result, err := control.ExecInContainer(hookCtx, ref.ID, composemodel.ExecSpec{
		Cmd:  hook.Command,
		User: hook.User,
	})
	switch {
	case err != nil && ctx.Err() != nil:
		err = context.Cause(ctx)
		step.End(err)
		return err
	case errors.Is(err, composemodel.ErrContainerNotRunning):
		step.Warn(fmt.Sprintf(warnHookNotRunning, name, phase))
		step.End(nil)
		return nil
	case err == nil:
		log.Debug().Int("exit_code", result.ExitCode).
			Str("stdout", string(result.Stdout)).Str("stderr", string(result.Stderr)).Msg("hook finished")
	}

	hookErr := newHookError(phase, target, result, err)
	switch {
	case hookErr == nil:
		step.End(nil)
		return nil
	case hook.OnError != composeconfig.HookOnErrorContinue:
		step.Fail(string(newHookOutput(result)), hookErr)
		return hookErr
	case phase == composemodel.HookPhasePreCapture:
		// The result says it: the record carries the failure.
		step.WarnInResult(hookErr.Error() + "; continuing")
	default:
		step.Warn(hookErr.Error() + "; continuing")
	}
	step.End(nil)
	return hookErr
}

// newHookError is how a hook that ran failed, nil when it exited 0: an exit, its own timeout, or
// the daemon's error.
func newHookError(phase composemodel.HookPhase, target hookTarget, result composemodel.ExecResult, err error) *hookError {
	switch {
	case err == nil && result.ExitCode == 0:
		return nil
	case err == nil:
		exitCode := result.ExitCode
		return &hookError{
			phase:    phase,
			target:   target,
			exitCode: &exitCode,
			reason:   newExitReason(exitCode, newHookOutput(result)),
		}
	case errors.Is(err, context.DeadlineExceeded):
		// The workflow's own end returned before: a deadline here is the hook's timeout.
		return &hookError{
			phase:  phase,
			target: target,
			reason: "timed out after " + target.hook.Timeout.String(),
		}
	default:
		return &hookError{
			phase:  phase,
			target: target,
			reason: err.Error(),
			cause:  err,
		}
	}
}

// newHookOutput is the stream a failed hook is explained by: its stderr, else its stdout, where
// Docker writes why the runtime could not start the command, a 127 or a 126.
func newHookOutput(result composemodel.ExecResult) []byte {
	if len(bytes.TrimSpace(result.Stderr)) > 0 {
		return result.Stderr
	}
	return result.Stdout
}

// newExitReason is a hook's exit code and the last line of its output, when it wrote one.
func newExitReason(exitCode int, output []byte) string {
	line := newLastLine(output)
	if line == "" {
		return fmt.Sprintf("exit %d", exitCode)
	}
	return fmt.Sprintf("exit %d: %s", exitCode, line)
}

// hookError is how one hook in one replica failed.
type hookError struct {
	phase  composemodel.HookPhase
	target hookTarget
	// exitCode is nil for a hook that did not exit: it timed out, or the daemon failed it.
	exitCode *int
	// reason says how it failed: its exit, its timeout or the daemon's error.
	reason string
	// cause is the daemon's error; nil for an exit or a timeout.
	cause error
}

func (e *hookError) Error() string {
	return fmt.Sprintf("%s hook of %s failed: %s", e.phase, newReplicaName(e.target.ref), e.reason)
}

func (e *hookError) Unwrap() error {
	return e.cause
}

// newFailedHook is the record of the hook let through.
func (e *hookError) newFailedHook() composemodel.FailedHook {
	return composemodel.FailedHook{
		Service:   e.target.ref.Service,
		Replica:   e.target.ref.Replica,
		Container: e.target.ref.Name,
		Hook:      e.phase,
		ExitCode:  e.exitCode,
		Error:     e.reason,
	}
}

// joinHookFailures is the first failure, naming how many more there were.
func joinHookFailures(failures []*hookError) error {
	switch more := len(failures) - 1; more {
	case 0:
		return failures[0]
	case 1:
		return fmt.Errorf("%w; and 1 more hook failed", failures[0])
	default:
		return fmt.Errorf("%w; and %d more hooks failed", failures[0], more)
	}
}

// newLastLine is the last line of output that is not blank, cut to maxOutputLine characters.
func newLastLine(output []byte) string {
	trimmed := strings.TrimSpace(string(output))
	line := strings.TrimSpace(trimmed[strings.LastIndexByte(trimmed, '\n')+1:])
	if utf8.RuneCountInString(line) <= maxOutputLine {
		return line
	}
	return string([]rune(line)[:maxOutputLine]) + "…"
}
