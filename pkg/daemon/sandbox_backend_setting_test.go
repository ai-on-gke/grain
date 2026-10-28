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
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
)

func TestAvailableSandboxBackendsFollowConfiguredStartupMethods(t *testing.T) {
	t.Parallel()
	cfg := Config{
		SandboxDir:            "/sandboxes",
		ContainerImage:        "example/guest",
		ContainerGitProxyHost: "172.17.0.1",
		GranuleSSHUser:        "root",
		GranuleWorkspace:      "/home/debian",
		GranuleGitProxyHost:   "172.18.0.1",
		K8sWorkspace:          "/workspace",
		K8sGitProxyHost:       "10.0.0.2",
	}
	want := []model.SandboxBackend{
		model.SandboxBackendContainer,
		model.SandboxBackendGranule,
		model.SandboxBackendKubernetes,
		model.SandboxBackendKubernetesGranule,
	}
	if got := cfg.availableSandboxBackends(); !reflect.DeepEqual(got, want) {
		t.Fatalf("availableSandboxBackends = %v, want %v", got, want)
	}

	cfg.ContainerGitProxyHost = ""
	want = []model.SandboxBackend{
		model.SandboxBackendGranule,
		model.SandboxBackendKubernetes,
		model.SandboxBackendKubernetesGranule,
	}
	if got := cfg.availableSandboxBackends(); !reflect.DeepEqual(got, want) {
		t.Fatalf("availableSandboxBackends without container plumbing = %v, want %v", got, want)
	}
}

func TestAvailableSandboxBackendsOffersNothingForASandboxDirAlone(t *testing.T) {
	t.Parallel()
	cfg := Config{SandboxDir: "/sandboxes"}
	want := []model.SandboxBackend{
		model.SandboxBackendKubernetes,
		model.SandboxBackendKubernetesGranule,
	}
	if got := cfg.availableSandboxBackends(); !reflect.DeepEqual(got, want) {
		t.Fatalf("availableSandboxBackends = %v, want %v: host directories are offered only with -host-sandboxes", got, want)
	}
}

func TestAvailableSandboxBackendsOffersOnlyContainersWithoutGranule(t *testing.T) {
	t.Parallel()
	cfg := Config{
		ContainerImage:        "example/guest",
		ContainerGitProxyHost: "172.17.0.1",
	}
	want := []model.SandboxBackend{
		model.SandboxBackendContainer,
		model.SandboxBackendKubernetes,
		model.SandboxBackendKubernetesGranule,
	}
	if got := cfg.availableSandboxBackends(); !reflect.DeepEqual(got, want) {
		t.Fatalf("availableSandboxBackends = %v, want %v", got, want)
	}
}

func TestStoredSandboxBackendSelectsItAtStartupButNotLive(t *testing.T) {
	t.Parallel()
	cfg := Config{
		ContainerSandboxes:    true,
		ContainerImage:        "example/guest",
		ContainerGitProxyHost: "172.17.0.1",
		GranuleSSHUser:        "root",
		GranuleWorkspace:      "/home/debian",
		GranuleGitProxyHost:   "172.18.0.1",
	}
	stored := model.Config{SandboxBackend: model.SandboxBackendGranule}

	runnable, notes := cfg.RunnableStoredConfig(stored)
	for _, note := range notes {
		if strings.Contains(note, "sandbox backend") {
			t.Fatalf("runnableStoredConfig rejected an available backend: %v", notes)
		}
	}
	started := cfg.WithModelConfig(runnable)
	if !started.GranuleSandboxes {
		t.Fatal("stored granule backend did not select granule at startup")
	}

	live := cfg.withLiveModelConfig(stored)
	if live.GranuleSandboxes {
		t.Fatal("live settings refresh changed the running backend")
	}
	if got := live.SandboxBackend(); got != model.SandboxBackendContainer {
		t.Fatalf("live backend = %q, want container until restart", got)
	}
}

