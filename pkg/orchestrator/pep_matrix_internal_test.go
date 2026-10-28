package orchestrator

// The local policy matrix: grain is the Policy Enforcement Point, and
// every claim a task carries -- the repositories it targets, writes and
// reads, the capabilities it is granted, its network, who asked for it
// and who approved it, and what it was derived from -- is judged
// against the workstation owner's grain_policy.yml at three places: the
// dispatch hold (heldByPolicy), the grant admitter (grantAdmitter)
// before it mints a scarce grant, and runOne's check before anything is
// built or cloned. Each case below is asserted at all three, and a
// refused one must never have a grant minted or reach
// Sandboxes.Acquire -- which is what every clone, token and agent of a
// run comes after -- and must leave an entry in the local audit log
// (Deps.PolicyAudit) at each place that refused it, naming the task,
// its root requester and whether it is hub-derived.
//
// The four gaps task 4190's review of PR #3959 found each have a case
// here, named by the review's finding: an empty Creator on a proposal
// (GSA1-02), deny: [repo-write:*] (GSA1-03), the hold and the admitter
// seeing different grants (GSA1-08), and capability-implied reads
// (GSA1-04).

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/dispatch"
	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/policy"
)

// pepSandboxes counts the sandboxes a run asks for and builds none.
type pepSandboxes struct {
	acquired atomic.Int32
}

var errPEPNoSandbox = errors.New("pep matrix: no sandbox is built in this test")

func (s *pepSandboxes) Acquire(context.Context, string, Shape) (Sandbox, error) {
	s.acquired.Add(1)
	return nil, errPEPNoSandbox
}

// pepHostSandboxes is pepSandboxes saying it isolates nothing from the
// host (-host-sandboxes).
type pepHostSandboxes struct{ *pepSandboxes }

func (pepHostSandboxes) Unisolated() bool { return true }

const (
	pepOwner    = "owner@example.com"
	pepStranger = "friend@example.com"
	pepLead     = "lead@example.com"
)

// pepPolicies are the policy files the matrix is run against.
var pepPolicies = map[string]string{
	"repos": "repos: [acme/widgets, acme/lib-*, ghe.example.com/corp/*]\n" +
		"local_requesters: [owner@example.com]\n",
	"capabilities": "repos: [acme/*]\nlocal_requesters: [owner@example.com]\n" +
		"capabilities:\n  allow: [gcp-key, repo-write:acme/*, grain-ci]\n  deny: [gcp-key:prod*]\n",
	"deny-repo-write": "repos: [acme/*]\nlocal_requesters: [owner@example.com]\n" +
		"capabilities:\n  deny: [repo-write:*]\n",
	"no-grain-ci": "repos: [acme/*]\nlocal_requesters: [owner@example.com]\n" +
		"capabilities:\n  allow: [gcp-key]\n",
	"network-none":  "repos: [acme/*]\nlocal_requesters: [owner@example.com]\nnetwork: [none]\n",
	"approvers":     "repos: [acme/*]\nlocal_requesters: [owner@example.com]\napprovers: [lead@example.com]\n",
	"unapproved-ok": "repos: [acme/*]\nlocal_requesters: [owner@example.com]\nallow_unapproved_external: true\n",
	"host-external-ok": "repos: [acme/*]\nlocal_requesters: [owner@example.com]\n" +
		"allow_external_on_host_sandboxes: true\n",
	"empty": "",
}

func pepRepo(s string) *model.RepoRef {
	r, err := model.ParseRepo(s)
	if err != nil {
		panic(err)
	}
	return &r
}

func pepApproved(by string) *model.Attribution {
	return &model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: by}}
}

func pepGrants(caps ...string) []model.Grant {
	var gs []model.Grant
	for _, c := range caps {
		gs = append(gs, model.Grant{Capability: c})
	}
	return gs
}

type pepCase struct {
	name   string
	policy string
	// chain are tasks stored beside task: what it was derived from.
	chain []model.Task
	task  model.Task
	// network is the deployment's own network access, "" for none set.
	network model.NetworkAccess
	// host runs the case on a daemon whose sandboxes are host
	// directories.
	host bool
	// refuse is a fragment of the refusal, "" when the task may run.
	refuse string
}

