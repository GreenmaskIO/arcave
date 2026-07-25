package utils

import (
	"bytes"
	"encoding/json"
	"io"
	"testing"

	"github.com/rs/zerolog"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// bridgeLogrus installs the bridge onto a fresh buffer and restores logrus's
// process-wide state afterwards — the bridge configures globals, so a test that
// left them set would leak into the next one.
func bridgeLogrus(t *testing.T, level zerolog.Level) *bytes.Buffer {
	t.Helper()

	originalLevel := logrus.GetLevel()
	originalOut := logrus.StandardLogger().Out
	t.Cleanup(func() {
		logrus.StandardLogger().ReplaceHooks(logrus.LevelHooks{})
		logrus.SetLevel(originalLevel)
		logrus.SetOutput(originalOut)
	})
	logrus.StandardLogger().ReplaceHooks(logrus.LevelHooks{})

	buf := &bytes.Buffer{}
	BridgeLogrus(zerolog.New(buf).Level(level), level)
	return buf
}

func TestBridgeLogrus(t *testing.T) {
	tests := []struct {
		name   string
		level  zerolog.Level
		emit   func()
		verify func(t *testing.T, records []map[string]any)
	}{
		{
			name:  "a warning keeps its level, message and fields",
			level: zerolog.InfoLevel,
			emit: func() {
				logrus.WithField("container", "arcave-demo-app-1").Warn("no logs are available with the local driver")
			},
			verify: func(t *testing.T, records []map[string]any) {
				require.Len(t, records, 1)
				assert.Equal(t, "warn", records[0]["level"])
				assert.Equal(t, "no logs are available with the local driver", records[0]["message"])
				assert.Equal(t, "arcave-demo-app-1", records[0]["container"])
				assert.Equal(t, "logrus", records[0]["source"])
			},
		},
		{
			name:  "an error keeps its level",
			level: zerolog.InfoLevel,
			emit:  func() { logrus.Error("failed to resolve image") },
			verify: func(t *testing.T, records []map[string]any) {
				require.Len(t, records, 1)
				assert.Equal(t, "error", records[0]["level"])
			},
		},
		{
			name:  "debug is filtered out at the default level",
			level: zerolog.InfoLevel,
			emit:  func() { logrus.Debug("resolving image") },
			verify: func(t *testing.T, records []map[string]any) {
				assert.Empty(t, records)
			},
		},
		{
			name:  "debug reaches the log once arcave asks for it",
			level: zerolog.DebugLevel,
			emit:  func() { logrus.Debug("resolving image") },
			verify: func(t *testing.T, records []map[string]any) {
				require.Len(t, records, 1)
				assert.Equal(t, "debug", records[0]["level"])
				assert.Equal(t, "resolving image", records[0]["message"])
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Arrange
			buf := bridgeLogrus(t, tt.level)

			// Act
			tt.emit()

			// Assert
			var records []map[string]any
			for line := range bytes.SplitSeq(bytes.TrimSpace(buf.Bytes()), []byte("\n")) {
				if len(line) == 0 {
					continue
				}
				var record map[string]any
				require.NoError(t, json.Unmarshal(line, &record), "log line %q", line)
				records = append(records, record)
			}
			tt.verify(t, records)
		})
	}
}

// The two levels that cannot be exercised through logrus itself — firing one
// kills or panics the process on the way out of the hook — are pinned here.
// Collapsing them onto Error is deliberate: the bridge relays a record, it does
// not decide the process's fate, and re-issuing at zerolog's own Fatal would
// call os.Exit from inside a log call.
func TestZerologLevel(t *testing.T) {
	tests := []struct {
		name string
		from logrus.Level
		want zerolog.Level
	}{
		{name: "panic collapses onto error", from: logrus.PanicLevel, want: zerolog.ErrorLevel},
		{name: "fatal collapses onto error", from: logrus.FatalLevel, want: zerolog.ErrorLevel},
		{name: "error", from: logrus.ErrorLevel, want: zerolog.ErrorLevel},
		{name: "warn", from: logrus.WarnLevel, want: zerolog.WarnLevel},
		{name: "info", from: logrus.InfoLevel, want: zerolog.InfoLevel},
		{name: "debug", from: logrus.DebugLevel, want: zerolog.DebugLevel},
		{name: "trace joins debug", from: logrus.TraceLevel, want: zerolog.DebugLevel},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Act
			got := zerologLevel(tt.from)

			// Assert
			assert.Equal(t, tt.want, got)
		})
	}
}

// logrus keeps printing its own WARN[0000] format alongside any hook, so the
// bridge has to silence it — otherwise "one output channel" would just mean one
// more.
func TestBridgeLogrus_SilencesLogrusOwnOutput(t *testing.T) {
	// Arrange
	bridgeLogrus(t, zerolog.InfoLevel)

	// Act
	logrus.Warn("no logs are available with the local driver")

	// Assert
	assert.Equal(t, io.Discard, logrus.StandardLogger().Out)
}
