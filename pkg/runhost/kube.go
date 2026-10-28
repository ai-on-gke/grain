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

package runhost

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/ai-on-gke/grain/pkg/k8s"
)

// The Kubernetes driver: one pod per run, created, listed and deleted
// through the same pkg/k8s client sandbox pods are
// (docs/non-disruptive-upgrade.md, "Run hosts", and the Kubernetes half
// of Stage 2).
//
// A run pod sees the directories it is given at the paths the daemon
// sees them, exactly as a docker run host does: each Mount is a subPath
// of one of the PersistentVolumeClaims the daemon pod itself has mounted
// (KubeVolume), mounted at the same path. So the pod is given those
// directories and not the volume around them -- the daemon's data volume
// also holds the store and the secrets, which a run host must never see.
//
// Where the pod may run is the volume's access mode:
//
//   - ReadWriteOnce (the default): RWO is per *node*, so every pod on the
//     node the volume is attached to shares it read-write. Run pods are
//     pinned to the daemon pod's node by a required podAffinity on the
//     daemon's own labels (Kubernetes.DaemonSelector). The pinning is
//     IgnoredDuringExecution, so a run pod already running stays where it
//     is while the daemon pod is replaced, and keeps the volume attached
//     to that node; the daemon's Deployment carries a matching preferred
//     podAffinity towards run pods so its replacement lands back beside
//     them (deploy/kubernetes/grain.yaml says what happens when it
//     cannot). ReadWriteOncePod forbids even that sharing and is refused
//     (CheckClaim).
//   - ReadWriteMany (Filestore, NFS): no pinning, so run pods spread
//     across nodes. gcsfuse-backed classes are refused: a rename there is
//     not atomic, and both the spool's exit.json and a refreshing CLI's
//     login depend on one being atomic.

// Pod labels: the selection keys, Kubernetes' own recommended ones so
// `kubectl get pods -l app.kubernetes.io/component=runhost` finds every
// run pod. The component is not k8s.SandboxValue, so the sandbox
// backend's prefix sweep (K8sSandboxes.ReapOrphans) never touches one.
const (
	KubeComponent = "runhost"
	// KubeDaemonComponent is the component label deploy/kubernetes/
	// gives the daemon pod: what a run pod's podAffinity selects, and
	// distinct from every other pod in the namespace.
	KubeDaemonComponent = "daemon"
	// KubeContainer is the run pod's one container.
	KubeContainer = "runhost"
)

// DefaultKubeGracePeriod is a run pod's terminationGracePeriodSeconds.
// A run pod has no drain of its own: the daemon's drain
// (pkg/daemon's shutdownDrain, which the daemon pod's own grace period is
// sized to) detaches from run hosts rather than waiting on them, so a run
// pod is only ever deleted when its run is finished or abandoned -- by
// Destroy -- or evicted with its node. Either way all it has to do in the
// grace period is what "grain runhost exec" does on SIGTERM: kill the
// CLI's process group and write exit.json, which takes seconds.
const DefaultKubeGracePeriod = 30

// KubeVolume is one PersistentVolumeClaim the daemon pod has mounted at
// MountPath. A Mount under MountPath reaches a run pod as a subPath of
// this claim.
type KubeVolume struct {
	Claim     string
	MountPath string
}

// Kubernetes is the Driver for a daemon running in a cluster.
type Kubernetes struct {
	Client k8s.Client
	// Volumes are the claims Mounts are resolved against. A Mount under
	// none of them is refused: the pod could not see it.
	Volumes []KubeVolume
	// DaemonSelector, when set, pins run pods to the node of the pod
	// these labels select -- the ReadWriteOnce arrangement. Empty spreads
	// them (ReadWriteMany).
	DaemonSelector map[string]string
	// ServiceAccount is the run pod's identity: the daemon's own, since
	// the run's mcpserver reaches its sandbox pod through pods/exec
	// exactly as it did as the daemon's child (open question 6).
	ServiceAccount string
	// Security is the daemon pod's own securityContexts, so a run pod
	// runs as the uid that owns the volume.
	Security k8s.PodSecurity
	// Env is set on the container: HOME, chiefly, which a uid with no
	// passwd entry has no other source for.
	Env map[string]string

	// apply, list and del are the seams a test replaces; nil means
	// Client.
	apply func(ctx context.Context, name string, manifest []byte) error
	list  func(ctx context.Context, selector string) ([]k8s.PodSummary, error)
	del   func(ctx context.Context, name string) error
}

var _ Driver = Kubernetes{}

// labelValue makes s a valid label value: at most 63 characters of
// [A-Za-z0-9._-], beginning and ending with an alphanumeric. The
// annotations carry the exact values; labels are for selection.
func labelValue(s string) string {
	b := []byte(s)
	for i, c := range b {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '.' || c == '_' || c == '-') {
			b[i] = '_'
		}
	}
	if len(b) > 63 {
		b = b[:63]
	}
	return strings.TrimFunc(string(b), func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9')
	})
}

