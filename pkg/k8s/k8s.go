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

// Package k8s drives the `kubectl` binary: creating, inspecting and
// deleting the pods a Kubernetes-backed grain deployment runs its
// sandboxes in, and repointing its own Deployment at a new image when it
// upgrades itself.
//
// It is the Kubernetes counterpart of pkg/granule, and it is deliberately
// the same *kind* of dependency: a shallow one, over a CLI, rather than
// a linked client library. Two reasons, both practical rather than
// aesthetic. The first is that client-go is an enormous dependency whose
// version has to be kept in step with the cluster's, and this package
// needs about eight verbs of it. The second is that `kubectl` is already
// the thing an operator debugging a stuck sandbox will reach for, so
// every call this package makes is one they can paste into a shell and
// watch fail the same way -- which is exactly the property that made the
// `granule` argv log worth having on the docker-backed deployments.
//
// Authentication is kubectl's own. In-cluster -- which is how grain runs
// under this backend -- that is the pod's mounted ServiceAccount token,
// picked up with no configuration at all; out of cluster it is whatever
// KUBECONFIG names. Nothing here handles credentials, and nothing here
// should: the RBAC in deploy/kubernetes/ is what decides what this
// package is allowed to do, and a deployment that has not been granted a
// verb fails on it loudly rather than silently doing less.
package k8s

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/kubectlgate"
)

// DefaultKubectl is the binary this package runs when a Client names no
// other. It is looked up on PATH, so the grain image's own kubectl is
// what a real deployment gets (Dockerfile).
const DefaultKubectl = "kubectl"

// PriorityClassInteractive, PriorityClassAgent and PriorityClassCI are
// the PriorityClasses deploy/kubernetes/ defines for sandbox pods, highest
// first (orchestrator.K8sPriorityClasses).
const (
	PriorityClassInteractive = "grain-interactive"
	PriorityClassAgent       = "grain-agent"
	PriorityClassCI          = "grain-ci"
)

// DefaultNamespace is where a deployment that named no namespace of its
// own puts its sandbox pods -- the same namespace deploy/kubernetes/
// installs grain itself into.
const DefaultNamespace = "grain"

// ManagedByLabel and ManagedByValue mark every pod this package creates,
// and are how ReapOrphans finds the ones a previous process of this
// deployment left behind. It is Kubernetes' own recommended label
// (app.kubernetes.io/managed-by), which means `kubectl get pods -l
// app.kubernetes.io/managed-by=grain` is the operator-side version of
// the same question.
const (
	ManagedByLabel = "app.kubernetes.io/managed-by"
	ManagedByValue = "grain"
)

// SandboxLabel and SandboxValue mark a pod as one run's *sandbox*, as
// opposed to any other pod grain might come to manage under the label
// above. Reaping matches on both, so a future grain-managed pod that is
// not a sandbox is not deleted by a sweep that only means to clear
// sandboxes.
const (
	SandboxLabel = "app.kubernetes.io/component"
	SandboxValue = "sandbox"
)

// InstanceLabel marks a pod with the grain deployment that created it
// (Client.Instance): the deployment's effective id, the same
// {deployment} its task branches are named by
// (model.EffectiveDeploymentID), which model.ValidateDeploymentID
// already keeps to a valid label value. It is what lets two grain
// deployments share a namespace without either reaping the other's
// pods.
const InstanceLabel = "app.kubernetes.io/instance"

// SandboxContainer is the name of the one container in a sandbox pod.
// `kubectl exec` needs it explicitly whenever a pod has more than one
// container, and naming it always -- rather than only when it matters --
// keeps the exec argv the same shape whatever a deployment's pod
// template grows into.
const SandboxContainer = "sandbox"

// Client is a configured kubectl. The zero value is usable and means
// "kubectl on PATH, against the DefaultNamespace, with whatever
// credentials kubectl finds for itself".
type Client struct {
	// Kubectl is the binary to run; DefaultKubectl when empty.
	Kubectl string
	// Kubeconfig is an optional path to a kubeconfig file passed as
	// "--kubeconfig <path>". When empty, kubectl uses KUBECONFIG or its
	// in-cluster ServiceAccount token.
	Kubeconfig string
	// Namespace is the namespace every call below operates in;
	// DefaultNamespace when empty.
	Namespace string
	// Timeout bounds each individual kubectl invocation (DefaultTimeout
	// when zero). It is deliberately per-call rather than per-operation:
	// a caller waiting for a pod to become ready polls this package in a
	// loop and owns that deadline itself, and what this bounds is one
	// API round trip that has stopped answering.
	Timeout time.Duration
	// Gate paces this client's kubectl processes against the API
	// server; kubectlgate.Default when nil, which is shared with every
	// other kubectl this process starts. A field so a test can hand it a
	// gate of its own.
	Gate *kubectlgate.Gate
	// Instance, when set, is the InstanceLabel value Manifest gives
	// every pod and ListSandboxes selects on, so a namespace shared by
	// several grain deployments has each one see only its own pods.
	// Empty sets no such label and lists every grain sandbox pod in the
	// namespace, as before the label existed.
	Instance string
}

// DefaultTimeout bounds one kubectl call. Generous for an API round trip
// and short enough that an unreachable API server surfaces as an error
// on the run that needed it rather than as a dispatch that never
// returns.
const DefaultTimeout = 60 * time.Second

func (c Client) kubectl() string {
	if c.Kubectl != "" {
		return c.Kubectl
	}
	return DefaultKubectl
}

// NamespaceOrDefault is c.Namespace, or DefaultNamespace when it names
// none. Exported because callers building an exec transport of their own
// (mcp.KubectlExecRunner) have to resolve the same default the same way.
func (c Client) NamespaceOrDefault() string {
	if c.Namespace != "" {
		return c.Namespace
	}
	return DefaultNamespace
}

func (c Client) gate() *kubectlgate.Gate {
	if c.Gate != nil {
		return c.Gate
	}
	return kubectlgate.Default
}

func (c Client) timeout() time.Duration {
	if c.Timeout > 0 {
		return c.Timeout
	}
	return DefaultTimeout
}

// PodShape is how big a sandbox pod is asked to be. Every field is
// required in the sense that Manifest writes all three: a dimension
// nobody chose still reaches the cluster as a number, the same property
// orchestrator.GranuleConfig.createArgs gives a VM, so a sandbox's size
// is always something this repo decided rather than a cluster default
// that may change under it.
type PodShape struct {
	// CPUs becomes the CPU limit (and, when CPUOvercommit <= 1, the CPU
	// request), in whole cores.
	CPUs int
	// MemoryMB becomes the memory limit (and, when MemoryOvercommit <= 1,
	// the memory request).
	MemoryMB int
	// DiskGB becomes the ephemeral-storage limit (and, when
	// DiskOvercommit <= 1, the ephemeral-storage request) -- the closest
	// Kubernetes analogue of a VM's root disk, and what actually gets a
	// build-heavy run evicted when it is set too low.
	DiskGB int
	// CPUOvercommit, when > 1, scales the CPU request down to
	// max(1, round(CPUs*1000/CPUOvercommit)) millicores ("<N>m") while
	// keeping the CPU limit at CPUs cores.
	CPUOvercommit float64
	// MemoryOvercommit, when > 1, scales the memory request down to
	// max(1, round(MemoryMB/MemoryOvercommit)) MiB ("<N>Mi") while
	// keeping the memory limit at MemoryMB MiB.
	MemoryOvercommit float64
	// DiskOvercommit, when > 1, scales the ephemeral-storage request down
	// to max(1, round(DiskGB*1024/DiskOvercommit)) MiB ("<N>Mi") while
	// keeping the ephemeral-storage limit at DiskGB GiB. kube-scheduler
	// bin-packs a node against the request, and a sandbox's disk is a
	// thin overlay that a run mostly never writes, so a request equal to
	// the limit kept a third 40 GiB sandbox off a node with ~100 GiB of
	// allocatable ephemeral storage however little the first two wrote.
	DiskOvercommit float64
}

