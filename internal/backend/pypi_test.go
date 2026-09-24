package backend

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPyPIExactLookup(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/pypi/ruff/json" {
			t.Error(r.URL.Path)
		}
		w.Write([]byte(`{"info":{"name":"ruff","version":"0.1","summary":"Linter"}}`))
	}))
	defer srv.Close()
	s, err := PyPIExact(context.Background(), srv.Client(), srv.URL, "ruff")
	if err != nil || len(s.Packages) != 1 || s.Packages[0].Manager != "uvx" {
		t.Fatal(s, err)
	}
	s, err = PyPIExact(context.Background(), srv.Client(), srv.URL, "python linter")
	if err != nil || len(s.Packages) != 0 || calls != 1 || len(s.Issues) == 0 {
		t.Fatal(s, err, calls)
	}
}
