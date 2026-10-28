package ui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoopbackUnauthorizedHint(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/tasks", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error": "sign in to use this server's API: this daemon does not trust loopback (-ui-trust-loopback=false)"}`))
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	client := NewHTTPClient(ts.URL)

	t.Run("no token file set", func(t *testing.T) {
		t.Setenv(LocalAPITokenFileEnv, "")
		t.Setenv("GRAIN_DATA_DIR", "")
		_, err := client.ListTasks(context.Background())
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		want := "sudo -u " + FileOwner(LocalAPITokenPath("/var/lib/grain/data")) + " env GRAIN_DATA_DIR=/var/lib/grain/data "
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error missing CLI auth hint: %v", err)
		}
	})

	t.Run("token file unreadable", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("skipping permission check as root")
		}
		unreadable := filepath.Join(t.TempDir(), LocalAPITokenFileName)
		if err := os.WriteFile(unreadable, []byte("tok"), 0o000); err != nil {
			t.Fatal(err)
		}
		t.Setenv(LocalAPITokenFileEnv, unreadable)
		t.Setenv("GRAIN_DATA_DIR", "")
		_, err := client.ListTasks(context.Background())
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !strings.Contains(err.Error(), "is unreadable") {
			t.Errorf("error missing unreadable hint: %v", err)
		}
	})
}
