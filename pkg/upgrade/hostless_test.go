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

package upgrade

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/installtrack"
)

// A unit still carrying -upgrade-image=<edition>/grain from the
// release-asset era is refused, never pulled from Docker Hub, and the
// refusal says where that name came from and what to set instead
// (grain/task-4084): it once told the operator to publish the image on
// the release, which no longer means anything.
func TestPullRefusesAHostlessConfiguredRepositoryWithTheFix(t *testing.T) {
	dir := t.TempDir()
	pulled := filepath.Join(dir, "pulled")
	const repo = "edition-local/grain"
	u := newUpgrader(t, Config{StatusFile: filepath.Join(dir, "status.json"), Image: &ImageConfig{
		Repository: repo,
		PullCmd:    []string{"sh", "-c", `printf '%s\n' "$1" >> ` + pulled, "sh"},
	}})
	p := newProgress(u.cfg.StatusFile, "main", Status{}, u.plan())

	err := u.pullImage(context.Background(), p, stepPull, repo+":main")
	if err == nil {
		t.Fatal("pullImage of a host-less ref succeeded, want a refusal")
	}
	msg := err.Error()
	for _, want := range []string{
		repo + ":main",
		"names no registry host",
		"Docker Hub",
		"-upgrade-image flag in this deployment's unit",
		"registry-qualified repository",
		"this build's default, " + installtrack.DefaultOSSGrainRepository + ", applies",
		"re-run the installer",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("refusal %q does not say %q", msg, want)
		}
	}
	if strings.Contains(msg, "release asset") || strings.Contains(msg, "publish it on the release") {
		t.Errorf("refusal %q still points at release assets", msg)
	}
	if _, err := os.Stat(pulled); !os.IsNotExist(err) {
		t.Errorf("PullCmd ran for a host-less ref: %q", readFile(t, pulled))
	}

	// The same thing, said on the pane before anybody presses Upgrade.
	target, ok := u.ImageTarget()
	if !ok {
		t.Fatal("ImageTarget() reported no image pipeline")
	}
	for _, want := range []string{repo, "-upgrade-image", installtrack.DefaultOSSGrainRepository, "re-run the installer"} {
		if !strings.Contains(target.RepositoryWarning, want) {
			t.Errorf("ImageTarget().RepositoryWarning = %q, want it to say %q", target.RepositoryWarning, want)
		}
	}
}

// A host-less ref that is not the configured repository -- a sandbox
// image a pulled build names -- is refused too, without blaming a flag
// that did not set it.
func TestPullRefusesAHostlessSandboxRef(t *testing.T) {
	dir := t.TempDir()
	u := newUpgrader(t, Config{StatusFile: filepath.Join(dir, "status.json"), Image: &ImageConfig{
		Repository: "docker.io/grainai/grain",
		PullCmd:    []string{"false"},
	}})
	p := newProgress(u.cfg.StatusFile, "main", Status{}, u.plan())
	err := u.pullImage(context.Background(), p, stepSandbox, "grain/guest:sha-111111111111")
	if err == nil || !strings.Contains(err.Error(), "names no registry host") || strings.Contains(err.Error(), "-upgrade-image") {
		t.Fatalf("pullImage(grain/guest:...) = %v, want a refusal naming no flag", err)
	}
	if target, _ := u.ImageTarget(); target.RepositoryWarning != "" {
		t.Errorf("RepositoryWarning = %q for a registry-qualified repository, want none", target.RepositoryWarning)
	}
}
