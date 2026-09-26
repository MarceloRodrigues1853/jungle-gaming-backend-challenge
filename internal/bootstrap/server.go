package bootstrap

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"time"

	"go.uber.org/fx"
)

// registerHTTPServer abre a porta no start e realiza shutdown gracioso no stop.
func registerHTTPServer(lifecycle fx.Lifecycle, config Config, handler http.Handler, logger *slog.Logger) {
	server := &http.Server{
		Addr:              config.HTTPAddress,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	var listener net.Listener
	lifecycle.Append(fx.Hook{
		OnStart: func(context.Context) error {
			opened, err := net.Listen("tcp", server.Addr)
			if err != nil {
				return err
			}
			listener = opened
			logger.Info("HTTP server started", "address", listener.Addr().String())
			go func() {
				if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
					logger.Error("HTTP server stopped unexpectedly", "error", err)
				}
			}()
			return nil
		},
		OnStop: func(ctx context.Context) error {
			logger.Info("HTTP server stopping")
			return server.Shutdown(ctx)
		},
	})
}
