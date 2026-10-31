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

package daemon

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/ai-on-gke/grain/pkg/installtrack"
	"github.com/ai-on-gke/grain/pkg/mcp"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/ui"
	"github.com/ai-on-gke/grain/pkg/version"
)

// podIPEnv is the environment variable a Kubernetes-backed deployment
// learns its own pod's address from. Nothing sets it for grain
// automatically -- deploy/kubernetes/deployment.yaml fills it from the
// downward API (fieldRef status.podIP), which is the only way a pod can
// be told its own address at all.
const PodIPEnv = "GRAIN_POD_IP"

// podNameEnv is the same arrangement for this pod's *name*, which the
// daemon needs to ask the kubelet how many times it has restarted
// (daemonHealthFor). A pod cannot learn its own name any more than it
// can learn its own address -- the Deployment's args are a literal list
// and the name is assigned when the ReplicaSet creates the pod -- so
// deploy/kubernetes/grain.yaml fills this from the downward API
// (fieldRef metadata.name) too.
const PodNameEnv = "GRAIN_POD_NAME"

// ownPodName is this pod's name, or "" on a deployment whose manifest
// predates podNameEnv. Empty is an answer rather than a failure here:
// the restart count is reported as untakeable, with that reason.
func ownPodName() string { return strings.TrimSpace(os.Getenv(PodNameEnv)) }

// DefaultKubeconfigPath is the standard out-of-cluster kubeconfig path
// under the grain state root (/var/lib/grain/kubeconfig).
const DefaultKubeconfigPath = "/var/lib/grain/kubeconfig"

var defaultKubeconfigPath = DefaultKubeconfigPath

// EffectiveK8sKubeconfig resolves the kubeconfig path to pass to
// pkg/k8s.Client, mcp.KubectlExecRunner, and `granule forward`.
//
// An explicit cfg.K8sKubeconfig wins. Otherwise, when KUBECONFIG is not
// already set in the process environment, it checks <data-dir>/kubeconfig
// and /var/lib/grain/kubeconfig (when -data-dir is /var/lib/grain/data or
// unset) and returns the first regular file that exists.
func EffectiveK8sKubeconfig(cfg Config) string {
	if p := strings.TrimSpace(cfg.K8sKubeconfig); p != "" {
		return p
	}
	if strings.TrimSpace(os.Getenv("KUBECONFIG")) != "" {
		return ""
	}
	var candidates []string
	if dir := strings.TrimSpace(cfg.DataDir); dir != "" {
		cleanDir := filepath.Clean(dir)
		candidates = append(candidates, filepath.Join(cleanDir, "kubeconfig"))
		if cleanDir == "/var/lib/grain/data" || filepath.Dir(cleanDir) == filepath.Dir(defaultKubeconfigPath) {
			candidates = append(candidates, defaultKubeconfigPath)
		}
	} else if defaultKubeconfigPath != "" {
		candidates = append(candidates, defaultKubeconfigPath)
	}
	for _, p := range candidates {
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			return p
		}
	}
	return ""
}

// k8sGitProxyHostOrPodIP resolves -k8s-git-proxy-host: the flag when it
// names something, and this pod's own IP otherwise.
//
// The fallback exists because the flag's right value in-cluster is not
// something a manifest can write down. A Deployment's args are a literal
// list with no variable expansion, and the address is assigned at
// scheduling time regardless -- so the manifest asks Kubernetes to put
// it in the environment and this reads it back. Sandbox pods route to a
// pod IP directly, so this alone reaches the proxy -- for as long as this
// pod lives. A sandbox that outlives it, under a run the next daemon pod
// adopts, needs -k8s-sandbox-service as well (K8sSandboxServiceHost).
func K8sGitProxyHostOrPodIP(flagValue string) string {
	if flagValue != "" {
		return flagValue
	}
	return strings.TrimSpace(os.Getenv(PodIPEnv))
}

