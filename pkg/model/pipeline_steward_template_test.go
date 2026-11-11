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
// titled so they hold the next candidate, fixes on main only (there is no
// convergence branch), daily-cap drops that are not red, and the escalation
// that does not stop the train -- and that the reconciler's half is not its
// to do.
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
		"fixes land on `main` only",
		"`Discard-Reason: daily-cap`",
		"`Discard-Reason: held-over`",
		"**Escalate after 3 red candidates.**",
		"`pipeline.release-escalated:vX.Y.Z`",
		"The train does not stop",
		"`rc.4`, `rc.5`",
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
	for _, retired := range []string{"`branch: release/vX.Y.Z`", "Backported-from", "cuts no more"} {
		if strings.Contains(body, retired) {
			t.Errorf("the steward's prompt still says %q, which the daily train retired", retired)
		}
	}
}

// ReleaseMaxRCAttempts is the train's own cap, so the escalation the
// reconciler raises is at the threshold the train escalates at.
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

// The steward is told to ground every claim in this wake's reads, to
// re-read before restating memory, to set apart what it cannot ground,
// and to fail its own report on grain's check and retry once.
func TestPipelineStewardIsToldToGroundEveryClaimInThisWake(t *testing.T) {
	t.Parallel()
	body := pipelineStewardBody(t)
	for _, want := range []string{
		"run `grain task read <id>` in this wake",
		"are memory, not records",
		"A summary of a summary is not a source.",
		"*in this wake*",
		"goes under a separate `" + model.PipelineStewardUnverifiedHeading + "` heading",
		"Run `grain report check --file <f>`.",
		"If it fails, your report fails",
		"Retry once only",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the steward's prompt does not say %q", want)
		}
	}
}
