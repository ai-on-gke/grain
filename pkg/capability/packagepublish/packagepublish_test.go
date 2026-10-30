// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package packagepublish

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
)

// ladder is a CredentialSource that answers for one repository and
// refuses every other, which is what the real ladder does for a
// deployment whose patterns cover one owner.
type ladder struct {
	repo     string
	registry string
	calls    []string
}

func (l *ladder) PackageCredential(registry, owner, repo string) (Credential, error) {
	l.calls = append(l.calls, registry+" "+owner+"/"+repo)
	if owner+"/"+repo != l.repo {
		return Credential{}, errors.New("no GitHub credential in this deployment covers " + owner + "/" + repo)
	}
	if registry == "" {
		registry = l.registry
	}
	return Credential{Registry: registry, Username: "x-access-token", Token: "minted-token", Name: "default"}, nil
}

func job(owner, name string) model.CapabilityContext {
	return model.CapabilityContext{Task: model.Task{Target: &model.RepoRef{Owner: owner, Name: name}}}
}

func TestPackagePublishPlacesTheLaddersCredentialForTheJobsRepository(t *testing.T) {
	source := &ladder{repo: "acme/widgets", registry: "ghcr.io"}
	p := New(Config{Credentials: source})

	if !p.Spec().SupportsCI() || p.SupportsCIEvent("push") || p.SupportsCIEvent("pull_request") ||
		!p.SupportsCIEvent("release") || !p.SupportsCIEvent("workflow_dispatch") {
		t.Fatalf("unexpected CI scope: spec=%+v", p.Spec())
	}

	m, err := p.Materialize(context.Background(), job("acme", "widgets"))
	if err != nil {
		t.Fatalf("materializing: %v", err)
	}
	if len(m.Placements) != 3 ||
		m.Placements[0].Content != "ghcr.io" || m.Placements[1].Content != "x-access-token" ||
		m.Placements[2].Content != "minted-token" {
		t.Fatalf("placements = %+v", m.Placements)
	}
	// The repository asked about is the job's own target: a workflow
	// cannot ask for somebody else's publishing credential.
	if len(source.calls) != 1 || !strings.HasSuffix(source.calls[0], "acme/widgets") {
		t.Errorf("ladder asked %v, want the job's own repository", source.calls)
	}

	// Off-default-host target passes StoredOwner() ("<host>/<owner>").
	forgeSource := &ladder{repo: "depot.code.forge.test/platform/grain", registry: "registry.forge.test"}
	forgeProvider := New(Config{Credentials: forgeSource})
	forgeJob := model.CapabilityContext{Task: model.Task{Target: &model.RepoRef{
		Host: "depot.code.forge.test", Owner: "platform", Name: "grain",
	}}}
	if _, err := forgeProvider.Materialize(context.Background(), forgeJob); err != nil {
		t.Fatalf("materializing off-default-host target: %v", err)
	}
	if len(forgeSource.calls) != 1 || !strings.HasSuffix(forgeSource.calls[0], "depot.code.forge.test/platform/grain") {
		t.Errorf("ladder asked %v, want StoredOwner()", forgeSource.calls)
	}
}

func TestPackagePublishRefusesWithTheLaddersOwnReason(t *testing.T) {
	p := New(Config{Credentials: &ladder{repo: "acme/widgets"}})

	resolution, err := p.Resolve(context.Background(), job("other", "thing"))
	if err != nil || !resolution.Refused {
		t.Fatalf("Resolve = %+v, %v", resolution, err)
	}
	if !strings.Contains(resolution.Reason, "other/thing") {
		t.Errorf("refusal %q does not repeat the ladder's reason", resolution.Reason)
	}

	// A deployment with no ladder at all, and a job with no repository:
	// both refuse rather than materializing empty files a `docker login`
	// would fail on minutes later.
	if r, err := New(Config{}).Resolve(context.Background(), job("acme", "widgets")); err != nil || !r.Refused {
		t.Fatalf("Resolve without a ladder = %+v, %v", r, err)
	}
	if r, err := p.Resolve(context.Background(), model.CapabilityContext{}); err != nil || !r.Refused {
		t.Fatalf("Resolve without a repository = %+v, %v", r, err)
	}
}

