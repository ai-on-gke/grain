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

// The Kubernetes half of this package (grain/task-358).
//
// A deployment running in a cluster has neither of the things the other
// two pipelines act on. There is no checkout to fetch into and no
// toolchain to build with, so the source path has nothing to do; and
// there is no ref file to rewrite and no service to restart, because
// what the pod runs is written in a Deployment's spec and changing it is
// what starts a rollout. So Config.Kube switches Start onto three steps:
//
//	run the new image once, as a throwaway pod, to prove the cluster can
//	pull it and that what is inside it starts -- and to learn the digest
//	it ran, which every later step and the Deployment are pinned to
//	carry this deployment's rows across if the new build knows a
//	different schema
//	repoint the Deployment at it, and let Kubernetes do the restart
//
// The health check is worth more here than on the image path, not less,
// and it is worth saying why. deploy/kubernetes/grain.yaml runs one
// replica with strategy: Recreate, because two grain processes against
// one SQLite store is data loss -- so the pod running now is stopped
// before its replacement is created, and an image that cannot start is
// an outage rather than a rollout that quietly stalls with the old pod
// still serving. Proving the image runs *before* the spec changes is
// what keeps that from being the way an operator finds out.
//
// There is nothing to roll back here either, for the image path's own
// reason: the check runs before the Deployment is touched, so a failure
// leaves the cluster running exactly what it was already running.
// Rolling back a *successful* upgrade that turns out to be bad is
// `kubectl rollout undo`, which is Kubernetes' own record of what came
// before and not something this package keeps.

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/k8s"
)

// KubeConfig, set on Config.Kube, makes an Upgrader upgrade a
// Kubernetes deployment: health-check an image and repoint a Deployment
// at it, instead of pulling and rewriting a ref file or fetching and
// building a binary.
//
// SrcDir, BuildCmd, BuiltBinary, InstallPath and Image are all unused
// when it is set. HealthCheckArgs, Timeout, Hold, Schema and StatusFile
// mean exactly what they do for the other two paths. RestartCmd is
// unused and should be left nil: the rollout *is* the restart.
type KubeConfig struct {
	// Repository is the image CI publishes a tag per branch to, with no
	// tag of its own -- "ghcr.io/bwsalmon/grain/grain". Required, and
	// the same value ImageConfig.Repository takes: which branch has an
	// image is a fact about CI, not about how a deployment runs it.
	Repository string
	// Deployment is the Deployment whose image an upgrade repoints --
	// "grain", deploy/kubernetes/grain.yaml's own. Required.
	Deployment string
	// Container names the container within it to repoint; "grain" when
	// empty, matching the same manifest.
	Container string
	// Namespace is where that Deployment lives, and where the health
	// check's throwaway pod is created; k8s.DefaultNamespace when empty.
	Namespace string
	// Kubectl is the binary pkg/k8s runs; empty finds "kubectl" on PATH.
	Kubectl string
	// HealthCheckTimeout bounds the throwaway pod: how long the cluster
	// is given to pull the image, schedule the pod and run the check
	// inside it. DefaultKubeHealthCheckTimeout when zero.
	//
	// Much longer than the image path's ten seconds, and for a reason
	// that is not about the check itself: `docker run` on the image path
	// happens after a pull this package has already waited out, while
	// here the pull is *part of* the check -- which is the point, since
	// "can this cluster pull this image" is exactly the question a
	// rollout would otherwise answer by taking the deployment down.
	HealthCheckTimeout time.Duration
	// VerifyCmd checks who published an image, as ImageConfig.VerifyCmd
	// does -- `cosign verify --key <release public key>` on a real
	// deployment -- but is handed the *tag*, since nothing here has
	// pulled the image to learn its digest and the first thing that
	// would is a pod running it. So it has to report what it verified:
	// the digest is read from its stdout (cosign's JSON payloads,
	// verify.go's verifiedDigests), and the check pod and everything
	// after it run that digest and nothing else. Run for the grain image
	// before the health check's pod is created and for the sandbox image
	// before its own.
	//
	// Nil skips the check with a warning on the signature step, as on
	// the image path.
	VerifyCmd []string
	// SandboxImageArgs asks the candidate grain image which Kubernetes
	// sandbox image it names. When set, that image is also pull-checked
	// in a throwaway pod before the Deployment is changed.
	SandboxImageArgs []string
	// UnitFlags are the names of the flags this deployment's own process
	// was started with -- "-data-dir", "-k8s-sandboxes" -- which here is
	// exactly the `args:` list of the container in the Deployment below,
	// since that list is this process's command line
	// (pkg/cli/daemon.go hands over the flags it saw, undefined ones
	// included).
	//
	// ImageConfig.UnitFlags's own comment has the reasoning, and it
	// applies here with kubectl in systemd's place: an operator applies
	// those args once and the Upgrade button rewrites only the image in
	// the same spec, so the command line stays frozen while the build
	// behind it moves with every upgrade and every rollback. What it
	// costs when they disagree is worse on this shape, not better --
	// deploy/kubernetes/grain.yaml runs one replica with strategy
	// Recreate, so a build that will not start is an outage, and a build
	// that starts while silently ignoring half its configuration is a
	// deployment quietly pointed at the wrong data directory.
	//
	// Checked, not enforced: see checkImageFlags for why a disagreement
	// does not stop the upgrade.
	//
	// Empty leaves the step out entirely.
	UnitFlags []string
	// FlagNamesArgs asks the candidate image which flags its daemon
	// accepts -- {"daemon", "-flag-names"} on a real deployment,
	// answered in a throwaway pod like every other question this path
	// puts to a build it has not cut over to. Nil leaves the check out,
	// the same way UnitFlags does.
	FlagNamesArgs []string
	// PriorityClasses are the PriorityClasses this deployment's sandbox
	// pods name (Settings -> Sandbox). Before the Deployment is
	// repointed they are checked, and grain's own created where they
	// are missing (k8s.EnsurePriorityClasses) -- the step a cluster set
	// up before grain shipped them would otherwise skip, leaving every
	// sandbox pod the new build creates refused by the API server. Never
	// fatal: the new build creates pods without a class it cannot find,
	// and says so. Empty leaves the step out.
	PriorityClasses []string
}

