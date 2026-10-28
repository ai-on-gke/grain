package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/agent/claude"
	"github.com/bwsalmon/grain/pkg/daemon"
	"github.com/bwsalmon/grain/pkg/dispatch"
	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/orchestrator"
	"github.com/bwsalmon/grain/pkg/secrets"
	"github.com/bwsalmon/grain/pkg/ui"
)

// discoveredAnthropicModels is what the stub provider below lists: twelve
// models, none of which the curated connection advertises but one.
var discoveredAnthropicModels = []string{
	"claude-opus-5-5", "claude-sonnet-5-5", "claude-haiku-4-5", "claude-fable-5-1",
	"claude-opus-5", "claude-sonnet-5", "claude-opus-4-1", "claude-sonnet-4-5",
	"claude-3-7-sonnet", "claude-3-5-haiku", "claude-opus-4", "claude-sonnet-4",
}

// discoveredModelsProvider is Anthropic's GET /v1/models, the one
// boundary stubbed here, answering 503 while down is set.
func discoveredModelsProvider(t *testing.T, down *atomic.Bool) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		if down.Load() {
			http.Error(w, "upstream unavailable", http.StatusServiceUnavailable)
			return
		}
		type entry struct {
			ID          string `json:"id"`
			DisplayName string `json:"display_name"`
		}
		var data []entry
		for _, id := range discoveredAnthropicModels {
			data = append(data, entry{ID: id, DisplayName: id})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data, "has_more": false})
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

// §5 of docs/test-plans/2026-10-06.md: a connection saved while its
// provider cannot be reached is filled by RunModelDiscovery's retry once
// the provider answers, a curated connection beside it is left alone,
// and a task naming a model only discovery advertised is dispatched to
// the connection that discovered it.
func TestDiscoveredModelIsRetriedAndRoutesDispatch(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	secretStore := testSecrets(t)
	logPath := filepath.Join(t.TempDir(), "dispatches")
	t.Setenv("GRAIN_MODEL_DISPATCH_LOG", logPath)

	var down atomic.Bool
	down.Store(true)
	provider, calls := discoveredModelsProvider(t, &down)

	backends := []model.AgentBackend{
		{ID: "claude-curated", Name: "Curated", Framework: model.AgentFrameworkClaude, Models: []string{"claude-sonnet-5"}},
		{ID: "claude-discovered", Name: "Discovered", Framework: model.AgentFrameworkClaude},
	}
	if err := store.PutConfig(ctx, model.Config{AgentBackends: backends}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"claude-curated", "claude-discovered"} {
		if err := secretStore.Set(secrets.AgentBackendCredentialSecret(id), secrets.AgentCredentialKey, []byte("sk-ant-oat01-"+id)); err != nil {
			t.Fatal(err)
		}
	}
	client := ui.NewClient(ui.Config{ClaudeModels: &daemon.ClaudeModelLister{
		Live:  daemon.NewLiveConfig(store, nil, daemon.Config{}, nil),
		Creds: secretStore,
		List: func(ctx context.Context, token string) ([]claude.Model, error) {
			return claude.CatalogFrom(ctx, provider.Client(), provider.URL, token)
		},
	}}, store)

	advertised := func(id string) []string {
		cfg, err := store.GetConfig(ctx)
		if err != nil || cfg == nil {
			t.Fatalf("reading config: %v", err)
		}
		for _, b := range cfg.AgentBackends {
			if b.ID != id {
				continue
			}
			// A connection with no saved models advertises what its
			// provider last listed (grain/task-4320), which nothing
			// writes into its Models; before the provider has answered
			// that is nothing discovered at all.
			if b.AdvertisesDiscovered() {
				if len(b.Models) != 0 {
					t.Fatalf("connection %q had discovered models saved as its own: %v", id, b.Models)
				}
				if b.Discovered == nil {
					return nil
				}
				return b.Discovered.Models
			}
			return b.Models
		}
		t.Fatalf("no connection %q", id)
		return nil
	}

	// The 10-minute retry, shortened: while the provider is down every
	// pass asks and fills nothing.
	runCtx, stop := context.WithCancel(ctx)
	defer stop()
	go client.RunModelDiscovery(runCtx, 20*time.Millisecond)
	for deadline := time.Now().Add(5 * time.Second); calls.Load() < 3; {
		if time.Now().After(deadline) {
			t.Fatalf("discovery asked the provider %d times in 5s, want at least 3 retries", calls.Load())
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := advertised("claude-discovered"); len(got) != 0 {
		t.Fatalf("connection filled while the provider was down: %v", got)
	}

	// The provider comes back; the next pass fills the empty connection.
	down.Store(false)
	for deadline := time.Now().Add(5 * time.Second); len(advertised("claude-discovered")) == 0; {
		if time.Now().After(deadline) {
			t.Fatal("connection still advertises nothing 5s after its provider came back")
		}
		time.Sleep(10 * time.Millisecond)
	}
	stop()
	if got := advertised("claude-discovered"); !slices.Equal(got, discoveredAnthropicModels) {
		t.Fatalf("discovered connection advertises %v, want the provider's %v", got, discoveredAnthropicModels)
	}
	if got := advertised("claude-curated"); !slices.Equal(got, []string{"claude-sonnet-5"}) {
		t.Fatalf("curated connection rewritten to %v", got)
	}

	// Dispatch: a task naming claude-opus-4-1, which only discovery
	// advertised, runs on the connection that discovered it, with that
	// connection's own credential.
	build := daemon.AgentFrameworks(daemon.Config{ClaudePath: modelDispatchClaudeStub(t)}, store, secretStore)
	human := model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "alice"}}
	task := model.Task{
		ID: "t-discovered", Intent: model.IntentImplement, Title: "Run on a discovered model",
		Origin:   model.Origin{Attribution: human, Reason: model.ReasonDirect},
		Approval: &human, Model: "claude-opus-4-1",
	}
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	run := dispatch.Dispatch{TaskID: task.ID, RunID: dispatch.RunID(task.ID, 1), Attempt: 1}
	if err := store.StartRun(ctx, model.Run{ID: run.RunID, TaskID: task.ID, Attempt: 1, StartedAt: now}, model.Limits{}); err != nil {
		t.Fatal(err)
	}
	framework, err := build(ctx, orchestrator.AgentChoice{Framework: task.AgentFramework, Model: task.Model})
	if err != nil {
		t.Fatalf("building for the discovered model: %v", err)
	}
	if got := framework.(*daemon.BackendFramework).Id; got != "claude-discovered" {
		t.Fatalf("dispatched to %q, want claude-discovered", got)
	}
	if _, err := orchestrator.RunDispatch(ctx, store, framework, orchestrator.Config{
		AgentFramework: model.AgentFrameworkClaude, Now: func() time.Time { return now },
	}, task, run, nil, t.TempDir(), "", nil, now); err != nil {
		t.Fatalf("dispatching on the discovered model: %v", err)
	}
	logged, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if want := "claude|claude-opus-4-1||sk-ant-oat01-claude-discovered"; strings.TrimSpace(string(logged)) != want {
		t.Fatalf("agent invoked as %q, want %q", strings.TrimSpace(string(logged)), want)
	}
	runs, err := store.Runs(ctx, task.ID)
	if err != nil || len(runs) != 1 || runs[0].Outcome != "succeeded" {
		t.Fatalf("runs = %+v (%v), want one successful run", runs, err)
	}
}
