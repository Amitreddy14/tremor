package shop

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func do(t *testing.T, h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, path, nil)
	} else {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestHealthzIgnoresDatabase(t *testing.T) {
	store := &MemoryStore{}
	store.SetDown(true)
	if rec := do(t, NewHandler(store), "GET", "/healthz", ""); rec.Code != http.StatusOK {
		t.Fatalf("/healthz = %d, want 200 even when the database is down", rec.Code)
	}
}

func TestReadyzFollowsDatabase(t *testing.T) {
	store := &MemoryStore{}
	h := NewHandler(store)

	if rec := do(t, h, "GET", "/readyz", ""); rec.Code != http.StatusOK {
		t.Fatalf("ready = %d, want 200", rec.Code)
	}
	store.SetDown(true)
	if rec := do(t, h, "GET", "/readyz", ""); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("down = %d, want 503", rec.Code)
	}
	store.SetDown(false)
	if rec := do(t, h, "GET", "/readyz", ""); rec.Code != http.StatusOK {
		t.Fatalf("recovered = %d, want 200", rec.Code)
	}
}

func TestCreateAndListOrders(t *testing.T) {
	h := NewHandler(&MemoryStore{})

	rec := do(t, h, "POST", "/orders", `{"item":"keyboard","quantity":2}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d, body %s", rec.Code, rec.Body)
	}
	var created Order
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.ID == "" || created.Item != "keyboard" || created.Quantity != 2 || created.CreatedAt.IsZero() {
		t.Fatalf("unexpected order %+v", created)
	}

	do(t, h, "POST", "/orders", `{"item":"mouse","quantity":1}`)

	rec = do(t, h, "GET", "/orders?limit=1", "")
	var list []Order
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusOK || len(list) != 1 || list[0].Item != "mouse" {
		t.Fatalf("list = %d %+v, want the newest order only", rec.Code, list)
	}
}

func TestListEmptyIsArray(t *testing.T) {
	rec := do(t, NewHandler(&MemoryStore{}), "GET", "/orders", "")
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Fatalf("empty list = %d %q, want 200 []", rec.Code, rec.Body)
	}
}

func TestCreateValidation(t *testing.T) {
	h := NewHandler(&MemoryStore{})
	bad := []string{
		`not json`,
		`{"item":"","quantity":1}`,
		`{"item":"  ","quantity":1}`,
		`{"item":"x","quantity":0}`,
		`{"item":"x","quantity":1001}`,
		`{"item":"x","quantity":1,"extra":true}`,
		`{"item":"` + strings.Repeat("a", 101) + `","quantity":1}`,
	}
	for _, body := range bad {
		if rec := do(t, h, "POST", "/orders", body); rec.Code != http.StatusBadRequest {
			t.Errorf("body %q = %d, want 400", body, rec.Code)
		}
	}
}

func TestListLimitValidation(t *testing.T) {
	h := NewHandler(&MemoryStore{})
	for _, q := range []string{"limit=0", "limit=101", "limit=abc"} {
		if rec := do(t, h, "GET", "/orders?"+q, ""); rec.Code != http.StatusBadRequest {
			t.Errorf("%s = %d, want 400", q, rec.Code)
		}
	}
}

func TestDatabaseOutageReturns503(t *testing.T) {
	store := &MemoryStore{}
	h := NewHandler(store)
	store.SetDown(true)

	if rec := do(t, h, "GET", "/orders", ""); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("list = %d, want 503", rec.Code)
	}
	if rec := do(t, h, "POST", "/orders", `{"item":"x","quantity":1}`); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("create = %d, want 503", rec.Code)
	}
}

func TestMethodNotAllowed(t *testing.T) {
	if rec := do(t, NewHandler(&MemoryStore{}), "DELETE", "/orders", ""); rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("DELETE /orders = %d, want 405", rec.Code)
	}
}