func TestUnavailableStoredSandboxBackendFallsBackToStartupSelection(t *testing.T) {
	t.Parallel()
	cfg := Config{ContainerSandboxes: true, ContainerImage: "example/guest", ContainerGitProxyHost: "172.17.0.1"}
	stored := model.Config{SandboxBackend: model.SandboxBackendGranule}

	runnable, notes := cfg.RunnableStoredConfig(stored)
	if runnable.SandboxBackend != model.SandboxBackendContainer {
		t.Fatalf("runnable backend = %q, want container", runnable.SandboxBackend)
	}
	found := false
	for _, note := range notes {
		found = found || strings.Contains(note, "sandbox backend") && strings.Contains(note, "cannot build")
	}
	if !found {
		t.Fatalf("runnableStoredConfig notes = %v, want unavailable-backend explanation", notes)
	}
}

func TestAvailableSandboxBackendsOffersNoneWhenDisabled(t *testing.T) {
	t.Parallel()
	cfg := Config{
		DisableNativeSandboxes: true,
	}
	want := []model.SandboxBackend{
		model.SandboxBackendKubernetes,
		model.SandboxBackendKubernetesGranule,
		model.SandboxBackendNone,
	}
	if got := cfg.availableSandboxBackends(); !reflect.DeepEqual(got, want) {
		t.Fatalf("availableSandboxBackends = %v, want %v", got, want)
	}
	if got := cfg.SandboxBackend(); got != model.SandboxBackendNone {
		t.Fatalf("sandboxBackend = %v, want %v", got, model.SandboxBackendNone)
	}
}

func TestStoredSandboxBackendNoneSelectsDisabledAtStartup(t *testing.T) {
	t.Parallel()
	cfg := Config{
		DisableNativeSandboxes: true,
	}
	stored := model.Config{SandboxBackend: model.SandboxBackendNone}

	runnable, notes := cfg.RunnableStoredConfig(stored)
	for _, note := range notes {
		if strings.Contains(note, "sandbox backend") {
			t.Fatalf("runnableStoredConfig rejected none backend: %v", notes)
		}
	}
	started := cfg.WithModelConfig(runnable)
	if !started.DisableNativeSandboxes {
		t.Fatal("stored none backend did not select disabled native sandboxes at startup")
	}
}

// A row written by a deployment that ran host sandboxes still says "host".
// It is no longer a backend at all, so it reads like any other value this
// daemon cannot build: the flags' own selection wins, and startup says so.
func TestStoredHostSandboxBackendFallsBackToStartupSelection(t *testing.T) {
	t.Parallel()
	cfg := Config{ContainerSandboxes: true, ContainerImage: "example/guest", ContainerGitProxyHost: "172.17.0.1"}
	runnable, notes := cfg.RunnableStoredConfig(model.Config{SandboxBackend: "host"})
	if runnable.SandboxBackend != model.SandboxBackendContainer {
		t.Fatalf("runnable backend = %q, want container", runnable.SandboxBackend)
	}
	if len(notes) == 0 {
		t.Fatal("a stored host backend was replaced without a note")
	}
}

// A run host cannot reach a container sandbox (CheckRunHostBackend), so
// a deployment with run hosts is not offered that backend in Settings,
// and one already stored runs on the flags' backend instead -- with a
// note, rather than a daemon that will not start over a setting whose
// screen it would then never serve.
func TestRunHostsAreNotOfferedTheContainerBackend(t *testing.T) {
	t.Parallel()
	cfg := Config{
		GranuleSandboxes:      true,
		ContainerImage:        "example/guest",
		ContainerGitProxyHost: "172.17.0.1",
		GranuleSSHUser:        "root",
		GranuleWorkspace:      "/home/debian",
		GranuleGitProxyHost:   "172.18.0.1",
		RunHostImage:          "example/grain",
	}
	want := []model.SandboxBackend{
		model.SandboxBackendGranule,
		model.SandboxBackendKubernetes,
		model.SandboxBackendKubernetesGranule,
	}
	if got := cfg.availableSandboxBackends(); !reflect.DeepEqual(got, want) {
		t.Fatalf("availableSandboxBackends with run hosts = %v, want %v", got, want)
	}

	runnable, notes := cfg.RunnableStoredConfig(model.Config{SandboxBackend: model.SandboxBackendContainer})
	if runnable.SandboxBackend != model.SandboxBackendGranule {
		t.Fatalf("runnable backend = %q, want granule", runnable.SandboxBackend)
	}
	found := false
	for _, note := range notes {
		found = found || strings.Contains(note, "sandbox backend") && strings.Contains(note, "cannot build")
	}
	if !found {
		t.Fatalf("runnableStoredConfig notes = %v, want the stored container backend explained", notes)
	}
	if started := cfg.WithModelConfig(runnable); started.ContainerSandboxes {
		t.Fatal("a stored container backend was selected for a daemon with run hosts")
	}
}

