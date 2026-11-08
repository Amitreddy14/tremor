// Command shop is the demo orders API used as a chaos target.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Amitreddy14/tremor/internal/shop"
	"github.com/Amitreddy14/tremor/internal/shop/mongostore"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("component", "shop")

	uri := os.Getenv("MONGO_URI")
	if uri == "" {
		log.Error("MONGO_URI is required")
		os.Exit(1)
	}
	database := getenv("MONGO_DB", "shop")
	addr := ":" + getenv("PORT", "8080")

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	store, err := mongostore.New(ctx, uri, database)
	if err != nil {
		log.Error("invalid MongoDB configuration", "error", err)
		os.Exit(1)
	}

	srv := &http.Server{
		Addr:              addr,
		Handler:           shop.NewHandler(store),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", addr, "database", database)
		errCh <- srv.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			log.Error("server failed", "error", err)
			os.Exit(1)
		}
	case <-ctx.Done():
		log.Info("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
		_ = store.Close(shutdownCtx)
	}
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
