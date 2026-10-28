package orchestrator_test

// The settings a cycle itself is the consumer of, changed while the
// daemon runs. Deps.MaxWorkers has its own test alongside the rest of
// the concurrency ones (concurrent_dispatch_test.go); this is its
// sibling, Config.MaxAgentTurns, which reaches the agent as
// agent.RunConfig.MaxTurns and so is observable at the far end of a real
// dispatch.

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/agent"
	"github.com/bwsalmon/grain/pkg/mcp"
	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/orchestrator"
)

// turnsFramework reports the turn cap each dispatch handed it, so a test
// can see which value actually reached the agent rather than which one
// the caller's own Deps still says.
type turnsFramework struct{ turns chan int }

func (f *turnsFramework) Run(_ context.Context, cfg agent.RunConfig) (*agent.Result, error) {
	f.turns <- cfg.MaxTurns
	return toolResult(agent.ToolCall{
		Name:      "comment_on_issue",
		Arguments: map[string]any{"comment": "done"},
	}), nil
}

// A change to max-agent-turns made through the store -- `grain
// settings`, or the UI's Settings page -- takes effect on the next
// dispatch, with no restart and no change to the Deps a long-lived
// daemon process already built, exactly as max-workers does.
func TestACycleAdoptsAMaxAgentTurnsChangeFromTheStoreWithoutRestart(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	filedTask(t, ctx, store, "t1", repo)
	_, client := newSim(t, "acme", "widgets", "main")

	fw := &turnsFramework{turns: make(chan int, 4)}
	deps := orchestrator.Deps{
		Store:     store,
		Client:    client,
		Sandboxes: orchestrator.NewHostSandboxes(t.TempDir()),
		Framework: orchestrator.StaticFramework(fw),
		// What this process started with -- the flag that seeded the row,
		// or the value stored the last time it was restarted.
		Config:     orchestrator.Config{MaxAgentTurns: 10},
		MaxWorkers: 1,
	}

	if err := store.PutConfig(ctx, model.Config{MaxWorkers: 1, MaxAgentTurns: 40}); err != nil {
		t.Fatalf("PutConfig: %v", err)
	}

	// With no InFlight to park it in, the cycle waits for the run it
	// starts, so the cap the agent was handed is on the channel by the
	// time this returns.
	if err := orchestrator.RunCycle(ctx, deps, baseTime); err != nil {
		t.Fatalf("RunCycle: %v", err)
	}

	select {
	case got := <-fw.turns:
		if got != 40 {
			t.Fatalf("the agent was given a %d-turn budget, want the stored 40 rather than the 10 this "+
				"process started with", got)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("no agent ran")
	}
}

// promptFramework is turnsFramework's counterpart for the prompt: it
// reports the prompt each dispatch was built with, which is where the
// deployment-wide prompt extension ends up (grain/task-114).
type promptFramework struct{ prompts chan string }

func (f *promptFramework) Run(_ context.Context, cfg agent.RunConfig) (*agent.Result, error) {
	f.prompts <- cfg.Prompt
	return toolResult(agent.ToolCall{
		Name:      "comment_on_issue",
		Arguments: map[string]any{"comment": "done"},
	}), nil
}

// The same "no restart" property for the deployment-wide prompt
// extension: an operator who changes what every run is told in Settings
// is changing the next run, not the next process. It is worth its own
// test rather than trusting the field's neighbour above, because this
// one is only live if RunCycle actually refreshes it -- a Config field
// read straight from whatever the daemon started with would pass every
// other test this package has.
func TestACycleAdoptsAPromptExtensionChangeFromTheStoreWithoutRestart(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	filedTask(t, ctx, store, "t1", repo)
	_, client := newSim(t, "acme", "widgets", "main")

	fw := &promptFramework{prompts: make(chan string, 4)}
	deps := orchestrator.Deps{
		Store:     store,
		Client:    client,
		Sandboxes: orchestrator.NewHostSandboxes(t.TempDir()),
		Framework: orchestrator.StaticFramework(fw),
		// What this process started with -- in practice nothing at all,
		// since no daemon flag seeds this one.
		Config:     orchestrator.Config{},
		MaxWorkers: 1,
	}

	const text = "Run `make lint` before you push."
	if err := store.PutConfig(ctx, model.Config{MaxWorkers: 1, PromptExtension: text}); err != nil {
		t.Fatalf("PutConfig: %v", err)
	}

	if err := orchestrator.RunCycle(ctx, deps, baseTime); err != nil {
		t.Fatalf("RunCycle: %v", err)
	}

	select {
	case got := <-fw.prompts:
		if !strings.Contains(got, text) {
			t.Fatalf("the agent's prompt does not carry the stored standing instructions: %q", got)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("no agent ran")
	}
}

// identitySandboxes reports the git identity each dispatch configured its
// sandbox with -- the last step of the setting's journey, where a
// deployment's chosen committer either reaches the .gitconfig every
// commit is authored against or does not.
//
// Root is forwarded explicitly: an embedded interface value carries no
// methods outside its own method set, and a sandbox that stopped
// answering "which directory are you" would be a different dispatch than
// the one being measured (SandboxPlacer's own doc comment on the same
// trap).
type identitySandboxes struct {
	inner orchestrator.Sandboxes
	seen  chan mcp.GitIdentity
}

func (s identitySandboxes) Acquire(ctx context.Context, name string, shape orchestrator.Shape) (orchestrator.Sandbox, error) {
	sb, err := s.inner.Acquire(ctx, name, shape)
	if err != nil {
		return nil, err
	}
	return identitySandbox{Sandbox: sb, seen: s.seen}, nil
}

type identitySandbox struct {
	orchestrator.Sandbox
	seen chan mcp.GitIdentity
}

func (s identitySandbox) ConfigureGitCredentials(ctx context.Context, remoteURL, token string, identity mcp.GitIdentity) error {
	s.seen <- identity
	return s.Sandbox.ConfigureGitCredentials(ctx, remoteURL, token, identity)
}

func (s identitySandbox) Root() (string, error) {
	rooted, ok := s.Sandbox.(interface{ Root() (string, error) })
	if !ok {
		return "", errors.New("not a rooted sandbox")
	}
	return rooted.Root()
}

// The same "no restart" property for the git identity every agent commits
// under (grain/task-14): an operator who points a deployment's commits at
// their own bot account is changing the next run, not the next process.
// Worth its own test for the reason the prompt extension's is -- a Config
// field read straight from whatever the daemon started with would pass
// every other test here.
func TestACycleAdoptsAnAgentGitIdentityChangeFromTheStoreWithoutRestart(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	filedTask(t, ctx, store, "t1", repo)
	_, client := newSim(t, "acme", "widgets", "main")

	fw := &promptFramework{prompts: make(chan string, 4)}
	seen := make(chan mcp.GitIdentity, 4)
	deps := orchestrator.Deps{
		Store:     store,
		Client:    client,
		Sandboxes: identitySandboxes{inner: orchestrator.NewHostSandboxes(t.TempDir()), seen: seen},
		Framework: orchestrator.StaticFramework(fw),
		// Configuring git credentials at all is what a minted token gates
		// (RunDispatch), so a deployment with no proxy never reaches the
		// step this test measures.
		MintSandboxToken: func(string) (string, error) { return "tok", nil },
		// An absolute base for the same reason
		// TestRunCycleRevokesASandboxToken's is: runOne points the
		// sandbox's git at GitRemoteBase+"/placeholder/placeholder.git"
		// as soon as a token is minted, and a credential-store line needs
		// a real URL. Nothing here says what identity to use -- that is
		// the point, and it comes from the row below.
		Config:     orchestrator.Config{GitRemoteBase: "http://proxy.example"},
		MaxWorkers: 1,
	}

	want := mcp.GitIdentity{Name: "acme bot", Email: "bot@acme.example"}
	if err := store.PutConfig(ctx, model.Config{MaxWorkers: 1, AgentGitName: want.Name, AgentGitEmail: want.Email}); err != nil {
		t.Fatalf("PutConfig: %v", err)
	}

	// The run itself fails, and that is beside the point: proxy.example
	// does not resolve, so the checkout that follows cannot succeed. The
	// identity is written before it, which is the step being measured --
	// the same shape TestRunCycleRevokesASandboxToken uses for the token.
	if err := orchestrator.RunCycle(ctx, deps, baseTime); err == nil {
		t.Fatal("expected the unreachable git remote to fail this run")
	}

	select {
	case got := <-seen:
		if got != want {
			t.Fatalf("the sandbox was given identity %+v, want the stored %+v", got, want)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("no sandbox had its git credentials configured")
	}
}

// A run commits as the account its git credential acts as -- what
// Deps.CredentialGitIdentity answers for the task and its target -- and
// not as grain's own `grain agent <grain-agent@localhost>`, an address no
// GitHub account owns. Half by half: a half the credential leaves empty
// is filled from the deployment's legacy commit author (Config.AgentGitName).
func TestACycleCommitsAsTheCredentialsOwnIdentity(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	filedTask(t, ctx, store, "t1", repo)
	if err := store.PutConfig(ctx, model.Config{MaxWorkers: 1, AgentGitName: "acme bot"}); err != nil {
		t.Fatalf("PutConfig: %v", err)
	}
	_, client := newSim(t, "acme", "widgets", "main")

	fw := &promptFramework{prompts: make(chan string, 4)}
	seen := make(chan mcp.GitIdentity, 4)
	asked := make(chan string, 4)
	deps := orchestrator.Deps{
		Store:            store,
		Client:           client,
		Sandboxes:        identitySandboxes{inner: orchestrator.NewHostSandboxes(t.TempDir()), seen: seen},
		Framework:        orchestrator.StaticFramework(fw),
		MintSandboxToken: func(string) (string, error) { return "tok", nil },
		CredentialGitIdentity: func(_ context.Context, taskID string, target *model.RepoRef) (mcp.GitIdentity, bool) {
			if target == nil {
				t.Errorf("asked for task %s with no target", taskID)
				return mcp.GitIdentity{}, false
			}
			asked <- taskID + " " + target.Owner + "/" + target.Name
			return mcp.GitIdentity{Email: "12345+operator@users.noreply.github.com"}, true
		},
		Config:     orchestrator.Config{GitRemoteBase: "http://proxy.example"},
		MaxWorkers: 1,
	}

	_ = orchestrator.RunCycle(ctx, deps, baseTime)

	want := mcp.GitIdentity{Name: "acme bot", Email: "12345+operator@users.noreply.github.com"}
	select {
	case got := <-seen:
		if got != want {
			t.Fatalf("the sandbox was given identity %+v, want the team's legacy name with the credential's address, %+v", got, want)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("no sandbox had its git credentials configured")
	}
	select {
	case got := <-asked:
		if got != "t1 acme/widgets" {
			t.Fatalf("the credential identity was asked for %q, want the task and its target", got)
		}
	default:
		t.Fatal("the credential identity was never asked for")
	}
}

// When Config.AgentGitName or Config.AgentGitEmail is explicitly set in
// grain_config, that explicit setting overrides the corresponding half
// derived from the credential (for instance, overriding a PAT's
// synthesized noreply address with the operator's address).
func TestACycleCommitsAsExplicitConfigIdentityOverTheCredential(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	filedTask(t, ctx, store, "t1", repo)
	if err := store.PutConfig(ctx, model.Config{MaxWorkers: 1, AgentGitEmail: "bsalmon@google.com"}); err != nil {
		t.Fatalf("PutConfig: %v", err)
	}
	credIdentity := mcp.GitIdentity{Name: "Olive Operator", Email: "12345+operator@users.noreply.github.com"}
	want := mcp.GitIdentity{Name: "Olive Operator", Email: "bsalmon@google.com"}
	_, client := newSim(t, "acme", "widgets", "main")

	fw := &promptFramework{prompts: make(chan string, 4)}
	seen := make(chan mcp.GitIdentity, 4)
	deps := orchestrator.Deps{
		Store:            store,
		Client:           client,
		Sandboxes:        identitySandboxes{inner: orchestrator.NewHostSandboxes(t.TempDir()), seen: seen},
		Framework:        orchestrator.StaticFramework(fw),
		MintSandboxToken: func(string) (string, error) { return "tok", nil },
		CredentialGitIdentity: func(_ context.Context, _ string, _ *model.RepoRef) (mcp.GitIdentity, bool) {
			return credIdentity, true
		},
		Config:     orchestrator.Config{GitRemoteBase: "http://proxy.example"},
		MaxWorkers: 1,
	}

	_ = orchestrator.RunCycle(ctx, deps, baseTime)

	select {
	case got := <-seen:
		if got != want {
			t.Fatalf("the sandbox was given identity %+v, want %+v", got, want)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("no sandbox had its git credentials configured")
	}
}
