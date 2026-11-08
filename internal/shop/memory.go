package shop

import (
	"context"
	"errors"
	"strconv"
	"sync"
)

// ErrUnavailable is returned by MemoryStore while it is marked down.
var ErrUnavailable = errors.New("store unavailable")

// MemoryStore is an in-memory Store for tests and local runs.
type MemoryStore struct {
	mu     sync.Mutex
	orders []Order
	down   bool
}

// SetDown simulates a database outage.
func (m *MemoryStore) SetDown(down bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.down = down
}

func (m *MemoryStore) Ping(context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.down {
		return ErrUnavailable
	}
	return nil
}

func (m *MemoryStore) Create(_ context.Context, o Order) (Order, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.down {
		return Order{}, ErrUnavailable
	}
	o.ID = "o-" + strconv.Itoa(len(m.orders)+1)
	m.orders = append(m.orders, o)
	return o, nil
}

// List returns the newest orders first.
func (m *MemoryStore) List(_ context.Context, limit int) ([]Order, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.down {
		return nil, ErrUnavailable
	}
	var out []Order
	for i := len(m.orders) - 1; i >= 0 && len(out) < limit; i-- {
		out = append(out, m.orders[i])
	}
	return out, nil
}