// DefaultKubeHealthCheckTimeout is KubeConfig.HealthCheckTimeout's
// fallback: long enough for a cold node to pull a multi-gigabyte image
// and run a subcommand that exits at once, and short enough that an
// upgrade cannot sit on it for the whole of Config.Timeout.
const DefaultKubeHealthCheckTimeout = 10 * time.Minute

// kubeHealthCheckPod is the name of the throwaway pod the health check
// runs in. Fixed rather than generated: an Upgrader runs one upgrade at
// a time (Start's own lock), so there is never a second one to collide
// with, and a fixed name means an operator watching an upgrade knows
// what to `kubectl describe` without reading a log first. Anything left
// over from a previous upgrade is deleted before this one creates its
// own.
const kubeHealthCheckPod = "grain-upgrade-check"

// kubeHealthCheckShape is what the check pod asks for. Deliberately
// small: it runs one subcommand that touches no store and exits
// immediately, so what this has to be big enough for is the image on
// disk, not the work.
var kubeHealthCheckShape = k8s.PodShape{CPUs: 1, MemoryMB: 512, DiskGB: 10}

// stepRollout is the Kubernetes path's own last step, in place of the
// image path's "record image": the Deployment is repointed and the
// cluster begins replacing this very pod.
const stepRollout = "roll out"

// stepPriorityClasses is KubeConfig.PriorityClasses' check.
const stepPriorityClasses = "priority classes"

func (c KubeConfig) container() string {
	if c.Container != "" {
		return c.Container
	}
	return "grain"
}

func (c KubeConfig) healthCheckTimeout() time.Duration {
	if c.HealthCheckTimeout > 0 {
		return c.HealthCheckTimeout
	}
	return DefaultKubeHealthCheckTimeout
}

// client is the kubectl this config's steps run through.
func (c KubeConfig) client() k8s.Client {
	return k8s.Client{Kubectl: c.Kubectl, Namespace: c.Namespace}
}