// K8sSandboxServiceHost resolves -k8s-sandbox-service to the cluster IP
// sandboxes are handed for the git proxy and the model proxies
// (K8sServiceFrontedNames), in place of this pod's own IP.
//
// The address has to outlive this pod. A run pod survives a rollout of
// the daemon Deployment and is adopted by the next daemon pod
// (docs/non-disruptive-upgrade.md, Stage 2b), and its sandbox was handed
// its URLs once, when it was created: this pod's IP is gone after the
// roll, and the Service's cluster IP is not. The port block
// (-sandbox-port-base) is what keeps the port half of those URLs.
//
// It is the cluster IP and not the Service's DNS name that a sandbox is
// handed, so that a sandbox needs no resolver to reach it: a pod whose
// task asked for no network may reach only the daemon pod
// (deploy/kubernetes/networkpolicy.yaml), and kube-dns is not that pod.
// A bare name is looked up in namespace, a dotted one as written.
func K8sSandboxServiceHost(ctx context.Context, lookup func(context.Context, string) ([]string, error), service, namespace string) (string, error) {
	name := service
	if !strings.Contains(name, ".") {
		name = service + "." + namespace + ".svc"
	}
	// Retried until ctx ends, because a first `kubectl apply` creates the
	// Service a moment after the Deployment and cluster DNS learns of it
	// a moment after that.
	addrs, err := lookup(ctx, name)
	for err != nil {
		select {
		case <-ctx.Done():
			return "", fmt.Errorf("resolving -k8s-sandbox-service %s: %w", name, err)
		case <-time.After(2 * time.Second):
		}
		addrs, err = lookup(ctx, name)
	}
	if len(addrs) != 1 {
		// A headless Service resolves to its pods' own addresses, which is
		// exactly the address this exists not to hand out.
		return "", fmt.Errorf("-k8s-sandbox-service %s resolves to %d addresses (%s), not one cluster IP -- "+
			"it has to be a ClusterIP Service, not a headless one, or sandboxes are handed a pod's address "+
			"again", name, len(addrs), strings.Join(addrs, ", "))
	}
	return addrs[0], nil
}

// AdvertiseViaK8sService points fabric's git proxy and model proxies at
// -k8s-sandbox-service's cluster IP, and does nothing on a deployment
// that names no Service or is not on Kubernetes.
//
// A Service that cannot be resolved fails startup rather than falling
// back to this pod's IP: the fallback would work until the first
// rollout and then strand every adopted run's push, which is the failure
// this exists to remove, moved somewhere harder to see.
func AdvertiseViaK8sService(ctx context.Context, cfg Config, fabric *Direct, lookup func(context.Context, string) ([]string, error)) error {
	if !KubernetesMode(cfg) || cfg.K8sSandboxService == "" {
		return nil
	}
	namespace := cfg.K8sNamespace
	if namespace == "" {
		namespace = model.DefaultK8sNamespace
	}
	lookupCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	host, err := K8sSandboxServiceHost(lookupCtx, lookup, cfg.K8sSandboxService, namespace)
	cancel()
	if err != nil {
		return err
	}
	gitProxyHost := EffectiveK8sGitProxyHost(cfg)
	if host == gitProxyHost {
		// What a headless Service resolves to while it has one pod.
		return fmt.Errorf("-k8s-sandbox-service %s resolves to this pod's own address %s -- it has to be a "+
			"ClusterIP Service rather than a headless one, or a sandbox that outlives this pod is left "+
			"holding an address that has gone", cfg.K8sSandboxService, host)
	}
	names := K8sServiceFrontedNames()
	if err := fabric.AdvertiseVia(host, names); err != nil {
		return err
	}
	log.Printf("grain daemon: sandboxes reach the %s at %s (-k8s-sandbox-service %s), which outlives this "+
		"pod; everything else they reach at %s", strings.Join(names, ", "), host, cfg.K8sSandboxService,
		gitProxyHost)
	return nil
}

// K8sWorkspaceFor resolves the working directory inside a Kubernetes sandbox
// when workspace is unset: /home/debian for kubernetes-granule and /home/agent
// for kubernetes.
func K8sWorkspaceFor(backend model.SandboxBackend, workspace string) string {
	if workspace != "" {
		return workspace
	}
	if backend == model.SandboxBackendKubernetesGranule {
		return model.DefaultK8sGranuleWorkspace
	}
	return model.DefaultK8sWorkspace
}

// EffectiveK8sWorkspace returns the workspace directory for cfg's selected
// Kubernetes backend, falling back to the backend's default when unset.
func EffectiveK8sWorkspace(cfg Config) string {
	return K8sWorkspaceFor(cfg.SandboxBackend(), cfg.K8sWorkspace)
}

