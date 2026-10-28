package antigravity

import (
	"context"
	"slices"
	"testing"

	"github.com/bwsalmon/grain/pkg/agent"
)

// The three places this package starts agy, each asked the same question:
// is the updater switched off? A binary that rewrites itself makes the
// Dockerfile's pin and docs/agy-surface.md's captures claims about a
// release the deployment has stopped running, and any one exec is enough
// to do it -- `agy models` did, on 1.1.27, even while refusing the
// listing for want of a credential (selfupdate.go).
//
// live_selfupdate_stub_test.go is the other half: these tests say grain
// asks, that one says agy listens.
func TestEveryAgyGrainStartsHasTheUpdaterDisabled(t *testing.T) {
	t.Run("a dispatch", func(t *testing.T) {
		r := &recordingRunner{stdout: okStream()}
		f := newFramework(r, "/usr/local/bin/grain", WithAPIKey("AIza-not-a-real-key"))
		if _, err := f.Run(context.Background(), agent.RunConfig{Prompt: "go", SandboxRoot: t.TempDir()}); err != nil {
			t.Fatalf("Run: %v", err)
		}
		wantDisabledUpdater(t, r.env)
	})

	t.Run("an interactive session", func(t *testing.T) {
		cmd := terminalCommand(t, agent.RunConfig{SandboxRoot: t.TempDir()}, WithAPIKey("AIza-not-a-real-key"))
		wantDisabledUpdater(t, cmd.Env)
	})

	t.Run("a model listing", func(t *testing.T) {
		p := newFakeProbe()
		if _, err := catalog(context.Background(), agent.LocalHost{}, p, "secret-key"); err != nil {
			t.Fatalf("catalog: %v", err)
		}
		// Both execs a listing makes, not just the one that was measured
		// to update: which subcommands agy updates from is the vendor's
		// to change, and this package has no way of hearing about it.
		for _, args := range []string{"models", "--help"} {
			wantDisabledUpdater(t, p.envs[args])
		}
	})
}

// wantDisabledUpdater insists on the exact entry rather than on the
// variable's presence: with AGY_CLI_DISABLE_AUTO_UPDATE=1 the binary
// replaced itself exactly as it does with the variable unset, so agy
// compares the value against a string rather than parsing it as a
// boolean, and "the obvious tidy-up" is a silent regression
// (docs/hand-runs/2026-09-17-agy-self-update.md).
func wantDisabledUpdater(t *testing.T, env []string) {
	t.Helper()
	if !slices.Contains(env, "AGY_CLI_DISABLE_AUTO_UPDATE=true") {
		t.Errorf("env = %v, want AGY_CLI_DISABLE_AUTO_UPDATE=true: without it agy replaces its own "+
			"binary mid-exec and this deployment stops running the release its image pins", env)
	}
}