// stageKube is the Kubernetes path: health-check the image the branch
// names, carry the schema across if it moved, and repoint the
// Deployment.
func (u *Upgrader) stageKube(ctx context.Context, p *progress, branch string) (string, error) {
	ref, err := u.imageRefIn(u.cfg.Kube.Repository, branch)
	if err != nil {
		return "", err
	}
	client := u.cfg.Kube.client()

	// What the Deployment already constrains its pod with, read before
	// anything is created: every throwaway pod below that runs the
	// *candidate grain image* runs under it, so that the question the
	// check asks is the question the rollout will ask. A candidate that
	// only starts as root has to fail here, where the Deployment is
	// still untouched, rather than after Recreate has stopped the pod
	// serving now.
	//
	// Read rather than configured: deploy/kubernetes/grain.yaml's
	// securityContext is the one place that answer lives, and a uid
	// named a second time in this deployment's flags would be a uid free
	// to drift from the one the rollout uses. A failure here is a
	// failure before the cut-over, which is the safe side of it.
	security, err := client.DeploymentSecurity(ctx, u.cfg.Kube.Deployment, u.cfg.Kube.container())
	if err != nil {
		return "", fmt.Errorf("reading the security context deployment/%s runs %s under, which the check pods have to run under too: %w",
			u.cfg.Kube.Deployment, u.cfg.Kube.container(), err)
	}

	// The health check's pod is also what pins this upgrade: the image
	// it reports having run is the one every later step runs and the
	// Deployment is set to, so without it there is nothing to pin to.
	// Refused before anything is created rather than skipped, for the
	// reason kubePin gives.
	if len(u.cfg.HealthCheckArgs) == 0 {
		return "", fmt.Errorf("a Kubernetes upgrade needs a health check: its pod reports the digest of the image it ran, "+
			"which is what the Deployment is pinned to, and without one there is no way to say what a rollout of %s would run", ref)
	}
	current, _ := client.DeploymentImage(ctx, u.cfg.Kube.Deployment, u.cfg.Kube.container())
	// Who published it, before the pod below runs it: with a verifier
	// configured, the pod is created from the digest it verified rather
	// than from the tag, so the tag moving in between cannot slip an
	// unsigned image into the check (verify.go).
	tag := ref
	checked := ref
	if len(u.cfg.Kube.VerifyCmd) > 0 {
		p.start(stepVerify)
		if checked, err = verifyTag(ctx, p, stepVerify, u.cfg.Kube.VerifyCmd, ref); err != nil {
			return "", fmt.Errorf("%w; leaving this deployment on the image it already runs", err)
		}
		p.done(stepVerify, fmt.Sprintf("%s is signed as %s", tag, strings.TrimPrefix(checked, tag+"@")))
		if u.sameKubeVersion(current, branch, tag, checked) || u.discardedRCUnchangedDetail(ctx, branch) != "" {
			if msg := u.discardedRCUnchangedDetail(ctx, branch); msg != "" {
				return p.markUnchanged(msg), nil
			}
			return p.markUnchanged(fmt.Sprintf("the version on %s has not changed (already running %s)", branch, checked)), nil
		}
	} else {
		p.logf("%s: warning: %s", stepVerify, unverifiedNote)
		p.skip(stepVerify, unverifiedNote)
	}

	p.start(stepHealthCheck)
	imageID, err := u.healthCheckInCluster(ctx, p, client, checked, append([]string{"grain"}, u.cfg.HealthCheckArgs...), security, stepHealthCheck)
	if err != nil {
		// Nothing to roll back: the Deployment is untouched, so the
		// cluster goes on running exactly the image it was already
		// running.
		return "", fmt.Errorf("health check failed on %s, leaving this deployment on the image it already runs: %w", ref, err)
	}
	if ref, err = kubePin(ref, imageID); err != nil {
		return "", fmt.Errorf("%w; leaving this deployment on the image it already runs", err)
	}
	// A pod created by digest runs that digest; one that reports another
	// ran something nobody verified, and is refused rather than pinned.
	if checked != tag && ref != checked {
		return "", fmt.Errorf("the health check's pod ran %s, not the %s that was verified; leaving this deployment on the image it already runs", ref, checked)
	}
	p.done(stepHealthCheck, fmt.Sprintf("%s runs in this cluster as %s", tag, strings.TrimPrefix(ref, tag+"@")))
	if u.sameKubeVersion(current, branch, tag, ref) || u.discardedRCUnchangedDetail(ctx, branch) != "" {
		if msg := u.discardedRCUnchangedDetail(ctx, branch); msg != "" {
			return p.markUnchanged(msg), nil
		}
		return p.markUnchanged(fmt.Sprintf("the version on %s has not changed (already running %s)", branch, ref)), nil
	}

	detail := "rolled out " + ref

	// Whether the image that just proved it runs will accept the args
	// the Deployment spec hands it. Never fatal -- see checkImageFlags --
	// so nothing here returns an error: what it can do is put the flag
	// names in front of an operator while they are still watching the
	// upgrade they asked for, rather than leaving them in a pod log
	// nobody reads until the replica is in CrashLoopBackOff.
	if len(u.cfg.Kube.FlagNamesArgs) > 0 && len(u.cfg.Kube.UnitFlags) > 0 {
		p.start(stepFlags)
		missing, err := u.checkKubeFlags(ctx, p, ref, security)
		if note := reportFlags(p, ref, u.cfg.Kube.UnitFlags, u.cfg.Kube.flagFix(), missing, err); note != "" {
			detail += "; " + note
		}
	} else {
		p.skip(stepFlags, "this deployment's flags are not written down in the Deployment that runs it")
	}

	if len(u.cfg.Kube.SandboxImageArgs) > 0 {
		p.start(stepSandbox)
		out, err := (kubeRunner{u: u, ref: ref, security: security}).ask(ctx, p, u.cfg.Kube.SandboxImageArgs)
		sandbox := strings.TrimSpace(out)
		if err != nil || sandbox == "" || strings.Contains(sandbox, "\n") {
			if err == nil {
				err = fmt.Errorf("answer %q is not one image reference", sandbox)
			}
			return "", fmt.Errorf("asking %s which Kubernetes sandbox image it expects: %w", ref, err)
		}
		// No security context on this one, and that is not an oversight:
		// this pod runs the *sandbox* image, which in production runs
		// with none either (k8s.PodOptions has nothing set for a
		// sandbox, by the argument in k8s.Manifest). Constraining it to
		// the daemon's uid would make this check ask a question no
		// sandbox pod ever answers.
		// The sandbox image is checked by the same verifier and for the
		// same reason, before a pod pulls and runs it. The pod is created
		// from the verified digest. A release build names its sandbox by
		// digest already (scripts/release-images.sh), so that is also
		// the digest every sandbox pod afterwards is created from; a
		// build naming a bare tag leaves its pods following the tag
		// (docs/image-signing.md).
		sandboxRun := sandbox
		if len(u.cfg.Kube.VerifyCmd) > 0 {
			if sandboxRun, err = verifyTag(ctx, p, stepSandbox, u.cfg.Kube.VerifyCmd, sandbox); err != nil {
				return "", fmt.Errorf("the Kubernetes sandbox image %s that %s expects: %w", sandbox, ref, err)
			}
		}
		if _, err := u.healthCheckInCluster(ctx, p, client, sandboxRun, []string{"bash", "-c", "true"}, k8s.PodSecurity{}, stepSandbox); err != nil {
			return "", fmt.Errorf("pulling the Kubernetes sandbox image %s that %s expects: %w", sandbox, ref, err)
		}
		p.done(stepSandbox, sandbox+" runs in this cluster")
		detail += " with sandbox image " + sandbox
	} else {
		p.skip(stepSandbox, "no Kubernetes sandbox image check configured")
	}
	var priorityClasses []string
	for _, name := range u.cfg.Kube.PriorityClasses {
		if name != "" {
			priorityClasses = append(priorityClasses, name)
		}
	}
	if len(priorityClasses) > 0 {
		p.start(stepPriorityClasses)
		p.done(stepPriorityClasses, ensureKubePriorityClasses(ctx, p, client, priorityClasses))
	} else {
		p.skip(stepPriorityClasses, "this deployment's sandbox pods name no PriorityClass")
	}
	// Every run in a run host lets go, its spool cursor written, before
	// the rollout this staging ends in (Config.Flush, upgrade.go's
	// flush): the restart has no command here, so this is the last
	// moment before it that this process is sure to have. Ahead of the
	// rows' export, so that the cursors the flush writes are in it on an
	// upgrade that migrates.
	resume := u.kubeFlush(ctx, p, client, ref)
	// Last before the cut-over, for the reason stageImage puts it there:
	// the shorter the gap between exporting this deployment's rows and
	// stopping the process still writing them, the less of it is lost.
	migrating, err := u.schemaStep(ctx, p, branch, kubeRunner{u: u, ref: ref, security: security})
	if err != nil {
		resume()
		return "", err
	}
	detail += migrating

	p.start(stepRollout)
	if err := client.SetDeploymentImage(ctx, u.cfg.Kube.Deployment, u.cfg.Kube.container(), ref); err != nil {
		u.discardSchemaUpgrade()
		resume()
		return "", fmt.Errorf("repointing the deployment: %w", err)
	}
	p.done(stepRollout, fmt.Sprintf("deployment/%s now runs %s", u.cfg.Kube.Deployment, ref))
	return detail, nil
}

