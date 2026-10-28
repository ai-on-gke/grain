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

// grain/task-358: a deployment running inside a Kubernetes cluster has
// no host of its own, and the controls that assume one -- reboot, power
// off, restart, the `top` snapshot, and the host's load and disk
// figures -- must not be wired at all under it. Each is nil
// rather than broken, which is ui.Config's existing contract for "not
// available here" and what makes the UI hide them instead of offering
// buttons that would take out a node full of other people's workloads.
//
// The Logs tab's sources are the same question asked of a map rather
// than a field (logSources, grain/task-488): a source that reads a unit
// this shape has no systemd for is left out, in place of a tab entry
// that can only error.
//
// These test the wiring in this file rather than what the cluster does
// with it: the flags, what checkConfig refuses, which backend run()
// would build, and which fields kubernetesMode withholds.
package cli

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/daemon"

	"github.com/ai-on-gke/grain/pkg/systemlog"
)

// kubeConfig is a config that would start: everything -k8s-sandboxes
// requires, plus the fields checkConfig demands of any deployment.
func kubeConfig() daemon.Config {
	return daemon.Config{
		DataDir:         "/data",
		K8sSandboxes:    true,
		K8sNamespace:    "grain",
		K8sImage:        "example/grain-sandbox:latest",
		K8sWorkspace:    "/home/agent",
		K8sGitProxyHost: "10.1.2.3",
		K8sDeployment:   "grain",
		K8sContainer:    "grain",
		MaxWorkers:      1,
		PollInterval:    30 * time.Second,
		AgentFramework:  "claude",
	}
}

func TestKubernetesConfigNeedsNoSandboxDir(t *testing.T) {
	t.Parallel()
	// -sandbox-dir roots HostSandboxes' own directories, and this
	// backend has none: a sandbox is a pod.
	if err := checkConfig(kubeConfig()); err != nil {
		t.Fatalf("checkConfig on a Kubernetes deployment: %v", err)
	}
}

func TestKubernetesConfigRefusesWhatItCannotRunWithout(t *testing.T) {
	t.Parallel()
	for name, mangle := range map[string]func(*daemon.Config){
		"no workspace": func(c *daemon.Config) { c.K8sWorkspace = "" },
		// Without an address a pod can reach this process at, every
		// checkout in every sandbox fails against a loopback that is the
		// pod's own.
		"no git proxy host":    func(c *daemon.Config) { c.K8sGitProxyHost = "" },
		"a bad node selector":  func(c *daemon.Config) { c.K8sNodeSelector = []string{"nodepool"} },
		"two sandbox backends": func(c *daemon.Config) { c.GranuleSandboxes = true },
		"three sandbox backends": func(c *daemon.Config) {
			c.GranuleSandboxes = true
			c.ContainerSandboxes = true
		},
	} {
		cfg := kubeConfig()
		mangle(&cfg)
		if err := checkConfig(cfg); err == nil {
			t.Errorf("checkConfig accepted a deployment with %s", name)
		}
	}
}

func TestTwoSandboxBackendsAreNamedInTheRefusal(t *testing.T) {
	t.Parallel()
	cfg := kubeConfig()
	cfg.GranuleSandboxes = true
	err := checkConfig(cfg)
	if err == nil {
		t.Fatal("checkConfig accepted two sandbox backends")
	}
	// Naming which two is what tells an operator which flag to drop.
	for _, want := range []string{"-granule-sandboxes", "-k8s-sandboxes"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to name %s", err, want)
		}
	}
}

func TestKubernetesUpgradeNeedsNoRefFile(t *testing.T) {
	t.Parallel()
	// A ref file is how a host deployment tells its unit which image to
	// start. A Deployment's own spec is that record here, so demanding
	// one would refuse a perfectly configured cluster deployment.
	cfg := kubeConfig()
	cfg.UpgradeImage = "ghcr.io/bwsalmon/grain/grain"
	if err := checkConfig(cfg); err != nil {
		t.Fatalf("checkConfig on a Kubernetes deployment with -upgrade-image: %v", err)
	}
	// But it does need to know what to repoint.
	cfg.K8sDeployment = ""
	if err := checkConfig(cfg); err == nil {
		t.Error("checkConfig accepted an upgradeable Kubernetes deployment with no Deployment to repoint")
	}
}

