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
	"path/filepath"
	"strings"
	"testing"
)

// Releases cut before the switch to Docker Hub (#739) -- v3.1.0 among
// them -- had release jobs that published to ghcr.io/bwsalmon/grain/*
// alone, under the old leaf names (guest, k8s-sandbox). Promoting one to
// docker.io/grainai/* failed naming every sha- image as never published
// (task 3249), and re-running such a release would only publish to GHCR
// again. So promotion copies what Docker Hub lacks from GHCR by digest,
// refusing a copy whose digest differs, and then promotes exactly as it
// does any other release.

// legacyAnswers is a docker stub's answers for a registry pair in which
// GHCR holds the release's images (when ghcrHas) and Docker Hub holds a
// tag only once this run has written it (`imagetools create --tag`).
// Every digest asked for is sha256:1234 -- except a Docker Hub copy's,
// which is copiedDigest. The GHCR grain image is the stamped build of
// release v1.5.0 from commit.
func legacyAnswers(t *testing.T, commit string, ghcrHas bool, copiedDigest string) string {
	t.Helper()
	has := "exit 0"
	if !ghcrHas {
		has = "exit 1"
	}
	script := `written() { grep -qF -- "--tag $1 " "$DOCKER_STUB_LOG"; }
case "$*" in
  "buildx imagetools inspect ghcr.io/"*" --format {{.Manifest.Digest}}")
    ` + strings.Replace(has, "exit 0", "echo sha256:1234; exit 0", 1) + ` ;;
  "buildx imagetools inspect ghcr.io/bwsalmon/grain/grain:v1.5.0 --format "*revision*)
    ` + strings.Replace(has, "exit 0", "echo "+commit+"; exit 0", 1) + ` ;;
  "buildx imagetools inspect ghcr.io/bwsalmon/grain/grain:v1.5.0 --format "*version*)
    ` + strings.Replace(has, "exit 0", "echo v1.5.0; exit 0", 1) + ` ;;
  "buildx imagetools inspect ghcr.io/"*) ` + has + ` ;;
  "pull -q ghcr.io/"*) ` + has + ` ;;
  "buildx imagetools inspect docker.io/grainai/"*" --format {{.Manifest.Digest}}")
    written "$4" || exit 1; echo ` + copiedDigest + `; exit 0 ;;
  "buildx imagetools inspect docker.io/grainai/grain:v1.5.0 --format "*revision*)
    written "$4" || exit 1; echo ` + commit + `; exit 0 ;;
  "buildx imagetools inspect docker.io/grainai/grain:v1.5.0 --format "*version*)
    written "$4" || exit 1; echo v1.5.0; exit 0 ;;
  "buildx imagetools inspect docker.io/grainai/"*) written "$4" || exit 1 ;;
  "pull -q docker.io/grainai/"*) written "$3" || exit 1 ;;
esac
`
	path := filepath.Join(t.TempDir(), "answers.sh")
	if err := os.WriteFile(path, []byte(script), 0o644); err != nil {
		t.Fatalf("writing the stub's answers: %v", err)
	}
	return path
}

// The promotion of a pre-Docker-Hub release copies its sandbox images,
// their bases and its stamped grain image from GHCR by digest, rebuilds
// nothing, and then moves vX.Y.Z and latest onto them.
func TestPromoteOfAPreDockerHubReleaseCopiesItsGHCRImagesByDigest(t *testing.T) {
	remote, commits := preDockerHubReleaseRepo(t)
	short := commits["released"][:7]
	out, docker, err := promoteRun(t, remote, []string{"promote"}, publishEnv(legacyAnswers(t, commits["released"], true, "sha256:1234")))
	if err != nil {
		t.Fatalf("promote failed: %v\n%s\ndocker was asked:\n%s", err, out, docker)
	}
	for _, copy := range [][2]string{
		{"sandbox:sha-" + short, "guest"},
		{"sandbox-k8s:sha-" + short, "k8s-sandbox"},
		{"sandbox-base:sha-" + short, "guest-base"},
		{"sandbox-k8s-base:sha-" + short, "k8s-sandbox-base"},
		{"grain:v1.5.0", "grain"},
	} {
		containsRun(t, docker, "buildx imagetools create --tag docker.io/grainai/"+copy[0]+" ghcr.io/bwsalmon/grain/"+copy[1]+"@sha256:1234")
	}
	containsRun(t, out, "copied ghcr.io/bwsalmon/grain/guest:sha-"+short+" to docker.io/grainai/sandbox:sha-"+short+" by digest (sha256:1234)")
	containsRun(t, out, "docker.io/grainai/grain:v1.5.0 already reports v1.5.0; nothing to rebuild")
	if strings.Contains(docker, "\nbuild ") || strings.HasPrefix(docker, "build ") {
		t.Errorf("promote rebuilt the grain image although GHCR's stamped build was copied:\n%s", docker)
	}
	containsRun(t, docker, retagLine("sandbox", short, "v1.5.0"))
	containsRun(t, docker, retagLine("sandbox-k8s", short, "latest"))
	containsRun(t, docker, "buildx imagetools create --tag docker.io/grainai/grain:latest docker.io/grainai/grain:v1.5.0")
}

