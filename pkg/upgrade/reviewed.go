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

// Whether the commit an image was built from reached its branch through a
// reviewed merge (pkg/reviewedmerge;
// docs/security/audits/2026-11-10-insider-risk.md, finding 3).
//
// The signature (verify.go) says the release key signed the image, which
// is to say CI built it. It does not say a reviewed commit went into it.
// Somebody who can merge on the forge without going through grain gets
// their commit built and signed like any other, and since the release
// workflow runs from that commit, it can also rewrite the release's own
// check and the provenance the release attests. So this check does not
// read the attestation. It reads the image's revision label, which the
// signature covers, and asks the forge how that commit and every
// first-parent commit since the running build's reached the branch. A
// commit cannot rewrite the forge's records of how it was merged.
//
// It runs in the signature step, after the signature, and only with one:
// without a signature the label is only what the registry says. A
// deployment that configures it with no VerifyCmd is refused rather than
// warned. That is also the answer to whether a missing VerifyCmd should
// refuse: for a deployment that has asked for this check, yes. For every
// other deployment it still warns, for the reason verify.go gives. Most
// installs carry no release key (scripts/setup.sh installs one only when
// GRAIN_IMAGE_VERIFY_KEY is given), so refusing by default would leave
// them on the build they run now with no way off it short of a
// reinstall.

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/reviewedmerge"
	"github.com/ai-on-gke/grain/pkg/version"
)

// ReviewedMergeConfig turns on the reviewed-merge check for an image
// upgrade.
type ReviewedMergeConfig struct {
	// Policy is the repository the images are built from, its release
	// branches and the forge logins its grain merges as.
	Policy reviewedmerge.Policy
	// Forge reads the forge's records; reviewedmerge.REST on a real
	// deployment.
	Forge reviewedmerge.Forge
	// Compare is the forge's comparison of the running build's commit
	// (base) with the target's (head). When the target is ahead, the
	// chain from the running build up to it is checked. When the two have
	// diverged (another branch, such as a release branch cut from main),
	// the chain from their merge base is: checking the target alone would
	// let a reviewed merge on top of a branch's unreviewed commits carry
	// them in. When the target is behind (a rollback), it is checked
	// alone. Nil checks the target alone.
	Compare func(ctx context.Context, repo, base, head string) (reviewedmerge.Comparison, error)
	// RevisionCmd prints a pulled image's revision label, with the image
	// appended; docker image inspect's when nil. The image path's alone:
	// the Kubernetes path has pulled nothing, and reads the label from
	// the registry by the signed digest (reviewed_registry.go).
	RevisionCmd []string
	// RunningRevision is the commit this deployment runs now;
	// version.Get().Revision when nil.
	RunningRevision func() string
	// MaxChain bounds the first-parent commits walked; 1000 when zero.
	MaxChain int
}

// reviewedTimeout bounds one whole check: a few API reads per merge, for
// as many merges as the chain carries.
var reviewedTimeout = 10 * time.Minute

var fullSHA = regexp.MustCompile(`^[0-9a-f]{40}$`)

// imageRevision is the commit a pulled image says it was built from.
func (u *Upgrader) imageRevision(ctx context.Context, p *progress, step string, rc *ReviewedMergeConfig, ref string) (string, error) {
	argv := rc.RevisionCmd
	if len(argv) == 0 {
		argv = []string{"docker", "image", "inspect", "--format", `{{index .Config.Labels "org.opencontainers.image.revision"}}`}
	}
	out, err := runAsked(ctx, p, step, imageInspectTimeout, append(append([]string{}, argv...), ref))
	if err != nil {
		return "", fmt.Errorf("reading the revision label of %s: %w", ref, err)
	}
	rev := strings.TrimSpace(out)
	if !fullSHA.MatchString(rev) {
		return "", fmt.Errorf("%s names no commit in its org.opencontainers.image.revision label (%q), so how it was reviewed cannot be asked", ref, rev)
	}
	return rev, nil
}

