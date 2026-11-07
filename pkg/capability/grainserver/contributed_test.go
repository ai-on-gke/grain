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
	"sync/atomic"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/capability"
	"github.com/ai-on-gke/grain/pkg/capability/capabilitytest"
	"github.com/ai-on-gke/grain/pkg/capability/grainserver"
	"github.com/ai-on-gke/grain/pkg/capability/protocol"
	"github.com/ai-on-gke/grain/pkg/model"
)

// A server a build contributed a method to (capability.Register) resolves,
// counts as in-process and is listed beside grain's own -- and stops doing
// all three once the method is unregistered.
func TestContributedMethodServer(t *testing.T) {
	if grainserver.IsInProcessGrant(capabilitytest.MethodGrant) {
		t.Fatalf("%s is in-process before anything registers it", capabilitytest.MethodGrant)
	}
	capabilitytest.RegisterMethod(t)

	if !grainserver.IsInProcessGrant(capabilitytest.MethodGrant) || !grainserver.IsInProcessGrant(capabilitytest.MethodRef) {
		t.Error("the contributed method's grant is not in-process")
	}
	lookup := grainserver.Lookup(nil)
	srv, err := lookup(capabilitytest.MethodServer)
	if err != nil || srv.ID != capabilitytest.MethodServer || srv.Methods[capabilitytest.MethodName] == nil {
		t.Fatalf("Lookup(%s) = %+v, %v", capabilitytest.MethodServer, srv, err)
	}
	if srv, err := lookup(grainserver.ID); err != nil || srv.ID != grainserver.ID {
		t.Errorf("Lookup(%s) = %+v, %v; want grain's own server still", grainserver.ID, srv, err)
	}
	names, err := model.NormalizeCapabilityNames([]string{capabilitytest.MethodRef}, lookup)
	if err != nil || !slices.Equal(names, []string{capabilitytest.MethodGrant}) {
		t.Errorf("normalizing %s = %v, %v; want %s", capabilitytest.MethodRef, names, err, capabilitytest.MethodGrant)
	}

	var ids []string
	for _, s := range grainserver.Statuses() {
		ids = append(ids, s.ID)
		if s.ID == capabilitytest.MethodServer && (!slices.Equal(s.Methods, []string{capabilitytest.MethodName}) || s.Transport != "in-process") {
			t.Errorf("status = %+v", s)
		}
	}
	if want := []string{grainserver.ID, grainserver.RepoID, grainserver.GitHubID, capabilitytest.MethodServer}; !slices.Equal(ids, want) {
		t.Errorf("Statuses() = %v, want %v", ids, want)
	}

	var fellBack bool
	_, _ = grainserver.Lookup(func(string) (*protocol.Server, error) { fellBack = true; return nil, nil })("https://grain.example/elsewhere")
	if !fellBack {
		t.Error("a server nothing registered did not fall back")
	}
}

// A method a build contributes to core's github server (capability.Register:
// githubsandbox's #sandbox-repo) is served on the one in-process github
// server beside core's #read-issues, listed once, and released through a
// server built afresh after its mint: Lookup rebuilds the github server
// every time, so the contributed method's lease must outlive the server
// that minted it, or release_ would never reach its Release.
func TestGitHubServerServesAContributedMethod(t *testing.T) {
	var minted, released atomic.Int32
	unregister := capability.Register(capability.Method{
		Server: grainserver.GitHubID,
		Spec: protocol.MethodSpec{
			Name: "scratch",
			Args: map[string]any{"type": "object", "additionalProperties": false},
			Mint: func(context.Context, string, time.Time, map[string]any) (*protocol.MintResponse, error) {
				minted.Add(1)
				return &protocol.MintResponse{Status: protocol.StatusOK}, nil
			},
			Release: func(context.Context, string) error {
				released.Add(1)
				return nil
			},
			Now: func() time.Time { return time.Time{} },
		},
	})
	defer unregister()

	st := grainserver.GitHubStatus()
	if want := []string{grainserver.ReadIssuesMethod, "scratch"}; !slices.Equal(st.Methods, want) {
		t.Fatalf("GitHubStatus().Methods = %v, want %v", st.Methods, want)
	}
	var github int
	for _, s := range grainserver.Statuses() {
		if s.ID == grainserver.GitHubID {
			github++
		}
	}
	if github != 1 {
		t.Errorf("Statuses() lists %s %d times, want once", grainserver.GitHubID, github)
	}

	lookup := grainserver.Lookup(nil)
	ref := grainserver.GitHubID + "#scratch"
	grants, err := model.CapabilityGrants([]model.Grant{
		{Capability: ref + "={}", Via: model.GrantByLabel},
		{Capability: grainserver.ReadIssuesGrant, Via: model.GrantByLabel},
	}, lookup)
	if err != nil {
		t.Fatalf("CapabilityGrants: %v", err)
	}
	pass, err := protocol.MintPass(context.Background(), grants, func(id string) (protocol.Minter, error) {
		return lookup(id)
	}, protocol.PassOptions{Deployment: "test", Task: "1", Attempt: 1, Deadline: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatalf("MintPass: %v", err)
	}
	if minted.Load() != 1 {
		t.Fatalf("minted %d times, want 1", minted.Load())
	}
	if _, err := pass.Release(context.Background()); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if released.Load() != 1 {
		t.Errorf("released %d times, want 1: the release missed the lease its mint recorded", released.Load())
	}
}
