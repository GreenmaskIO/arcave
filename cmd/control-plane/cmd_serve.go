package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"

	"github.com/greenmaskio/arcave/internal/httpapi"
)

// serveShutdownTimeout is how long in-flight requests get to finish on shutdown.
const serveShutdownTimeout = 10 * time.Second

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Build the provider registry and serve the API",
	Args:  cobra.NoArgs,
	RunE:  runServe,
}

// runServe builds every registered provider, then listens and serves until a signal.
func runServe(cmd *cobra.Command, _ []string) error {
	ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	logger := log.Ctx(ctx)
	if !configFound {
		logger.Warn().Msg("no config file found: serving on the default address with no providers registered")
	}

	registry, err := newRegistry(ctx, cpCfg.Providers)
	if err != nil {
		return err
	}
	defer registry.Close(ctx)

	for name, p := range registry {
		logger.Info().Str("provider", name).Str("kind", string(p.Kind())).Msg("provider registered")
	}

	// Bound here, not in ListenAndServe: an address already in use must fail the command.
	listener, err := net.Listen("tcp", cpCfg.API.Address)
	if err != nil {
		return fmt.Errorf("bind api.address %q: %w", cpCfg.API.Address, err)
	}

	srv := &http.Server{
		Handler: httpapi.NewHTTPHandler(version),
		// Every request carries the command's logger.
		BaseContext:       func(net.Listener) context.Context { return ctx },
		ReadHeaderTimeout: serveShutdownTimeout,
	}

	served := make(chan error, 1)
	go func() { served <- srv.Serve(listener) }()
	logger.Info().Str("address", listener.Addr().String()).Msg("control plane is serving")

	select {
	case err := <-served:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("serve api: %w", err)
		}
		return nil
	case <-ctx.Done():
		logger.Info().Msg("shutting down")
	}

	// Detached from ctx: it is already cancelled, and a cancelled Shutdown
	// severs every connection at once.
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), serveShutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shut down api: %w", err)
	}
	return nil
}

func init() {
	rootCmd.AddCommand(serveCmd)
}
