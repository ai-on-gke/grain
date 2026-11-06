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

package grainserver_test

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/capability/grainserver"
	"github.com/ai-on-gke/grain/pkg/capability/protocol"
	"github.com/ai-on-gke/grain/pkg/model"
)

func TestInProcessServerNormalizesAndMintsJournal(t *testing.T) {
	t.Parallel()

	lookup := grainserver.Lookup(nil)
	names, err := model.NormalizeCapabilityNames([]string{grainserver.JournalRef, grainserver.JournalGrant}, lookup)
	if err != nil {
		t.Fatalf("NormalizeCapabilityNames: %v", err)
	}
	if !slices.Equal(names, []string{grainserver.JournalGrant}) {
		t.Fatalf("normalized = %v, want [%s]", names, grainserver.JournalGrant)
	}

	grants, err := model.CapabilityGrants([]model.Grant{{Capability: names[0], Via: model.GrantByLabel}}, lookup)
	if err != nil {
		t.Fatalf("CapabilityGrants: %v", err)
	}
	pass, err := protocol.MintPass(context.Background(), grants, func(id string) (protocol.Minter, error) {
		return lookup(id)
	}, protocol.PassOptions{
		Deployment: "test",
		Task:       "1",
		Attempt:    1,
		Deadline:   time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("MintPass: %v", err)
	}
	t.Cleanup(func() { pass.Release(context.Background()) })

	st := grainserver.Status()
	if st.ID != grainserver.ID || !slices.Contains(st.Methods, grainserver.JournalMethod) || !slices.Contains(st.Methods, grainserver.HealthMethod) {
		t.Fatalf("Status() = %+v, want %s with %s and %s", st, grainserver.ID, grainserver.JournalMethod, grainserver.HealthMethod)
	}
	if !grainserver.HasJournal([]string{grainserver.JournalGrant}) || !grainserver.HasJournal([]string{"grain-log"}) {
		t.Fatalf("HasJournal failed to match canonical or legacy journal grant")
	}

	sqlDefault, err := model.NormalizeCapabilityNames([]string{grainserver.SQLRef}, lookup)
	if err != nil || !slices.Equal(sqlDefault, []string{grainserver.SQLReadGrant}) {
		t.Fatalf("NormalizeCapabilityNames(sql) = %v, %v; want [%s]", sqlDefault, err, grainserver.SQLReadGrant)
	}
	if model.IsDangerousGrant(grainserver.SQLReadGrant) {
		t.Fatalf("SQLReadGrant should not be dangerous")
	}
	if !model.IsDangerousGrant(grainserver.SQLWriteGrant) || !model.IsPersonOnlyGrant(grainserver.SQLWriteGrant) {
		t.Fatalf("SQLWriteGrant should be dangerous and person-only")
	}

	hostExec, err := model.NormalizeCapabilityNames([]string{grainserver.HostExecRef}, lookup)
	if err != nil || !slices.Equal(hostExec, []string{grainserver.HostExecGrant}) {
		t.Fatalf("NormalizeCapabilityNames(host-exec) = %v, %v; want [%s]", hostExec, err, grainserver.HostExecGrant)
	}
	if !slices.Contains(st.Methods, grainserver.HostExecMethod) {
		t.Fatalf("Status().Methods = %v, want %s", st.Methods, grainserver.HostExecMethod)
	}
	if !model.IsDangerousGrant(grainserver.HostExecGrant) || !model.IsPersonOnlyGrant(grainserver.HostExecGrant) {
		t.Fatalf("HostExecGrant should be dangerous and person-only")
	}
	if got := model.CanonicalLegacyGrant(grainserver.LegacySelfRepair); got != grainserver.HostExecGrant {
		t.Fatalf("CanonicalLegacyGrant(self-repair) = %q, want %q", got, grainserver.HostExecGrant)
	}
	if !grainserver.HasHostExec([]string{grainserver.HostExecGrant}) || !grainserver.HasHostExec([]string{"self-repair"}) {
		t.Fatalf("HasHostExec failed to match canonical or legacy host-exec grant")
	}

	ciGrant, err := model.NormalizeCapabilityNames([]string{grainserver.CIRef}, lookup)
	if err != nil || !slices.Equal(ciGrant, []string{grainserver.CIGrant}) {
		t.Fatalf("NormalizeCapabilityNames(ci) = %v, %v; want [%s]", ciGrant, err, grainserver.CIGrant)
	}
	if !slices.Contains(st.Methods, grainserver.CIMethod) {
		t.Fatalf("Status().Methods = %v, want %s", st.Methods, grainserver.CIMethod)
	}
	if model.IsDangerousGrant(grainserver.CIGrant) {
		t.Fatalf("CIGrant should not be dangerous: it opens no credential")
	}
	if got := model.CanonicalLegacyGrant(grainserver.LegacyGrainCI); got != grainserver.CIGrant {
		t.Fatalf("CanonicalLegacyGrant(grain-ci) = %q, want %q", got, grainserver.CIGrant)
	}
	if !grainserver.IsCIGrant(grainserver.CIGrant) || !grainserver.IsCIGrant("grain-ci") {
		t.Fatalf("IsCIGrant failed to match canonical or legacy ci grant")
	}

	source, err := model.NormalizeCapabilityNames([]string{grainserver.SourceRef}, lookup)
	if err != nil || !slices.Equal(source, []string{grainserver.SourceGrant}) {
		t.Fatalf("NormalizeCapabilityNames(source) = %v, %v; want [%s]", source, err, grainserver.SourceGrant)
	}
	if !slices.Contains(st.Methods, grainserver.SourceMethod) {
		t.Fatalf("Status().Methods = %v, want %s", st.Methods, grainserver.SourceMethod)
	}
	if model.IsDangerousGrant(grainserver.SourceGrant) {
		t.Fatalf("SourceGrant should not be dangerous: it is read-only, as self-debug was")
	}
	wantSelfDebug := []string{"api:self-debug", grainserver.SourceGrant}
	if got := model.CanonicalLegacyGrants(grainserver.LegacySelfDebug); !slices.Equal(got, wantSelfDebug) {
		t.Fatalf("CanonicalLegacyGrants(self-debug) = %v, want %v", got, wantSelfDebug)
	}
	if !grainserver.HasSource([]string{grainserver.SourceGrant}) || !grainserver.HasSource([]string{"self-debug"}) ||
		!grainserver.IsSourceGrant(grainserver.SourceRef) {
		t.Fatalf("HasSource/IsSourceGrant failed to match canonical, bare or legacy source grant")
	}
	if grainserver.HasSource([]string{"api:self-debug"}) {
		t.Fatalf("HasSource matched api:self-debug, which grants no source checkout")
	}
}

// A pass's deadline comes from the orchestrator's clock, which need not
// be the wall clock -- a test world's is months behind it -- and the
// in-process server, running inside grain, holds it to no clock of its own.
func TestInProcessServerMintsOnTheCallersClock(t *testing.T) {
	t.Parallel()

	lookup := grainserver.Lookup(nil)
	grants, err := model.CapabilityGrants([]model.Grant{{Capability: grainserver.HealthGrant, Via: model.GrantByLabel}}, lookup)
	if err != nil {
		t.Fatalf("CapabilityGrants: %v", err)
	}
	then := time.Now().Add(-90 * 24 * time.Hour)
	pass, err := protocol.MintPass(context.Background(), grants, func(id string) (protocol.Minter, error) {
		return lookup(id)
	}, protocol.PassOptions{
		Deployment: "test",
		Task:       "1",
		Attempt:    1,
		Deadline:   then.Add(time.Hour),
		Now:        func() time.Time { return then },
	})
	if err != nil {
		t.Fatalf("MintPass with a deadline behind the wall clock: %v", err)
	}
	pass.Release(context.Background())
}

// repo#write normalizes to the one value the git proxy honours, which is
// what migration 85 writes for git-push-any-ref, and nothing narrower
// validates: a task-level refs list the proxy would not honour is refused
// rather than recorded as though it did something.
func TestRepoServerNormalizesGitPushAnyRef(t *testing.T) {
	t.Parallel()

	lookup := grainserver.Lookup(nil)
	reordered := grainserver.RepoWriteRef + `={"refs":["refs/tags/*","refs/heads/*"],"force":true}`
	names, err := model.NormalizeCapabilityNames([]string{grainserver.GitPushAnyRefGrant}, lookup)
	if err != nil || !slices.Equal(names, []string{grainserver.GitPushAnyRefGrant}) {
		t.Fatalf("NormalizeCapabilityNames(%s) = %v, %v; want it unchanged", grainserver.GitPushAnyRefGrant, names, err)
	}
	if !grainserver.IsGitPushAnyRefGrant(grainserver.GitPushAnyRefGrant) ||
		!grainserver.IsGitPushAnyRefGrant(reordered) ||
		!grainserver.IsGitPushAnyRefGrant(grainserver.LegacyGitPushAnyRef) {
		t.Fatal("IsGitPushAnyRefGrant missed the canonical grant, its reordered spelling or the legacy alias")
	}
	if got := model.CanonicalLegacyGrant(grainserver.LegacyGitPushAnyRef); got != grainserver.GitPushAnyRefGrant {
		t.Fatalf("CanonicalLegacyGrant(%q) = %q, want %q", grainserver.LegacyGitPushAnyRef, got, grainserver.GitPushAnyRefGrant)
	}
	for _, narrow := range []string{
		grainserver.RepoWriteRef,
		grainserver.RepoWriteRef + `={"refs":["refs/heads/*"],"force":true}`,
		grainserver.RepoWriteRef + `={"refs":["refs/heads/*","refs/tags/*"],"force":false}`,
		grainserver.RepoWriteRef + `={"refs":["refs/heads/main","refs/tags/*"],"force":true}`,
	} {
		if grainserver.IsGitPushAnyRefGrant(narrow) {
			t.Errorf("IsGitPushAnyRefGrant(%s) = true; want only the full any-ref grant", narrow)
		}
		if _, err := model.NormalizeCapabilityNames([]string{narrow}, lookup); err == nil {
			t.Errorf("NormalizeCapabilityNames(%s) validated; want it refused", narrow)
		}
	}
	if model.IsDangerousGrant(grainserver.GitPushAnyRefGrant) {
		t.Error("GitPushAnyRefGrant is dangerous; git-push-any-ref was a team grant, and the migration must not change who may give it")
	}
	if !grainserver.IsInProcessGrant(grainserver.GitPushAnyRefGrant) {
		t.Error("GitPushAnyRefGrant is not read as an in-process grant")
	}

	grants, err := model.CapabilityGrants([]model.Grant{{Capability: grainserver.GitPushAnyRefGrant, Via: model.GrantByLabel}}, lookup)
	if err != nil {
		t.Fatalf("CapabilityGrants: %v", err)
	}
	pass, err := protocol.MintPass(context.Background(), grants, func(id string) (protocol.Minter, error) {
		return lookup(id)
	}, protocol.PassOptions{Deployment: "test", Task: "1", Attempt: 1, Deadline: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatalf("MintPass: %v", err)
	}
	pass.Release(context.Background())

	st := grainserver.RepoStatus()
	if want := []string{grainserver.RepoReadMethod, grainserver.RepoWriteMethod}; st.ID != grainserver.RepoID || !slices.Equal(st.Methods, want) {
		t.Fatalf("RepoStatus() = %+v, want %s with %v", st, grainserver.RepoID, want)
	}
}

// repo#read {repo} and repo#write {repo} -- a CI job's access to another
// repository, formerly repo-read:/repo-write: -- validate and record as
// model.RepoReadGrant / RepoWriteGrant, read back as their repository, and
// are not the any-ref grant; neither method takes a repo beside anything
// else, or a value that is not a repository.
func TestRepoServerNormalizesRepoAccess(t *testing.T) {
	t.Parallel()

	lookup := grainserver.Lookup(nil)
	tools := model.RepoRef{Owner: "acme", Name: "tools"}
	for _, tc := range []struct {
		grant  string
		target func(string) (model.RepoRef, bool)
	}{
		{model.RepoReadGrant(tools), model.RepoReadTarget},
		{model.RepoWriteGrant(tools), model.RepoWriteTarget},
	} {
		names, err := model.NormalizeCapabilityNames([]string{tc.grant}, lookup)
		if err != nil || !slices.Equal(names, []string{tc.grant}) {
			t.Fatalf("NormalizeCapabilityNames(%s) = %v, %v; want it unchanged", tc.grant, names, err)
		}
		if got, ok := tc.target(tc.grant); !ok || got != tools {
			t.Errorf("target(%s) = %v, %v; want %v", tc.grant, got, ok, tools)
		}
		if !model.IsRepoAccessGrant(tc.grant) || !grainserver.IsInProcessGrant(tc.grant) {
			t.Errorf("%s is not read as an in-process repository access grant", tc.grant)
		}
		if grainserver.IsGitPushAnyRefGrant(tc.grant) {
			t.Errorf("IsGitPushAnyRefGrant(%s) = true; want only the any-ref value", tc.grant)
		}
	}
	if model.IsRepoAccessGrant(grainserver.GitPushAnyRefGrant) {
		t.Error("GitPushAnyRefGrant is read as repository access")
	}
	for _, bad := range []string{
		grainserver.RepoReadRef,
		grainserver.RepoReadRef + `={"repo":"acme"}`,
		grainserver.RepoReadRef + `={"repo":"acme/tools","force":true}`,
		grainserver.RepoWriteRef + `={"repo":"acme/tools","refs":["refs/heads/*","refs/tags/*"],"force":true}`,
		grainserver.RepoWriteRef + `={"repo":7}`,
	} {
		if _, err := model.NormalizeCapabilityNames([]string{bad}, lookup); err == nil {
			t.Errorf("NormalizeCapabilityNames(%s) validated; want it refused", bad)
		}
	}
}
