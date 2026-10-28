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
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/agent"
	"github.com/ai-on-gke/grain/pkg/k8s"
)

// TestKubeRunHostSurvivesADaemonRoll is the Kubernetes half of the
// acceptance test's run-host side, against a real cluster: a stand-in
// daemon runs as a one-replica, Recreate Deployment with a ReadWriteOnce
// claim, exactly the shape of deploy/kubernetes/grain.yaml, and starts a
// run in a run pod pinned to its node. The Deployment is then rolled
// (`kubectl rollout restart`) mid-run. The stopping daemon detaches, the
// run pod keeps running and writing its spool on the shared claim with
// no daemon at all, and the replacement daemon pod lists it, decides to
// adopt it, and tails it to the end from the persisted cursor: one run
// pod, one transcript with no hole and no duplicate, and nothing left
// behind.
//
// The stand-in daemon is this test binary (TestKubeRunHostDaemonRole)
// in an image with grain and kubectl, loaded into a kind cluster. It
// needs docker, kind and a cluster, so it runs only with
// GRAIN_RUNHOST_KIND=<kind cluster name>; docs/test-runs/
// 2026-10-13-k8s-run-host-roll.md is a run of it.
func TestKubeRunHostSurvivesADaemonRoll(t *testing.T) {
	cluster := os.Getenv("GRAIN_RUNHOST_KIND")
	if cluster == "" {
		t.Skip("set GRAIN_RUNHOST_KIND=<kind cluster> to run against a real cluster")
	}
	ctx := context.Background()
	dir := t.TempDir()
	for _, b := range [][]string{
		{"go", "build", "-o", filepath.Join(dir, "grain"), "github.com/ai-on-gke/grain/cmd/grain"},
		{"go", "test", "-c", "-o", filepath.Join(dir, "runhost.test"), "github.com/ai-on-gke/grain/pkg/runhost"},
	} {
		cmd := exec.Command(b[0], b[1:]...)
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %v\n%s", b, err, out)
		}
	}
	kubectl, err := exec.LookPath("kubectl")
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(kubectl)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "kubectl"), data, 0o755); err != nil {
		t.Fatal(err)
	}
	dockerfile := "FROM debian:bookworm-slim\nCOPY grain runhost.test kubectl /usr/local/bin/\n"
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte(dockerfile), 0o644); err != nil {
		t.Fatal(err)
	}
	stamp := time.Now().Unix()
	image := fmt.Sprintf("grain-runhost-test:%d", stamp)
	ns := fmt.Sprintf("grain-rh-%d", stamp)
	run := func(stdin string, args ...string) string {
		t.Helper()
		cmd := exec.Command(args[0], args[1:]...)
		if stdin != "" {
			cmd.Stdin = strings.NewReader(stdin)
		}
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return string(out)
	}
	run("", "docker", "build", "-q", "-t", image, dir)
	run("", "kind", "load", "docker-image", "--name", cluster, image)
	run(kubeTestManifest(ns, image), "kubectl", "apply", "-f", "-")
	t.Cleanup(func() { _ = exec.Command("kubectl", "delete", "namespace", ns, "--wait=false").Run() })

	client := k8s.Client{Namespace: ns}
	// daemonLogs returns the logs of the daemon pods that are running, or
	// were, keyed by pod name.
	daemonLogs := func() map[string]string {
		pods, err := client.ListPods(ctx, "app.kubernetes.io/component="+KubeDaemonComponent)
		if err != nil {
			t.Fatal(err)
		}
		logs := map[string]string{}
		for _, p := range pods {
			l, _ := client.Logs(ctx, p.Name, "daemon")
			logs[p.Name] = l
		}
		return logs
	}
	waitFor := func(what string, within time.Duration, ok func(map[string]string) bool) map[string]string {
		t.Helper()
		deadline := time.Now().Add(within)
		for {
			logs := daemonLogs()
			if ok(logs) {
				return logs
			}
			for _, l := range logs {
				if strings.Contains(l, "--- FAIL") {
					t.Fatalf("a daemon pod failed while waiting for %s: %v", what, logs)
				}
			}
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %s; daemon logs: %v", what, logs)
			}
			time.Sleep(2 * time.Second)
		}
	}
	var first string
	waitFor("the first daemon to start a run", 4*time.Minute, func(logs map[string]string) bool {
		for name, l := range logs {
			if strings.Contains(l, "RUN-STARTED") && strings.Contains(l, "line 5\n") {
				first = name
				return true
			}
		}
		return false
	})
	runPods, err := client.ListPods(ctx, k8s.SandboxLabel+"="+KubeComponent)
	if err != nil || len(runPods) != 1 {
		t.Fatalf("run pods before the roll = %+v (%v), want one", runPods, err)
	}
	runPod := runPods[0]
	t.Logf("daemon pod %s started run pod %s on node %s; rolling the daemon", first, runPod.Name, runPod.Node)

	run("", "kubectl", "-n", ns, "rollout", "restart", "deployment/grain")
	logs := waitFor("the next daemon to adopt the run", 4*time.Minute, func(logs map[string]string) bool {
		for name, l := range logs {
			if name != first && (strings.Contains(l, "ADOPTED-OK") || strings.Contains(l, "ADOPT-FAIL")) {
				return true
			}
		}
		return false
	})
	for name, l := range logs {
		if name == first {
			continue
		}
		t.Logf("second daemon pod %s:\n%s", name, l)
		if !strings.Contains(l, "ADOPTED-OK") {
			t.Fatalf("the second daemon did not adopt the run cleanly")
		}
		if !strings.Contains(l, "ADOPTING "+runPod.Name+" ") {
			t.Fatalf("the second daemon adopted something other than run pod %s", runPod.Name)
		}
	}
}

