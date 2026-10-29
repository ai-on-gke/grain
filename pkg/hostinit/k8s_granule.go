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

// k8s_granule.go is PlatformK8sGranule's entire implementation, the
// same shape k8s.go gives PlatformK8sContainer: a preflight table
// reading this platform's own prerequisites off the real cluster, then
// `kubectl apply -f grain-granule.yaml` exactly once, a wait for the
// rollout, and a hand-off to ProvisionOverAPI. See
// docs/k8s-granule-design.md for why this platform needs its own
// preflight, its own manifest and its own options rather than a flag on
// PlatformK8sContainer's: the two platforms' pods disagree about
// whether privileged is allowed at all, so no single check or manifest
// could mean the right thing for both.
//
// What does carry over, reused rather than duplicated: k8sRequiredPerms
// (minus the one entry this platform's manifest never creates --
// see k8sGranuleRequiredPerms), lastLine, writeK8sScratchManifest,
// deleteK8sScratchPod/Manifest, and the StepManual/StepRefused idiom
// every check below reports through. What does not: the namespace
// pod-security check wants the opposite label, the node-level
// prerequisite is nested virtualization rather than RBAC/storage/CNI,
// and this platform's own K8sGranuleOptions names a granule runtime
// image and a node selector that K8sOptions has no field for.
package hostinit

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/k8s"
)

// K8sGranuleOptions configures -platform=k8s-granule: which manifest to
// apply, what the Namespace/Deployment/Service inside it are named, the
// granule runtime image every real and preflight pod runs, and the node
// selector that pins every one of them to a nested-virt-capable pool.
// See docs/k8s-granule-design.md for why this platform exists and
// docs/grain-init-platforms.md for where it sits next to the other
// three. Ignored by every other Platform.
type K8sGranuleOptions struct {
	// Namespace, Deployment and Service are deploy/kubernetes/
	// grain-granule.yaml's own names -- "grain" unless a caller edited
	// the manifest. Empty means DefaultK8sNamespace/Deployment/Service,
	// the same defaults -platform=k8s-container uses: the two platforms
	// never run in the same namespace at once (docs/k8s-granule-design.md
	// §1's "one deployment per namespace"), so there is no reason for
	// their defaults to differ.
	Namespace  string
	Deployment string
	Service    string

	// Manifest is the path to deploy/kubernetes/grain-granule.yaml,
	// applied once (StepDone the first time, StepSkipped once
	// deployment/<Deployment> already exists -- never re-applied over a
	// deployment an operator may have since edited by hand, the same
	// bar ensureK8sManifest holds itself to). Empty means
	// <RepoRoot>/deploy/kubernetes/grain-granule.yaml.
	Manifest string

	// Image is the packed guest image every pod this platform creates
	// runs -- a self-contained image with its own guest disk and kernel
	// already baked in, the shape Granulefile's own build produces and
	// -platform=linux-kvm's -sandbox-image names (docs/k8s-granule-design.md
	// §4). This is deliberately *not* GranuleRuntimeImage/
	// DefaultGranuleRuntimeImage ("granule:vendored", what
	// scripts/granule/build-granule-image.sh produces and every
	// `granule granulefile build -kontur-image` names): that image is
	// the thinner, unpacked granule+cloud-hypervisor runtime with no
	// guest disk at all, and a pod running it exits immediately rather
	// than booting -- the mistake its own name invites, confirmed
	// against a real cluster. Empty means cmd/grain's own
	// -k8s-granule-image default, which is defaultSandboxImage -- the
	// same per-commit guest CI publishes (guest:sha-<commit>, and
	// guest:inputs-<hash> content-addressed on the guest's own inputs)
	// and -platform=linux-kvm's -sandbox-image already pulls. This
	// package does not default it itself (Options.validate no longer
	// requires it either): stamping that value in is cmd/grain's job,
	// not pkg/hostinit's, the same division K8s.SandboxImage already
	// draws. An operator with their own guest -- a team layer, a
	// different base -- still names it explicitly.
	Image string
	// NodeSelector pins every pod this platform creates -- the
	// preflight's own throwaway nested-virt check included -- to a
	// node pool that actually offers nested virtualization. Empty pins
	// to none, which is only honest on a single-pool cluster
	// (docs/k8s-granule-design.md §2's "one real gap").
	NodeSelector map[string]string

	// Context and Kubeconfig, given, are passed to every kubectl call
	// this platform makes as --context/--kubeconfig, mirroring
	// K8sOptions' own fields.
	Context    string
	Kubeconfig string

	// ManagePortForward, unless it points at false, has this platform
	// start its own `kubectl port-forward svc/<Service>` immediately
	// before ProvisionOverAPI and stop it immediately after -- see
	// k8s.go's own top-of-file comment for why -platform=k8s-container
	// owns that much, which applies identically here. Defaults to true.
	ManagePortForward *bool

	// RolloutTimeout bounds how long `kubectl rollout status` waits for
	// the Deployment to come up after apply. Empty means
	// DefaultK8sRolloutTimeout.
	RolloutTimeout time.Duration
	// PreflightTimeout bounds the nested-virt check's own wait for the
	// throwaway pod to reach Ready. Empty means
	// DefaultK8sGranulePreflightTimeout -- deliberately much shorter
	// than -platform=k8s-container's own DefaultK8sPreflightTimeout:
	// docs/k8s-granule-design.md §2 argues a real boot is low tens of
	// seconds, and a check that waited minutes would be waiting out an
	// emulated boot rather than proving an accelerated one.
	PreflightTimeout time.Duration
}