// PodOptions is everything Create needs beyond a name and a shape.
type PodOptions struct {
	// Image is the sandbox image to run. Required: this package has no
	// default worth guessing, since the image is the whole of what a
	// dispatched agent finds when it lands.
	Image string
	// ServiceAccount is the pod's Kubernetes identity. Empty leaves the
	// namespace's "default" account, which is the right answer for a
	// sandbox: nothing in a sandbox should be talking to the API server
	// at all, and deploy/kubernetes/ binds no permissions to that
	// account.
	ServiceAccount string
	// Command replaces the image's own entrypoint. Empty means
	// DefaultCommand -- a sandbox pod's job is to sit still and be
	// exec'd into, which is not what most images do on their own.
	Command []string
	// Env is set on the container, sorted by key so the manifest a given
	// set produces does not depend on map iteration order.
	Env map[string]string
	// Labels are merged over the two this package sets itself
	// (ManagedByLabel, SandboxLabel), which cannot be overridden --
	// reaping depends on them.
	Labels map[string]string
	// NodeSelector, when set, pins sandbox pods to a subset of nodes --
	// e.g. a node pool sized for build workloads, or one with nested
	// virtualization available.
	NodeSelector map[string]string
	// Privileged runs the sandbox container privileged. Off by default,
	// and a deployment that turns it on has given a dispatched agent
	// root on the node: it is here because a sandbox that has to run
	// docker or kind needs it, and refusing to express it would only
	// mean a deployment forking this file.
	Privileged bool
	// PriorityClassName is the pod's spec.priorityClassName: which of
	// the PriorityClasses deploy/kubernetes/ ships (grain-interactive,
	// grain-agent, grain-ci) the scheduler weighs it by, and so which
	// pods it may preempt on a full cluster and which may preempt it.
	// Empty sets none, leaving the pod at the cluster's global default
	// priority -- what every sandbox pod had before the classes existed.
	// A name the cluster has no PriorityClass for is refused by the API
	// server at Create, which says so (see ErrNoPriorityClass);
	// EnsurePriorityClasses is how a caller makes sure it will not be.
	PriorityClassName string
	// Security is the securityContext the pod and its container run
	// under. Both halves are empty by default, which is what a sandbox
	// pod wants -- see the note in Manifest on why a sandbox names no
	// uid of its own -- and what every caller but the self-upgrade
	// leaves them as.
	//
	// The self-upgrade sets it from the Deployment it is about to
	// repoint (Client.DeploymentSecurity), so that the throwaway pod
	// proving a candidate image starts is constrained exactly as the
	// pod that will replace the running daemon: an image that only
	// works as root has to fail the check rather than fail the rollout
	// that has already stopped the old pod.
	Security PodSecurity

	// HostDevices are host device paths bind-mounted into the container
	// at the same path, one hostPath volume and one volumeMount per
	// entry -- what a privileged pod needs for /dev/kvm, which has no
	// other way in: there is no resource request a cluster meters a
	// device node by, only a path on the node's own filesystem. Added
	// for -platform=k8s-granule (docs/k8s-granule-design.md), which is
	// the only caller that ever sets Privileged and this together; no
	// sandbox pod needs either.
	HostDevices []string

	// NoCommand, if true, sets no "command" on the container at all --
	// not Command, and not DefaultCommand either -- leaving the image's
	// own ENTRYPOINT/CMD to run unmodified.
	//
	// Every sandbox pod leaves this false: DefaultCommand ("sit still
	// and be exec'd into") is exactly what a plain sandbox container
	// wants, since its whole job is waiting for a `kubectl exec`. A
	// granule pod's job is the opposite -- its one process *is* the
	// thing to run, cloud-hypervisor booted by the image's own
	// ENTRYPOINT (`granule-shim`, "run" mode by default with no
	// arguments at all) -- so overriding it the way Command/
	// DefaultCommand do would replace the VM boot with a shell that
	// sits still doing nothing, which is not something a sandbox pod
	// carrying Privileged and HostDevices should ever do by a default
	// meant for a different kind of pod entirely.
	NoCommand bool

	// ReadinessExec, given, is the pod's readinessProbe: run this
	// command in the container and consider it Ready only once the
	// command exits zero. Empty leaves Kubernetes' own default -- Ready
	// the moment the container starts -- which is right for every
	// sandbox pod pkg/k8s creates today, since those have nothing
	// running inside them worth waiting on beyond the container itself.
	// A granule pod's main process is a VM still booting, which is
	// exactly what this is for: ["granule", "ready", "-timeout", "0"],
	// the same probe third_party/granule/deploy/k8s/gke-pod-example.yaml
	// ships, dialling the guest's own agent over vsock rather than
	// trusting the container's own liveness.
	ReadinessExec []string

	// Setup, given, prepares files in an init container before the
	// sandbox container starts, and mounts them into it read-only -- see
	// Setup's own doc comment. Nil for every pod that needs nothing
	// prepared, whose manifest is then exactly what it was before this
	// field existed.
	Setup *Setup
	// SecretMounts are Secrets mounted read-only, with files readable by
	// their owner only, into the sandbox container alone -- what a
	// granule pod's wrapper finds its sandbox-access credentials in
	// (pkg/sandboxaccess). The Secret may be created after the pod: the
	// kubelet holds the container until the volume can be mounted.
	SecretMounts []SecretMount
}

// Setup is an init container that leaves files for the sandbox container
// to find at fixed paths: what orchestrator.K8sSandboxes points a pod's
// apt, pip, docker and CA bundle at a package cache with.
//
// It exists because nothing else can. A sandbox image runs as its own
// unprivileged user (USER debian in Dockerfile.k8s-sandbox) with no sudo,
// and `kubectl exec` -- the only way into a running pod -- always runs as
// that user, so a file under /etc cannot be written into a running
// sandbox pod at all. A pod's mounts are decided before it starts, so
// that is where those files have to come from instead.
//
// The init container runs the pod's own image, as that image's own user
// and with the sandbox container's own securityContext: nothing here asks
// for root, which a pod-security "baseline" or "restricted" namespace
// would be right to look twice at. It does not need it. Script writes
// into an emptyDir mounted at SetupDir, and each of Files is a read-only
// subPath mount of one of those files at its path in the sandbox
// container -- read-only, so the agent who owns the file still cannot
// change it, and the runtime creates the mount point and its parent
// directories itself, as root, where the image has none.
type Setup struct {
	// Script runs under `sh -c`, with SetupDir as $1 and Args after it.
	// It must leave every SubPath Files names as a regular file in $1:
	// one it does not is created by the kubelet as a *directory*, which
	// the sandbox would then find in place of a configuration file.
	Script string
	Args   []string
	Files  []SetupFile
}

// SetupFile is one file a Setup script leaves in SetupDir, and where the
// sandbox container finds it.
type SetupFile struct {
	SubPath   string
	MountPath string
}

// SetupContainer and SetupVolume name a Setup's init container and the
// emptyDir it shares with the sandbox container, mounted in the former
// at SetupDir.
const (
	SetupContainer = "setup"
	SetupVolume    = "grain-setup"
	SetupDir       = "/grain-setup"
)

// SecurityContext is a securityContext as the API server takes it --
// {"runAsUser": 1000, "fsGroup": 1000, ...} -- carried verbatim rather
// than as a struct of the fields this repo happens to use today.
//
// Verbatim on purpose. Its one producer reads a Deployment's own context
// back out of the cluster so a check pod can be constrained the same
// way, and a typed subset would silently drop whatever that Deployment
// grows next (a supplementalGroups, a seLinuxOptions) -- which would put
// the check back to being weaker than the rollout it is standing in for,
// quietly, which is the exact failure it exists to prevent.
type SecurityContext map[string]any

// PodSecurity is the pair of securityContexts a pod runs under: the
// pod-level one, which is where runAsUser, runAsGroup and fsGroup live,
// and the container-level one, which is where capabilities and
// allowPrivilegeEscalation do. Kubernetes keeps them in two places and
// neither implies the other, so copying one without the other would copy
// half of what constrains a pod.
type PodSecurity struct {
	Pod       SecurityContext
	Container SecurityContext
}

// Empty reports whether this pair constrains anything at all.
func (s PodSecurity) Empty() bool { return len(s.Pod) == 0 && len(s.Container) == 0 }

// DefaultCommand is what a sandbox pod runs when PodOptions names no
// command: sit still until something execs into it, and exit promptly
// when Kubernetes asks it to.
//
// `sleep infinity` under PID 1 in a container ignores SIGTERM unless the
// shell forwards it, which turns every Release into a fifteen-second
// wait for the kill. Trapping it costs one more word and makes a
// released sandbox disappear immediately.
var DefaultCommand = []string{"sh", "-c", "trap 'exit 0' TERM; sleep infinity & wait"}

