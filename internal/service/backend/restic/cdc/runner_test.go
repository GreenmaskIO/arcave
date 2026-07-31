package cdc

import (
	"context"
	"strings"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/greenmaskio/arcave/internal/model"
)

// restic prints one ndjson object per line and one of them is the summary; the
// rest is progress, and a line that is not JSON at all is still restic talking.
func TestRunner_run(t *testing.T) {
	tests := []struct {
		name   string
		stdout []string
		stderr []string
		want   resticSummary
		// wantLogged is what the run must leave in the log, so a failure is
		// diagnosable from arcave's own output.
		wantLogged []string
	}{
		{
			name:       "the summary is the result, progress is debug noise",
			stdout:     []string{backupStatusLine, backupSummaryLine},
			want:       resticSummary{MessageType: "summary", SnapshotID: "3f8a1c0d9b", DataAdded: 1048576, TotalBytesProcessed: 10485760},
			wantLogged: []string{"restic progress"},
		},
		{
			name:   "a run that prints no summary yields the zero value",
			stdout: []string{backupStatusLine},
			want:   resticSummary{},
		},
		{
			name:       "a line that is not JSON is logged rather than dropped",
			stdout:     []string{"warning: could not read /m/volume/x"},
			wantLogged: []string{"could not read"},
		},
		{
			name:       "an error message goes out at warn",
			stdout:     []string{`{"message_type":"error","error":{"message":"boom"}}`},
			wantLogged: []string{"restic reported an error"},
		},
		{
			name:       "stderr is warned line by line",
			stderr:     []string{"Fatal: unable to open config file", "Is there a repository at the following location?"},
			wantLogged: []string{"unable to open config file", "Is there a repository"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			containers := &mockRunner{}
			containers.On("RunOneShot", mock.Anything, matchRestic("backup")).
				Run(func(args mock.Arguments) {
					spec := args.Get(1).(model.OneShotSpec)
					writeLines(spec.Stdout, tt.stdout)
					writeLines(spec.Stderr, tt.stderr)
				}).Return(nil).Once()
			logs := &strings.Builder{}
			ctx := zerolog.New(logs).Level(zerolog.DebugLevel).WithContext(context.Background())
			r := runner{containers: containers, image: demoImage, repoURL: demoRepoURL}

			// Act
			got, err := r.run(ctx, resticCmd{args: []string{"backup", "/m", "--json"}})

			// Assert
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
			for _, want := range tt.wantLogged {
				assert.Contains(t, logs.String(), want)
			}
			containers.AssertExpectations(t)
		})
	}
}

// The last line of a run that ended without a trailing newline is still read:
// it is the one a killed container most often leaves behind.
func TestRunner_run_UnterminatedLastLine(t *testing.T) {
	// Arrange
	containers := &mockRunner{}
	containers.On("RunOneShot", mock.Anything, matchRestic("backup")).
		Run(func(args mock.Arguments) {
			spec := args.Get(1).(model.OneShotSpec)
			_, _ = spec.Stdout.Write([]byte(backupSummaryLine))
		}).Return(nil).Once()
	ctx := zerolog.Nop().WithContext(context.Background())
	r := runner{containers: containers, image: demoImage, repoURL: demoRepoURL}

	// Act
	got, err := r.run(ctx, resticCmd{args: []string{"backup", "--json"}})

	// Assert
	require.NoError(t, err)
	assert.Equal(t, "3f8a1c0d9b", got.SnapshotID)
	containers.AssertExpectations(t)
}

// A line arriving in pieces — which is what a framed container stream does —
// is still one line by the time it is parsed.
func TestLineWriter_Write(t *testing.T) {
	tests := []struct {
		name   string
		chunks []string
		want   []string
	}{
		{name: "one whole line", chunks: []string{"alpha\n"}, want: []string{"alpha"}},
		{name: "two lines in one write", chunks: []string{"alpha\nbeta\n"}, want: []string{"alpha", "beta"}},
		{name: "one line split across writes", chunks: []string{"al", "pha", "\n"}, want: []string{"alpha"}},
		{name: "a trailing partial line is flushed", chunks: []string{"alpha\nbe", "ta"}, want: []string{"alpha", "beta"}},
		{name: "blank lines are dropped", chunks: []string{"\n\nalpha\n\n"}, want: []string{"alpha"}},
		{name: "carriage returns are trimmed", chunks: []string{"alpha\r\n"}, want: []string{"alpha"}},
		{name: "nothing written emits nothing", chunks: nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			var got []string
			w := &lineWriter{emit: func(line string) { got = append(got, line) }}

			// Act
			for _, chunk := range tt.chunks {
				n, err := w.Write([]byte(chunk))
				require.NoError(t, err)
				assert.Equal(t, len(chunk), n, "a Writer must report every byte consumed")
			}
			w.flush()

			// Assert
			assert.Equal(t, tt.want, got)
		})
	}
}

// writeLines writes each line with its newline, the way a container's stream
// would deliver them.
func writeLines(w interface{ Write([]byte) (int, error) }, lines []string) {
	for _, line := range lines {
		_, _ = w.Write([]byte(line + "\n"))
	}
}
