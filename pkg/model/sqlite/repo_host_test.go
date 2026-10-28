package sqlite_test

import (
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
)

// A task on a git host other than the default keeps its host through
// the store -- target, reads, writes and the run's pinned target -- so
// the git proxy scopes and credentials it on that host; and a team's
// per-repository credential override applies on its own host only
// (docs/git-hosts.md, phase 2).
func TestATaskOnAnotherGitHostKeepsItsHost(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	if err := store.UpdateConfig(ctx, func(c *model.Config) error {
		c.GitConnections = []model.TeamGitConnection{
			{Credential: "dotcom"},
			{Credential: "ghe", Host: "ghe.example.com"},
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	ghe := model.RepoRef{Host: "ghe.example.com", Owner: "acme", Name: "widgets"}
	dotcom := model.RepoRef{Owner: "acme", Name: "widgets"}
	if err := store.PutRepoConfig(ctx, model.RepoConfig{Repo: ghe, GitCredential: "ghe-widgets"}); err != nil {
		t.Fatal(err)
	}

	tk := task("h0st", true)
	tk.Target = &ghe
	tk.Reads = []model.RepoRef{dotcom, {Host: "ghe.example.com", Owner: "acme", Name: "docs"}}
	tk.Writes = []model.WriteTarget{{Repo: model.RepoRef{Host: "ghe.example.com", Owner: "acme", Name: "infra"}, Base: "main"}}
	if err := store.PutTask(ctx, tk); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetTask(ctx, "h0st")
	if err != nil {
		t.Fatal(err)
	}
	if got.Target == nil || *got.Target != ghe || got.Target.String() != "ghe.example.com/acme/widgets" {
		t.Errorf("target = %+v, want %+v", got.Target, ghe)
	}
	if len(got.Reads) != 2 || got.Reads[0] != dotcom || got.Reads[1].Host != "ghe.example.com" {
		t.Errorf("reads = %+v", got.Reads)
	}
	if len(got.Writes) != 1 || got.Writes[0].Repo.Host != "ghe.example.com" {
		t.Errorf("writes = %+v", got.Writes)
	}

	if err := store.StartRun(ctx, model.Run{ID: "r1", TaskID: "h0st", Sandbox: "sandbox-0", Attempt: 1, StartedAt: now}, model.Limits{}); err != nil {
		t.Fatal(err)
	}
	if err := store.SetRunSandbox(ctx, "r1", "sandbox-0", got.Target); err != nil {
		t.Fatal(err)
	}
	target, reads, writes, err := store.GitScope(ctx, "sandbox-0")
	if err != nil {
		t.Fatal(err)
	}
	if target == nil || *target != ghe || len(reads) != 2 || len(writes) != 1 || writes[0].Host != "ghe.example.com" {
		t.Errorf("GitScope = %+v, %+v, %+v", target, reads, writes)
	}

	// The override for ghe's acme/widgets is that repository's, not the
	// default host's acme/widgets.
	for _, tc := range []struct{ host, repo, want string }{
		{"ghe.example.com", "widgets", "ghe-widgets"},
		{"ghe.example.com", "docs", "ghe"},
		{"", "widgets", "dotcom"},
	} {
		name, ok, err := store.GitCredentialOverrideOn(ctx, "sandbox-0", tc.host, "acme", tc.repo)
		if err != nil || !ok || name != tc.want {
			t.Errorf("GitCredentialOverrideOn(%q, acme/%s) = %q, %v, %v; want %q", tc.host, tc.repo, name, ok, err, tc.want)
		}
	}
}