func pepCases() []pepCase {
	own := func(t model.Task) model.Task {
		if t.Target == nil {
			t.Target = &widgets
		}
		if t.Creator == "" {
			t.Creator = pepOwner
		}
		return t
	}
	stranger := func(id string, tags ...string) model.Task {
		return model.Task{ID: id, Title: "root", Target: &widgets, Creator: pepStranger, Approval: pepApproved(pepLead), Tags: tags}
	}
	ownRoot := model.Task{ID: "root", Title: "root", Target: &widgets, Creator: pepOwner}
	proposedBy := func(id string) []model.Link { return []model.Link{{Kind: model.LinkProposedBy, Target: id}} }

	return []pepCase{
		// Repositories: target, writes, reads, patterns and hosts.
		{name: "target attached", policy: "repos", task: own(model.Task{})},
		{name: "target outside", policy: "repos", task: own(model.Task{Target: pepRepo("acme/gadgets")}), refuse: "repository acme/gadgets"},
		{name: "target by pattern", policy: "repos", task: own(model.Task{Target: pepRepo("acme/lib-core")})},
		{name: "target pattern does not cross owners", policy: "repos", task: own(model.Task{Target: pepRepo("evil/lib-core")}), refuse: "repository evil/lib-core"},
		{name: "target compared without case", policy: "repos", task: own(model.Task{Target: pepRepo("ACME/Widgets")})},
		{name: "target on another host by pattern", policy: "repos", task: own(model.Task{Target: pepRepo("ghe.example.com/corp/app")})},
		{name: "host entry does not cover github.com", policy: "repos", task: own(model.Task{Target: pepRepo("corp/app")}), refuse: "corp/app"},
		{name: "github.com entry does not cover another host", policy: "repos", task: own(model.Task{Target: pepRepo("ghe.example.com/acme/widgets")}), refuse: "acme/widgets"},
		{name: "unknown host", policy: "repos", task: own(model.Task{Target: pepRepo("git.evil.example/corp/app")}), refuse: "corp/app"},
		{name: "writes attached", policy: "repos", task: own(model.Task{Writes: []model.WriteTarget{{Repo: *pepRepo("acme/lib-a"), Base: "main"}}})},
		{name: "writes outside", policy: "repos", task: own(model.Task{Writes: []model.WriteTarget{{Repo: *pepRepo("evil/sink"), Base: "main"}}}), refuse: "repository evil/sink"},
		{name: "reads outside", policy: "repos", task: own(model.Task{Reads: []model.RepoRef{*pepRepo("evil/secrets")}}), refuse: "repository evil/secrets"},
		{name: "reads attached", policy: "repos", task: own(model.Task{Reads: []model.RepoRef{*pepRepo("acme/lib-x")}})},
		{name: "empty policy refuses every repository", policy: "empty", task: own(model.Task{}), refuse: "repository acme/widgets"},

		// Capability-implied repositories (GSA1-04): a grant reaching a
		// repository the task never names answers to repos: too.
		// self-debug serves grain-src/ from the deployment's baked-in
		// source and implies no repository at all (task 4337), so it is
		// admitted under a repos: list that does not name bwsalmon/grain.
		{name: "GSA1-04 self-debug implies no repository (baked source, task 4337)", policy: "repos", task: own(model.Task{Grants: pepGrants(model.SelfDebugCapability)})},
		{name: "GSA1-04 repo-read of bwsalmon/grain outside", policy: "repos", task: own(model.Task{Grants: pepGrants("repo-read:bwsalmon/grain")}), refuse: "repository bwsalmon/grain"},
		{name: "repo-read grant outside", policy: "repos", task: own(model.Task{Grants: pepGrants("repo-read:evil/x")}), refuse: "repository evil/x"},
		{name: "repo-write grant outside", policy: "repos", task: own(model.Task{Grants: pepGrants("repo-write:evil/x")}), refuse: "repository evil/x"},
		{name: "repo-read grant attached", policy: "repos", task: own(model.Task{Grants: pepGrants("repo-read:acme/lib-y")})},

		// Capabilities: allow, deny and patterns.
		{name: "allowed capability", policy: "capabilities", task: own(model.Task{Grants: pepGrants("gcp-key")})},
		{name: "allowed family covers its parameters", policy: "capabilities", task: own(model.Task{Grants: pepGrants("gcp-key:team-a")})},
		{name: "deny pattern beats allow", policy: "capabilities", task: own(model.Task{Grants: pepGrants("gcp-key:prod-eu")}), refuse: `forbids capability "gcp-key:prod-eu"`},
		{name: "allowed parameter pattern", policy: "capabilities", task: own(model.Task{Grants: pepGrants("repo-write:acme/widgets")})},
		{name: "capability outside allow", policy: "capabilities", task: own(model.Task{Grants: pepGrants("console")}), refuse: `does not allow capability "console"`},
		{name: "one bad grant among good ones", policy: "capabilities", task: own(model.Task{Grants: pepGrants("gcp-key", "github-sandbox")}), refuse: "github-sandbox"},
		{name: "GSA1-03 deny repo-write:* matches a grant", policy: "deny-repo-write", task: own(model.Task{Grants: pepGrants("repo-write:acme/widgets")}), refuse: `forbids capability "repo-write:acme/widgets"`},
		{name: "GSA1-03 deny repo-write:* without regard to case", policy: "deny-repo-write", task: own(model.Task{Grants: pepGrants("REPO-WRITE:Acme/Widgets")}), refuse: "forbids capability"},
		{name: "deny repo-write:* leaves other grants", policy: "deny-repo-write", task: own(model.Task{Grants: pepGrants("repo-read:acme/lib")})},
		// GSA1-08: a grant the admitter adds by default is a grant the
		// policy judges, wherever it is seen.
		{name: "GSA1-08 default grant outside allow", policy: "no-grain-ci",
			task:   own(model.Task{Grants: []model.Grant{{Capability: "grain-ci", Via: model.GrantByDefault}}}),
			refuse: `does not allow capability "grain-ci"`},

		// Network.
		{name: "network asked for and allowed", policy: "network-none", task: own(model.Task{NetworkAccess: model.NetworkNone})},
		{name: "network asked for and refused", policy: "network-none", task: own(model.Task{NetworkAccess: model.NetworkOpen}), refuse: `network access "open"`},
		{name: "deployment default network refused", policy: "network-none", network: model.NetworkOpen, task: own(model.Task{}), refuse: `network access "open"`},
		{name: "deployment default network allowed", policy: "network-none", network: model.NetworkNone, task: own(model.Task{})},

		// Requesters and approvals.
		{name: "grain's own task", policy: "repos", task: model.Task{Target: &widgets}},
		{name: "local requester without case", policy: "repos", task: model.Task{Target: &widgets, Creator: "OWNER@example.com"}},
		{name: "external unapproved", policy: "repos", task: model.Task{Target: &widgets, Creator: pepStranger}, refuse: "no human approval"},
		{name: "external approved", policy: "repos", task: model.Task{Target: &widgets, Creator: pepStranger, Approval: pepApproved(pepLead)}},
		{name: "external approved by itself", policy: "repos", task: model.Task{Target: &widgets, Creator: pepStranger, Approval: pepApproved("FRIEND@example.com")}, refuse: "approved only by its own requester"},
		{name: "external approved by automation", policy: "repos",
			task:   model.Task{Target: &widgets, Creator: pepStranger, Approval: &model.Attribution{Actor: model.Principal{Kind: model.PrincipalAutomation, ID: "grain"}}},
			refuse: "no human approval"},
		{name: "external approved by somebody not an approver", policy: "approvers", task: model.Task{Target: &widgets, Creator: pepStranger, Approval: pepApproved(pepOwner)}, refuse: "not one of this machine's approvers"},
		{name: "external approved by an approver", policy: "approvers", task: model.Task{Target: &widgets, Creator: pepStranger, Approval: pepApproved(pepLead)}},
		{name: "external unapproved where allowed", policy: "unapproved-ok", task: model.Task{Target: &widgets, Creator: pepStranger}},
		{name: "allow_unapproved_external still checks repos", policy: "unapproved-ok", task: model.Task{Target: pepRepo("evil/x"), Creator: pepStranger}, refuse: "repository evil/x"},

		// Work grain files itself inherits its root requester.
		{name: "GSA1-02 proposal naming nobody from an external root", policy: "repos",
			chain: []model.Task{stranger("root")}, task: model.Task{Target: &widgets, Links: proposedBy("root")}, refuse: "no human approval"},
		{name: "proposal naming nobody from the owner's root", policy: "repos",
			chain: []model.Task{ownRoot}, task: model.Task{Target: &widgets, Links: proposedBy("root")}},
		{name: "proposal of a proposal of an external root", policy: "repos",
			chain: []model.Task{stranger("root"), {ID: "mid", Title: "mid", Target: &widgets, Links: proposedBy("root")}},
			task:  model.Task{Target: &widgets, Links: proposedBy("mid")}, refuse: "no human approval"},
		{name: "port of an external task", policy: "repos",
			chain: []model.Task{stranger("root")}, task: model.Task{Target: &widgets, Links: []model.Link{{Kind: model.LinkPortOf, Target: "root"}}}, refuse: "no human approval"},
		{name: "repair of an external task", policy: "repos",
			chain: []model.Task{stranger("root")}, task: model.Task{Target: &widgets, WorkOrigin: model.WorkOrigin("task:root")}, refuse: "no human approval"},
		{name: "proposal from a task that is not stored", policy: "repos", task: model.Task{Target: &widgets, Links: proposedBy("gone")}, refuse: "no human approval"},
		{name: "proposals that derive from each other", policy: "repos",
			chain: []model.Task{{ID: "loop", Title: "loop", Target: &widgets, Links: proposedBy("task")}},
			task:  model.Task{Target: &widgets, Links: proposedBy("loop")}, refuse: "no human approval"},
		{name: "derived task approved afresh", policy: "repos",
			chain: []model.Task{stranger("root")}, task: model.Task{Target: &widgets, Links: proposedBy("root"), Approval: pepApproved(pepLead)}},

		// Unisolated host sandboxes run only the owner's own.
		{name: "host sandboxes run the owner's task", policy: "repos", host: true, task: own(model.Task{})},
		{name: "host sandboxes refuse an approved external task", policy: "repos", host: true,
			task: model.Task{Target: &widgets, Creator: pepStranger, Approval: pepApproved(pepLead)}, refuse: "unisolated host sandboxes"},
		{name: "host sandboxes run external work where allowed", policy: "host-external-ok", host: true,
			task: model.Task{Target: &widgets, Creator: pepStranger, Approval: pepApproved(pepLead)}},
	}
}

