package security

import (
	"context"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bwsalmon/grain/pkg/daemon"
	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/ui"
	"github.com/bwsalmon/grain/pkg/whererun"
)

// TestCLIRunSettingsStoreNoImage holds PUT /api/settings/cli-run to the
// location alone. Releases before v2.3.0 took an image beside it, which
// their per-run docker launcher started the CLI from with its
// credential; that launcher is gone, and an image sent now is not stored
// for anything to start.
func TestCLIRunSettingsStoreNoImage(t *testing.T) {
	ui.ResetIAPConfig()
	t.Cleanup(ui.ResetIAPConfig)

	f := newRouteFixtureFor(t, true)
	body := `{"settings":{"claude":{"location":"container","image":"--privileged"}}}`
	rec := f.serve(t, http.MethodPut, "/api/settings/cli-run", body, http.Header{})
	if rec.code != http.StatusOK {
		t.Fatalf("PUT /api/settings/cli-run: %d %s, want 200", rec.code, rec.excerpt())
	}
	if strings.Contains(rec.body.String(), "privileged") {
		t.Errorf("the answer carries the image back: %s", rec.excerpt())
	}
	cfg, err := f.store.GetConfig(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.CLIRun[model.AgentFrameworkClaude]; got != (model.CLIRunSetting{Location: model.CLIRunContainer}) {
		t.Errorf("stored %+v, want the location alone", got)
	}
}

// TestCLIRunSettingsAreAPersonsAndAudited holds the same route to a
// person -- an agent's in-process request is refused -- and has a change
// of location recorded in the credential audit, as a connection's CLI
// path is: where the CLI runs is what runs with its credential.
func TestCLIRunSettingsAreAPersonsAndAudited(t *testing.T) {
	ui.ResetIAPConfig()
	t.Cleanup(ui.ResetIAPConfig)

	body := `{"settings":{"codex":{"location":"host"}}}`

	f := newRouteFixtureFor(t, true)
	if rec := serveAsAgent(t, f, http.MethodPut, "/api/settings/cli-run", body); rec.code != http.StatusForbidden {
		t.Errorf("an agent got %d %s, want 403: choosing where a CLI runs is a person's", rec.code, rec.excerpt())
	}
	if cfg, _ := f.store.GetConfig(context.Background()); cfg != nil && len(cfg.CLIRun) != 0 {
		t.Errorf("an agent's refused save stored %v", cfg.CLIRun)
	}

	if rec := f.serve(t, http.MethodPut, "/api/settings/cli-run", body, http.Header{}); rec.code != http.StatusOK {
		t.Fatalf("the access code's session: %d %s, want 200", rec.code, rec.excerpt())
	}
	entries, err := f.store.ListCredentialAccess(context.Background(), 50)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range entries {
		if e.Resource == "cli-run/"+model.AgentFrameworkCodex && strings.Contains(e.Outcome, `"host"`) {
			found = true
		}
	}
	if !found {
		t.Errorf("saving codex's location left no credential-access audit entry: %+v", entries)
	}
}

// TestCLIRunSettingsRefuseHostWhereThereIsNoHost holds PUT
// /api/settings/cli-run, as a daemon with no host executor serves it
// (daemon.Config.CheckCLIRunSettings) -- Kubernetes, or a container
// with no grain hostd -- to refusing "host" for every CLI with a 400
// that says why, storing nothing; a container setting still saves. A
// daemon whose hostd is configured accepts host, down or not.
func TestCLIRunSettingsRefuseHostWhereThereIsNoHost(t *testing.T) {
	ui.ResetIAPConfig()
	t.Cleanup(ui.ResetIAPConfig)

	inContainer := whererun.Detection{Host: false, Signal: "GRAIN_IN_CONTAINER=1"}
	noHostd := func(string) bool { return false }
	daemonUI := func(cfg daemon.Config) func(*ui.Config) {
		exec := daemon.DetectHostExecutor(cfg, inContainer, noHostd)
		cfg.HostExec = &exec
		return func(c *ui.Config) {
			c.CLIRunCheck = cfg.CheckCLIRunSettings
			c.HostExecutor = func(ctx context.Context) any { return cfg.HostExecutorStatus(ctx) }
		}
	}
	for name, cfg := range map[string]daemon.Config{
		"kubernetes":         {K8sSandboxes: true},
		"container no hostd": {},
	} {
		for _, fw := range model.CLIRunFrameworks {
			t.Run(name+"/"+fw, func(t *testing.T) {
				f := newRouteFixtureWith(t, true, false, daemonUI(cfg))
				rec := f.serve(t, http.MethodPut, "/api/settings/cli-run", `{"settings":{"`+fw+`":{"location":"host"}}}`, http.Header{})
				if rec.code != http.StatusBadRequest || !strings.Contains(rec.body.String(), "no host executor") {
					t.Fatalf("PUT host for %s: %d %s, want a 400 saying there is no host executor", fw, rec.code, rec.excerpt())
				}
				if c, _ := f.store.GetConfig(context.Background()); c != nil && len(c.CLIRun) != 0 {
					t.Errorf("a refused host was stored: %v", c.CLIRun)
				}
				rec = f.serve(t, http.MethodPut, "/api/settings/cli-run", `{"settings":{"`+fw+`":{"location":"container"}}}`, http.Header{})
				if rec.code != http.StatusOK {
					t.Errorf("PUT container for %s: %d %s, want 200", fw, rec.code, rec.excerpt())
				}
				rec = f.serve(t, http.MethodGet, "/api/settings/cli-run", "", http.Header{})
				if rec.code != http.StatusOK || !strings.Contains(rec.body.String(), `"hostAvailable":false`) {
					t.Errorf("GET /api/settings/cli-run: %d %s, want the host executor reported unavailable", rec.code, rec.excerpt())
				}
			})
		}
	}

	t.Run("bare machine refuses container", func(t *testing.T) {
		cfg := daemon.Config{}
		f := newRouteFixtureWith(t, true, false, func(c *ui.Config) { c.CLIRunCheck = cfg.CheckCLIRunSettings })
		rec := f.serve(t, http.MethodPut, "/api/settings/cli-run", `{"settings":{"claude":{"location":"container"}}}`, http.Header{})
		if rec.code != http.StatusBadRequest || !strings.Contains(rec.body.String(), "container is not available") {
			t.Fatalf("PUT container on the bare machine: %d %s, want a 400 saying why", rec.code, rec.excerpt())
		}
		if c, _ := f.store.GetConfig(context.Background()); c != nil && len(c.CLIRun) != 0 {
			t.Errorf("a refused container was stored: %v", c.CLIRun)
		}
	})

	t.Run("hostd configured but down", func(t *testing.T) {
		f := newRouteFixtureWith(t, true, false, daemonUI(daemon.Config{HostdSocket: filepath.Join(t.TempDir(), "hostd.sock")}))
		rec := f.serve(t, http.MethodPut, "/api/settings/cli-run", `{"settings":{"antigravity":{"location":"host"}}}`, http.Header{})
		if rec.code != http.StatusOK {
			t.Fatalf("PUT host for agy with hostd configured: %d %s, want 200", rec.code, rec.excerpt())
		}
		if !strings.Contains(rec.body.String(), `"reachable":false`) {
			t.Errorf("the answer does not report hostd unreachable: %s", rec.excerpt())
		}
	})
}
