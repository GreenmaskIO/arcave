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

package rest

import (
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	"github.com/rs/zerolog"
	"github.com/ziflex/lecho/v3"
)

// newEnrichLogger stamps the request's logger into its context and reports the
// request's start. Its completion line is skipped: newJournalLogger writes it.
func newEnrichLogger(logger *lecho.Logger) echo.MiddlewareFunc {
	return lecho.Middleware(lecho.Config{
		Logger: logger,
		Enricher: func(_ echo.Context, l zerolog.Context) zerolog.Context {
			return l.Str("component", "restic-rest")
		},
		BeforeNext:       logStarted,
		AfterNextSkipper: func(echo.Context) bool { return true },
	})
}

// newJournalLogger reports the end of every request — at DEBUG when an operation
// answered, at ERROR when an error escaped one — and is the only place the
// cause of a 500 is written down.
func newJournalLogger() echo.MiddlewareFunc {
	return middleware.RequestLoggerWithConfig(middleware.RequestLoggerConfig{
		// HandleError lets the error handler pick the status before the line is
		// written, or the journal reports one it decided itself.
		HandleError:      true,
		LogLatency:       true,
		LogRemoteIP:      true,
		LogHost:          true,
		LogMethod:        true,
		LogURI:           true,
		LogUserAgent:     true,
		LogStatus:        true,
		LogError:         true,
		LogContentLength: true,
		LogResponseSize:  true,
		LogValuesFunc:    logAnswered,
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

// logAnswered writes one completion line through the logger newEnrichLogger stamped.
func logAnswered(c echo.Context, v middleware.RequestLoggerValues) error {
	logger := zerolog.Ctx(c.Request().Context())
	evt := logger.Debug()
	if v.Error != nil {
		evt = logger.Err(v.Error)
	}
	evt.Str("remote_ip", v.RemoteIP).
		Str("host", v.Host).
		Str("method", v.Method).
		Str("uri", v.URI).
		Str("user_agent", v.UserAgent).
		Int("status", v.Status).
		Dur("latency", v.Latency).
		Str("bytes_in", v.ContentLength).
		Int64("bytes_out", v.ResponseSize).
		Msg("restic rest response")
	return nil
}