// pepAudit is a real local audit log in a test's own directory, as the
// daemon opens it (policy.OpenAuditLog).
type pepAudit struct {
	t    *testing.T
	path string
}

func newPEPAudit(t *testing.T) (*pepAudit, func(policy.AuditEntry)) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "hub-audit.log")
	log, err := policy.OpenAuditLog(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = log.Close() })
	return &pepAudit{t: t, path: path}, log.Write
}

func (a *pepAudit) entries() []policy.AuditEntry {
	a.t.Helper()
	es, err := policy.ReadAuditLog(a.path)
	if err != nil {
		a.t.Fatalf("reading the audit log: %v", err)
	}
	return es
}

// pepWantRequester is who a case's task is requested by, worked out from
// the case itself rather than by rootRequester: the first Creator from
// the task up its chain (stored root first).
func pepWantRequester(c pepCase) string {
	name := c.task.Creator
	for i := len(c.chain) - 1; i >= 0; i-- {
		if name == "" {
			name = c.chain[i].Creator
		}
	}
	return name
}

// checkPEPEntry fails unless e is the local-policy refusal of task at
// stage, carrying the case's requester and reason.
func checkPEPEntry(t *testing.T, e policy.AuditEntry, c pepCase, stage string) {
	t.Helper()
	requester := pepWantRequester(c)
	if e.Message != policy.AuditLocalPolicy || e.Outcome != policy.AuditRefused || e.Task != "task" || e.Stage != stage ||
		!strings.Contains(e.Reason, c.refuse) || e.Requester != requester || e.Time.IsZero() {
		t.Errorf("audit entry = %+v, want a %s refusal of task by %q naming %q", e, stage, requester, c.refuse)
	}
}

