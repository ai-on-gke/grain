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
	"os"
	"os/exec"
	"strings"
	"testing"
)

// The registry image (registry.design.md step 9) is published "beside
// grainai/grain, grainai/sandbox, and grainai/sandbox-k8s" -- and that
// includes the releases, not only every merge's sha- and branch tags.
// `promote`, `rc` and `pending` used to handle only the three images they
// were written for, so a promoted release had no registry:vX.Y.Z, no
// registry:latest and no rc tag (grain task 3295). These run the real
// script against a release whose own tree builds a registry image, and
// one whose tree predates it.

// registryReleaseFiles are what a release's tree carries once its
// release job builds the registry image: the image's Dockerfile, and a
// scripts/release-images.sh with build_registry_image.
var registryReleaseFiles = map[string]string{
	"Dockerfile.registry":       "FROM scratch\nARG RELEASE=\nARG MAIN_GRAIN_REGISTRY=./cmd/grain-registry\n",
	"scripts/release-images.sh": "build_registry_image() {\n  :\n}\n",
}

func registryReleaseRepo(t *testing.T) (string, map[string]string) {
	t.Helper()
	return releaseRepoWithFiles(t, postSplitDockerfile, true, registryReleaseFiles)
}

// A promotion rebuilds the registry image from the release's own
// Dockerfile.registry with the release stamped, publishes and signs it
// before any moving tag names it, and only then moves latest and its
// sha- tag onto it.
func TestPromoteRebuildsTheRegistryImageWithTheReleaseStamped(t *testing.T) {
	remote, commits := registryReleaseRepo(t)
	short := commits["released"][:7]

	out, docker, err := promoteRun(t, remote, []string{"--dry-run", "promote"}, nil)
	if err != nil {
		t.Fatalf("promote --dry-run failed: %v\n%s", err, out)
	}
	containsRun(t, out, "and rebuild docker.io/grainai/registry:sha-"+short+" from "+commits["released"]+
		" (./cmd/grain-registry) with RELEASE=v1.5.0 as v1.5.0 and latest, unless docker.io/grainai/registry:v1.5.0 already reports v1.5.0")
	if docker != "" {
		t.Errorf("a dry run asked docker for something:\n%s", docker)
	}

	out, docker, err = promoteRun(t, remote, []string{"promote"}, publishEnv(stubAnswers(t, short, "", "v1.5.0")))
	if err != nil {
		t.Fatalf("promote failed: %v\n%s\ndocker was asked:\n%s", err, out, docker)
	}
	containsRun(t, docker, "buildx imagetools inspect docker.io/grainai/registry:sha-"+short+"\n")
	containsRun(t, docker, "--build-arg MAIN_GRAIN_REGISTRY=./cmd/grain-registry ")
	containsRun(t, docker, "--build-arg GIT_REVISION="+commits["released"]+" ")
	containsRun(t, docker, "--build-arg RELEASE=v1.5.0 --label org.opencontainers.image.version=v1.5.0 -t docker.io/grainai/registry:v1.5.0 -f Dockerfile.registry .")
	containsRun(t, docker, "run --rm docker.io/grainai/registry:v1.5.0 version --json")
	// Signed (here: told it goes unsigned, there being no key) once it is
	// pushed, by the digest that push gave it.
	containsRun(t, out, "docker.io/grainai/registry:v1.5.0 is published unsigned")

	push := strings.Index(docker, "push docker.io/grainai/registry:v1.5.0")
	if push < 0 {
		t.Fatalf("the rebuilt registry image was never pushed:\n%s", docker)
	}
	for _, moving := range []string{
		"buildx imagetools create --tag docker.io/grainai/registry:latest docker.io/grainai/registry:v1.5.0",
		"buildx imagetools create --tag docker.io/grainai/registry:sha-" + short + " docker.io/grainai/registry:v1.5.0",
		retagLine("sandbox", short, "latest"),
	} {
		at := strings.Index(docker, moving)
		if at < 0 {
			t.Errorf("promote did not %q:\n%s", moving, docker)
		} else if at < push {
			t.Errorf("promote ran %q before it pushed the rebuilt registry image:\n%s", moving, docker)
		}
	}
	containsRun(t, out, "docker.io/grainai/registry:v1.5.0 is "+commits["released"]+" stamped v1.5.0")
}

// The registry image is in the pre-pass: a release whose
// registry:sha-<short> was never published moves nothing at all.
func TestPromoteOfAReleaseMissingItsRegistryImageMovesNothing(t *testing.T) {
	remote, commits := registryReleaseRepo(t)
	short := commits["released"][:7]
	env := publishEnv(stubAnswers(t, short, "", "v1.5.0"))
	env["DOCKER_STUB_MISSING"] = "docker.io/grainai/registry:sha-" + short
	out, docker, err := promoteRun(t, remote, []string{"promote"}, env)
	if err == nil {
		t.Fatalf("promote of a release missing its registry image succeeded:\n%s\ndocker was asked:\n%s", out, docker)
	}
	containsRun(t, out, "never published docker.io/grainai/registry:sha-"+short)
	if strings.Contains(docker, "imagetools create") || strings.Contains(docker, "\nbuild ") || strings.HasPrefix(docker, "build ") ||
		strings.Contains(docker, "\npush ") {
		t.Errorf("promote of a release missing its registry image built or moved something:\n%s", docker)
	}
}