func TestHostSandboxDeploymentStillNeedsARefFile(t *testing.T) {
	t.Parallel()
	cfg := daemon.Config{
		DataDir: "/data", SandboxDir: "/sandbox", MaxWorkers: 1,
		PollInterval: 30 * time.Second, AgentFramework: "claude",
		UpgradeImage: "ghcr.io/bwsalmon/grain/grain",
	}
	if err := checkConfig(cfg); err == nil {
		t.Error("checkConfig accepted -upgrade-image with no ref file on a non-Kubernetes deployment")
	}
}

func TestKubernetesModeWithholdsEveryHostFacingControl(t *testing.T) {
	t.Parallel()
	cfg := kubeConfig()
	if !daemon.KubernetesMode(cfg) {
		t.Fatal("kubernetesMode is false for a -k8s-sandboxes deployment")
	}
	// Each of these would otherwise act on a machine this deployment
	// does not own -- see the fields' own comments in startUIServer.
	if daemon.HostAction(cfg, func(ctx context.Context) error { return nil }) != nil {
		t.Error("a host power action is wired under Kubernetes")
	}
	if daemon.HostStatsOrNil(cfg, nil, nil) != nil {
		t.Error("HostStats is wired under Kubernetes: it would report a shared node's pressure as this deployment's")
	}
	if daemon.HostVolumesOrNil(cfg, []daemon.HostDisk{{Holds: "store", Path: "/data"}}) != nil {
		t.Error("SandboxVolumes is wired under Kubernetes: the volumes it would size are the node's, not this deployment's")
	}
	if daemon.HostTopOrNil(cfg) != nil {
		t.Error("HostTop is wired under Kubernetes: the processes it would list are not the ones spending the node")
	}
}

func TestHostDeploymentKeepsItsHostFacingControls(t *testing.T) {
	t.Parallel()
	// The other half of the test above: withholding them under
	// Kubernetes must not withhold them everywhere.
	cfg := daemon.Config{DataDir: "/data", SandboxDir: "/sandbox"}
	if daemon.KubernetesMode(cfg) {
		t.Fatal("kubernetesMode is true for a host deployment")
	}
	if daemon.HostAction(cfg, func(ctx context.Context) error { return nil }) == nil {
		t.Error("a host power action is withheld from a deployment that owns its host")
	}
	if daemon.HostStatsOrNil(cfg, nil, nil) == nil {
		t.Error("HostStats is withheld from a deployment that owns its host")
	}
	if daemon.HostVolumesOrNil(cfg, []daemon.HostDisk{{Holds: "store", Path: "/data"}}) == nil {
		t.Error("SandboxVolumes is withheld from a deployment that owns its host, so its Settings pane cannot resolve the floor it is editing")
	}
	if daemon.HostTopOrNil(cfg) == nil {
		t.Error("HostTop is withheld from a deployment that owns its host")
	}
}