func TestKubernetesSandboxDefaultsAndModelConfigRoundTrip(t *testing.T) {
	t.Setenv("GRAIN_POD_IP", "")
	cfg := Config{
		K8sSandboxes:          true,
		GranuleGitProxyHost:   "172.18.0.1",
		ContainerGitProxyHost: "172.17.0.1",
	}
	if got := EffectiveK8sWorkspace(cfg); got != model.DefaultK8sWorkspace {
		t.Fatalf("EffectiveK8sWorkspace(k8s) = %q, want %q", got, model.DefaultK8sWorkspace)
	}
	if got := EffectiveK8sSandboxImage(cfg); got != DefaultK8sSandboxImage {
		t.Fatalf("EffectiveK8sSandboxImage = %q, want %q", got, DefaultK8sSandboxImage)
	}
	if got := EffectiveK8sGitProxyHost(cfg); got != "172.18.0.1" {
		t.Fatalf("EffectiveK8sGitProxyHost fallback = %q, want 172.18.0.1", got)
	}

	granuleCfg := Config{
		K8sGranuleSandboxes:   true,
		ContainerGitProxyHost: "172.17.0.1",
	}
	if got := EffectiveK8sWorkspace(granuleCfg); got != model.DefaultK8sGranuleWorkspace {
		t.Fatalf("EffectiveK8sWorkspace(k8s-granule) = %q, want %q", got, model.DefaultK8sGranuleWorkspace)
	}
	if got := EffectiveK8sGranuleImage(granuleCfg); got != DefaultSandboxImage {
		t.Fatalf("EffectiveK8sGranuleImage = %q, want %q", got, DefaultSandboxImage)
	}
	if got := EffectiveK8sGitProxyHost(granuleCfg); got != "127.0.0.1" {
		t.Fatalf("EffectiveK8sGitProxyHost off-cluster k8s-granule = %q, want 127.0.0.1", got)
	}
	if got := SandboxAdvertiseHost(granuleCfg); got != "127.0.0.1" {
		t.Fatalf("SandboxAdvertiseHost off-cluster k8s-granule = %q, want 127.0.0.1", got)
	}

	k8sContainerFallbackCfg := Config{
		K8sSandboxes:          true,
		ContainerGitProxyHost: "172.17.0.1",
	}
	if got := EffectiveK8sGitProxyHost(k8sContainerFallbackCfg); got != "172.17.0.1" {
		t.Fatalf("EffectiveK8sGitProxyHost container fallback = %q, want 172.17.0.1", got)
	}

	full := Config{
		K8sGranuleSandboxes: true,
		K8sNamespace:        "custom-ns",
		K8sKubectl:          "/opt/bin/kubectl",
		K8sImage:            "example/k8s-sandbox:v1",
		K8sGranuleImage:     "example/k8s-granule:v1",
		K8sWorkspace:        "/custom/work",
		K8sGitProxyHost:     "10.20.30.40",
		K8sSandboxService:   "grain-proxy",
		K8sServiceAccount:   "sandbox-sa",
		K8sNodeSelector:     []string{"pool=kvm", "zone=us-central1-a"},
		K8sPrivileged:       true,
		K8sNetworkPolicy:    true,
		K8sDeployment:       "grain-deploy",
		K8sContainer:        "grain-ctr",
	}
	mc := full.ToModelConfig()
	roundTripped := (Config{}).WithModelConfig(mc)
	if !roundTripped.K8sGranuleSandboxes ||
		roundTripped.K8sNamespace != "custom-ns" ||
		roundTripped.K8sKubectl != "/opt/bin/kubectl" ||
		roundTripped.K8sImage != "example/k8s-sandbox:v1" ||
		roundTripped.K8sGranuleImage != "example/k8s-granule:v1" ||
		roundTripped.K8sWorkspace != "/custom/work" ||
		roundTripped.K8sGitProxyHost != "10.20.30.40" ||
		roundTripped.K8sSandboxService != "grain-proxy" ||
		roundTripped.K8sServiceAccount != "sandbox-sa" ||
		!reflect.DeepEqual(roundTripped.K8sNodeSelector, []string{"pool=kvm", "zone=us-central1-a"}) ||
		!roundTripped.K8sPrivileged ||
		!roundTripped.K8sNetworkPolicy ||
		roundTripped.K8sDeployment != "grain-deploy" ||
		roundTripped.K8sContainer != "grain-ctr" {
		t.Fatalf("roundTripped config mismatch: %+v", roundTripped)
	}
}