// kubeFlush is flush for the Kubernetes path, which flushes only when
// repointing the Deployment at ref will start a rollout. One that already
// runs ref is left as it is by `kubectl set image`: no new pod is coming
// to adopt the runs, and this process carries on serving with its tails
// stopped. When the answer cannot be read it does not flush either, which
// is the side the Kubernetes path was on before it flushed at all: the
// pod's termination detaches the runs (runhost.Host.Detach).
func (u *Upgrader) kubeFlush(ctx context.Context, p *progress, client k8s.Client, ref string) func() {
	if u.cfg.Flush == nil {
		return func() {}
	}
	current, err := client.DeploymentImage(ctx, u.cfg.Kube.Deployment, u.cfg.Kube.container())
	if err != nil {
		p.logf("not flushing the runs in run hosts before the rollout: %v -- the pod's termination detaches them", err)
		return func() {}
	}
	if current == ref {
		p.logf("not flushing the runs in run hosts: deployment/%s already runs %s, so nothing rolls out", u.cfg.Kube.Deployment, ref)
		return func() {}
	}
	return u.flush(p)
}

func kubeDigestRef(ref string) (string, bool) {
	_, digest, ok := strings.Cut(ref, "@")
	if !ok || !strings.HasPrefix(digest, "sha256:") || len(digest) == len("sha256:") {
		return "", false
	}
	return normalizeRepository(repositoryOf(ref)) + "@" + digest, true
}

