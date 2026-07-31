package cdc

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/rs/zerolog"

	"github.com/greenmaskio/arcave/internal/model"
)

// The restic --json message types this backend reads; the rest goes to the debug
// log unread.
const (
	messageTypeSummary = "summary"
	messageTypeStatus  = "status"
	messageTypeError   = "error"
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

// runner executes restic commands in one-shot containers of the stock image,
// against one repository URL.
type runner struct {
	containers ContainerRunner
	image      string
	repoURL    string
}

// run executes one command and returns its summary line, zero-valued when the
// command printed none. A non-zero exit comes back as the one-shot mechanism's
// *model.ContainerExitError, carrying the container's own output.
func (r runner) run(ctx context.Context, cmd resticCmd) (resticSummary, error) {
	log := zerolog.Ctx(ctx)

	var summary resticSummary
	stdout := &lineWriter{emit: func(line string) { readResticLine(log, line, &summary) }}
	stderr := &lineWriter{emit: func(line string) { log.Warn().Str("restic", line).Msg("restic stderr") }}

	log.Debug().Strs("args", cmd.args).Msg("running restic")
	err := r.containers.RunOneShot(ctx, model.OneShotSpec{
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
		return resticSummary{}, fmt.Errorf("restic %s: %w", strings.Join(cmd.args, " "), err)
	}
	return summary, nil
}

// readResticLine routes one line of restic's ndjson. A line that is not JSON is
// logged rather than dropped: restic prints warnings that way.
func readResticLine(log *zerolog.Logger, line string, summary *resticSummary) {
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
	case messageTypeError:
		log.Warn().Str("restic", line).Msg("restic reported an error")
	default:
		log.Debug().Str("restic", line).Msg("restic stdout")
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
