package cli

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/daemon"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/policy"
)

func mustCIPolicy(t *testing.T, doc string) *policy.Policy {
	t.Helper()
	p, err := policy.Parse([]byte(doc))
	if err != nil {
		t.Fatalf("parsing policy: %v", err)
	}
	return p
}

// GSA1-07: a CI job's repositories, capabilities and network answer to
// the owner's -policy the way a task's do, glob semantics included.
func TestCIJobsAreJudgedAgainstTheLocalPolicy(t *testing.T) {
	t.Parallel()
	const base = "repos: [acme/*]\n"
	for _, tc := range []struct {
		name   string
		policy string
		caps   []string
		repo   string
		refuse string
	}{
		{name: "allowed", policy: base, caps: []string{"repo-read:acme/tools", "gcp-key"}},
		{name: "repository under test outside repos", policy: base, repo: "evil/widgets", refuse: "repository evil/widgets"},
		{name: "declared read outside repos", policy: base, caps: []string{"repo-read:evil/secrets"}, refuse: "repository evil/secrets"},
		{name: "declared write outside repos", policy: base, caps: []string{"repo-write:evil/secrets"}, refuse: "repository evil/secrets"},
		{name: "family deny covers parameterised grant", policy: base + "capabilities: {deny: [gcp-key]}\n",
			caps: []string{"gcp-key:team-a"}, refuse: `forbids capability "gcp-key:team-a"`},
		{name: "glob deny on repo-write", policy: base + "capabilities: {deny: [\"repo-write:acme/*\"]}\n",
			caps: []string{"repo-write:acme/tools"}, refuse: `forbids capability "repo-write:acme/tools"`},
		{name: "allow list refuses the rest", policy: base + "capabilities: {allow: [\"repo-read:*\"]}\n",
			caps: []string{"repo-read:acme/tools", "package-publish"}, refuse: `does not allow capability "package-publish"`},
		{name: "implicit grain-ci is not judged", policy: base + "capabilities: {allow: [gcp-key]}\n",
			caps: []string{"grain-ci", "gcp-key"}},
		{name: "network none refuses open CI sandboxes", policy: base + "network: [none]\n", refuse: `network access "open"`},
		{name: "network open allowed", policy: base + "network: [open]\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			repo := tc.repo
			if repo == "" {
				repo = "acme/widgets"
			}
			err := daemon.CheckCIJobPolicy(mustCIPolicy(t, tc.policy), ci.SandboxRequest{
				Name: "ci-job", Repo: repo, Ref: "main", Event: ci.EventPush, Capabilities: tc.caps,
			})
			switch {
			case tc.refuse == "" && err != nil:
				t.Fatalf("refused: %v", err)
			case tc.refuse != "" && (err == nil || !strings.Contains(err.Error(), tc.refuse)):
				t.Fatalf("err = %v, want one naming %q", err, tc.refuse)
			}
		})
	}
	if err := daemon.CheckCIJobPolicy(nil, ci.SandboxRequest{Repo: "evil/widgets", Capabilities: []string{"repo-write:evil/x"}}); err != nil {
		t.Fatalf("a daemon with no -policy refused a job: %v", err)
	}
}

// The refusal comes before the sandbox: no machine, no scope, and one
// line in the daemon log saying why.
func TestCIJobRefusedByLocalPolicyGetsNoSandbox(t *testing.T) {
	t.Parallel()
	fake := &fakeSandbox{}
	adapter, backend, scopes := newCIAdapter(fake)
	adapter.Policy = mustCIPolicy(t, "repos: [acme/widgets]\n")
	var logged []string
	adapter.Log = func(format string, args ...any) { logged = append(logged, fmt.Sprintf(format, args...)) }
	req := ci.SandboxRequest{
		Name: "ci-run1-pushed", Repo: "acme/widgets", Ref: "agent-branch", Event: ci.EventPush,
		Capabilities: []string{"repo-read:other/private"},
	}
	_, err := adapter.Acquire(context.Background(), req)
	if err == nil || !strings.Contains(err.Error(), "local policy refuses this CI job") ||
		!strings.Contains(err.Error(), "other/private") {
		t.Fatalf("Acquire err = %v, want a local policy refusal naming other/private", err)
	}
	if backend.acquired != "" {
		t.Fatalf("a sandbox %q was built for a job local policy refuses", backend.acquired)
	}
	if got := scopes.Repos(req.Name); len(got) != 0 {
		t.Fatalf("read scopes granted to a refused job: %v", got)
	}
	if len(logged) != 1 || !strings.Contains(logged[0], "refused by local policy") || !strings.Contains(logged[0], req.Name) {
		t.Fatalf("log = %q, want one refusal line naming the job", logged)
	}

	// The same job inside the policy is built as before.
	req.Capabilities = nil
	job, err := adapter.Acquire(context.Background(), req)
	if err != nil {
		t.Fatalf("Acquire of an allowed job: %v", err)
	}
	if err := job.Release(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// Admission materializes scarce grants before any sandbox exists, so it
// asks the policy first and never reaches a provider for a refused job.
func TestCIGrantAdmissionRefusedByLocalPolicyMaterializesNothing(t *testing.T) {
	t.Parallel()
	asked := false
	admission := &daemon.CiGrantReservations{
		Capabilities: func() *model.CapabilityRegistry { asked = true; return model.NewCapabilityRegistry() },
		Policy:       mustCIPolicy(t, "repos: [acme/*]\ncapabilities: {deny: [\"gcp-key:*\"]}\n"),
	}
	_, err := admission.Acquire(context.Background(), ci.SandboxRequest{
		Name: "ci-run1-deploy", Repo: "acme/widgets", Event: ci.EventPush, Capabilities: []string{"gcp-key:prod"},
	}, nil)
	if err == nil || !strings.Contains(err.Error(), `forbids capability "gcp-key:prod"`) {
		t.Fatalf("Acquire err = %v, want a refusal of gcp-key:prod", err)
	}
	if asked {
		t.Fatal("the capability registry was asked for a job local policy refuses")
	}
}
