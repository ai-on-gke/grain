package daemon

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/agent/antigravity"
	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/model/sqlite"
	"github.com/bwsalmon/grain/pkg/orchestrator"
)

// grain/task-4527: grain compiles in no model name. A task that chose no
// model runs on its team's default when a connection serves it, on the
// provider's own default while the team has none, and waits -- saying
// so -- on a connection that advertises nothing at all.

func providerDefaultPool(t *testing.T, teamDefault string, backends ...model.AgentBackend) *AgentPool {
	t.Helper()
	db, err := sqlite.Open(sqlite.DefaultConfig(filepath.Join(t.TempDir(), "data")))
	if err != nil {
		t.Fatalf("opening embedded sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	store := model.New(db)
	ctx := context.Background()
	if err := store.Init(ctx); err != nil {
		t.Fatalf("applying schema: %v", err)
	}
	if err := store.UpdateConfig(ctx, func(tm *model.Config) error {
		tm.DefaultModel = teamDefault
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	cfg, err := store.GetConfig(ctx)
	if err != nil || cfg == nil {
		cfg = &model.Config{}
	}
	cfg.AgentBackends = backends
	if err := store.PutConfig(ctx, *cfg); err != nil {
		t.Fatal(err)
	}
	return NewAgentPool(Config{AgyPath: "/bin/true", AgentAuthMode: model.AntigravityAuthAmbient}, store, nil)
}

func discovered(models ...string) *model.DiscoveredModels {
	return &model.DiscoveredModels{Models: models, RefreshedAt: time.Now()}
}

func TestDefaultModelResolution(t *testing.T) {
	t.Parallel()
	agy := model.AgentBackend{ID: "agy", Framework: model.AgentFrameworkAntigravity,
		AuthMode: model.AntigravityAuthAmbient, Discovered: discovered("gemini-3.8-flash", "gemini-3.1-pro")}
	ctx := context.Background()
	for name, tc := range map[string]struct {
		teamDefault string
		wantModel   string // what resolve chose; "" leaves it to the connection
		wantRun     string // what agy is asked for
		wantWait    string
	}{
		"team default in the discovered list":  {teamDefault: "gemini-3.1-pro", wantModel: "gemini-3.1-pro", wantRun: "gemini-3.1-pro"},
		"team default not in the list waits":   {teamDefault: "gemini-0-gone", wantModel: "gemini-0-gone", wantWait: "serves model gemini-0-gone"},
		"no team default runs agy's own auto":  {teamDefault: "", wantModel: "", wantRun: model.ProviderModelAuto},
		"multiple defaults picks first served": {teamDefault: "gemini-0-gone, gemini-3.1-pro, gemini-3.8-flash", wantModel: "gemini-0-gone", wantRun: "gemini-3.1-pro"},
	} {
		t.Run(name, func(t *testing.T) {
			pool := providerDefaultPool(t, tc.teamDefault, agy)
			live := DispatchConfig(ctx, pool.Store, pool.cfg)
			req, err := pool.resolve(ctx, live, orchestrator.AgentChoice{})
			if err != nil {
				t.Fatalf("resolve: %v", err)
			}
			if req.model != tc.wantModel {
				t.Errorf("resolved model = %q, want %q", req.model, tc.wantModel)
			}
			reason := pool.Availability(ctx, orchestrator.AgentChoice{})
			if tc.wantWait != "" {
				if !strings.Contains(reason, tc.wantWait) {
					t.Errorf("wait reason = %q, want it to contain %q", reason, tc.wantWait)
				}
				return
			}
			if reason != "" {
				t.Fatalf("waits: %q", reason)
			}
			built, err := pool.Build(ctx, orchestrator.AgentChoice{})
			if err != nil {
				t.Fatalf("Build: %v", err)
			}
			bf := built.(*BackendFramework)
			defer bf.Lease.Release()
			f := bf.Framework.(*antigravity.Framework)
			if f.Model() != tc.wantRun {
				t.Errorf("agy asked for model %q, want %q", f.Model(), tc.wantRun)
			}
			if tc.wantRun == model.ProviderModelAuto && f.Effort() != "" {
				t.Errorf("auto asked for --effort %q, want none", f.Effort())
			}
		})
	}
}

// A connection with no models -- a provider that never answered, a
// custom command with an empty list -- is not given a made-up name: a
// task that would run on it waits, naming the connection.
func TestNoModelsIsAWaitNotAGuess(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for name, b := range map[string]model.AgentBackend{
		"provider never answered": {ID: "agy", Framework: model.AgentFrameworkAntigravity, AuthMode: model.AntigravityAuthAmbient,
			Discovered: &model.DiscoveredModels{Error: "agy: no key"}},
		"custom command with no list": {ID: "house", Framework: model.AgentFrameworkCustom, Command: "run-agent"},
	} {
		t.Run(name, func(t *testing.T) {
			pool := providerDefaultPool(t, "", b)
			want := `connection "` + b.ID + `" has no models -- set one`
			if reason := pool.Availability(ctx, orchestrator.AgentChoice{}); !strings.HasPrefix(reason, want) {
				t.Errorf("rotation wait reason = %q, want it to start %q", reason, want)
			}
			if reason := pool.Availability(ctx, orchestrator.AgentChoice{BackendID: b.ID}); !strings.HasPrefix(reason, want) {
				t.Errorf("pinned wait reason = %q, want it to start %q", reason, want)
			}
			if _, err := pool.Build(ctx, orchestrator.AgentChoice{}); err == nil || !strings.Contains(err.Error(), "no models") {
				t.Errorf("Build = %v, want a no-models refusal", err)
			}
		})
	}
	// Once somebody picks a model, the same connection runs it.
	pool := providerDefaultPool(t, "house-model",
		model.AgentBackend{ID: "house", Framework: model.AgentFrameworkCustom, Command: "run-agent", Models: []string{"house-model"}})
	if reason := pool.Availability(ctx, orchestrator.AgentChoice{}); reason != "" {
		t.Errorf("a connection with a model waits: %q", reason)
	}
}