// EffectiveK8sGitProxyHost resolves the address Kubernetes sandbox pods reach
// the daemon's git and model proxies at: K8sGitProxyHost, $GRAIN_POD_IP,
// 127.0.0.1 for off-cluster kubernetes-granule sandboxes reached over
// `granule forward`, or the host deployment's GranuleGitProxyHost /
// ContainerGitProxyHost fallback for plain kubernetes pods.
func EffectiveK8sGitProxyHost(cfg Config) string {
	if host := K8sGitProxyHostOrPodIP(cfg.K8sGitProxyHost); host != "" {
		return host
	}
	if cfg.K8sGranuleSandboxes {
		return "127.0.0.1"
	}
	if cfg.GranuleGitProxyHost != "" {
		return cfg.GranuleGitProxyHost
	}
	return cfg.ContainerGitProxyHost
}

// EffectiveK8sSandboxImage resolves the container image for kubernetes sandbox
// pods, falling back to DefaultK8sSandboxImage (or the version-pinned default)
// when unset.
func EffectiveK8sSandboxImage(cfg Config) string {
	if cfg.K8sSandboxImage != "" {
		return cfg.K8sSandboxImage
	}
	if cfg.K8sImage != "" {
		return cfg.K8sImage
	}
	if DefaultK8sSandboxImage != model.DefaultK8sSandboxImage {
		return DefaultK8sSandboxImage
	}
	return ui.DefaultK8sSandboxImageFor(installtrack.TrackOSS, version.Get())
}

// EffectiveK8sGranuleImage resolves the packed guest image for
// kubernetes-granule sandbox pods, falling back to DefaultSandboxImage (or the
// version-pinned default) when unset, and mirroring the stamped guest image to
// SandboxImageMirror (or a derived cross-project guest mirror repository) when
// configured.
func EffectiveK8sGranuleImage(cfg Config) string {
	return effectiveK8sGranuleImage(cfg, func(base, mirrorRepo string) string {
		return resolveOrMirrorSandboxImage(context.Background(), cfg, base, mirrorRepo)
	})
}

// k8sGranuleImageForSettings is ui.Config.K8sGranuleImage: the guest image
// kubernetes-granule pods are created from under mc, laid over the
// configuration running holds -- the stamp this build carries and the
// mirror it is copied into, which the UI's own version-derived default
// knows nothing of -- so that the Settings pane probes the image the
// daemon really runs rather than one it never asks for.
//
// The stored k8s_granule_image is restart-only (withLiveModelConfig keeps
// running's), but it is what the pane shows, so a stored one is what is
// probed. Nothing is copied into the mirror from here: a settings request
// answers from what startup (or applySandboxImage) already mirrored, and
// from the mirror's tag where nothing has been.
func k8sGranuleImageForSettings(running Config, mc model.Config) string {
	cfg := running.withLiveModelConfig(mc)
	if pinned := strings.TrimSpace(mc.K8sGranuleImage); pinned != "" {
		cfg.K8sGranuleImage = pinned
	}
	return effectiveK8sGranuleImage(cfg, mirroredSandboxImageIfKnown)
}

// effectiveK8sGranuleImage is EffectiveK8sGranuleImage with how the
// stamped guest becomes its mirrored reference left to mirror.
func effectiveK8sGranuleImage(cfg Config, mirror func(base, mirrorRepo string) string) string {
	base := DefaultSandboxImage
	if base == model.DefaultSandboxImage {
		base = ui.DefaultSandboxImageFor(installtrack.TrackOSS, version.Get())
	}
	if mirrorRepo := sandboxImageMirrorRepo(cfg); mirrorRepo != "" {
		// -k8s-granule-image's flag default is the stamp itself, so a
		// daemon started with -sandbox-image-mirror and no image of its
		// own arrives here with the stamp in K8sGranuleImage: that is
		// the image to mirror, not a pin that opts out of the mirror.
		pinned := cfg.K8sGranuleImage
		if pinned == DefaultSandboxImage || pinned == model.DefaultSandboxImage || pinned == base {
			pinned = ""
		}
		if pinned != "" && !isMirroredGuestRef(pinned, mirrorRepo) {
			return pinned
		}
		return mirror(base, mirrorRepo)
	}
	if cfg.K8sGranuleImage != "" {
		return cfg.K8sGranuleImage
	}
	return base
}