func (u *Upgrader) sameKubeVersion(current, branch, tag, ref string) bool {
	if prevDigest, ok := kubeDigestRef(current); ok {
		if newDigest, ok := kubeDigestRef(ref); ok && prevDigest == newDigest {
			return true
		}
	}
	if current != "" && !strings.Contains(current, "@") &&
		normalizeRepository(repositoryOf(current)) == normalizeRepository(repositoryOf(ref)) &&
		current == tag &&
		(versionTag.MatchString(branch) || candidateTag.MatchString(branch) || strings.HasPrefix(branch, "sha-")) {
		return true
	}
	if rel := u.runningRelease(); rel != "" && branch == rel &&
		(current == "" || normalizeRepository(repositoryOf(current)) == normalizeRepository(repositoryOf(ref))) {
		return true
	}
	return false
}

// kubePin turns ref, the tag the health check's pod was created from,
// into a reference to exactly the image that pod ran:
// `repo:tag@sha256:...`, the shape image.go's pinImage records and for
// the same reason (grain/task-1492, grain/task-1559).
//
// The tag moves. CI pushes the branch's tag again on every commit, and a
// Deployment whose pods pull with imagePullPolicy: Always fetches
// whatever it names at the moment a node starts one -- so a rollout set
// to the tag would run whatever the tag named at rollout time, and every
// restart after it whatever it names then, which need not be the image
// the health check, the flag check, the sandbox question or the schema
// step ever ran against. Every step after the health check therefore
// runs against the pinned ref, and the pinned ref is what the Deployment
// is set to. The tag stays in it, ahead of the digest, for the person
// reading `kubectl get deployment` or the upgrade pane: the runtime
// resolves a reference carrying both by the digest alone.
//
// imageID is the pod's status.containerStatuses[].imageID, which the
// runtime writes as "docker-pullable://repo@sha256:..." or
// "repo@sha256:...". Anything else -- empty, a bare "sha256:..." that is
// an image's own ID rather than a registry digest, or a digest from some
// other repository -- is refused rather than rolled out by tag: there is
// then nothing to pin to, and no way to say what the rollout would run.
func kubePin(ref, imageID string) (string, error) {
	id := strings.TrimPrefix(strings.TrimSpace(imageID), "docker-pullable://")
	repo, digest, ok := strings.Cut(id, "@")
	if !ok || !strings.HasPrefix(digest, "sha256:") || len(digest) == len("sha256:") ||
		normalizeRepository(repo) != normalizeRepository(repositoryOf(ref)) {
		return "", fmt.Errorf("the health check's pod reported no registry digest for %s (its imageID was %q), so there is nothing to pin the upgrade to", ref, imageID)
	}
	return ref + "@" + digest, nil
}

