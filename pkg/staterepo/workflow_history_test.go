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

package staterepo_test

// A state repository's CI step is installed once and lives in somebody
// else's repository from then on, so what grain will and will not do to
// it afterwards is the whole of this file.
//
// grain decides by byte equality with a template, which is the only test
// that tells an operator's runner or added step from grain's own text
// without deciding what a workflow is allowed to say. Matching only the
// *current* template made that test say "not grain's" about every file
// grain had already installed the moment the template's comment block
// was reworded -- so those repositories kept whatever image they were
// installed with, forever, and no fix to this file ever reached a
// deployment that had one. The templates grain has retired are kept in
// workflow_history.go for that reason, and these are the properties that
// makes: an earlier grain's file is adopted, one somebody edited is
// still theirs, and an edit to the template that forgets to record the
// text it replaced fails here rather than silently stranding everybody.

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/staterepo"
)

// pre258Workflow is the workflow as a deployment running the grain of
// commit d7bf153e installed it: the template that was current from the
// day deployments started installing the file until commit b898473e,
// and so the one every state repository that already has a check is
// carrying. Written out here rather than reached for through the
// package's own unexported list, because "grain recognises its own
// list" is not the claim -- the claim is that grain recognises this
// text, which came out of git and not out of this build.
const pre258Workflow = `# Written by grain. Checks that grain can still load this repository.
#
# grain writes this file whenever it is not here -- on the sync after a
# merge dropped it, or the first sync after a deployment adopted this
# repository -- and never rewrites one that is. So an edit to it is
# safe: pin the image below, change the runner, add a step of your own,
# and grain leaves what you wrote alone. To have the check run somewhere
# else instead, put your own file here; to stop grain offering it at
# all, set "noWorkflow": true in the deployment's state-repo.json.
#
# This repository is a grain deployment's database, written out as text
# (see README.md), so a pull request against it is a change to what that
# deployment runs. The one question a reviewer cannot answer by reading
# the diff is whether the result will load: a file that is not valid
# JSON, a row missing a column the schema insists on, or a link naming a
# row that is no longer in the dump all read as perfectly plausible text
# and fail at import time -- which, without this step, means after the
# merge, on the deployment, in front of whoever is on call rather than
# whoever wrote the change.
#
# ` + "`grain state check`" + ` is that import, run against a database it throws
# away afterwards: the same single transaction, the same constraints, the
# same rollback on any inconsistency. It needs no credential, no data
# directory and no daemon, only a checkout -- which is why this whole job
# is a checkout and one ` + "`docker run`" + `.
#
# On pull requests only, deliberately. grain commits to this repository
# itself, on a timer, straight to its branch, every time its database
# changes; validating those pushes would spend a CI run each time a task
# changed state, on a dump grain had just written out of the very
# database the check would import it back into.
#
# The image is grain's own published container -- its entrypoint is the
# grain binary, and the package is public, so this step needs no
# credential and no secret. It does have to be a build that knows the
# same schema as the deployment: ` + "`grain state check`" + ` refuses a dump
# stamped with any other, so a failure reading "repository is at schema
# N, this build knows M" means this tag and the deployment have drifted
# apart. Point IMAGE at the tag that deployment runs --
# ` + "`grain state status`" + ` on its host prints both numbers.
name: grain state check

on:
  pull_request:
  # So it can be run by hand as well: after changing IMAGE below, or
  # after the deployment has been upgraded.
  workflow_dispatch:

permissions:
  contents: read

concurrency:
  group: ${{ github.workflow }}-${{ github.ref }}
  cancel-in-progress: true

jobs:
  check:
    name: grain can load this
    runs-on: ubuntu-latest

    steps:
      - uses: actions/checkout@v4

      - name: grain state check
        env:
          IMAGE: "@IMAGE@"
        run: |
          set -euo pipefail
          # A repository that has been formatted but that no deployment
          # has seeded yet holds no dump at all, and there is nothing to
          # validate until one arrives. ` + "`grain state check`" + ` calls that an
          # error, and for a CI step pointed at the wrong directory it is
          # one, so the empty case is answered here -- where a reader of
          # this file can see it -- rather than by making the check itself
          # laxer for everyone.
          if [ ! -d tables ]; then
            echo "No tables/ directory here: no grain deployment has written its database"
            echo "to this repository yet, so there is nothing to check."
            exit 0
          fi
          docker run --rm -v "$PWD:/state:ro" "$IMAGE" state check /state
`

// pre258 renders that template the way the grain of the day would have.
func pre258(image string) string {
	return strings.ReplaceAll(pre258Workflow, "@IMAGE@", image)
}

