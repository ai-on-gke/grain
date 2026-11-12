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

package deploy

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// nightly.yml's security-attacks job runs the manifest's completeness
// check and every in-process path, the real-sandbox jobs run and judge
// every live path, and the release train requires the job by the name
// GitHub reports it under.
func TestNightlyRunsEverySecurityAttackPath(t *testing.T) {
	var manifest struct {
		Paths []struct {
			Name  string   `yaml:"name"`
			Mode  string   `yaml:"mode"`
			Tests []string `yaml:"tests"`
		} `yaml:"paths"`
	}
	if err := yaml.Unmarshal([]byte(read(t, "tests", "security", "attacks.yaml")), &manifest); err != nil {
		t.Fatalf("tests/security/attacks.yaml does not parse: %v", err)
	}
	wf := parseNightly(t)
	jobRun := func(id string) string {
		job, ok := wf.Jobs[id]
		if !ok {
			t.Fatalf("nightly.yml has no %s job", id)
		}
		var b strings.Builder
		for _, s := range job.Steps {
			b.WriteString(s.Run + "\n")
		}
		return b.String()
	}

	sa := jobRun("security-attacks")
	for _, want := range []string{
		"go run ./cmd/security-attacks check",
		"go run ./cmd/security-attacks run ",
		`-summary "$GITHUB_STEP_SUMMARY"`,
		"-failed /tmp/security-attacks.failed",
	} {
		if !strings.Contains(sa, want) {
			t.Errorf("security-attacks does not run %q", want)
		}
	}
	steps := wf.Jobs["security-attacks"].Steps
	if last := steps[len(steps)-1]; !strings.Contains(last.Run, "/tmp/security-attacks.failed") {
		t.Errorf("security-attacks' last step is %q; the failed paths' names have to end the log, the tail grain hands nightly-ci-review", last.Name)
	}

	live := map[string]string{"live-vm": "kvm-sandboxes", "live-k8s": "real-kubernetes"}
	modes := map[string]bool{}
	for _, p := range manifest.Paths {
		job, ok := live[p.Mode]
		if !ok {
			continue
		}
		modes[p.Mode] = true
		run := jobRun(job)
		for _, test := range p.Tests {
			if !strings.Contains(run, test) {
				t.Errorf("attack path %s's %s is not in nightly's %s job, which runs %s paths", p.Name, test, job, p.Mode)
			}
		}
	}
	for mode, job := range live {
		if !modes[mode] {
			t.Errorf("tests/security/attacks.yaml has no %s path, so %s judges nothing", mode, job)
		}
		if want := "go run ./cmd/security-attacks judge -mode " + mode + " "; !strings.Contains(jobRun(job), want) {
			t.Errorf("nightly's %s job does not judge its attack paths (%q)", job, want)
		}
	}

	gate := read(t, "scripts", "candidate-gate.sh")
	for _, id := range stage1Jobs {
		if !strings.Contains(gate, "\n"+id+"=") && !strings.Contains(gate, `-`+id+"=") {
			t.Errorf("scripts/candidate-gate.sh's CHECK_QUALIFICATION_JOBS does not require Stage 1 job %s", id)
		}
	}
	if !strings.Contains(gate, "\nsecurity-attacks=security attack paths") {
		t.Errorf("scripts/candidate-gate.sh does not require security-attacks by the name nightly.yml gives it (security attack paths)")
	}
}
