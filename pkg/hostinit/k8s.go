// k8s.go is PlatformK8sContainer's entire implementation: a preflight
// table reading the three prerequisites deploy/kubernetes/README.md's
// "Before you apply anything" section lists off the real cluster rather
// than guessing at them, then `kubectl apply -f grain.yaml` exactly
// once, a wait for the rollout, and a hand-off to ProvisionOverAPI --
// see docs/k8s-init-design.md for why the shape is this and not more.
//
// None of initFor's other steps apply here. There is no local docker,
// Go toolchain or granule CLI to build: the daemon this platform brings
// up runs as somebody else's cluster's Deployment, built by CI and
// pulled from a registry, not by this process.
//
// One deliberate divergence from docs/k8s-init-design.md, called out
// here because the design argues the opposite and the task building
// this platform asked for this shape instead: that design says grain
// init "does not start or manage a port-forward subprocess" and leaves
// reaching UIAddr to an operator who already has a route. This file
// does start one -- a `kubectl port-forward svc/<Service>` whose whole
// lifetime is this one Init call, started right before ProvisionOverAPI
// and stopped right after, never left running past this process exiting
// -- because otherwise this platform has no path to the one thing
// ProvisionOverAPI needs (something answering at UIAddr) without an
// operator performing a manual step on every single run, not just the
// first one. It remains overridable (K8sOptions.ManagePortForward)
// for an operator who already has their own route and wants this
// platform to leave it alone, which is the scenario the design was
// actually describing.
package hostinit

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// K8sOptions configures -platform=k8s-container: which manifests to
// apply, what the Namespace/Deployment/Service inside them are named,
// and the handful of things the preflight checks below read off the
// cluster itself rather than assume. See docs/k8s-init-design.md for
// why this platform exists and docs/grain-init-platforms.md for where
// it sits next to the other three. Ignored by every other Platform.
type K8sOptions struct {
	// Namespace is deploy/kubernetes/grain.yaml's own Namespace --
	// "grain" unless a caller edited the manifest to use a different
	// one. Empty means DefaultK8sNamespace.
	Namespace string
	// Deployment and Service are the names of the Deployment and
	// Service grain.yaml defines -- read back to watch the rollout and
	// to port-forward to. Empty means DefaultK8sDeployment /
	// DefaultK8sService.
	Deployment string
	Service    string

	// Manifest is the path to deploy/kubernetes/grain.yaml, applied
	// once (StepDone the first time, StepSkipped once
	// deployment/<Deployment> already exists -- never re-applied over a
	// deployment an operator may have since edited by hand). Empty
	// means <RepoRoot>/deploy/kubernetes/grain.yaml.
	Manifest string
	// NetworkPolicyManifest is deploy/kubernetes/networkpolicy.yaml,
	// applied only when RequireNetworkPolicy is set. Empty means
	// <RepoRoot>/deploy/kubernetes/networkpolicy.yaml.
	NetworkPolicyManifest string

	// SandboxImage is the image the preflight's throwaway pod pulls,
	// and must be the same one Manifest's own -k8s-sandbox-image names
	// (checkK8sSandboxImageMatchesManifest refuses otherwise). cmd/grain
	// reads it out of Manifest (ManifestK8sSandboxImage) unless the
	// operator names one, and falls back to its own build's stamped
	// default (pkg/cli/sandboximage.go) only for a manifest that
	// passes none -- not a constant in this package, which would go
	// stale the moment CI publishes a new image.
	// Required for -platform=k8s-container (Options.validate rejects
	// an empty one): there is no context-free default this package
	// could pick that would not be a guess.
	SandboxImage string
	// StorageClass is the name the storage-class preflight check's
	// scratch PVC requests. Empty tests the cluster's own default
	// class, the same thing grain.yaml's PVC does by leaving
	// storageClassName unset.
	StorageClass string
	// RunHostRWXClass, given, is the ReadWriteMany StorageClass this
	// deployment puts its run hosts' shared volume on
	// (deploy/kubernetes/runhost-rwx.yaml, -k8s-run-host-rwx) so run
	// pods may spread across nodes. Preflight refuses a gcsfuse-backed
	// class, where renames are not atomic. Empty is the default: run
	// directories on grain-data (ReadWriteOnce, checked above) with run
	// pods pinned to the daemon's node.
	RunHostRWXClass string

	// RequireNetworkPolicy, if true, is this deployment asserting that
	// it will dispatch network:none tasks and so needs a CNI that
	// actually enforces NetworkPolicy -- not merely accepts the
	// object. Preflight proves this by running a pod under
	// networkpolicy.yaml's own deny-all-egress shape and confirming
	// its egress is actually blocked, the same verification step
	// networkpolicy.yaml's own header tells an operator to perform by
	// hand. False (the default) skips that check and the apply of
	// networkpolicy.yaml both -- a deployment that never asks for
	// network isolation needs neither, and the daemon itself already
	// refuses a network:none task rather than silently running it
	// networked (orchestrator.K8sConfig.NetworkPolicyEnforced).
	RequireNetworkPolicy bool

	// Context and Kubeconfig, given, are passed to every kubectl call
	// this platform makes as --context/--kubeconfig, so it can target
	// a cluster other than kubectl's own current-context default.
	Context    string
	Kubeconfig string

	// ManagePortForward, unless it points at false, has this platform
	// start its own `kubectl port-forward svc/<Service> <UIAddr's
	// port>:80` immediately before ProvisionOverAPI and stop it
	// immediately after -- see this file's own doc comment for why.
	// False means an operator already has UIAddr reachable some other
	// way (their own port-forward, an Ingress, an internal route) and
	// this platform must not start a second one. Defaults to true.
	ManagePortForward *bool

	// RolloutTimeout bounds how long `kubectl rollout status` waits
	// for the Deployment to come up after apply. Empty means
	// DefaultK8sRolloutTimeout.
	RolloutTimeout time.Duration
	// PreflightTimeout bounds each individual `kubectl wait` the
	// preflight checks below run (the image-pull pod, the
	// storage-class PVC, the NetworkPolicy-enforcement pod). Empty
	// means DefaultK8sPreflightTimeout.
	PreflightTimeout time.Duration
}

// Defaults for K8sOptions, absent an operator override -- see each
// field's own doc comment above for what they name.
const (
	DefaultK8sNamespace  = "grain"
	DefaultK8sDeployment = "grain"
	DefaultK8sService    = "grain"

	DefaultK8sRolloutTimeout   = 5 * time.Minute
	DefaultK8sPreflightTimeout = 90 * time.Second
)

// namespace for every scratch object the preflight checks below create
// and delete again: the image-pull pod, the storage-class PVC, the
// NetworkPolicy-enforcement pod and policy. "default" rather than
// K8sOptions.Namespace because these checks all run before grain.yaml
// is ever applied -- the "grain" namespace itself may not exist yet,
// and these checks have nothing to do with it existing.
const k8sPreflightNamespace = "default"

// kubectlArgs prepends k's --kubeconfig/--context to args, so every
// kubectl invocation in this file agrees on which cluster it targets.
func kubectlArgs(k K8sOptions, args ...string) []string {
	var out []string
	if k.Kubeconfig != "" {
		out = append(out, "--kubeconfig="+k.Kubeconfig)
	}
	if k.Context != "" {
		out = append(out, "--context="+k.Context)
	}
	return append(out, args...)
}