func TestAvailableSandboxBackendsRetainsStartupNoneAndHostAfterSwitchingToKubernetes(t *testing.T) {
	t.Parallel()
	noneCfg := Config{
		DisableNativeSandboxes: true,
	}
	startedFromNone := noneCfg.WithModelConfig(model.Config{
		SandboxBackend: model.SandboxBackendKubernetes,
	})
	wantNone := []model.SandboxBackend{
		model.SandboxBackendKubernetes,
		model.SandboxBackendKubernetesGranule,
		model.SandboxBackendNone,
	}
	if got := startedFromNone.availableSandboxBackends(); !reflect.DeepEqual(got, wantNone) {
		t.Fatalf("availableSandboxBackends after switching from none to kubernetes = %v, want %v", got, wantNone)
	}

	hostCfg := Config{
		HostSandboxes: true,
		SandboxDir:    "/sandboxes",
	}
	startedFromHost := hostCfg.WithModelConfig(model.Config{
		SandboxBackend: model.SandboxBackendKubernetesGranule,
	})
	wantHost := []model.SandboxBackend{
		model.SandboxBackendKubernetes,
		model.SandboxBackendKubernetesGranule,
		model.SandboxBackendHost,
	}
	if got := startedFromHost.availableSandboxBackends(); !reflect.DeepEqual(got, wantHost) {
		t.Fatalf("availableSandboxBackends after switching from host to kubernetes-granule = %v, want %v", got, wantHost)
	}
}

