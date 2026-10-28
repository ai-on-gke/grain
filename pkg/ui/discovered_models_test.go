package ui_test

// grain/task-2308: adding a provider should be enough to make every model
// its key reaches a choice in the Model pickers. grain/task-4320: and it
// keeps being enough -- what a connection with no saved models advertises
// is what its provider reports now, not a list saved once.

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/secrets"
	"github.com/ai-on-gke/grain/pkg/ui"
)

// mutableModelLister answers with whatever its catalog says now, or
// err, and counts the calls -- a provider that adds and drops models.
type mutableModelLister struct {
	mu    sync.Mutex
	ids   []string
	err   error
	calls int
}

func (l *mutableModelLister) set(err error, ids ...string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.ids, l.err = ids, err
}

func (l *mutableModelLister) ListModels(context.Context, string) (ui.AgentModelCatalog, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls++
	if l.err != nil {
		return ui.AgentModelCatalog{}, l.err
	}
	var catalog ui.AgentModelCatalog
	for _, id := range l.ids {
		catalog.Models = append(catalog.Models, ui.AgentModel{ID: id})
	}
	return catalog, nil
}

func putBackends(t *testing.T, client *ui.Client, backends ...model.AgentBackend) {
	t.Helper()
	ctx := context.Background()
	cfg, err := client.Store.GetConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if cfg == nil {
		cfg = &model.Config{}
	}
	cfg.AgentBackends = backends
	if err := client.Store.PutConfig(ctx, *cfg); err != nil {
		t.Fatal(err)
	}
}

