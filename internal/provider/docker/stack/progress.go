package stack

import (
	"io"
	"os"

	"github.com/docker/compose/v2/pkg/progress"
	"github.com/rs/zerolog"

	arcavecompose "github.com/greenmaskio/arcave/internal/provider/docker/stack/internal/client/compose"
)

// composeProgressWriter translates compose.progress into the library's render
// mode plus the writer it renders on. Never stdout, which carries arcave's JSON.
func composeProgressWriter(mode string, logger zerolog.Logger) io.WriteCloser {
	switch mode {
	case ProgressQuiet:
		// Discarding the stream too covers the warnings compose writes outside the UI.
		progress.Mode = progress.ModeQuiet
		return nopWriteCloser{io.Discard}
	case ProgressTTY:
		// ModeAuto, not ModeTTY: compose falls back to plain lines in a pipe.
		progress.Mode = progress.ModeAuto
		return nopWriteCloser{os.Stderr}
	default:
		progress.Mode = progress.ModeJSON
		return arcavecompose.NewProgressLogger(logger)
	}
}

// nopWriteCloser makes a stream arcave does not own safe to hand to a Close.
type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }
