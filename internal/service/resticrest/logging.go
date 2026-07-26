package resticrest

import (
	"context"
	"net/http"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

// Logging is a middleware rather than a line in each handler, for the same
// reason the path gate is one: what a request was and what it was answered with
// are both known here, and the status is known *nowhere else* — an http.Handler
// tells nobody what it wrote, and the mux's own 405 runs no handler of ours at
// all.
//
// What the layers below add is the *reason* for an answer that is not a plain
// 200, at the level that reason deserves, and they reach the logger the way
// every other arcave layer does: zerolog.Ctx(r.Context()), which this middleware
// is what guarantees is populated. Nothing below here resolves a logger itself.

// emptyContextLogger is what zerolog.Ctx hands back for a context that carries
// no logger of its own. It is read once, at init, so a
// zerolog.DefaultContextLogger the program installs later is honoured as a
// logger rather than mistaken for absence.
var emptyContextLogger = zerolog.Ctx(context.Background())

// withRequestLog stamps the request's logger with the request and logs its two
// ends.
//
// Two lines and not one: a request that never completes — a store that hangs, a
// body nobody drains, a ranged read that never returns — is invisible from a
// completion line alone, and that is precisely the failure a log gets read to
// diagnose. Both are DEBUG, because a healthy backup is thousands of requests;
// what went wrong says so at its own level, from the layer that knows why.
func withRequestLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		logger := requestLogger(r)
		r = r.WithContext(logger.WithContext(r.Context()))

		// The three headers that decide what a request means — the range being
		// read, the listing format negotiated, the create=true that makes a POST
		// / an init — and the size of what is being uploaded. Each is added only
		// when present: an empty field says nothing and costs a column.
		started := logger.Debug()
		if v := r.Header.Get("Range"); v != "" {
			started = started.Str("range", v)
		}
		if v := r.Header.Get("Accept"); v != "" {
			started = started.Str("accept", v)
		}
		if r.URL.RawQuery != "" {
			started = started.Str("query", r.URL.RawQuery)
		}
		if r.ContentLength > 0 {
			started = started.Int64("content_length", r.ContentLength)
		}
		started.Msg("restic rest request")

		rec := &statusRecorder{ResponseWriter: w}
		start := time.Now()
		next.ServeHTTP(rec, r)

		served := logger.Debug()
		// A 405 is the mux's answer, decided with no handler of ours in the call
		// stack, so this is the only place it can be reported — and it is worth
		// reporting: restic never sends a method a path does not have.
		if rec.status() == http.StatusMethodNotAllowed {
			served = logger.Warn()
		}
		served.Int("status", rec.status()).Int64("bytes", rec.written).
			Dur("duration", time.Since(start)).Msg("restic rest request served")
	})
}

// requestLogger resolves the logger this request's lines go to and stamps it
// with the request, so no line below has to name the method or the path again.
//
// The context's logger is arcave's own — level and format already resolved by
// the command that built it — and is used whenever the context carries one. The
// global logger stands in otherwise, so a handler served from a context nobody
// prepared says something rather than nothing.
//
// Absence is detected by identity against what zerolog answers for a bare
// context, and not by asking whether the logger is disabled. The two differ in
// exactly one case, and it is the case worth getting right: a program that
// installs a zerolog.DefaultContextLogger has already said what a request
// carrying no logger of its own should log to — including "nothing" — and a
// level check would overrule that with the global logger. (A zerolog.Nop()
// handed to WithContext is not that case and cannot be: zerolog declines to
// store a disabled logger in a context at all, so it never arrives here.)
func requestLogger(r *http.Request) zerolog.Logger {
	logger := zerolog.Ctx(r.Context())
	if logger == emptyContextLogger {
		logger = &log.Logger
	}
	return logger.With().
		Str("component", "resticrest").
		Str("method", r.Method).
		Str("path", r.URL.Path).
		Logger()
}

// statusRecorder remembers what was answered, which is the one thing an
// http.Handler reports to no one: the status and the body size never leave the
// ResponseWriter.
type statusRecorder struct {
	http.ResponseWriter
	code    int // 0 until WriteHeader; see status
	written int64
}

// WriteHeader keeps the first status only: the stdlib ignores every later one as
// superfluous, and the log must say what went on the wire.
func (rec *statusRecorder) WriteHeader(code int) {
	if rec.code == 0 {
		rec.code = code
	}
	rec.ResponseWriter.WriteHeader(code)
}

func (rec *statusRecorder) Write(p []byte) (int, error) {
	n, err := rec.ResponseWriter.Write(p)
	rec.written += int64(n)
	return n, err
}

// status is what the handler answered: the first WriteHeader, or the 200 the
// stdlib implies for a handler that only wrote a body — or wrote nothing at all,
// which is how every successful HEAD here ends.
func (rec *statusRecorder) status() int {
	if rec.code == 0 {
		return http.StatusOK
	}
	return rec.code
}

// Unwrap hands http.NewResponseController the writer underneath, so wrapping
// costs a handler below nothing it might need — a flush, a write deadline.
// Nothing here asks for one today; the cost of leaving it out is a bug in
// whatever first does.
func (rec *statusRecorder) Unwrap() http.ResponseWriter { return rec.ResponseWriter }
