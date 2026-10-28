package demo

// The demo's persistent sandboxes: two staging environments the rail's
// Staging group (ui/src/components/Sidebar.jsx) and System -> Sandboxes'
// panel can show without a sandbox backend. "docs-preview" is served
// under the UI at /staging/docs-preview/ by a page this process answers
// itself, so the subpath link opens something real; "grain-staging" is
// exposed on a host port only, the shape the operator's staging grain
// takes, and is crash-looping so the rail's warning dot has something to
// show. `grain demo -staging` runs them; ui/e2e/staging.spec.js reads both.

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/ports"
	"github.com/bwsalmon/grain/pkg/ui"
)

type demoPersistentSandboxes struct {
	started  time.Time
	upstream string

	mu      sync.Mutex
	stopped map[string]bool
}

var _ ui.PersistentSandboxes = (*demoPersistentSandboxes)(nil)

// newDemoPersistentSandboxes starts the page docs-preview serves, on a
// loopback port of its own, for as long as ctx lasts.
func newDemoPersistentSandboxes(ctx context.Context) (*demoPersistentSandboxes, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("demo staging page: %w", err)
	}
	srv := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprintf(w, "<!doctype html><title>docs-preview</title><h1>docs-preview</h1><p>Served from the demo's persistent sandbox at %s.</p>", r.URL.Path)
		}),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() { _ = srv.Serve(ln) }()
	go func() {
		<-ctx.Done()
		_ = srv.Close()
	}()
	return &demoPersistentSandboxes{started: time.Now().UTC(), upstream: ln.Addr().String(), stopped: map[string]bool{}}, nil
}

func (d *demoPersistentSandboxes) List(context.Context) ([]model.PersistentSandboxStatus, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	docs := model.PersistentSandboxStatus{
		Name: "docs-preview", Repo: "acme/docs", Ref: "main", Command: "npm run preview -- --port 4281",
		State: "running", Enabled: true, Sandbox: "persistent-docs-preview", StartedAt: d.started,
		SandboxPort: 4281, UISubpath: model.PersistentSandboxSubpath("docs-preview"),
	}
	staging := model.PersistentSandboxStatus{
		Name: "grain-staging", Repo: "bwsalmon/grain", Ref: "candidate", Command: "grain demo -addr :4290",
		State: "restarting", Enabled: true, Sandbox: "persistent-grain-staging", Restarts: 4,
		LastError: "exited with status 1", NextAttempt: time.Now().UTC().Add(40 * time.Second),
		SandboxPort: ports.Demo, HostPort: ports.StagingGrain,
		Events: []model.PersistentSandboxEvent{
			{At: d.started, Kind: "start", Message: `running "grain demo -addr :4290"`},
			{At: d.started.Add(3 * time.Second), Kind: "exit", Message: "the command exited with status 1 after 3s"},
		},
	}
	for _, st := range []*model.PersistentSandboxStatus{&docs, &staging} {
		if d.stopped[st.Name] {
			st.State, st.StartedAt, st.NextAttempt = "stopped", time.Time{}, time.Time{}
		}
	}
	return []model.PersistentSandboxStatus{docs, staging}, nil
}

func (d *demoPersistentSandboxes) Logs(name string) (string, bool) {
	switch name {
	case "docs-preview":
		return "> preview\n  Local: http://localhost:4281/\n", true
	case "grain-staging":
		return "grain: listen tcp :4290: bind: address already in use\n", true
	}
	return "", false
}

func (d *demoPersistentSandboxes) Control(_ context.Context, name, action string) error {
	if name != "docs-preview" && name != "grain-staging" {
		return fmt.Errorf("no persistent sandbox %q", name)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	switch action {
	case "stop":
		d.stopped[name] = true
	case "start", "restart", "rebuild":
		delete(d.stopped, name)
	}
	return nil
}

func (d *demoPersistentSandboxes) Dial(ctx context.Context, name string) (net.Conn, error) {
	if name != "docs-preview" {
		return nil, fmt.Errorf("persistent sandbox %q is not served under the UI", name)
	}
	var dialer net.Dialer
	return dialer.DialContext(ctx, "tcp", d.upstream)
}

func (d *demoPersistentSandboxes) StripPrefix(name string) (strip, served bool) {
	return true, name == "docs-preview"
}
