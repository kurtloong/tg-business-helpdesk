package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHealthcheck(t *testing.T) {
	for _, code := range []int{200, 503} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/readyz" {
				t.Error(r.URL.Path)
			}
			w.WriteHeader(code)
		}))
		err := healthcheck(context.Background(), strings.TrimPrefix(server.URL, "http://"))
		server.Close()
		if (err == nil) != (code == 200) {
			t.Fatalf("status=%d error=%v", code, err)
		}
	}
	if err := healthcheck(context.Background(), "invalid"); err == nil {
		t.Fatal("invalid address accepted")
	}
}
