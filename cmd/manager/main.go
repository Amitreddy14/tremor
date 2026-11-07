// Command manager is the Tremor operator entrypoint.
//
// Phase 0 ships only the process skeleton (flags, structured logging,
// health endpoints, graceful shutdown). Phase 1 wires in controller-runtime
// and the ChaosExperiment controller.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/Amitreddy14/tremor/internal/server"
	"github.com/Amitreddy14/tremor/internal/version"
)

func main() {
	probeAddr := flag.String("health-probe-bind-address", ":8081", "address the health probe endpoint binds to")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println(version.String())
		return
	}

	log := slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("component", "manager")

	var ready atomic.Bool
	srv := &http.Server{
		Addr:              *probeAddr,
		Handler:           server.New(ready.Load),
		ReadHeaderTimeout: 5 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		log.Info("starting", "version", version.String(), "probeAddr", *probeAddr)
		ready.Store(true)
		errCh <- srv.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("server failed", "error", err)
			os.Exit(1)
		}
	case <-ctx.Done():
		log.Info("shutting down")
		ready.Store(false)
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			log.Error("graceful shutdown failed", "error", err)
			os.Exit(1)
		}
	}
}