// The Logs tab is the same question in a different shape: three of the
// four sources a host deployment shows read a host this deployment does
// not have, and a tab offering them in a pod offers sources that can
// only error (grain/task-488).
func TestKubernetesLogSourcesAreOnlyTheOnesAPodCanAnswer(t *testing.T) {
	t.Setenv(daemon.PodNameEnv, "grain-7d9f-abcde")
	sources := daemon.LogSources(kubeConfig())

	for _, name := range []string{"config-sync", "dmesg"} {
		if _, ok := sources[name]; ok {
			t.Errorf("%q is wired under Kubernetes: there is no such unit and no kernel log of this deployment's own", name)
		}
	}
	// The git proxy's audit log is still a file in -data-dir, which here
	// is the pod's own volume.
	if _, ok := sources["git-proxy-audit"]; !ok {
		t.Error("git-proxy-audit is withheld under Kubernetes, but the proxy writes it into this pod's own data dir")
	}
	// And "daemon" is the same log, read the way this shape has of
	// reading it: `kubectl logs` against this pod, which is what
	// deploy/kubernetes/README.md tells an operator to run by hand.
	daemon, ok := sources["daemon"].(systemlog.Pod)
	if !ok {
		t.Fatalf("daemon log source = %T, want one reading this pod's own container", sources["daemon"])
	}
	if daemon.Name != "grain-7d9f-abcde" || daemon.Container != "grain" {
		t.Errorf("daemon log source reads %q/%q, want this pod's own container", daemon.Name, daemon.Container)
	}
	if daemon.Pods == nil {
		t.Error("the daemon log source has nothing to read through")
	}
}

// A pod that was not told its own name gets no "daemon" source at all,
// rather than one aimed at a pod found by guesswork -- the same refusal
// daemonHealthFor makes for the restart count. The System pane's daemon
// line says why beside the tab.
func TestNoDaemonLogSourceForAPodThatDoesNotKnowItsOwnName(t *testing.T) {
	// Unset rather than absent: this process may itself be running in a
	// pod when the test suite runs.
	t.Setenv(daemon.PodNameEnv, "")
	sources := daemon.LogSources(kubeConfig())

	if _, ok := sources["daemon"]; ok {
		t.Error("a daemon log source is wired for a pod that does not know which pod it is")
	}
	if _, ok := sources["git-proxy-audit"]; !ok {
		t.Error("git-proxy-audit went with it, though nothing about that file needs a pod name")
	}
}

// The other half: withholding three sources under Kubernetes must not
// withhold them from the deployment shape that has them.
func TestHostDeploymentKeepsAllFourLogSources(t *testing.T) {
	t.Parallel()
	sources := daemon.LogSources(daemon.Config{DataDir: "/data", SandboxDir: "/sandbox"})

	for _, name := range []string{"daemon", "git-proxy-audit", "config-sync", "dmesg"} {
		if _, ok := sources[name]; !ok {
			t.Errorf("%q is withheld from a deployment that owns its host", name)
		}
	}
	if _, ok := sources["daemon"].(systemlog.Journalctl); !ok {
		t.Errorf("the host daemon log source = %T, want its unit's journal", sources["daemon"])
	}
}

func TestSandboxAdvertiseHostIsThePodsOwnAddress(t *testing.T) {
	t.Parallel()
	// A pod's 127.0.0.1 is its own, so the git proxy has to be
	// advertised at an address a sandbox pod can route to -- which
	// in-cluster is grain's own pod IP.
	if got := daemon.SandboxAdvertiseHost(kubeConfig()); got != "10.1.2.3" {
		t.Errorf("sandboxAdvertiseHost = %q, want the pod IP", got)
	}
	// Host sandboxing shares this process's network namespace and so
	// reaches loopback for real; advertising anything would be wrong.
	if got := daemon.SandboxAdvertiseHost(daemon.Config{SandboxDir: "/sandbox"}); got != "" {
		t.Errorf("sandboxAdvertiseHost for host sandboxing = %q, want empty", got)
	}
	if got := daemon.SandboxAdvertiseHost(daemon.Config{GranuleSandboxes: true, GranuleGitProxyHost: "172.17.0.1"}); got != "172.17.0.1" {
		t.Errorf("sandboxAdvertiseHost for granule = %q, want the bridge gateway", got)
	}
}