// parseKeyValues turns repeated "key=value" flag values into the map a
// pod spec's nodeSelector wants. checkConfig has already refused an
// entry with no "=", so anything reaching here splits; a value
// containing further "=" keeps them, since only the first separates.
func ParseKeyValues(entries []string) map[string]string {
	if len(entries) == 0 {
		return nil
	}
	out := make(map[string]string, len(entries))
	for _, entry := range entries {
		key, value, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		out[strings.TrimSpace(key)] = value
	}
	return out
}

// checkSandboxBackend refuses a configuration with no sandbox backend to
// build. No backend flag is still a startable deployment when the plumbing
// for one was passed, since the Settings pane's stored choice
// (model.Config.SandboxBackend) selects among those; run() refuses once
// that has been read if it selected nothing either. local is whether this
// build carries the test-only directory backend (localSandboxes). It also
// refuses a backend this deployment's run hosts cannot reach
// (CheckRunHostBackend), and host sandboxes on a daemon others can reach
// (CheckHostSandboxes). On Kubernetes it warns of any agent session
// store a pod restart would lose (warnSessionStoresOffClaim).
func CheckSandboxBackend(cfg Config, local bool) error {
	if err := CheckRunHostBackend(cfg); err != nil {
		return err
	}
	warnSessionStoresOffClaim(cfg)
	if err := CheckHostSandboxes(cfg); err != nil {
		return err
	}
	if len(SelectedSandboxBackends(cfg)) > 0 || cfg.hasStartupSandboxPlumbing() || local {
		return nil
	}
	return errors.New("no sandbox backend selected: pass -container-sandboxes, -granule-sandboxes, -k8s-sandboxes, " +
		"-k8s-granule-sandboxes, -host-sandboxes, or -disable-native-sandboxes")
}

func (c Config) hasStartupSandboxPlumbing() bool {
	return c.DisableNativeSandboxes ||
		(c.HostSandboxes && c.SandboxDir != "") ||
		(c.ContainerImage != "" && c.ContainerGitProxyHost != "" && c.RunHostImage == "") ||
		(c.GranuleSSHUser != "" && c.GranuleWorkspace != "" && c.GranuleGitProxyHost != "") ||
		(c.K8sWorkspace != "" && c.K8sGitProxyHost != "")
}

// HostSandboxesUsage and AllowUnisolatedHostSandboxesUsage are the help
// text of the two flags CheckHostSandboxes reads.
const (
	HostSandboxesUsage = "dispatch each run into its own plain directory under -sandbox-dir on this host, with NO isolation: runs " +
		"execute as this daemon's user, with this host's tools and network. For a single-user workstation; " +
		"refused on a daemon others can reach (a non-loopback -ui-addr, -executor-addr, IAP, -run-host-image) " +
		"unless -allow-unisolated-host-sandboxes is also passed"
	AllowUnisolatedHostSandboxesUsage = "let -host-sandboxes start on a daemon others can reach, accepting that everyone " +
		"who can file a task there may run anything on this host as this daemon's user"
	HostSandboxEnvUsage = "NAME or NAME=VALUE, repeatable, only with -host-sandboxes: an environment variable every host-sandbox " +
		"run of this machine's own tasks (not anybody else's) gets on top of the default allowlist -- NAME alone passes this daemon's own value, NAME=VALUE " +
		"sets that one. For ambient workstation credentials a run should use as you: SSH_AUTH_SOCK, HTTPS_PROXY/" +
		"HTTP_PROXY/NO_PROXY, CLOUDSDK_CONFIG=$HOME/.config/gcloud. Every run then acts with that identity. HOME " +
		"cannot be set (it stays pinned to the run's directory, which keeps its git credentials its own). Values reach " +
		"runs through a 0600 file and are redacted from what run_command returns, but a NAME=VALUE entry sits on this " +
		"daemon's own command line, so one with credentials in a URL (user:pass@) is refused: export it and pass NAME alone"
)