func storedBackend(t *testing.T, client *ui.Client, id string) model.AgentBackend {
	t.Helper()
	cfg, err := client.Store.GetConfig(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range cfg.AgentBackends {
		if b.ID == id {
			return b
		}
	}
	t.Fatalf("no connection %q stored", id)
	return model.AgentBackend{}
}

// grain/task-4320: a connection with no saved models advertises what its
// provider reports now -- not a snapshot written into its Models once --
// and a pinned list, a custom command and a provider that cannot be
// asked are each left as they were.
func TestDiscoveredModelsAreAdvertisedLiveNotSaved(t *testing.T) {
	t.Parallel()
	_, client := testServer(t)
	ctx := context.Background()
	gemini := &mutableModelLister{}
	gemini.set(nil, "gemini-3.1-pro", "gemini-3.7-flash", "gemini-3.7-flash", " ")
	client.Config.AgentModels = gemini
	client.Config.ClaudeModels = fakeModelLister{err: errors.New("no Anthropic key")}
	putBackends(t, client,
		model.AgentBackend{ID: "gemini-new", Framework: model.AgentFrameworkAntigravity},
		model.AgentBackend{ID: "gemini-pinned", Framework: model.AgentFrameworkAntigravity, Models: []string{"gemini-3.1-pro"}},
		model.AgentBackend{ID: "gemini-excluding", Framework: model.AgentFrameworkAntigravity, ExcludedModels: []string{"gemini-3.7-flash"}},
		model.AgentBackend{ID: "claude-nokey", Framework: model.AgentFrameworkClaude},
		model.AgentBackend{ID: "codex-unwired", Framework: model.AgentFrameworkCodex},
		model.AgentBackend{ID: "custom", Framework: model.AgentFrameworkCustom, Command: "run-agent", Models: []string{"house-model"}},
	)

	if _, err := client.AdvertiseDiscoveredModels(ctx); err != nil {
		t.Fatal(err)
	}
	advertised := func(id string) []string { return storedBackend(t, client, id).SupportedModels() }
	for id, want := range map[string][]string{
		"gemini-new":       {"gemini-3.1-pro", "gemini-3.7-flash"},
		"gemini-pinned":    {"gemini-3.1-pro"},
		"gemini-excluding": {"gemini-3.1-pro"},
		// Never answered: no models, rather than a name grain made up.
		"claude-nokey":  nil,
		"codex-unwired": nil,
		"custom":        {"house-model"},
	} {
		if got := advertised(id); !slices.Equal(got, want) {
			t.Errorf("%s advertises %v, want %v", id, got, want)
		}
	}
	if b := storedBackend(t, client, "gemini-new"); len(b.Models) != 0 || b.Discovered == nil || b.Discovered.RefreshedAt.IsZero() {
		t.Errorf("gemini-new stored Models %v, Discovered %+v: want no saved models and a dated discovered list", b.Models, b.Discovered)
	}
	if b := storedBackend(t, client, "claude-nokey"); b.Discovered == nil || b.Discovered.Error != "no Anthropic key" {
		t.Errorf("claude-nokey Discovered = %+v, want the lister's error", b.Discovered)
	}
	if b := storedBackend(t, client, "custom"); b.Discovered != nil {
		t.Errorf("a custom command was asked for its models: %+v", b.Discovered)
	}

	// An unchanged answer writes nothing.
	if n, err := client.AdvertiseDiscoveredModels(ctx); err != nil || n != 0 {
		t.Fatalf("a pass with nothing new changed %d connections (err %v), want 0", n, err)
	}

	// The provider adds one model and drops another: the next pass is
	// all it takes, with nothing saved by hand.
	gemini.set(nil, "gemini-3.1-pro", "gemini-4-ultra")
	if _, err := client.AdvertiseDiscoveredModels(ctx); err != nil {
		t.Fatal(err)
	}
	if got, want := advertised("gemini-new"), []string{"gemini-3.1-pro", "gemini-4-ultra"}; !slices.Equal(got, want) {
		t.Errorf("after the provider changed, gemini-new advertises %v, want %v", got, want)
	}
	if got, want := advertised("gemini-pinned"), []string{"gemini-3.1-pro"}; !slices.Equal(got, want) {
		t.Errorf("a pinned list widened to %v, want %v", got, want)
	}
	if b := storedBackend(t, client, "gemini-new"); b.NoLongerDiscovers("gemini-4-ultra") || !b.NoLongerDiscovers("gemini-3.7-flash") {
		t.Errorf("NoLongerDiscovers is wrong for %+v", b.Discovered)
	}
}

// A provider that stops answering keeps the last list it gave, with the
// error beside it, rather than falling back to the framework default.
func TestDiscoveredModelsSurviveAnUnreachableProvider(t *testing.T) {
	t.Parallel()
	_, client := testServer(t)
	ctx := context.Background()
	claude := &mutableModelLister{}
	claude.set(nil, "claude-opus-5-5", "claude-haiku-4-5")
	client.Config.ClaudeModels = claude
	putBackends(t, client, model.AgentBackend{ID: "c", Framework: model.AgentFrameworkClaude})
	if _, err := client.AdvertiseDiscoveredModels(ctx); err != nil {
		t.Fatal(err)
	}
	refreshed := storedBackend(t, client, "c").Discovered.RefreshedAt

	claude.set(errors.New("503 upstream unavailable"))
	if n, err := client.AdvertiseDiscoveredModels(ctx); err != nil || n != 1 {
		t.Fatalf("recording the failure changed %d (err %v), want 1", n, err)
	}
	b := storedBackend(t, client, "c")
	if got, want := b.SupportedModels(), []string{"claude-opus-5-5", "claude-haiku-4-5"}; !slices.Equal(got, want) {
		t.Errorf("while unreachable it advertises %v, want the last good list %v", got, want)
	}
	if b.Discovered.Error != "503 upstream unavailable" || !b.Discovered.RefreshedAt.Equal(refreshed) {
		t.Errorf("Discovered = %+v, want the error and the last good list's own time", b.Discovered)
	}
	// The same failure again is not news.
	if n, _ := client.AdvertiseDiscoveredModels(ctx); n != 0 {
		t.Errorf("a repeated failure changed %d connections, want 0", n)
	}
	claude.set(nil, "claude-opus-5-5", "claude-haiku-4-5")
	if _, err := client.AdvertiseDiscoveredModels(ctx); err != nil {
		t.Fatal(err)
	}
	if b := storedBackend(t, client, "c"); b.Discovered.Error != "" {
		t.Errorf("the provider answered again but the error stayed: %+v", b.Discovered)
	}
}

// Connections saved before discovery was live: a list exactly equal to
// what the provider reports was the old fill (or an "advertise all") and
// becomes auto-advertising; any other list is a curation and stays a
// pin. Once a connection has discovered under this scheme it is never
// migrated again, so a later hand-pinned full list stays pinned.
func TestDiscoveryMigratesOnlyListsThatMatchTheProvider(t *testing.T) {
	t.Parallel()
	_, client := testServer(t)
	ctx := context.Background()
	codex := &mutableModelLister{}
	codex.set(nil, "gpt-5.5", "gpt-5.5-mini", "gpt-5.1-codex")
	client.Config.CodexModels = codex
	putBackends(t, client,
		model.AgentBackend{ID: "filled", Framework: model.AgentFrameworkCodex, Models: []string{"gpt-5.1-codex", "gpt-5.5", "gpt-5.5-mini"}},
		model.AgentBackend{ID: "subset", Framework: model.AgentFrameworkCodex, Models: []string{"gpt-5.5"}},
		model.AgentBackend{ID: "superset", Framework: model.AgentFrameworkCodex, Models: []string{"gpt-5.5", "gpt-5.5-mini", "gpt-5.1-codex", "o3"}},
	)
	if _, err := client.AdvertiseDiscoveredModels(ctx); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string][]string{
		"filled":   nil,
		"subset":   {"gpt-5.5"},
		"superset": {"gpt-5.5", "gpt-5.5-mini", "gpt-5.1-codex", "o3"},
	} {
		if got := storedBackend(t, client, id).Models; !slices.Equal(got, want) {
			t.Errorf("%s saved models = %v, want %v", id, got, want)
		}
	}

	// Pinned by hand to everything after discovering: not migrated.
	b := storedBackend(t, client, "subset")
	b.Models = []string{"gpt-5.5", "gpt-5.5-mini", "gpt-5.1-codex"}
	putBackends(t, client, storedBackend(t, client, "filled"), b)
	codex.set(nil, "gpt-5.5", "gpt-5.5-mini", "gpt-5.1-codex", "gpt-6")
	if _, err := client.AdvertiseDiscoveredModels(ctx); err != nil {
		t.Fatal(err)
	}
	if got := storedBackend(t, client, "subset").SupportedModels(); slices.Contains(got, "gpt-6") {
		t.Errorf("a hand-pinned list widened to %v", got)
	}
	if got := storedBackend(t, client, "filled").SupportedModels(); !slices.Contains(got, "gpt-6") {
		t.Errorf("a migrated connection did not pick up a new model: %v", got)
	}
}

