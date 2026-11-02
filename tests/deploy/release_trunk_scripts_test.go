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
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The release train's scripts (design/design/release-from-trunk.design.md, RFT-4)
// carry their own unit tests as shell, beside them, because what they
// test is shell: scripts/candidate-gate_test.sh drives select-lkg and
// check-soak against JSON shaped like the release gate's answers, and
// scripts/release-from-trunk_test.sh drives both phases of the train
// against a bare remote. This is what runs them in CI.
func TestReleaseTrainShellSuites(t *testing.T) {
	for _, tool := range []string{"git", "jq"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not on PATH: %v", tool, err)
		}
	}
	for _, suite := range []string{"candidate-gate_test.sh", "release-from-trunk_test.sh"} {
		t.Run(suite, func(t *testing.T) {
			cmd := exec.Command("bash", filepath.Join(repoRoot(t), "scripts", suite))
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("scripts/%s: %v\n%s", suite, err, out)
			}
			if !strings.Contains(string(out), "passed") || strings.Contains(string(out), "\nFAIL ") {
				t.Fatalf("scripts/%s did not report every case passing:\n%s", suite, out)
			}
			t.Logf("scripts/%s:\n%s", suite, out)
		})
	}
}

// The legacy cut is gone, and nothing still names it as a script to run.
func TestTheLegacyCandidateCutIsRetired(t *testing.T) {
	root := repoRoot(t)
	if _, err := exec.Command("test", "-e", filepath.Join(root, "scripts", "release-candidate-cut.sh")).CombinedOutput(); err == nil {
		t.Errorf("scripts/release-candidate-cut.sh still exists: scripts/release-from-trunk.sh replaced it")
	}
	for _, path := range [][]string{
		{"scripts", "release-images.sh"},
		{"scripts", "release-notes.sh"},
		{".grain", "workflows", "promote.yml"},
	} {
		if text := read(t, path...); strings.Contains(text, "release-candidate-cut.sh") {
			t.Errorf("%s still names scripts/release-candidate-cut.sh", filepath.Join(path...))
		}
	}
}