// ResolveHostSandboxEnv turns -host-sandbox-env's entries into the
// NAME=VALUE list orchestrator.HostSandboxes.WithEnv takes: a bare NAME
// is looked up in lookup (this daemon's environment) and an entry with
// "=" is taken as written. A bare NAME this daemon does not have is
// returned in unset rather than refused -- an SSH agent that is not
// running today is not a reason the daemon cannot start -- so the caller
// can say so. A name that is not one, or HOME, is refused
// (mcp.ValidateSandboxEnv); so is a name given twice, since which one a
// run would see is otherwise a question of ordering. So is a literal
// entry whose value carries a URL's userinfo password ("user:pass@"):
// that entry is a flag on this daemon's own command line, readable by
// every user on the host for as long as it runs, and the same value
// passed by NAME from this daemon's environment is not (GSA1-12).
func ResolveHostSandboxEnv(entries []string, lookup func(string) (string, bool)) (env, unset []string, err error) {
	seen := map[string]bool{}
	for _, entry := range entries {
		name, _, literal := strings.Cut(entry, "=")
		if seen[name] {
			return nil, nil, fmt.Errorf("-host-sandbox-env names %s more than once", name)
		}
		seen[name] = true
		kv := entry
		if !literal {
			value, ok := lookup(name)
			if !ok {
				if err := mcp.ValidateSandboxEnv([]string{name + "="}); err != nil {
					return nil, nil, fmt.Errorf("-host-sandbox-env: %w", err)
				}
				unset = append(unset, name)
				continue
			}
			kv = name + "=" + value
		} else if urlPassword.MatchString(entry) {
			return nil, nil, fmt.Errorf("-host-sandbox-env %s=...: the value carries a password (user:pass@), and "+
				"a NAME=VALUE entry sits on this daemon's command line where every user on the host can read it -- "+
				"export %s in the daemon's environment and pass -host-sandbox-env %s alone", name, name, name)
		}
		if err := mcp.ValidateSandboxEnv([]string{kv}); err != nil {
			return nil, nil, fmt.Errorf("-host-sandbox-env: %w", err)
		}
		env = append(env, kv)
	}
	return env, unset, nil
}

// urlPassword is a URL's userinfo with a password in it -- the
// "user:pass@" of a proxy URL -- which ResolveHostSandboxEnv refuses in a
// literal entry.
var urlPassword = regexp.MustCompile(`://[^/\s@:]*:[^/\s@]*@`)

// CheckHostSandboxes refuses -host-sandboxes anywhere it would hand
// somebody other than this host's own user a shell on it.
//
// A host sandbox is a directory and nothing more: whatever a run does, it
// does as the daemon's own user, on the daemon's own filesystem and
// network. That is the point of it on a single-user workstation, where
// the agent is meant to reach the same compilers, CLIs and proxies its
// owner does. It is also why it does not start on a daemon that is
// plainly shared -- one whose UI listens beyond loopback, which takes
// remote executors, sits behind IAP, or hands its runs to run hosts or a
// cluster -- unless the operator says, with
// -allow-unisolated-host-sandboxes, that every person who can file a
// task there may run anything on this host. Local policy then
// still keeps tasks that are not this machine's own off the host
// (policy.Policy.CheckUnisolated) unless it is told otherwise.
func CheckHostSandboxes(cfg Config) error {
	if !cfg.HostSandboxes {
		if len(cfg.HostSandboxEnv) > 0 {
			return errors.New("-host-sandbox-env is only for -host-sandboxes: every other backend runs its " +
				"commands in a guest whose environment is its own")
		}
		return nil
	}
	if _, _, err := ResolveHostSandboxEnv(cfg.HostSandboxEnv, os.LookupEnv); err != nil {
		return err
	}
	if cfg.SandboxDir == "" {
		return errors.New("-sandbox-dir is required with -host-sandboxes: it is the directory each run's own " +
			"working directory is created under")
	}
	if cfg.K8sSandboxes || cfg.K8sGranuleSandboxes {
		// Not a matter of trust: a pod has no host directory to hand out.
		return errors.New("-host-sandboxes cannot be used with Kubernetes sandboxes")
	}
	shared := hostSandboxSharingFlags(cfg)
	if len(shared) > 0 && !cfg.AllowUnisolatedHostSandboxes {
		return fmt.Errorf("-host-sandboxes runs every task unisolated, as this daemon's own user on this host, "+
			"and this daemon is reachable by others (%s) -- bind -ui-addr to loopback (e.g. 127.0.0.1:4242) "+
			"and drop the shared listeners for a single-user workstation, use -container-sandboxes or "+
			"-granule-sandboxes, or pass -allow-unisolated-host-sandboxes if everyone who can file a task "+
			"here may run anything on this host", strings.Join(shared, ", "))
	}
	return nil
}

// checkHostConfinement is mcp.CheckHostConfinement, a variable so tests
// can stand in for a host with or without bubblewrap.
var checkHostConfinement = mcp.CheckHostConfinement

