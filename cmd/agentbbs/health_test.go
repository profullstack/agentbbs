package main

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/profullstack/agentbbs/internal/store"
)

func TestHandleHealth(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	a := &app{st: st}

	rec := httptest.NewRecorder()
	a.handleHealth(rec, httptest.NewRequest(http.MethodGet, "/api/health", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != `{"status":"ok","db":"ok"}` {
		t.Fatalf("healthy: %d %s", rec.Code, rec.Body.String())
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Fatalf("Cache-Control = %q", cc)
	}

	st.Close()
	rec = httptest.NewRecorder()
	a.handleHealth(rec, httptest.NewRequest(http.MethodGet, "/api/health", nil))
	if rec.Code != http.StatusServiceUnavailable || rec.Body.String() != `{"status":"error","db":"down"}` {
		t.Fatalf("closed store: %d %s", rec.Code, rec.Body.String())
	}
}