func TestSandboxBackendOptionsTable(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		cfg  Config
		want []model.SandboxBackendOption
	}{
		{
			name: "minimal config offers kubernetes backends and explains others",
			cfg:  Config{},
			want: []model.SandboxBackendOption{
				{Backend: model.SandboxBackendContainer, Available: false, Reason: "needs a container image and a git proxy host"},
				{Backend: model.SandboxBackendGranule, Available: false, Reason: "needs SSH user/workspace/git proxy host for granule"},
				{Backend: model.SandboxBackendKubernetes, Available: true},
				{Backend: model.SandboxBackendKubernetesGranule, Available: true},
				{Backend: model.SandboxBackendHost, Available: false, Reason: "needs -host-sandboxes at daemon startup"},
				{Backend: model.SandboxBackendNone, Available: false, Reason: "needs -disable-native-sandboxes at daemon startup"},
			},
		},
		{
			name: "container missing only git proxy host and granule missing SSH user and workspace",
			cfg: Config{
				ContainerImage:      "example/guest:latest",
				GranuleGitProxyHost: "172.18.0.1",
				HostSandboxes:       true,
			},
			want: []model.SandboxBackendOption{
				{Backend: model.SandboxBackendContainer, Available: false, Reason: "needs a git proxy host (-container-git-proxy-host)"},
				{Backend: model.SandboxBackendGranule, Available: false, Reason: "needs SSH user/workspace for granule"},
				{Backend: model.SandboxBackendKubernetes, Available: true},
				{Backend: model.SandboxBackendKubernetesGranule, Available: true},
				{Backend: model.SandboxBackendHost, Available: false, Reason: "needs a sandbox directory (-sandbox-dir)"},
				{Backend: model.SandboxBackendNone, Available: false, Reason: "needs -disable-native-sandboxes at daemon startup"},
			},
		},
		{
			name: "container missing only image",
			cfg: Config{
				ContainerGitProxyHost: "172.17.0.1",
			},
			want: []model.SandboxBackendOption{
				{Backend: model.SandboxBackendContainer, Available: false, Reason: "needs a container image (-container-sandbox-image)"},
				{Backend: model.SandboxBackendGranule, Available: false, Reason: "needs SSH user/workspace/git proxy host for granule"},
				{Backend: model.SandboxBackendKubernetes, Available: true},
				{Backend: model.SandboxBackendKubernetesGranule, Available: true},
				{Backend: model.SandboxBackendHost, Available: false, Reason: "needs -host-sandboxes at daemon startup"},
				{Backend: model.SandboxBackendNone, Available: false, Reason: "needs -disable-native-sandboxes at daemon startup"},
			},
		},
		{
			name: "run host image disables container and malformed node selector disables kubernetes backends",
			cfg: Config{
				ContainerImage:         "example/guest:latest",
				ContainerGitProxyHost:  "172.17.0.1",
				RunHostImage:           "example/grain:latest",
				GranuleSSHUser:         "root",
				GranuleWorkspace:       "/home/debian",
				GranuleGitProxyHost:    "172.18.0.1",
				K8sNodeSelector:        []string{"pool=sandboxes", "x"},
				HostSandboxes:          true,
				SandboxDir:             "/var/lib/grain/sandboxes",
				DisableNativeSandboxes: true,
			},
			want: []model.SandboxBackendOption{
				{Backend: model.SandboxBackendContainer, Available: false, Reason: "not available when run hosts (-run-host-image) are enabled"},
				{Backend: model.SandboxBackendGranule, Available: true},
				{Backend: model.SandboxBackendKubernetes, Available: false, Reason: "node selector entry 'x' is not key=value"},
				{Backend: model.SandboxBackendKubernetesGranule, Available: false, Reason: "node selector entry 'x' is not key=value"},
				{Backend: model.SandboxBackendHost, Available: true},
				{Backend: model.SandboxBackendNone, Available: true},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.cfg.SandboxBackendOptions()
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("SandboxBackendOptions() = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestKubernetesGranuleIsOfferedAndRunnableInImageMode(t *testing.T) {
	t.Parallel()
	cfg := Config{
		ContainerSandboxes:    true,
		ContainerImage:        "ghcr.io/bwsalmon/grain/guest:main",
		ContainerGitProxyHost: "172.17.0.1",
		GranuleSSHUser:        "debian",
		GranuleWorkspace:      "/home/debian",
		GranuleGitProxyHost:   "172.18.0.1",
		UpgradeImage:          "ghcr.io/bwsalmon/grain/grain:main",
		UpgradeImageRefFile:   "/var/lib/grain/data/image-ref",
	}

	wantAvailable := []model.SandboxBackend{
		model.SandboxBackendContainer,
		model.SandboxBackendGranule,
		model.SandboxBackendKubernetes,
		model.SandboxBackendKubernetesGranule,
	}
	if got := cfg.availableSandboxBackends(); !reflect.DeepEqual(got, wantAvailable) {
		t.Fatalf("availableSandboxBackends in image mode = %v, want %v", got, wantAvailable)
	}

	var foundK8sGranule bool
	for _, opt := range cfg.SandboxBackendOptions() {
		if opt.Backend == model.SandboxBackendKubernetesGranule {
			foundK8sGranule = true
			if !opt.Available || opt.Reason != "" {
				t.Fatalf("kubernetes-granule option in image mode = %+v, want available with empty reason", opt)
			}
		}
	}
	if !foundK8sGranule {
		t.Fatal("kubernetes-granule missing from SandboxBackendOptions()")
	}

	runnable, notes := cfg.RunnableStoredConfig(model.Config{
		SandboxBackend: model.SandboxBackendKubernetesGranule,
	})
	for _, note := range notes {
		if strings.Contains(note, "sandbox backend") {
			t.Fatalf("RunnableStoredConfig(kubernetes-granule) in image mode rejected backend: %v", notes)
		}
	}
	if runnable.SandboxBackend != model.SandboxBackendKubernetesGranule {
		t.Fatalf("runnable.SandboxBackend = %q, want %q", runnable.SandboxBackend, model.SandboxBackendKubernetesGranule)
	}
	started := cfg.WithModelConfig(runnable)
	if !started.K8sGranuleSandboxes || started.ContainerSandboxes {
		t.Fatalf("WithModelConfig(kubernetes-granule) = %+v, want K8sGranuleSandboxes=true and ContainerSandboxes=false", started)
	}
	if got := deploymentMode(started); got != "image" {
		t.Fatalf("deploymentMode(started) = %q, want image", got)
	}
}

func TestKubernetesModeVsKubernetesSandboxesPredicateSplit(t *testing.T) {
	t.Setenv(PodNameEnv, "")
	t.Setenv(PodIPEnv, "")
	t.Setenv("KUBERNETES_SERVICE_HOST", "")

	cfg := Config{
		K8sGranuleSandboxes: true,
		UpgradeImage:        "ghcr.io/bwsalmon/grain/grain",
	}
	if !KubernetesSandboxes(cfg) {
		t.Fatal("KubernetesSandboxes(cfg) = false for off-cluster K8sGranuleSandboxes, want true")
	}
	if KubernetesMode(cfg) {
		t.Fatal("KubernetesMode(cfg) = true for off-cluster K8sGranuleSandboxes, want false")
	}
	if got := deploymentMode(cfg); got != "image" {
		t.Fatalf("deploymentMode(off-cluster K8sGranuleSandboxes with UpgradeImage) = %q, want image", got)
	}
	if got := BudgetSentence(cfg.Limits(), KubernetesSandboxes(cfg)); !strings.Contains(got, "cluster scheduler") {
		t.Fatalf("BudgetSentence(off-cluster K8sGranuleSandboxes) = %q, want cluster scheduler message", got)
	}

	t.Setenv(PodNameEnv, "grain-0")
	if !InKubernetesPod() {
		t.Fatal("InKubernetesPod() = false with GRAIN_POD_NAME set, want true")
	}
	if !KubernetesMode(cfg) {
		t.Fatal("KubernetesMode(cfg) = false for in-pod K8sGranuleSandboxes, want true")
	}
}

func TestSandboxBackendAvailableWithUpgradeImageOffClusterVsInPod(t *testing.T) {
	t.Setenv(PodNameEnv, "")
	t.Setenv(PodIPEnv, "")
	t.Setenv("KUBERNETES_SERVICE_HOST", "")

	cfg := Config{
		UpgradeImage: "ghcr.io/bwsalmon/grain/grain",
	}
	if !cfg.sandboxBackendAvailable(model.SandboxBackendKubernetesGranule) {
		t.Fatal("sandboxBackendAvailable(kubernetes-granule) = false off-cluster with UpgradeImage set, want true")
	}

	t.Setenv(PodNameEnv, "grain-0")
	if cfg.sandboxBackendAvailable(model.SandboxBackendKubernetesGranule) {
		t.Fatal("sandboxBackendAvailable(kubernetes-granule) = true in-pod with UpgradeImage and no UpgradeImageRefFile, want false")
	}
	cfg.UpgradeImageRefFile = "/var/lib/grain/data/image-ref"
	if !cfg.sandboxBackendAvailable(model.SandboxBackendKubernetesGranule) {
		t.Fatal("sandboxBackendAvailable(kubernetes-granule) = false when UpgradeImageRefFile is set, want true")
	}
}

func TestEffectiveK8sKubeconfigDiscovery(t *testing.T) {
	t.Setenv("KUBECONFIG", "")

	dir := t.TempDir()
	kubeconfigInDataDir := filepath.Join(dir, "kubeconfig")
	if err := os.WriteFile(kubeconfigInDataDir, []byte("apiVersion: v1\n"), 0o600); err != nil {
		t.Fatalf("WriteFile kubeconfig: %v", err)
	}
	if got := EffectiveK8sKubeconfig(Config{DataDir: dir}); got != kubeconfigInDataDir {
		t.Fatalf("EffectiveK8sKubeconfig(DataDir with kubeconfig) = %q, want %q", got, kubeconfigInDataDir)
	}

	// Explicit K8sKubeconfig takes precedence.
	if got := EffectiveK8sKubeconfig(Config{DataDir: dir, K8sKubeconfig: "/custom/kubeconfig"}); got != "/custom/kubeconfig" {
		t.Fatalf("EffectiveK8sKubeconfig(explicit) = %q, want /custom/kubeconfig", got)
	}

	// When KUBECONFIG is already set in the environment, EffectiveK8sKubeconfig returns "" so kubectl uses the env var.
	t.Setenv("KUBECONFIG", "/env/kubeconfig")
	if got := EffectiveK8sKubeconfig(Config{DataDir: dir}); got != "" {
		t.Fatalf("EffectiveK8sKubeconfig with KUBECONFIG env set = %q, want empty", got)
	}
	t.Setenv("KUBECONFIG", "")

	// Standard container install layout: /var/lib/grain/kubeconfig with DataDir=/var/lib/grain/data.
	rootDir := t.TempDir()
	fakeDefaultKubeconfig := filepath.Join(rootDir, "kubeconfig")
	if err := os.WriteFile(fakeDefaultKubeconfig, []byte("apiVersion: v1\n"), 0o600); err != nil {
		t.Fatalf("WriteFile fakeDefaultKubeconfig: %v", err)
	}
	prevDefault := defaultKubeconfigPath
	defaultKubeconfigPath = fakeDefaultKubeconfig
	t.Cleanup(func() { defaultKubeconfigPath = prevDefault })

	nestedDataDir := filepath.Join(rootDir, "data")
	if err := os.MkdirAll(nestedDataDir, 0o755); err != nil {
		t.Fatalf("MkdirAll nestedDataDir: %v", err)
	}
	if got := EffectiveK8sKubeconfig(Config{DataDir: nestedDataDir}); got != fakeDefaultKubeconfig {
		t.Fatalf("EffectiveK8sKubeconfig(parent kubeconfig) = %q, want %q", got, fakeDefaultKubeconfig)
	}
}

func TestSandboxAdvertiseHostAndEndpointPortsForOffClusterK8sGranule(t *testing.T) {
	t.Setenv(PodIPEnv, "")
	cfg := Config{
		K8sGranuleSandboxes:   true,
		GranuleGitProxyHost:   "172.18.0.1",
		ContainerGitProxyHost: "172.17.0.1",
	}
	if got := SandboxAdvertiseHost(cfg); got != "127.0.0.1" {
		t.Fatalf("SandboxAdvertiseHost(off-cluster K8sGranuleSandboxes) = %q, want 127.0.0.1", got)
	}
	fabric := NewDirect(SandboxAdvertiseHost(cfg))
	ep, stop, err := fabric.Serve("git proxy", http.NewServeMux())
	if err != nil {
		t.Fatalf("fabric.Serve: %v", err)
	}
	defer stop(context.Background())
	if !strings.HasPrefix(ep.URL, "http://127.0.0.1:") {
		t.Fatalf("ep.URL = %q, want http://127.0.0.1:<port>", ep.URL)
	}
	var ports SandboxServicePorts
	ports.Serve(fabric)
	gotPorts := ports.EndpointPorts()
	if len(gotPorts) != 1 || gotPorts[0] <= 0 {
		t.Fatalf("ports.EndpointPorts() = %v, want 1 positive port", gotPorts)
	}
}
