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

package ui_test

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/ui"
	"github.com/ai-on-gke/grain/pkg/model"
)

type mapRevResolver map[string]string

func (m mapRevResolver) ResolveRepoRev(_ context.Context, repo model.RepoRef, rev string) (string, bool, error) {
	if rev == "" {
		rev = "HEAD"
	}
	sha, ok := m[strings.ToLower(repo.String())+"@"+rev]
	if !ok {
		// Check if repo itself is known to this mock.
		repoKnown := false
		prefix := strings.ToLower(repo.String()) + "@"
		for k := range m {
			if strings.HasPrefix(k, prefix) {
				repoKnown = true
				break
			}
		}
		if !repoKnown {
			return "", false, nil
		}
		return "", true, nil
	}
	return sha, true, nil
}

func TestCommittedContextPlacementsPinningAutoGrantAndChildInheritance(t *testing.T) {
	t.Parallel()
	c, _, ctx := testClient(t)
	widgetsSHA := "1111111111111111111111111111111111111111"
	specsSHA := "2222222222222222222222222222222222222222"
	c.Config.RepoRevs = mapRevResolver{
		"acme/widgets@HEAD": widgetsSHA,
		"acme/widgets@main": widgetsSHA,
		"acme/specs@HEAD":   specsSHA,
		"acme/specs@v1.0.0": specsSHA,
	}

	// 1. Target-repo placement: ref resolved to SHA, bare path normalized.
	targetTask, err := c.CreateTask(ctx, ui.CreateTaskRequest{
		Title:    "[PLC] Implement checkout flow",
		Repo:     "acme/widgets",
		Designs:  []string{"design/plc/042@main"},
		Approved: true,
	})
	if err != nil {
		t.Fatalf("CreateTask target-repo context: %v", err)
	}
	if len(targetTask.Designs) != 1 || targetTask.Designs[0] != "design/plc/042@"+widgetsSHA {
		t.Fatalf("targetTask.Designs = %v, want [design/plc/042@%s]", targetTask.Designs, widgetsSHA)
	}
	if len(targetTask.Context) != 1 || targetTask.Context[0].Repo != "acme/widgets" ||
		targetTask.Context[0].Path != "design/plc/042" || targetTask.Context[0].Rev != widgetsSHA ||
		targetTask.Context[0].Placement != "target" {
		t.Fatalf("targetTask.Context = %+v", targetTask.Context)
	}

	// 2. Separate context repo placement: refused without -read when autoGrantContextReads is empty.
	_, err = c.CreateTask(ctx, ui.CreateTaskRequest{
		Title:   "[PLC] Missing read grant",
		Repo:    "acme/widgets",
		Designs: []string{"acme/specs:plc/042@v1.0.0"},
	})
	if err == nil || !strings.Contains(err.Error(), "-read acme/specs") {
		t.Fatalf("expected missing -read validation error, got %v", err)
	}

	// 3. Separate context repo with explicit -read succeeds and resolves tag to SHA.
	extTask, err := c.CreateTask(ctx, ui.CreateTaskRequest{
		Title:    "[PLC] External context with explicit read",
		Repo:     "acme/widgets",
		Reads:    []string{"acme/specs"},
		Designs:  []string{"acme/specs:plc/042@v1.0.0"},
		Approved: true,
	})
	if err != nil {
		t.Fatalf("CreateTask external context: %v", err)
	}
	if len(extTask.Context) != 1 || extTask.Context[0].Placement != "read" ||
		extTask.Context[0].Repo != "acme/specs" || extTask.Context[0].Rev != specsSHA {
		t.Fatalf("extTask.Context = %+v", extTask.Context)
	}

	// 4. Enabling autoGrantContextReads auto-grants -read without explicit Reads.
	cfg := model.DefaultConfig()
	cfg.AutoGrantContextReads = []string{"acme/specs"}
	if err := c.Store.PutConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	autoTask, err := c.CreateTask(ctx, ui.CreateTaskRequest{
		Title: "[PLC] External context with auto-granted read",
		Repo:  "acme/widgets",
		Context: []ui.ContextRefInput{{
			Repo: "acme/specs",
			Path: "plc/042",
			Rev:  "v1.0.0",
		}},
		Approved: true,
	})
	if err != nil {
		t.Fatalf("CreateTask auto-granted context: %v", err)
	}
	if !slices.Contains(autoTask.Reads, "acme/specs") {
		t.Fatalf("autoTask.Reads = %v, want acme/specs auto-granted", autoTask.Reads)
	}

	// Disable auto-grant again to test UpdateTask read removal protection and child inheritance.
	cfg.AutoGrantContextReads = nil
	if err := c.Store.PutConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	emptyReads := []string{}
	if _, err := c.UpdateTask(ctx, extTask.ID, ui.UpdateTaskRequest{Reads: &emptyReads}); err == nil {
		t.Fatal("expected UpdateTask dropping required context read repo to fail")
	}

	// 5. Same-repo child task inherits parent's context and -read grant automatically.
	sameRepoChild, err := c.CreateTask(ctx, ui.CreateTaskRequest{
		Title:      "[PLC] Follow-up task",
		Repo:       "acme/widgets",
		ProposedBy: extTask.ID,
	})
	if err != nil {
		t.Fatalf("CreateTask same-repo child: %v", err)
	}
	if !slices.Contains(sameRepoChild.Reads, "acme/specs") {
		t.Fatalf("sameRepoChild.Reads = %v, want inherited acme/specs", sameRepoChild.Reads)
	}
	if len(sameRepoChild.Context) != 1 || sameRepoChild.Context[0].Rev != specsSHA {
		t.Fatalf("sameRepoChild.Context = %+v, want inherited pinned context", sameRepoChild.Context)
	}

	// 6. Cross-repo child task inherits parent's target-repo context qualified as acme/widgets:... with -read acme/widgets.
	crossRepoChild, err := c.CreateTask(ctx, ui.CreateTaskRequest{
		Title:      "[PLC] Cross-repo follow-up",
		Repo:       "acme/docs",
		ProposedBy: targetTask.ID,
	})
	if err != nil {
		t.Fatalf("CreateTask cross-repo child: %v", err)
	}
	if !slices.Contains(crossRepoChild.Reads, "acme/widgets") {
		t.Fatalf("crossRepoChild.Reads = %v, want acme/widgets", crossRepoChild.Reads)
	}
	if len(crossRepoChild.Designs) != 1 || crossRepoChild.Designs[0] != "acme/widgets:design/plc/042@"+widgetsSHA {
		t.Fatalf("crossRepoChild.Designs = %v", crossRepoChild.Designs)
	}

	// 7. Child filed with its own context keeps its own rather than inheriting parent's.
	overrideSHA := "3333333333333333333333333333333333333333"
	explicitChild, err := c.CreateTask(ctx, ui.CreateTaskRequest{
		Title:      "[PLC] Explicit context child",
		Repo:       "acme/widgets",
		ProposedBy: extTask.ID,
		Designs:    []string{"design/plc/099@" + overrideSHA},
	})
	if err != nil {
		t.Fatalf("CreateTask explicit child: %v", err)
	}
	if len(explicitChild.Designs) != 1 || explicitChild.Designs[0] != "design/plc/099@"+overrideSHA {
		t.Fatalf("explicitChild.Designs = %v, want only child's own context", explicitChild.Designs)
	}
}