// hostSandboxHiddenPaths is what -host-sandboxes runs every run's
// run_command with hidden (orchestrator.HostSandboxes.WithHidden): this
// daemon's data directory, which holds its local API token, plus its
// database, secrets and keys.
//
// That token is the access code, and the code with the UI's URL is full
// owner access (docs/access-code.md), so a host where run_command cannot
// hide it (no bubblewrap, or a kernel that refuses unprivileged user
// namespaces) is refused at start-up rather than left with the code one
// `cat` away from every agent.
func hostSandboxHiddenPaths(ctx context.Context, cfg Config) ([]string, error) {
	if cfg.DataDir == "" {
		return nil, nil
	}
	dataDir, err := filepath.Abs(cfg.DataDir)
	if err != nil {
		return nil, err
	}
	if err := checkHostConfinement(ctx); err != nil {
		return nil, fmt.Errorf("-host-sandboxes needs every run's commands to run with "+
			"this daemon's data directory %s hidden, because its access code lives there and a run executes "+
			"as this daemon's own account -- and that cannot be done on this host: %w. Install bubblewrap "+
			"(apt-get install bubblewrap) and allow it unprivileged user namespaces, or use "+
			"-container-sandboxes or -granule-sandboxes", dataDir, err)
	}
	log.Printf("grain daemon: host sandboxes run every command under bubblewrap with %s hidden", dataDir)
	return []string{dataDir}, nil
}

// HostSandboxRunsDir is the directory -host-sandboxes creates each run's
// own directory under: "runs" inside -sandbox-dir, not -sandbox-dir
// itself. orchestrator.HostSandboxes.ReapOrphans removes everything under
// its root that no live run holds, and -sandbox-dir also holds the package
// and Actions caches by default (<sandbox-dir>/cache), which a reap of the
// whole directory would delete at every start.
func HostSandboxRunsDir(cfg Config) string { return filepath.Join(cfg.SandboxDir, "runs") }

// hostSandboxSharingFlags names each setting that makes this daemon
// reachable by somebody other than the user on its own host.
func hostSandboxSharingFlags(cfg Config) []string {
	var shared []string
	if cfg.UiAddr != "" && !loopbackListenAddr(cfg.UiAddr) {
		shared = append(shared, fmt.Sprintf("-ui-addr %s", cfg.UiAddr))
	}
	if cfg.ExecutorAddr != "" {
		shared = append(shared, "-executor-addr")
	}
	if cfg.IapBackendService != "" || cfg.IapResource != "" {
		shared = append(shared, "IAP")
	}
	if cfg.RunHostImage != "" {
		shared = append(shared, "-run-host-image")
	}
	return shared
}

// loopbackListenAddr reports whether a host:port listen address binds
// loopback only. An empty host (":4242") and an unspecified one
// ("0.0.0.0", "::") bind every interface, so they are not.
func loopbackListenAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// selectedSandboxBackends names every sandbox backend flag this config
// passed, so checkConfig can refuse two at once and say which two.
func SelectedSandboxBackends(cfg Config) []string {
	var selected []string
	if cfg.DisableNativeSandboxes {
		selected = append(selected, "-disable-native-sandboxes")
	}
	if cfg.ContainerSandboxes {
		selected = append(selected, "-container-sandboxes")
	}
	if cfg.GranuleSandboxes {
		selected = append(selected, "-granule-sandboxes")
	}
	if cfg.K8sSandboxes {
		selected = append(selected, "-k8s-sandboxes")
	}
	if cfg.K8sGranuleSandboxes {
		selected = append(selected, "-k8s-granule-sandboxes")
	}
	if cfg.HostSandboxes {
		selected = append(selected, "-host-sandboxes")
	}
	return selected
}

// sandboxBackend is the one backend this configuration selects, or "" when
// it selects none -- which checkConfig refuses outside of tests
// (localSandboxes).
func (c Config) SandboxBackend() model.SandboxBackend {
	switch {
	case c.DisableNativeSandboxes:
		return model.SandboxBackendNone
	case c.ContainerSandboxes:
		return model.SandboxBackendContainer
	case c.GranuleSandboxes:
		return model.SandboxBackendGranule
	case c.K8sSandboxes:
		return model.SandboxBackendKubernetes
	case c.K8sGranuleSandboxes:
		return model.SandboxBackendKubernetesGranule
	case c.HostSandboxes:
		return model.SandboxBackendHost
	default:
		return ""
	}
}