// kubeRevision is the commit ref, pinned to the digest the verifier
// signed off on, says it was built from, read from the registry.
func kubeRevision(ctx context.Context, ref string) (string, error) {
	rev, err := registryImageRevision(ctx, ref)
	if err != nil {
		return "", fmt.Errorf("reading the revision label of %s from its registry: %w", ref, err)
	}
	if !fullSHA.MatchString(rev) {
		return "", fmt.Errorf("%s names no commit in its org.opencontainers.image.revision label (%q), so how it was reviewed cannot be asked", ref, rev)
	}
	return rev, nil
}

// checkImageReviewedMerge is checkReviewedMerge for the image path: ref
// has been pulled, and its label is read from the local copy.
func (u *Upgrader) checkImageReviewedMerge(ctx context.Context, p *progress, step, ref string) (string, error) {
	rc := u.cfg.Image.ReviewedMerge
	return u.checkReviewedMerge(ctx, p, step, rc, u.cfg.Image.VerifyCmd, ref, func(ctx context.Context) (string, error) {
		return u.imageRevision(ctx, p, step, rc, ref)
	})
}

// checkKubeReviewedMerge is checkReviewedMerge for the Kubernetes path:
// ref is pinned to the digest verifyTag returned and nothing has pulled
// it, so the label is read from the registry by that digest.
func (u *Upgrader) checkKubeReviewedMerge(ctx context.Context, p *progress, step, ref string) (string, error) {
	return u.checkReviewedMerge(ctx, p, step, u.cfg.Kube.ReviewedMerge, u.cfg.Kube.VerifyCmd, ref, func(ctx context.Context) (string, error) {
		return kubeRevision(ctx, ref)
	})
}

// checkReviewedMerge refuses ref, an image already pinned and its
// signature verified, unless the commit revision reads out of it reached
// its branch through reviewed merges. It returns what the step should
// say on success, and "" when the deployment has not configured the
// check.
func (u *Upgrader) checkReviewedMerge(ctx context.Context, p *progress, step string, rc *ReviewedMergeConfig, verifyCmd []string, ref string, revision func(context.Context) (string, error)) (string, error) {
	if rc == nil {
		return "", nil
	}
	if len(verifyCmd) == 0 {
		return "", fmt.Errorf("the reviewed-merge check is configured but no signature verification is (-upgrade-verify-cmd), and without a signature %s's revision label is only what the registry says; configure both", ref)
	}
	if rc.Forge == nil {
		return "", fmt.Errorf("the reviewed-merge check has no forge to ask")
	}
	ctx, cancel := context.WithTimeout(ctx, reviewedTimeout)
	defer cancel()
	target, err := revision(ctx)
	if err != nil {
		return "", err
	}
	running := version.Get().Revision
	if rc.RunningRevision != nil {
		running = rc.RunningRevision()
	}
	since := ""
	if fullSHA.MatchString(running) && running != target && rc.Compare != nil {
		cmp, err := rc.Compare(ctx, rc.Policy.Repo, running, target)
		if err != nil {
			return "", fmt.Errorf("asking the forge how %s compares with the running %s: %w", shortSHA(target), shortSHA(running), err)
		}
		switch cmp.Status {
		case "ahead", "identical":
			since = running
		case "diverged":
			if !fullSHA.MatchString(cmp.MergeBase) {
				return "", fmt.Errorf("the forge names no merge base (%q) for %s and the running %s, so the chain to %s cannot be checked", cmp.MergeBase, shortSHA(target), shortSHA(running), shortSHA(target))
			}
			if cmp.MergeBase != target {
				since = cmp.MergeBase
			}
		}
	}
	max := rc.MaxChain
	if max <= 0 {
		max = 1000
	}
	if since != "" {
		p.logf("%s: asking %s how %s and every first-parent commit since %s reached their branch", step, rc.Policy.Repo, shortSHA(target), shortSHA(since))
	} else {
		p.logf("%s: asking %s how %s reached its branch", step, rc.Policy.Repo, shortSHA(target))
	}
	rec, err := rc.Policy.CheckChain(ctx, rc.Forge, target, since, max)
	if err != nil {
		return "", fmt.Errorf("%s was built from %s, which did not reach %s through a reviewed merge: %w", ref, shortSHA(target), rc.Policy.Repo, err)
	}
	return fmt.Sprintf("%s reached its branch through %d reviewed merge(s)", shortSHA(target), len(rec.Merges)), nil
}

func shortSHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}