// kubectlCommand is kubectlArgs rendered as a command line an operator
// can paste, and is what every Step.Command naming kubectl in this file
// and k8s_granule.go is built from. A hand-written "kubectl ..." string
// dropped -k8s-context/-k8s-kubeconfig, so a run against a non-current
// context told the operator to look at a different cluster from the
// one it had just checked.
func kubectlCommand(k K8sOptions, args ...string) string {
	words := append([]string{"kubectl"}, kubectlArgs(k, args...)...)
	for i, w := range words {
		words[i] = shellWord(w)
	}
	return strings.Join(words, " ")
}

// shellWord single-quotes w for a POSIX shell unless every byte of it is
// one a shell leaves alone, so an ordinary command line stays readable
// and a kubeconfig path with a space in it still pastes as one word.
func shellWord(w string) string {
	if w != "" && strings.Trim(w, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_-./:=@%+,") == "" {
		return w
	}
	return "'" + strings.ReplaceAll(w, "'", `'\''`) + "'"
}

// runKubectl is kubectlArgs plus the actual sys.Run call, the shape
// nearly every check and apply step below needs.
func runKubectl(ctx context.Context, sys System, k K8sOptions, args ...string) (string, error) {
	return sys.Run(ctx, "", nil, "kubectl", kubectlArgs(k, args...)...)
}

// deleteK8sScratchPod deletes a preflight check's own throwaway pod and
// actually waits for it to be gone -- confirmed necessary, not merely
// cautious, against a real cluster during this platform's own testing:
// a `--wait=false` delete returns the moment the object is marked for
// deletion, not once it is actually gone, and a pod's default
// terminationGracePeriodSeconds is 30s. A preflight run shortly after
// a previous one (an operator fixing one problem and re-running, or
// this package's own tests run back to back) would otherwise apply a
// same-named pod on top of one still terminating, which kubectl apply
// treats as a no-op patch rather than a fresh create -- so no pod is
// ever actually running when `kubectl wait --for=condition=Ready`
// polls it, and the object disappearing out from under that wait (the
// previous run's own delete finally completing) reports as a bare
// "NotFound", not as a timeout. Waiting here, once, is cheaper than
// chasing that race on every call site.
//
// --grace-period=1 because every scratch pod runs `sleep`, which as
// PID 1 ignores SIGTERM and so always sat out the full 30s default --
// on a kind cluster that was most of an otherwise idempotent re-run's
// wall time. Nothing in a scratch pod has anything to shut down.
func deleteK8sScratchPod(ctx context.Context, sys System, k K8sOptions, name string, timeout time.Duration) {
	runKubectl(ctx, sys, k, "delete", "pod", name, "-n", k8sPreflightNamespace,
		"--ignore-not-found", "--grace-period=1", "--timeout="+timeout.String())
}

// deleteK8sScratchManifest is deleteK8sScratchPod's counterpart for a
// check whose scratch objects came from a written manifest (-f) rather
// than `kubectl run` -- the storage-class and NetworkPolicy checks.
func deleteK8sScratchManifest(ctx context.Context, sys System, k K8sOptions, path string, timeout time.Duration) {
	runKubectl(ctx, sys, k, "delete", "-f", path, "--ignore-not-found", "--grace-period=1", "--timeout="+timeout.String())
}

// lastLine returns the last non-blank line of s, trimmed -- what a
// caller comparing kubectl's output against a single expected token
// ("yes"/"no", a jsonpath scalar) has to read instead of the whole
// trimmed string: System.Run returns stdout and stderr merged
// (its own doc comment says so), and `kubectl auth can-i` in
// particular writes a "Warning: resource ... is not namespace scoped"
// line to stderr before its yes/no verdict on stdout for any
// cluster-scoped resource -- real, not hypothetical, this exact
// command against a real cluster during this platform's own testing.
// An exact-equality comparison against the whole output would see
// that warning glued onto the front of "yes" and call it a "no".
func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

// writeK8sScratchManifest writes content to a fixed path under
// dataDir -- not a randomly-named temp file -- so that the exact
// `kubectl apply -f <path>` command line this run produces is the same
// on every run against the same DataDir, which is what lets a test
// assert on it with fakeSystem's exact-string command table rather
// than a pattern match.
func writeK8sScratchManifest(dataDir, name, content string) (string, error) {
	dir := filepath.Join(dataDir, "k8s-preflight")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		return "", err
	}
	return path, nil
}

// --- preflight ------------------------------------------------------

// k8sContainerChecks is PlatformK8sContainer's entire preflight list,
// in the order checksFor runs it: a gate (kubectl reaches the cluster
// at all), then every other prerequisite this platform depends on,
// each reporting its own Step either way so a cluster missing several
// of them hears about all of them in one run -- the same property
// every other platform's preflight list holds itself to. The last
// entry (the uid-1000 migration) aborts on its own, since applying a
// new manifest over an unmigrated volume is not safe to let the apply
// step attempt.
func k8sContainerChecks(k K8sOptions, dataDir string) []platformCheck {
	return []platformCheck{
		checkK8sClusterReachable(k),
		checkK8sNamespacePodSecurity(k),
		checkK8sPermissions(k),
		checkK8sSandboxImageMatchesManifest(k),
		checkK8sSandboxImagePullable(k, dataDir, k.PreflightTimeout),
		checkK8sStorageClassRWO(k, dataDir, k.PreflightTimeout),
		checkK8sRunHostRWXClass(k),
		checkK8sNetworkPolicyEnforced(k, dataDir, k.PreflightTimeout),
		checkK8sUid1000Migration(k),
	}
}

// checkK8sClusterReachable is this platform's gate, the same role
// checkGOOS plays for the two host platforms: every other check in
// this list runs kubectl against the real cluster, so a kubectl that
// is missing entirely or cannot reach the API server at all makes
// every one of them fail with the same unhelpful transport error
// rather than their own. `kubectl cluster-info` needs no namespace and
// no RBAC grant beyond the most basic discovery access, so a cluster
// this fails against is not one anything else here could have passed.
func checkK8sClusterReachable(k K8sOptions) platformCheck {
	return func(ctx context.Context, sys System, r *Result) (bool, bool) {
		return checkKubectlReachesCluster(ctx, sys, k, r)
	}
}