func TestLocalPolicyMatrixAtTheDispatchHoldAndBeforeTheSandbox(t *testing.T) {
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	for _, c := range pepCases() {
		t.Run(c.name, func(t *testing.T) {
			src, ok := pepPolicies[c.policy]
			if !ok {
				t.Fatalf("no policy %q", c.policy)
			}
			p, err := policy.Parse([]byte(src))
			if err != nil {
				t.Fatalf("parsing policy %q: %v", c.policy, err)
			}
			store, ctx := openTreeStore(t)
			if c.network != "" {
				cfg, err := store.GetConfig(ctx)
				if err != nil {
					t.Fatal(err)
				}
				if cfg == nil {
					cfg = &model.Config{}
				}
				cfg.NetworkAccess = c.network
				if err := store.PutConfig(ctx, *cfg); err != nil {
					t.Fatal(err)
				}
			}
			for _, parent := range c.chain {
				if err := store.PutTask(ctx, parent); err != nil {
					t.Fatal(err)
				}
			}
			task := c.task
			task.ID, task.Title = "task", c.name
			if err := store.PutTask(ctx, task); err != nil {
				t.Fatal(err)
			}

			counter := &pepSandboxes{}
			var sandboxes Sandboxes = counter
			if c.host {
				sandboxes = pepHostSandboxes{counter}
			}
			audit, write := newPEPAudit(t)
			deps := Deps{Store: store, Policy: p, Sandboxes: sandboxes, PolicyAudit: write}
			refused := c.refuse != ""

			// The dispatch hold, and what it says on the task.
			if held := heldByPolicy(ctx, deps)(task.ID); held != refused {
				t.Errorf("dispatch hold: held = %v, want %v", held, refused)
			}
			wait, err := store.GrantWaitOf(ctx, task.ID)
			if err != nil {
				t.Fatal(err)
			}
			if refused {
				if wait == nil || wait.Capability != model.PolicyWaitCapability || !strings.Contains(wait.Resource, c.refuse) {
					t.Errorf("dispatch hold: wait = %+v, want a local-policy wait naming %q", wait, c.refuse)
				}
			} else if wait != nil {
				t.Errorf("dispatch hold: an allowed task carries a wait: %+v", wait)
			}

			// The hold's refusal is written down once, not once a cycle.
			heldByPolicy(ctx, deps)(task.ID)
			entries := audit.entries()
			if !refused {
				if len(entries) != 0 {
					t.Errorf("an allowed task was audited as refused: %+v", entries)
				}
			} else if len(entries) != 1 {
				t.Errorf("audit log after two holds: %d entries, want 1: %+v", len(entries), entries)
			} else {
				checkPEPEntry(t, entries[0], c, policyStageHold)
			}

			// The admitter, with every grant the task carries scarce: a
			// refused task mints nothing and keeps the hold's reason.
			var minters []model.CapabilityProvider
			for _, grant := range task.Grants {
				minters = append(minters, &mintCountingCapability{name: grant.Capability})
			}
			admitDeps := deps
			admitDeps.Config.Capabilities = model.NewCapabilityRegistry(minters...)
			admission := admitOnce(t, ctx, admitDeps, task.ID)
			if admission.Release != nil {
				admission.Release()
			}
			minted := 0
			for _, m := range minters {
				minted += m.(*mintCountingCapability).minted
			}
			if refused {
				if admission.Admitted || minted != 0 {
					t.Errorf("admitter: admitted = %v with %d grant(s) minted, want held with none", admission.Admitted, minted)
				}
				if wait, err := store.GrantWaitOf(ctx, task.ID); err != nil || wait == nil ||
					wait.Capability != model.PolicyWaitCapability || !strings.Contains(wait.Resource, c.refuse) {
					t.Errorf("admitter: wait = %+v, %v; want the local-policy wait naming %q kept", wait, err, c.refuse)
				}
			} else if !admission.Admitted || minted != len(minters) {
				t.Errorf("admitter: admitted = %v with %d of %d grant(s) minted, want admitted with all", admission.Admitted, minted, len(minters))
			}
			// Keeping the hold's wait, the admitter's refusal is no new
			// entry in the audit log.
			if es := audit.entries(); len(es) != len(entries) {
				t.Errorf("audit log after the admitter: %d entries, want %d: %+v", len(es), len(entries), es)
			}

			// The same decision from the check runOne makes.
			err = checkPolicy(ctx, deps, task, deploymentNetworkAccess(ctx, store))
			if refused != (err != nil) || (refused && !strings.Contains(err.Error(), c.refuse)) {
				t.Errorf("pre-sandbox check: err = %v, want refusal %q", err, c.refuse)
			}

			// And from runOne itself, before any sandbox -- and so any
			// clone, token or agent -- is asked for.
			runID := "task-1"
			if err := store.StartRun(ctx, model.Run{ID: runID, TaskID: task.ID, Sandbox: runID, Attempt: 1, StartedAt: now}, model.Limits{}); err != nil {
				t.Fatal(err)
			}
			err = runOne(ctx, deps, dispatch.Dispatch{TaskID: task.ID, RunID: runID, Attempt: 1}, nil, now)
			if refused {
				if err == nil || !strings.Contains(err.Error(), "local policy refuses") || !strings.Contains(err.Error(), c.refuse) {
					t.Errorf("runOne: err = %v, want the local policy's refusal naming %q", err, c.refuse)
				}
				if n := counter.acquired.Load(); n != 0 {
					t.Errorf("runOne asked for %d sandbox(es) for a task local policy refuses", n)
				}
				// Starting the run cleared the hold's wait, so runOne's
				// refusal is a transition of its own.
				if es := audit.entries(); len(es) != len(entries)+1 {
					t.Errorf("audit log after runOne: %d entries, want %d: %+v", len(es), len(entries)+1, es)
				} else {
					checkPEPEntry(t, es[len(es)-1], c, policyStageSandbox)
				}
			} else if n := counter.acquired.Load(); n != 1 || !errors.Is(err, errPEPNoSandbox) {
				t.Errorf("runOne: err = %v after %d sandbox request(s), want it to reach Acquire once", err, n)
			}
		})
	}
}