// A copy whose digest is not its source's is not the image that was
// tested: the promotion stops there and moves no release tag.
func TestPromoteOfAPreDockerHubReleaseRefusesACopyWithAnotherDigest(t *testing.T) {
	remote, commits := preDockerHubReleaseRepo(t)
	out, docker, err := promoteRun(t, remote, []string{"promote"}, publishEnv(legacyAnswers(t, commits["released"], true, "sha256:5678")))
	if err == nil {
		t.Fatalf("promote succeeded although the copy's digest differs:\n%s", out)
	}
	containsRun(t, out, "the copy is not the image that was tested")
	if strings.Contains(docker, ":v1.5.0 docker.io/") || strings.Contains(docker, "--tag docker.io/grainai/sandbox:latest") {
		t.Errorf("promote moved a release tag after a bad copy:\n%s", docker)
	}
}

// A pre-Docker-Hub release that GHCR does not have either says so, and
// does not send the operator to re-run a release that publishes to GHCR.
func TestPromoteOfAPreDockerHubReleaseMissingFromGHCRSaysWhy(t *testing.T) {
	remote, commits := preDockerHubReleaseRepo(t)
	short := commits["released"][:7]
	out, docker, err := promoteRun(t, remote, []string{"promote"}, publishEnv(legacyAnswers(t, commits["released"], false, "sha256:1234")))
	if err == nil {
		t.Fatalf("promote succeeded with no images in either registry:\n%s", out)
	}
	containsRun(t, out, "never published docker.io/grainai/sandbox:sha-"+short)
	containsRun(t, out, "v1.5.0 predates the switch to Docker Hub (#739)")
	if strings.Contains(out, "POST /api/repos/") {
		t.Errorf("promote of a pre-Docker-Hub release gave the generic re-run advice:\n%s", out)
	}
	if strings.Contains(docker, "imagetools create") {
		t.Errorf("promote moved a tag with nothing to promote:\n%s", docker)
	}
}

// A release cut since the switch copies nothing from GHCR, and keeps the
// generic advice: its own release job publishes to Docker Hub.
func TestPromoteOfAPostDockerHubReleaseCopiesNothingFromGHCR(t *testing.T) {
	remote, commits := releaseRepo(t)
	missing := "docker.io/grainai/sandbox:sha-" + commits["released"][:7]
	env := publishEnv(stubAnswers(t, commits["released"][:7], "", "v1.5.0"))
	env["DOCKER_STUB_MISSING"] = missing
	out, docker, err := promoteRun(t, remote, []string{"promote"}, env)
	if err == nil {
		t.Fatalf("promote succeeded with %s missing:\n%s", missing, out)
	}
	containsRun(t, out, "POST /api/repos/")
	if strings.Contains(docker, "ghcr.io") || strings.Contains(out, "predates") {
		t.Errorf("promote of a post-switch release looked at GHCR:\nout:\n%s\ndocker:\n%s", out, docker)
	}
}

// The dry run says which images a pre-Docker-Hub promotion would copy.
func TestPromoteDryRunOfAPreDockerHubReleaseNamesTheCopy(t *testing.T) {
	remote, commits := preDockerHubReleaseRepo(t)
	short := commits["released"][:7]
	out, _, err := promoteRun(t, remote, []string{"--dry-run", "promote"}, nil)
	if err != nil {
		t.Fatalf("promote --dry-run failed: %v\n%s", err, out)
	}
	containsRun(t, out, "v1.5.0 predates the switch to Docker Hub (#739), so first copy by digest")
	containsRun(t, out, "ghcr.io/bwsalmon/grain/{guest,k8s-sandbox,guest-base,k8s-sandbox-base}:sha-"+short)
}
