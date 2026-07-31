// Package rest assembles the restic REST backend: the generated routes over
// the handler, the middleware they run behind, and the OpenAPI document they
// were generated from. The only package of this tree that names echo.
package rest

import (
	"context"
	stdlog "log"
	"net"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"
	gommonlog "github.com/labstack/gommon/log"
	"github.com/rs/zerolog"
	echoSwagger "github.com/swaggo/echo-swagger"
	"github.com/ziflex/lecho/v3"

	"github.com/greenmaskio/arcave/internal/service/backend/restic/rest/api"
	docs "github.com/greenmaskio/arcave/internal/service/backend/restic/rest/docs/api"
	"github.com/greenmaskio/arcave/internal/service/backend/restic/rest/handler"
	"github.com/greenmaskio/arcave/internal/service/backend/restic/rest/model"
)

// readHeaderTimeout sheds a client that connects and sends no request header.
const readHeaderTimeout = 10 * time.Second

// NewEcho returns the HTTP server for the restic protocol, already routed. The
// context is here for its logger: lecho reads none from a request, and echo's
// own output must not reach stdout, which is a command's result channel.
//
// Nothing here resolves a repository: the operation reading {repoName} looks it up.
func NewEcho(ctx context.Context, repos model.RepositoryResolver) *echo.Echo {
	logger := lecho.From(*zerolog.Ctx(ctx))

	e := echo.New()
	e.HideBanner, e.HidePort = true, true
	e.Logger = logger
	// StdLogger is what echo hands net/http as its ErrorLog, and echo.New aims it
	// at stdout — a command's result channel and arcave mcp's JSON-RPC channel. So
	// a panic escaping the chain lands in the command's log like everything else.
	e.StdLogger = stdlog.New(zerolog.Ctx(ctx).With().Str("component", "restic-rest").Logger(), "", 0)
	e.Server.ReadHeaderTimeout = readHeaderTimeout
	// Requests inherit the command's context, so a layer under the routes reads
	// the logger the command resolved rather than zerolog's global.
	e.Server.BaseContext = func(net.Listener) context.Context { return ctx }

	// Order is load-bearing: RequestID before the journal stamps a logger from
	// it, Recover inside the journal so a recovered panic names its request and
	// still ends in a served line.
	e.Use(middleware.RequestID())
	e.Use(enrichLogger(logger))
	// Recover's default level is none of the ones it switches on, so the stack
	// would be logged with no level at all.
	e.Use(middleware.RecoverWithConfig(middleware.RecoverConfig{LogLevel: gommonlog.ERROR}))
	// Gzip and BodyLimit are deliberately absent: restic's objects are encrypted
	// and incompressible, and a pack has no upper bound.

	// No HTTPErrorHandler: every status this protocol declares is an operation's
	// own response object, so what reaches echo is the 500 its default answers.
	// The strict middleware list is empty for want of anything to put in it.
	api.RegisterHandlers(e, api.NewStrictHandler(handler.New(repos), nil))

	// Served from the binary, so a reader sees the document these routes were
	// generated from. Echo prefers a static segment to a parametric one, so this
	// shadows {repoName}: no repository is reachable under the name "docs".
	e.GET("/docs/*", echoSwagger.EchoWrapHandler(echoSwagger.InstanceName(docs.InstanceName)))

	return e
}
