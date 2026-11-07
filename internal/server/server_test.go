package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func get(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func TestHealthz(t *testing.T) {
	if rec := get(t, New(nil), "/healthz"); rec.Code != http.StatusOK {
		t.Fatalf("/healthz = %d, want 200", rec.Code)
	}
}

func TestReadyz(t *testing.T) {
	ready := false
	h := New(func() bool { return ready })

	if rec := get(t, h, "/readyz"); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("/readyz before ready = %d, want 503", rec.Code)
	}
	ready = true
	if rec := get(t, h, "/readyz"); rec.Code != http.StatusOK {
		t.Fatalf("/readyz after ready = %d, want 200", rec.Code)
	}
}

func TestVersion(t *testing.T) {
	rec := get(t, New(nil), "/version")
	if rec.Code != http.StatusOK {
		t.Fatalf("/version = %d, want 200", rec.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if body["version"] == "" || body["commit"] == "" {
		t.Fatalf("missing fields in %v", body)
	}
}