func (c Config) withSandboxBackend(backend model.SandboxBackend) Config {
	if c.DisableNativeSandboxes {
		c.startupDisableNativeSandboxes = true
	}
	if c.HostSandboxes {
		c.startupHostSandboxes = true
	}
	c.DisableNativeSandboxes = backend == model.SandboxBackendNone
	c.ContainerSandboxes = backend == model.SandboxBackendContainer
	c.GranuleSandboxes = backend == model.SandboxBackendGranule
	c.K8sSandboxes = backend == model.SandboxBackendKubernetes
	c.K8sGranuleSandboxes = backend == model.SandboxBackendKubernetesGranule
	c.HostSandboxes = backend == model.SandboxBackendHost
	return c
}

// sandboxBackendAvailability reports whether this process was started with
// all host-side plumbing needed to construct backend after a restart, and when
// false, the human-readable reason why it is unavailable.
//
// The selector flags seed the initial choice; their companion arguments are
// what make other choices safe to offer in Settings. Settings returns every
// backend in model.SandboxBackends() with its availability and reason so no
// backend is silently omitted from the dropdown.
func (c Config) sandboxBackendAvailability(backend model.SandboxBackend) (bool, string) {
	switch backend {
	case model.SandboxBackendNone:
		if !c.DisableNativeSandboxes && !c.startupDisableNativeSandboxes {
			return false, "needs -disable-native-sandboxes at daemon startup"
		}
		return true, ""
	case model.SandboxBackendHost:
		// Only on a daemon started with -host-sandboxes: no isolation at
		// all is not something a Settings choice may switch a deployment
		// to, since the flag is where CheckHostSandboxes asks whether
		// this daemon is a workstation's own.
		if !c.HostSandboxes && !c.startupHostSandboxes {
			return false, "needs -host-sandboxes at daemon startup"
		}
		if c.SandboxDir == "" {
			return false, "needs a sandbox directory (-sandbox-dir)"
		}
		return true, ""
	case model.SandboxBackendContainer:
		// Not with run hosts, which cannot reach a container sandbox
		// (CheckRunHostBackend).
		if c.RunHostImage != "" {
			return false, "not available when run hosts (-run-host-image) are enabled"
		}
		switch {
		case c.ContainerImage == "" && c.ContainerGitProxyHost == "":
			return false, "needs a container image and a git proxy host"
		case c.ContainerImage == "":
			return false, "needs a container image (-container-sandbox-image)"
		case c.ContainerGitProxyHost == "":
			return false, "needs a git proxy host (-container-git-proxy-host)"
		default:
			return true, ""
		}
	case model.SandboxBackendGranule:
		var missing []string
		if c.GranuleSSHUser == "" {
			missing = append(missing, "SSH user")
		}
		if c.GranuleWorkspace == "" {
			missing = append(missing, "workspace")
		}
		if c.GranuleGitProxyHost == "" {
			missing = append(missing, "git proxy host")
		}
		if len(missing) > 0 {
			return false, fmt.Sprintf("needs %s for granule", strings.Join(missing, "/"))
		}
		return true, ""
	case model.SandboxBackendKubernetes, model.SandboxBackendKubernetesGranule:
		// Both Kubernetes backends have built-in image, workspace, and git
		// proxy host fallbacks (EffectiveK8sSandboxImage,
		// EffectiveK8sGranuleImage, EffectiveK8sWorkspace,
		// EffectiveK8sGitProxyHost) and the release container image ships
		// kubectl (Dockerfile). Off-cluster machine deployments (such as a
		// corp VM in container mode) upgrade via upgrade.ModeImage on the
		// host rather than in-cluster upgrade.ModeKube, so c.UpgradeImage
		// only blocks kubernetes-granule when the daemon itself runs inside
		// a Kubernetes pod without an image ref file.
		if backend == model.SandboxBackendKubernetesGranule &&
			c.UpgradeImage != "" && c.UpgradeImageRefFile == "" && InKubernetesPod() {
			return false, "not available in-cluster when -upgrade-image is set without -upgrade-image-ref-file"
		}
		for _, entry := range c.K8sNodeSelector {
			if !strings.Contains(entry, "=") {
				return false, fmt.Sprintf("node selector entry '%s' is not key=value", entry)
			}
		}
		return true, ""
	default:
		return false, fmt.Sprintf("unknown sandbox backend %q", backend)
	}
}