// checkKubectlReachesCluster is the gate both Kubernetes platforms
// share. A kubectl with no kubeconfig at all falls back to
// localhost:8080 and fails cluster-info with five repeated memcache.go
// discovery errors ahead of anything useful, so when cluster-info fails
// and no -k8s-context was named, `kubectl config current-context` is
// asked first, and its failure becomes a one-line refusal instead.
// cluster-info is still tried before that rather than after, so that
// kubectl running inside a pod (in-cluster config, no current context)
// passes.
func checkKubectlReachesCluster(ctx context.Context, sys System, k K8sOptions, r *Result) (bool, bool) {
	const name = "kubectl cluster access"
	_, err := runKubectl(ctx, sys, k, "cluster-info")
	if err == nil {
		r.add(Step{Name: name, Status: StepSkipped, Detail: "kubectl reaches the cluster"})
		return true, false
	}
	if k.Context == "" {
		if out, cerr := runKubectl(ctx, sys, k, "config", "current-context"); cerr != nil {
			said := lastLine(out)
			if said == "" {
				said = cerr.Error()
			}
			r.add(Step{Name: name, Status: StepRefused, Detail: fmt.Sprintf(
				"kubectl has no current context (`kubectl config current-context`: %s) -- grain init needs a "+
					"kubeconfig for the target cluster: set KUBECONFIG or pass -k8s-kubeconfig, and select the "+
					"cluster with `kubectl config use-context` or -k8s-context", said)})
			return false, true
		}
	}
	r.add(Step{Name: name, Status: StepRefused, Detail: fmt.Sprintf(
		"`kubectl cluster-info` failed: %v -- grain init needs a working kubectl already pointed at "+
			"the target cluster (installed, with a kubeconfig that has it as the current context, or "+
			"-k8s-context/-k8s-kubeconfig naming one) before anything about that cluster can be checked", err)})
	return false, true
}

// checkK8sNamespacePodSecurity is the refusal docs/k8s-init-design.md
// and this platform's own task both call out by name: a "grain"
// namespace whose pod-security.kubernetes.io/enforce is not "baseline"
// can run a privileged or host-mounting sandbox pod, which is exactly
// what PlatformK8sContainer must never allow -- a cluster like that is
// PlatformK8sGranule's, not this one, and this platform must refuse it
// rather than quietly proceed and hand away the isolation its whole
// trade is built on. A namespace that does not exist yet passes: apply
// (grain.yaml) is what creates it, with this label already set to
// "baseline".
func checkK8sNamespacePodSecurity(k K8sOptions) platformCheck {
	return func(ctx context.Context, sys System, r *Result) (bool, bool) {
		const name = "namespace pod-security level"
		out, err := runKubectl(ctx, sys, k, "get", "namespace", k.Namespace,
			"-o", `jsonpath={.metadata.labels.pod-security\.kubernetes\.io/enforce}`)
		if err != nil {
			r.add(Step{Name: name, Status: StepSkipped, Detail: fmt.Sprintf(
				"namespace %q does not exist yet -- deploy/kubernetes/grain.yaml creates it with "+
					"pod-security.kubernetes.io/enforce=baseline", k.Namespace)})
			return true, false
		}
		level := lastLine(out)
		if level == "" || level == "baseline" {
			r.add(Step{Name: name, Status: StepSkipped, Detail: fmt.Sprintf("namespace %q enforces %q", k.Namespace, level)})
			return true, false
		}
		r.add(Step{Name: name, Status: StepRefused, Detail: fmt.Sprintf(
			"namespace %q has pod-security.kubernetes.io/enforce=%q, not \"baseline\" -- a %q namespace can "+
				"run a privileged or host-mounting sandbox pod, which is exactly what -platform=%s must never "+
				"allow (that is what -platform=%s is for instead); grain init refuses to apply here rather than "+
				"tighten a label an operator may have relaxed on purpose, for a reason it cannot know",
			k.Namespace, level, level, PlatformK8sContainer, PlatformK8sGranule)})
		return false, false
	}
}

// k8sPerm is one verb/resource pair grain.yaml's own Role grants the
// daemon over itself, and so one the identity running grain init must
// also be able to create before `kubectl apply` of that manifest can
// possibly succeed.
type k8sPerm struct {
	resource   string
	namespaced bool
}

// k8sRequiredPerms is every object deploy/kubernetes/grain.yaml
// creates: Namespace, ServiceAccount, Role, RoleBinding, ClusterRole,
// ClusterRoleBinding (the read-only node grant the sandbox budget needs),
// PriorityClass (the priorities sandbox pods run at), PersistentVolumeClaim, ConfigMap, Deployment, Service -- named exactly as
// `kubectl auth can-i` expects a resource name, which is its plural,
// lower-cased form.
var k8sRequiredPerms = []k8sPerm{
	{"namespaces", false},
	{"serviceaccounts", true},
	{"roles.rbac.authorization.k8s.io", true},
	{"rolebindings.rbac.authorization.k8s.io", true},
	{"clusterroles.rbac.authorization.k8s.io", false},
	{"clusterrolebindings.rbac.authorization.k8s.io", false},
	{"priorityclasses.scheduling.k8s.io", false},
	{"persistentvolumeclaims", true},
	{"configmaps", true},
	{"deployments.apps", true},
	{"services", true},
}

// checkK8sPermissions confirms the identity grain init runs as can
// create everything grain.yaml defines, with `kubectl auth can-i`
// against each one individually -- a single `kubectl apply --dry-run`
// would answer the same question less precisely (one combined failure,
// not which objects specifically), and would not run cleanly against
// a cluster this identity cannot even read the apply's own
// Namespace/PVC objects on yet.
func checkK8sPermissions(k K8sOptions) platformCheck {
	return func(ctx context.Context, sys System, r *Result) (bool, bool) {
		const name = "permission to create what grain.yaml creates"
		var missing []string
		for _, p := range k8sRequiredPerms {
			args := []string{"auth", "can-i", "create", p.resource}
			if p.namespaced {
				args = append(args, "-n", k.Namespace)
			}
			out, err := runKubectl(ctx, sys, k, args...)
			if err != nil || lastLine(out) != "yes" {
				missing = append(missing, p.resource)
			}
		}
		if len(missing) == 0 {
			r.add(Step{Name: name, Status: StepSkipped, Detail: "the current kubeconfig identity can create every object grain.yaml defines"})
			return true, false
		}
		r.add(Step{Name: name, Status: StepManual, Detail: fmt.Sprintf(
			"the current kubeconfig identity cannot create: %s -- ask whoever administers this cluster for a "+
				"Role or ClusterRole granting create on these (namespace %q for the namespaced ones), or run "+
				"grain init as an identity that already has it", strings.Join(missing, ", "), k.Namespace)})
		return false, false
	}
}

// ManifestK8sSandboxImage returns the -k8s-sandbox-image argument the
// daemon in manifest (deploy/kubernetes/grain.yaml or an operator's
// copy) is started with, or "" if it passes none and so runs its own
// build's stamped default. It reads the manifest as text, one argument
// per line the way grain.yaml writes its args list, skipping comments
// -- not a YAML parse, which would need to know which container in
// which object is the daemon.
func ManifestK8sSandboxImage(manifest string) (string, error) {
	data, err := os.ReadFile(manifest)
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(data), "\n") {
		arg := strings.TrimSpace(line)
		if strings.HasPrefix(arg, "#") {
			continue
		}
		arg = strings.Trim(strings.TrimSpace(strings.TrimPrefix(arg, "- ")), `"'`)
		for _, flag := range []string{"-k8s-sandbox-image=", "--k8s-sandbox-image="} {
			if strings.HasPrefix(arg, flag) {
				return strings.TrimPrefix(arg, flag), nil
			}
		}
	}
	return "", nil
}