// Manifest renders the pod this package would create for name -- kept
// separate from Create so a test can assert on exactly what is sent to
// the cluster without a cluster anywhere, the same separation
// mcp.DockerExecRunner.execArgs makes for its own invocation.
//
// It returns JSON rather than YAML because every Kubernetes manifest
// *is* JSON as far as the API server is concerned (YAML is a surface
// syntax over it), and encoding/json gives a stable, quoted, unambiguous
// rendering with no dependency and no indentation rules to get wrong.
func (c Client) Manifest(name string, shape PodShape, opts PodOptions) ([]byte, error) {
	if name == "" {
		return nil, fmt.Errorf("k8s: a sandbox pod needs a name")
	}
	if opts.Image == "" {
		return nil, fmt.Errorf("k8s: sandbox pod %q has no image to run", name)
	}
	labels := map[string]any{}
	for k, v := range opts.Labels {
		labels[k] = v
	}
	labels[ManagedByLabel] = ManagedByValue
	labels[SandboxLabel] = SandboxValue
	if c.Instance != "" {
		labels[InstanceLabel] = c.Instance
	}

	command := opts.Command
	if !opts.NoCommand && len(command) == 0 {
		command = DefaultCommand
	}

	limits := map[string]any{
		"cpu":               strconv.Itoa(shape.CPUs),
		"memory":            strconv.Itoa(shape.MemoryMB) + "Mi",
		"ephemeral-storage": strconv.Itoa(shape.DiskGB) + "Gi",
	}
	requests := map[string]any{
		"cpu":               cpuRequestQuantity(shape.CPUs, shape.CPUOvercommit),
		"memory":            memoryRequestQuantity(shape.MemoryMB, shape.MemoryOvercommit),
		"ephemeral-storage": diskRequestQuantity(shape.DiskGB, shape.DiskOvercommit),
	}
	container := map[string]any{
		"name":            SandboxContainer,
		"image":           opts.Image,
		"imagePullPolicy": "IfNotPresent",
		"resources":       map[string]any{"requests": requests, "limits": limits},
	}
	if len(command) > 0 {
		container["command"] = command
	}
	if len(opts.Env) > 0 {
		container["env"] = envList(opts.Env)
	}
	// No runAsUser beside it unless a caller asked for one, deliberately.
	// Which uid a sandbox runs as is its image's own declaration -- USER
	// agent, uid 1000, in the shipped Dockerfile.k8s-sandbox, and
	// whatever a deployment that builds its own says -- and pinning a
	// number here would override images this package has never seen, for
	// a home directory (-k8s-workspace) it cannot know exists under that
	// uid. It has nothing to do with the uid the *daemon* runs as either
	// (deploy/kubernetes/grain.yaml's securityContext, uid 1000 there
	// too, and by coincidence rather than by requirement): the two pods
	// share no filesystem and meet only over `kubectl exec`, which runs
	// as the target container's user whoever asked for it. So
	// PodOptions.Security is empty for every sandbox, and a sandbox pod's
	// manifest is what it always was.
	if len(opts.Security.Container) > 0 || opts.Privileged {
		security := map[string]any{}
		for k, v := range opts.Security.Container {
			security[k] = v
		}
		if opts.Privileged {
			security["privileged"] = true
		}
		container["securityContext"] = security
	}
	if len(opts.ReadinessExec) > 0 {
		container["readinessProbe"] = map[string]any{
			"exec":           map[string]any{"command": toAnySlice(opts.ReadinessExec)},
			"periodSeconds":  readinessPeriodSeconds,
			"timeoutSeconds": readinessTimeoutSeconds,
		}
	}
	var volumes, volumeMounts []any
	for _, dev := range opts.HostDevices {
		volName := hostDeviceVolumeName(dev)
		// CharDevice, not the untyped default: on a node without the
		// device, an untyped hostPath has the kubelet create an empty
		// directory at that path on the node itself (seen for /dev/kvm on
		// the 10-05 kind run), the VM monitor then fails with "Is a
		// directory", and the stray directory is left in the node's /dev.
		// Typed, the pod fails to start with a FailedMount naming the
		// path instead.
		volumes = append(volumes, map[string]any{
			"name":     volName,
			"hostPath": map[string]any{"path": dev, "type": "CharDevice"},
		})
		volumeMounts = append(volumeMounts, map[string]any{"name": volName, "mountPath": dev})
	}
	for i, m := range opts.SecretMounts {
		volName := secretVolumeName(i)
		volumes = append(volumes, map[string]any{
			"name":   volName,
			"secret": map[string]any{"secretName": m.Secret, "defaultMode": 0o400},
		})
		volumeMounts = append(volumeMounts, map[string]any{"name": volName, "mountPath": m.MountPath, "readOnly": true})
	}
	var initContainers []any
	if opts.Setup != nil {
		volumes = append(volumes, map[string]any{"name": SetupVolume, "emptyDir": map[string]any{}})
		for _, file := range opts.Setup.Files {
			volumeMounts = append(volumeMounts, map[string]any{
				"name": SetupVolume, "mountPath": file.MountPath, "subPath": file.SubPath, "readOnly": true,
			})
		}
		setup := map[string]any{
			"name":            SetupContainer,
			"image":           opts.Image,
			"imagePullPolicy": "IfNotPresent",
			"command":         toAnySlice(append([]string{"sh", "-c", opts.Setup.Script, "sh", SetupDir}, opts.Setup.Args...)),
			// The sandbox's own size: an init container's requests are
			// counted as max(init, containers), so this adds nothing to
			// what the pod is scheduled with.
			"resources":    map[string]any{"requests": requests, "limits": limits},
			"volumeMounts": []any{map[string]any{"name": SetupVolume, "mountPath": SetupDir}},
		}
		// Constrained exactly as the sandbox container is, and never
		// privileged: writing a few files into an emptyDir needs nothing.
		if len(opts.Security.Container) > 0 {
			security := map[string]any{}
			for k, v := range opts.Security.Container {
				security[k] = v
			}
			setup["securityContext"] = security
		}
		initContainers = append(initContainers, setup)
	}
	if len(volumeMounts) > 0 {
		container["volumeMounts"] = volumeMounts
	}

	spec := map[string]any{
		"containers": []any{container},
		// Never, not Always: a sandbox whose process died is a sandbox
		// whose run is over, and restarting it would hand the next tool
		// call a filesystem that has silently lost everything the run
		// put in it.
		"restartPolicy": "Never",
		// Nothing in a sandbox talks to the API server, so mounting a
		// token there is a credential handed to a dispatched agent for
		// no reason (see PodOptions.ServiceAccount).
		"automountServiceAccountToken": false,
		// A released sandbox should go away now. Its work is already
		// finished or abandoned by the time Release is called, so there
		// is nothing for a grace period to let it finish.
		"terminationGracePeriodSeconds": 5,
	}
	if len(opts.Security.Pod) > 0 {
		security := map[string]any{}
		for k, v := range opts.Security.Pod {
			security[k] = v
		}
		spec["securityContext"] = security
	}
	if opts.ServiceAccount != "" {
		spec["serviceAccountName"] = opts.ServiceAccount
	}
	if opts.PriorityClassName != "" {
		spec["priorityClassName"] = opts.PriorityClassName
	}
	if len(opts.NodeSelector) > 0 {
		selector := map[string]any{}
		for k, v := range opts.NodeSelector {
			selector[k] = v
		}
		spec["nodeSelector"] = selector
	}
	if len(volumes) > 0 {
		spec["volumes"] = volumes
	}
	if len(initContainers) > 0 {
		spec["initContainers"] = initContainers
	}

	pod := map[string]any{
		"apiVersion": "v1",
		"kind":       "Pod",
		"metadata": map[string]any{
			"name":      name,
			"namespace": c.NamespaceOrDefault(),
			"labels":    labels,
		},
		"spec": spec,
	}
	return json.Marshal(pod)
}

func cpuRequestQuantity(cpus int, overcommit float64) string {
	if overcommit > 1 && !math.IsInf(overcommit, 0) && cpus > 0 {
		milli := max(1, int(math.Round(float64(cpus)*1000/overcommit)))
		return strconv.Itoa(milli) + "m"
	}
	return strconv.Itoa(cpus)
}

func memoryRequestQuantity(memoryMB int, overcommit float64) string {
	if overcommit > 1 && !math.IsInf(overcommit, 0) && memoryMB > 0 {
		mb := max(1, int(math.Round(float64(memoryMB)/overcommit)))
		return strconv.Itoa(mb) + "Mi"
	}
	return strconv.Itoa(memoryMB) + "Mi"
}

func diskRequestQuantity(diskGB int, overcommit float64) string {
	if overcommit > 1 && !math.IsInf(overcommit, 0) && diskGB > 0 {
		mb := max(1, int(math.Round(float64(diskGB)*1024/overcommit)))
		return strconv.Itoa(mb) + "Mi"
	}
	return strconv.Itoa(diskGB) + "Gi"
}

// readinessPeriodSeconds and readinessTimeoutSeconds match the probe
// third_party/granule/deploy/k8s/gke-pod-example.yaml ships: frequent
// enough that `kubectl wait --for=condition=Ready` notices a guest boot
// (gke.md's own measured 2.3s) within a couple of polls, and a timeout
// well under that period since "-timeout 0" below already makes each
// attempt a single, fast dial rather than one that retries on its own.
const (
	readinessPeriodSeconds  = 5
	readinessTimeoutSeconds = 5
)

// toAnySlice renders a []string as the []any JSON encoding needs it to
// be when it sits inside a map[string]any built by hand -- json.Marshal
// handles []string on its own, but the surrounding map is typed any,
// which stops Go from inferring that this field should too.
func toAnySlice(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}

// hostDeviceVolumeName derives a DNS-1123-safe volume name from a device
// path -- "/dev/kvm" becomes "dev-kvm" -- so PodOptions.HostDevices needs
// nothing from a caller beyond the path itself.
func hostDeviceVolumeName(path string) string {
	return strings.ReplaceAll(strings.Trim(path, "/"), "/", "-")
}

// envList renders opts.Env as the API's own list-of-{name,value},
// ordered by name so one Env map always produces one manifest.
func envList(env map[string]string) []any {
	names := make([]string, 0, len(env))
	for name := range env {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]any, 0, len(names))
	for _, name := range names {
		out = append(out, map[string]any{"name": name, "value": env[name]})
	}
	return out
}

// Create applies the pod Manifest renders. It returns as soon as the API
// server has accepted the pod, which is well before the pod is running:
// waiting for that is Ready's job, and a caller wanting both calls
// WaitReady.
func (c Client) Create(ctx context.Context, name string, shape PodShape, opts PodOptions) error {
	manifest, err := c.Manifest(name, shape, opts)
	if err != nil {
		return err
	}
	if _, err := c.runInput(ctx, string(manifest), "apply", "--validate=false", "-f", "-"); err != nil {
		if opts.PriorityClassName != "" && missingPriorityClass(err) {
			return fmt.Errorf("k8s: creating sandbox pod %q: %w %q -- apply deploy/kubernetes/grain.yaml "+
				"(or grain-granule.yaml), which defines grain's PriorityClasses, or clear the priority class "+
				"in Settings -> Sandbox to create sandbox pods without one: %w",
				name, ErrNoPriorityClass, opts.PriorityClassName, err)
		}
		return fmt.Errorf("k8s: creating sandbox pod %q: %w", name, err)
	}
	return nil
}

// ErrNoPriorityClass is what Create wraps when the API server refuses a
// pod for naming a PriorityClass the cluster does not have -- a
// deployment whose operator never applied the manifests that define
// them. It is a configuration fault rather than a transient one, and
// says so, rather than reaching the run as kubectl's own one-liner.
var ErrNoPriorityClass = errors.New("the cluster has no PriorityClass named")

