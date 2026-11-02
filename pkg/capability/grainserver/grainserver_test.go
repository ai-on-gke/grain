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