// checkK8sSandboxImageMatchesManifest refuses a -k8s-sandbox-image that
// differs from the one the manifest about to be applied starts the
// daemon with. The pull check below proves one image; the daemon then
// dispatches every task onto the other -- which is what a local `go
// build` did by default, its unstamped :latest against grain.yaml's
// :main. A manifest that names no image, or one that cannot be read
// (the apply step reports that), leaves nothing to compare.
func checkK8sSandboxImageMatchesManifest(k K8sOptions) platformCheck {
	return func(ctx context.Context, sys System, r *Result) (bool, bool) {
		const name = "sandbox image matches the manifest"
		image, err := ManifestK8sSandboxImage(k.Manifest)
		switch {
		case err != nil:
			r.add(Step{Name: name, Status: StepSkipped, Detail: fmt.Sprintf("could not read %s: %v", k.Manifest, err)})
			return true, false
		case image == "":
			r.add(Step{Name: name, Status: StepSkipped, Detail: fmt.Sprintf(
				"%s passes no -k8s-sandbox-image, so the daemon runs its own build's default", k.Manifest)})
			return true, false
		case image == k.SandboxImage:
			r.add(Step{Name: name, Status: StepSkipped, Detail: fmt.Sprintf("%q, as %s runs it", image, k.Manifest)})
			return true, false
		}
		r.add(Step{Name: name, Status: StepRefused, Detail: fmt.Sprintf(
			"-k8s-sandbox-image is %q but %s starts the daemon with -k8s-sandbox-image=%q -- the pull check "+
				"would prove one image while every task runs on the other; leave -k8s-sandbox-image unset "+
				"(grain init then reads it from the manifest), pass the manifest's own, or edit the manifest",
			k.SandboxImage, k.Manifest, image)})
		return false, false
	}
}

// k8sPreflightImagePod is the throwaway pod name the sandbox-image
// check creates and deletes -- fixed, not generated per run, both so
// a second run cleans up a first run's leftovers (the same name) and
// so the command line is deterministic for a test's exact-match table.
const k8sPreflightImagePod = "grain-init-preflight-image"

// checkK8sSandboxImagePullable is the first of
// docs/k8s-init-design.md's three "before you apply anything"
// prerequisites: create a throwaway pod running -k8s-sandbox-image
// with no command but sleep, wait for it to reach Ready, then delete
// it -- the same "read the real thing rather than guess" instinct
// ensureSandboxNetwork already applies on the host platforms, since a
// registry HEAD request can be right about authentication and wrong
// about a node-local pull secret or mirror.
func checkK8sSandboxImagePullable(k K8sOptions, dataDir string, timeout time.Duration) platformCheck {
	return func(ctx context.Context, sys System, r *Result) (bool, bool) {
		const name = "sandbox image pulls"
		pod := k8sPreflightImagePod
		cleanup := func() { deleteK8sScratchPod(ctx, sys, k, pod, timeout) }
		// A run of grain init killed mid-preflight (an operator's
		// Ctrl-C, this process losing its own parent) never reaches
		// cleanup's own defer -- confirmed against a real cluster,
		// where it left exactly this pod behind and the next run's
		// `kubectl run` failed with AlreadyExists rather than ever
		// reaching the pull check it exists to perform. Best-effort,
		// blocking: a genuinely fresh cluster has nothing to delete
		// here, so this costs one quick "not found" round trip.
		cleanup()
		if _, err := runKubectl(ctx, sys, k, "run", pod, "-n", k8sPreflightNamespace,
			"--image="+k.SandboxImage, "--restart=Never", "--command", "--", "sleep", "3600"); err != nil {
			r.add(Step{Name: name, Status: StepRefused, Detail: fmt.Sprintf(
				"could not even create a throwaway pod for %q in namespace %q: %v", k.SandboxImage, k8sPreflightNamespace, err)})
			return false, false
		}
		// A failed check keeps its pod, so the describe command it prints
		// has something to describe: deleting it here answered that
		// command with NotFound. The delete-before-create above is what
		// removes it, on the next run.
		failed := false
		defer func() {
			if !failed {
				cleanup()
			}
		}()
		if _, err := runKubectl(ctx, sys, k, "wait", "pod/"+pod, "-n", k8sPreflightNamespace,
			"--for=condition=Ready", "--timeout="+timeout.String()); err != nil {
			failed = true
			reason, _ := runKubectl(ctx, sys, k, "get", "pod", pod, "-n", k8sPreflightNamespace,
				"-o", "jsonpath={.status.containerStatuses[0].state.waiting.reason}")
			r.add(Step{Name: name, Status: StepManual, Detail: fmt.Sprintf(
				"%q did not reach Running in namespace %q within %s (%s): %v -- pod/%s is "+
					"left in place for the command below; the next grain init deletes it before checking again",
				k.SandboxImage, k8sPreflightNamespace, timeout, strings.TrimSpace(reason), err, pod),
				Command: kubectlCommand(k, "-n", k8sPreflightNamespace, "describe", "pod", pod)})
			return false, false
		}
		r.add(Step{Name: name, Status: StepSkipped, Detail: fmt.Sprintf("%q pulled and started in namespace %q", k.SandboxImage, k8sPreflightNamespace)})
		return true, false
	}
}

// k8sPreflightPVC and k8sPreflightPVCPod are the scratch
// PersistentVolumeClaim and the pod that mounts it, both fixed names
// for the same reason k8sPreflightImagePod is.
const (
	k8sPreflightPVC    = "grain-init-preflight-pvc"
	k8sPreflightPVCPod = "grain-init-preflight-pvc-pod"
)

// k8sScratchPVCManifest is a 1Mi PVC against k.StorageClass (the
// cluster's own default class if empty, the same thing grain.yaml's
// PVC does by leaving storageClassName unset), paired with a pod that
// mounts it. The pod is not optional decoration: a StorageClass whose
// volumeBindingMode is WaitForFirstConsumer -- the default for kind's
// own "standard" class, and for several real cloud provisioners
// (EBS gp2/gp3 among them) -- never binds a PVC that nothing has
// scheduled a consumer for, so a bare PVC would time out on exactly
// the clusters most likely to run this platform. Waiting for the pod
// to become Ready proves the bind, the attach and the mount all
// actually happened, which is a stronger claim than "Bound" alone.
func k8sScratchPVCManifest(k K8sOptions, image string) string {
	storageClassLine := ""
	if k.StorageClass != "" {
		storageClassLine = "\n  storageClassName: " + k.StorageClass
	}
	return fmt.Sprintf(`apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: %[1]s
  namespace: %[2]s
spec:
  accessModes: ["ReadWriteOnce"]
  resources:
    requests:
      storage: 1Mi%[3]s
---
apiVersion: v1
kind: Pod
metadata:
  name: %[4]s
  namespace: %[2]s
spec:
  restartPolicy: Never
  containers:
    - name: check
      image: %[5]s
      command: ["sleep", "3600"]
      volumeMounts:
        - name: scratch
          mountPath: /mnt/scratch
  volumes:
    - name: scratch
      persistentVolumeClaim:
        claimName: %[1]s
`, k8sPreflightPVC, k8sPreflightNamespace, storageClassLine, k8sPreflightPVCPod, image)
}

