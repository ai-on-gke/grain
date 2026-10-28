package relay

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func fakePod(name, ns, ip string, ready bool, labels map[string]string) map[string]any {
	status := "False"
	if ready {
		status = "True"
	}
	return map[string]any{
		"metadata": map[string]any{"name": name, "namespace": ns, "labels": labels},
		"status": map[string]any{
			"phase":      "Running",
			"podIP":      ip,
			"conditions": []any{map[string]any{"type": "Ready", "status": status}},
		},
	}
}

// fakeAPI serves the few pod endpoints PodResolver uses, and records every
// request so the test can check it never asked for anything else.
type fakeAPI struct {
	mu       sync.Mutex
	list     []map[string]any
	byName   map[string]map[string]any
	events   chan map[string]any
	paths    []string
	gotToken string
}

func (f *fakeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.paths = append(f.paths, r.Method+" "+r.URL.Path+"?"+r.URL.RawQuery)
	f.gotToken = r.Header.Get("Authorization")
	f.mu.Unlock()
	if r.Method != http.MethodGet {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	const prefix = "/api/v1/namespaces/sandboxes/pods"
	switch {
	case r.URL.Path == prefix && r.URL.Query().Get("watch") == "1":
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		for {
			select {
			case ev := <-f.events:
				_ = json.NewEncoder(w).Encode(ev)
				w.(http.Flusher).Flush()
			case <-r.Context().Done():
				return
			}
		}
	case r.URL.Path == prefix:
		_ = json.NewEncoder(w).Encode(map[string]any{
			"metadata": map[string]any{"resourceVersion": "10"},
			"items":    f.list,
		})
	case strings.HasPrefix(r.URL.Path, prefix+"/"):
		name := strings.TrimPrefix(r.URL.Path, prefix+"/")
		f.mu.Lock()
		p, ok := f.byName[name]
		f.mu.Unlock()
		if !ok {
			http.Error(w, `{"reason":"NotFound"}`, http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(p)
	default:
		http.Error(w, "forbidden", http.StatusForbidden)
	}
}

func TestPodResolver(t *testing.T) {
	sel := map[string]string{"granule.gke.io/sandbox": "true", "sandbox-name": "alpha"}
	api := &fakeAPI{
		list: []map[string]any{
			fakePod("sbx-alpha-pod", "sandboxes", "10.0.0.5", true, sel),
			fakePod("sbx-notready", "sandboxes", "10.0.0.6", false, map[string]string{"granule.gke.io/sandbox": "true"}),
			// A pod that should never have matched the list's selector;
			// the resolver checks labels itself as well.
			fakePod("stray", "sandboxes", "10.0.0.7", true, map[string]string{"app": "db"}),
		},
		byName: map[string]map[string]any{
			"late":       fakePod("late", "sandboxes", "10.0.0.8", true, map[string]string{"granule.gke.io/sandbox": "true"}),
			"unlabelled": fakePod("unlabelled", "sandboxes", "10.0.0.9", true, map[string]string{"app": "db"}),
		},
		events: make(chan map[string]any, 4),
	}
	srv := httptest.NewServer(api)
	defer srv.Close()

	r, err := NewPodResolver(PodResolverConfig{
		APIServer:   srv.URL,
		HTTPClient:  srv.Client(),
		BearerToken: func() (string, error) { return "sa-token", nil },
		Namespace:   "sandboxes",
		Selector:    "granule.gke.io/sandbox=true",
		NameLabel:   "sandbox-name",
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	go func() { _ = r.Run(ctx) }()

	for _, tc := range []struct {
		target, ip, refusal string
	}{
		{"sbx-alpha-pod", "10.0.0.5", ""},
		{"alpha", "10.0.0.5", ""}, // by NameLabel
		{"late", "10.0.0.8", ""},  // GET fallback for a pod not yet watched
		{"sbx-notready", "", "not ready"},
		{"stray", "", "outside the relay's selector"},
		{"unlabelled", "", "outside the relay's selector"},
		{"missing", "", "no sandbox"},
		{"kube-system/kube-dns", "", "not a pod or sandbox name"},
		{"../../nodes", "", "not a pod or sandbox name"},
		{"", "", "not a pod or sandbox name"},
	} {
		ip, err := r.Resolve(ctx, tc.target)
		if tc.refusal == "" {
			if err != nil || ip != tc.ip {
				t.Errorf("Resolve(%q) = %q, %v; want %q", tc.target, ip, err, tc.ip)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), tc.refusal) {
			t.Errorf("Resolve(%q) = %q, %v; want refusal containing %q", tc.target, ip, err, tc.refusal)
		}
	}

	// The watch keeps the cache current.
	api.events <- map[string]any{"type": "MODIFIED", "object": fakePod("sbx-notready", "sandboxes", "10.0.0.6", true, map[string]string{"granule.gke.io/sandbox": "true"})}
	api.events <- map[string]any{"type": "DELETED", "object": fakePod("sbx-alpha-pod", "sandboxes", "10.0.0.5", true, sel)}
	deadline := time.Now().Add(5 * time.Second)
	for {
		_, errAlpha := r.Resolve(ctx, "alpha")
		ip, errReady := r.Resolve(ctx, "sbx-notready")
		if errAlpha != nil && errReady == nil && ip == "10.0.0.6" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("watch events not applied: alpha err=%v, notready=%q err=%v", errAlpha, ip, errReady)
		}
		time.Sleep(20 * time.Millisecond)
	}

	api.mu.Lock()
	defer api.mu.Unlock()
	if api.gotToken != "Bearer sa-token" {
		t.Errorf("Authorization = %q", api.gotToken)
	}
	for _, p := range api.paths {
		if !strings.HasPrefix(p, "GET /api/v1/namespaces/sandboxes/pods") {
			t.Errorf("resolver made an unexpected request: %s", p)
		}
		if strings.HasPrefix(p, "GET /api/v1/namespaces/sandboxes/pods?") && !strings.Contains(p, "labelSelector=granule.gke.io%2Fsandbox%3Dtrue") {
			t.Errorf("list/watch without the label selector: %s", p)
		}
	}
}

func TestNewPodResolverRequiresSelector(t *testing.T) {
	base := PodResolverConfig{
		APIServer:   "https://example",
		HTTPClient:  http.DefaultClient,
		BearerToken: func() (string, error) { return "", nil },
		Namespace:   "sandboxes",
	}
	if _, err := NewPodResolver(base); err == nil {
		t.Fatal("NewPodResolver with no selector succeeded")
	}
	bad := base
	bad.Selector, bad.Namespace = "a=b", "Bad/NS"
	if _, err := NewPodResolver(bad); err == nil {
		t.Fatal("NewPodResolver with an invalid namespace succeeded")
	}
}

func TestLabelSelector(t *testing.T) {
	sel, err := parseSelector("a=1, b==2, c!=3, d, !e")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		labels map[string]string
		want   bool
	}{
		{map[string]string{"a": "1", "b": "2", "d": ""}, true},
		{map[string]string{"a": "1", "b": "2", "c": "4", "d": "x"}, true},
		{map[string]string{"a": "1", "b": "2", "c": "3", "d": "x"}, false},
		{map[string]string{"a": "1", "b": "2"}, false},
		{map[string]string{"a": "1", "b": "2", "d": "", "e": "y"}, false},
		{map[string]string{"a": "2", "b": "2", "d": ""}, false},
	} {
		if got := sel.matches(tc.labels); got != tc.want {
			t.Errorf("matches(%v) = %v, want %v", tc.labels, got, tc.want)
		}
	}
	if _, err := parseSelector("env in (a,b)"); err == nil {
		t.Error("set-based selector accepted")
	}

}