// DefaultK8sGranulePreflightTimeout bounds the nested-virt check's wait
// for the throwaway pod to reach Ready -- 30s, the figure
// docs/k8s-granule-design.md §2 derives from gke.md's own validated
// numbers (a real boot at 2.3s end to end): room for a cold image pull
// to finish before the boot itself is even timed, while staying far
// short of what an emulated boot would need.
const DefaultK8sGranulePreflightTimeout = 30 * time.Second

// kubectlArgsGranule is kubectlArgs' counterpart for K8sGranuleOptions,
// which does not embed a K8sOptions and so cannot share that function's
// signature.
func kubectlArgsGranule(g K8sGranuleOptions, args ...string) []string {
	var out []string
	if g.Kubeconfig != "" {
		out = append(out, "--kubeconfig="+g.Kubeconfig)
	}
	if g.Context != "" {
		out = append(out, "--context="+g.Context)
	}
	return append(out, args...)
}

func runKubectlGranule(ctx context.Context, sys System, g K8sGranuleOptions, args ...string) (string, error) {
	return sys.Run(ctx, "", nil, "kubectl", kubectlArgsGranule(g, args...)...)
}

// --- preflight ------------------------------------------------------

// k8sGranuleChecks is PlatformK8sGranule's entire preflight list, in
// the order checksFor runs it: the cluster-reachable gate, the
// namespace pod-security gate (the opposite label from
// PlatformK8sContainer's own), RBAC, the nested-virtualization check
// (which folds in the image-pull check -- see its own doc comment),
// and the uid-1000 migration -- the same five-check shape k8sContainerChecks
// holds itself to, so a cluster missing several prerequisites hears
// about all of them in one run.
func k8sGranuleChecks(g K8sGranuleOptions, dataDir string) []platformCheck {
	return []platformCheck{
		checkK8sGranuleClusterReachable(g),
		checkK8sGranuleNamespacePodSecurity(g),
		checkK8sGranulePermissions(g),
		checkK8sGranuleNestedVirt(g, dataDir, g.PreflightTimeout),
		checkK8sGranuleUid1000Migration(g),
	}
}

// checkK8sGranuleClusterReachable is checkK8sClusterReachable's own
// counterpart -- see that function's doc comment for why this is the
// gate every other check in this list depends on.
func checkK8sGranuleClusterReachable(g K8sGranuleOptions) platformCheck {
	return func(ctx context.Context, sys System, r *Result) (bool, bool) {
		return checkKubectlReachesCluster(ctx, sys, k8sGranuleOptionsAsK8sOptions(g), r)
	}
}

// k8sGranuleEnforceLabel is the exact label
// docs/k8s-granule-design.md §1 settles on: reported in every manual
// step below, never applied by this process itself -- see that section for
// why relaxing a namespace's Pod Security admission on an operator's
// behalf is too large a thing for a bring-up command to do quietly.
const k8sGranuleEnforceLabel = "pod-security.kubernetes.io/enforce=privileged"