// missingPriorityClass reports whether err is the API server's priority
// admission plugin refusing a pod whose priorityClassName names no
// PriorityClass: `pods "x" is forbidden: no PriorityClass with name y
// was found`.
func missingPriorityClass(err error) bool {
	var cmdErr *commandError
	if !errors.As(err, &cmdErr) {
		return false
	}
	return strings.Contains(cmdErr.stderr, "no PriorityClass with name")
}

// Delete removes a sandbox pod. A pod that is already gone is not an
// error (--ignore-not-found): Release is called on paths that may have
// already lost the pod -- an evicted node, a sweep that raced a
// finishing run -- and "it is not there" is the outcome the caller
// wanted either way.
//
// wait says whether to block until the pod is actually gone. A caller
// releasing a finished run passes false -- the pod is already
// unreachable to that run the moment the API server accepts the
// deletion, and blocking a finishing run on the kubelet's own teardown
// buys nothing. A caller about to create a pod under the same name
// passes true, because a name is not free again until the old object
// has left etcd.
func (c Client) Delete(ctx context.Context, name string, wait bool) error {
	if _, err := c.run(ctx, "delete", "pod", name, "--ignore-not-found",
		"--wait="+strconv.FormatBool(wait)); err != nil {
		return fmt.Errorf("k8s: deleting sandbox pod %q: %w", name, err)
	}
	return nil
}

// Phase is a pod's status.phase, verbatim: "Pending", "Running",
// "Succeeded", "Failed" or "Unknown". A pod that does not exist reports
// PhaseMissing, which is this package's own value rather than the API's
// -- the API answers that question with an error, and the difference
// between "gone" and "the cluster is not answering" is one every caller
// here has to make.
type Phase string

const (
	PhasePending   Phase = "Pending"
	PhaseRunning   Phase = "Running"
	PhaseSucceeded Phase = "Succeeded"
	PhaseFailed    Phase = "Failed"
	PhaseUnknown   Phase = "Unknown"
	PhaseMissing   Phase = "Missing"
)

// UnschedulablePrefix starts Status.Reason when the scheduler has marked
// a pod PodScheduled=False because no node can take it.
const UnschedulablePrefix = "Unschedulable:"

// Unschedulable reports whether the scheduler has said no node can take
// this pod -- see WaitReady's unschedulable bound for why that is not, on
// its own, a reason to give up.
func (s Status) Unschedulable() bool {
	return s.Phase == PhasePending && strings.HasPrefix(s.Reason, UnschedulablePrefix)
}

// Terminal reports whether a phase is one a pod will never leave --
// which is what tells a wait loop to give up now rather than sit out its
// whole deadline against a pod that has already lost.
func (p Phase) Terminal() bool {
	return p == PhaseSucceeded || p == PhaseFailed || p == PhaseMissing
}

// Status is what a caller needs to know about a pod that is not ready
// yet: which phase it is in, and -- when the kubelet has said something
// about why -- what it said. The reason is what turns "this sandbox
// never came up" into "ImagePullBackOff on the image you named", which
// is the difference between a report an operator can act on and one they
// have to go find in `kubectl describe`.
type Status struct {
	Phase Phase
	// Ready is the pod's Ready condition, which is a stronger statement
	// than PhaseRunning: a pod is Running from the moment its first
	// container starts, and Ready only once every container in it is up.
	Ready bool
	// Reason is the pod's own status.reason, or the first waiting
	// container's reason and message when the pod itself gives none --
	// "ImagePullBackOff", "CrashLoopBackOff", an eviction's own
	// explanation -- or, for a pod no node has taken yet, the
	// scheduler's own PodScheduled=False reason and message
	// ("Unschedulable: 0/1 nodes are available: 1 Insufficient memory.").
	// Empty when nothing has gone wrong yet.
	Reason string
	// ImageID is the first container's status.containerStatuses[].imageID:
	// the image the kubelet actually ran, as the runtime names it --
	// "docker-pullable://repo@sha256:..." under dockershim and cri-dockerd,
	// "repo@sha256:..." under containerd. Unlike the image a pod spec asks
	// for, it does not move when a tag is pushed again, which is what lets
	// a caller pin itself to exactly what a pod ran (pkg/upgrade's
	// Kubernetes path does). Empty until a container has been started.
	ImageID string
}

// podStatusFormat is the go-template Status is read with. One kubectl
// call rather than three jsonpath ones, and a template rather than
// `-o json` plus a decode of the pod's whole schema into this package:
// what is wanted is four strings, and the pod API type is not something
// this package should have an opinion about (see the package doc).
//
// The reason goes last because it is free text a kubelet message could
// put a "|" in; an imageID never holds one, nor a space, which is what
// separates the imageIDs of a pod with more than one container.
//
// A pod with a Setup reports its init container first: while that is
// still pulling, or once it has failed, the sandbox container is only
// "PodInitializing", which says nothing and is left out -- and WaitReady
// recognises an image-pull failure by the reason it starts with.
const podStatusFormat = `{{.status.phase}}` +
	`|{{range .status.conditions}}{{if eq .type "Ready"}}{{.status}}{{end}}{{end}}` +
	`|{{range .status.containerStatuses}}{{.imageID}} {{end}}` +
	`|{{if .status.reason}}{{.status.reason}}{{else}}` +
	`{{range .status.initContainerStatuses}}{{if .state.waiting}}{{.state.waiting.reason}}: {{.state.waiting.message}}{{end}}` +
	`{{if .state.terminated}}{{if .state.terminated.exitCode}}init container {{.name}} exited {{.state.terminated.exitCode}}: {{.state.terminated.reason}} {{end}}{{end}}{{end}}` +
	`{{range .status.containerStatuses}}{{if .state.waiting}}{{if ne .state.waiting.reason "PodInitializing"}}` +
	`{{.state.waiting.reason}}: {{.state.waiting.message}}{{end}}{{end}}{{end}}` +
	`{{range .status.conditions}}{{if and (eq .type "PodScheduled") (eq .status "False")}}{{.reason}}: {{.message}}{{end}}{{end}}` +
	`{{end}}`

// Status reads name's current phase, readiness and failure reason. A pod
// that does not exist is reported as PhaseMissing with a nil error,
// because every caller here treats "gone" as an answer rather than as a
// failure to get one.
func (c Client) Status(ctx context.Context, name string) (Status, error) {
	out, err := c.run(ctx, "get", "pod", name, "-o", "go-template="+podStatusFormat)
	if err != nil {
		if isNotFound(err) {
			return Status{Phase: PhaseMissing}, nil
		}
		return Status{}, fmt.Errorf("k8s: reading sandbox pod %q: %w", name, err)
	}
	fields := strings.SplitN(strings.TrimSpace(out), "|", 4)
	for len(fields) < 4 {
		fields = append(fields, "")
	}
	st := Status{Phase: Phase(fields[0]), Ready: fields[1] == "True", Reason: strings.TrimSpace(fields[3])}
	if ids := strings.Fields(fields[2]); len(ids) > 0 {
		st.ImageID = ids[0]
	}
	if st.Phase == "" {
		st.Phase = PhaseUnknown
	}
	return st, nil
}

// podTerminationsFormat is the go-template OOMKilled is read with: each
// container's current and last termination reason, as name=reason.
const podTerminationsFormat = `{{range .status.containerStatuses}}` +
	`{{if .state.terminated}}{{.name}}={{.state.terminated.reason}} {{end}}` +
	`{{if .lastState.terminated}}{{.name}}={{.lastState.terminated.reason}} {{end}}{{end}}`

// OOMKilled reports whether any container of pod name has terminated
// with reason OOMKilled -- now, or in its last termination before a
// restart -- and which one. A pod that no longer exists reports false
// with a nil error, as Status reports it missing.
func (c Client) OOMKilled(ctx context.Context, name string) (container string, killed bool, err error) {
	out, err := c.run(ctx, "get", "pod", name, "-o", "go-template="+podTerminationsFormat)
	if err != nil {
		if isNotFound(err) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("k8s: reading sandbox pod %q's container terminations: %w", name, err)
	}
	for _, field := range strings.Fields(out) {
		if n, reason, ok := strings.Cut(field, "="); ok && reason == "OOMKilled" {
			return n, true, nil
		}
	}
	return "", false, nil
}

// podDisruptionFormat is the go-template Disrupted is read with: the
// reason and message of the pod's DisruptionTarget condition, when it is
// True -- the condition the control plane sets on a pod it is about to
// take away for reasons of its own rather than the pod's: the scheduler
// preempting it for a higher-priority pod (PreemptionByScheduler), the
// eviction API, a node's taint manager, or the kubelet under node
// pressure.
const podDisruptionFormat = `{{range .status.conditions}}{{if and (eq .type "DisruptionTarget") (eq .status "True")}}` +
	`{{.reason}}: {{.message}}{{end}}{{end}}`

// Disrupted reports whether the cluster took pod name away from under
// whatever was running in it: either the pod carries a True
// DisruptionTarget condition (how is its reason and message,
// "PreemptionByScheduler: ..."), or the pod no longer exists at all --
// which, asked of a pod its owner has not yet deleted, is the same
// verdict seen too late, since a preempted pod is gone within its few
// seconds of grace. A pod that is there and undisrupted reports false,
// and so does one this cannot read (the error says why).
func (c Client) Disrupted(ctx context.Context, name string) (how string, disrupted bool, err error) {
	out, err := c.run(ctx, "get", "pod", name, "-o", "go-template="+podDisruptionFormat)
	if err != nil {
		if isNotFound(err) {
			return fmt.Sprintf("sandbox pod %s no longer exists", name), true, nil
		}
		return "", false, fmt.Errorf("k8s: reading sandbox pod %q's conditions: %w", name, err)
	}
	if reason := strings.TrimSpace(out); reason != "" {
		return fmt.Sprintf("sandbox pod %s was disrupted (%s)", name, reason), true, nil
	}
	return "", false, nil
}

