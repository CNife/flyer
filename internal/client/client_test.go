package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPageStatusUsesHead(t *testing.T) {
	var method, path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		w.WriteHeader(http.StatusGone)
	}))
	defer srv.Close()

	c := New(srv.URL+"/", "", "")
	status, err := c.PageStatus(context.Background(), "Ab3dE9xZ")
	if err != nil {
		t.Fatalf("PageStatus: %v", err)
	}
	if status != http.StatusGone {
		t.Fatalf("status = %d, want %d", status, http.StatusGone)
	}
	if method != http.MethodHead {
		t.Fatalf("method = %s, want HEAD", method)
	}
	if path != "/Ab3dE9xZ" {
		t.Fatalf("path = %s, want /Ab3dE9xZ", path)
	}
}
