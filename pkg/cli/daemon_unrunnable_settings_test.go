package cli

// daemon_unrunnable_settings_test.go is grain/task-316: a grain_config
// row carrying a value no daemon can run on -- a poll interval of 0
// above all -- must not be adopted just because it is stored.
//
// What it used to buy was a deployment that looked alive and did
// nothing: the UI server came up, the reconcile loop panicked out of
// time.NewTicker on its way into its first sleep, and the one line about
// the flag that would have supplied a usable interval said it was being
// ignored in favour of the 0. Every route to such a row is ordinary
// (runnableStoredConfig's own doc comment), which is why the daemon has
// to read it rather than trust it.

import (
	"context"
	"log"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/daemon"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

// storedButUnrunnable is the row this whole file is about: every value a
// deployment is actually paced by left at the zero model.DefaultConfig
// leaves them at, with the rest filled in so that nothing else in it is
// in question.
func storedButUnrunnable() model.Config {
	return model.Config{
		PollInterval: 0, MaxWorkers: 0, MaxMergers: -1,
		GitHubHost: "github.com",
	}
}

// flagsForUnrunnable is the daemon those flags started: checkConfig has
// already refused anything unusable here, so these are what the stored
// row's zeroes have to give way to.
func flagsForUnrunnable() daemon.Config {
	return daemon.Config{
		PollInterval: 30 * time.Second, MaxWorkers: 4, MaxMergers: 1,
		AgentFramework: model.AgentFrameworkAntigravity,
		GeminiModel:    "gemini-2.5-pro", ClaudeModel: "claude-sonnet-5",
		GithubHost: "github.com",
	}
}

func TestLoadConfigRefusesToBePacedByAStoredZero(t *testing.T) {
	store, db, err := daemon.OpenStore("sqlite", "", t.TempDir())
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	defer db.Close()
	ctx := context.Background()
	if err := store.PutConfig(ctx, storedButUnrunnable()); err != nil {
		t.Fatalf("PutConfig: %v", err)
	}

	var logs strings.Builder
	log.SetOutput(&logs)
	defer log.SetOutput(os.Stderr)

	flagCfg := flagsForUnrunnable()
	got, err := daemon.LoadConfig(ctx, store, flagCfg)
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if got.PollInterval != flagCfg.PollInterval {
		t.Errorf("poll interval = %s, want the flag's %s -- a ticker cannot be built from the stored 0",
			got.PollInterval, flagCfg.PollInterval)
	}

	// Said once, so that whoever finds the deployment running at 30s can
	// find out why it is not running at what the settings row says.
	out := logs.String()
	for _, want := range []string{
		"stored poll interval is 0s, which no daemon can run on -- running with -poll-interval=30s",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("loadConfig log = %q, want it to contain %q", out, want)
		}
	}
	// And the line that used to be there instead, which said the exact
	// opposite of what the daemon now does with the flag.
	if strings.Contains(out, "ignoring -poll-interval") {
		t.Errorf("loadConfig log = %q, want no claim that the flag was ignored: it is what the daemon is running on", out)
	}
}

// A stored row that is merely *different* from the flags is still the
// last word -- the fallback above is about values grain cannot run on,
// not a way back in for a flag somebody changed.
func TestLoadConfigStillPrefersAStoredIntervalItCanRunOn(t *testing.T) {
	t.Parallel()
	store, db, err := daemon.OpenStore("sqlite", "", t.TempDir())
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	defer db.Close()
	ctx := context.Background()
	stored := storedButUnrunnable()
	stored.PollInterval, stored.MaxWorkers, stored.MaxMergers = 5*time.Second, 2, 0
	if err := store.PutConfig(ctx, stored); err != nil {
		t.Fatalf("PutConfig: %v", err)
	}

	got, err := daemon.LoadConfig(ctx, store, flagsForUnrunnable())
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	if got.PollInterval != 5*time.Second {
		t.Fatalf("loaded %s, want the stored 5s", got.PollInterval)
	}
}

// The same row arriving under a *running* daemon -- a settings pull
// request merged into the state repository and imported on a sync tick
// -- must not stop the loop either: liveConfig keeps pacing by what the
// process is already running with rather than resetting the ticker to 0,
// which panics in exactly the same way.
func TestLiveConfigKeepsItsPaceWhenAStoredZeroArrives(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := testStore(t)
	cfg := flagsForUnrunnable()
	putConfig(t, store, cfg.ToModelConfig())
	live := daemon.NewLiveConfig(store, nil, cfg, nil)
	deps := orchestrator.Deps{}

	putConfig(t, store, storedButUnrunnable())
	if live.Refresh(ctx, &deps) {
		t.Error("refresh reported a changed poll interval, so the loop would reset its ticker to the stored 0")
	}
	now := live.Current()
	if now.PollInterval != cfg.PollInterval {
		t.Fatalf("running with %s, want the %s it was already pacing by",
			now.PollInterval, cfg.PollInterval)
	}
}
