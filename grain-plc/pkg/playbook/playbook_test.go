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

package playbook_test

import (
	"errors"
	"testing"

	"github.com/ai-on-gke/grain/grain-plc/pkg/playbook"
)

const testCommitSHA = "0123456789abcdef0123456789abcdef01234567"

func TestCommittedContextFolderRuleValidation(t *testing.T) {
	goodPath, err := playbook.BuildContextPath("plc", "checkout-redesign")
	if err != nil || goodPath != ".grain/context/plc/checkout-redesign" {
		t.Fatalf("BuildContextPath = %q, err=%v", goodPath, err)
	}

	for _, bad := range []string{
		"",
		"docs/prd.md",
		".grain/context",
		".grain/context/plc",
		"/.grain/context/plc/slug",
		".grain/context/plc/../secret",
		".grain\\context\\plc\\slug",
	} {
		if err := playbook.ValidateContextPath(bad); !errors.Is(err, playbook.ErrInvalidContextPath) {
			t.Errorf("ValidateContextPath(%q) = %v, want ErrInvalidContextPath", bad, err)
		}
	}

	if err := playbook.ValidateCommitSHA(testCommitSHA); err != nil {
		t.Fatalf("ValidateCommitSHA valid: %v", err)
	}
	for _, badSHA := range []string{"short", "0123456789ABCDEF0123456789abcdef01234567", "zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz"} {
		if err := playbook.ValidateCommitSHA(badSHA); !errors.Is(err, playbook.ErrInvalidCommitSHA) {
			t.Errorf("ValidateCommitSHA(%q) = %v, want ErrInvalidCommitSHA", badSHA, err)
		}
	}
}

func TestContextRefAndManifestParsing(t *testing.T) {
	ref, err := playbook.ParseContextRef("acme/widgets-read@" + testCommitSHA + ":.grain/context/plc/alpha")
	if err != nil {
		t.Fatalf("ParseContextRef: %v", err)
	}
	if ref.Repo != "acme/widgets-read" || ref.Rev != testCommitSHA || ref.Path != ".grain/context/plc/alpha" {
		t.Fatalf("parsed ref = %+v", ref)
	}
	formatted := playbook.FormatContextRef(ref)
	if formatted != "acme/widgets-read@"+testCommitSHA+":.grain/context/plc/alpha" {
		t.Fatalf("FormatContextRef = %q", formatted)
	}

	manifestYAML := []byte(`schema: 1
external_ref: "plc:proj/alpha"
spec_rev: "v3"
generated_by: "plc-studio"
files:
  - path: "prd.md"
    role: "prd"
  - path: "design.md"
    role: "design"
`)
	m, err := playbook.ParseContextManifest(manifestYAML)
	if err != nil {
		t.Fatalf("ParseContextManifest: %v", err)
	}
	if m.Schema != 1 || len(m.Files) != 2 {
		t.Fatalf("parsed manifest = %+v", m)
	}

	badYAML := []byte("schema: 2\nfiles:\n  - path: ../escape.md\n")
	if _, err := playbook.ParseContextManifest(badYAML); !errors.Is(err, playbook.ErrInvalidContextManifest) {
		t.Fatalf("expected ErrInvalidContextManifest, got %v", err)
	}
}

func TestPlaybookPlanningTargetAndReadPlacements(t *testing.T) {
	defaults := playbook.DefaultPlaybooks()
	if len(defaults) < 2 {
		t.Fatalf("expected at least 2 default playbooks, got %d", len(defaults))
	}

	// 1. Target placement keeps context in target repo without adding extra reads.
	targetTasks, err := playbook.Plan(defaults[0], playbook.PlanParams{
		ProjectSlug: "alpha-launch",
		ExternalRef: "plc:proj/alpha",
		TargetRepo:  "acme/widgets",
		Placement:   playbook.PlacementTarget,
		ContextRev:  testCommitSHA,
	})
	if err != nil {
		t.Fatalf("Plan(PlacementTarget): %v", err)
	}
	if len(targetTasks) != 2 {
		t.Fatalf("len(targetTasks) = %d, want 2", len(targetTasks))
	}
	first := targetTasks[0].Request
	if first.Context[0].Repo != "" || first.Context[0].Path != ".grain/context/plc/alpha-launch" || first.Context[0].Rev != testCommitSHA {
		t.Fatalf("first task context = %+v", first.Context)
	}
	if len(first.Reads) != 0 {
		t.Fatalf("unexpected reads on target placement: %v", first.Reads)
	}

	// 2. Read placement points Context.Repo at acme/widgets-read and adds it to Reads.
	readTasks, err := playbook.Plan(defaults[1], playbook.PlanParams{
		ProjectSlug: "alpha-launch",
		ExternalRef: "plc:proj/alpha",
		TargetRepo:  "acme/widgets",
		Placement:   playbook.PlacementRead,
		ContextRev:  testCommitSHA,
	})
	if err != nil {
		t.Fatalf("Plan(PlacementRead): %v", err)
	}
	if len(readTasks) != 1 {
		t.Fatalf("len(readTasks) = %d, want 1", len(readTasks))
	}
	readReq := readTasks[0].Request
	if readReq.Context[0].Repo != "acme/widgets-read" {
		t.Fatalf("Context[0].Repo = %q, want acme/widgets-read", readReq.Context[0].Repo)
	}
	if len(readReq.Reads) != 1 || readReq.Reads[0] != "acme/widgets-read" {
		t.Fatalf("Reads = %v, want [acme/widgets-read]", readReq.Reads)
	}
}