// podRestartsFormat is the go-template ContainerRestarts is read with:
// the named container's own restartCount out of the pod's status, and
// nothing else. A pod whose kubelet has not published a status for that
// container yet -- a Pending one, or one whose containerStatuses are
// missing entirely -- renders as the empty string rather than failing,
// which is what lets ContainerRestarts tell "not published yet" from
// "the API server refused the read".
const podRestartsFormat = `{{range .status.containerStatuses}}{{if eq .name %q}}{{.restartCount}}{{end}}{{end}}`

// ContainerRestarts is how many times the kubelet has restarted a
// container of pod name since that *pod* was created -- the kubelet's
// own .status.containerStatuses[].restartCount.
//
// It is what a Kubernetes-backed deployment has in place of a journal to
// count its own starts in (cmd/grain's daemon_restarts.go): grain runs
// as one replica of a Deployment, so a grain that is crash-looping is a
// CrashLoopBackOff of a single pod, and this number is the only record
// of it that outlives the container it happened to.
//
// The count is of restarts and not of starts, and it has no window: it
// counts from the pod's creation, and it goes back to zero whenever the
// pod is replaced -- which every rollout does. A caller reporting it to
// a person has to say so, which is why nothing here rounds it into the
// shape of the journal reading beside it.
func (c Client) ContainerRestarts(ctx context.Context, name, container string) (int, error) {
	if name == "" || container == "" {
		return 0, fmt.Errorf("k8s: reading a container's restart count needs a pod and a container (got %q/%q)", name, container)
	}
	out, err := c.run(ctx, "get", "pod", name, "-o", "go-template="+fmt.Sprintf(podRestartsFormat, container))
	if err != nil {
		return 0, fmt.Errorf("k8s: reading pod %q's restart count: %w", name, err)
	}
	count := strings.TrimSpace(out)
	if count == "" {
		return 0, fmt.Errorf("k8s: pod %q has no status for a container called %q", name, container)
	}
	n, err := strconv.Atoi(count)
	if err != nil {
		return 0, fmt.Errorf("k8s: pod %q reported restartCount %q for container %q, which is not a number", name, count, container)
	}
	return n, nil
}

// WaitReady polls until name's pod is Ready, until it reaches a phase it
// will never leave, or until ctx or deadline runs out -- whichever comes
// first. poll is how often it re-asks; a non-positive one means
// DefaultPollInterval.
//
// It polls rather than using `kubectl wait --for=condition=Ready`
// because a pod that will never be ready is the case worth handling
// well: `kubectl wait` sits out its whole timeout against an
// ImagePullBackOff and then says only that it timed out, where this
// gives up at once and says what the kubelet said.
//
// unschedulable bounds, separately and usually more tightly than
// deadline, how long the scheduler may go on saying no node can take the
// pod (PodScheduled=False, reason Unschedulable) before the wait gives
// up with the scheduler's own message. It is a bound rather than an
// immediate failure because the condition is not always final: a node
// still releasing a deleted pod's requests, or a cluster autoscaler
// adding a node, both clear it on their own. A non-positive one leaves
// deadline as the only bound.
func (c Client) WaitReady(ctx context.Context, name string, deadline time.Time, unschedulable, poll time.Duration) error {
	if poll <= 0 {
		poll = DefaultPollInterval
	}
	var last Status
	var unschedulableSince time.Time
	for {
		st, err := c.Status(ctx, name)
		if err == nil {
			last = st
			if st.Ready {
				return nil
			}
			if !st.Unschedulable() {
				unschedulableSince = time.Time{}
			} else if unschedulableSince.IsZero() {
				unschedulableSince = time.Now()
			} else if unschedulable > 0 && time.Since(unschedulableSince) >= unschedulable {
				return &UnschedulableError{Pod: name, For: unschedulable.Round(time.Second), Reason: st.Reason}
			}
			if st.Phase.Terminal() {
				return fmt.Errorf("k8s: sandbox pod %q will never be ready: it is %s%s", name, st.Phase, reasonDetail(st.Reason))
			}
			// An image-pull failure leaves a pod Pending forever. It is a
			// kubelet verdict, not a transient scheduling state, so preserve
			// that useful reason instead of sitting out the readiness timeout.
			if strings.HasPrefix(st.Reason, "ErrImagePull:") || strings.HasPrefix(st.Reason, "ImagePullBackOff:") {
				hint := ""
				if h := ImagePullMirrorHint(st.Reason, "", ""); h != "" {
					hint = " -- " + h
				}
				return fmt.Errorf("k8s: sandbox pod %q will never be ready: it is %s%s%s", name, st.Phase, reasonDetail(st.Reason), hint)
			}
		}
		if time.Now().After(deadline) {
			if err != nil {
				return fmt.Errorf("k8s: gave up waiting for sandbox pod %q: %w", name, err)
			}
			return fmt.Errorf("k8s: sandbox pod %q was still %s when the wait ran out%s",
				name, last.Phase, reasonDetail(last.Reason))
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(poll):
		}
	}
}

// UnschedulableError is WaitReady giving up on a pod the scheduler went
// on marking Unschedulable for the whole of its bound. A type of its own
// because the caller has a decision to make on it that it cannot make on
// any other failure: a pod that did not fit *right now* is a reason to
// wait and try again, not a fault of the run that asked for it.
type UnschedulableError struct {
	Pod    string
	For    time.Duration
	Reason string
}

func (e *UnschedulableError) Error() string {
	return fmt.Sprintf("k8s: sandbox pod %q could not be scheduled for %s: %s -- when that says \"Insufficient\", "+
		"no node has room for the cpu, memory or ephemeral storage the pod requests: %s", e.Pod, e.For, e.Reason, ShrinkOrAddNode)
}

// ShrinkOrAddNode is what every "this pod does not fit" message ends on:
// what an operator can do about it.
const ShrinkOrAddNode = "shrink the sandbox (sandboxCpus/sandboxMemoryMb/sandboxDiskGb in Settings, or the task's own) " +
	"or add a node that fits it"

// DefaultPollInterval is how often WaitReady re-asks the API server
// while it waits.
const DefaultPollInterval = 2 * time.Second

func reasonDetail(reason string) string {
	if reason == "" {
		return ""
	}
	return " (" + reason + ")"
}

// ImageRegistryProject parses a Google Artifact Registry or Container Registry
// image reference (`<host>/<project>/<repo>[:tag][@digest]`) into its registry
// host, GCP project, and repository path suffix. Returns empty strings when
// imageRef is not hosted on pkg.dev or gcr.io.
func ImageRegistryProject(imageRef string) (host, project, repoSuffix string) {
	ref, _, _ := strings.Cut(strings.TrimSpace(imageRef), "@")
	if idx := strings.LastIndex(ref, ":"); idx != -1 && idx > strings.LastIndex(ref, "/") {
		ref = ref[:idx]
	}
	parts := strings.SplitN(ref, "/", 3)
	if len(parts) < 3 {
		return "", "", ""
	}
	h := strings.ToLower(parts[0])
	if h != "gcr.io" && !strings.HasSuffix(h, ".gcr.io") && !strings.HasSuffix(h, ".pkg.dev") {
		return "", "", ""
	}
	if parts[1] == "" || parts[2] == "" {
		return "", "", ""
	}
	return h, parts[1], parts[2]
}

// SuggestedMirrorRepository returns the recommended Artifact Registry mirror
// repository in clusterProject for imageRef. When clusterProject is unknown or
// matches imageRef's own project, `<cluster-project>` is used as the placeholder.
func SuggestedMirrorRepository(imageRef, clusterProject string) string {
	targetProject := strings.TrimSpace(clusterProject)
	if host, imgProject, suffix := ImageRegistryProject(imageRef); host != "" && suffix != "" {
		if targetProject == "" || targetProject == imgProject {
			targetProject = "<cluster-project>"
		}
		return host + "/" + targetProject + "/" + suffix
	}
	if targetProject == "" {
		targetProject = "<cluster-project>"
	}
	return "us-docker.pkg.dev/" + targetProject + "/grain/guest"
}

// ExtractImageFromPullError extracts the image reference quoted inside a
// kubelet image-pull error message, or returns "" if none is found.
func ExtractImageFromPullError(reason string) string {
	for _, marker := range []string{`pull and unpack image "`, `Failed to pull image "`, `for "`} {
		if _, rest, ok := strings.Cut(reason, marker); ok {
			if img, _, ok := strings.Cut(rest, `"`); ok && strings.Contains(img, "/") {
				return img
			}
		}
	}
	for _, field := range strings.Fields(reason) {
		trimmed := strings.Trim(field, `"'(),:;`)
		if h, _, _ := ImageRegistryProject(trimmed); h != "" {
			return trimmed
		}
	}
	return ""
}

// IsCrossOrgImagePullError reports whether reason describes a kubelet image
// pull failure against a Google Artifact Registry or GCR repository caused by
// an authorization / 403 Forbidden failure (such as a GKE cluster in an
// external GCP organization pulling a Google-track guest image).
func IsCrossOrgImagePullError(reason string) bool {
	lower := strings.ToLower(reason)
	hasGoogleRegistry := strings.Contains(lower, ".pkg.dev") || strings.Contains(lower, "gcr.io")
	if !hasGoogleRegistry {
		return false
	}
	hasAuthError := strings.Contains(lower, "403 forbidden") ||
		strings.Contains(lower, "failed to authorize") ||
		strings.Contains(lower, "downloadartifacts") ||
		strings.Contains(lower, "permission") ||
		strings.Contains(lower, "unauthorized")
	return hasAuthError
}

