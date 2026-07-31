package rest

import (
	"github.com/labstack/echo/v4"
	"github.com/rs/zerolog"
	"github.com/ziflex/lecho/v3"
)

// enrichLogger reports both ends of every request, stamps the request's logger into
// its context, and is the only place the cause of a 500 is written down.
func enrichLogger(logger *lecho.Logger) echo.MiddlewareFunc {
	return lecho.Middleware(lecho.Config{
		Logger: logger,
		// Levels are lecho's: an operation that answered logs at the logger's
		// own level, an error escaping one at ERROR — so 404s are not noise.
		//
		// HandleError lets the error handler pick the status before the line is
		// written, or the journal reports one it decided itself.
		HandleError: true,
		Enricher: func(_ echo.Context, l zerolog.Context) zerolog.Context {
			return l.Str("component", "restic-rest")
		},
		BeforeNext: logStarted,
	})
}

// logStarted names what a request asked for before anything answered it: a
// request that never completes is invisible from a completion line alone.
func logStarted(c echo.Context) {
	r := c.Request()
	evt := zerolog.Ctx(r.Context()).Debug()
	if v := r.Header.Get("Range"); v != "" {
		evt = evt.Str("range", v)
	}
	if v := r.Header.Get("Accept"); v != "" {
		evt = evt.Str("accept", v)
	}
	if r.URL.RawQuery != "" {
		evt = evt.Str("query", r.URL.RawQuery)
	}
	if r.ContentLength > 0 {
		evt = evt.Int64("content_length", r.ContentLength)
	}
	evt.Msg("restic rest request")
}
