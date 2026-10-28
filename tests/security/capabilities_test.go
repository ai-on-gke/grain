package security

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/capability/packagepublish"
	"github.com/ai-on-gke/grain/pkg/capability/registrypush"
	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/model"
)

// A capability is authority a run holds: a credential placed in its
// sandbox, a tool on its roster. Two things decide whether a run gets
// one, and each is a boundary here (a run's team decides nothing since
// the single-tenant reset, stage 4):
//
//   - its parent: a proposal asking for a capability the task that
//     filed it does not hold is never queued without a person
//     (model.AutoSubmits, model.EscalatedCapabilities);
//   - its event, for CI: a scheduled run is not a release and nobody
//     asked for it by hand, so it is handed no write-scoped publishing
//     credential and no release secret.

func resolution(t *testing.T, reg *model.CapabilityRegistry, task model.Task, capability string) model.Resolution {
	t.Helper()
	resolved, err := model.ResolveGrants(context.Background(), reg, model.CapabilityContext{Task: task, Now: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range resolved {
		if r.Grant.Capability == capability {
			return r.Resolution
		}
	}
	t.Fatalf("ResolveGrants said nothing about %q: %+v", capability, resolved)
	return model.Resolution{}
}

func TestAProposalCannotWidenItsParentsCapabilities(t *testing.T) {
	label := func(names ...string) []model.Grant {
		var gs []model.Grant
		for _, n := range names {
			gs = append(gs, model.Grant{Capability: n, Via: model.GrantByLabel})
		}
		return gs
	}
	parent := model.Task{ID: "parent", AutoMerge: true, Grants: label("grain-ci")}

	// Within what the parent holds: queued on the parent's say-so.
	same := model.Task{ID: "child", Grants: label("grain-ci")}
	if !model.AutoSubmits(same, parent) {
		t.Fatal("a proposal holding only its parent's capabilities was not auto-submitted; " +
			"the refusals below would prove nothing")
	}

	// Beyond it: filed, but only a person can queue it, and the
	// escalation is named so that approving it is visibly approving
	// the grant.
	wider := model.Task{ID: "child", Grants: label("grain-ci", "self-debug")}
	if model.AutoSubmits(wider, parent) {
		t.Error("a proposal asking for a capability its parent lacks was queued without a person")
	}
	if got := model.EscalatedCapabilities(wider, parent); len(got) != 1 || got[0] != "self-debug" {
		t.Errorf("EscalatedCapabilities = %v, want [self-debug]", got)
	}
	if note := model.CapabilityEscalationNote(parent.ID, model.EscalatedCapabilities(wider, parent)); !strings.Contains(note, "self-debug") {
		t.Errorf("the escalation note does not name the capability: %q", note)
	}

}

// The publishing credentials write to a repository's packages. They are
// CI's alone -- a task holding one is refused it -- and in CI they go to
// a release or a run somebody asked for by hand, never to a cron:
// neither a caller's own schedule run nor the one grain fires itself.
// cmd/grain's resolveCICapabilities asks exactly these two questions of
// each provider (SupportsCIEvent, then SupportsTrustedSchedule).
func TestScheduledCIGetsNoPublishingCredential(t *testing.T) {
	providers := []model.CapabilityProvider{
		packagepublish.New(packagepublish.Config{}),
		registrypush.New(),
	}
	for _, p := range providers {
		name := p.Spec().Name
		if !p.Spec().CIOnly {
			t.Errorf("%s is not CI-only: a task could hold a publishing credential", name)
		}
		events, ok := p.(model.CIEventCapability)
		if !ok {
			t.Errorf("%s has no CI event gate, so every event -- schedule included -- would be handed it", name)
			continue
		}
		if events.SupportsCIEvent(string(ci.EventSchedule)) {
			t.Errorf("%s is handed to a scheduled run", name)
		}
		for _, untrusted := range []ci.Event{ci.EventPush, ci.EventPullRequest} {
			if events.SupportsCIEvent(string(untrusted)) {
				t.Errorf("%s is handed to a %s run", name, untrusted)
			}
		}
		if trusted, ok := p.(model.TrustedScheduleCapability); ok && trusted.SupportsTrustedSchedule() {
			t.Errorf("%s opts in to grain's own trusted schedule runs", name)
		}

		reg := model.NewCapabilityRegistry(p)
		task := model.Task{ID: "t", Grants: []model.Grant{{Capability: name, Via: model.GrantByLabel}}}
		if r := resolution(t, reg, task, name); !r.Refused {
			t.Errorf("a task holding %s was not refused it", name)
		}
	}
}

// Release secrets are a repository's own live credentials (an npm token,
// a signing key). A scheduled run -- trusted or not -- does not so much
// as read which ones the repository has, and no job it runs is handed
// one, even a job that declares it needs them.
func TestScheduledCIGetsNoReleaseSecret(t *testing.T) {
	workflow, err := ci.Parse(".grain/workflows/nightly.yml", []byte(`on:
  schedule:
    - cron: "0 3 * * *"
  workflow_dispatch:
jobs:
  publish:
    cache: false
    secrets: [NPM_TOKEN]
    steps:
      - run: "true"
  plain:
    cache: false
    steps:
      - run: "true"
`))
	if err != nil {
		t.Fatal(err)
	}

	for _, trusted := range []bool{false, true} {
		sandboxes := &recordingSandboxes{}
		var mu sync.Mutex
		asked := 0
		finished := make(chan string, 8)
		service := &ci.Service{
			Source: staticWorkflows{workflows: []ci.Workflow{workflow}},
			Engine: &ci.Engine{Sandboxes: sandboxes},
			ReleaseSecrets: func(context.Context, string) ([]string, error) {
				mu.Lock()
				asked++
				mu.Unlock()
				return []string{"NPM_TOKEN"}, nil
			},
			Log: func(format string, args ...any) {
				if strings.HasPrefix(format, "ci: run %s: %s@%s: ") {
					select {
					case finished <- args[0].(string):
					default:
					}
				}
			},
		}
		run, err := service.Start(context.Background(), ci.Request{
			Repo: "acme/widgets", Ref: "main", Event: ci.EventSchedule, TrustedSchedule: trusted,
		})
		if err != nil {
			t.Fatalf("starting a schedule run (trusted=%v): %v", trusted, err)
		}
		waitForCIRun(t, finished, run.ID)

		mu.Lock()
		if asked != 0 {
			t.Errorf("a schedule run (trusted=%v) read the repository's release secrets", trusted)
		}
		mu.Unlock()
		for _, req := range sandboxes.requests() {
			if len(req.ReleaseSecrets) != 0 {
				t.Errorf("a schedule run (trusted=%v) handed a job release secrets %v", trusted, req.ReleaseSecrets)
			}
		}
		if len(sandboxes.requests()) == 0 {
			t.Errorf("a schedule run (trusted=%v) acquired no sandbox at all, so this proves nothing", trusted)
		}
	}
}

func waitForCIRun(t *testing.T, finished <-chan string, id string) {
	t.Helper()
	timeout := time.After(2 * time.Minute)
	for {
		select {
		case got := <-finished:
			if got == id {
				return
			}
		case <-timeout:
			t.Fatalf("CI run %s did not finish", id)
		}
	}
}

type staticWorkflows struct{ workflows []ci.Workflow }

func (s staticWorkflows) Workflows(context.Context, string, string) ([]ci.Workflow, string, error) {
	return s.workflows, "0123456789abcdef0123456789abcdef01234567", nil
}

// recordingSandboxes hands out sandboxes in which every command succeeds,
// and remembers what each job asked for.
type recordingSandboxes struct {
	mu   sync.Mutex
	seen []ci.SandboxRequest
}

func (r *recordingSandboxes) Acquire(_ context.Context, req ci.SandboxRequest) (ci.Sandbox, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seen = append(r.seen, req)
	return okSandbox{}, nil
}

func (r *recordingSandboxes) requests() []ci.SandboxRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]ci.SandboxRequest(nil), r.seen...)
}

type okSandbox struct{}

func (okSandbox) Name() string { return "security-ci" }
func (okSandbox) Exec(context.Context, ci.Command) (ci.CommandResult, error) {
	return ci.CommandResult{}, nil
}
func (okSandbox) Release(context.Context) error { return nil }