// ImagePullMirrorHint returns an actionable suggestion to configure
// -sandbox-image-mirror when reason is a cross-org / 403 Forbidden image pull
// failure against Artifact Registry or GCR, or "" otherwise.
func ImagePullMirrorHint(reason, imageRef, clusterProject string) string {
	if !IsCrossOrgImagePullError(reason) {
		return ""
	}
	if imageRef == "" {
		imageRef = ExtractImageFromPullError(reason)
	}
	mirror := SuggestedMirrorRepository(imageRef, clusterProject)
	imgDesc := "the sandbox image"
	if imageRef != "" {
		imgDesc = imageRef
	}
	return fmt.Sprintf(
		"cluster nodes cannot pull %s across GCP organization boundaries (403 Forbidden); "+
			"configure a sandbox image mirror repository in the cluster's GCP project with: "+
			"grain settings -sandbox-image-mirror %s",
		imgDesc, mirror)
}

// ClusterProject best-effort detects the GCP project ID that the target
// Kubernetes cluster runs in, by inspecting GKE node providerIDs
// (`gce://<project>/<zone>/<node>`) or the current kubeconfig context name
// (`gke_<project>_<location>_<cluster>`). Returns "" when the project cannot
// be determined.
func (c Client) ClusterProject(ctx context.Context) string {
	if out, err := c.run(ctx, "get", "nodes", "-o", `jsonpath={range .items[*]}{.spec.providerID}{"\n"}{end}`); err == nil {
		for _, line := range strings.Split(out, "\n") {
			if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "gce://"); ok {
				if proj, _, ok := strings.Cut(rest, "/"); ok && proj != "" {
					return proj
				}
			}
		}
	}
	if out, err := c.run(ctx, "config", "current-context"); err == nil {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(out), "gke_"); ok {
			if proj, _, ok := strings.Cut(rest, "_"); ok && proj != "" {
				return proj
			}
		}
	}
	return ""
}

const podWaitingImagesFormat = `{{range .items}}{{range .status.containerStatuses}}{{if .state.waiting}}{{.image}}|{{.state.waiting.reason}}: {{.state.waiting.message}}{{"\n"}}{{end}}{{end}}{{end}}`

// CheckImagePullConnectivity checks whether the target cluster has reported or
// is likely to encounter cross-project / cross-organization image pull failures
// for imageRef, and returns an error with a setup suggestion for
// -sandbox-image-mirror when connectivity is missing.
func (c Client) CheckImagePullConnectivity(ctx context.Context, imageRef string) error {
	imageRef = strings.TrimSpace(imageRef)
	if imageRef == "" {
		return nil
	}
	clusterProject := c.ClusterProject(ctx)
	if out, err := c.run(ctx, "get", "pods", "-l", ManagedByLabel+"="+ManagedByValue, "-o", "go-template="+podWaitingImagesFormat); err == nil {
		for _, line := range strings.Split(out, "\n") {
			img, reason, ok := strings.Cut(strings.TrimSpace(line), "|")
			if !ok || reason == "" {
				continue
			}
			if img == imageRef || strings.HasPrefix(reason, "ErrImagePull:") || strings.HasPrefix(reason, "ImagePullBackOff:") {
				if hint := ImagePullMirrorHint(reason, imageRef, clusterProject); hint != "" {
					return errors.New(hint)
				}
			}
		}
	}
	if _, imgProject, _ := ImageRegistryProject(imageRef); imgProject == "source-project" && clusterProject != "" && clusterProject != imgProject {
		mirror := SuggestedMirrorRepository(imageRef, clusterProject)
		return fmt.Errorf(
			"cluster runs in GCP project %q while sandbox image %s is in %q; if cluster nodes cannot pull across GCP organization boundaries (403 Forbidden), configure a mirror repository with: grain settings -sandbox-image-mirror %s",
			clusterProject, imageRef, imgProject, mirror)
	}
	return nil
}

// WaitTerminal polls until name's pod has finished one way or the other
// -- Succeeded, Failed, or gone -- or until ctx or deadline runs out.
// It is WaitReady's counterpart for a pod that is meant to run once and
// exit rather than to sit still and be exec'd into: an upgrade's health
// check, which proves the cluster can pull the new image and that the
// binary inside it runs at all.
//
// A pod that is merely Running is not finished, and a pod that never
// leaves Pending is the case this has to bound: it returns what the
// kubelet said (an image that cannot be pulled, a node with no room)
// rather than only that the wait ran out.
func (c Client) WaitTerminal(ctx context.Context, name string, deadline time.Time, poll time.Duration) (Status, error) {
	if poll <= 0 {
		poll = DefaultPollInterval
	}
	var last Status
	for {
		st, err := c.Status(ctx, name)
		if err == nil {
			last = st
			if st.Phase.Terminal() {
				return st, nil
			}
		}
		if time.Now().After(deadline) {
			if err != nil {
				return Status{}, fmt.Errorf("k8s: gave up waiting for pod %q to finish: %w", name, err)
			}
			return last, fmt.Errorf("k8s: pod %q was still %s when the wait ran out%s",
				name, last.Phase, reasonDetail(last.Reason))
		}
		select {
		case <-ctx.Done():
			return Status{}, ctx.Err()
		case <-time.After(poll):
		}
	}
}

// Logs returns what a pod's container has printed -- the whole of it.
// Best-effort by contract: a caller asks for this to explain a failure
// it has already detected, so an error here should be reported alongside
// that failure rather than replacing it.
func (c Client) Logs(ctx context.Context, name, container string) (string, error) {
	return c.TailLogs(ctx, name, container, 0)
}

// TailLogs is Logs bounded to the last lines lines, which is what a
// caller reading a *live* pod's log wants: grain's own, for the UI's
// Logs tab under a Kubernetes deployment (pkg/systemlog.Pod), where the
// whole log is however much this daemon has printed since it started and
// the pane asks for a few hundred lines of it.
//
// Zero or negative asks kubectl for no bound at all, which is what Logs
// above is.
func (c Client) TailLogs(ctx context.Context, name, container string, lines int) (string, error) {
	args := []string{"logs", name}
	if container != "" {
		args = append(args, "-c", container)
	}
	if lines > 0 {
		args = append(args, "--tail", strconv.Itoa(lines))
	}
	out, err := c.run(ctx, args...)
	if err != nil {
		return out, fmt.Errorf("k8s: reading pod %q's logs: %w", name, err)
	}
	return out, nil
}

// ListSandboxes returns the names of every sandbox pod in this
// namespace that this package created -- both of its own labels, so a
// namespace shared with anything else is not swept up by a caller
// reaping orphans.
//
// With an Instance it is narrowed to this deployment's pods: those
// labelled with it, and those carrying no InstanceLabel at all. The
// unlabelled ones are pods a daemon created before the label existed,
// and are still this deployment's to readopt and reap after an upgrade;
// a pod another deployment labelled is never listed. A label selector
// cannot say "this value or absent", so that is two calls.
func (c Client) ListSandboxes(ctx context.Context) ([]string, error) {
	selector := ManagedByLabel + "=" + ManagedByValue + "," + SandboxLabel + "=" + SandboxValue
	if c.Instance == "" {
		return c.listPodNames(ctx, selector)
	}
	names, err := c.listPodNames(ctx, selector+","+InstanceLabel+"="+c.Instance)
	if err != nil {
		return nil, err
	}
	legacy, err := c.listPodNames(ctx, selector+",!"+InstanceLabel)
	if err != nil {
		return nil, err
	}
	return append(names, legacy...), nil
}

func (c Client) listPodNames(ctx context.Context, selector string) ([]string, error) {
	out, err := c.run(ctx, "get", "pods", "-l", selector, "-o", "go-template={{range .items}}{{.metadata.name}}\n{{end}}")
	if err != nil {
		return nil, fmt.Errorf("k8s: listing grain sandbox pods: %w", err)
	}
	var names []string
	for _, line := range strings.Split(out, "\n") {
		if name := strings.TrimSpace(line); name != "" {
			names = append(names, name)
		}
	}
	return names, nil
}

// ApplyPod applies a pod manifest a caller rendered itself -- the run
// hosts of pkg/runhost, which are not sandboxes and so are not what
// Manifest renders -- through the same kubectl, namespace and RBAC as
// Create. Like Create it returns once the API server has accepted the
// pod.
func (c Client) ApplyPod(ctx context.Context, name string, manifest []byte) error {
	if _, err := c.runInput(ctx, string(manifest), "apply", "--validate=false", "-f", "-"); err != nil {
		return fmt.Errorf("k8s: creating pod %q: %w", name, err)
	}
	return nil
}

// PodSummary is one pod as ListPods reads it: what a caller that keys
// its own state off labels and annotations needs, and nothing of the
// pod's spec.
type PodSummary struct {
	Name        string            `json:"name"`
	Labels      map[string]string `json:"labels"`
	Annotations map[string]string `json:"annotations"`
	Phase       Phase             `json:"phase"`
	// Deleting is true once the pod has a deletionTimestamp: it is
	// terminating and will not run anything new.
	Deleting bool `json:"deleting"`
	// Node is the node the pod was scheduled to, empty while pending.
	Node string `json:"node"`
}

// ListPods returns every pod in this namespace matching selector (a
// label selector, as `kubectl get -l` takes it).
func (c Client) ListPods(ctx context.Context, selector string) ([]PodSummary, error) {
	out, err := c.run(ctx, "get", "pods", "-l", selector, "-o", "json")
	if err != nil {
		return nil, fmt.Errorf("k8s: listing pods %q: %w", selector, err)
	}
	return parsePodList([]byte(out))
}