// A registry rebuild that reports an edition is not core grain-registry,
// and moves nothing -- the check build_registry_image makes of every
// merge's build.
func TestPromoteOfARegistryRebuildReportingAnEditionMovesNothing(t *testing.T) {
	remote, commits := registryReleaseRepo(t)
	short := commits["released"][:7]
	answers := stubAnswers(t, short, "", "v1.5.0")
	prependAnswer(t, answers, `  "run --rm docker.io/grainai/registry:v1.5.0 version --json") printf '{"release":"v1.5.0","edition":"google"}\n' ;;`)
	out, docker, err := promoteRun(t, remote, []string{"promote"}, publishEnv(answers))
	if err == nil {
		t.Fatalf("promote of a registry rebuild reporting an edition succeeded:\n%s\ndocker was asked:\n%s", out, docker)
	}
	containsRun(t, out, "docker.io/grainai/registry:v1.5.0 was built from ./cmd/grain-registry, core grain, but reports an edition")
	if strings.Contains(docker, "push docker.io/grainai/registry:v1.5.0") || strings.Contains(docker, ":latest") {
		t.Errorf("promote published a registry rebuild reporting an edition, or moved latest:\n%s", docker)
	}
}

// A release cut before its release job built a registry image has none
// to promote: promoting (or rolling back to) it neither asks for one nor
// builds one.
func TestPromoteOfAReleaseThatPredatesTheRegistryImageLeavesItAlone(t *testing.T) {
	remote, commits := releaseRepoWithDockerfile(t, postSplitDockerfile)
	short := commits["released"][:7]

	out, _, err := promoteRun(t, remote, []string{"--dry-run", "promote"}, nil)
	if err != nil {
		t.Fatalf("promote --dry-run failed: %v\n%s", err, out)
	}
	containsRun(t, out, "v1.5.0's tree publishes no registry image")

	out, docker, err := promoteRun(t, remote, []string{"promote"}, publishEnv(stubAnswers(t, short, "", "v1.5.0")))
	if err != nil {
		t.Fatalf("promote failed: %v\n%s\ndocker was asked:\n%s", err, out, docker)
	}
	if strings.Contains(docker, "grainai/registry") {
		t.Errorf("promote of a release that predates the registry image touched it:\n%s", docker)
	}
	containsRun(t, out, "v1.5.0's tree publishes no registry image, so none was published for it")
}

// The rc train publishes the registry image as the rc and moves its
// candidate and rc channel tags with the others'.
func TestReleaseImagesRCPublishesTheRegistryImage(t *testing.T) {
	remote, commits := releaseRepoWithFiles(t, postSplitDockerfile, true, registryReleaseFiles)
	cmd := exec.Command("git", "branch", "candidate", commits["candidate"])
	cmd.Dir = remote
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git branch candidate: %v\n%s", err, out)
	}
	short := commits["candidate"][:7]

	out, docker, err := promoteRun(t, remote, []string{"--dry-run", "rc"}, nil)
	if err != nil {
		t.Fatalf("rc --dry-run failed: %v\n%s", err, out)
	}
	containsRun(t, out, "and rebuild docker.io/grainai/registry:sha-"+short+" from "+commits["candidate"]+
		" (./cmd/grain-registry) with RELEASE=v1.6.0-rc.1 as v1.6.0-rc.1, candidate rc")
	if docker != "" {
		t.Errorf("a dry run asked docker for something:\n%s", docker)
	}

	out, docker, err = promoteRun(t, remote, []string{"rc"}, publishEnv(stubAnswers(t, short, "", "v1.6.0-rc.1")))
	if err != nil {
		t.Fatalf("rc failed: %v\n%s\ndocker was asked:\n%s", err, out, docker)
	}
	containsRun(t, docker, "buildx imagetools inspect docker.io/grainai/registry:sha-"+short+"\n")
	containsRun(t, docker, "--build-arg RELEASE=v1.6.0-rc.1 --label org.opencontainers.image.version=v1.6.0-rc.1 -t docker.io/grainai/registry:v1.6.0-rc.1 -f Dockerfile.registry .")
	push := strings.Index(docker, "push docker.io/grainai/registry:v1.6.0-rc.1")
	if push < 0 {
		t.Fatalf("the rebuilt registry image was never pushed:\n%s", docker)
	}
	for _, tag := range []string{"candidate", "rc", "sha-" + short} {
		moving := "buildx imagetools create --tag docker.io/grainai/registry:" + tag + " docker.io/grainai/registry:v1.6.0-rc.1"
		at := strings.Index(docker, moving)
		if at < 0 {
			t.Errorf("rc did not %q:\n%s", moving, docker)
		} else if at < push {
			t.Errorf("rc ran %q before it pushed the rebuilt registry image:\n%s", moving, docker)
		}
	}
	if strings.Contains(docker, "registry:latest") {
		t.Errorf("publishing an rc moved registry:latest:\n%s", docker)
	}
}

// prependAnswer puts the case arm line first in the stub answers at path,
// so it answers ahead of stubAnswers' own arms.
func prependAnswer(t *testing.T, path, line string) {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the stub's answers: %v", err)
	}
	script := strings.Replace(string(body), "case \"$*\" in\n", "case \"$*\" in\n"+line+"\n", 1)
	if err := os.WriteFile(path, []byte(script), 0o644); err != nil {
		t.Fatalf("writing the stub's answers: %v", err)
	}
}