// normalizeRepository spells a repository the way a kubelet reports it,
// so that "example/grain" in this deployment's config and the
// "docker.io/example/grain" a runtime writes into imageID compare equal:
// a first component with no "." or ":" in it, and not "localhost", is a
// Docker Hub path, and a Docker Hub path of one component is under
// "library/".
func normalizeRepository(repo string) string {
	first, rest, hasSlash := strings.Cut(repo, "/")
	if hasSlash && (strings.ContainsAny(first, ".:") || first == "localhost") {
		return repo
	}
	if !hasSlash {
		return "docker.io/library/" + repo
	}
	if first == "docker.io" || first == "index.docker.io" {
		if !strings.Contains(rest, "/") {
			return "docker.io/library/" + rest
		}
		return "docker.io/" + rest
	}
	return "docker.io/" + repo
}

// checkKubeFlags asks the candidate image which daemon flags it defines
// and returns the ones this Deployment's args pass that it does not --
// the flags that will be ignored from the rollout onwards.
//
// It is checkImageFlags with a pod in place of a `docker run`, and it
// asks the identical question of the identical subcommand. The one
// difference is where the answer comes from: `kubectl logs`, which
// carries whatever the build wrote to stderr in the same stream, so a
// build too old to know the question answers with a usage dump rather
// than with nothing. flagsNotDefined is the part that has to survive
// that, and its own comment says how.
//
// security is the Deployment's own, like every other pod here that runs
// the candidate image: a build that only starts as root would otherwise
// answer this question from a pod the rollout could not reproduce.
func (u *Upgrader) checkKubeFlags(ctx context.Context, p *progress, ref string, security k8s.PodSecurity) ([]string, error) {
	out, err := (kubeRunner{u: u, ref: ref, security: security}).ask(ctx, p, u.cfg.Kube.FlagNamesArgs)
	if err != nil {
		return nil, err
	}
	return flagsNotDefined(ref, out, u.cfg.Kube.UnitFlags)
}

// flagFix is what this path tells an operator to do about a flag the new
// image will ignore. It names the object rather than a script, because
// that is where the command line is: a Deployment somebody applied, and
// which only another apply changes. The manifest in this repository is
// named beside it since that is usually what was applied, but the object
// is the thing that has to change -- a cluster edited in place has
// drifted from the file.
func (c KubeConfig) flagFix() string {
	return fmt.Sprintf("Edit container %s's args in deployment/%s (deploy/kubernetes/grain.yaml) "+
		"and re-apply it", c.container(), c.Deployment)
}