// podLabels are the pod's labels: the selection keys, and the run-host
// labels (task, run, generation, protocol and the deadline as Unix
// seconds) in label-safe form. The exact values -- the deadline as RFC
// 3339 and the spool path, neither of which a label value can hold --
// are the annotations (Labels.Map), which are what List reads back.
//
// instance, when set, is the deployment's effective id as
// k8s.InstanceLabel, the label grain's sandbox pods carry too, so
// `kubectl get pods -l app.kubernetes.io/instance=<id>` finds all of a
// deployment's pods. List does not select on it: LabelDeployment
// already narrows List to this installation by its permanent id, and
// the effective id can be edited in Settings, which would leave the next
// daemon unable to adopt run pods labelled with the old one.
func podLabels(l Labels, instance string) map[string]string {
	m := map[string]string{
		k8s.ManagedByLabel: k8s.ManagedByValue,
		k8s.SandboxLabel:   KubeComponent,
		k8s.InstanceLabel:  instance,
		LabelDeployment:    labelValue(l.Deployment),
		LabelTask:          labelValue(l.Task),
		LabelRun:           labelValue(l.Run),
		LabelGeneration:    labelValue(l.Generation),
		LabelProtocol:      strconv.Itoa(l.Protocol),
	}
	if !l.Deadline.IsZero() {
		m[LabelDeadline] = strconv.FormatInt(l.Deadline.Unix(), 10)
	}
	for k, v := range m {
		if v == "" {
			delete(m, k)
		}
	}
	return m
}

// volumeFor returns the volume p is under, the longest MountPath first.
func (k Kubernetes) volumeFor(p string) (int, string, bool) {
	best, rel := -1, ""
	for i, v := range k.Volumes {
		r, err := filepath.Rel(v.MountPath, p)
		if err != nil || r == ".." || strings.HasPrefix(r, "../") {
			continue
		}
		if best < 0 || len(v.MountPath) > len(k.Volumes[best].MountPath) {
			best, rel = i, r
		}
	}
	return best, rel, best >= 0
}

// Manifest renders spec as a pod.
func (k Kubernetes) Manifest(spec Spec) ([]byte, error) {
	if len(spec.Command) == 0 {
		return nil, fmt.Errorf("runhost: spec %s has no command", spec.Name)
	}
	var volumes, mounts []any
	used := map[int]bool{}
	for _, m := range spec.Mounts {
		i, rel, ok := k.volumeFor(m.Path)
		if !ok {
			return nil, fmt.Errorf("runhost: %s is on none of the run-host volumes (%s), so a run pod could not see it",
				m.Path, k.volumeList())
		}
		name := fmt.Sprintf("v%d", i)
		if !used[i] {
			used[i] = true
			volumes = append(volumes, map[string]any{
				"name":                  name,
				"persistentVolumeClaim": map[string]any{"claimName": k.Volumes[i].Claim},
			})
		}
		vm := map[string]any{"name": name, "mountPath": m.Path, "readOnly": m.ReadOnly}
		if rel != "." {
			vm["subPath"] = rel
		}
		mounts = append(mounts, vm)
	}
	container := map[string]any{
		"name":            KubeContainer,
		"image":           spec.Image,
		"imagePullPolicy": "IfNotPresent",
		"command":         spec.Command,
	}
	if len(mounts) > 0 {
		container["volumeMounts"] = mounts
	}
	if len(k.Env) > 0 {
		var env []any
		for _, name := range slices.Sorted(maps.Keys(k.Env)) {
			env = append(env, map[string]any{"name": name, "value": k.Env[name]})
		}
		container["env"] = env
	}
	if len(k.Security.Container) > 0 {
		container["securityContext"] = map[string]any(k.Security.Container)
	}
	podSpec := map[string]any{
		"containers": []any{container},
		// Never: a run host whose CLI exited is finished, and its
		// exit.json is the record of it. Restarting would run the CLI
		// again from the top of the spool.
		"restartPolicy":                 "Never",
		"terminationGracePeriodSeconds": DefaultKubeGracePeriod,
	}
	if k.ServiceAccount != "" {
		podSpec["serviceAccountName"] = k.ServiceAccount
	}
	if len(volumes) > 0 {
		podSpec["volumes"] = volumes
	}
	if len(k.Security.Pod) > 0 {
		podSpec["securityContext"] = map[string]any(k.Security.Pod)
	}
	if len(k.DaemonSelector) > 0 {
		podSpec["affinity"] = map[string]any{
			"podAffinity": map[string]any{
				"requiredDuringSchedulingIgnoredDuringExecution": []any{map[string]any{
					"labelSelector": map[string]any{"matchLabels": k.DaemonSelector},
					"topologyKey":   "kubernetes.io/hostname",
				}},
			},
		}
	}
	pod := map[string]any{
		"apiVersion": "v1",
		"kind":       "Pod",
		"metadata": map[string]any{
			"name":        spec.Name,
			"namespace":   k.Client.NamespaceOrDefault(),
			"labels":      podLabels(spec.Labels, k.Client.Instance),
			"annotations": spec.Labels.Map(),
		},
		"spec": podSpec,
	}
	return json.Marshal(pod)
}

