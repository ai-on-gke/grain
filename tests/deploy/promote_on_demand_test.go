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
	"path/filepath"
	"strings"
	"testing"
)

// Promoting a commit whose release run was superseded (grain/task-3334).
//
// release.yml's `release images` job keeps one run in flight and one
// pending per branch, so a burst of merges cancels every pending run but
// the newest: on 2026-11-06 nine merges in seven minutes left eight
// commits with no sha- images. A tag cut on one of them later failed
// promotion with "the release job never published ... for it". With
// RELEASE_ON_DEMAND_BUILD=dispatch the promotion has release.yml build
// that commit instead -- the same build-and-test job, checked out at it
// -- waits for its images, and goes on to publish them.
//
// These run the real script with a stub docker whose registry lacks the
// commit's sha- images until a stub gh is asked to dispatch the release
// workflow, which is what "publishes" them.

// onDemandEnv is publishEnv for a promotion of the "released" commit
// whose sha- images do not exist until gh dispatches release.yml. onDispatch
// is what the dispatched run does: "publish" its images, or "fail"; or
// "in-flight" for a run already going that publishes once it is asked
// about.
// runs, when set, is what `gh run list` reports before any dispatch. It
// returns the environment and the path gh logs its arguments to.
func onDemandEnv(t *testing.T, commits map[string]string, onDispatch, runs string) (map[string]string, string) {
	t.Helper()
	dir := t.TempDir()
	short := commits["released"][:7]
	marker := filepath.Join(dir, "published")
	runsFile := filepath.Join(dir, "runs")
	if runs != "" {
		if err := os.WriteFile(runsFile, []byte(runs+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// The registry answers for the commit's sha- images only once the
	// on-demand run has published them.
	answers := "case \"$*\" in\n" +
		"  \"buildx imagetools inspect docker.io/grainai/sandbox:sha-" + short + "\"|" +
		"\"buildx imagetools inspect docker.io/grainai/sandbox-k8s:sha-" + short + "\"|" +
		"\"buildx imagetools inspect docker.io/grainai/grain:sha-" + short + "\") [ -f \"" + marker + "\" ] || exit 1 ;;\n" +
		"esac\n" +
		". \"" + stubAnswers(t, short, "", "v1.5.0") + "\"\n"
	answersPath := filepath.Join(dir, "answers.sh")
	if err := os.WriteFile(answersPath, []byte(answers), 0o644); err != nil {
		t.Fatal(err)
	}
	ghLog := filepath.Join(dir, "gh.log")
	gh := "#!/bin/sh\n" +
		"echo \"$@\" >> \"" + ghLog + "\"\n" +
		"case \"$1 $2\" in\n" +
		"  \"run list\") [ -f \"" + runsFile + "\" ] && cat \"" + runsFile + "\"\n" +
		"    [ \"" + onDispatch + "\" = in-flight ] && : > \"" + marker + "\"\n" +
		"    exit 0 ;;\n" +
		"  \"workflow run\")\n" +
		"    case \"" + onDispatch + "\" in\n" +
		"      publish) : > \"" + marker + "\"; echo \"completed success https://github.example/actions/runs/2\" > \"" + runsFile + "\" ;;\n" +
		"      fail) echo \"completed failure https://github.example/actions/runs/2\" > \"" + runsFile + "\" ;;\n" +
		"    esac\n" +
		"    exit 0 ;;\n" +
		"esac\n" +
		"exit 0\n"
	ghBin := filepath.Join(dir, "bin")
	if err := os.MkdirAll(ghBin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ghBin, "gh"), []byte(gh), 0o755); err != nil {
		t.Fatal(err)
	}
	env := publishEnv(answersPath)
	env["PATH"] = ghBin + ":" + realToolPath(t)
	env["RELEASE_ON_DEMAND_BUILD"] = "dispatch"
	env["RELEASE_ON_DEMAND_POLL"] = "0"
	env["RELEASE_ON_DEMAND_TIMEOUT"] = "5"
	return env, ghLog
}

func readLog(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return string(b)
}

func TestPromoteOfASupersededCommitBuildsItOnDemandThenPromotes(t *testing.T) {
	remote, commits := releaseRepo(t)
	short := commits["released"][:7]
	env, ghLog := onDemandEnv(t, commits, "publish", "")
	out, docker, err := promoteRun(t, remote, []string{"promote"}, env)
	if err != nil {
		t.Fatalf("promote of a superseded commit failed: %v\n%s\ndocker was asked:\n%s", err, out, docker)
	}
	containsRun(t, out, "has no published sha-"+short+" images")
	containsRun(t, out, "dispatching release.yml")
	containsRun(t, readLog(t, ghLog), "workflow run release.yml --repo bwsalmon/grain --ref main -f commit="+commits["released"])
	// And then promoted exactly as a commit whose release ran would be.
	containsRun(t, docker, retagLine("sandbox", short, "v1.5.0"))
	containsRun(t, docker, retagLine("sandbox", short, "latest"))
	containsRun(t, docker, "buildx imagetools create --tag docker.io/grainai/grain:latest docker.io/grainai/grain:v1.5.0")
}

// An on-demand run already in flight for the commit -- a second promote
// tick, or a tag and a promotion racing -- is waited on, not doubled.
func TestPromoteWaitsOnAnOnDemandBuildAlreadyInFlight(t *testing.T) {
	remote, commits := releaseRepo(t)
	env, ghLog := onDemandEnv(t, commits, "in-flight", "in_progress - https://github.example/actions/runs/1")
	out, docker, err := promoteRun(t, remote, []string{"promote"}, env)
	if err != nil {
		t.Fatalf("promote failed waiting on an in-flight on-demand build: %v\n%s\ndocker was asked:\n%s", err, out, docker)
	}
	containsRun(t, out, "already being built on demand (https://github.example/actions/runs/1)")
	if strings.Contains(readLog(t, ghLog), "workflow run") {
		t.Errorf("dispatched a second on-demand build while one was in flight:\n%s", readLog(t, ghLog))
	}
}

// A failed on-demand build moves nothing and says where to look.
func TestPromoteWhoseOnDemandBuildFailsMovesNothing(t *testing.T) {
	remote, commits := releaseRepo(t)
	short := commits["released"][:7]
	env, _ := onDemandEnv(t, commits, "fail", "")
	out, docker, err := promoteRun(t, remote, []string{"promote"}, env)
	if err == nil {
		t.Fatalf("promote succeeded although the on-demand build failed:\n%s", out)
	}
	containsRun(t, out, "ended failure without publishing")
	containsRun(t, out, "never published docker.io/grainai/sandbox:sha-"+short)
	if strings.Contains(docker, "imagetools create") || strings.Contains(docker, "build ") {
		t.Errorf("promote moved a tag although the on-demand build failed:\n%s", docker)
	}
}

// A commit on no release branch is never built on demand: a tag on a
// side branch is not something the release job would ever have built.
func TestPromoteRefusesToBuildACommitOffTheReleaseBranchesOnDemand(t *testing.T) {
	remote, commits := releaseRepo(t)
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = remote
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com",
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("checkout", "-q", "-b", "side", commits["old"])
	git("commit", "-q", "--allow-empty", "-m", "side")
	side := git("rev-parse", "HEAD")
	git("tag", "-a", "v1.7.0", "-m", "Production release v1.7.0", side)
	git("checkout", "-q", "main")
	env, ghLog := onDemandEnv(t, commits, "publish", "")
	env["DOCKER_STUB_MISSING"] = "docker.io/grainai/sandbox:sha-" + side[:7]
	out, docker, err := promoteRun(t, remote, []string{"promote", "v1.7.0"}, env)
	if err == nil {
		t.Fatalf("promote of a side-branch commit succeeded:\n%s", out)
	}
	containsRun(t, out, "refusing to build "+side+" on demand")
	if strings.Contains(readLog(t, ghLog), "workflow run") {
		t.Errorf("dispatched an on-demand build of a side-branch commit:\n%s", readLog(t, ghLog))
	}
	if strings.Contains(docker, "imagetools create") {
		t.Errorf("promote moved a tag for a side-branch commit:\n%s", docker)
	}
}

// release.yml is what the dispatch reaches: it takes a commit input,
// builds that commit, gives such a run its own concurrency group (so it
// neither cancels nor is cancelled by the branch's runs) and refuses a
// commit off the release branches.
func TestReleaseWorkflowBuildsACommitOnDemand(t *testing.T) {
	wf := read(t, ".github", "workflows", "release.yml")
	for _, want := range []string{
		"      commit:\n",
		"ref: ${{ inputs.commit }}",
		"format('commit-{0}', inputs.commit)",
		"refs/remotes/origin/main",
		"needs.on-demand.outputs.build == 'true'",
		"run-name:",
	} {
		containsRun(t, wf, want)
	}
	for _, name := range []string{"promote.yml", "promote-release.yml", "publish-release.yml"} {
		containsRun(t, read(t, ".github", "workflows", name), "RELEASE_ON_DEMAND_BUILD: dispatch")
	}
}
