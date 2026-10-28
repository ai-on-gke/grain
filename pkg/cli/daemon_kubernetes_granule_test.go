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

// docs/k8s-granule-design.md's own backend: -k8s-granule-sandboxes,
// alongside -k8s-sandboxes rather than a flag on it, because the two
// platforms' pods disagree about whether privileged is allowed at all.
// These mirror daemon_kubernetes_test.go's own coverage of the flags
// this reuses (kubernetesMode, checkConfig's refusals, which backend
// run() would build) rather than repeating what that file already
// proves about the fields the two share.
package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/daemon"
)

// kubeGranuleConfig is a config that would start: everything
// -k8s-granule-sandboxes requires, plus the fields checkConfig demands
// of any deployment.
func kubeGranuleConfig() daemon.Config {
	return daemon.Config{
		DataDir:             "/data",
		K8sGranuleSandboxes: true,
		K8sNamespace:        "grain",
		K8sGranuleImage:     "example/granule:latest",
		K8sWorkspace:        "/home/debian",
		K8sGitProxyHost:     "10.1.2.3",
		MaxWorkers:          1,
		PollInterval:        30 * time.Second,
		AgentFramework:      "claude",
	}
}

func TestKubernetesGranuleConfigNeedsNoSandboxDir(t *testing.T) {
	t.Parallel()
	if err := checkConfig(kubeGranuleConfig()); err != nil {
		t.Fatalf("checkConfig on a Kubernetes granule deployment: %v", err)
	}
}

func TestKubernetesGranuleConfigRefusesWhatItCannotRunWithout(t *testing.T) {
	t.Parallel()
	for name, mangle := range map[string]func(*daemon.Config){
		"no image":             func(c *daemon.Config) { c.K8sGranuleImage = "" },
		"no workspace":         func(c *daemon.Config) { c.K8sWorkspace = "" },
		"no git proxy host":    func(c *daemon.Config) { c.K8sGitProxyHost = "" },
		"a bad node selector":  func(c *daemon.Config) { c.K8sNodeSelector = []string{"nodepool"} },
		"two sandbox backends": func(c *daemon.Config) { c.GranuleSandboxes = true },
	} {
		cfg := kubeGranuleConfig()
		mangle(&cfg)
		if err := checkConfig(cfg); err == nil {
			t.Errorf("checkConfig accepted a deployment with %s", name)
		}
	}
}

// TestKubernetesGranuleRefusesUpgradeImage is a deliberate gap, not an
// oversight: this backend's self-upgrade would need a privileged,
// /dev/kvm-mounted health-check pod rather than the plain one
// -k8s-sandboxes checks a candidate image with, and nothing here builds
// that yet. Refusing loudly at startup is better than accepting the
// flag and failing the first upgrade attempt.
func TestKubernetesGranuleRefusesUpgradeImage(t *testing.T) {
	t.Parallel()
	cfg := kubeGranuleConfig()
	cfg.UpgradeImage = "ghcr.io/bwsalmon/grain/grain"
	err := checkConfig(cfg)
	if err == nil {
		t.Fatal("checkConfig accepted -upgrade-image with -k8s-granule-sandboxes")
	}
	if !strings.Contains(err.Error(), "k8s-granule-sandboxes") {
		t.Errorf("error = %q, want it to name the flag", err)
	}
}

func TestKubernetesGranuleAndK8sSandboxesAreMutuallyExclusive(t *testing.T) {
	t.Parallel()
	cfg := kubeGranuleConfig()
	cfg.K8sSandboxes = true
	cfg.K8sImage = "example/sandbox"
	err := checkConfig(cfg)
	if err == nil {
		t.Fatal("checkConfig accepted both -k8s-sandboxes and -k8s-granule-sandboxes")
	}
	for _, want := range []string{"-k8s-sandboxes", "-k8s-granule-sandboxes"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to name %s", err, want)
		}
	}
}

// TestKubernetesGranuleModeWithholdsEveryHostFacingControl is
// TestKubernetesModeWithholdsEveryHostFacingControl's counterpart: when
// the daemon itself runs inside a Kubernetes pod, a granule pod is no
// more a host this deployment owns than a plain one is.
func TestKubernetesGranuleModeWithholdsEveryHostFacingControl(t *testing.T) {
	t.Setenv(daemon.PodNameEnv, "grain-0")
	cfg := kubeGranuleConfig()
	if !daemon.KubernetesMode(cfg) {
		t.Fatal("kubernetesMode is false for an in-pod -k8s-granule-sandboxes deployment")
	}
	if daemon.HostStatsOrNil(cfg, nil, nil) != nil {
		t.Error("HostStats is wired under in-pod Kubernetes granule sandboxing")
	}
	if daemon.HostTopOrNil(cfg) != nil {
		t.Error("HostTop is wired under in-pod Kubernetes granule sandboxing")
	}
}

func TestKubernetesGranuleOffClusterKeepsHostFacingControls(t *testing.T) {
	t.Setenv(daemon.PodNameEnv, "")
	t.Setenv(daemon.PodIPEnv, "")
	t.Setenv("KUBERNETES_SERVICE_HOST", "")
	cfg := kubeGranuleConfig()
	if daemon.KubernetesMode(cfg) {
		t.Fatal("KubernetesMode is true for an off-cluster -k8s-granule-sandboxes deployment")
	}
	if !daemon.KubernetesSandboxes(cfg) {
		t.Fatal("KubernetesSandboxes is false for an off-cluster -k8s-granule-sandboxes deployment")
	}
	if daemon.HostStatsOrNil(cfg, nil, nil) == nil {
		t.Error("HostStats is withheld on an off-cluster -k8s-granule-sandboxes deployment")
	}
	if daemon.HostTopOrNil(cfg) == nil {
		t.Error("HostTop is withheld on an off-cluster -k8s-granule-sandboxes deployment")
	}
}

func TestKubernetesGranuleSandboxAdvertiseHostIsTheGitProxyHost(t *testing.T) {
	t.Parallel()
	cfg := kubeGranuleConfig()
	if got := daemon.SandboxAdvertiseHost(cfg); got != cfg.K8sGitProxyHost {
		t.Errorf("sandboxAdvertiseHost = %q, want %q", got, cfg.K8sGitProxyHost)
	}
}

func TestKubernetesGranuleImageFlagDefaultsToDefaultSandboxImage(t *testing.T) {
	t.Parallel()
	cfg, _, _, err := parseDaemonArgs([]string{"-data-dir=/data"})
	if err != nil {
		t.Fatalf("parseDaemonArgs: %v", err)
	}
	if cfg.K8sGranuleImage != daemon.DefaultSandboxImage {
		t.Fatalf("K8sGranuleImage = %q, want %q", cfg.K8sGranuleImage, daemon.DefaultSandboxImage)
	}
}

func TestKubernetesGranuleAcceptsUpgradeImageWithRefFile(t *testing.T) {
	t.Parallel()
	cfg := kubeGranuleConfig()
	cfg.UpgradeImage = "ghcr.io/bwsalmon/grain/google-grain"
	cfg.UpgradeImageRefFile = "/var/lib/grain/data/image-ref"
	if err := checkConfig(cfg); err != nil {
		t.Fatalf("checkConfig with -k8s-granule-sandboxes and -upgrade-image-ref-file: %v", err)
	}
}