func TestGitProxyHostFallsBackToThePodIPEnvironment(t *testing.T) {
	// A Deployment's args are a literal list with no variable expansion,
	// and a pod's address is assigned at scheduling time -- so the
	// manifest asks Kubernetes to put it in the environment and this
	// reads it back.
	t.Setenv(daemon.PodIPEnv, "10.4.5.6")
	if got := daemon.K8sGitProxyHostOrPodIP(""); got != "10.4.5.6" {
		t.Errorf("k8sGitProxyHostOrPodIP(\"\") = %q, want the pod IP from the environment", got)
	}
	// An explicit flag still wins: a deployment reaching this process
	// through something other than its pod address said so on purpose.
	if got := daemon.K8sGitProxyHostOrPodIP("grain.grain.svc"); got != "grain.grain.svc" {
		t.Errorf("k8sGitProxyHostOrPodIP = %q, want the flag's own value", got)
	}
}

// The restart count under Kubernetes is the kubelet's, not a journal's
// (grain/task-367) -- and a pod that was not told its own name says why
// it has no count rather than reporting none at all.
func TestTheRestartCountUnderKubernetesIsThePodsOwn(t *testing.T) {
	// Unset rather than absent: this process may itself be running in a
	// pod when the test suite runs.
	t.Setenv(daemon.PodNameEnv, "")
	health := daemon.DaemonHealthFor(kubeConfig(), time.Now()).Health()

	if health.Starts != 0 {
		t.Errorf("starts = %d, want none: nothing told this pod its own name", health.Starts)
	}
	if !strings.Contains(health.Error, daemon.PodNameEnv) {
		t.Errorf("error = %q, want the variable the manifest fills from the downward API", health.Error)
	}
	// The shape is reported whether or not the count could be taken: a
	// cluster operator told to re-run scripts/setup.sh has been sent to
	// a host that does not exist.
	if !strings.Contains(health.FlagFix, "deployment/grain") || strings.Contains(health.FlagFix, "setup.sh") {
		t.Errorf("flagFix = %q, want the Deployment's own args", health.FlagFix)
	}
	if !strings.Contains(health.LogCommand, "kubectl -n grain logs") {
		t.Errorf("logCommand = %q, want the pod log, since this image carries no journal", health.LogCommand)
	}

	// The other half: a deployment that owns its host still counts
	// starts in its unit's journal and is still told about its unit.
	host := daemon.DaemonHealthFor(daemon.Config{DataDir: "/data", SandboxDir: "/sandbox"}, time.Now()).Health()
	if !strings.Contains(host.LogCommand, "journalctl -u grain-daemon.service") {
		t.Errorf("logCommand on a host deployment = %q, want its unit's journal", host.LogCommand)
	}
	if !strings.Contains(host.FlagFix, "scripts/setup.sh") {
		t.Errorf("flagFix on a host deployment = %q, want the script that rewrites the unit", host.FlagFix)
	}
}

// A deployment that named neither object still names them in what it
// reports: the flag defaults and the manifest agree on "grain", and a
// health reading that named nothing would be advice an operator cannot
// act on.
func TestTheKubernetesHealthNamesTheObjectsEvenWithNoFlags(t *testing.T) {
	t.Parallel()
	cfg := kubeConfig()
	cfg.K8sDeployment, cfg.K8sContainer = "", ""
	deployment, container := daemon.K8sSelfNames(cfg)
	if deployment != "grain" || container != "grain" {
		t.Errorf("k8sSelfNames = %q/%q, want the names deploy/kubernetes/grain.yaml gives them", deployment, container)
	}
}

func TestParseKeyValuesKeepsEverythingAfterTheFirstSeparator(t *testing.T) {
	t.Parallel()
	got := daemon.ParseKeyValues([]string{"cloud.google.com/gke-nodepool=builds", "topology=zone=b"})
	if got["cloud.google.com/gke-nodepool"] != "builds" {
		t.Errorf("nodeSelector = %v", got)
	}
	if got["topology"] != "zone=b" {
		t.Errorf("a value containing = was truncated: %v", got)
	}
	if daemon.ParseKeyValues(nil) != nil {
		t.Error("parseKeyValues of nothing should be nil, not an empty selector the pod spec would carry")
	}
}