// checkK8sStorageClassRWO is the hardest of the three prerequisites to
// check honestly, per docs/k8s-init-design.md: a StorageClass does not
// declare access modes itself, a PVC's binding does. This creates a
// scratch PVC against the named class requesting ReadWriteOnce, plus a
// pod that mounts it, waits for that pod to become Ready, then deletes
// both -- anything less than an actual bind and mount is a guess the
// README's own wording ("a storage class offering ReadWriteOnce
// volumes") does not license.
func checkK8sStorageClassRWO(k K8sOptions, dataDir string, timeout time.Duration) platformCheck {
	return func(ctx context.Context, sys System, r *Result) (bool, bool) {
		const name = "storage class offers ReadWriteOnce"
		path, err := writeK8sScratchManifest(dataDir, "storageclass-check.yaml", k8sScratchPVCManifest(k, k.SandboxImage))
		if err != nil {
			r.add(Step{Name: name, Status: StepManual, Detail: fmt.Sprintf("could not write a scratch manifest: %v", err)})
			return false, false
		}
		cleanup := func() { deleteK8sScratchManifest(ctx, sys, k, path, timeout) }
		// See checkK8sSandboxImagePullable's own comment on the same
		// line: best-effort, blocking delete-before-create, so a
		// previous run killed mid-check cannot wedge every later one.
		cleanup()
		if _, err := runKubectl(ctx, sys, k, "apply", "-f", path); err != nil {
			r.add(Step{Name: name, Status: StepManual, Detail: fmt.Sprintf("kubectl apply -f %s: %v", path, err)})
			return false, false
		}
		// Kept on failure for the describe commands below to read -- see
		// checkK8sSandboxImagePullable's own comment on the same line.
		failed := false
		defer func() {
			if !failed {
				cleanup()
			}
		}()
		const kept = " -- the scratch PVC and pod are left in place for the commands below; the next grain " +
			"init deletes them before checking again"
		if _, err := runKubectl(ctx, sys, k, "wait", "pod/"+k8sPreflightPVCPod, "-n", k8sPreflightNamespace,
			"--for=condition=Ready", "--timeout="+timeout.String()); err != nil {
			failed = true
			className := k.StorageClass
			if className == "" {
				className = "(cluster default)"
			}
			// The consuming pod runs -k8s-sandbox-image, so an image that
			// does not pull also leaves this pod short of Ready -- seen
			// against a real kind cluster, where an ImagePullBackOff
			// sandbox image was reported here as a storage class that
			// could not give ReadWriteOnce. A PVC that did bind has
			// answered this check's own question; name the pod's reason
			// instead of blaming the class.
			if phase, perr := runKubectl(ctx, sys, k, "get", "pvc", k8sPreflightPVC, "-n", k8sPreflightNamespace,
				"-o", "jsonpath={.status.phase}"); perr == nil && strings.TrimSpace(phase) == "Bound" {
				reason, _ := runKubectl(ctx, sys, k, "get", "pod", k8sPreflightPVCPod, "-n", k8sPreflightNamespace,
					"-o", "jsonpath={.status.containerStatuses[0].state.waiting.reason}")
				r.add(Step{Name: name, Status: StepManual, Detail: fmt.Sprintf(
					"a scratch PersistentVolumeClaim against storage class %s bound, but the pod mounting it "+
						"(image %q) did not become Ready within %s (%s) -- the class looks fine; fix whatever "+
						"stops that pod starting (see \"sandbox image pulls\") and re-run"+kept,
					className, k.SandboxImage, timeout, strings.TrimSpace(reason)),
					Command: kubectlCommand(k, "-n", k8sPreflightNamespace, "describe", "pod", k8sPreflightPVCPod)})
				return false, false
			}
			r.add(Step{Name: name, Status: StepManual, Detail: fmt.Sprintf(
				"a scratch PersistentVolumeClaim against storage class %s did not bind and mount within %s: "+
					"%v -- grain's state is a single-writer SQLite database and needs a class that actually "+
					"gives ReadWriteOnce volumes"+kept, className, timeout, err),
				Command: kubectlCommand(k, "-n", k8sPreflightNamespace, "describe", "pvc", k8sPreflightPVC) + "; " +
					kubectlCommand(k, "-n", k8sPreflightNamespace, "describe", "pod", k8sPreflightPVCPod)})
			return false, false
		}
		r.add(Step{Name: name, Status: StepSkipped, Detail: "a scratch ReadWriteOnce PVC bound and mounted"})
		return true, false
	}
}

// checkK8sRunHostRWXClass reads the provisioner of the class
// RunHostRWXClass names and refuses one backed by gcsfuse
// (docs/non-disruptive-upgrade.md, "The shared credential directory"):
// the spool's exit.json and a refreshing CLI's login are both written by
// rename, and a rename there is not atomic. It does not bind a scratch
// claim, as the ReadWriteOnce check does: the daemon checks the bound
// claim's access modes itself at startup (runhost.CheckClaim).
func checkK8sRunHostRWXClass(k K8sOptions) platformCheck {
	return func(ctx context.Context, sys System, r *Result) (bool, bool) {
		const name = "run-host storage class is ReadWriteMany-safe"
		if k.RunHostRWXClass == "" {
			return true, false
		}
		out, err := runKubectl(ctx, sys, k, "get", "storageclass", k.RunHostRWXClass, "-o", "go-template={{.provisioner}}")
		if err != nil {
			r.add(Step{Name: name, Status: StepManual, Detail: fmt.Sprintf("reading storage class %q: %v",
				k.RunHostRWXClass, err), Command: kubectlCommand(k, "get", "storageclass")})
			return false, false
		}
		provisioner := strings.TrimSpace(out)
		if strings.Contains(strings.ToLower(provisioner+" "+k.RunHostRWXClass), "gcsfuse") {
			r.add(Step{Name: name, Status: StepManual, Detail: fmt.Sprintf(
				"storage class %q is gcsfuse-backed (%s): renames there are not atomic, and run hosts' spools and "+
					"shared logins depend on atomic renames -- use Filestore (filestore.csi.storage.gke.io) or NFS",
				k.RunHostRWXClass, provisioner)})
			return false, false
		}
		r.add(Step{Name: name, Status: StepSkipped, Detail: fmt.Sprintf("storage class %q is provisioned by %s",
			k.RunHostRWXClass, provisioner)})
		return true, false
	}
}

// k8sPreflightNetpolName and k8sPreflightNetpolPod are the scratch
// NetworkPolicy and pod the CNI-enforcement check creates and deletes.
const (
	k8sPreflightNetpolName = "grain-init-preflight-netpol"
	k8sPreflightNetpolPod  = "grain-init-preflight-netpol-pod"
)

// k8sScratchNetpolManifest denies all egress from any pod carrying its
// own podSelector's label -- the same shape
// deploy/kubernetes/networkpolicy.yaml applies to real sandbox pods,
// scaled down to a self-contained check that needs neither the "grain"
// namespace nor a running daemon to mean something.
func k8sScratchNetpolManifest() string {
	return fmt.Sprintf(`apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: %s
  namespace: %s
spec:
  podSelector:
    matchLabels:
      grain.dev/init-preflight: network-none
  policyTypes: ["Egress"]
  egress: []
`, k8sPreflightNetpolName, k8sPreflightNamespace)
}

