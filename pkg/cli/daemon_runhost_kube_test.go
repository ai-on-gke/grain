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

package cli

// TestDaemonRollAdoptsTheRunAndItOpensItsPullRequest is
// TestDaemonRestartAdoptsTheRunAndItOpensItsPullRequest on Kubernetes:
// the same acceptance test of design/design/non-disruptive-upgrade.design.md, against the
// real `grain daemon` running as deploy/kubernetes/grain.yaml's shape of
// Deployment (one replica, Recreate, a ReadWriteOnce claim) in a kind
// cluster, with -k8s-sandboxes and -run-host-image=self:
//
//	With an agent run in flight, roll the Deployment. The same run
//	finishes and opens its pull request; its transcript has no hole; its
//	deadline is not reset.
//
// The first daemon pod dispatches the run: a sandbox pod
// (orchestrator.K8sSandboxes) and a run pod (runhost.Kubernetes) whose
// agent is the stub claude of restartStubClaude, put on the claim's
// credential directory by an init container. The Deployment is then
// rolled while the stub waits. The stopping pod lets the run go, the run
// pod and the sandbox pod stay up, and the next daemon pod claims the
// sandbox pod back (K8sSandboxes.Readopt, before the startup reap),
// adopts the run pod and carries the run to its end
// (orchestrator.ResumeAdoptedRun), which opens the pull request.
//
// How the Deployment is rolled depends on whether the cluster has a
// registry, because an upgrade pins the Deployment to the digest its
// health-check pod reports and an image `kind load` put on a node has
// none:
//
//   - GRAIN_RUNHOST_KIND_REGISTRY=localhost:5001 (kind's local-registry
//     recipe; docs/test-runs/2026-10-15-k8s-daemon-roll-adoption.md has
//     the commands): two builds of the image are pushed there, and the
//     roll is an Upgrade to the second through POST /api/upgrade, so
//     upgrade.Upgrader.stageKube flushes the run's tail before it
//     repoints the Deployment.
//   - Unset: the image is loaded with `kind load` and the roll is
//     `kubectl rollout restart`, which leaves the detach to the pod's
//     termination (runhost.Host.Detach).
//
// The GitHub the daemon talks to is githubsim and a `git http-backend`,
// served by this process on the kind network's gateway. The roles the
// image runs this test binary in (seeding the claim, reading the store,
// asking for the upgrade) are TestDaemonRollAdoptionRole.
//
// The task is durable (design/design/durable-tasks.design.md#DT-1), so its run
// has no deadline to carry across the roll.
// TestDaemonRollResumesADurableRunItDidNotAdopt is the same roll with no
// run host, where the run is suspended and resumed instead. Both passed
// on kind with `kubectl rollout restart` for DT-S3. On the Upgrade path
// the health check failed before the roll there (the design doc's "What
// the kind runs found").
//
// It needs docker, kind and a cluster, so it runs only with
// GRAIN_RUNHOST_KIND=<kind cluster name>, as
// pkg/runhost's TestKubeRunHostSurvivesADaemonRoll does; grain CI cannot
// create a kind cluster (.grain/workflows/tests.yml).

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/daemon"
	"github.com/ai-on-gke/grain/pkg/github/githubsim"
	"github.com/ai-on-gke/grain/pkg/k8s"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/runhost"
)

const (
	kubeAdoptTask    = "runhost-kube-roll"
	kubeAdoptOwner   = "acme"
	kubeAdoptRepo    = "widgets"
	kubeAdoptData    = "/data"
	kubeAdoptCredDir = "/data/agent-home"
	// kubeAdoptBranch is the upgrade's target: one of
	// upgrade.DefaultAllowedBranches, so the daemon needs no
	// -upgrade-allowed-branches.
	kubeAdoptBranch = "main"
	// kubeAdoptState prefixes the inspect role's one line of output.
	kubeAdoptState = "KUBE-ADOPT-STATE "
	// kubeAdoptGitHub is where the daemon pod's forward sidecar listens:
	// the git proxy forwards over plain HTTP only to a loopback address
	// (a local mock), and the mock is on this machine, not in the pod.
	kubeAdoptGitHub = "127.0.0.1:7780"
)

// kubeAdoptStore is what the inspect role reads out of the daemon's store.
type kubeAdoptStore struct {
	// Deployment is the store's instance ID, which run pods are labelled
	// with (runhost.Kubernetes.List).
	Deployment string
	Live       []model.Run
	Runs       []model.Run
	Transcript string
	// UpgradeStatus is <data dir>/upgrade-status.json, "" when absent.
	UpgradeStatus string
	// Suspensions is the task's suspension rows (model.Store.Suspensions).
	Suspensions []model.Suspension
	// Invocations is the stub claude's arguments, one line per exec.
	Invocations string
}

func TestDaemonRollAdoptsTheRunAndItOpensItsPullRequest(t *testing.T) {
	testDaemonRoll(t, true)
}