func (c Config) sandboxBackendAvailable(backend model.SandboxBackend) bool {
	ok, _ := c.sandboxBackendAvailability(backend)
	return ok
}

// SandboxBackendOptions returns every backend in model.SandboxBackends() in
// presentation order, with Available and Reason populated for this daemon
// configuration.
func (c Config) SandboxBackendOptions() []model.SandboxBackendOption {
	all := model.SandboxBackends()
	out := make([]model.SandboxBackendOption, 0, len(all))
	for _, backend := range all {
		ok, reason := c.sandboxBackendAvailability(backend)
		out = append(out, model.SandboxBackendOption{
			Backend:   backend,
			Available: ok,
			Reason:    reason,
		})
	}
	return out
}

func (c Config) sandboxBackendOptions() []model.SandboxBackendOption {
	return c.SandboxBackendOptions()
}

// AvailableSandboxBackends lists the backends this daemon can construct, in
// model.SandboxBackends() order.
func (c Config) AvailableSandboxBackends() []model.SandboxBackend {
	return c.availableSandboxBackends()
}

func (c Config) availableSandboxBackends() []model.SandboxBackend {
	var available []model.SandboxBackend
	for _, backend := range model.SandboxBackends() {
		if c.sandboxBackendAvailable(backend) {
			available = append(available, backend)
		}
	}
	return available
}

// sandboxAdvertiseHost is the address this deployment's sandboxes reach
// this process at, in place of the loopback its listeners would otherwise
// bind and name.
//
// Empty for host sandboxing, which shares this process's network
// namespace and so reaches loopback for real. The two isolated backends
// each name their own address, because neither a VM guest nor a pod can
// reach this process's 127.0.0.1 and each learns the right address a
// different way (a docker bridge gateway an operator looks up, versus
// this pod's own IP from the downward API).
//
// It has one caller: the Direct fabric run() builds from it
// (sandboxnet.go). Every sandbox-facing listener then asks that fabric
// rather than asking this, which is what stops one of them answering for
// a different backend than the rest -- the bug sandboxnet.go's header
// describes, where the model proxies read cfg.granuleGitProxyHost while
// the git proxy and both caches came through here.
func SandboxAdvertiseHost(cfg Config) string {
	if cfg.K8sSandboxes || cfg.K8sGranuleSandboxes {
		return EffectiveK8sGitProxyHost(cfg)
	}
	if cfg.ContainerSandboxes {
		return cfg.ContainerGitProxyHost
	}
	if cfg.HostSandboxes {
		return ""
	}
	return cfg.GranuleGitProxyHost
}

// InKubernetesPod reports whether this daemon process itself is running
// inside a Kubernetes pod.
func InKubernetesPod() bool {
	return ownPodName() != "" ||
		strings.TrimSpace(os.Getenv(PodIPEnv)) != "" ||
		strings.TrimSpace(os.Getenv("KUBERNETES_SERVICE_HOST")) != ""
}

// KubernetesSandboxes reports whether this deployment schedules its
// sandboxes on a Kubernetes cluster (-k8s-sandboxes or
// -k8s-granule-sandboxes), regardless of whether the daemon itself runs
// in-cluster or on an off-cluster host.
func KubernetesSandboxes(cfg Config) bool {
	return cfg.K8sSandboxes || cfg.K8sGranuleSandboxes
}

// KubernetesMode reports whether this daemon is running as part of an
// in-cluster Kubernetes deployment rather than owning a machine.
//
// Plain -k8s-sandboxes is always an in-cluster deployment. With
// -k8s-granule-sandboxes, the controller may either run inside a cluster
// pod (deploy/kubernetes/grain-granule.yaml, where GRAIN_POD_NAME,
// GRAIN_POD_IP, or KUBERNETES_SERVICE_HOST is set) or on an off-cluster
// machine (such as a corp VM in container mode) that schedules sandbox
// pods onto a remote GKE cluster via kubeconfig and `granule forward`.
// An off-cluster controller still owns its host, so it keeps hostd,
// host stats/controls, and container-mode self-upgrade.
func KubernetesMode(cfg Config) bool {
	return cfg.K8sSandboxes || (cfg.K8sGranuleSandboxes && InKubernetesPod())
}
