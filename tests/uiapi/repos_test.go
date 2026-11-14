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

package uiapi

import (
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/demo/demotest"
)

// ported from ui/e2e/base-branch-existence.spec.js:169 (and the server
// half of :32, the same refusal behind the New task form).
func TestClassic_BaseBranchExistence_APIRefusesAMissingBaseAndFilesAPresentOne(t *testing.T) {
	t.Parallel()
	srv := demotest.Start(t, demotest.Options{Args: []string{"-github-sim"}})
	refused := srv.Do(t, http.MethodPost, "/api/tasks", map[string]any{
		"title":       fmt.Sprintf("Missing base over the API %d", time.Now().UnixNano()),
		"description": "Filed straight at the API.",
		"repo":        srv.Seed.Repo,
		"base":        "nope",
	}).WantStatus(t, http.StatusBadRequest)
	if want := `base "nope" does not exist on acme/widgets`; !strings.Contains(refused.Error(), want) {
		t.Errorf("refusal = %q, want it to contain %q", refused.Error(), want)
	}

	srv.Do(t, http.MethodPost, "/api/tasks", map[string]any{
		"title":       fmt.Sprintf("Present base over the API %d", time.Now().UnixNano()),
		"description": "Filed straight at the API.",
		"repo":        srv.Seed.Repo,
		"base":        "release/1.0",
	}).WantStatus(t, http.StatusCreated)
}

// ported from ui/e2e/release_name_validation_walkthrough.spec.js:64.
// The classic test relied on the one before it creating "v3.0-lock"
// through the pane; this one creates it itself.
func TestClassic_ReleaseNameValidation_ADirectPOSTGetsTheSameAnswers(t *testing.T) {
	t.Parallel()
	srv := demotest.Start(t, demotest.Options{})
	const releases = "/api/repos/acme/widgets/releases"
	refused := []string{"v3.0..x", "v3.lock", "v3."}
	rule := regexp.MustCompile(`cannot contain "\.\.", end in "\." or "\.lock"`)

	for _, name := range refused {
		res := srv.Do(t, http.MethodPost, releases, map[string]any{"name": name}).WantStatus(t, http.StatusBadRequest)
		if !rule.MatchString(res.Error()) {
			t.Errorf("POST %q: refusal %q does not spell out the rule", name, res.Error())
		}
	}
	// A name that only looks like one is created; a second release by
	// that name is refused for being in use, not for its name.
	srv.Do(t, http.MethodPost, releases, map[string]any{"name": "v3.0-lock"}).WantOK(t)
	again := srv.Do(t, http.MethodPost, releases, map[string]any{"name": "v3.0-lock"}).WantStatus(t, http.StatusBadRequest)
	if !strings.Contains(again.Error(), "already has an unmerged") {
		t.Errorf("second v3.0-lock refused with %q, want it in use", again.Error())
	}

	var list []struct{ Name string }
	srv.Do(t, http.MethodGet, releases, nil).WantOK(t).Items(t, &list)
	var names []string
	for _, r := range list {
		names = append(names, r.Name)
	}
	if !slices.Contains(names, "v3.0-lock") {
		t.Errorf("releases %v do not include v3.0-lock", names)
	}
	for _, name := range refused {
		if slices.Contains(names, name) {
			t.Errorf("releases %v include the refused %q", names, name)
		}
	}
}

// ported from ui/e2e/standing-work-switch-walkthrough.spec.js:178, with
// its step 3's switch turned off over the API the pane calls.
func TestClassic_StandingWorkSwitch_AHandFiledTaskIsStillAcceptedWhileStandingWorkIsOff(t *testing.T) {
	t.Parallel()
	srv := demotest.Start(t, demotest.Options{Args: []string{
		"-github-sim", "-fake-ci", "-base-branch-ci", "-scheduled-workflows", "-schedules", "-reconcile", "2s",
	}})
	const ownership = "/api/repos/acme/widgets/work-ownership"
	srv.Do(t, http.MethodPut, ownership, map[string]any{"default": "off"}).WantOK(t)
	var off struct {
		Default     string `json:"default"`
		DefaultRuns bool   `json:"defaultRuns"`
		Items       []struct {
			Item string `json:"item"`
			Runs bool   `json:"runs"`
		} `json:"items"`
	}
	srv.Do(t, http.MethodGet, ownership, nil).WantOK(t).JSON(t, &off)
	if off.Default != "off" || off.DefaultRuns {
		t.Fatalf("work ownership after turning it off = %+v", off)
	}
	for _, item := range off.Items {
		if item.Runs {
			t.Errorf("%s still runs with standing work off", item.Item)
		}
	}

	id := srv.Do(t, http.MethodPost, "/api/tasks", map[string]any{
		"title":       "Hand-filed while standing work is off",
		"description": "walk",
		"repo":        srv.Seed.Repo,
	}).WantOK(t).ID(t)
	var all []struct{ ID string }
	srv.Do(t, http.MethodGet, "/api/tasks", nil).WantOK(t).Items(t, &all)
	if !slices.ContainsFunc(all, func(task struct{ ID string }) bool { return task.ID == id }) {
		t.Errorf("hand-filed task %s is not listed", id)
	}
}
