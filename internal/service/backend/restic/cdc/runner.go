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

package cdc

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/rs/zerolog"

	"github.com/greenmaskio/arcave/internal/model"
)

// The restic --json message types this backend reads; the rest goes to the debug
// log unread. The two streams carry different ones and never overlap: measured
// against restic 0.19.1, stdout is status/summary (and init's own initialized),
// while every error — per item during a run, and the fatal one last — is stderr's.
const (
	messageTypeSummary   = "summary"
	messageTypeStatus    = "status"
	messageTypeError     = "error"
	messageTypeExitError = "exit_error"
)

// fatalPrefix opens the last line of a run without --json that failed.
const fatalPrefix = "Fatal: "

// resticCmd is one restic invocation: the arguments after "restic", and the
// binds the container needs. Built before any container starts, so the execution
// strategy stays this file's business.
type resticCmd struct {
	args  []string
	binds []string
}

// resticSummary is the summary line of a --json run. Only backup fills the byte
// counters; restore leaves them zero.
type resticSummary struct {
	MessageType         string `json:"message_type"`
	SnapshotID          string `json:"snapshot_id"`
	DataAdded           int64  `json:"data_added"`
	TotalBytesProcessed int64  `json:"total_bytes_processed"`
}

// resticProblem is a line of restic's stderr: one per item a run could not
// handle, and one exit_error last, carrying the diagnosis the CLI would have
// printed. Error/During/Item are the error message's; Message is exit_error's.
type resticProblem struct {
	MessageType string `json:"message_type"`
	Error       struct {
		Message string `json:"message"`
	} `json:"error"`
	During  string `json:"during"`
	Item    string `json:"item"`
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// ContainerRunner is all this file needs of the engine: one throwaway container
// per restic command. The volume half of VolumeRunner is the snapshotter's.
type ContainerRunner interface {
	RunOneShot(ctx context.Context, spec model.OneShotSpec) error
}

// repositories is what reaching any repository through restic takes.
type repositories struct {
	containers  ContainerRunner
	repoURLBase string
	options     Options
}

func newRepositories(containers ContainerRunner, repoURLBase string, options Options) repositories {
	return repositories{
		containers:  containers,
		repoURLBase: repoURLBase,
		options:     options,
	}
}

// newRunner is a runner to objectID's repository, without clearing its locks; an objectID that is
// no volume's is refused.
func (r repositories) newRunner(objectID string) (runner, error) {
	if err := requireRepository(objectID); err != nil {
		return runner{}, err
	}
	return runner{
		containers:  r.containers,
		image:       r.options.Image,
		repoURL:     newRepositoryURL(r.repoURLBase, objectID),
		hostNetwork: r.options.HostNetwork,
	}, nil
}

// newRepositoryURL is base plus the repository's directory, objectID. The trailing
// slash is the restic protocol's: without it restic talks to the wrong path.
func newRepositoryURL(base, objectID string) string {
	return base + "/" + objectID + "/"
}

// runner executes restic commands in one-shot containers of the stock image,
// against one repository URL.
type runner struct {
	containers  ContainerRunner
	image       string
	repoURL     string
	hostNetwork bool
}

// run executes one command and returns its summary line, zero-valued when the
// command printed none. A non-zero exit comes back as the one-shot mechanism's
// *model.ContainerExitError, carrying the container's own output — fronted by
// restic's own diagnosis when it printed one, since "exited with status 1" says
// nothing a user can act on.
func (r runner) run(ctx context.Context, cmd resticCmd) (resticSummary, error) {
	log := zerolog.Ctx(ctx)

	var summary resticSummary
	stdout := &lineWriter{emit: func(line string) { readResticStdout(log, line, &summary) }}
	err := r.exec(ctx, cmd, stdout)
	stdout.flush()
	if err != nil {
		return resticSummary{}, err
	}
	return summary, nil
}

// output executes one command and returns its stdout whole, for a command whose
// output is one document rather than ndjson lines.
func (r runner) output(ctx context.Context, cmd resticCmd) ([]byte, error) {
	var stdout bytes.Buffer
	if err := r.exec(ctx, cmd, &stdout); err != nil {
		return nil, err
	}
	return stdout.Bytes(), nil
}

// exec runs one command in a one-shot container, its stdout to stdout; stderr is
// logged, and its fatal line fronts the error of a failed run.
func (r runner) exec(ctx context.Context, cmd resticCmd, stdout io.Writer) error {
	log := zerolog.Ctx(ctx)

	var fatal string
	stderr := &lineWriter{emit: func(line string) { readResticStderr(log, line, &fatal) }}

	log.Debug().Strs("args", cmd.args).Msg("running restic")
	err := r.containers.RunOneShot(ctx, model.OneShotSpec{
		Image: r.image,
		Cmd:   cmd.args,
		Env: []string{
			"RESTIC_REPOSITORY=" + r.repoURL,
			"RESTIC_PASSWORD=" + resticPassword,
		},
		Binds:       cmd.binds,
		HostNetwork: r.hostNetwork,
		Stdout:      stdout,
		Stderr:      stderr,
	})
	// Flushed whatever the outcome: the last line of a failing run is the one
	// worth having.
	stderr.flush()
	if err != nil {
		if fatal != "" {
			return fmt.Errorf("restic %s: %s: %w", strings.Join(cmd.args, " "), fatal, err)
		}
		return fmt.Errorf("restic %s: %w", strings.Join(cmd.args, " "), err)
	}
	return nil
}

// readResticStdout routes one line of restic's ndjson. A line that is not JSON
// is logged rather than dropped: restic prints warnings that way, and so does
// anything the image writes that is not restic.
func readResticStdout(log *zerolog.Logger, line string, summary *resticSummary) {
	var msg resticSummary
	if err := json.Unmarshal([]byte(line), &msg); err != nil {
		log.Debug().Str("restic", line).Msg("restic stdout")
		return
	}
	switch msg.MessageType {
	case messageTypeSummary:
		*summary = msg
	case messageTypeStatus:
		log.Debug().Str("restic", line).Msg("restic progress")
	default:
		// init's "initialized" lands here, as does anything a newer restic adds.
		log.Debug().Str("restic", line).Msg("restic stdout")
	}
}

// readResticStderr logs what a run could not do and keeps the fatal message for
// the caller to return. Only the last exit_error is kept: restic prints one, and
// it is the line the CLI would have ended on.
func readResticStderr(log *zerolog.Logger, line string, fatal *string) {
	var msg resticProblem
	if err := json.Unmarshal([]byte(line), &msg); err != nil {
		// An unopenable repository fails before --json takes effect, and a run
		// without --json ends on a plain "Fatal: " line.
		if message, isFatal := strings.CutPrefix(line, fatalPrefix); isFatal {
			*fatal = message
		}
		log.Warn().Str("restic", line).Msg("restic stderr")
		return
	}
	switch msg.MessageType {
	case messageTypeError:
		log.Warn().
			Str("restic", msg.Error.Message).
			Str("during", msg.During).
			Str("item", msg.Item).
			Msg("restic reported an error")
	case messageTypeExitError:
		*fatal = msg.Message
		log.Warn().Str("restic", msg.Message).Int("code", msg.Code).Msg("restic failed")
	default:
		log.Warn().Str("restic", line).Msg("restic stderr")
	}
}

// lineWriter splits a stream into lines and hands each complete one to emit. A
// Writer rather than a pipe-and-scanner: the one-shot mechanism already owns the
// goroutine draining the container's output.
type lineWriter struct {
	buf  bytes.Buffer
	emit func(line string)
}

func (w *lineWriter) Write(p []byte) (int, error) {
	w.buf.Write(p)
	for {
		line, err := w.buf.ReadString('\n')
		if err != nil {
			// No newline yet: keep the partial line and wait for the rest.
			w.buf.Reset()
			w.buf.WriteString(line)
			break
		}
		w.emitLine(line)
	}
	return len(p), nil
}

// flush emits whatever is left without a trailing newline.
func (w *lineWriter) flush() {
	w.emitLine(w.buf.String())
	w.buf.Reset()
}

func (w *lineWriter) emitLine(line string) {
	if line = strings.TrimRight(line, "\r\n"); line != "" {
		w.emit(line)
	}
}