// kubeTestManifest is the stand-in daemon's namespace: grain.yaml's
// shape, cut down to what the run-host path uses.
func kubeTestManifest(ns, image string) string {
	return strings.NewReplacer("{{NS}}", ns, "{{IMAGE}}", image).Replace(`
apiVersion: v1
kind: Namespace
metadata: {name: {{NS}}}
---
apiVersion: v1
kind: ServiceAccount
metadata: {name: grain, namespace: {{NS}}}
---
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata: {name: grain, namespace: {{NS}}}
rules:
  - apiGroups: [""]
    resources: ["pods"]
    verbs: ["get", "list", "watch", "create", "delete"]
  - apiGroups: [""]
    resources: ["persistentvolumeclaims"]
    verbs: ["get"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata: {name: grain, namespace: {{NS}}}
roleRef: {apiGroup: rbac.authorization.k8s.io, kind: Role, name: grain}
subjects: [{kind: ServiceAccount, name: grain, namespace: {{NS}}}]
---
apiVersion: v1
kind: PersistentVolumeClaim
metadata: {name: grain-data, namespace: {{NS}}}
spec:
  accessModes: ["ReadWriteOnce"]
  resources: {requests: {storage: 1Gi}}
---
apiVersion: apps/v1
kind: Deployment
metadata: {name: grain, namespace: {{NS}}}
spec:
  replicas: 1
  strategy: {type: Recreate}
  selector: {matchLabels: {app.kubernetes.io/name: grain}}
  template:
    metadata:
      labels: {app.kubernetes.io/name: grain, app.kubernetes.io/component: daemon}
    spec:
      serviceAccountName: grain
      securityContext: {runAsNonRoot: true, runAsUser: 1000, runAsGroup: 1000, fsGroup: 1000}
      terminationGracePeriodSeconds: 300
      affinity:
        podAffinity:
          preferredDuringSchedulingIgnoredDuringExecution:
            - weight: 100
              podAffinityTerm:
                labelSelector: {matchLabels: {app.kubernetes.io/component: runhost}}
                topologyKey: kubernetes.io/hostname
      containers:
        - name: daemon
          image: {{IMAGE}}
          imagePullPolicy: Never
          command: ["/usr/local/bin/runhost.test", "-test.run", "^TestKubeRunHostDaemonRole$", "-test.v", "-test.timeout", "30m"]
          env:
            - {name: GRAIN_RUNHOST_KUBE_ROLE, value: daemon}
            - {name: GRAIN_RUNHOST_KUBE_NS, value: {{NS}}}
            - {name: GRAIN_RUNHOST_KUBE_IMAGE, value: {{IMAGE}}}
            - {name: HOME, value: /tmp}
          volumeMounts: [{name: data, mountPath: /data}]
      volumes:
        - name: data
          persistentVolumeClaim: {claimName: grain-data}
`)
}

// kubeTestCursor is the stand-in daemon's "store": the spool cursor and
// when it was last advanced, on the claim, written by rename.
type kubeTestCursor struct {
	Cursor int64     `json:"cursor"`
	At     time.Time `json:"at"`
}

const kubeTestLines = 120