// parsePodList reads `kubectl get pods -o json` down to PodSummary.
func parsePodList(data []byte) ([]PodSummary, error) {
	var list struct {
		Items []struct {
			Metadata struct {
				Name              string            `json:"name"`
				Labels            map[string]string `json:"labels"`
				Annotations       map[string]string `json:"annotations"`
				DeletionTimestamp *string           `json:"deletionTimestamp"`
			} `json:"metadata"`
			Spec struct {
				NodeName string `json:"nodeName"`
			} `json:"spec"`
			Status struct {
				Phase Phase `json:"phase"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, fmt.Errorf("k8s: reading a pod list: %w", err)
	}
	pods := make([]PodSummary, 0, len(list.Items))
	for _, it := range list.Items {
		pods = append(pods, PodSummary{
			Name:        it.Metadata.Name,
			Labels:      it.Metadata.Labels,
			Annotations: it.Metadata.Annotations,
			Phase:       it.Status.Phase,
			Deleting:    it.Metadata.DeletionTimestamp != nil,
			Node:        it.Spec.NodeName,
		})
	}
	return pods, nil
}

// ClaimSummary is what ClaimInfo reads off a PersistentVolumeClaim: the
// part a caller deciding whether the volume can be shared needs.
type ClaimSummary struct {
	StorageClass string
	// Provisioner is the claim's storage-provisioner annotation, set on
	// a dynamically provisioned claim; empty for a statically bound one.
	Provisioner string
	// AccessModes are the bound volume's (status), or the requested ones
	// (spec) while the claim is unbound.
	AccessModes []string
}

// ClaimInfo reads a PersistentVolumeClaim in this namespace. It needs
// only `get` on persistentvolumeclaims: the storage class and the volume
// behind it are cluster-scoped and deliberately not read.
func (c Client) ClaimInfo(ctx context.Context, name string) (ClaimSummary, error) {
	out, err := c.run(ctx, "get", "pvc", name, "-o", "json")
	if err != nil {
		return ClaimSummary{}, fmt.Errorf("k8s: reading claim %q: %w", name, err)
	}
	return parseClaim([]byte(out))
}

func parseClaim(data []byte) (ClaimSummary, error) {
	var doc struct {
		Metadata struct {
			Annotations map[string]string `json:"annotations"`
		} `json:"metadata"`
		Spec struct {
			StorageClassName *string  `json:"storageClassName"`
			AccessModes      []string `json:"accessModes"`
		} `json:"spec"`
		Status struct {
			AccessModes []string `json:"accessModes"`
		} `json:"status"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return ClaimSummary{}, fmt.Errorf("k8s: reading a claim: %w", err)
	}
	s := ClaimSummary{AccessModes: doc.Status.AccessModes}
	if len(s.AccessModes) == 0 {
		s.AccessModes = doc.Spec.AccessModes
	}
	if doc.Spec.StorageClassName != nil {
		s.StorageClass = *doc.Spec.StorageClassName
	}
	s.Provisioner = doc.Metadata.Annotations["volume.kubernetes.io/storage-provisioner"]
	if s.Provisioner == "" {
		s.Provisioner = doc.Metadata.Annotations["volume.beta.kubernetes.io/storage-provisioner"]
	}
	return s, nil
}

// SetDeploymentImage repoints a Deployment's container at ref, which is
// how a Kubernetes-backed deployment upgrades itself: there is no binary
// on a host to overwrite and no container to restart, only a spec to
// change and a controller that rolls it out.
//
// It returns as soon as the API server has accepted the change. Watching
// the rollout that follows is RolloutStatus's job -- and on the
// deployment grain is *itself* running in, watching it means watching
// this process be replaced, so the two are deliberately separable.
func (c Client) SetDeploymentImage(ctx context.Context, deployment, container, ref string) error {
	if deployment == "" || container == "" || ref == "" {
		return fmt.Errorf("k8s: repointing a deployment needs a deployment, a container and an image (got %q/%q/%q)",
			deployment, container, ref)
	}
	if _, err := c.run(ctx, "set", "image", "deployment/"+deployment, container+"="+ref); err != nil {
		return fmt.Errorf("k8s: repointing deployment %q at %q: %w", deployment, ref, err)
	}
	return nil
}

// DeploymentImage reads back which image a Deployment's container is
// currently running -- what an upgrade rolls back *to*, and what the UI
// reports as the version in force.
func (c Client) DeploymentImage(ctx context.Context, deployment, container string) (string, error) {
	tmpl := fmt.Sprintf(`{{range .spec.template.spec.containers}}{{if eq .name %q}}{{.image}}{{end}}{{end}}`, container)
	out, err := c.run(ctx, "get", "deployment", deployment, "-o", "go-template="+tmpl)
	if err != nil {
		return "", fmt.Errorf("k8s: reading deployment %q's image: %w", deployment, err)
	}
	image := strings.TrimSpace(out)
	if image == "" {
		return "", fmt.Errorf("k8s: deployment %q has no container called %q", deployment, container)
	}
	return image, nil
}

// DeploymentSecurity reads the securityContexts a Deployment's pod
// template runs its named container under: the pod-level one and that
// container's own, both verbatim.
//
// It is what lets the self-upgrade's health check ask the question the
// rollout will ask. The Deployment is the only place that answer exists
// -- an operator who edited grain.yaml's securityContext changed it
// there and nowhere else -- so reading it back beats configuring the
// same uid twice and letting the two drift.
//
// A Deployment with no securityContext anywhere is not an error: it
// returns an empty PodSecurity, which is a pod that runs as whatever its
// image says, which is exactly what that Deployment's own pods do.
func (c Client) DeploymentSecurity(ctx context.Context, deployment, container string) (PodSecurity, error) {
	if deployment == "" || container == "" {
		return PodSecurity{}, fmt.Errorf("k8s: reading a deployment's security context needs a deployment and a container (got %q/%q)",
			deployment, container)
	}
	out, err := c.run(ctx, "get", "deployment", deployment, "-o", "json")
	if err != nil {
		return PodSecurity{}, fmt.Errorf("k8s: reading deployment %q's security context: %w", deployment, err)
	}
	// Only the fields wanted are named; encoding/json drops the rest of
	// a Deployment, which is most of it.
	var doc struct {
		Spec struct {
			Template struct {
				Spec struct {
					SecurityContext SecurityContext `json:"securityContext"`
					Containers      []struct {
						Name            string          `json:"name"`
						SecurityContext SecurityContext `json:"securityContext"`
					} `json:"containers"`
				} `json:"spec"`
			} `json:"template"`
		} `json:"spec"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		return PodSecurity{}, fmt.Errorf("k8s: reading deployment %q's security context: %w", deployment, err)
	}
	podSpec := doc.Spec.Template.Spec
	for _, c := range podSpec.Containers {
		if c.Name == container {
			return PodSecurity{Pod: podSpec.SecurityContext, Container: c.SecurityContext}, nil
		}
	}
	return PodSecurity{}, fmt.Errorf("k8s: deployment %q has no container called %q", deployment, container)
}

// RolloutStatus waits for a Deployment's rollout to finish, bounded by
// wait. It is `kubectl rollout status` verbatim, and its output is
// returned so a caller reporting progress has the same lines an operator
// would see.
func (c Client) RolloutStatus(ctx context.Context, deployment string, wait time.Duration) (string, error) {
	if wait <= 0 {
		wait = c.timeout()
	}
	out, err := c.runCtx(ctx, wait+10*time.Second, "", "rollout", "status", "deployment/"+deployment,
		"--timeout="+wait.String())
	if err != nil {
		return out, fmt.Errorf("k8s: waiting for deployment %q to roll out: %w", deployment, err)
	}
	return out, nil
}

// ClusterCapacity sums the allocatable CPU (in whole cores), memory
// (in MiB) and ephemeral storage (in whole GiB) across all schedulable
// Ready nodes in the cluster matching nodeSelector. A node that reports
// no allocatable ephemeral storage adds none. A node is counted when it is not cordoned
// (!spec.unschedulable) and its Ready condition is True.
//
// Other pods' requests are deliberately not subtracted: low-priority
// headroom pods (deploy/kubernetes/headroom.yaml) sit on Ready nodes to
// make a cluster autoscaler scale up ahead of demand and are preempted
// by sandbox pods, so the room they hold is grain's to spend, and
// counting it out would shrink the budget by exactly the headroom meant
// to grow it.
func (c Client) ClusterCapacity(ctx context.Context, nodeSelector map[string]string) (cpus, memoryMB, diskGB int, err error) {
	args := []string{"get", "nodes"}
	if selector := formatLabelSelector(nodeSelector); selector != "" {
		args = append(args, "-l", selector)
	}
	args = append(args, "-o", "json")
	out, err := c.run(ctx, args...)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("k8s: reading cluster node capacity: %w", err)
	}
	return parseClusterCapacity([]byte(out), nodeSelector)
}

func formatLabelSelector(selector map[string]string) string {
	if len(selector) == 0 {
		return ""
	}
	keys := make([]string, 0, len(selector))
	for k := range selector {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+selector[k])
	}
	return strings.Join(parts, ",")
}

type nodeCondition struct {
	Type   string `json:"type"`
	Status string `json:"status"`
}

type nodeCapacityItem struct {
	Metadata struct {
		Name   string            `json:"name"`
		Labels map[string]string `json:"labels"`
	} `json:"metadata"`
	Spec struct {
		Unschedulable bool `json:"unschedulable"`
	} `json:"spec"`
	Status struct {
		Allocatable struct {
			CPU              string `json:"cpu"`
			Memory           string `json:"memory"`
			EphemeralStorage string `json:"ephemeral-storage"`
		} `json:"allocatable"`
		Conditions []nodeCondition `json:"conditions"`
	} `json:"status"`
}

func parseClusterCapacity(data []byte, nodeSelector map[string]string) (cpus, memoryMB, diskGB int, err error) {
	var list struct {
		Items []nodeCapacityItem `json:"items"`
	}
	if err := json.Unmarshal(data, &list); err != nil {
		return 0, 0, 0, fmt.Errorf("k8s: reading node list: %w", err)
	}
	items := list.Items
	if len(items) == 0 {
		var single nodeCapacityItem
		if err := json.Unmarshal(data, &single); err == nil &&
			(single.Status.Allocatable.CPU != "" || single.Status.Allocatable.Memory != "") {
			items = []nodeCapacityItem{single}
		}
	}

	hasLabels := false
	for _, it := range items {
		if len(it.Metadata.Labels) > 0 {
			hasLabels = true
			break
		}
	}

	var totalMilliCPU int64
	var totalMemoryBytes, totalDiskBytes int64
	for _, it := range items {
		if it.Spec.Unschedulable || !nodeReady(it.Status.Conditions) {
			continue
		}
		if len(nodeSelector) > 0 && hasLabels && !matchesNodeSelector(it.Metadata.Labels, nodeSelector) {
			continue
		}
		milliCPU, err := parseMilliCPU(it.Status.Allocatable.CPU)
		if err != nil {
			return 0, 0, 0, fmt.Errorf("k8s: node %q allocatable cpu: %w", it.Metadata.Name, err)
		}
		memBytes, err := parseMemoryBytes(it.Status.Allocatable.Memory)
		if err != nil {
			return 0, 0, 0, fmt.Errorf("k8s: node %q allocatable memory: %w", it.Metadata.Name, err)
		}
		if storage := it.Status.Allocatable.EphemeralStorage; storage != "" {
			diskBytes, err := parseMemoryBytes(storage)
			if err != nil {
				return 0, 0, 0, fmt.Errorf("k8s: node %q allocatable ephemeral-storage: %w", it.Metadata.Name, err)
			}
			totalDiskBytes += diskBytes
		}
		totalMilliCPU += milliCPU
		totalMemoryBytes += memBytes
	}

	return int(totalMilliCPU / 1000), int(totalMemoryBytes / (1 << 20)), int(totalDiskBytes / (1 << 30)), nil
}

func nodeReady(conditions []nodeCondition) bool {
	for _, c := range conditions {
		if c.Type == "Ready" {
			return c.Status == "True"
		}
	}
	return false
}

func matchesNodeSelector(labels, selector map[string]string) bool {
	for k, v := range selector {
		if labels[k] != v {
			return false
		}
	}
	return true
}

func parseMilliCPU(raw string) (int64, error) {
	q := strings.TrimSpace(raw)
	if q == "" {
		return 0, errors.New("empty cpu quantity")
	}
	if num, ok := strings.CutSuffix(q, "m"); ok {
		if v, err := strconv.ParseInt(num, 10, 64); err == nil {
			if v < 0 {
				return 0, fmt.Errorf("negative cpu quantity %q", raw)
			}
			return v, nil
		}
		v, err := strconv.ParseFloat(num, 64)
		if err != nil || v < 0 || math.IsNaN(v) || math.IsInf(v, 0) {
			return 0, fmt.Errorf("invalid cpu quantity %q", raw)
		}
		return int64(math.Round(v)), nil
	}
	if v, err := strconv.ParseInt(q, 10, 64); err == nil {
		if v < 0 {
			return 0, fmt.Errorf("negative cpu quantity %q", raw)
		}
		return v * 1000, nil
	}
	v, err := strconv.ParseFloat(q, 64)
	if err != nil || v < 0 || math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, fmt.Errorf("invalid cpu quantity %q", raw)
	}
	return int64(math.Round(v * 1000)), nil
}

func parseMemoryBytes(raw string) (int64, error) {
	q := strings.TrimSpace(raw)
	if q == "" {
		return 0, errors.New("empty memory quantity")
	}
	suffixes := []struct {
		suffix string
		mult   int64
	}{
		{"Ki", 1 << 10},
		{"Mi", 1 << 20},
		{"Gi", 1 << 30},
		{"Ti", 1 << 40},
		{"Pi", 1 << 50},
		{"Ei", 1 << 60},
		{"k", 1e3},
		{"K", 1e3},
		{"M", 1e6},
		{"G", 1e9},
		{"T", 1e12},
		{"P", 1e15},
		{"E", 1e18},
	}
	for _, s := range suffixes {
		if num, ok := strings.CutSuffix(q, s.suffix); ok {
			if v, err := strconv.ParseInt(num, 10, 64); err == nil {
				if v < 0 {
					return 0, fmt.Errorf("negative memory quantity %q", raw)
				}
				return v * s.mult, nil
			}
			v, err := strconv.ParseFloat(num, 64)
			if err != nil || v < 0 || math.IsNaN(v) || math.IsInf(v, 0) {
				return 0, fmt.Errorf("invalid memory quantity %q", raw)
			}
			return int64(math.Round(v * float64(s.mult))), nil
		}
	}
	if v, err := strconv.ParseInt(q, 10, 64); err == nil {
		if v < 0 {
			return 0, fmt.Errorf("negative memory quantity %q", raw)
		}
		return v, nil
	}
	v, err := strconv.ParseFloat(q, 64)
	if err != nil || v < 0 || math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, fmt.Errorf("invalid memory quantity %q", raw)
	}
	return int64(math.Round(v)), nil
}

// --- running kubectl -----------------------------------------------------

func (c Client) run(ctx context.Context, args ...string) (string, error) {
	return c.runCtx(ctx, c.timeout(), "", args...)
}

func (c Client) runInput(ctx context.Context, stdin string, args ...string) (string, error) {
	return c.runCtx(ctx, c.timeout(), stdin, args...)
}

// runCtx is the one place this package executes anything. It prepends
// "-n <namespace>" to every call, so no caller above has to remember to
// -- a kubectl that falls back to the *kubeconfig's* current namespace
// is how a sandbox ends up somewhere nothing will ever reap it.
//
// Every call waits its turn at the client's gate (kubectlgate), and one
// that failed without ever reaching the API server -- a dial or TLS
// handshake that timed out -- or, for an exec, without the API server
// reaching the kubelet (kubectlgate.IsTransientExecStart), is run again after a short back-off, up
// to kubectlgate.MaxAttempts in all. That retry is safe whatever the
// verb, apply and delete included, because a request that never got a
// connection cannot have been applied; anything the server did answer
// is returned as it is. timeout bounds each attempt rather than all of
// them, so a dial that sat out most of it still leaves the retry a
// whole one; ctx bounds the lot.
func (c Client) runCtx(ctx context.Context, timeout time.Duration, stdin string, args ...string) (string, error) {
	var argv []string
	if c.Kubeconfig != "" {
		argv = append(argv, "--kubeconfig", c.Kubeconfig)
	}
	argv = append(argv, "-n", c.NamespaceOrDefault())
	argv = append(argv, args...)
	for attempt := 1; ; attempt++ {
		callCtx, cancel := context.WithTimeout(ctx, timeout)
		out, err := c.runOnce(callCtx, stdin, argv)
		cancel()
		var cmdErr *commandError
		if err == nil || attempt >= kubectlgate.MaxAttempts ||
			!errors.As(err, &cmdErr) || !kubectlgate.IsTransientExecStart(cmdErr.stderr) {
			return out, err
		}
		if !kubectlgate.Sleep(ctx, kubectlgate.BackoffFor(cmdErr.stderr, attempt)) {
			return out, err
		}
	}
}

// runOnce is one kubectl process: admitted through the gate, run, and
// its failure, if any, wrapped as a commandError.
func (c Client) runOnce(ctx context.Context, stdin string, argv []string) (string, error) {
	release, err := c.gate().Acquire(ctx)
	if err != nil {
		return "", &commandError{argv: append([]string{c.kubectl()}, argv...), err: err}
	}
	defer release()
	cmd := exec.CommandContext(ctx, c.kubectl(), argv...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		return out.String(), &commandError{
			argv:   append([]string{c.kubectl()}, argv...),
			stderr: strings.TrimSpace(errBuf.String()),
			err:    err,
		}
	}
	return out.String(), nil
}

// commandError is what a failed kubectl reports: the command that failed,
// what it printed, and the process error underneath. The argv is in the
// message because it is runnable -- an operator reading a failed
// dispatch can paste it and watch the same failure happen to them.
type commandError struct {
	argv   []string
	stderr string
	err    error
}

func (e *commandError) Error() string {
	msg := fmt.Sprintf("`%s`: %v", strings.Join(e.argv, " "), e.err)
	if e.stderr != "" {
		msg += ": " + e.stderr
	}
	return msg
}

func (e *commandError) Unwrap() error { return e.err }

// isNotFound reports whether err is kubectl saying the object does not
// exist, as opposed to any other failure. kubectl exits 1 for both, so
// this reads what it printed -- the same "tell the two apart by stderr"
// shape mcp.DockerExecRunner's execFailedBeforeGuest has to use, and for
// the same reason.
func isNotFound(err error) bool {
	var cmdErr *commandError
	if !errors.As(err, &cmdErr) {
		return false
	}
	return strings.Contains(cmdErr.stderr, "NotFound") || strings.Contains(cmdErr.stderr, "not found")
}