// healthCheckInCluster runs ref once, as a throwaway pod, and fails
// unless it exits zero.
//
// It proves three things at once, which is why it is worth a whole pod:
// that this cluster can pull the image (registry reachable, credentials
// good, tag actually published), that a node will schedule it, and that
// the binary inside starts and can answer HealthCheckArgs -- the same
// narrow question the other two paths ask, and the same class of failure
// it catches (a panic in an init, a missing library, a build that is not
// a working executable). What it still cannot catch is a build that
// starts fine and misbehaves only against real traffic; nothing short of
// running it can.
//
// security is what the pod is constrained by -- the Deployment's own,
// for a pod running the candidate grain image, so that "it starts" means
// "it starts as the uid that will run it". Empty for a pod checking an
// image that runs unconstrained in production, which is the sandbox
// image's case.
//
// On success it returns the imageID the pod reported -- the image it
// actually ran, which kubePin turns into what the rollout is set to.
func (u *Upgrader) healthCheckInCluster(ctx context.Context, p *progress, client k8s.Client, ref string, command []string, security k8s.PodSecurity, step string) (string, error) {
	timeout := u.cfg.Kube.healthCheckTimeout()
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Whatever a previous upgrade left behind, before the name is
	// needed. A check pod is deleted on every path below, so one still
	// here belongs to an upgrade whose process died mid-check.
	if err := client.Delete(ctx, kubeHealthCheckPod, true); err != nil {
		return "", fmt.Errorf("clearing the previous check pod: %w", err)
	}
	p.logf("%s: running %s as pod %s%s (up to %s, including the image pull)",
		step, ref, kubeHealthCheckPod, securitySuffix(security), shortDuration(timeout))
	if err := client.Create(ctx, kubeHealthCheckPod, kubeHealthCheckShape, k8s.PodOptions{
		Image: ref,
		// The image's own ENTRYPOINT is `grain` (Dockerfile), so this is
		// the same subcommand the other two paths run -- "schema-version"
		// on a real deployment.
		Command:  command,
		Security: security,
	}); err != nil {
		return "", err
	}
	// Deleted whatever happens: a check pod that outlived its check is
	// clutter in the namespace an operator has to explain to themselves,
	// and its logs have already been read by then. Detached from ctx
	// because the ordinary way this fails is the timeout above, which
	// would otherwise mean the pod that just proved something is wrong
	// is also the pod nothing cleans up.
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		_ = client.Delete(cleanup, kubeHealthCheckPod, false)
	}()

	st, err := client.WaitTerminal(ctx, kubeHealthCheckPod, time.Now().Add(timeout), 0)
	logs, logErr := client.Logs(ctx, kubeHealthCheckPod, k8s.SandboxContainer)
	if logErr == nil {
		if printed := strings.TrimSpace(logs); printed != "" {
			for _, line := range strings.Split(printed, "\n") {
				p.logf("%s: %s", step, line)
			}
		}
	}
	if err != nil {
		return "", err
	}
	if st.Phase != k8s.PhaseSucceeded {
		return "", fmt.Errorf("the check pod is %s%s%s", st.Phase, reasonSuffix(st.Reason), logSuffix(logs))
	}
	return st.ImageID, nil
}

// securitySuffix says in the progress log what a check pod is
// constrained by. Worth a few words on the line an operator reads: "the
// candidate starts" means something different when it started as root
// from when it started as the uid the rollout is about to hand it.
func securitySuffix(security k8s.PodSecurity) string {
	if security.Empty() {
		return ""
	}
	uid, ok := security.Pod["runAsUser"]
	if !ok {
		return " under this deployment's own security context"
	}
	if n, isNumber := uid.(float64); isNumber {
		// JSON has one number type, so a uid arrives as a float; print
		// it as the integer it is rather than as 1.00067e+09.
		return " as uid " + strconv.FormatFloat(n, 'f', -1, 64)
	}
	return fmt.Sprintf(" as uid %v", uid)
}

func reasonSuffix(reason string) string {
	if reason = strings.TrimSpace(reason); reason != "" {
		return " (" + reason + ")"
	}
	return ""
}

// logSuffix puts the last of what the check pod printed into the error
// itself, not only into the progress log: an upgrade that failed is read
// as one line on a pane far more often than as a whole log.
func logSuffix(logs string) string {
	logs = strings.TrimSpace(logs)
	if logs == "" {
		return ""
	}
	if i := strings.LastIndexByte(logs, '\n'); i >= 0 {
		logs = strings.TrimSpace(logs[i+1:])
	}
	if logs == "" {
		return ""
	}
	return ": " + logs
}

// kubeRunner is schema.go's schemaRunner for the Kubernetes path: a
// throwaway pod running the image this upgrade has health-checked and
// has not yet cut over to.
type kubeRunner struct {
	u   *Upgrader
	ref string
	// security is the Deployment's own, for the same reason the health
	// check pod runs under it: a candidate that cannot read its own
	// binary as uid 1000 should fail the question it is asked here, not
	// after the cut-over.
	security k8s.PodSecurity
	// pod is the pod's name, kubeAskPod when empty. A release notes
	// preview asks in one of its own, since it runs beside upgrades
	// rather than inside one.
	pod string
}

// name is the pod ask runs in.
func (r kubeRunner) name() string {
	if r.pod != "" {
		return r.pod
	}
	return kubeAskPod
}

// kubeAskPod is the name of the pod ask runs in -- separate from
// kubeHealthCheckPod so that a question asked after the check does not
// race the check pod's own deletion.
const kubeAskPod = "grain-upgrade-ask"

