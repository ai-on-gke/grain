package repowrite_test

import (
	"context"
	"testing"

	"github.com/ai-on-gke/grain/pkg/capability/repowrite"
	"github.com/ai-on-gke/grain/pkg/model"
)

func TestRepoWriteProviderSpec(t *testing.T) {
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	p := repowrite.New(repo)
	spec := p.Spec()

	if spec.Name != "repo-write:acme/widgets" {
		t.Errorf("Spec.Name = %q, want repo-write:acme/widgets", spec.Name)
	}
	if !spec.CIOnly {
		t.Error("Spec.CIOnly must be true")
	}
	if !spec.SupportsCI() {
		t.Error("Spec.SupportsCI() must be true")
	}
	if spec.SupportsTask() {
		t.Error("Spec.SupportsTask() must be false")
	}
	if p.Repo() != repo {
		t.Errorf("Repo() = %v, want %v", p.Repo(), repo)
	}
}

func TestRepoWriteSupportsCIEvents(t *testing.T) {
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	p := repowrite.New(repo)

	if p.SupportsCIEvent("push") {
		t.Error("SupportsCIEvent(push) should be false")
	}
	if p.SupportsCIEvent("pull_request") {
		t.Error("SupportsCIEvent(pull_request) should be false")
	}
	if !p.SupportsCIEvent("release") {
		t.Error("SupportsCIEvent(release) should be true")
	}
	if !p.SupportsCIEvent("workflow_dispatch") {
		t.Error("SupportsCIEvent(workflow_dispatch) should be true")
	}
}

func TestRepoWriteDynamicProvider(t *testing.T) {
	p, ok := repowrite.DynamicProvider("repo-write:acme/tools")
	if !ok {
		t.Fatal("DynamicProvider(repo-write:acme/tools) returned false")
	}
	if p.Spec().Name != "repo-write:acme/tools" {
		t.Errorf("Spec.Name = %q, want repo-write:acme/tools", p.Spec().Name)
	}

	if _, ok := repowrite.DynamicProvider("repo-write:invalid"); ok {
		t.Error("DynamicProvider(repo-write:invalid) should return false")
	}
	if _, ok := repowrite.DynamicProvider("other-cap"); ok {
		t.Error("DynamicProvider(other-cap) should return false")
	}
}

func TestRepoWriteResolve(t *testing.T) {
	repo := model.RepoRef{Owner: "acme", Name: "tools"}
	p := repowrite.New(repo)
	res, err := p.Resolve(context.Background(), model.CapabilityContext{CI: true})
	if err != nil || res.Refused {
		t.Fatalf("Resolve = %+v, %v", res, err)
	}
}
