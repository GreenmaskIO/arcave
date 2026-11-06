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

package utils

import (
	"fmt"
	"io"
	"os"
	"time"

	"github.com/rs/zerolog"
)

const (
	LogFormatJson = "json"
	LogFormatText = "text"
)

// DefaultLogLevel is the level of a log no level was set for; DefaultLogFormat the global flag
// default, reused by init when persisting log settings into the project config.
var (
	DefaultLogLevel  = zerolog.LevelInfoValue
	DefaultLogFormat = LogFormatText
)

var (
	errUnknownLogLevel  = fmt.Errorf("unknown log level")
	errUnknownLogFormat = fmt.Errorf("unknown log format")
)

// NewLogger builds the logger writing to out at levelName in logFormat.
func NewLogger(levelName string, logFormat string, out io.Writer) (zerolog.Logger, error) {

	var logLevel zerolog.Level
	switch levelName {
	case zerolog.LevelDebugValue:
		logLevel = zerolog.DebugLevel
	case zerolog.LevelInfoValue:
		logLevel = zerolog.InfoLevel
	case zerolog.LevelWarnValue:
		logLevel = zerolog.WarnLevel
	default:
		return zerolog.Logger{}, fmt.Errorf("log level %s: %w", levelName, errUnknownLogLevel)
	}

	var formatWriter io.Writer
	switch logFormat {
	case LogFormatJson:
		formatWriter = out
	case LogFormatText:
		formatWriter = zerolog.ConsoleWriter{Out: out, TimeFormat: time.RFC3339}
	default:
		// zerolog discards everything written to a nil writer, so an unknown
		// format would otherwise silence every log line.
		return zerolog.Logger{}, fmt.Errorf("log format %s: %w", logFormat, errUnknownLogFormat)
	}

	if levelName == zerolog.LevelDebugValue {
		return zerolog.New(formatWriter).
			Level(logLevel).
			With().
			Timestamp().
			Caller().
			Int("pid", os.Getpid()).Logger(), nil
	}
	return zerolog.New(formatWriter).
		Level(logLevel).
		With().
		Timestamp().
		Logger(), nil
}

// SetDefaultContextLogger builds the logger and installs it as zerolog's default
// for a context that carries none. It returns the logger it installed, because
// the composition root also has to hand it to collaborators that take one
// explicitly — the logrus bridge and the compose progress adapter.
func SetDefaultContextLogger(levelName string, logFormat string, out io.Writer) (zerolog.Logger, error) {
	logger, err := NewLogger(levelName, logFormat, out)
	if err != nil {
		return zerolog.Logger{}, fmt.Errorf("get logger: %w", err)
	}
	zerolog.DefaultContextLogger = &logger
	return logger, nil
}
