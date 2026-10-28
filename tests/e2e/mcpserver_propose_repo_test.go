// -propose-repo is the only thing about the escape-hatch tools a
// dispatch gets to say, and what it changes is a sentence in a
// description rather than a tool's presence -- so nothing downstream
// fails when it stops arriving. The layer tests each hold one link
// (pkg/model lists the repos, pkg/agent writes the flags,
// pkg/cli/mcpserver.go parses them, pkg/mcp folds them into the
// description), and between them sits a flag block with no compiler
// anywhere near it. This file joins the links the way a dispatched run
// does: a real `grain mcpserver` subprocess with the flag on its argv,
// asked over real stdio MCP for the tool list an agent actually reads.
//
// The failure it guards against is the one the flag exists to prevent,
// one layer up: a run that cannot see which repositories are on offer
// files its proposal against its own repo, and nobody finds out until a
// human retargets it by hand (grain/task-144).
package e2e

import (
	"strings"
	"testing"
)

func TestMCPServerNamesTheProposableReposInProposeTasksDescription(t *testing.T) {
	t.Parallel()
	bin := buildGrainBinary(t)

	// The repo argument's own description, which is where an agent reads
	// what it may write there -- not the tool's top-level text.
	repoArgument := func(t *testing.T, args ...string) string {
		t.Helper()
		p := startMCPServer(t, bin, append([]string{"-sandbox-root", t.TempDir()}, args...)...)
		infos, err := p.ListTools(t.Context())
		if err != nil {
			t.Fatalf("tools/list against grain mcpserver: %v\nstderr:\n%s", err, p.stderr.String())
		}
		for _, info := range infos {
			if info.Name != "propose_task" {
				continue
			}
			properties, ok := info.InputSchema["properties"].(map[string]any)
			if !ok {
				t.Fatalf("propose_task's inputSchema has no properties: %v", info.InputSchema)
			}
			spec, ok := properties["repo"].(map[string]any)
			if !ok {
				t.Fatalf("propose_task takes no repo argument: %v", properties)
			}
			description, _ := spec["description"].(string)
			return description
		}
		t.Fatalf("tools/list advertised no propose_task at all: %v", infos)
		return ""
	}

	described := repoArgument(t, "-propose-repo", "acme/widgets", "-propose-repo", "acme/docs")
	for _, want := range []string{"acme/widgets", "acme/docs"} {
		if !strings.Contains(described, want) {
			t.Errorf("propose_task's repo argument = %q, want it to name %q", described, want)
		}
	}

	// A dispatch told nothing -- a `grain mcpserver` run by hand, and
	// every test in this directory that forks one -- gets the rule
	// without the list rather than an empty one, and the tool is on the
	// roster either way.
	bare := repoArgument(t)
	for _, want := range []string{"acme/widgets", "acme/docs"} {
		if strings.Contains(bare, want) {
			t.Errorf("propose_task's repo argument = %q, want no repos named without the flag", bare)
		}
	}
	if !strings.HasPrefix(described, bare) {
		t.Errorf("the list replaced part of the description rather than adding to it:\n%q\n%q", described, bare)
	}

	// An argument that names no repository is dropped rather than being
	// fatal: these values come off grain's own store, so a bad one is
	// grain's bug to fix and not a run's to lose its sandbox tools over.
	// startMCPServer's own cleanup is the other half of that assertion --
	// it fails the test if the process died rather than exiting cleanly
	// when its stdin closed.
	survived := repoArgument(t, "-propose-repo", "not a repo", "-propose-repo", "acme/widgets")
	if !strings.Contains(survived, "acme/widgets") {
		t.Errorf("propose_task's repo argument = %q, want the repo that did parse still named", survived)
	}
}

// propose_task once offered "restricted" network access, which no
// sandbox backend grain ships can build: a proposal asking for it was
// filed with the deployment default instead, and said so only in a
// note (57096986c). What an agent reads is the schema a real server puts
// on the wire, so that is what this asks
// (docs/test-runs/2026-10-04-security-boundaries.md).
func TestMCPServerProposeTaskOffersNoRestrictedNetwork(t *testing.T) {
	t.Parallel()
	p := startMCPServer(t, buildGrainBinary(t), "-sandbox-root", t.TempDir())
	infos, err := p.ListTools(t.Context())
	if err != nil {
		t.Fatalf("tools/list against grain mcpserver: %v\nstderr:\n%s", err, p.stderr.String())
	}
	for _, info := range infos {
		if info.Name != "propose_task" {
			continue
		}
		properties, _ := info.InputSchema["properties"].(map[string]any)
		spec, ok := properties["network_access"].(map[string]any)
		if !ok {
			t.Fatalf("propose_task takes no network_access argument: %v", properties)
		}
		enum, _ := spec["enum"].([]any)
		if len(enum) == 0 {
			t.Fatalf("propose_task's network_access has no enum: %v", spec)
		}
		for _, v := range enum {
			if v == "restricted" {
				t.Errorf("propose_task's network_access offers %q (enum %v); no sandbox grain builds has it", v, enum)
			}
		}
		if description, _ := spec["description"].(string); strings.Contains(description, "restricted") {
			t.Errorf("propose_task's network_access description still mentions restricted: %q", description)
		}
		t.Logf("propose_task network_access enum: %v", enum)
		return
	}
	t.Fatalf("tools/list advertised no propose_task at all")
}