// An edit between the hold and runOne -- a capability granted, a target
// moved -- is caught by runOne's own read of the task: the hold's
// verdict is never carried into the run.
func TestLocalPolicyRechecksATaskEditedAfterTheHold(t *testing.T) {
	p, err := policy.Parse([]byte(pepPolicies["capabilities"]))
	if err != nil {
		t.Fatal(err)
	}
	store, ctx := openTreeStore(t)
	counter := &pepSandboxes{}
	audit, write := newPEPAudit(t)
	deps := Deps{Store: store, Policy: p, Sandboxes: counter, PolicyAudit: write}
	task := model.Task{ID: "task", Title: "edited", Target: &widgets, Creator: pepOwner}
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	if heldByPolicy(ctx, deps)(task.ID) {
		t.Fatal("the task was held before its edit")
	}
	if err := store.UpdateTask(ctx, task.ID, func(t *model.Task) error {
		t.Grants = append(t.Grants, model.Grant{Capability: "github-sandbox"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	if err := store.StartRun(ctx, model.Run{ID: "task-1", TaskID: task.ID, Sandbox: "task-1", Attempt: 1, StartedAt: now}, model.Limits{}); err != nil {
		t.Fatal(err)
	}
	err = runOne(ctx, deps, dispatch.Dispatch{TaskID: task.ID, RunID: "task-1", Attempt: 1}, nil, now)
	if err == nil || !strings.Contains(err.Error(), "github-sandbox") {
		t.Errorf("runOne err = %v, want the edited grant refused", err)
	}
	if n := counter.acquired.Load(); n != 0 {
		t.Errorf("runOne asked for %d sandbox(es) after the edit", n)
	}
	es := audit.entries()
	if len(es) != 1 {
		t.Fatalf("audit log: %+v, want runOne's refusal of the edited task", es)
	}
	checkPEPEntry(t, es[0], pepCase{task: task, refuse: "github-sandbox"}, policyStageSandbox)

	// The next cycle's hold refuses it for the same reason: the wait
	// runOne recorded does not change, and nothing more is written.
	if !heldByPolicy(ctx, deps)(task.ID) {
		t.Fatal("the edited task was not held")
	}
	if es := audit.entries(); len(es) != 1 {
		t.Errorf("audit log after the next hold: %+v, want only runOne's entry", es)
	}
}

// The audit is written on a transition of the task's local-policy wait:
// a new refusal, a different reason, or a refusal after the policy let
// the task through for a while -- and not again for each cycle the same
// refusal holds it. The admitter's refusals are written the same way.
func TestLocalPolicyAuditsEachChangeOfTheWait(t *testing.T) {
	store, ctx := openTreeStore(t)
	audit, write := newPEPAudit(t)
	parse := func(name string) *policy.Policy {
		p, err := policy.Parse([]byte(pepPolicies[name]))
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	task := model.Task{ID: "task", Title: "audited", Target: pepRepo("acme/gadgets"), Creator: pepStranger}
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	deps := Deps{Store: store, Policy: parse("repos"), Sandboxes: &pepSandboxes{}, PolicyAudit: write}
	hold := func(want bool) {
		t.Helper()
		if held := heldByPolicy(ctx, deps)(task.ID); held != want {
			t.Fatalf("held = %v, want %v", held, want)
		}
	}
	wantEntries := func(n int, reason string) {
		t.Helper()
		es := audit.entries()
		if len(es) != n {
			t.Fatalf("audit log: %d entries, want %d: %+v", len(es), n, es)
		}
		if reason != "" {
			e := es[n-1]
			if !strings.Contains(e.Reason, reason) || e.Requester != pepStranger || e.Repo != task.Target.String() {
				t.Errorf("latest entry = %+v, want a refusal of %s's task naming %q", e, pepStranger, reason)
			}
		}
	}

	hold(true)
	hold(true)
	hold(true)
	wantEntries(1, "repository acme/gadgets")

	// A different policy refuses it for another reason.
	deps.Policy = parse("approvers")
	hold(true)
	hold(true)
	wantEntries(2, "")

	// A policy that allows it clears the wait and writes nothing...
	deps.Policy = parse("host-external-ok")
	if err := store.UpdateTask(ctx, task.ID, func(t *model.Task) error {
		t.Target = &widgets
		t.Approval = pepApproved(pepLead)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	task.Target, task.Approval = &widgets, pepApproved(pepLead)
	hold(false)
	wantEntries(2, "")

	// ...so the next refusal, at the admitter, is a transition again.
	deps.Policy = parse("no-grain-ci")
	if err := store.UpdateTask(ctx, task.ID, func(t *model.Task) error {
		t.Grants = pepGrants("console")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	admit := grantAdmitter(ctx, deps, nil, time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC))
	for range 2 {
		adm, err := admit(ctx, dispatch.Candidate{TaskID: task.ID, RunID: "task-1", Attempt: 1})
		if err != nil || adm.Admitted {
			t.Fatalf("admission = %+v, %v; want the task refused", adm, err)
		}
	}
	wantEntries(3, `capability "console"`)
	if e := audit.entries()[2]; e.Stage != policyStageAdmission {
		t.Errorf("admitter entry stage = %q, want %q", e.Stage, policyStageAdmission)
	}
}