// TestDaemonRollResumesADurableRunItDidNotAdopt is the same roll with no
// run host: the durable run's claude is a child of the daemon pod, as
// every run on a deployment with no adoptable framework is -- on
// -k8s-granule-sandboxes, which has no run pods, that is every run
// (design/design/durable-tasks.design.md#DT-1, case 2). The stopping pod drains
// it and finishes it as interrupted, which is a restart suspension; the
// next daemon pod starts the task on its first cycles, ahead of the
// queue, resumed (--resume) into the session the first run's stub
// named, whose store is on the claim (HOME=/data/home); and that run
// opens the pull request.
func TestDaemonRollResumesADurableRunItDidNotAdopt(t *testing.T) {
	testDaemonRoll(t, false)
}

// testDaemonRoll is both roll tests: adopt is whether the daemon runs
// claude in run pods (-run-host-image=self), which the next daemon pod
// adopts, or as its own child, which it cannot.
func testDaemonRoll(t *testing.T, adopt bool) {
	cluster := os.Getenv("GRAIN_RUNHOST_KIND")
	if cluster == "" {
		t.Skip("set GRAIN_RUNHOST_KIND=<kind cluster> to run against a real cluster")
	}
	registry := os.Getenv("GRAIN_RUNHOST_KIND_REGISTRY")
	viaUpgrade := registry != ""
	for _, bin := range []string{"docker", "kind", "kubectl", "git"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Fatalf("%s: %v", bin, err)
		}
	}
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

	// By the nanosecond: two of these started together must not share
	// a namespace.
	stamp := time.Now().UnixNano()
	ns := fmt.Sprintf("grain-adopt-%d", stamp)
	repository := fmt.Sprintf("grain-kube-adopt-%d", stamp)
	if viaUpgrade {
		repository = registry + "/" + repository
	}
	image := repository + ":first"

	// The image: the daemon, the run host (-run-host-image=self) and the
	// sandbox all run it, and this test binary in it plays the roles.
	// buildpack-deps:*-scm has the git the daemon and a sandbox need. The build's own
	// default sandbox image is this one, because an upgrade's checks ask
	// the candidate which sandbox image it names and pull that.
	dir := t.TempDir()
	for _, b := range [][]string{
		{"go", "build", "-ldflags", "-X github.com/ai-on-gke/grain/pkg/cli.defaultK8sSandboxImage=" + image, "-o", filepath.Join(dir, "grain"), "github.com/ai-on-gke/grain/cmd/grain"},
		{"go", "test", "-c", "-o", filepath.Join(dir, "grain.test"), "github.com/ai-on-gke/grain/pkg/cli"},
	} {
		cmd := exec.Command(b[0], b[1:]...)
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %v\n%s", b, err, out)
		}
	}
	kubectl, _ := exec.LookPath("kubectl")
	data, err := os.ReadFile(kubectl)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "kubectl"), data, 0o755); err != nil {
		t.Fatal(err)
	}
	// grain is the entrypoint, as in the shipped image, whose Deployment
	// args start with the subcommand. The user and working directory are
	// Dockerfile.k8s-sandbox's: a sandbox pod's git credentials are
	// written relative to where `kubectl exec` starts
	// (mcp.ConfigureGitCredentialsOverSSH), so that has to be $HOME.
	dockerfile := `FROM buildpack-deps:bookworm-scm
RUN useradd --create-home --uid 1000 --shell /bin/bash agent
COPY grain grain.test kubectl /usr/local/bin/
USER agent
WORKDIR /home/agent
ENTRYPOINT ["/usr/local/bin/grain"]
`
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte(dockerfile), 0o644); err != nil {
		t.Fatal(err)
	}
	run("", "docker", "build", "-q", "-t", image, dir)
	t.Cleanup(func() { _ = exec.Command("docker", "rmi", "-f", image).Run() })
	if viaUpgrade {
		// The upgrade's target: the same build under another digest, so
		// that repointing the Deployment at it is a rollout.
		next := repository + ":" + kubeAdoptBranch
		run("", "docker", "build", "-q", "--label", "grain.test/build=next", "-t", next, dir)
		t.Cleanup(func() { _ = exec.Command("docker", "rmi", "-f", next).Run() })
		run("", "docker", "push", "-q", image)
		run("", "docker", "push", "-q", next)
	} else {
		run("", "kind", "load", "docker-image", "--name", cluster, image)
	}

	// GitHub, on the kind network's gateway, which the daemon pod
	// reaches through its node.
	branch := model.BranchName(kubeAdoptTask)
	upstream := t.TempDir()
	bare := initBareRepo(t, filepath.Join(upstream, kubeAdoptOwner, kubeAdoptRepo+".git"))
	runLive(t, upstream, "git", "-C", bare, "config", "http.receivepack", "true")
	seedDir := filepath.Join(t.TempDir(), "seed")
	runLive(t, t.TempDir(), "git", "clone", bare, seedDir)
	runLive(t, seedDir, "git", "config", "user.email", "seed@example.com")
	runLive(t, seedDir, "git", "config", "user.name", "seed")
	if err := os.WriteFile(filepath.Join(seedDir, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runLive(t, seedDir, "git", "add", "README.md")
	runLive(t, seedDir, "git", "commit", "-q", "-m", "initial commit")
	runLive(t, seedDir, "git", "push", "-q", "origin", "main")
	sim := &syncedSim{sim: githubsim.New(kubeAdoptOwner, kubeAdoptRepo, bare, "main")}
	sim.seedPassingCheck(branch)
	gateway := kindGateway(t)
	ln, err := net.Listen("tcp", net.JoinHostPort(gateway, "0"))
	if err != nil {
		t.Fatalf("listening on the kind network's gateway %s: %v", gateway, err)
	}
	handler := githubHostHandler(sim, upstream, nil)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Logf("github: %s %s", r.Method, r.URL)
		handler.ServeHTTP(w, r)
	}))
	server.Listener.Close()
	server.Listener = ln
	server.Start()
	t.Cleanup(server.Close)
	githubHost := strings.TrimPrefix(server.URL, "http://")

	run(kubeAdoptManifest(ns, image, repository, githubHost, viaUpgrade, adopt), "kubectl", "apply", "-f", "-")
	t.Cleanup(func() {
		if t.Failed() {
			out, _ := exec.Command("kubectl", "-n", ns, "get", "pods,deployments", "-o", "wide").CombinedOutput()
			t.Logf("namespace %s at the end:\n%s", ns, out)
		}
		_ = exec.Command("kubectl", "delete", "namespace", ns, "--wait=false").Run()
	})

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	client := k8s.Client{Namespace: ns}
	daemonPods := func() []kubeAdoptPod {
		return kubeAdoptPods(t, ns, "app.kubernetes.io/component="+runhost.KubeDaemonComponent)
	}
	// servingDaemon is the daemon pod that is up and not on its way out.
	servingDaemon := func() string {
		for _, p := range daemonPods() {
			if p.Ready && !p.Terminating {
				return p.Name
			}
		}
		return ""
	}
	inspect := func(pod string) (kubeAdoptStore, error) {
		var s kubeAdoptStore
		out, err := exec.CommandContext(ctx, "kubectl", "-n", ns, "exec", pod, "-c", "grain", "--",
			"env", "GRAIN_KUBE_ADOPT_ROLE=inspect", "/usr/local/bin/grain.test",
			"-test.run", "^TestDaemonRollAdoptionRole$").CombinedOutput()
		for _, line := range strings.Split(string(out), "\n") {
			if rest, ok := strings.CutPrefix(line, kubeAdoptState); ok {
				return s, json.Unmarshal([]byte(rest), &s)
			}
		}
		return s, fmt.Errorf("inspecting the store in %s: %v\n%s", pod, err, out)
	}
	podLogs := &kubeAdoptLogs{logs: map[string]*bytes.Buffer{}}
	defer podLogs.dump(t)

	// The first daemon pod dispatches the run into a run pod.
	var first, deployment string
	var live model.Run
	var runPod runhost.Info
	if !pollUntil(ctx, 6*time.Minute, func() bool {
		if first = servingDaemon(); first == "" {
			return false
		}
		podLogs.follow(ctx, ns, first)
		s, err := inspect(first)
		if err != nil {
			t.Log(err)
			return false
		}
		deployment = s.Deployment
		infos, _ := runhost.Kubernetes{Client: client}.List(ctx, deployment)
		for _, r := range s.Live {
			if r.TaskID == kubeAdoptTask && adopt && len(infos) == 1 {
				live, runPod = r, infos[0]
				return true
			}
			// A daemon child's transcript reaches the store only as it
			// ends, so the stub having been exec'd is what says it is going.
			if r.TaskID == kubeAdoptTask && !adopt && len(infos) == 0 && s.Invocations != "" {
				live = r
				return true
			}
		}
		return false
	}) {
		if adopt {
			t.Fatal("the first daemon pod never had the run in a run pod")
		}
		t.Fatal("the first daemon pod never had the run going as its own child")
	}
	// Long enough for the first daemon's tail to read the stub's first
	// line, so the transcript is split across the two daemon pods.
	time.Sleep(3 * time.Second)
	startedAt := live.StartedAt
	// The task is durable: its run has no deadline, and its run pod
	// none to enforce (orchestrator.runHostDeadline).
	if adopt && !runPod.Labels.Deadline.IsZero() {
		t.Fatalf("run pod %s carries deadline %s, want none for a durable run", runPod.Name, runPod.Labels.Deadline)
	}
	if live.AgentStartedAt == nil || live.AgentStartedAt.Before(startedAt) {
		t.Fatalf("live run %s reads back agent start %v (started %s), so an adopter would carry its deadline from the wrong moment",
			live.ID, live.AgentStartedAt, startedAt)
	}
	sandboxes := kubeAdoptPods(t, ns, k8s.SandboxLabel+"="+k8s.SandboxValue)
	if len(sandboxes) != 1 {
		t.Fatalf("sandbox pods before the roll = %+v, want the run's one", sandboxes)
	}
	t.Logf("daemon pod %s started run %s (run pod %q), with sandbox pod %s", first, live.ID, runPod.Name, sandboxes[0].Name)

	// The agent's push, while the run is in flight.
	runLive(t, seedDir, "git", "checkout", "-q", "-b", branch)
	if err := os.WriteFile(filepath.Join(seedDir, "NOTES.md"), []byte("across a roll\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runLive(t, seedDir, "git", "add", "NOTES.md")
	runLive(t, seedDir, "git", "commit", "-q", "-m", "work done across a roll")
	runLive(t, seedDir, "git", "push", "-q", "origin", branch)

	// The roll.
	if viaUpgrade {
		out := run("", "kubectl", "-n", ns, "exec", first, "-c", "grain", "--",
			"env", "GRAIN_KUBE_ADOPT_ROLE=upgrade", "/usr/local/bin/grain.test", "-test.run", "^TestDaemonRollAdoptionRole$", "-test.v")
		t.Logf("asked %s to upgrade to %s:\n%s", first, kubeAdoptBranch, out)
	} else {
		run("", "kubectl", "-n", ns, "rollout", "restart", "deployment/grain")
	}
	var second string
	if !pollUntil(ctx, 10*time.Minute, func() bool {
		second = servingDaemon()
		return second != "" && second != first
	}) {
		t.Fatalf("no second daemon pod came up after the roll; daemon pods: %+v", daemonPods())
	}
	podLogs.follow(ctx, ns, second)
	secondUp := time.Now()
	t.Logf("daemon pod %s replaced %s", second, first)
	if !adopt {
		testDaemonRollResumes(t, ctx, sim, inspect, second, branch, live, secondUp)
		return
	}
	if infos, err := (runhost.Kubernetes{Client: client}).List(ctx, deployment); err != nil || len(infos) != 1 || infos[0].Name != runPod.Name {
		t.Fatalf("after the roll, run pods = %+v (%v); want %s still there", infos, err, runPod.Name)
	}
	if after := kubeAdoptPods(t, ns, k8s.SandboxLabel+"="+k8s.SandboxValue); len(after) != 1 || after[0].UID != sandboxes[0].UID {
		t.Fatalf("after the roll, sandbox pods = %+v; want %+v, the same pod, claimed back rather than reaped and recreated", after, sandboxes)
	}
	if n := sim.pullRequestCount(); n != 0 {
		t.Fatalf("%d pull request(s) opened before the run finished", n)
	}

	// The agent finishes, under the second daemon pod.
	run("", "kubectl", "-n", ns, "exec", second, "-c", "grain", "--", "touch", filepath.Join(kubeAdoptCredDir, "release"))
	var final kubeAdoptStore
	var finished model.Run
	if !pollUntil(ctx, 5*time.Minute, func() bool {
		s, err := inspect(second)
		if err != nil || len(s.Runs) == 0 || s.Runs[len(s.Runs)-1].FinishedAt == nil {
			return false
		}
		final, finished = s, s.Runs[len(s.Runs)-1]
		return true
	}) {
		t.Fatal("the run never finished under the second daemon pod")
	}
	if finished.ID != live.ID {
		t.Fatalf("the run that finished is %s, not the one in flight across the roll (%s)", finished.ID, live.ID)
	}
	if finished.Outcome != "succeeded" {
		t.Fatalf("the adopted run ended %q (%s), want succeeded", finished.Outcome, finished.Detail)
	}
	if !finished.StartedAt.Equal(startedAt) {
		t.Fatalf("the adopted run's start moved from %s to %s, which resets its deadline", startedAt, finished.StartedAt)
	}
	if !pollUntil(ctx, liveFinishWindow, func() bool { return sim.pullRequestCount() > 0 }) {
		t.Fatalf("the adopted run ended %q (%s), but the second daemon pod opened no pull request for %s",
			finished.Outcome, finished.Detail, branch)
	}
	if pr := sim.onlyPullRequest(t); pr.Head != branch {
		t.Fatalf("pull request head = %q, want %q", pr.Head, branch)
	}
	for _, line := range []string{"line written before the restart", "line written after the restart"} {
		if n := strings.Count(final.Transcript, line); n != 1 {
			t.Errorf("transcript has %q %d times, want once (a hole or a duplicate across the roll):\n%s", line, n, final.Transcript)
		}
	}
	if before, after := strings.Index(final.Transcript, "before the restart"), strings.Index(final.Transcript, "after the restart"); before > after {
		t.Errorf("transcript is out of order across the roll:\n%s", final.Transcript)
	}
	if !pollUntil(ctx, 2*time.Minute, func() bool {
		infos, _ := runhost.Kubernetes{Client: client}.List(ctx, deployment)
		return len(infos) == 0
	}) {
		t.Errorf("the run pod was left behind after the adopted run finished")
	}

	// What each daemon pod said it did.
	if got := podLogs.get(second); !strings.Contains(got, "run-host adoption: 1 adopted, 0 lost") {
		t.Errorf("the second daemon pod did not log adopting the run:\n%s", got)
	}
	if viaUpgrade {
		if got := podLogs.get(first); !strings.Contains(got, "flushed the spool cursors of 1 run(s)") {
			t.Errorf("the first daemon pod did not log the upgrade's flush (stageKube):\n%s", got)
		}
		img := strings.TrimSpace(run("", "kubectl", "-n", ns, "get", "deployment/grain", "-o", "jsonpath={.spec.template.spec.containers[0].image}"))
		if !strings.HasPrefix(img, repository+":"+kubeAdoptBranch+"@sha256:") {
			t.Errorf("after the upgrade the Deployment runs %q, want %s:%s pinned to a digest", img, repository, kubeAdoptBranch)
		}
		t.Logf("upgrade status after the roll: %s", final.UpgradeStatus)
	}
}

// testDaemonRollResumes is TestDaemonRollResumesADurableRunItDidNotAdopt
// after the roll: the run in flight (live) ended as a restart
// suspension, and the next daemon pod (second, serving from secondUp)
// started the task again on its first cycles, resumed into the stub's
// session, and that run opened the pull request.
func testDaemonRollResumes(t *testing.T, ctx context.Context, sim *syncedSim, inspect func(string) (kubeAdoptStore, error),
	second, branch string, live model.Run, secondUp time.Time) {
	t.Helper()
	var final kubeAdoptStore
	var resumed model.Run
	if !pollUntil(ctx, 5*time.Minute, func() bool {
		s, err := inspect(second)
		if err != nil || len(s.Runs) < 2 {
			return false
		}
		last := s.Runs[len(s.Runs)-1]
		if last.ID == live.ID || last.FinishedAt == nil {
			return false
		}
		final, resumed = s, last
		return true
	}) {
		t.Fatal("the second daemon pod never finished a run after the one in flight across the roll")
	}
	var interrupted *model.Run
	for i := range final.Runs {
		if final.Runs[i].ID == live.ID {
			interrupted = &final.Runs[i]
		}
	}
	if interrupted == nil || interrupted.Outcome != model.InterruptedOutcome {
		t.Fatalf("the run in flight across the roll ended %+v, want it %s", interrupted, model.InterruptedOutcome)
	}
	var susp *model.Suspension
	for i := range final.Suspensions {
		if final.Suspensions[i].RunID == live.ID {
			susp = &final.Suspensions[i]
		}
	}
	if susp == nil || susp.Kind != model.SuspendRestart {
		t.Fatalf("suspensions = %+v, want run %s suspended with kind %s", final.Suspensions, live.ID, model.SuspendRestart)
	}
	if susp.ResumedRunID != resumed.ID {
		t.Errorf("the restart suspension was resumed by %q, want %s", susp.ResumedRunID, resumed.ID)
	}
	if resumed.Outcome != "succeeded" {
		t.Fatalf("the resumed run %s ended %q (%s), want succeeded", resumed.ID, resumed.Outcome, resumed.Detail)
	}
	// Ahead of the queue on the new daemon's first cycles, not after a
	// retry backoff: it is a suspension, and the task is durable.
	if wait := resumed.StartedAt.Sub(secondUp); wait > 2*time.Minute {
		t.Errorf("the resumed run started %s after the second daemon pod was serving, want it on its first cycles", wait)
	}
	calls := strings.Split(strings.TrimSpace(final.Invocations), "\n")
	if len(calls) < 2 || strings.Contains(calls[0], "--resume") || !strings.Contains(calls[len(calls)-1], "--resume stub-session") {
		t.Errorf("stub claude was exec'd with:\n%s\nwant the first fresh and the last --resume stub-session "+
			"(the session store on the claim survived the roll)", final.Invocations)
	}
	if !strings.Contains(final.Transcript, "line written after the restart") {
		t.Errorf("the resumed run's transcript has no line from the resumed session:\n%s", final.Transcript)
	}
	if !pollUntil(ctx, liveFinishWindow, func() bool { return sim.pullRequestCount() > 0 }) {
		t.Fatalf("the resumed run ended %q (%s), but no pull request opened for %s", resumed.Outcome, resumed.Detail, branch)
	}
	if pr := sim.onlyPullRequest(t); pr.Head != branch {
		t.Fatalf("pull request head = %q, want %q", pr.Head, branch)
	}
	t.Logf("run %s ended %s across the roll, suspended (%s); run %s resumed it %s after the second daemon pod came up and opened the pull request",
		live.ID, interrupted.Outcome, susp.Kind, resumed.ID, resumed.StartedAt.Sub(secondUp).Round(time.Second))
}

// TestDaemonRollAdoptionRole is this test binary inside the cluster,
// playing what TestDaemonRollAdoptsTheRunAndItOpensItsPullRequest cannot
// do from outside it, by GRAIN_KUBE_ADOPT_ROLE:
//
//   - seed, the daemon pod's init container: the claim's home and
//     credential directories, the stub claude, the GitHub credential
//     and the task, once.
//   - inspect, exec'd in a daemon pod: the run rows, the transcript and
//     the upgrade status, as one line of JSON.
//   - upgrade, exec'd in the first daemon pod: POST /api/upgrade, as the
//     Upgrade button does.
//   - forward, the daemon pod's sidecar: kubeAdoptGitHub, forwarded to
//     GRAIN_KUBE_ADOPT_UPSTREAM.
func TestDaemonRollAdoptionRole(t *testing.T) {
	role := os.Getenv("GRAIN_KUBE_ADOPT_ROLE")
	if role == "" {
		t.Skip("the in-cluster half of TestDaemonRollAdoptsTheRunAndItOpensItsPullRequest")
	}
	ctx := context.Background()
	switch role {
	case "seed":
		marker := filepath.Join(kubeAdoptData, ".kube-adopt-seeded")
		if _, err := os.Stat(marker); err == nil {
			return
		}
		for _, d := range []string{filepath.Join(kubeAdoptData, "home"), filepath.Join(kubeAdoptData, "secrets", "github"), kubeAdoptCredDir} {
			if err := os.MkdirAll(d, 0o700); err != nil {
				t.Fatal(err)
			}
		}
		restartStubClaude(t, kubeAdoptCredDir)
		// Named explicitly: grain has no default model of its own, and
		// the -agent-framework flag this used to pass is gone.
		seedTestDaemonConnections(t, daemon.Config{DataDir: kubeAdoptData, ClaudePath: kubeAdoptCredDir + "/claude"}, `git:
  - credential: anonymous
models:
  - id: claude
    framework: claude
    models: [claude-sonnet-5]
    credential_file: `+kubeAdoptCredDir+`/token
`)
		store, db, err := daemon.OpenStore("sqlite", "", kubeAdoptData)
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		task := model.Task{
			ID: kubeAdoptTask, Intent: model.IntentImplement,
			Title: "run across a Deployment roll", Body: "the stub agent waits for the roll",
			Origin: model.Origin{
				Attribution: model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "tester"}},
				Reason:      model.ReasonDirect,
			},
			Binding: model.BindingDirective,
			Target:  &model.RepoRef{Owner: kubeAdoptOwner, Name: kubeAdoptRepo},
			// Small enough for a kind node beside the daemon pod; the
			// default sandbox asks for 8 GiB.
			SandboxCPUs: 1, SandboxMemoryMB: 1024, SandboxDiskGB: 5,
			// Durable (design/design/durable-tasks.design.md#DT-1): no deadline,
			// priority dispatch, and placed on the adoptable run host --
			// the run this roll has to carry across is a durable one.
			Grants: []model.Grant{{Capability: model.DurableCapability}},
			// Fixed, so the branch this test pushes and looks for the pull
			// request on is the one the daemon uses, whatever its
			// deployment's branch template makes of the title.
			Branch: model.BranchName(kubeAdoptTask),
		}
		task.Approval = &model.Attribution{Actor: task.Origin.Attribution.Actor}
		if err := store.PutTask(ctx, task); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(marker, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	case "inspect":
		store, db, err := daemon.OpenStore("sqlite", "", kubeAdoptData)
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		var s kubeAdoptStore
		if s.Deployment, err = store.InstanceID(ctx); err != nil {
			t.Fatal(err)
		}
		if s.Live, err = store.LiveRuns(ctx); err != nil {
			t.Fatal(err)
		}
		if s.Runs, err = store.Runs(ctx, kubeAdoptTask); err != nil {
			t.Fatal(err)
		}
		if len(s.Runs) > 0 {
			if s.Transcript, _, err = store.RunTranscript(ctx, kubeAdoptTask, s.Runs[len(s.Runs)-1].Attempt); err != nil {
				t.Fatal(err)
			}
		}
		if s.Suspensions, err = store.Suspensions(ctx, kubeAdoptTask); err != nil {
			t.Fatal(err)
		}
		invocations, _ := os.ReadFile(filepath.Join(kubeAdoptCredDir, "invocations"))
		s.Invocations = string(invocations)
		status, _ := os.ReadFile(filepath.Join(kubeAdoptData, "upgrade-status.json"))
		s.UpgradeStatus = string(status)
		line, err := json.Marshal(s)
		if err != nil {
			t.Fatal(err)
		}
		fmt.Println(kubeAdoptState + string(line))
	case "upgrade":
		req, _ := http.NewRequest(http.MethodPost, "http://127.0.0.1:8080/api/upgrade",
			strings.NewReader(`{"branch":"`+kubeAdoptBranch+`"}`))
		req.Header.Set("Content-Type", "application/json")
		// A single-tenant daemon wants its access code from every caller,
		// loopback included (`grain access-code`).
		code, err := os.ReadFile(filepath.Join(kubeAdoptData, "local-api-token"))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(code)))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusAccepted {
			t.Fatalf("POST /api/upgrade = %s: %s", resp.Status, body)
		}
		fmt.Printf("POST /api/upgrade = %s\n", resp.Status)
	case "forward":
		ln, err := net.Listen("tcp", kubeAdoptGitHub)
		if err != nil {
			t.Fatal(err)
		}
		upstream := os.Getenv("GRAIN_KUBE_ADOPT_UPSTREAM")
		for {
			in, err := ln.Accept()
			if err != nil {
				t.Fatal(err)
			}
			go func() {
				defer in.Close()
				out, err := net.Dial("tcp", upstream)
				if err != nil {
					fmt.Printf("dialling %s: %v\n", upstream, err)
					return
				}
				defer out.Close()
				go func() { _, _ = io.Copy(out, in) }()
				_, _ = io.Copy(in, out)
			}()
		}
	default:
		t.Fatalf("unknown GRAIN_KUBE_ADOPT_ROLE %q", role)
	}
}

