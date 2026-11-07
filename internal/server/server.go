// Package server hosts the operator's HTTP endpoints for health probes.
//
// In Phase 1 the controller-runtime manager takes over probes and metrics;
// this package stays as the tiny, dependency-free base the rest builds on.
package server

import (
	"encoding/json"
	"net/http"

	"github.com/Amitreddy14/tremor/internal/version"
)

// New returns a handler exposing /healthz (liveness), /readyz (readiness)
// and /version. ready reports whether the process can serve work.
func New(ready func() bool) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		if ready != nil && !ready() {
			http.Error(w, "not ready", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ready"))
	})

	mux.HandleFunc("/version", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"version": version.Version,
			"commit":  version.Commit,
		})
	})

	return mux
}
