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

// DefaultLogLevel and DefaultLogFormat are the global flag defaults, reused by
// init when persisting log settings into the project config.
var (
	DefaultLogLevel  = zerolog.LevelInfoValue
	DefaultLogFormat = LogFormatText
)

var errUnknownLogFormat = fmt.Errorf("unknown log format")

func GetLogger(logLevelStr string, logFormat string) (zerolog.Logger, error) {

	var logLevel zerolog.Level
	switch logLevelStr {
	case zerolog.LevelDebugValue:
		logLevel = zerolog.DebugLevel
	case zerolog.LevelInfoValue:
		logLevel = zerolog.InfoLevel
	case zerolog.LevelWarnValue:
		logLevel = zerolog.WarnLevel
	default:
		return zerolog.Logger{}, fmt.Errorf("log level %s: %w", logLevelStr, errUnknownLogFormat)
	}

	var formatWriter io.Writer
	switch logFormat {
	case LogFormatJson:
		formatWriter = os.Stderr
	case LogFormatText:
		formatWriter = zerolog.ConsoleWriter{Out: os.Stderr, TimeFormat: time.RFC3339}
	}

	if logLevelStr == zerolog.LevelDebugValue {
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
func SetDefaultContextLogger(logLevelStr string, logFormat string) (zerolog.Logger, error) {
	logger, err := GetLogger(logLevelStr, logFormat)
	if err != nil {
		return zerolog.Logger{}, fmt.Errorf("get logger: %w", err)
	}
	zerolog.DefaultContextLogger = &logger
	return logger, nil
}
