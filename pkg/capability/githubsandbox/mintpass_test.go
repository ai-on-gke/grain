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

package githubsandbox_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/capability/githubsandbox"
	"github.com/ai-on-gke/grain/pkg/capability/grainserver"
	"github.com/ai-on-gke/grain/pkg/capability/protocol"
	"github.com/ai-on-gke/grain/pkg/model"
)

func sandboxRepoGrants(t *testing.T, lookup protocol.Lookup, n int) []protocol.Grant {
	t.Helper()
	grants, err := model.CapabilityGrants([]model.Grant{{Capability: model.GitHubSandboxRepoGrant, Via: model.GrantByLabel}}, lookup)
	if err != nil {
		t.Fatalf("CapabilityGrants(%s): %v", model.GitHubSandboxRepoGrant, err)
	}
	if len(grants) != 1 {
		t.Fatalf("CapabilityGrants(%s) = %d grants, want 1", model.GitHubSandboxRepoGrant, len(grants))
	}
	var out []protocol.Grant
	for range n {
		out = append(out, grants[0])
	}
	return out
}

// TestSandboxRepoMintsThroughMintPass mints github#sandbox-repo the way a
// task run does -- grainserver.Lookup, protocol.MintPass, and so
// protocol.Server.Mint's response validation -- rather than calling the
// MethodSpec directly. A placement path the protocol reserves fails every
// mint there, after the repository was already created on GitHub.
func TestSandboxRepoMintsThroughMintPass(t *testing.T) {
	app := githubsandbox.NewFakeApp("grain-bot")
	t.Cleanup(githubsandbox.SetCurrent(app.Provider()))
	creds := githubsandbox.NewFakeCredentials()

	lookup := grainserver.Lookup(nil)
	grants := sandboxRepoGrants(t, lookup, 1)
	ctx := model.WithMintCredentials(model.WithMintRunID(context.Background(), "run-7"), creds.Resolver())
	pass, err := protocol.MintPass(ctx, grants, func(id string) (protocol.Minter, error) {
		return lookup(id)
	}, protocol.PassOptions{
		Deployment: "dep",
		Task:       "7",
		Attempt:    1,
		Deadline:   time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("MintPass(%s): %v", model.GitHubSandboxRepoGrant, err)
	}
	if len(pass.Grants) != 1 {
		t.Fatalf("pass has %d grants, want 1", len(pass.Grants))
	}
	minted := pass.Grants[0]
	if minted.Response.Status != protocol.StatusOK {
		t.Fatalf("mint = %+v", minted.Response)
	}
	var paths []string
	for _, pl := range minted.Response.Placements {
		paths = append(paths, pl.Path)
	}
	if !slices.Contains(paths, githubsandbox.SandboxTokenPath) || !slices.Contains(paths, githubsandbox.SandboxRepoPath) {
		t.Errorf("placements = %v, want %s and %s", paths, githubsandbox.SandboxTokenPath, githubsandbox.SandboxRepoPath)
	}
	// Named for the attempt, which carries the deployment, so two
	// deployments sharing one App installation never collide.
	if want := []string{githubsandbox.RepoPrefix + "-" + minted.AttemptID}; !slices.Equal(app.Repos(), want) {
		t.Errorf("repositories = %v, want %v", app.Repos(), want)
	}
	if !strings.Contains(minted.AttemptID, "dep") {
		t.Errorf("attempt id %q does not name the deployment", minted.AttemptID)
	}

	pass.Release(context.Background())
	if got := app.Repos(); len(got) != 0 {
		t.Errorf("after release: repositories %v remain", got)
	}
	runs := creds.AuditedRuns()
	if len(runs) == 0 {
		t.Fatal("no credentials were resolved")
	}
	for i, run := range runs {
		if run != "run-7" {
			t.Errorf("credential resolve %d audited under run %q, want run-7 (mint and release alike)", i, run)
		}
	}
}

// TestSandboxRepoPlacementsStillConflictAcrossGrants pins what keeps the
// unreserved token path safe: two grants placing it in one pass are
// refused by the pass's own conflict check, and every repository either
// created is deleted again.
func TestSandboxRepoPlacementsStillConflictAcrossGrants(t *testing.T) {
	app := githubsandbox.NewFakeApp("grain-bot")
	t.Cleanup(githubsandbox.SetCurrent(app.Provider()))
	creds := githubsandbox.NewFakeCredentials()

	lookup := grainserver.Lookup(nil)
	grants := sandboxRepoGrants(t, lookup, 2)
	ctx := model.WithMintCredentials(model.WithMintRunID(context.Background(), "run-8"), creds.Resolver())
	pass, err := protocol.MintPass(ctx, grants, func(id string) (protocol.Minter, error) {
		return lookup(id)
	}, protocol.PassOptions{
		Deployment: "dep",
		Task:       "8",
		Attempt:    1,
		Deadline:   time.Now().Add(time.Hour),
	})
	if err == nil {
		pass.Release(context.Background())
		t.Fatal("MintPass with two grants placing one path succeeded")
	}
	var refused *protocol.RefusedError
	if !errors.As(err, &refused) && !strings.Contains(err.Error(), githubsandbox.SandboxTokenPath) {
		t.Errorf("MintPass error = %v, want a conflict on %s", err, githubsandbox.SandboxTokenPath)
	}
	if strings.Contains(err.Error(), "reserved") {
		t.Errorf("MintPass error = %v: the token path should not be reserved", err)
	}
	if got := app.Repos(); len(got) != 0 {
		t.Errorf("after the refused pass: repositories %v remain", got)
	}
}
