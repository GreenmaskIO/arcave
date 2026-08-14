package cdc

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/rs/zerolog"

	dockermodel "github.com/greenmaskio/arcave/internal/provider/docker/internal/model"
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

// containerRunner is all this file needs of the engine: one throwaway container
// per restic command. The volume half of VolumeRunner is the snapshotter's.
type containerRunner interface {
	RunOneShot(ctx context.Context, spec dockermodel.OneShotSpec) error
}

// runner executes restic commands in one-shot containers of the stock image,
// against one repository URL.
type runner struct {
	containers containerRunner
	image      string
	repoURL    string
}

// run executes one command and returns its summary line, zero-valued when the
// command printed none. A non-zero exit comes back as the one-shot mechanism's
// *dockermodel.ContainerExitError, carrying the container's own output — fronted by
// restic's own diagnosis when it printed one, since "exited with status 1" says
// nothing a user can act on.
func (r runner) run(ctx context.Context, cmd resticCmd) (resticSummary, error) {
	log := zerolog.Ctx(ctx)

	var summary resticSummary
	var fatal string
	stdout := &lineWriter{emit: func(line string) { readResticStdout(log, line, &summary) }}
	stderr := &lineWriter{emit: func(line string) { readResticStderr(log, line, &fatal) }}

	log.Debug().Strs("args", cmd.args).Msg("running restic")
	err := r.containers.RunOneShot(ctx, dockermodel.OneShotSpec{
		Image: r.image,
		Cmd:   cmd.args,
		Env: []string{
			"RESTIC_REPOSITORY=" + r.repoURL,
			"RESTIC_PASSWORD=" + resticPassword,
		},
		Binds:  cmd.binds,
		Stdout: stdout,
		Stderr: stderr,
	})
	// Flushed whatever the outcome: the last line of a failing run is the one
	// worth having.
	stdout.flush()
	stderr.flush()
	if err != nil {
		if fatal != "" {
			return resticSummary{}, fmt.Errorf("restic %s: %s: %w", strings.Join(cmd.args, " "), fatal, err)
		}
		return resticSummary{}, fmt.Errorf("restic %s: %w", strings.Join(cmd.args, " "), err)
	}
	return summary, nil
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
		// Not every failure is structured: a repository restic cannot open is
		// reported before --json takes effect.
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