// checkK8sGranuleNamespacePodSecurity is
// checkK8sNamespacePodSecurity's mirror image: a "grain" namespace
// whose pod-security.kubernetes.io/enforce is not "privileged" cannot
// run the privileged, /dev/kvm-mounted pods this platform's every
// sandbox is, so this refuses anything other than "privileged" --
// exactly the opposite of what PlatformK8sContainer's own check
// refuses, and for the same reason stated the other way around: a
// cluster relaxed for this platform is PlatformK8sContainer's hole to
// never open, and a cluster still at "baseline" (or not yet labelled at
// all) is this platform's own prerequisite, not yet met.
//
// Unlike that check, a namespace that does not exist yet does *not*
// pass: PlatformK8sContainer's own manifest creates its Namespace as
// part of `kubectl apply`, with the right label already on it, so
// "does not exist" there means "apply will create it correctly".
// This platform's manifest never creates a Namespace at all
// (docs/k8s-granule-design.md §3) -- grain init must not perform the
// relaxation on an operator's behalf, which means it must not create
// the namespace either, since a namespace grain init created would
// have no label on it at all (equivalent to "baseline", the one thing
// this platform refuses). So "does not exist" and "exists at the
// wrong label" are reported with the same StepManual, differing only
// in whether the suggested command includes `kubectl create namespace`
// first.
//
// Always a gate: nothing else in this platform's preflight list can
// run against a namespace that would refuse the very pod those checks
// need to schedule.
//
// Reported as StepManual, not StepRefused, in both branches below --
// docs/k8s-granule-design.md §1 is explicit that this is "a StepManual
// with the exact command," not a bare refusal: unlike the handful of
// genuine StepRefused checks elsewhere in this package, there is
// nothing ambiguous for grain init to guess at here, only a command it
// has decided not to run on the operator's behalf. StepRefused's own
// Command field is never printed (cmd/grain's printInitResult only
// walks it for StepManual), so reporting this as StepRefused would
// name the fix in Detail's prose but never show the operator the exact
// command -- the "two commands, not one" case especially, since nobody
// should have to improvise which flags `kubectl create namespace`
// needs.
func checkK8sGranuleNamespacePodSecurity(g K8sGranuleOptions) platformCheck {
	return func(ctx context.Context, sys System, r *Result) (bool, bool) {
		const name = "namespace pod-security level"
		k := k8sGranuleOptionsAsK8sOptions(g)
		labelCmd := kubectlCommand(k, "label", "namespace", g.Namespace, k8sGranuleEnforceLabel, "--overwrite")
		out, err := runKubectlGranule(ctx, sys, g, "get", "namespace", g.Namespace,
			"-o", `jsonpath={.metadata.labels.pod-security\.kubernetes\.io/enforce}`)
		if err != nil {
			r.add(Step{Name: name, Status: StepManual, Detail: fmt.Sprintf(
				"namespace %q does not exist yet -- grain init never creates or labels a namespace for this "+
					"platform itself (docs/k8s-granule-design.md §1: relaxing Pod Security admission is too "+
					"large a thing for a bring-up command to do quietly); create it and apply the label "+
					"yourself first", g.Namespace),
				Command: kubectlCommand(k, "create", "namespace", g.Namespace) + " && " + labelCmd})
			return false, true
		}
		level := lastLine(out)
		if level == "privileged" {
			r.add(Step{Name: name, Status: StepSkipped, Detail: fmt.Sprintf("namespace %q enforces %q", g.Namespace, level)})
			return true, false
		}
		detail := fmt.Sprintf(
			"namespace %q has pod-security.kubernetes.io/enforce=%q, not \"privileged\" -- every sandbox pod "+
				"-platform=%s creates is privileged with a real /dev/kvm, which a %q-enforcing namespace refuses "+
				"outright; grain init will not relax this label on your behalf (docs/k8s-granule-design.md §1), "+
				"so apply it yourself and re-run", g.Namespace, level, PlatformK8sGranule, level)
		if level == "" {
			detail = fmt.Sprintf(
				"namespace %q carries no pod-security.kubernetes.io/enforce label at all, which Kubernetes "+
					"treats as unrestricted admission for every *other* check but is not what -platform=%s "+
					"needs asserted -- every sandbox pod this platform creates is privileged with a real "+
					"/dev/kvm, and grain init will not apply this label on your behalf "+
					"(docs/k8s-granule-design.md §1)", g.Namespace, PlatformK8sGranule)
		}
		r.add(Step{Name: name, Status: StepManual, Detail: detail, Command: labelCmd})
		return false, true
	}
}