// checkK8sNetworkPolicyEnforced is docs/k8s-init-design.md's second
// prerequisite, conditional on k.RequireNetworkPolicy: a cluster whose
// CNI accepts NetworkPolicy objects but does not enforce them is
// indistinguishable from one that does by reading the object back, so
// this runs networkpolicy.yaml's own header instructions for real --
// apply a deny-all-egress policy, run a pod under it, and confirm that
// pod actually cannot reach the internet -- rather than trust a
// "kubectl get networkpolicy" that would succeed on either kind of
// cluster. Skipped entirely, with no kubectl call at all, when
// RequireNetworkPolicy is false: a deployment that never asks for
// network isolation does not need this, the same way the README says.
func checkK8sNetworkPolicyEnforced(k K8sOptions, dataDir string, timeout time.Duration) platformCheck {
	return func(ctx context.Context, sys System, r *Result) (bool, bool) {
		const name = "CNI enforces NetworkPolicy"
		if !k.RequireNetworkPolicy {
			r.add(Step{Name: name, Status: StepSkipped, Detail: "skipped: -k8s-require-network-policy is not set -- this deployment is not asserting it will ever dispatch a network:none task"})
			return true, false
		}
		path, err := writeK8sScratchManifest(dataDir, "networkpolicy-check.yaml", k8sScratchNetpolManifest())
		if err != nil {
			r.add(Step{Name: name, Status: StepManual, Detail: fmt.Sprintf("could not write a scratch manifest: %v", err)})
			return false, false
		}
		cleanupPod := func() { deleteK8sScratchPod(ctx, sys, k, k8sPreflightNetpolPod, timeout) }
		cleanupPolicy := func() { deleteK8sScratchManifest(ctx, sys, k, path, timeout) }
		// See checkK8sSandboxImagePullable's own comment: best-effort,
		// blocking delete-before-create against a previous run's
		// leftovers.
		cleanupPod()
		cleanupPolicy()
		if _, err := runKubectl(ctx, sys, k, "apply", "-f", path); err != nil {
			r.add(Step{Name: name, Status: StepManual, Detail: fmt.Sprintf("kubectl apply -f %s: %v", path, err)})
			return false, false
		}
		defer cleanupPolicy()
		if _, err := runKubectl(ctx, sys, k, "run", k8sPreflightNetpolPod, "-n", k8sPreflightNamespace,
			"--image="+k.SandboxImage, "--restart=Never", "--labels=grain.dev/init-preflight=network-none",
			"--command", "--", "sleep", "60"); err != nil {
			r.add(Step{Name: name, Status: StepManual, Detail: fmt.Sprintf("could not create a test pod: %v", err)})
			return false, false
		}
		defer cleanupPod()
		if _, err := runKubectl(ctx, sys, k, "wait", "pod/"+k8sPreflightNetpolPod, "-n", k8sPreflightNamespace,
			"--for=condition=Ready", "--timeout="+timeout.String()); err != nil {
			r.add(Step{Name: name, Status: StepManual, Detail: fmt.Sprintf("the test pod never became ready: %v", err)})
			return false, false
		}
		_, execErr := runKubectl(ctx, sys, k, "exec", "-n", k8sPreflightNamespace, k8sPreflightNetpolPod, "--",
			"curl", "-m", "3", "-s", "-o", "/dev/null", "http://1.1.1.1")
		if execErr == nil {
			r.add(Step{Name: name, Status: StepRefused, Detail: fmt.Sprintf(
				"a pod under a deny-all-egress NetworkPolicy still reached the internet -- this cluster's CNI " +
					"accepts NetworkPolicy objects but does not enforce them, so -k8s-require-network-policy " +
					"cannot be honored here: a network:none task would still be refused by grain itself " +
					"(orchestrator.K8sConfig.NetworkPolicyEnforced), which is safe, but there is no point " +
					"asking for it until this cluster runs a policy-enforcing CNI (Calico, Cilium, Antrea, " +
					"Weave, or GKE's own dataplane)")})
			return false, false
		}
		r.add(Step{Name: name, Status: StepSkipped, Detail: "a pod under a deny-all-egress NetworkPolicy could not reach the internet -- this cluster's CNI enforces it"})
		return true, false
	}
}

// checkK8sUid1000Migration detects deploy/kubernetes/README.md's
// uid-1000 migration condition -- a deployment/<Deployment> whose pod
// template predates the current securityContext, still running as
// root -- and reports it in exactly the [manual] idiom this package
// uses everywhere else: a precise command, not performed. Unlike
// README.md's own wording ("a volume written by the root this used to
// be"), this does not inspect the volume's actual file ownership --
// there is no supportable way to do that without mounting it a second
// time, which ReadWriteOnce forbids while the daemon's own pod still
// holds it -- so it reads the one thing it safely can: whether the
// existing Deployment's securityContext already names uid 1000. That
// is an honest proxy, not a guess about file ownership: a Deployment
// that already asks for uid 1000 either already ran the migration or
// never needed it, either way with nothing left for this check to find.
func checkK8sUid1000Migration(k K8sOptions) platformCheck {
	return func(ctx context.Context, sys System, r *Result) (bool, bool) {
		const name = "uid-1000 migration"
		out, err := runKubectl(ctx, sys, k, "get", "deployment", k.Deployment, "-n", k.Namespace,
			"-o", "jsonpath={.spec.template.spec.securityContext.runAsUser}")
		if err != nil {
			r.add(Step{Name: name, Status: StepSkipped, Detail: fmt.Sprintf("deployment/%s does not exist yet -- nothing to migrate", k.Deployment)})
			return true, false
		}
		if lastLine(out) == "1000" {
			r.add(Step{Name: name, Status: StepSkipped, Detail: fmt.Sprintf("deployment/%s already runs as uid 1000", k.Deployment)})
			return true, false
		}
		cmd := fmt.Sprintf(`%[3]s scale deployment/%[2]s --replicas=0
%[3]s rollout status deployment/%[2]s --timeout=2m
%[3]s apply -f - <<'EOF'
apiVersion: batch/v1
kind: Job
metadata:
  name: grain-data-chown
spec:
  backoffLimit: 1
  template:
    spec:
      restartPolicy: Never
      securityContext:
        runAsUser: 0
      containers:
        - name: chown
          image: ghcr.io/bwsalmon/grain/grain:main
          command: ["/bin/sh", "-c", "chown -R 1000:1000 /data"]
          volumeMounts:
            - name: data
              mountPath: /data
      volumes:
        - name: data
          persistentVolumeClaim:
            claimName: grain-data
EOF
%[3]s wait --for=condition=complete job/grain-data-chown --timeout=30m
%[3]s delete job grain-data-chown`, k.Namespace, k.Deployment, kubectlCommand(k, "-n", k.Namespace))
		r.add(Step{Name: name, Status: StepManual, Detail: fmt.Sprintf(
			"deployment/%s predates the uid-1000 securityContext (runAsUser=%q) -- its volume is likely still "+
				"owned by uid 0; deploy/kubernetes/README.md's one-off chown migration has to run before grain "+
				"init applies a manifest that starts the daemon as uid 1000, or the new pod's init container "+
				"fails making /data/home writable", k.Deployment, lastLine(out)),
			Command: cmd})
		return false, true
	}
}

// --- apply, rollout, reach, provision --------------------------------