// kindGateway is the IPv4 gateway of docker's "kind" network: the
// address of this machine as a kind node sees it.
func kindGateway(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("docker", "network", "inspect", "kind", "-f", "{{range .IPAM.Config}}{{.Gateway}} {{end}}").Output()
	if err != nil {
		t.Fatalf("reading the kind network's gateway: %v", err)
	}
	for _, gw := range strings.Fields(string(out)) {
		if ip := net.ParseIP(gw); ip != nil && ip.To4() != nil {
			return gw
		}
	}
	t.Fatalf("the kind network has no IPv4 gateway: %q", out)
	return ""
}

type kubeAdoptPod struct {
	Name, UID          string
	Ready, Terminating bool
}

// kubeAdoptPods lists the pods in ns matching selector, with what
// k8s.Client's own listing leaves out: the UID, which tells a pod
// claimed back from one recreated under the same name.
func kubeAdoptPods(t *testing.T, ns, selector string) []kubeAdoptPod {
	t.Helper()
	out, err := exec.Command("kubectl", "-n", ns, "get", "pods", "-l", selector, "-o", "json").Output()
	if err != nil {
		t.Logf("listing pods %s: %v", selector, err)
		return nil
	}
	var list struct {
		Items []struct {
			Metadata struct {
				Name              string     `json:"name"`
				UID               string     `json:"uid"`
				DeletionTimestamp *time.Time `json:"deletionTimestamp"`
			} `json:"metadata"`
			Status struct {
				Conditions []struct{ Type, Status string } `json:"conditions"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal(out, &list); err != nil {
		t.Fatal(err)
	}
	var pods []kubeAdoptPod
	for _, i := range list.Items {
		p := kubeAdoptPod{Name: i.Metadata.Name, UID: i.Metadata.UID, Terminating: i.Metadata.DeletionTimestamp != nil}
		for _, c := range i.Status.Conditions {
			p.Ready = p.Ready || (c.Type == "Ready" && c.Status == "True")
		}
		pods = append(pods, p)
	}
	return pods
}

// kubeAdoptLogs follows each daemon pod's log from its start, so the
// first pod's is still there once the roll has deleted it.
type kubeAdoptLogs struct {
	mu   sync.Mutex
	logs map[string]*bytes.Buffer
}

func (l *kubeAdoptLogs) follow(ctx context.Context, ns, pod string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, ok := l.logs[pod]; ok {
		return
	}
	buf := &bytes.Buffer{}
	l.logs[pod] = buf
	cmd := exec.CommandContext(ctx, "kubectl", "-n", ns, "logs", "-f", "-c", "grain", pod)
	cmd.Stdout = &lockedWriter{mu: &l.mu, w: buf}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err == nil {
		go func() { _ = cmd.Wait() }()
	}
}

func (l *kubeAdoptLogs) get(pod string) string {
	l.mu.Lock()
	defer l.mu.Unlock()
	if b, ok := l.logs[pod]; ok {
		return b.String()
	}
	return ""
}

func (l *kubeAdoptLogs) dump(t *testing.T) {
	if !t.Failed() {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	for pod, b := range l.logs {
		t.Logf("daemon pod %s:\n%s", pod, b)
	}
}

type lockedWriter struct {
	mu *sync.Mutex
	w  io.Writer
}

func (w *lockedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.w.Write(p)
}

// kubeAdoptManifest is deploy/kubernetes/grain.yaml cut down to one
// namespace: its PriorityClasses (cluster-wide, so shared by every run of
// this test), RBAC, claim, Services and Deployment, with run hosts on
// when runHosts (-run-host-image=self over the claim) and, viaUpgrade,
// the Kubernetes upgrade path pointed at repository.
func kubeAdoptManifest(ns, image, repository, githubHost string, viaUpgrade, runHosts bool) string {
	args := []string{
		"daemon", "-data-dir=" + kubeAdoptData, "-ui-addr=0.0.0.0:8080", "-as=tester",
		"-poll-interval=1s",
		"-claude-path=" + kubeAdoptCredDir + "/claude", "-claude-oauth-token-file=" + kubeAdoptCredDir + "/token",
		"-github-host=" + kubeAdoptGitHub, "-github-insecure-http",
		"-k8s-sandboxes", "-k8s-namespace=" + ns, "-k8s-workspace=/home/agent", "-k8s-sandbox-image=" + image,
		"-sandbox-port-base=9728", "-k8s-sandbox-service=grain-sandbox",
		"-k8s-deployment=grain", "-k8s-container=grain",
	}
	if runHosts {
		args = append(args,
			"-run-host-image=self", "-run-host-credential-dir="+kubeAdoptCredDir, "-run-host-dir="+kubeAdoptData+"/runhosts",
			"-k8s-run-host-volume=grain-data:"+kubeAdoptData, "-run-host-daemon-url=http://grain."+ns+".svc")
	}
	if viaUpgrade {
		args = append(args, "-upgrade-image="+repository)
	}
	quoted := make([]string, len(args))
	for i, a := range args {
		quoted[i] = fmt.Sprintf("%q", a)
	}
	pull := "Never"
	if viaUpgrade {
		pull = "IfNotPresent"
	}
	return strings.NewReplacer("{{NS}}", ns, "{{IMAGE}}", image, "{{PULL}}", pull, "{{ARGS}}", strings.Join(quoted, ", "),
		"{{GITHUB}}", githubHost).Replace(`
apiVersion: v1
kind: Namespace
metadata: {name: {{NS}}}
---
apiVersion: scheduling.k8s.io/v1
kind: PriorityClass
metadata: {name: grain-daemon}
value: 2000000
preemptionPolicy: Never
---
apiVersion: scheduling.k8s.io/v1
kind: PriorityClass
metadata: {name: grain-interactive}
value: 1000000
preemptionPolicy: PreemptLowerPriority
---
apiVersion: scheduling.k8s.io/v1
kind: PriorityClass
metadata: {name: grain-agent}
value: 100000
preemptionPolicy: Never
---
apiVersion: scheduling.k8s.io/v1
kind: PriorityClass
metadata: {name: grain-ci}
value: 1000
preemptionPolicy: Never
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
    resources: ["pods/exec"]
    verbs: ["create"]
  - apiGroups: [""]
    resources: ["pods/log"]
    verbs: ["get"]
  - apiGroups: [""]
    resources: ["persistentvolumeclaims"]
    verbs: ["get"]
  - apiGroups: ["apps"]
    resources: ["deployments"]
    verbs: ["get", "list", "watch"]
  - apiGroups: ["apps"]
    resources: ["deployments"]
    resourceNames: ["grain"]
    verbs: ["patch", "update"]
  - apiGroups: ["apps"]
    resources: ["replicasets"]
    verbs: ["get", "list", "watch"]
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
                labelSelector: {matchLabels: {app.kubernetes.io/managed-by: grain, app.kubernetes.io/component: runhost}}
                topologyKey: kubernetes.io/hostname
      initContainers:
        - name: seed
          image: {{IMAGE}}
          imagePullPolicy: {{PULL}}
          command: ["/usr/local/bin/grain.test", "-test.run", "^TestDaemonRollAdoptionRole$", "-test.v"]
          env:
            - {name: GRAIN_KUBE_ADOPT_ROLE, value: seed}
            - {name: HOME, value: /data/home}
          volumeMounts: [{name: data, mountPath: /data}]
      containers:
        - name: grain
          image: {{IMAGE}}
          imagePullPolicy: {{PULL}}
          args: [{{ARGS}}]
          env:
            - name: GRAIN_POD_IP
              valueFrom: {fieldRef: {fieldPath: status.podIP}}
            - name: GRAIN_POD_NAME
              valueFrom: {fieldRef: {fieldPath: metadata.name}}
            - {name: HOME, value: /data/home}
          ports: [{name: http, containerPort: 8080}]
          readinessProbe:
            httpGet: {path: /api/auth/access-code, port: http}
            initialDelaySeconds: 2
            periodSeconds: 2
          volumeMounts: [{name: data, mountPath: /data}]
        - name: github
          image: {{IMAGE}}
          imagePullPolicy: {{PULL}}
          command: ["/usr/local/bin/grain.test", "-test.run", "^TestDaemonRollAdoptionRole$", "-test.v", "-test.timeout", "0"]
          env:
            - {name: GRAIN_KUBE_ADOPT_ROLE, value: forward}
            - {name: GRAIN_KUBE_ADOPT_UPSTREAM, value: "{{GITHUB}}"}
      volumes:
        - name: data
          persistentVolumeClaim: {claimName: grain-data}
---
apiVersion: v1
kind: Service
metadata: {name: grain, namespace: {{NS}}}
spec:
  selector: {app.kubernetes.io/name: grain}
  ports: [{name: http, port: 80, targetPort: http}]
---
apiVersion: v1
kind: Service
metadata: {name: grain-sandbox, namespace: {{NS}}}
spec:
  publishNotReadyAddresses: true
  selector: {app.kubernetes.io/name: grain, app.kubernetes.io/component: daemon}
  ports:
    - {name: git-proxy, port: 9728, targetPort: 9728}
    - {name: claude-proxy, port: 9744, targetPort: 9744}
    - {name: openai-proxy, port: 9745, targetPort: 9745}
    - {name: gemini-proxy, port: 9746, targetPort: 9746}
`)
}
