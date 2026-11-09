package runstore

import (
	"context"
	"errors"
	"testing"
	"time"
)

var base = time.Date(2026, 11, 8, 10, 0, 0, 0, time.UTC)

func run(id, ns, name, fault, result string, age time.Duration) Run {
	return Run{ID: id, Namespace: ns, Name: name, FaultType: fault, Result: result, StartedAt: base.Add(-age)}
}

func seeded(t *testing.T) *MemoryStore {
	t.Helper()
	m := &MemoryStore{}
	ctx := context.Background()
	for _, r := range []Run{
		run("a", "demo", "kill-shop", "PodKill", "completed", 3*time.Hour),
		run("b", "demo", "kill-shop", "PodKill", "failed", 2*time.Hour),
		run("c", "demo", "mongo-out", "DependencyOutage", "completed", time.Hour),
		run("d", "other", "kill-x", "PodKill", "completed", 30*time.Minute),
	} {
		if err := m.Save(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	return m
}

func ids(runs []Run) string {
	s := ""
	for _, r := range runs {
		s += r.ID
	}
	return s
}

func TestListFilterAndOrder(t *testing.T) {
	m := seeded(t)
	ctx := context.Background()
	tests := []struct {
		name string
		f    Filter
		want string
	}{
		{"all newest first", Filter{}, "dcba"},
		{"namespace", Filter{Namespace: "demo"}, "cba"},
		{"name", Filter{Name: "kill-shop"}, "ba"},
		{"fault type", Filter{FaultType: "PodKill"}, "dba"},
		{"result", Filter{Result: "completed"}, "dca"},
		{"since", Filter{Since: base.Add(-150 * time.Minute)}, "dcb"},
		{"limit", Filter{Limit: 2}, "dc"},
		{"combined", Filter{Namespace: "demo", FaultType: "PodKill", Result: "completed"}, "a"},
		{"no match", Filter{Namespace: "nope"}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := m.List(ctx, tt.f)
			if err != nil {
				t.Fatal(err)
			}
			if ids(got) != tt.want {
				t.Fatalf("got %q, want %q", ids(got), tt.want)
			}
		})
	}
}

func TestSaveIsIdempotentUpsert(t *testing.T) {
	m := &MemoryStore{}
	ctx := context.Background()
	r := run("x", "demo", "n", "PodKill", "failed", time.Hour)
	_ = m.Save(ctx, r)
	r.Result = "completed"
	_ = m.Save(ctx, r)

	if m.Len() != 1 {
		t.Fatalf("Len = %d, want 1", m.Len())
	}
	got, err := m.Get(ctx, "x")
	if err != nil || got.Result != "completed" {
		t.Fatalf("Get = %+v, %v; want the replaced run", got, err)
	}
}

func TestGetNotFound(t *testing.T) {
	if _, err := (&MemoryStore{}).Get(context.Background(), "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestAvailability(t *testing.T) {
	if _, ok := (Run{}).Availability(); ok {
		t.Error("no probes should report no availability")
	}
	if a, ok := (Run{ProbeSuccesses: 3, ProbeFailures: 1}).Availability(); !ok || a != 0.75 {
		t.Errorf("Availability = %v, %v; want 0.75, true", a, ok)
	}
}