func (k Kubernetes) volumeList() string {
	var s []string
	for _, v := range k.Volumes {
		s = append(s, v.Claim+" at "+v.MountPath)
	}
	if len(s) == 0 {
		return "none configured"
	}
	return strings.Join(s, ", ")
}

// Create implements Driver.
func (k Kubernetes) Create(ctx context.Context, spec Spec) error {
	manifest, err := k.Manifest(spec)
	if err != nil {
		return err
	}
	if k.apply != nil {
		return k.apply(ctx, spec.Name, manifest)
	}
	return k.Client.ApplyPod(ctx, spec.Name, manifest)
}

// List implements Driver. A pod that is terminating, or whose phase is
// Succeeded or Failed, is listed as not running: its spool is final, or
// about to be.
func (k Kubernetes) List(ctx context.Context, deployment string) ([]Info, error) {
	selector := k8s.ManagedByLabel + "=" + k8s.ManagedByValue + "," + k8s.SandboxLabel + "=" + KubeComponent +
		"," + LabelDeployment + "=" + labelValue(deployment)
	list := k.list
	if list == nil {
		list = k.Client.ListPods
	}
	pods, err := list(ctx, selector)
	if err != nil {
		return nil, err
	}
	var infos []Info
	for _, p := range pods {
		l, err := ParseLabels(p.Annotations)
		if err != nil || l.Deployment != deployment {
			continue
		}
		running := !p.Deleting && !p.Phase.Terminal()
		infos = append(infos, Info{Name: p.Name, Labels: l, Running: running})
	}
	return infos, nil
}

// Destroy implements Driver. It does not wait for the pod to leave:
// names are never reused, and the run is over the moment the deletion is
// accepted.
func (k Kubernetes) Destroy(ctx context.Context, name string) error {
	if k.del != nil {
		return k.del(ctx, name)
	}
	return k.Client.Delete(ctx, name, false)
}

// CheckClaim says whether claim may hold run-host directories under the
// access mode the deployment chose (rwx false: ReadWriteOnce, with run
// pods pinned to the daemon's node). It refuses what cannot work and
// returns, as warning, what it could not confirm.
func CheckClaim(c k8s.ClaimSummary, claim string, rwx bool) (warning string, err error) {
	modes := strings.Join(c.AccessModes, ",")
	if isGCSFuse(c) {
		return "", fmt.Errorf("run-host volume %s is gcsfuse-backed (class %q, provisioner %q): a rename there is not atomic "+
			"and in-place rewrites are not coherent between readers, and run hosts depend on both. "+
			"Use a ReadWriteOnce disk, or Filestore or NFS for ReadWriteMany", claim, c.StorageClass, c.Provisioner)
	}
	if slices.Contains(c.AccessModes, "ReadWriteOncePod") {
		return "", fmt.Errorf("run-host volume %s is ReadWriteOncePod, which lets only the daemon pod mount it; "+
			"run pods need ReadWriteOnce (pinned to the daemon's node) or ReadWriteMany", claim)
	}
	if rwx && !slices.Contains(c.AccessModes, "ReadWriteMany") {
		return "", fmt.Errorf("run-host volume %s is %s, not ReadWriteMany, so run pods spread across nodes could not mount it; "+
			"drop -k8s-run-host-rwx to pin them to the daemon's node instead", claim, modes)
	}
	if !rwx && !slices.Contains(c.AccessModes, "ReadWriteOnce") && !slices.Contains(c.AccessModes, "ReadWriteMany") {
		return fmt.Sprintf("run-host volume %s reports access modes %q; run pods are pinned to the daemon's node and "+
			"expect ReadWriteOnce", claim, modes), nil
	}
	if rwx && c.Provisioner == "" && c.StorageClass == "" {
		return fmt.Sprintf("run-host volume %s is statically bound and grain cannot see what backs it from inside the "+
			"namespace; it must not be gcsfuse", claim), nil
	}
	return "", nil
}

// isGCSFuse recognises a gcsfuse-backed claim from what a namespaced
// read can see: the provisioner annotation, or the class name.
func isGCSFuse(c k8s.ClaimSummary) bool {
	p := strings.ToLower(c.Provisioner + " " + c.StorageClass)
	return strings.Contains(p, "gcsfuse") || strings.Contains(p, "gcs-fuse")
}