// k8sGranuleRequiredPerms is k8sRequiredPerms minus "namespaces":
// deploy/kubernetes/grain-granule.yaml never creates a Namespace object
// (docs/k8s-granule-design.md §3), so the identity running grain init
// never needs permission to create one for this platform to succeed.
var k8sGranuleRequiredPerms = func() []k8sPerm {
	var perms []k8sPerm
	for _, p := range k8sRequiredPerms {
		if p.resource != "namespaces" {
			perms = append(perms, p)
		}
	}
	return perms
}()

// checkK8sGranulePermissions is checkK8sPermissions' own counterpart,
// checking k8sGranuleRequiredPerms against grain-granule.yaml instead
// of k8sRequiredPerms against grain.yaml.
func checkK8sGranulePermissions(g K8sGranuleOptions) platformCheck {
	return func(ctx context.Context, sys System, r *Result) (bool, bool) {
		const name = "permission to create what grain-granule.yaml creates"
		var missing []string
		for _, p := range k8sGranuleRequiredPerms {
			args := []string{"auth", "can-i", "create", p.resource}
			if p.namespaced {
				args = append(args, "-n", g.Namespace)
			}
			out, err := runKubectlGranule(ctx, sys, g, args...)
			if err != nil || lastLine(out) != "yes" {
				missing = append(missing, p.resource)
			}
		}
		if len(missing) == 0 {
			r.add(Step{Name: name, Status: StepSkipped, Detail: "the current kubeconfig identity can create every object grain-granule.yaml defines"})
			return true, false
		}
		r.add(Step{Name: name, Status: StepManual, Detail: fmt.Sprintf(
			"the current kubeconfig identity cannot create: %s -- ask whoever administers this cluster for a "+
				"Role or ClusterRole granting create on these (namespace %q for the namespaced ones), or run "+
				"grain init as an identity that already has it", strings.Join(missing, ", "), g.Namespace)})
		return false, false
	}
}

// k8sGranulePreflightPod is the throwaway pod the nested-virt check
// creates and deletes -- fixed, not generated per run, for the same two
// reasons k8sPreflightImagePod's own doc comment gives.
const k8sGranulePreflightPod = "grain-init-preflight-granule"

// k8sGranuleNestedVirtManifest renders the throwaway pod
// docs/k8s-granule-design.md §2 describes: the *actual* granule runtime
// image, privileged, with /dev/kvm hostPath-mounted at the same path,
// CHV_NET_MODE=none (no network needed to prove a boot), and the same
// "granule ready -timeout 0" readinessProbe gke-pod-example.yaml ships
// -- so `kubectl wait --for=condition=Ready` waits on the guest
// answering, not merely on the container starting. nodeSelectorYAML is
// g.NodeSelector already rendered (or empty), kept as a parameter
// rather than read from g here so this function has no System/kubectl
// of its own to need in a unit test.
func k8sGranuleNestedVirtManifest(namespace, image string, nodeSelector map[string]string) string {
	var nodeSelectorBlock string
	if len(nodeSelector) > 0 {
		var lines []string
		keys := make([]string, 0, len(nodeSelector))
		for k := range nodeSelector {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			lines = append(lines, fmt.Sprintf("    %s: %q", k, nodeSelector[k]))
		}
		nodeSelectorBlock = "  nodeSelector:\n" + strings.Join(lines, "\n") + "\n"
	}
	return fmt.Sprintf(`apiVersion: v1
kind: Pod
metadata:
  name: %[1]s
  namespace: %[2]s
spec:
  restartPolicy: Never
  terminationGracePeriodSeconds: 10
%[4]s  containers:
    - name: sandbox
      image: %[3]s
      imagePullPolicy: IfNotPresent
      env:
        - name: CHV_NET_MODE
          value: "none"
      securityContext:
        privileged: true
      volumeMounts:
        - name: dev-kvm
          mountPath: /dev/kvm
      readinessProbe:
        exec:
          command: ["granule", "ready", "-timeout", "0"]
        periodSeconds: 5
        timeoutSeconds: 5
  volumes:
    - name: dev-kvm
      hostPath:
        path: /dev/kvm
        type: CharDevice
`, k8sGranulePreflightPod, namespace, image, nodeSelectorBlock)
}