// initForK8sContainer is PlatformK8sContainer's entire post-preflight
// path, replacing every one of initFor's host-build steps: apply
// deploy/kubernetes/grain.yaml once, apply networkpolicy.yaml if this
// deployment asked for it, wait for the rollout, establish a route to
// UIAddr, then hand off to ProvisionOverAPI unchanged -- "the reuse
// this proposal is about is this one function call"
// (docs/k8s-init-design.md).
func initForK8sContainer(ctx context.Context, sys System, opts Options, r *Result) {
	applied := ensureK8sManifest(ctx, sys, opts, r)

	if opts.K8s.RequireNetworkPolicy {
		ensureK8sNetworkPolicyManifest(ctx, sys, opts, r)
	} else {
		r.add(Step{Name: "apply the sandbox NetworkPolicy", Status: StepSkipped,
			Detail: "skipped: -k8s-require-network-policy is not set"})
	}

	rolledOut := false
	if applied {
		rolledOut = ensureK8sRollout(ctx, sys, opts, r)
	} else {
		r.add(Step{Name: "wait for the rollout", Status: StepSkipped, Detail: "skipped: the manifest was not applied"})
	}

	attempted := false
	var stop func()
	if rolledOut {
		stop, attempted = establishK8sReachability(ctx, opts, r)
	} else {
		r.add(Step{Name: "reach the daemon's API", Status: StepSkipped, Detail: "skipped: the rollout did not finish"})
	}
	if stop != nil {
		defer stop()
	}

	ProvisionOverAPI(ctx, sys, opts, r, attempted)
	checkK8sNodeFitsSandbox(ctx, sys, opts, r, attempted)
	ensureK8sTeamGitHubCredential(ctx, sys, opts, r)
}

// ensureK8sTeamGitHubCredential stands in for ensureCredentialLadder on
// the two Kubernetes platforms, which cannot run it: that step writes
// secrets/github/<name>.token under -data-dir, and here -data-dir is a
// scratch directory on this machine while the daemon's secrets live on
// the cluster's PersistentVolume. Before this, -github-token-file was
// accepted and silently dropped -- grain init exited 0 against a kind
// cluster, and the daemon then refused every task into -github-repo
// with `team "default" has no GitHub credential`.
//
// Instead the token becomes the default team's own git connection to
// github.com, over the same loopback-trusted team API the Team settings
// Git pane uses (POST /api/deployment/git-connections; the single team
// token this used to PUT is retired, grain/task-3929). A team that
// already has a credential, from an earlier run or a team admin, is left
// alone.
func ensureK8sTeamGitHubCredential(ctx context.Context, sys System, opts Options, r *Result) {
	const name = "give the deployment the GitHub token"
	if opts.GitHubToken == "" {
		r.add(Step{Name: name, Status: StepSkipped,
			Detail: "no token given -- set one in Settings -> Git (/settings/git)"})
		return
	}
	teamURL := deploymentURL(opts)
	url := teamURL + "/git-connections"
	if opts.DryRun {
		r.add(Step{Name: name, Status: StepWould,
			Detail: fmt.Sprintf("would POST the token to %s if the default team has no credential yet", url)})
		return
	}
	status, body, err := sys.HTTPRequest(ctx, http.MethodGet, teamURL, nil)
	if err != nil || status != http.StatusOK {
		r.add(Step{Name: name, Status: StepSkipped, Detail: fmt.Sprintf(
			"the daemon's API is not reachable (GET %s: status %d: %v) -- see the steps above", teamURL, status, err)})
		return
	}
	var team teamSettings
	if err := json.Unmarshal(body, &team); err != nil {
		r.add(Step{Name: name, Status: StepManual, Detail: fmt.Sprintf("parsing %s's response: %v", teamURL, err)})
		return
	}
	if has, what := team.gitCredential(); has {
		r.add(Step{Name: name, Status: StepSkipped, Detail: fmt.Sprintf("the default team already has one (%s)", what)})
		return
	}
	reqBody, err := json.Marshal(struct {
		Type  string `json:"type"`
		Host  string `json:"host"`
		Value string `json:"value"`
	}{"pat", "github.com", opts.GitHubToken})
	if err != nil {
		r.add(Step{Name: name, Status: StepManual, Detail: fmt.Sprintf("could not build the request: %v", err)})
		return
	}
	status, respBody, err := sys.HTTPRequest(ctx, http.MethodPost, url, reqBody)
	if err != nil || status != http.StatusOK {
		// Never echo the token into a printed command: point at the
		// pane that takes it instead.
		r.add(Step{Name: name, Status: StepManual, Detail: fmt.Sprintf(
			"POST %s: status %d: %v: %s -- sign in and paste the token in Settings -> Git (/settings/git)",
			url, status, err, strings.TrimSpace(string(respBody)))})
		return
	}
	r.add(Step{Name: name, Status: StepDone, Detail: "stored as the deployment's git connection to github.com"})
}

// ensureK8sManifest is the get-or-create bar this whole package holds
// every step to, applied to a manifest instead of a binary: StepDone
// the first time, StepSkipped once deployment/<Deployment> already
// exists in the namespace, so a second run of grain init never
// re-applies over a deployment an operator may have since edited by
// hand (docs/k8s-init-design.md's own argument for this shape).
func ensureK8sManifest(ctx context.Context, sys System, opts Options, r *Result) bool {
	const name = "apply the grain manifest"
	k := opts.K8s
	cmd := kubectlCommand(k, "apply", "-f", k.Manifest)
	if opts.DryRun {
		r.add(Step{Name: name, Status: StepWould, Detail: fmt.Sprintf("would apply %s if deployment/%s does not already exist", k.Manifest, k.Deployment), Command: cmd})
		return false
	}
	if _, err := runKubectl(ctx, sys, k, "get", "deployment", k.Deployment, "-n", k.Namespace); err == nil {
		r.add(Step{Name: name, Status: StepSkipped, Detail: fmt.Sprintf(
			"deployment/%s already exists in namespace %q -- grain init applies the manifest once and leaves "+
				"upgrades to `kubectl -n %s rollout restart deployment/%s` or the Upgrade pane, never "+
				"re-applying over a deployment an operator may have edited by hand",
			k.Deployment, k.Namespace, k.Namespace, k.Deployment)})
		return true
	}
	if _, err := runKubectl(ctx, sys, k, "apply", "-f", k.Manifest); err != nil {
		r.add(Step{Name: name, Status: StepManual, Detail: fmt.Sprintf("kubectl apply -f %s failed: %v", k.Manifest, err), Command: cmd})
		return false
	}
	r.add(Step{Name: name, Status: StepDone, Detail: fmt.Sprintf("applied %s", k.Manifest)})
	return true
}