// RunModelDiscovery asks again every interval, so a model the provider
// adds reaches the connection without a save.
func TestModelDiscoveryRefreshesOnItsInterval(t *testing.T) {
	t.Parallel()
	_, client := testServer(t)
	claude := &mutableModelLister{}
	claude.set(nil, "claude-opus-5-5")
	client.Config.ClaudeModels = claude
	putBackends(t, client, model.AgentBackend{ID: "c", Framework: model.AgentFrameworkClaude})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go client.RunModelDiscovery(ctx, 20*time.Millisecond)
	waitAdvertises(t, client, "c", []string{"claude-opus-5-5"})
	claude.set(nil, "claude-opus-5-5", "claude-fable-5-1")
	waitAdvertises(t, client, "c", []string{"claude-opus-5-5", "claude-fable-5-1"})
}

func waitAdvertises(t *testing.T, client *ui.Client, id string, want []string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		got := storedBackend(t, client, id).SupportedModels()
		if slices.Equal(got, want) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s advertises %v, want %v", id, got, want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Saving a connection's credential asks its provider again at once, and
// a settings save never writes the discovered list itself: it carries
// the stored one over, whatever the request said.
func TestCredentialSaveRefreshesAndSettingsSavesKeepDiscovered(t *testing.T) {
	t.Parallel()
	srv, client := testServer(t)
	storeSettingsRow(t, client)
	client.Config.Secrets = secrets.New(t.TempDir())
	claude := &mutableModelLister{}
	claude.set(errors.New("no Claude Code OAuth token is configured"))
	client.Config.ClaudeModels = claude
	if rec := do(t, srv, http.MethodPut, "/api/deployment/agent-backends/c", `{"framework":"claude"}`); rec.Code != http.StatusOK {
		t.Fatalf("saving the connection: %d %s", rec.Code, rec.Body)
	}
	deadline := time.Now().Add(5 * time.Second)
	for storedBackend(t, client, "c").Discovered == nil {
		if time.Now().After(deadline) {
			t.Fatal("the save never asked the provider")
		}
		time.Sleep(10 * time.Millisecond)
	}
	claude.set(nil, "claude-opus-5-5", "claude-sonnet-5-5")
	if rec := do(t, srv, http.MethodPut, "/api/agent-backends/c/credential", `{"value":"sk-ant-oat"}`); rec.Code != http.StatusOK {
		t.Fatalf("saving the credential: %d %s", rec.Code, rec.Body)
	}
	waitAdvertises(t, client, "c", []string{"claude-opus-5-5", "claude-sonnet-5-5"})

	// A save naming an exclusion, and a made-up discovered list.
	if rec := do(t, srv, http.MethodPut, "/api/deployment/agent-backends/c",
		`{"framework":"claude","excludedModels":[" claude-sonnet-5-5 "],"discovered":{"models":["claude-made-up"]}}`); rec.Code != http.StatusOK {
		t.Fatalf("saving the exclusion: %d %s", rec.Code, rec.Body)
	}
	b := storedBackend(t, client, "c")
	if got, want := b.SupportedModels(), []string{"claude-opus-5-5"}; !slices.Equal(got, want) {
		t.Errorf("after excluding, c advertises %v, want %v", got, want)
	}
	if !slices.Equal(b.ExcludedModels, []string{"claude-sonnet-5-5"}) {
		t.Errorf("excludedModels saved as %q", b.ExcludedModels)
	}
}

// hungModelLister never answers until release is closed, whatever its
// context says -- the worst a provider's CLI can do. started is closed
// on the first call.
type hungModelLister struct {
	once    *sync.Once
	started chan struct{}
	release chan struct{}
}

func newHungModelLister(t *testing.T) hungModelLister {
	l := hungModelLister{once: &sync.Once{}, started: make(chan struct{}), release: make(chan struct{})}
	t.Cleanup(func() { close(l.release) })
	return l
}

func (l hungModelLister) ListModels(context.Context, string) (ui.AgentModelCatalog, error) {
	l.once.Do(func() { close(l.started) })
	<-l.release
	return ui.AgentModelCatalog{}, errors.New("released")
}

// grain/task-3903: saving a connection kicks model discovery, which asks
// the connection's provider what it serves. That is the background's
// business: a lister that hangs must not hold up the save, the next
// save, or the GET /api/config the Team settings pane reads after one.
func TestSavingAConnectionDoesNotWaitOnAHungModelLister(t *testing.T) {
	t.Parallel()
	srv, client := testServer(t)
	storeSettingsRow(t, client)
	lister := newHungModelLister(t)
	client.Config.ClaudeModels = lister

	within := func(what string, fn func() *httptest.ResponseRecorder) {
		t.Helper()
		done := make(chan *httptest.ResponseRecorder, 1)
		go func() { done <- fn() }()
		select {
		case rec := <-done:
			if rec.Code != http.StatusOK {
				t.Fatalf("%s: code %d: %s", what, rec.Code, rec.Body.String())
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%s did not answer while a model lister hung", what)
		}
	}
	// No models, so discovery asks the lister for them.
	within("first save", func() *httptest.ResponseRecorder {
		return do(t, srv, http.MethodPut, "/api/deployment/agent-backends/hung", `{"framework":"claude"}`)
	})
	select {
	case <-lister.started:
	case <-time.After(5 * time.Second):
		t.Fatal("the save never asked the lister for models")
	}
	within("GET /api/config", func() *httptest.ResponseRecorder {
		return do(t, srv, http.MethodGet, "/api/config", "")
	})
	within("second save", func() *httptest.ResponseRecorder {
		return do(t, srv, http.MethodPut, "/api/deployment/agent-backends/hung", `{"framework":"claude","name":"Hung"}`)
	})
	within("GET /api/config after the second save", func() *httptest.ResponseRecorder {
		return do(t, srv, http.MethodGet, "/api/config", "")
	})
}

// And a pass does not wait on such a lister for ever: each is given
// ModelDiscoveryListTimeout, after which the pass moves on and lets the
// next one -- a save's kick included -- run.
func TestModelDiscoveryGivesUpOnAListerThatDoesNotAnswer(t *testing.T) {
	t.Parallel()
	_, client := testServer(t)
	ctx := context.Background()
	client.ModelDiscoveryListTimeout = 50 * time.Millisecond
	client.Config.ClaudeModels = ctxModelLister{}
	if err := client.Store.PutConfig(ctx, model.Config{AgentBackends: []model.AgentBackend{
		{ID: "slow", Framework: model.AgentFrameworkClaude},
	}}); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := client.AdvertiseDiscoveredModels(ctx)
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("discovery waited on a lister past its timeout")
	}
}

// ctxModelLister answers only when its context is done, as a CLI run
// under exec.CommandContext does when it is killed.
type ctxModelLister struct{}

func (ctxModelLister) ListModels(ctx context.Context, _ string) (ui.AgentModelCatalog, error) {
	<-ctx.Done()
	return ui.AgentModelCatalog{}, ctx.Err()
}
