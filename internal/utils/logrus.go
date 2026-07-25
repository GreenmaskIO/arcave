package utils

import (
	"io"

	"github.com/rs/zerolog"
	"github.com/sirupsen/logrus"
)

// logrusSourceField marks a record as one arcave relayed rather than wrote, so a
// reader can tell arcave's own words from a dependency's.
const logrusSourceField = "logrus"

// BridgeLogrus routes logrus onto logger, so arcave's output stays one channel in
// one format.
//
// It exists because the compose library, docker/cli and buildkit log through the
// package-level logrus logger — dozens of Warnf/Debugf calls that reach no stream
// arcave hands them and render in logrus's own `WARN[0000] …` format. Under
// log.format: json those lines are raw text in the middle of a machine-readable
// stream, which is exactly the mode `arcave mcp` and CI run in.
//
// level is the resolved arcave log level, and setting it on logrus is not
// optional: logrus filters at Info by default, so without this `--log-level
// debug` would show none of compose's debug output. Leaving logrus at Debug
// always would instead pay for formatting every record zerolog then discards.
//
// It configures process-wide globals, so it belongs to the composition root and
// is called once, before anything in docker or compose has had a chance to log.
func BridgeLogrus(logger zerolog.Logger, level zerolog.Level) {
	// logrus keeps writing its own format to stderr even with a hook attached,
	// so its output is silenced and the hook becomes the only path out.
	logrus.SetOutput(io.Discard)
	logrus.SetLevel(logrusLevel(level))
	logrus.AddHook(&logrusBridge{logger: logger})
}

// logrusBridge re-emits every logrus record as a zerolog one. A hook, rather
// than a logrus.Formatter over a writer, because a hook receives the entry with
// its fields still structured — a formatter would have to render them to text
// for zerolog to re-parse.
type logrusBridge struct {
	logger zerolog.Logger
}

// Levels reports every level, because the filtering is logrus's own: BridgeLogrus
// sets it from arcave's resolved level, and a hook that filtered again would only
// be a second place to keep the two in step.
func (b *logrusBridge) Levels() []logrus.Level {
	return logrus.AllLevels
}

// Fire re-issues one entry. It never fails: a record arcave could not relay is not
// worth failing the caller's own log call over, and logrus would print the error
// to the output that was just discarded anyway.
func (b *logrusBridge) Fire(entry *logrus.Entry) error {
	record := b.logger.WithLevel(zerologLevel(entry.Level)).Str("source", logrusSourceField)
	for key, value := range entry.Data {
		record = record.Interface(key, value)
	}
	record.Msg(entry.Message)
	return nil
}

// logrusLevel is the level to filter logrus at so that it emits exactly what
// zerolog will keep. Anything arcave cannot name (Disabled, NoLevel) becomes the
// quietest logrus has.
func logrusLevel(level zerolog.Level) logrus.Level {
	switch level {
	case zerolog.TraceLevel:
		return logrus.TraceLevel
	case zerolog.DebugLevel:
		return logrus.DebugLevel
	case zerolog.InfoLevel:
		return logrus.InfoLevel
	case zerolog.WarnLevel:
		return logrus.WarnLevel
	case zerolog.ErrorLevel:
		return logrus.ErrorLevel
	case zerolog.FatalLevel:
		return logrus.FatalLevel
	default:
		return logrus.PanicLevel
	}
}

// zerologLevel maps a logrus level onto zerolog's. Panic and Fatal collapse into
// Error: the record is relayed, not acted on — whether the process dies is
// logrus's own decision, made after its hooks have run, and re-issuing the record
// at zerolog's Fatal would kill the process from inside a log call.
func zerologLevel(level logrus.Level) zerolog.Level {
	switch level {
	case logrus.PanicLevel, logrus.FatalLevel, logrus.ErrorLevel:
		return zerolog.ErrorLevel
	case logrus.WarnLevel:
		return zerolog.WarnLevel
	case logrus.InfoLevel:
		return zerolog.InfoLevel
	case logrus.DebugLevel, logrus.TraceLevel:
		return zerolog.DebugLevel
	default:
		return zerolog.InfoLevel
	}
}