// checkK8sGranuleNestedVirt is docs/k8s-granule-design.md §2's entire
// argument, in code: schedule the real sandbox image, privileged, with
// /dev/kvm, and watch whether it boots a VM in the time real
// acceleration takes -- not whether /dev/kvm merely exists, which is
// all PlatformLinuxKVM's own weaker check can prove from outside a pod.
// A pod that fails to reach Ready in timeout is reported as one
// StepManual covering nested virt being off, acceleration silently
// falling back to emulation, or /dev/kvm being unreachable from the
// pod -- deliberately not distinguished, since this check cannot tell
// them apart and the fix an operator reaches for is the same either
// way (§2's own closing paragraph).
//
// This is also where §4's image check is folded in, per that section's
// own argument: a pull failure and a boot failure are both just "this
// pod never became Ready," and distinguishing them from outside the
// pod is exactly as hard as it already is for -platform=k8s-container's
// own pull check, which does not try either.
//
// The StepManual this reports carries no Command: the throwaway pod is
// already gone by the time an operator reads it (cleanup runs in a
// defer right below, before this function returns), so "kubectl
// describe pod grain-init-preflight-granule" would name a pod that no
// longer exists. k8sGranuleDiagnosePreflightPod is called first, while
// the pod is still live, and its output is folded into Detail instead
// -- the operator should not have to go looking themselves.
func checkK8sGranuleNestedVirt(g K8sGranuleOptions, dataDir string, timeout time.Duration) platformCheck {
	if timeout <= 0 {
		timeout = DefaultK8sGranulePreflightTimeout
	}
	return func(ctx context.Context, sys System, r *Result) (bool, bool) {
		const name = "nested virtualization (scheduled boot)"
		manifest := k8sGranuleNestedVirtManifest(g.Namespace, g.Image, g.NodeSelector)
		path, err := writeK8sScratchManifest(dataDir, "granule-nested-virt-check.yaml", manifest)
		if err != nil {
			r.add(Step{Name: name, Status: StepManual, Detail: fmt.Sprintf("could not write a scratch manifest: %v", err)})
			return false, false
		}
		cleanup := func() { deleteK8sScratchManifest(ctx, sys, k8sGranuleOptionsAsK8sOptions(g), path, timeout) }
		// Best-effort, blocking delete-before-create against a previous
		// run's leftovers -- see checkK8sSandboxImagePullable's own
		// comment on the same line for why this is necessary rather than
		// merely cautious.
		cleanup()
		if _, err := runKubectlGranule(ctx, sys, g, "apply", "-f", path); err != nil {
			r.add(Step{Name: name, Status: StepRefused, Detail: fmt.Sprintf(
				"could not even create a throwaway pod for %q in namespace %q: %v", g.Image, g.Namespace, err)})
			return false, false
		}
		defer cleanup()
		if _, err := runKubectlGranule(ctx, sys, g, "wait", "pod/"+k8sGranulePreflightPod, "-n", g.Namespace,
			"--for=condition=Ready", "--timeout="+timeout.String()); err != nil {
			diagnosis, terminated := k8sGranuleDiagnosePreflightPod(ctx, sys, g)
			explanation := "nested virtualization is not enabled, not accelerated, or /dev/kvm is not " +
				"reachable from the pod. Confirm the node pool's nested-virt flag, confirm the kvm kernel " +
				"module is loaded on the node, and confirm no admission policy is silently stripping the " +
				"hostPath or the privileged flag before it reaches the kubelet"
			if k8sGranuleNoKVMDevice(diagnosis) {
				r.add(Step{Name: name, Status: StepRefused, Detail: fmt.Sprintf(
					"the node the pod was scheduled on has no /dev/kvm character device, so no sandbox pod "+
						"can boot a VM there: enable nested virtualization on the node pool (and load the kvm "+
						"module), or pin sandboxes to a pool that has it with -k8s-node-selector.\n\n%s", diagnosis)})
				return false, false
			}
			if hint := k8s.ImagePullMirrorHint(diagnosis, g.Image, ""); hint != "" {
				explanation = hint
			} else if terminated {
				explanation = fmt.Sprintf(
					"the container already exited rather than timing out waiting to boot -- a likely sign "+
						"that %q is not a packed guest (a self-contained image with its own disk and kernel "+
						"baked in) but the plain granule runtime image instead, which has no guest disk and "+
						"cannot boot at all. Check -k8s-granule-image before assuming nested virtualization "+
						"is misconfigured", g.Image)
			}
			r.add(Step{Name: name, Status: StepManual, Detail: fmt.Sprintf(
				"a pod running %q, privileged with /dev/kvm, did not become Ready in namespace %q within %s: "+
					"%v -- %s.\n\n%s",
				g.Image, g.Namespace, timeout, err, explanation, diagnosis)})
			return false, false
		}
		r.add(Step{Name: name, Status: StepSkipped, Detail: fmt.Sprintf(
			"a privileged pod running %q booted a VM over /dev/kvm and answered ready in namespace %q", g.Image, g.Namespace)})
		return true, false
	}
}