// ensureK8sNetworkPolicyManifest applies networkpolicy.yaml the same
// get-or-create way ensureK8sManifest applies grain.yaml, only called
// at all when opts.K8s.RequireNetworkPolicy is set.
func ensureK8sNetworkPolicyManifest(ctx context.Context, sys System, opts Options, r *Result) bool {
	const name = "apply the sandbox NetworkPolicy"
	k := opts.K8s
	cmd := kubectlCommand(k, "apply", "-f", k.NetworkPolicyManifest)
	if opts.DryRun {
		r.add(Step{Name: name, Status: StepWould, Detail: fmt.Sprintf("would apply %s", k.NetworkPolicyManifest), Command: cmd})
		return false
	}
	if _, err := runKubectl(ctx, sys, k, "get", "networkpolicy", "grain-sandbox-network-none", "-n", k.Namespace); err == nil {
		r.add(Step{Name: name, Status: StepSkipped, Detail: "already applied"})
		return true
	}
	if _, err := runKubectl(ctx, sys, k, "apply", "-f", k.NetworkPolicyManifest); err != nil {
		r.add(Step{Name: name, Status: StepManual, Detail: fmt.Sprintf("kubectl apply -f %s failed: %v", k.NetworkPolicyManifest, err), Command: cmd})
		return false
	}
	r.add(Step{Name: name, Status: StepDone, Detail: fmt.Sprintf("applied %s", k.NetworkPolicyManifest)})
	return true
}

// ensureK8sRollout waits for the Deployment's rollout the way an
// operator would by hand (deploy/kubernetes/README.md's own
// `kubectl ... rollout status`) -- grain init does not reimplement
// that wait with its own polling loop, matching
// docs/k8s-init-design.md's "preflight, apply once, provision" and
// its explicit refusal to duplicate kubectl's own rollout machinery.
func ensureK8sRollout(ctx context.Context, sys System, opts Options, r *Result) bool {
	const name = "wait for the rollout"
	k := opts.K8s
	if opts.DryRun {
		r.add(Step{Name: name, Status: StepWould, Detail: "would wait for the rollout to finish"})
		return false
	}
	out, err := runKubectl(ctx, sys, k, "rollout", "status", "deployment/"+k.Deployment, "-n", k.Namespace,
		"--timeout="+k.RolloutTimeout.String())
	if err != nil {
		r.add(Step{Name: name, Status: StepManual, Detail: fmt.Sprintf("kubectl rollout status: %v: %s", err, out),
			Command: kubectlCommand(k, "-n", k.Namespace, "rollout", "status", "deployment/"+k.Deployment)})
		return false
	}
	r.add(Step{Name: name, Status: StepDone, Detail: "rollout complete"})
	return true
}

// establishK8sReachability is where this platform diverges from
// docs/k8s-init-design.md -- see this file's own top-of-file doc
// comment for why. opts.K8sPortForward is nil when
// K8sOptions.ManagePortForward is false (an operator's own route is
// already in place) or when -dry-run; either way this reports the
// step and returns ok=true/false without starting anything, same as
// every other DryRun/Skipped branch in this package.
func establishK8sReachability(ctx context.Context, opts Options, r *Result) (stop func(), ok bool) {
	const name = "reach the daemon's API"
	k := opts.K8s
	if opts.DryRun {
		r.add(Step{Name: name, Status: StepWould, Detail: fmt.Sprintf(
			"would run kubectl -n %s port-forward svc/%s <port>:80 and point -ui-addr at it", k.Namespace, k.Service)})
		return nil, false
	}
	if opts.K8sPortForward == nil {
		r.add(Step{Name: name, Status: StepSkipped, Detail: fmt.Sprintf(
			"-k8s-manage-port-forward=false -- assuming %s is already reachable (an operator-managed kubectl "+
				"port-forward, Ingress, or internal route)", opts.UIAddr)})
		return nil, true
	}
	stop, err := opts.K8sPortForward(ctx, opts)
	if err != nil {
		r.add(Step{Name: name, Status: StepManual, Detail: fmt.Sprintf("could not establish a route to the daemon: %v", err),
			Command: kubectlCommand(k, "-n", k.Namespace, "port-forward", "svc/"+k.Service, portOf(opts.UIAddr)+":80")})
		return nil, false
	}
	r.add(Step{Name: name, Status: StepDone, Detail: fmt.Sprintf("kubectl port-forward svc/%s -> %s", k.Service, opts.UIAddr)})
	return stop, true
}

// portOf returns addr's port, or addr itself if it has none -- used
// only to build a Step.Command an operator can read, so a malformed
// UIAddr here produces a slightly odd suggested command rather than an
// error this is not the place to surface.
func portOf(addr string) string {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	return port
}

// k8sPortForwardReadyTimeout bounds how long k8sPortForwardViaKubectl
// waits for `kubectl port-forward` to report it is listening before
// giving up on it.
const k8sPortForwardReadyTimeout = 15 * time.Second

// k8sPortForwardViaKubectl is the default Options.K8sPortForward for
// -platform=k8s-container: `kubectl port-forward svc/<Service>
// <UIAddr's port>:80`, started here and stopped by the returned stop
// func, with a lifetime scoped to this one Init call -- see this
// file's own top-of-file doc comment for why this platform owns that
// much and no more. This talks to the real kubectl directly
// (os/exec), not through the System interface: System is deliberately
// kept to Run/HTTP/IsRoot (see its own doc comment), and a
// long-lived, streamed-output subprocess is a different shape of
// effect than Run's "block until exit, return combined output" -- a
// test substitutes its own Options.K8sPortForward instead of a fourth
// System capability.
func k8sPortForwardViaKubectl(ctx context.Context, opts Options) (stop func(), err error) {
	port := portOf(opts.UIAddr)
	args := kubectlArgs(opts.K8s, "port-forward", "-n", opts.K8s.Namespace, "svc/"+opts.K8s.Service, port+":80")
	return startKubectlPortForward(ctx, args)
}

// startKubectlPortForward is k8sPortForwardViaKubectl's actual
// subprocess management, factored out so
// k8sGranulePortForwardViaKubectl (k8s_granule.go) can share it rather
// than duplicate everything below the argv this file's own caller and
// that one build differently (K8sOptions vs K8sGranuleOptions).
func startKubectlPortForward(ctx context.Context, args []string) (stop func(), err error) {
	cmd := exec.CommandContext(ctx, "kubectl", args...)
	// Explicit, not left nil: kubectl needs this process's own
	// environment (KUBECONFIG, HOME, PATH) to find the same cluster
	// RealSystem.Run just reached, and tests/security's own
	// exec-env-implicit ratchet is right that "inherits everything"
	// should be a line here, not a default nobody chose.
	cmd.Env = os.Environ()
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		return nil, err
	}

	ready := make(chan error, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			if strings.Contains(scanner.Text(), "Forwarding from") {
				ready <- nil
				go io.Copy(io.Discard, stdout) //nolint:errcheck // draining only, to keep the subprocess from blocking on a full stdout pipe
				return
			}
		}
		ready <- fmt.Errorf("kubectl port-forward exited before reporting it was ready")
	}()

	select {
	case err := <-ready:
		if err != nil {
			cmd.Wait()
			return nil, err
		}
	case <-time.After(k8sPortForwardReadyTimeout):
		cmd.Process.Kill()
		cmd.Wait()
		return nil, fmt.Errorf("kubectl port-forward did not report readiness within %s", k8sPortForwardReadyTimeout)
	case <-ctx.Done():
		cmd.Process.Kill()
		cmd.Wait()
		return nil, ctx.Err()
	}

	return func() {
		cmd.Process.Kill()
		cmd.Wait()
	}, nil
}
