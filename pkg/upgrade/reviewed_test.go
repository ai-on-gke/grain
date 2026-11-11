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
	"path/filepath"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/reviewedmerge"
)

var (
	runningSHA = strings.Repeat("1", 40)
	selfSHA    = strings.Repeat("2", 40)
	targetSHA  = strings.Repeat("3", 40)
	forkSHA    = strings.Repeat("4", 40)
)

// chainForge is the forge's record of main: runningSHA, then selfSHA,
// then targetSHA, each a pull request's merge commit. Grain merged the
// target; selfMerged says whether the one before it was merged by its own
// author with no review. With diverged, selfSHA sits on forkSHA instead,
// the commit a release branch was cut from, and the running build is on
// a branch of its own past forkSHA.
type chainForge struct{ selfMerged, diverged bool }

func (f chainForge) PullsForCommit(_ context.Context, _, sha string) ([]reviewedmerge.Pull, error) {
	by := "grain-bot[bot]"
	if sha == selfSHA && f.selfMerged {
		by = "mallory"
	}
	return []reviewedmerge.Pull{{Number: 1, Merged: true, MergeCommitSHA: sha, Base: "main", Author: by, MergedBy: by}}, nil
}

func (chainForge) Reviews(context.Context, string, int) ([]reviewedmerge.Review, error) {
	return nil, nil
}

func (f chainForge) FirstParent(_ context.Context, _, sha string) (string, error) {
	if f.diverged {
		return map[string]string{targetSHA: selfSHA, selfSHA: forkSHA}[sha], nil
	}
	return map[string]string{targetSHA: selfSHA, selfSHA: runningSHA}[sha], nil
}

func (f chainForge) compare(context.Context, string, string, string) (reviewedmerge.Comparison, error) {
	if f.diverged {
		return reviewedmerge.Comparison{Status: "diverged", MergeBase: forkSHA}, nil
	}
	return reviewedmerge.Comparison{Status: "ahead", MergeBase: runningSHA}, nil
}

func reviewedUpgrader(t *testing.T, dir string, verify []string, f chainForge) *Upgrader {
	t.Helper()
	return newUpgrader(t, Config{
		Image: &ImageConfig{
			Repository: "docker.io/grainai/grain",
			RefFile:    filepath.Join(dir, "image.env"),
			PullCmd:    []string{"true"},
			RunCmd:     []string{"sh", "-c", `printf 'run %s\n' "$1" >> ` + filepath.Join(dir, "log"), "sh"},
			VerifyCmd:  verify,
			ReviewedMerge: &ReviewedMergeConfig{
				Policy:          reviewedmerge.Policy{Repo: "example/grain", Branches: []string{"main"}, Mergers: []string{"grain-bot[bot]"}},
				Forge:           f,
				Compare:         f.compare,
				RevisionCmd:     []string{"sh", "-c", "echo " + targetSHA, "sh"},
				RunningRevision: func() string { return runningSHA },
			},
		},
		HealthCheckArgs: []string{"schema-version"},
		StatusFile:      filepath.Join(dir, "upgrade-status.json"),
	})
}

// TestUpgraderImageChecksTheChainSinceTheRunningBuild: a signed image
// whose commit and every first-parent commit since the running build's
// were merged by grain goes ahead, and the signature step says so.
func TestUpgraderImageChecksTheChainSinceTheRunningBuild(t *testing.T) {
	dir := t.TempDir()
	u := reviewedUpgrader(t, dir, fakeVerifyCmd(filepath.Join(dir, "verify.log"), 0), chainForge{})
	if err := u.Start("main"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	status := waitForPhase(t, u, PhaseOK)
	if got := stepDetail(t, status, stepVerify); !strings.Contains(got, "through 2 reviewed merge(s)") {
		t.Errorf("signature step detail = %q, want it to say the two merges were reviewed", got)
	}
}

// TestUpgraderImageRefusesASelfMergedCommitUnderAReviewedOne is the
// attack the check exists for: a writer self-merges on the forge, grain
// merges something on top, and the image of grain's merge is signed like
// any other. It is never run.
func TestUpgraderImageRefusesASelfMergedCommitUnderAReviewedOne(t *testing.T) {
	dir := t.TempDir()
	u := reviewedUpgrader(t, dir, fakeVerifyCmd(filepath.Join(dir, "verify.log"), 0), chainForge{selfMerged: true})
	if err := u.Start("main"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	status := waitForPhase(t, u, PhaseFailed)
	if got := stepState(t, status, stepVerify); got != StepFailed {
		t.Errorf("signature step = %q, want %q", got, StepFailed)
	}
	if !strings.Contains(status.Detail, "merged by mallory") {
		t.Errorf("status detail = %q, want it to name the unreviewed merge", status.Detail)
	}
	if got := stepState(t, status, stepHealthCheck); got != StepPending {
		t.Errorf("health check step = %q, want it never started", got)
	}
}

// TestUpgraderImageChecksADivergedBranchFromItsMergeBase: an image of a
// branch the running build is not behind -- a release branch cut from
// main, upgraded to from a main build -- is checked from where the two
// branches part, not alone. A self-merged commit under a reviewed tip of
// that branch is refused like one on the running build's own.
func TestUpgraderImageChecksADivergedBranchFromItsMergeBase(t *testing.T) {
	for _, selfMerged := range []bool{false, true} {
		dir := t.TempDir()
		u := reviewedUpgrader(t, dir, fakeVerifyCmd(filepath.Join(dir, "verify.log"), 0), chainForge{selfMerged: selfMerged, diverged: true})
		if err := u.Start("main"); err != nil {
			t.Fatalf("Start: %v", err)
		}
		if !selfMerged {
			status := waitForPhase(t, u, PhaseOK)
			if got := stepDetail(t, status, stepVerify); !strings.Contains(got, "through 2 reviewed merge(s)") {
				t.Errorf("signature step detail = %q, want both merges since the merge base checked", got)
			}
			continue
		}
		status := waitForPhase(t, u, PhaseFailed)
		if !strings.Contains(status.Detail, "merged by mallory") {
			t.Errorf("status detail = %q, want it to name the unreviewed merge under the diverged tip", status.Detail)
		}
	}
}

// TestUpgraderImageRefusesTheReviewedMergeCheckWithoutASignature: the
// revision label the check reads is only what the registry says unless a
// signature covers it, so asking for the check with no VerifyCmd refuses
// rather than warns.
func TestUpgraderImageRefusesTheReviewedMergeCheckWithoutASignature(t *testing.T) {
	dir := t.TempDir()
	u := reviewedUpgrader(t, dir, nil, chainForge{})
	if err := u.Start("main"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	status := waitForPhase(t, u, PhaseFailed)
	if !strings.Contains(status.Detail, "no signature verification is (-upgrade-verify-cmd)") {
		t.Errorf("status detail = %q, want it to ask for -upgrade-verify-cmd", status.Detail)
	}
}