// k8sGranuleNoKVMDevice reports whether the preflight pod's diagnosis
// shows the kubelet refusing its CharDevice hostPath: the node has no
// /dev/kvm at all. Without "type: CharDevice" (the 10-05 kind run) the
// kubelet instead created an empty directory at /dev/kvm on the node,
// cloud-hypervisor failed with "Is a directory", and this check blamed
// -k8s-granule-image.
func k8sGranuleNoKVMDevice(diagnosis string) bool {
	return strings.Contains(diagnosis, "hostPath type check failed") && strings.Contains(diagnosis, "/dev/kvm")
}

// k8sGranuleDiagnosePreflightPod gathers what an operator would
// otherwise have to run `kubectl describe`/`logs` themselves to see,
// before checkK8sGranuleNestedVirt's own cleanup deletes the pod: its
// phase, its one container's state (whichever of waiting/running/
// terminated applies -- a wrong, non-bootable image reports
// terminated with a non-zero exitCode here, not merely "did not
// become Ready"), its recent events, and its recent log output. Each
// `kubectl` call is best-effort and independent of the others: a
// cluster too old to answer one, or a container that never started to
// have logs at all, reports "(none)" for that piece rather than
// losing the rest.
//
// terminated reports whether the container's state is "terminated"
// rather than "waiting" or "running" -- the shape a wrong,
// non-bootable -k8s-granule-image produces (the container starts,
// finds no guest disk, and exits immediately) and the shape a genuine
// nested-virt problem never does (that pod is still "running", just
// never answers its readinessProbe). The caller uses this to pick
// which explanation to lead with, so the one real signal this check
// can tell apart from "did not become Ready" is not buried in prose
// that reads the same for both.
func k8sGranuleDiagnosePreflightPod(ctx context.Context, sys System, g K8sGranuleOptions) (diagnosis string, terminated bool) {
	get := func(jsonpath string) string {
		out, err := runKubectlGranule(ctx, sys, g, "get", "pod", k8sGranulePreflightPod, "-n", g.Namespace, "-o", jsonpath)
		if err != nil || strings.TrimSpace(out) == "" {
			return "(none)"
		}
		return strings.TrimSpace(out)
	}
	state := get("jsonpath={.status.containerStatuses[0].state}")
	eventsOut, err := runKubectlGranule(ctx, sys, g, "get", "event", "-n", g.Namespace,
		"--field-selector", "involvedObject.name="+k8sGranulePreflightPod, "--sort-by=.lastTimestamp")
	if err != nil || strings.TrimSpace(eventsOut) == "" {
		eventsOut = "(none)"
	}
	logsOut, err := runKubectlGranule(ctx, sys, g, "logs", k8sGranulePreflightPod, "-n", g.Namespace, "--tail=20")
	if err != nil || strings.TrimSpace(logsOut) == "" {
		logsOut = "(none)"
	}
	diagnosis = fmt.Sprintf(
		"pod phase: %s\ncontainer state: %s\nrecent events:\n%s\nrecent log output:\n%s",
		get("jsonpath={.status.phase}"), state, strings.TrimSpace(eventsOut), strings.TrimSpace(logsOut))
	return diagnosis, strings.Contains(state, `"terminated"`)
}

