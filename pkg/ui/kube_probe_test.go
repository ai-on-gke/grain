package ui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/model/sqlite"
)

// TestTheKubernetesProbesAreAnsweredWithoutTheAccessCode: the kubelet's
// readiness and liveness probes carry no access code, and a single-tenant
// daemon answers every route but a handful with 401 to such a caller
// (singleTenantGate) -- /api/config included, which the shipped manifests
// used to probe. That left the daemon pod never Ready (so its Services
// had no endpoints) and restarted by its liveness probe every few
// minutes, interrupting whatever was running. Every probe path in
// deploy/kubernetes is served here as the kubelet would ask for it.
func TestTheKubernetesProbesAreAnsweredWithoutTheAccessCode(t *testing.T) {
	db, err := sqlite.Open(sqlite.DefaultConfig(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	store := model.New(db)
	if err := store.Init(context.Background()); err != nil {
		t.Fatal(err)
	}
	srv := NewServer(Config{Actor: DefaultActor("alice"), LocalAPIToken: "the-access-code"}, store)

	probe := regexp.MustCompile(`(?m)^\s*(readinessProbe|livenessProbe):\s*\n\s*httpGet:\s*\n\s*path:\s*(\S+)`)
	for _, file := range []string{"grain.yaml", "grain-granule.yaml"} {
		data, err := os.ReadFile(filepath.Join("..", "..", "deploy", "kubernetes", file))
		if err != nil {
			t.Fatal(err)
		}
		found := probe.FindAllStringSubmatch(string(data), -1)
		if len(found) != 2 {
			t.Fatalf("%s: found %d probes, want a readiness and a liveness probe", file, len(found))
		}
		for _, m := range found {
			r := httptest.NewRequest(http.MethodGet, "http://10.244.0.6:8080"+m[2], nil)
			r.RemoteAddr = "10.244.0.1:41234"
			r.Header.Set("User-Agent", "kube-probe/1.36")
			w := httptest.NewRecorder()
			srv.ServeHTTP(w, r)
			if w.Code < 200 || w.Code >= 400 {
				t.Errorf("%s: %s GET %s = %d, which the kubelet reads as a failure: %s", file, m[1], m[2], w.Code, w.Body.String())
			}
		}
	}
}