// The repositories this change exists for: a check installed before
// commit b898473e, pinned to the tag of the day, in a deployment that
// has since been upgraded. grain has to recognise the file as its own,
// bring it up to the wording this build would have written, and point it
// at the image this deployment runs -- and then have nothing left to do.
func TestAWorkflowAnEarlierGrainWroteIsAdopted(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	const was = staterepo.DefaultCheckImage
	writeWorkflow(t, dir, pre258(was))

	if got, ok := staterepo.RenderedImage([]byte(pre258(was))); !ok || got != was {
		t.Fatalf("grain does not recognise a workflow it wrote itself: %q, %v", got, ok)
	}
	if staterepo.RenderedByThisBuild([]byte(pre258(was))) {
		t.Error("an earlier grain's rendering is read as this build's own")
	}

	const now = "ghcr.io/bwsalmon/grain/grain:sha-abc1234"
	wrote, err := staterepo.EnsureWorkflow(dir, now, false)
	if err != nil {
		t.Fatalf("ensuring the workflow: %v", err)
	}
	if !wrote {
		t.Fatal("a check an earlier grain installed was left where it was, so no fix to this file " +
			"will ever reach the repository carrying it")
	}
	if got, want := read(t, workflowPath(dir)), string(staterepo.Workflow(now)); got != want {
		t.Errorf("the adopted workflow is not what this build writes:\n%s", got)
	}

	// Adopted once. A file grain rewrote on every tick would be a commit
	// per sync in somebody else's repository.
	if wrote, err := staterepo.EnsureWorkflow(dir, now, false); err != nil || wrote {
		t.Errorf("the adopted workflow was rewritten a second time (%v, %v)", wrote, err)
	}
}

// The other half, and the one that has to keep holding: widening what
// grain recognises must not widen what it overwrites. An earlier grain's
// text with one character of somebody's own in it is theirs, exactly as
// this build's text with one character of somebody's own in it is.
func TestAnEditedWorkflowAnEarlierGrainWroteIsStillLeftAlone(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		body string
	}{
		{
			name: "a runner of their own",
			body: strings.Replace(pre258(staterepo.DefaultCheckImage),
				"runs-on: ubuntu-latest", "runs-on: self-hosted", 1),
		},
		{
			name: "a step of their own",
			body: pre258(staterepo.DefaultCheckImage) + "\n      - run: echo done\n",
		},
		{
			name: "an image that is not one token",
			body: strings.Replace(pre258(staterepo.DefaultCheckImage),
				`IMAGE: "`+staterepo.DefaultCheckImage+`"`, `IMAGE: "" # ours`, 1),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, ok := staterepo.RenderedImage([]byte(tc.body)); ok {
				t.Error("a workflow somebody edited is read as grain's own")
			}
			dir := t.TempDir()
			writeWorkflow(t, dir, tc.body)
			wrote, err := staterepo.EnsureWorkflow(dir, "ghcr.io/bwsalmon/grain/grain:sha-abc1234", false)
			if err != nil {
				t.Fatalf("ensuring the workflow: %v", err)
			}
			if wrote {
				t.Error("grain rewrote a workflow somebody had edited")
			}
			if got := read(t, workflowPath(dir)); got != tc.body {
				t.Errorf("an edited workflow was overwritten:\n%s", got)
			}
		})
	}
}

// The tripwire. Editing the template in format.go without recording the
// text it replaced is a change with no symptom in any test and no
// symptom on any deployment either: everything keeps working here, and
// every repository already carrying the old text quietly stops being
// maintained, with nothing to say so until somebody wonders years later
// why a check is still running a build from before the upgrade.
//
// So the current template is pinned by its digest. If this test fails
// because you meant to change the template, the fix is two steps and the
// first one is the one worth having a test for:
//
//  1. Copy the template *as it was before your change* into
//     workflow_history.go as a new earlierWorkflowN, and add it to the
//     front of earlierWorkflows.
//  2. Put the digest this test prints below.
func TestTheCurrentTemplateIsRecorded(t *testing.T) {
	t.Parallel()
	// The digest of the current template, rendered with a fixed image so
	// that what is pinned is the text and not this build's own stamp.
	const want = "e8252ba38f3e5668236c294e18178c80bb5aff22a39b508ef080eaa1fdf9c73d"
	sum := sha256.Sum256(staterepo.Workflow("@IMAGE@"))
	got := hex.EncodeToString(sum[:])
	if got == want {
		return
	}
	t.Errorf("the workflow template has changed.\n"+
		"Copy the version of it you replaced into workflow_history.go as a new\n"+
		"earlierWorkflowN, add it to the front of earlierWorkflows, and change the\n"+
		"digest in this test to:\n\n\t%s\n\n"+
		"Skipping the first step strands every state repository that already carries\n"+
		"the old text: grain stops recognising the file as its own and never touches\n"+
		"it again, image line included.", got)
}

func writeWorkflow(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(workflowPath(dir)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(workflowPath(dir), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
