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

package deploy

// A Kubernetes deployment has no host and no grain hostd: every agent
// CLI runs as a subprocess in the daemon pod's own container
// (design/design/cli-run-location.design.md, "Deployments with no host
// executor"). Both manifests are held to starting a daemon that agrees:
// no run location is asked for, so each CLI takes its default, which
// resolves to the pod's own container -- and nothing looks for hostd's
// socket to decide it.

import (
	"context"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/daemon"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/whererun"
)

func TestTheKubernetesDaemonsStartWithDefaultRunLocationsAndNoHostd(t *testing.T) {
	for file, dep := range map[string]k8sDeployment{
		"grain.yaml":         grainDeployment(t),
		"grain-granule.yaml": grainGranuleDeployment(t),
	} {
		t.Run(file, func(t *testing.T) {
			c := grainContainer(t, dep)
			flags := daemonArgs(t, c)
			for _, name := range []string{"claude-run", "codex-run", "agy-run", "hostd-socket"} {
				if v, ok := flags[name]; ok {
					t.Errorf("%s passes -%s=%s; a Kubernetes daemon has no host for it to name", file, name, v)
				}
			}
			for _, env := range c.Env {
				if strings.HasPrefix(env.Name, "GRAIN_") && strings.HasSuffix(env.Name, "_RUN") {
					t.Errorf("%s sets %s; a Kubernetes daemon has no host for it to name", file, env.Name)
				}
			}

			_, k8s := flags["k8s-sandboxes"]
			_, granule := flags["k8s-granule-sandboxes"]
			t.Setenv(daemon.PodNameEnv, "grain-0")
			cfg := daemon.Config{K8sSandboxes: k8s, K8sGranuleSandboxes: granule}
			if !daemon.KubernetesMode(cfg) {
				t.Fatalf("%s passes neither -k8s-sandboxes nor -k8s-granule-sandboxes", file)
			}
			// What the daemon command does at start (pkg/cli's
			// runDaemonCommand), with a socket lookup that fails the test.
			exec := daemon.DetectHostExecutor(cfg, whererun.Detection{Host: false, Signal: "/proc/1/cgroup names kubepods"},
				func(p string) bool {
					t.Errorf("the daemon looked for grain hostd at %s on Kubernetes", p)
					return false
				})
			cfg.HostExec = &exec
			if exec.Kind != daemon.HostExecutorNone || exec.HostAvailable() {
				t.Errorf("the host executor is %+v, want none", exec)
			}
			if err := daemon.CheckHostExecutorFlags(cfg); err != nil {
				t.Errorf("refused at start: %v", err)
			}
			if err := daemon.CheckCLIRunFlags(cfg); err != nil {
				t.Errorf("refused at start: %v", err)
			}
			for _, r := range cfg.ResolveCLIRuns(context.Background()) {
				if r.Source != "default" || r.Location != model.CLIRunContainer || r.Via != daemon.ViaDaemon || r.Err != "" {
					t.Errorf("%s resolves to %+v, want its default, in the pod's own container", r.Framework, r)
				}
			}
		})
	}
}
