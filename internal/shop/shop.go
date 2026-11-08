// Package shop is the demo application Tremor injects failures into: a tiny
// orders API whose readiness depends on its database.
package shop

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Order is a customer order.
type Order struct {
	ID        string    `json:"id"`
	Item      string    `json:"item"`
	Quantity  int       `json:"quantity"`
	CreatedAt time.Time `json:"createdAt"`
}

// Store persists orders. Implementations: memory (tests) and MongoDB.
type Store interface {
	Ping(ctx context.Context) error
	Create(ctx context.Context, o Order) (Order, error)
	List(ctx context.Context, limit int) ([]Order, error)
}

const (
	maxBody      = 1 << 20
	maxItemLen   = 100
	maxQuantity  = 1000
	defaultLimit = 20
	maxLimit     = 100
	storeTimeout = 2 * time.Second
)

// NewHandler returns the shop's HTTP API:
//
//	GET  /healthz  liveness: the process is up
//	GET  /readyz   readiness: the database answers
//	POST /orders   create an order
//	GET  /orders   list recent orders (reads the database)
func NewHandler(s Store) http.Handler {
	h := &handler{store: s}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", h.healthz)
	mux.HandleFunc("GET /readyz", h.readyz)
	mux.HandleFunc("POST /orders", h.createOrder)
	mux.HandleFunc("GET /orders", h.listOrders)
	return mux
}

type handler struct{ store Store }

func (h *handler) healthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *handler) readyz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), storeTimeout)
	defer cancel()
	if err := h.store.Ping(ctx); err != nil {
		writeError(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

type createRequest struct {
	Item     string `json:"item"`
	Quantity int    `json:"quantity"`
}

func (h *handler) createOrder(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBody)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()

	var req createRequest
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	req.Item = strings.TrimSpace(req.Item)
	if err := validate(req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), storeTimeout)
	defer cancel()
	order, err := h.store.Create(ctx, Order{Item: req.Item, Quantity: req.Quantity, CreatedAt: time.Now().UTC()})
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}
	writeJSON(w, http.StatusCreated, order)
}

func (h *handler) listOrders(w http.ResponseWriter, r *http.Request) {
	limit := defaultLimit
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > maxLimit {
			writeError(w, http.StatusBadRequest, "limit must be between 1 and "+strconv.Itoa(maxLimit))
			return
		}
		limit = n
	}

	ctx, cancel := context.WithTimeout(r.Context(), storeTimeout)
	defer cancel()
	orders, err := h.store.List(ctx, limit)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}
	if orders == nil {
		orders = []Order{}
	}
	writeJSON(w, http.StatusOK, orders)
}

func validate(req createRequest) error {
	switch {
	case req.Item == "":
		return errors.New("item is required")
	case len(req.Item) > maxItemLen:
		return errors.New("item is too long")
	case req.Quantity < 1 || req.Quantity > maxQuantity:
		return errors.New("quantity must be between 1 and " + strconv.Itoa(maxQuantity))
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
