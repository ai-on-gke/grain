package reporead_test

import (
	"context"
	"testing"

	"github.com/bwsalmon/grain/pkg/capability/reporead"
	"github.com/bwsalmon/grain/pkg/model"
)

func TestRepoReadProviderSpec(t *testing.T) {
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	p := reporead.New(repo)
	spec := p.Spec()

	if spec.Name != "repo-read:acme/widgets" {
		t.Errorf("Spec.Name = %q, want repo-read:acme/widgets", spec.Name)
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

func TestRepoReadDynamicProvider(t *testing.T) {
	p, ok := reporead.DynamicProvider("repo-read:acme/tools")
	if !ok {
		t.Fatal("DynamicProvider(repo-read:acme/tools) returned false")
	}
	if p.Spec().Name != "repo-read:acme/tools" {
		t.Errorf("Spec.Name = %q, want repo-read:acme/tools", p.Spec().Name)
	}

	if _, ok := reporead.DynamicProvider("repo-read:invalid"); ok {
		t.Error("DynamicProvider(repo-read:invalid) should return false")
	}
	if _, ok := reporead.DynamicProvider("other-cap"); ok {
		t.Error("DynamicProvider(other-cap) should return false")
	}
}

func TestRepoReadResolve(t *testing.T) {
	repo := model.RepoRef{Owner: "acme", Name: "tools"}
	p := reporead.New(repo)
	res, err := p.Resolve(context.Background(), model.CapabilityContext{CI: true})
	if err != nil || res.Refused {
		t.Fatalf("Resolve = %+v, %v", res, err)
	}
}
