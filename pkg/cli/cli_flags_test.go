package cli

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRunCLI_GlobalFlags(t *testing.T) {
	var gotAuth, gotTunnel, gotUser string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotTunnel = r.Header.Get("X-Grain-Tunnel-Token")
		gotUser = r.Header.Get("X-Forwarded-User")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
	}))
	defer srv.Close()

	err := runCLI([]string{
		"-server=" + srv.URL,
		"-token=my-secret-token",
		"-as=alice@example.com",
		"list",
	})
	if err != nil {
		t.Fatalf("runCLI failed: %v", err)
	}

	if gotAuth != "Bearer my-secret-token" {
		t.Errorf("got Authorization %q, want Bearer my-secret-token", gotAuth)
	}
	if gotTunnel != "my-secret-token" {
		t.Errorf("got X-Grain-Tunnel-Token %q, want my-secret-token", gotTunnel)
	}
	if gotUser != "alice@example.com" {
		t.Errorf("got X-Forwarded-User %q, want alice@example.com", gotUser)
	}
}

func TestRunCLI_UsageIncludesNewFlags(t *testing.T) {
	for _, flag := range []string{"-token", "-audience", "-as", "-server"} {
		if !strings.Contains(usage, flag) {
			t.Errorf("usage missing expected flag %q", flag)
		}
	}
}
