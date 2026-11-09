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

package model_test

import (
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
)

func pipelineStewardBody(t *testing.T) string {
	t.Helper()
	for _, tmpl := range model.SystemTemplates(model.RepoRef{}) {
		if tmpl.ID == model.PipelineStewardTemplateID {
			return tmpl.Body
		}
	}
	t.Fatalf("no %s among the system templates", model.PipelineStewardTemplateID)
	return ""
}

// The steward is told its half of a red release candidate
// (design/playbook/releases.playbook.md §5 and §12): triage, blockers
// titled so they hold the next candidate, rc.3's convergence branch and
// the escalation after the last attempt -- and that the reconciler's half
// is not its to do.
func TestPipelineStewardIsToldHowToTriageARedReleaseCandidate(t *testing.T) {
	t.Parallel()
	body := pipelineStewardBody(t)
	for _, want := range []string{
		"`pipeline.rc-failed:<rc_tag>`",
		"**Triage `<floor>..<rc_sha>`.**",
		"start the title with `" + model.RCBlockerMarker("<rc_tag>") + "` exactly",
		"`at_front: true`",
		"check run `" + model.ReleaseHoldCheckName + "` in progress",
		"while triage is pending",
		"name `<rc_tag>` in your closing report",
		"`rc.(N+1)` is not cut",
		"`release/vX.Y.Z`",
		"`branch: release/vX.Y.Z`",
		"`Backported-from: <main_sha>`",
		"**Escalate after 3 red candidates.**",
		"`pipeline.release-escalated:vX.Y.Z`",
		"`agent.<your task id>:release-escalated:vX.Y.Z`",
		"`pipeline.flake-budget:<repo>:<item>`",
		"`pipeline.release-infra-stalled:<rc_tag>`",
		"never cut, tag, approve or promote a release",
		"`propose_task` for a revert or a red release candidate's blocker",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the steward's prompt does not say %q", want)
		}
	}
}

// ReleaseMaxRCAttempts is the train's own cap, so the escalation the
// reconciler raises is the one the train stops at.
func TestReleaseMaxRCAttemptsIsTheTrainsOwn(t *testing.T) {
	t.Parallel()
	conf, err := os.ReadFile("../../scripts/release.conf")
	if err != nil {
		t.Fatal(err)
	}
	want := "\nRELEASE_MAX_RC_ATTEMPTS=" + strconv.Itoa(model.ReleaseMaxRCAttempts) + "\n"
	if !strings.Contains(string(conf), want) {
		t.Errorf("scripts/release.conf does not set %q", strings.TrimSpace(want))
	}
}

func TestReleaseFloorAndPreviousCandidate(t *testing.T) {
	t.Parallel()
	tags := map[string]string{
		"v1.2.0": "a2", "v1.3.0": "a3", "v1.10.0": "a10", "v1.4.0-pending": "p",
		"v1.4.0-rc.1": "r1", "v1.4.0-rc.1-discarded": "r1", "v1.4.0-rc.2": "r2", "v1.3.0-rc.4": "o4",
	}
	rc, _ := model.ParseRCTag("v1.4.0-rc.3")
	if name, sha, ok := model.ReleaseFloor(tags, rc); !ok || name != "v1.3.0" || sha != "a3" {
		t.Errorf("ReleaseFloor = %s %s %v, want v1.3.0 a3", name, sha, ok)
	}
	if name, sha, ok := model.PreviousCandidate(tags, rc); !ok || name != "v1.4.0-rc.2" || sha != "r2" {
		t.Errorf("PreviousCandidate = %s %s %v, want v1.4.0-rc.2 r2", name, sha, ok)
	}
	first, _ := model.ParseRCTag("v1.0.0-rc.1")
	if _, _, ok := model.ReleaseFloor(tags, first); ok {
		t.Error("ReleaseFloor found a release before v1.0.0")
	}
	if _, _, ok := model.PreviousCandidate(tags, first); ok {
		t.Error("PreviousCandidate found a candidate before rc.1")
	}
}