// k8sGranuleOptionsAsK8sOptions adapts a K8sGranuleOptions to the
// K8sOptions shape deleteK8sScratchManifest/deleteK8sScratchPod take --
// both functions only ever read Context/Kubeconfig off what they are
// given, so this costs nothing beyond the two fields those calls
// actually use.
func k8sGranuleOptionsAsK8sOptions(g K8sGranuleOptions) K8sOptions {
	return K8sOptions{Context: g.Context, Kubeconfig: g.Kubeconfig}
}

// checkK8sGranuleUid1000Migration is checkK8sUid1000Migration's own
// counterpart: the daemon's own pod in this platform's manifest is
// never privileged (docs/k8s-granule-design.md §3's "the daemon's own
// pod is still never privileged"), identical in shape to
// -platform=k8s-container's, so the same uid-1000 migration condition
// applies identically.
func checkK8sGranuleUid1000Migration(g K8sGranuleOptions) platformCheck {
	return func(ctx context.Context, sys System, r *Result) (bool, bool) {
		const name = "uid-1000 migration"
		out, err := runKubectlGranule(ctx, sys, g, "get", "deployment", g.Deployment, "-n", g.Namespace,
			"-o", "jsonpath={.spec.template.spec.securityContext.runAsUser}")
		if err != nil {
			r.add(Step{Name: name, Status: StepSkipped, Detail: fmt.Sprintf("deployment/%s does not exist yet -- nothing to migrate", g.Deployment)})
			return true, false
		}
		if lastLine(out) == "1000" {
			r.add(Step{Name: name, Status: StepSkipped, Detail: fmt.Sprintf("deployment/%s already runs as uid 1000", g.Deployment)})
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
%[3]s delete job grain-data-chown`, g.Namespace, g.Deployment, kubectlCommand(k8sGranuleOptionsAsK8sOptions(g), "-n", g.Namespace))
		r.add(Step{Name: name, Status: StepManual, Detail: fmt.Sprintf(
			"deployment/%s predates the uid-1000 securityContext (runAsUser=%q) -- its volume is likely still "+
				"owned by uid 0; deploy/kubernetes/README.md's one-off chown migration has to run before grain "+
				"init applies a manifest that starts the daemon as uid 1000, or the new pod's init container "+
				"fails making /data/home writable", g.Deployment, lastLine(out)),
			Command: cmd})
		return false, true
	}
}

// --- apply, rollout, reach, provision --------------------------------

// initForK8sGranule is PlatformK8sGranule's entire post-preflight path
// -- initForK8sContainer's own counterpart: apply
// deploy/kubernetes/grain-granule.yaml once, wait for the rollout,
// establish a route to UIAddr, then hand off to ProvisionOverAPI
// unchanged.
func initForK8sGranule(ctx context.Context, sys System, opts Options, r *Result) {
	applied := ensureK8sGranuleManifest(ctx, sys, opts, r)

	rolledOut := false
	if applied {
		rolledOut = ensureK8sGranuleRollout(ctx, sys, opts, r)
	} else {
		r.add(Step{Name: "wait for the rollout", Status: StepSkipped, Detail: "skipped: the manifest was not applied"})
	}

	attempted := false
	var stop func()
	if rolledOut {
		stop, attempted = establishK8sGranuleReachability(ctx, opts, r)
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

// ensureK8sGranuleManifest is ensureK8sManifest's own counterpart,
// applied to grain-granule.yaml instead of grain.yaml.
func ensureK8sGranuleManifest(ctx context.Context, sys System, opts Options, r *Result) bool {
	const name = "apply the grain-granule manifest"
	g := opts.K8sGranule
	cmd := kubectlCommand(k8sGranuleOptionsAsK8sOptions(g), "apply", "-f", g.Manifest)
	if opts.DryRun {
		r.add(Step{Name: name, Status: StepWould, Detail: fmt.Sprintf("would apply %s if deployment/%s does not already exist", g.Manifest, g.Deployment), Command: cmd})
		return false
	}
	if _, err := runKubectlGranule(ctx, sys, g, "get", "deployment", g.Deployment, "-n", g.Namespace); err == nil {
		r.add(Step{Name: name, Status: StepSkipped, Detail: fmt.Sprintf(
			"deployment/%s already exists in namespace %q -- grain init applies the manifest once and leaves "+
				"upgrades to `kubectl -n %s rollout restart deployment/%s`, never re-applying over a "+
				"deployment an operator may have edited by hand",
			g.Deployment, g.Namespace, g.Namespace, g.Deployment)})
		return true
	}
	if _, err := runKubectlGranule(ctx, sys, g, "apply", "-f", g.Manifest); err != nil {
		r.add(Step{Name: name, Status: StepManual, Detail: fmt.Sprintf("kubectl apply -f %s failed: %v", g.Manifest, err), Command: cmd})
		return false
	}
	r.add(Step{Name: name, Status: StepDone, Detail: fmt.Sprintf("applied %s", g.Manifest)})
	return true
}

// ensureK8sGranuleRollout is ensureK8sRollout's own counterpart.
func ensureK8sGranuleRollout(ctx context.Context, sys System, opts Options, r *Result) bool {
	const name = "wait for the rollout"
	g := opts.K8sGranule
	if opts.DryRun {
		r.add(Step{Name: name, Status: StepWould, Detail: "would wait for the rollout to finish"})
		return false
	}
	out, err := runKubectlGranule(ctx, sys, g, "rollout", "status", "deployment/"+g.Deployment, "-n", g.Namespace,
		"--timeout="+g.RolloutTimeout.String())
	if err != nil {
		r.add(Step{Name: name, Status: StepManual, Detail: fmt.Sprintf("kubectl rollout status: %v: %s", err, out),
			Command: kubectlCommand(k8sGranuleOptionsAsK8sOptions(g), "-n", g.Namespace, "rollout", "status", "deployment/"+g.Deployment)})
		return false
	}
	r.add(Step{Name: name, Status: StepDone, Detail: "rollout complete"})
	return true
}

// establishK8sGranuleReachability is establishK8sReachability's own
// counterpart -- see k8s.go's top-of-file comment for why this platform
// owns a port-forward's lifetime the same way -platform=k8s-container
// does.
func establishK8sGranuleReachability(ctx context.Context, opts Options, r *Result) (stop func(), ok bool) {
	const name = "reach the daemon's API"
	g := opts.K8sGranule
	if opts.DryRun {
		r.add(Step{Name: name, Status: StepWould, Detail: fmt.Sprintf(
			"would run kubectl -n %s port-forward svc/%s <port>:80 and point -ui-addr at it", g.Namespace, g.Service)})
		return nil, false
	}
	if opts.K8sGranulePortForward == nil {
		r.add(Step{Name: name, Status: StepSkipped, Detail: fmt.Sprintf(
			"-k8s-granule-manage-port-forward=false -- assuming %s is already reachable (an operator-managed "+
				"kubectl port-forward, Ingress, or internal route)", opts.UIAddr)})
		return nil, true
	}
	stop, err := opts.K8sGranulePortForward(ctx, opts)
	if err != nil {
		r.add(Step{Name: name, Status: StepManual, Detail: fmt.Sprintf("could not establish a route to the daemon: %v", err),
			Command: kubectlCommand(k8sGranuleOptionsAsK8sOptions(g), "-n", g.Namespace, "port-forward", "svc/"+g.Service, portOf(opts.UIAddr)+":80")})
		return nil, false
	}
	r.add(Step{Name: name, Status: StepDone, Detail: fmt.Sprintf("kubectl port-forward svc/%s -> %s", g.Service, opts.UIAddr)})
	return stop, true
}

// k8sGranulePortForwardViaKubectl is the default
// Options.K8sGranulePortForward -- k8sPortForwardViaKubectl's own
// counterpart, reading K8sGranuleOptions instead of K8sOptions.
func k8sGranulePortForwardViaKubectl(ctx context.Context, opts Options) (stop func(), err error) {
	g := opts.K8sGranule
	port := portOf(opts.UIAddr)
	args := kubectlArgsGranule(g, "port-forward", "-n", g.Namespace, "svc/"+g.Service, port+":80")
	return startKubectlPortForward(ctx, args)
}