// rcRepo is releaseRepoWithDockerfile's repository, with release
// candidates on main the way the train leaves them: v1.6.0-rc.1 on
// "candidate" (the commit refs/heads/candidate points at), and an older
// v1.5.0-rc.9 nothing points at.
func rcRepo(t *testing.T) (string, map[string]string) {
	t.Helper()
	dir, commits := releaseRepoWithDockerfile(t, postSplitDockerfile)
	for _, args := range [][]string{
		{"branch", "candidate", commits["candidate"]},
		{"tag", "-a", "v1.5.0-rc.9", "-m", "Release candidate v1.5.0-rc.9", commits["unreleased"]},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(cmd.Environ(), "GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com",
			"GIT_CONFIG_GLOBAL=/dev/null")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return dir, commits
}

func TestReleaseImagesRCPublishesTheCandidateChannel(t *testing.T) {
	remote, commits := rcRepo(t)
	short := commits["candidate"][:7]

	t.Run("no argument follows refs/heads/candidate", func(t *testing.T) {
		out, docker, err := promoteRun(t, remote, []string{"--dry-run", "rc"}, nil)
		if err != nil {
			t.Fatalf("rc --dry-run failed: %v\n%s", err, out)
		}
		containsRun(t, out, "phase 'rc': would retag ghcr.io/bwsalmon/grain/{guest,k8s-sandbox}:sha-"+short+" as v1.6.0-rc.1, candidate rc")
		containsRun(t, out, "and rebuild ghcr.io/bwsalmon/grain/grain:sha-"+short+" from "+commits["candidate"])
		containsRun(t, out, "with RELEASE=v1.6.0-rc.1 as v1.6.0-rc.1, candidate rc")
		if strings.Contains(out, "google-grain") {
			t.Errorf("rc --dry-run mentioned google-grain:\n%s", out)
		}
		if docker != "" {
			t.Errorf("a dry run asked docker for something:\n%s", docker)
		}
	})

	t.Run("an older rc publishes itself and moves no channel", func(t *testing.T) {
		out, _, err := promoteRun(t, remote, []string{"--dry-run", "rc", "v1.5.0-rc.9"}, nil)
		if err != nil {
			t.Fatalf("rc v1.5.0-rc.9 failed: %v\n%s", err, out)
		}
		containsRun(t, out, "newest release candidate is v1.6.0-rc.1, not v1.5.0-rc.9")
		containsRun(t, out, "candidate and rc stay put")
	})

	t.Run("a release tag is not an rc", func(t *testing.T) {
		out, _, err := promoteRun(t, remote, []string{"--dry-run", "rc", "v1.5.0"}, nil)
		if err == nil {
			t.Fatalf("rc v1.5.0 succeeded:\n%s", out)
		}
		containsRun(t, out, "v1.5.0 is not a release candidate tag")
	})

	t.Run("publishing", func(t *testing.T) {
		out, docker, err := promoteRun(t, remote, []string{"rc"}, publishEnv(stubAnswers(t, short, "", "v1.6.0-rc.1")))
		if err != nil {
			t.Fatalf("rc failed: %v\n%s\ndocker was asked:\n%s", err, out, docker)
		}
		for _, image := range []string{"guest", "k8s-sandbox"} {
			for _, tag := range []string{"v1.6.0-rc.1", "candidate", "rc"} {
				containsRun(t, docker, retagLine(image, short, tag))
			}
		}
		containsRun(t, docker, "--build-arg RELEASE=v1.6.0-rc.1 --label org.opencontainers.image.version=v1.6.0-rc.1 -t ghcr.io/bwsalmon/grain/grain:v1.6.0-rc.1")
		containsRun(t, docker, "push ghcr.io/bwsalmon/grain/grain:v1.6.0-rc.1")
		for _, tag := range []string{"candidate", "rc", "sha-" + short} {
			containsRun(t, docker, "buildx imagetools create --tag ghcr.io/bwsalmon/grain/grain:"+tag+" ghcr.io/bwsalmon/grain/grain:v1.6.0-rc.1")
		}
		if strings.Contains(docker, ":latest") || strings.Contains(docker, "google-grain") {
			t.Errorf("publishing an rc touched latest or google-grain:\n%s", docker)
		}
	})
}

func TestReleaseImagesDiscardRCMarksWithoutMovingTheChannel(t *testing.T) {
	remote, _ := rcRepo(t)

	out, docker, err := promoteRun(t, remote, []string{"--dry-run", "discard-rc", "v1.6.0-rc.1"}, nil)
	if err != nil {
		t.Fatalf("discard-rc --dry-run failed: %v\n%s", err, out)
	}
	containsRun(t, out, "would tag ghcr.io/bwsalmon/grain/{grain,guest,k8s-sandbox}:v1.6.0-rc.1 as v1.6.0-rc.1-discarded, and leave candidate and rc where they are")
	if docker != "" {
		t.Errorf("a dry run asked docker for something:\n%s", docker)
	}

	env := publishEnv("")
	env["DOCKER_STUB_MISSING"] = "ghcr.io/bwsalmon/grain/k8s-sandbox:v1.6.0-rc.1"
	out, docker, err = promoteRun(t, remote, []string{"discard-rc", "v1.6.0-rc.1"}, env)
	if err != nil {
		t.Fatalf("discard-rc failed: %v\n%s\ndocker was asked:\n%s", err, out, docker)
	}
	for _, image := range []string{"grain", "guest"} {
		containsRun(t, docker, "buildx imagetools create --tag ghcr.io/bwsalmon/grain/"+image+":v1.6.0-rc.1-discarded ghcr.io/bwsalmon/grain/"+image+":v1.6.0-rc.1")
	}
	containsRun(t, out, "never published as k8s-sandbox")
	if strings.Contains(docker, ":candidate") || strings.Contains(docker, ":rc ") || strings.Contains(docker, "k8s-sandbox:v1.6.0-rc.1-discarded") || strings.Contains(docker, "google-grain") {
		t.Errorf("discard-rc moved a channel tag, touched google-grain, or marked an image the rc was never published as:\n%s", docker)
	}

	if out, _, err := promoteRun(t, remote, []string{"discard-rc"}, nil); err == nil {
		t.Errorf("discard-rc with no tag succeeded:\n%s", out)
	}
}