// TestKubeRunHostDaemonRole is the stand-in daemon, run inside the
// cluster by TestKubeRunHostSurvivesADaemonRoll. The first pod starts the
// run and detaches when the rollout stops it; the next adopts it.
func TestKubeRunHostDaemonRole(t *testing.T) {
	if os.Getenv("GRAIN_RUNHOST_KUBE_ROLE") != "daemon" {
		t.Skip("the in-cluster half of TestKubeRunHostSurvivesADaemonRoll")
	}
	const data, deployment = "/data", "kubetest"
	cursorFile, teeFile := filepath.Join(data, "cursor.json"), filepath.Join(data, "first.tee")
	host := &Host{
		LocalHost: agent.LocalHost{Logf: t.Logf},
		Driver: Kubernetes{
			Client:         k8s.Client{Namespace: os.Getenv("GRAIN_RUNHOST_KUBE_NS")},
			Volumes:        []KubeVolume{{Claim: "grain-data", MountPath: data}},
			ServiceAccount: "grain",
			DaemonSelector: map[string]string{"app.kubernetes.io/name": "grain", "app.kubernetes.io/component": KubeDaemonComponent},
			Security:       k8s.PodSecurity{Pod: k8s.SecurityContext{"runAsUser": os.Getuid(), "runAsGroup": os.Getgid()}},
		},
		Deployment:  deployment,
		Generation:  "test",
		Image:       os.Getenv("GRAIN_RUNHOST_KUBE_IMAGE"),
		Grain:       "/usr/local/bin/grain",
		SpoolRoot:   filepath.Join(data, "runhosts", "spool"),
		StagingRoot: filepath.Join(data, "runhosts", "staging"),
		Poll:        100 * time.Millisecond,
		CursorEvery: time.Nanosecond,
		OnCursor: func(_ context.Context, _ string, c int64) {
			b, _ := json.Marshal(kubeTestCursor{Cursor: c, At: time.Now()})
			_ = os.WriteFile(cursorFile+".tmp", b, 0o600)
			_ = os.Rename(cursorFile+".tmp", cursorFile)
		},
		Detach: func(context.Context) bool { return true },
	}
	raw, err := os.ReadFile(cursorFile)
	if errors.Is(err, os.ErrNotExist) {
		// The first daemon: start the run, and stop when the rollout
		// says so.
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM)
		defer stop()
		ctx = WithRun(ctx, Run{Task: "7", Run: "r1", Deadline: time.Now().Add(20 * time.Minute)})
		tee, err := os.Create(teeFile)
		if err != nil {
			t.Fatal(err)
		}
		defer tee.Close()
		fmt.Println("RUN-STARTED")
		script := fmt.Sprintf(`i=1; while [ $i -le %d ]; do echo "line $i"; i=$((i+1)); sleep 0.25; done`, kubeTestLines)
		_, err = host.Run(ctx, agent.Command{Path: "/bin/sh", Args: []string{"-c", script}}, io.MultiWriter(&syncFile{tee}, os.Stdout))
		fmt.Printf("first daemon: Run = %v\n", err)
		if err != ErrDetached {
			t.Fatalf("the stopped daemon's Run = %v, want ErrDetached", err)
		}
		fmt.Println("DETACHED")
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	var c kubeTestCursor
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatal(err)
	}
	fail := func(format string, args ...any) {
		fmt.Printf("ADOPT-FAIL: "+format+"\n", args...)
		select {} // a Deployment restarts a container that exits
	}
	ctx := context.Background()
	infos, err := host.Driver.List(ctx, deployment)
	if err != nil {
		fail("listing: %v", err)
	}
	plan := Decide([]Claim{{Run: "r1", Task: "7", LastSeen: c.At}}, infos,
		func(i Info) SpoolState { e, _ := ReadExit(i.Labels.Spool); return SpoolState{Exited: e != nil} }, time.Now())
	if len(plan.Adopt) != 1 {
		fail("plan = %+v, want one adoption", plan)
	}
	a := plan.Adopt[0]
	fmt.Printf("ADOPTING %s at cursor %d after a %s gap\n", a.Host.Name, c.Cursor, a.Gap.Round(time.Millisecond))
	var second bytes.Buffer
	out, err := host.Attach(ctx, a.Host, c.Cursor, &second)
	if err != nil {
		fail("attach: %v", err)
	}
	firstTee, _ := os.ReadFile(teeFile)
	if string(firstTee)+second.String() != out {
		fail("transcript has a hole or a duplicate: first %d bytes, second %d bytes, whole %d bytes",
			len(firstTee), second.Len(), len(out))
	}
	if strings.Count(out, "\n") != kubeTestLines {
		fail("the run did not finish across the roll: %q", out)
	}
	// Destroy does not wait for the pod to leave; it is listed, not
	// running, while it terminates.
	for gone := time.Now().Add(time.Minute); ; time.Sleep(time.Second) {
		left, _ := host.Driver.List(ctx, deployment)
		if len(left) == 0 {
			break
		}
		if left[0].Running || time.Now().After(gone) {
			fail("the finished run pod was left behind: %+v", left)
		}
	}
	fmt.Printf("ADOPTED-OK: %d lines, %d read by the first daemon and %d by this one\n",
		kubeTestLines, strings.Count(string(firstTee), "\n"), strings.Count(second.String(), "\n"))
	select {}
}

// syncFile flushes each write straight to the file, so the next daemon
// reads exactly what this one had tee'd when it persisted its cursor.
type syncFile struct{ f *os.File }

func (s *syncFile) Write(p []byte) (int, error) {
	n, err := s.f.Write(p)
	if err == nil {
		err = s.f.Sync()
	}
	return n, err
}