// The gate grain/task-382 is about, checked on the only two paths a
// grant of this capability can arrive by. A CI job carries an event and
// is judged on it (cmd/grain's resolveCICapabilities); a task carries
// none, so a task holding this is refused outright -- and the assertion
// that matters is not that something failed but that the ladder was
// never asked and no placement exists, because the failure this replaces
// resolved perfectly well and wrote a live registry token into the
// sandbox.
func TestPackagePublishReachesACIJobAndNeverATask(t *testing.T) {
	source := &ladder{repo: "acme/widgets", registry: "ghcr.io"}
	p := New(Config{Credentials: source})
	reg := model.NewCapabilityRegistry(p)
	grant := model.Grant{Capability: CapabilityName, Via: model.GrantByLabel}

	// A task -- one that names the very repository the ladder answers
	// for, which is exactly the case that used to succeed.
	task := job("acme", "widgets")
	task.Task.Grants = []model.Grant{grant}
	resolved, err := model.ResolveGrants(context.Background(), reg, task)
	if err != nil {
		t.Fatalf("resolving for a task: %v", err)
	}
	if len(resolved) != 1 || !resolved[0].Resolution.Refused {
		t.Fatalf("a task's grant resolved to %+v, want a refusal", resolved)
	}
	if reason := resolved[0].Resolution.Reason; !strings.Contains(reason, "CI capability") ||
		!strings.Contains(reason, "CI capabilities") {
		t.Errorf("refusal %q does not tell an operator where this is granted instead", reason)
	}
	if len(source.calls) != 0 {
		t.Errorf("the credential ladder was asked %v for a task, so a token was minted for one", source.calls)
	}
	materialized, err := model.MaterializeGrants(context.Background(), reg, task, resolved)
	if err != nil {
		t.Fatalf("materializing a refused grant: %v", err)
	}
	for _, m := range materialized {
		if len(m.Materialization.Placements) != 0 {
			t.Fatalf("a refused grant placed %+v in the sandbox", m.Materialization.Placements)
		}
	}

	// The CI path, unchanged: the same repository, resolved and placed.
	// The event gate itself is resolveCICapabilities' own and is
	// asserted above (SupportsCIEvent); what this holds is that the
	// task-path refusal did not take CI with it.
	ciJob := job("acme", "widgets")
	ciJob.CI = true
	ciJob.Task.Grants = []model.Grant{grant}
	resolved, err = model.ResolveGrants(context.Background(), reg, ciJob)
	if err != nil || len(resolved) != 1 || resolved[0].Resolution.Refused {
		t.Fatalf("a CI job's grant resolved to %+v, %v", resolved, err)
	}
	materialized, err = model.MaterializeGrants(context.Background(), reg, ciJob, resolved)
	if err != nil || len(materialized) != 1 || len(materialized[0].Materialization.Placements) != 3 {
		t.Fatalf("a CI job materialized %+v, %v", materialized, err)
	}
}

// A job with no repository at all -- the backstop, now that no task can
// hold this capability in the first place. What it owes whoever is
// looking at the failure is a sentence they can act on.
func TestPackagePublishWithNoRepositorySaysWhatToDo(t *testing.T) {
	r, err := New(Config{Credentials: &ladder{repo: "acme/widgets"}}).
		Resolve(context.Background(), model.CapabilityContext{CI: true})
	if err != nil || !r.Refused {
		t.Fatalf("Resolve without a repository = %+v, %v", r, err)
	}
	for _, want := range []string{CapabilityName, "names no repository", "CI capabilities"} {
		if !strings.Contains(r.Reason, want) {
			t.Errorf("refusal %q does not mention %q", r.Reason, want)
		}
	}
}

func TestPackagePublishUsesTheConfiguredRegistry(t *testing.T) {
	source := &ladder{repo: "acme/widgets"}
	p := New(Config{Credentials: source, Registry: "ghes.example.com"})
	m, err := p.Materialize(context.Background(), job("acme", "widgets"))
	if err != nil {
		t.Fatal(err)
	}
	if m.Placements[0].Content != "ghes.example.com" {
		t.Errorf("registry placed as %q, want the deployment's own", m.Placements[0].Content)
	}
}

// The daily release train is a cron grain fires from main, and it
// publishes the rc and pending images, so package-publish opts in to
// trusted schedules alongside repo-write and release-gate-read. The
// event gate itself still refuses `schedule`: an untrusted caller's
// `-event schedule` run gets nothing.
func TestTrustedScheduleIsAdmittedButAPlainScheduleIsNot(t *testing.T) {
	var p model.CapabilityProvider = New(Config{})
	trusted, ok := p.(model.TrustedScheduleCapability)
	if !ok || !trusted.SupportsTrustedSchedule() {
		t.Fatal("package-publish does not opt in to trusted schedules, so the release train's cron cannot publish")
	}
	if New(Config{}).SupportsCIEvent("schedule") {
		t.Fatal("package-publish accepts any schedule run, including one a caller asked for")
	}
}