// ask runs args in the new build and hands back what it printed.
//
// It is a pod rather than a `docker run` for the obvious reason -- there
// is no container runtime in this pod to run one with -- and the answer
// comes back through `kubectl logs` rather than through a pipe, which is
// the one real difference: a build that logged something before
// answering has that in the same stream. newBuildSchema already refuses
// an answer that is not a plain number, so that goes wrong safely.
//
// The two kinds of failure are told apart the way image.go's ranAndFailed
// tells them apart for a `docker run`, and here the structure says it
// outright: everything up to the pod's own phase is the question never
// being put -- a kubectl that would not run, an API server that refused,
// a pull that never finished -- and only a pod that reached a terminal
// phase other than Succeeded is the build's own answer. The first kind
// is marked errCouldNotAsk, so a caller that carries on past a failed
// question (checkKubeFlags) can say which one it survived instead of
// blaming a build that was never asked.
func (r kubeRunner) ask(ctx context.Context, p *progress, args []string) (string, error) {
	client := r.u.cfg.Kube.client()
	pod := r.name()
	timeout := r.u.cfg.Kube.healthCheckTimeout()
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	if err := client.Delete(ctx, pod, true); err != nil {
		return "", fmt.Errorf("%w: %w", errCouldNotAsk, err)
	}
	if err := client.Create(ctx, pod, kubeHealthCheckShape, k8s.PodOptions{
		Image:    r.ref,
		Command:  append([]string{"grain"}, args...),
		Security: r.security,
	}); err != nil {
		return "", fmt.Errorf("%w: %w", errCouldNotAsk, err)
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		_ = client.Delete(cleanup, pod, false)
	}()

	st, err := client.WaitTerminal(ctx, pod, time.Now().Add(timeout), 0)
	if err != nil {
		return "", fmt.Errorf("%w: %w", errCouldNotAsk, err)
	}
	logs, logErr := client.Logs(ctx, pod, k8s.SandboxContainer)
	if st.Phase != k8s.PhaseSucceeded {
		return "", fmt.Errorf("the pod asking %s is %s%s%s", r.ref, st.Phase, reasonSuffix(st.Reason), logSuffix(logs))
	}
	if logErr != nil {
		// The build answered and this could not read the answer, which
		// is the machinery's failure and not the build's.
		return "", fmt.Errorf("%w: %w", errCouldNotAsk, logErr)
	}
	return logs, nil
}

// rehearse is the migration rehearsal, and this path cannot do one.
//
// The rehearsal's whole shape is "give the new build a directory of this
// host's filesystem, read-only, at the path it already has out here"
// (imageRunner.rehearse) -- which works because a docker deployment and
// the container it starts share a kernel and a filesystem. A pod in a
// cluster shares neither with this one, and grain's export lives on a
// ReadWriteOnce claim that only this pod can mount, so there is nothing
// to hand it.
//
// It returns an error rather than quietly succeeding, so that a
// deployment which does configure a migration check finds out here
// instead of cutting over on a rehearsal that never ran. cmd/grain
// configures none on this path, and schemaStep says so in the log when
// it skips it.
func (r kubeRunner) rehearse(ctx context.Context, p *progress, dir string, args []string) error {
	return fmt.Errorf("a Kubernetes upgrade cannot rehearse a migration: the rehearsal needs the new build to "+
		"read %s, which is on a volume only this pod can mount -- leave Schema.MigrateCheckArgs unset on this "+
		"path, or run the check by hand against a pod that mounts a copy", dir)
}

// ensureKubePriorityClasses is the priority-classes step: what it found,
// as the step's detail. What it could not do is a warning, not a
// failure -- see KubeConfig.PriorityClasses.
func ensureKubePriorityClasses(ctx context.Context, p *progress, client k8s.Client, names []string) string {
	report := client.EnsurePriorityClasses(ctx, names)
	var parts []string
	if len(report.Present) > 0 {
		parts = append(parts, strings.Join(report.Present, ", ")+" present")
	}
	if len(report.Created) > 0 {
		parts = append(parts, "created "+strings.Join(report.Created, ", "))
	}
	for _, name := range append(append([]string(nil), report.Missing...), report.Unknown...) {
		p.logf("%s: warning: %v", stepPriorityClasses, report.Errors[name])
	}
	if len(report.Missing) > 0 {
		parts = append(parts, "missing "+strings.Join(report.Missing, ", ")+
			" -- sandbox pods will be created without them, and an alert raised, until they exist")
	}
	if len(report.Unknown) > 0 {
		parts = append(parts, "could not read "+strings.Join(report.Unknown, ", "))
	}
	return strings.Join(parts, "; ")
}
