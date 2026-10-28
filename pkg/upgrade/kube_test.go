package upgrade

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func kubeUpgrader(t *testing.T, kubectl string, healthCheck []string) *Upgrader {
	t.Helper()
	// The same two things newUpgrader does, and for the same reasons --
	// this helper had neither, which is what let a kube test's upgrade
	// outlive it.
	//
	// logToTest claims the journal, so a line this upgrade writes is
	// attributed to the test that caused it rather than printed as
	// "[upgrade log with no test to attach it to]".
	//
	// waitForIdle is the one that matters: without it a test returns
	// while its upgrade is still walking its steps, and that goroutine
	// goes on running inside whatever test is next. Seen on grain CI as
	// this test's own "step 3/6 unit flags" line surfacing in the middle
	// of TestUpgraderImageStopsWhenTheSandboxQuestionTimesOut -- which is
	// the worst test to inherit a stray goroutine, since it sets the
	// package-wide imageRunTimeout to 100ms and then asserts a step
	// stopped on that bound. Cleanups run last-registered-first, so this
	// waits for the upgrade before logToTest hands the journal back.
	//
	// It costs nothing for a test that never starts one: Done answers
	// with a closed channel when nothing is in flight (noUpgradeRunning).
	logToTest(t)
	u := New(Config{
		Kube: &KubeConfig{
			Repository: "example/grain",
			Deployment: "grain",
			Namespace:  "grain",
			Kubectl:    kubectl,
		},
		HealthCheckArgs: healthCheck,
		StatusFile:      filepath.Join(t.TempDir(), "upgrade-status.json"),
	})
	t.Cleanup(func() { waitForIdle(t, u) })
	return u
}

func TestKubeModeIsItsOwnPipeline(t *testing.T) {
	u := kubeUpgrader(t, "/nonexistent", []string{"schema-version"})
	if got := u.Mode(); got != ModeKube {
		t.Errorf("Mode = %q, want %q", got, ModeKube)
	}
	// No pull step and no restart step: the cluster does the pull (as
	// part of the health check) and the restart (as the rollout). The
	// flag check is there, though -- the args are in a spec that an
	// upgrade does not rewrite, which is the drift it exists for.
	if got := strings.Join(u.plan(), ","); got != stepVerify+","+stepHealthCheck+","+stepFlags+","+stepSandbox+","+stepPriorityClasses+","+stepSchema+","+stepRollout {
		t.Errorf("plan = %q", got)
	}
	if !strings.Contains(u.description(), "deployment/grain") {
		t.Errorf("description = %q, want it to name what is being repointed", u.description())
	}
}

func TestKubeUpgradeCountsAsRestartingWithoutARestartCommand(t *testing.T) {
	// The rollout is the restart, and it has already begun by the time
	// the last step returns -- so the success has to be recorded as
	// "restarting" and the queue hold left for whatever comes up next,
	// exactly as after a successful RestartCmd.
	u := kubeUpgrader(t, "/nonexistent", nil)
	if !u.restarts() {
		t.Error("a Kubernetes upgrade does not count as restarting, so it would release its own hold and report no restart")
	}
	// And an ordinary deployment with no restart command still does not.
	if New(Config{StatusFile: filepath.Join(t.TempDir(), "s.json")}).restarts() {
		t.Error("an upgrade with no restart mechanism counts as restarting")
	}
}

func TestKubeModeWinsOverAnImageConfig(t *testing.T) {
	// A cluster deployment names an image repository too; a caller that
	// set both meant the cluster, not a ref file on a host it does not
	// have.
	u := New(Config{
		Kube:       &KubeConfig{Repository: "example/grain", Deployment: "grain"},
		Image:      &ImageConfig{Repository: "example/grain", RefFile: "/tmp/ref"},
		StatusFile: filepath.Join(t.TempDir(), "s.json"),
	})
	if got := u.Mode(); got != ModeKube {
		t.Errorf("Mode = %q, want %q", got, ModeKube)
	}
}

// kubeDeploymentJSON is what `kubectl get deployment -o json` answers
// for a Deployment shaped like deploy/kubernetes/grain.yaml's: uid 1000,
// no capabilities, and not root.
const kubeDeploymentJSON = `{"spec":{"template":{"spec":{
  "securityContext":{"runAsNonRoot":true,"runAsUser":1000,"runAsGroup":1000,"fsGroup":1000},
  "containers":[{"name":"grain","securityContext":{"allowPrivilegeEscalation":false,"capabilities":{"drop":["ALL"]}}}]
}}}}`

// kubeDigest is the digest every fake check pod below reports having run,
// and kubeSucceeded the `get pod` answer of a pod that ran it and exited
// zero -- phase|ready|imageIDs|reason, pkg/k8s's podStatusFormat. The
// imageID is spelled the way cri-dockerd spells it, and with the
// docker.io a kubelet adds to a repository configured without it.
const (
	kubeDigest    = "sha256:5e1f0c0ffee"
	kubeSucceeded = "Succeeded|False|docker-pullable://docker.io/example/grain@" + kubeDigest + " |"
)

// fakeKubectl writes a kubectl stand-in that answers `get pod` from
// phase, `get deployment` with kubeDeploymentJSON, and `logs` from logs
// whichever pod is asked, and records every call it was made -- and
// every manifest it was applied, so a test can assert on the pod that
// would have been created.
func fakeKubectl(t *testing.T, phase, logs string) (path string, calls func() string, manifests func() []string) {
	t.Helper()
	return fakeKubectlAnswering(t, phase, logs, logs)
}

// fakeKubectlAnswering is fakeKubectl with the two throwaway pods told
// apart: checkLogs is what the health check's pod printed, askLogs what
// the pod answering a question (the sandbox image, the flag names)
// printed. They have to be separable for a flag check to be testable at
// all -- the check pod and the ask pod run the same image, and only the
// second one is asked anything.
func fakeKubectlAnswering(t *testing.T, phase, checkLogs, askLogs string) (path string, calls func() string, manifests func() []string) {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "calls")
	applied := filepath.Join(dir, "manifests")
	// The two pods' logs go through files rather than through the script,
	// so a log holding newlines or quotes -- which is exactly what a
	// build too old to know -flag-names prints -- does not have to
	// survive shell quoting.
	check := filepath.Join(dir, "check.log")
	ask := filepath.Join(dir, "ask.log")
	for path, content := range map[string]string{check: checkLogs, ask: askLogs} {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("writing a fake pod log: %v", err)
		}
	}
	script := "#!/bin/sh\n" +
		"printf '%s\\036' \"$*\" >> " + log + "\n" +
		"case \"$3 $4\" in\n" +
		"  'get deployment') printf '%s' '" + kubeDeploymentJSON + "' ;;\n" +
		"  'get '*) printf '%s' '" + phase + "' ;;\n" +
		"  'logs '*) if [ \"$4\" = " + kubeAskPod + " ]; then cat " + ask + "; else cat " + check + "; fi ;;\n" +
		"  'apply '*) { cat; printf '\\036'; } >> " + applied + " ;;\n" +
		"esac\n" +
		"exit 0\n"
	path = filepath.Join(dir, "kubectl")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("writing the fake kubectl: %v", err)
	}
	return path, func() string {
			data, _ := os.ReadFile(log)
			return strings.ReplaceAll(string(data), "\x1e", " ;; ")
		}, func() []string {
			data, _ := os.ReadFile(applied)
			var out []string
			for _, manifest := range strings.Split(string(data), "\x1e") {
				if manifest = strings.TrimSpace(manifest); manifest != "" {
					out = append(out, manifest)
				}
			}
			return out
		}
}

// podSpec unmarshals one applied manifest and hands back its spec.
func podSpec(t *testing.T, manifest string) map[string]any {
	t.Helper()
	var pod struct {
		Metadata struct {
			Name string `json:"name"`
		} `json:"metadata"`
		Spec map[string]any `json:"spec"`
	}
	if err := json.Unmarshal([]byte(manifest), &pod); err != nil {
		t.Fatalf("unmarshalling an applied manifest: %v", err)
	}
	return pod.Spec
}

// podImage is the image the one container in an applied manifest runs.
func podImage(t *testing.T, manifest string) string {
	t.Helper()
	container := podSpec(t, manifest)["containers"].([]any)[0].(map[string]any)
	return container["image"].(string)
}

func TestStageKubeRepointsTheDeploymentOnlyAfterTheCheckPasses(t *testing.T) {
	kubectl, calls, _ := fakeKubectl(t, kubeSucceeded, "")
	u := kubeUpgrader(t, kubectl, []string{"schema-version"})
	p := newProgress(u.cfg.StatusFile, "main", Status{}, u.plan())
	defer p.stop()

	detail, err := u.stageKube(context.Background(), p, "grain/task-358")
	if err != nil {
		t.Fatalf("stageKube: %v", err)
	}
	// The tag CI publishes for a branch replaces '/' with '-'.
	if !strings.Contains(detail, "example/grain:grain..task-358") {
		t.Errorf("detail = %q, want the image it rolled out", detail)
	}
	recorded := calls()
	// At the digest the check pod ran, not at the tag: CI may push the
	// tag again before a node pulls it (grain/task-1559).
	if !strings.Contains(recorded, "set image deployment/grain grain=example/grain:grain..task-358@"+kubeDigest) {
		t.Errorf("calls = %q, want the deployment repointed at the digest the check ran", recorded)
	}
	// The check pod is deleted whichever way the check went: one left
	// behind is clutter an operator has to explain to themselves.
	if !strings.Contains(recorded, "delete pod "+kubeHealthCheckPod) {
		t.Errorf("calls = %q, want the check pod cleaned up", recorded)
	}
}

func TestStageKubeRefusesWhenTheCheckPodReportsNoDigest(t *testing.T) {
	// A pod that ran and exited zero but whose runtime named no registry
	// digest -- an image built on the node, say -- proved something about
	// an image nothing can name again, so the rollout would be by tag and
	// could run anything. Refused, with the Deployment untouched.
	for _, status := range []string{"Succeeded|False||", "Succeeded|False|sha256:5e1f0c0ffee |", "Succeeded|False|other/grain@" + kubeDigest + " |"} {
		kubectl, calls, _ := fakeKubectl(t, status, "")
		u := kubeUpgrader(t, kubectl, []string{"schema-version"})
		p := newProgress(u.cfg.StatusFile, "main", Status{}, u.plan())

		_, err := u.stageKube(context.Background(), p, "main")
		p.stop()
		if err == nil {
			t.Fatalf("stageKube succeeded with a check pod reporting %q", status)
		}
		if !strings.Contains(err.Error(), "nothing to pin") || !strings.Contains(err.Error(), "already runs") {
			t.Errorf("error = %q, want it to say there was nothing to pin to and the deployment is unchanged", err)
		}
		if strings.Contains(calls(), "set image") {
			t.Errorf("the deployment was repointed with a check pod reporting %q", status)
		}
	}
}

func TestStageKubeRefusesWithoutAHealthCheckToPinTo(t *testing.T) {
	kubectl, calls, _ := fakeKubectl(t, kubeSucceeded, "")
	u := kubeUpgrader(t, kubectl, nil)
	p := newProgress(u.cfg.StatusFile, "main", Status{}, u.plan())
	defer p.stop()

	if _, err := u.stageKube(context.Background(), p, "main"); err == nil {
		t.Fatal("stageKube rolled out with no health check to say which image it was")
	}
	if strings.Contains(calls(), "set image") || strings.Contains(calls(), "apply") {
		t.Errorf("calls = %q, want nothing created and the deployment untouched", calls())
	}
}

func TestKubePin(t *testing.T) {
	for _, tc := range []struct {
		ref, imageID, want string
	}{
		{"example/grain:main", "docker-pullable://docker.io/example/grain@sha256:abc", "example/grain:main@sha256:abc"},
		{"ghcr.io/bwsalmon/grain/grain:main", "ghcr.io/bwsalmon/grain/grain@sha256:abc", "ghcr.io/bwsalmon/grain/grain:main@sha256:abc"},
		{"grain:main", "docker.io/library/grain@sha256:abc", "grain:main@sha256:abc"},
		{"localhost:5000/grain:main", "localhost:5000/grain@sha256:abc", "localhost:5000/grain:main@sha256:abc"},
		// An image's own ID, not a registry digest.
		{"example/grain:main", "sha256:abc", ""},
		{"example/grain:main", "docker://sha256:abc", ""},
		// Some other repository's digest says nothing about this one.
		{"ghcr.io/bwsalmon/grain/grain:main", "docker.io/bwsalmon/grain/grain@sha256:abc", ""},
		{"example/grain:main", "", ""},
		{"example/grain:main", "example/grain@sha256:", ""},
	} {
		got, err := kubePin(tc.ref, tc.imageID)
		if tc.want == "" {
			if err == nil {
				t.Errorf("kubePin(%q, %q) = %q, want a refusal", tc.ref, tc.imageID, got)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("kubePin(%q, %q) = %q, %v; want %q", tc.ref, tc.imageID, got, err, tc.want)
		}
	}
}

func TestStageKubeLeavesTheDeploymentAloneWhenTheCheckFails(t *testing.T) {
	// The whole reason the check runs before the spec is touched: with
	// strategy Recreate, an image that cannot start is an outage rather
	// than a rollout that stalls with the old pod still serving.
	kubectl, calls, _ := fakeKubectl(t, "Failed|False||Error: exec format error", "boom")
	u := kubeUpgrader(t, kubectl, []string{"schema-version"})
	p := newProgress(u.cfg.StatusFile, "main", Status{}, u.plan())
	defer p.stop()

	_, err := u.stageKube(context.Background(), p, "main")
	if err == nil {
		t.Fatal("stageKube succeeded against an image whose check pod failed")
	}
	if !strings.Contains(err.Error(), "already runs") {
		t.Errorf("error = %q, want it to say this deployment is unchanged", err)
	}
	if strings.Contains(calls(), "set image") {
		t.Error("the deployment was repointed despite a failed health check")
	}
}

// The check grain/task-244 added: before the rollout, ask the image the
// cluster has just proved it can run whether it accepts the args the
// Deployment hands it. Those args were applied once, by hand, and the
// Upgrade button rewrites only the image in the same spec -- so this is
// the only moment the two can be compared while the old pod is still
// serving.

// kubeFlagsUpgrader is kubeUpgrader with the flag check configured: the
// args this deployment is running with, and the question to put to the
// candidate build.
func kubeFlagsUpgrader(t *testing.T, kubectl string, unitFlags []string) *Upgrader {
	t.Helper()
	u := kubeUpgrader(t, kubectl, []string{"schema-version"})
	u.cfg.Kube.UnitFlags = unitFlags
	u.cfg.Kube.FlagNamesArgs = []string{"daemon", "-flag-names"}
	return u
}

// stageKubeStatus runs one staging and hands back what it reported --
// the Detail line, and the status file the pane reads.
func stageKubeStatus(t *testing.T, u *Upgrader, branch string) (string, Status) {
	t.Helper()
	p := newProgress(u.cfg.StatusFile, branch, Status{}, u.plan())
	detail, err := u.stageKube(context.Background(), p, branch)
	if err != nil {
		t.Fatalf("stageKube: %v", err)
	}
	p.stop()
	status, err := readStatus(u.cfg.StatusFile)
	if err != nil {
		t.Fatalf("reading the status file: %v", err)
	}
	return detail, status
}

func TestStageKubeNamesTheArgsTheNewImageWillIgnore(t *testing.T) {
	kubectl, calls, _ := fakeKubectlAnswering(t, kubeSucceeded, "",
		"data-dir value\nk8s-sandboxes bool\nk8s-namespace value\n")
	u := kubeFlagsUpgrader(t, kubectl, []string{"-data-dir", "-k8s-namespace", "-k8s-sandboxes", "-reboot-cmd"})

	detail, status := stageKubeStatus(t, u, "main")

	step := stepDetail(t, status, stepFlags)
	if !strings.Contains(step, "-reboot-cmd") {
		t.Errorf("%s step = %q, want it to name the arg the new build does not define", stepFlags, step)
	}
	if strings.Contains(step, "-data-dir") {
		t.Errorf("%s step = %q, want only the args that are a problem in it", stepFlags, step)
	}
	// The fix is an apply against the Deployment, not a script on a host
	// this deployment does not have.
	if !strings.Contains(step, "deployment/grain") {
		t.Errorf("%s step = %q, want the spec that passes it named", stepFlags, step)
	}
	if !strings.Contains(detail, "-reboot-cmd") {
		t.Errorf("detail = %q, want the disagreement carried into the upgrade's own status", detail)
	}
	// Reported, not refused: the upgrade that removes a flag is the one
	// every already-applied Deployment needs.
	if !strings.Contains(calls(), "set image deployment/grain") {
		t.Errorf("calls = %q, want the rollout to have happened anyway", calls())
	}
}

func TestStageKubeSaysSoWhenTheNewImageTakesEveryArg(t *testing.T) {
	kubectl, _, _ := fakeKubectlAnswering(t, kubeSucceeded, "", "data-dir value\nk8s-sandboxes bool\n")
	u := kubeFlagsUpgrader(t, kubectl, []string{"-data-dir", "-k8s-sandboxes"})

	detail, status := stageKubeStatus(t, u, "main")

	if got := stepDetail(t, status, stepFlags); !strings.Contains(got, "accepts all 2 flags") {
		t.Errorf("%s step = %q, want it to say the spec and the image agree", stepFlags, got)
	}
	if strings.Contains(detail, "ignore") {
		t.Errorf("detail = %q, want nothing about ignored args when there are none", detail)
	}
}

func TestStageKubeDoesNotReadAUsageDumpAsAnAnswer(t *testing.T) {
	// A build older than the question answers with a usage error and its
	// whole flag dump, and on this path stderr and stdout arrive in the
	// same `kubectl logs` stream. Read loosely, those lines are a flag
	// list -- which would report "everything is fine" about a build that
	// never answered. Only "not checked" is honest here.
	kubectl, calls, _ := fakeKubectlAnswering(t, kubeSucceeded, "",
		"flag provided but not defined: -flag-names\nUsage of daemon:\n  -data-dir string\n    \tthe directory\n")
	u := kubeFlagsUpgrader(t, kubectl, []string{"-data-dir", "-reboot-cmd"})

	detail, status := stageKubeStatus(t, u, "main")

	step := stepDetail(t, status, stepFlags)
	if !strings.Contains(step, "not checked") {
		t.Errorf("%s step = %q, want it to say the question could not be answered", stepFlags, step)
	}
	if strings.Contains(detail, "ignore") {
		t.Errorf("detail = %q, want no claim about args from an answer that never came", detail)
	}
	// And an unanswerable question is not a reason to strand a rollback
	// on the build it is rolling back from.
	if !strings.Contains(calls(), "set image deployment/grain") {
		t.Errorf("calls = %q, want the rollout to have happened anyway", calls())
	}
}

func TestStageKubeSkipsTheFlagCheckWhenItIsNotConfigured(t *testing.T) {
	kubectl, _, _ := fakeKubectl(t, kubeSucceeded, "")
	u := kubeUpgrader(t, kubectl, []string{"schema-version"})

	_, status := stageKubeStatus(t, u, "main")

	for _, step := range status.Steps {
		if step.Name == stepFlags && step.State != StepSkipped {
			t.Errorf("%s step = %v, want it skipped", stepFlags, step.State)
		}
	}
}

func TestTheFlagCheckAsksFromAPodShapedLikeTheRolloutsOwn(t *testing.T) {
	// The rule grain/task-242 established for every pod that runs the
	// candidate grain image, applied to the pod this step adds: asking
	// the question as root would be asking a question the rollout, which
	// runs as uid 1000, never asks.
	kubectl, _, manifests := fakeKubectlAnswering(t, kubeSucceeded, "", "data-dir value\n")
	u := kubeFlagsUpgrader(t, kubectl, []string{"-data-dir"})

	stageKubeStatus(t, u, "main")

	var asked int
	for _, manifest := range manifests() {
		if !strings.Contains(manifest, kubeAskPod) {
			continue
		}
		asked++
		security, ok := podSpec(t, manifest)["securityContext"].(map[string]any)
		if !ok {
			t.Fatalf("the pod asking for the flag list has no securityContext: %s", manifest)
		}
		if security["runAsUser"] != float64(1000) || security["runAsNonRoot"] != true {
			t.Errorf("the pod asking for the flag list has securityContext %v, want the Deployment's own", security)
		}
	}
	if asked != 1 {
		t.Errorf("%d pods asked the flag question, want one", asked)
	}
}

func TestTheFlagStepSaysWhenTheClusterCouldNotPutTheQuestion(t *testing.T) {
	// The whole of that distinction, through the step an operator reads:
	// a cluster that would not create the pod is this deployment failing
	// to ask, and saying "a build older than the question cannot answer"
	// about it would send somebody looking at the wrong thing. It is
	// still not a reason to refuse the rollout.
	dir := t.TempDir()
	kubectl := filepath.Join(dir, "kubectl")
	log := filepath.Join(dir, "calls")
	script := "#!/bin/sh\n" +
		"printf '%s\\036' \"$*\" >> " + log + "\n" +
		"case \"$3 $4\" in\n" +
		"  'get deployment') printf '%s' '" + kubeDeploymentJSON + "' ;;\n" +
		"  'get '*) printf '%s' '" + kubeSucceeded + "' ;;\n" +
		// Only the pod that asks the question: the health check before it
		// has to get its own pod, or the upgrade never reaches this step.
		"  'apply '*) body=$(cat); case \"$body\" in *" + kubeAskPod + "*) " +
		"echo 'error: the server could not find the requested resource' >&2; exit 1 ;; esac ;;\n" +
		"esac\n" +
		"exit 0\n"
	if err := os.WriteFile(kubectl, []byte(script), 0o755); err != nil {
		t.Fatalf("writing the fake kubectl: %v", err)
	}
	u := kubeFlagsUpgrader(t, kubectl, []string{"-data-dir"})

	detail, status := stageKubeStatus(t, u, "main")

	step := stepDetail(t, status, stepFlags)
	if !strings.Contains(step, "never asked") {
		t.Errorf("%s step = %q, want it to say the question was never put", stepFlags, step)
	}
	if strings.Contains(step, "older than the question") {
		t.Errorf("%s step = %q, want a cluster that would not ask not reported as a build that would not answer", stepFlags, step)
	}
	if strings.Contains(detail, "ignore") {
		t.Errorf("detail = %q, want no claim about args from a question nobody put", detail)
	}
	recorded, _ := os.ReadFile(log)
	if !strings.Contains(string(recorded), "set image deployment/grain") {
		t.Errorf("calls = %q, want the rollout to have happened anyway", recorded)
	}
}

func TestAQuestionTheClusterNeverPutIsNotTheBuildsAnswer(t *testing.T) {
	// The distinction image.go's ranAndFailed draws for a `docker run`,
	// drawn here by where in ask the failure happened: a kubectl that
	// will not create the pod is this deployment failing to ask, not the
	// candidate failing to answer, and the flag step says so rather than
	// blaming a build that was never started.
	dir := t.TempDir()
	kubectl := filepath.Join(dir, "kubectl")
	script := "#!/bin/sh\ncase \"$3\" in apply) echo 'the server could not find the requested resource' >&2; exit 1 ;; esac\nexit 0\n"
	if err := os.WriteFile(kubectl, []byte(script), 0o755); err != nil {
		t.Fatalf("writing the fake kubectl: %v", err)
	}
	u := kubeUpgrader(t, kubectl, nil)

	_, err := kubeRunner{u: u, ref: "example/grain:main"}.ask(context.Background(), nil, []string{"daemon", "-flag-names"})
	if err == nil {
		t.Fatal("ask succeeded against a kubectl that refused to create the pod")
	}
	if !errors.Is(err, errCouldNotAsk) {
		t.Errorf("error = %q, want it marked as a question that was never put", err)
	}
}

func TestTheCheckPodRunsAsTheUidTheDeploymentDoes(t *testing.T) {
	// The question the check has to ask is the question the rollout will
	// ask. A candidate that only starts as root -- a base image whose
	// entrypoint writes somewhere root-owned, a CLI installed where only
	// root can read it -- has to fail here, where the Deployment is
	// still untouched, rather than after Recreate has stopped the pod
	// serving now.
	kubectl, calls, manifests := fakeKubectl(t, kubeSucceeded, "example/sandbox:main")
	u := kubeUpgrader(t, kubectl, []string{"schema-version"})
	u.cfg.Kube.SandboxImageArgs = []string{"k8s-sandbox-image"}
	p := newProgress(u.cfg.StatusFile, "main", Status{}, u.plan())
	defer p.stop()

	if _, err := u.stageKube(context.Background(), p, "main"); err != nil {
		t.Fatalf("stageKube: %v", err)
	}
	// The uid is read from the Deployment about to be repointed, not
	// configured a second time in this deployment's own flags.
	if !strings.Contains(calls(), "get deployment grain -o json") {
		t.Errorf("calls = %q, want the Deployment's security context read", calls())
	}

	applied := manifests()
	if len(applied) == 0 {
		t.Fatal("no pod was applied")
	}
	var sandboxChecks int
	for _, manifest := range applied {
		spec := podSpec(t, manifest)
		image := podImage(t, manifest)
		if image == "example/sandbox:main" {
			// The sandbox image is checked unconstrained, because that is
			// how a sandbox pod actually runs: it declares its own uid,
			// and pinning the daemon's would ask a question no sandbox
			// ever answers.
			sandboxChecks++
			if _, ok := spec["securityContext"]; ok {
				t.Errorf("the sandbox image's check pod was constrained to the daemon's security context: %v", spec["securityContext"])
			}
			continue
		}
		security, ok := spec["securityContext"].(map[string]any)
		if !ok {
			t.Fatalf("the pod running %s has no securityContext: it would prove the image starts as root, which is not what will run it", image)
		}
		if security["runAsUser"] != float64(1000) || security["runAsNonRoot"] != true {
			t.Errorf("pod running %s has securityContext %v, want the Deployment's own", image, security)
		}
		container := spec["containers"].([]any)[0].(map[string]any)
		containerSecurity, ok := container["securityContext"].(map[string]any)
		if !ok || containerSecurity["allowPrivilegeEscalation"] != false {
			t.Errorf("pod running %s has container securityContext %v, want the Deployment container's own", image, container["securityContext"])
		}
	}
	if sandboxChecks != 1 {
		t.Errorf("the sandbox image was checked %d times, want once", sandboxChecks)
	}
	// The health check runs the tag -- it is what reports the digest --
	// and every pod after it runs exactly the image the check ran.
	for i, manifest := range applied {
		image := podImage(t, manifest)
		if image == "example/sandbox:main" {
			continue
		}
		want := "example/grain:main@" + kubeDigest
		if i == 0 {
			want = "example/grain:main"
		}
		if image != want {
			t.Errorf("pod %d ran %s, want %s", i, image, want)
		}
	}
}

func TestStageKubeStopsIfItCannotReadWhatTheDeploymentRunsAs(t *testing.T) {
	// Before the Deployment is touched, so the cluster goes on running
	// what it already runs -- and loudly, rather than quietly checking
	// the candidate as root.
	kubectl, calls, _ := fakeKubectl(t, kubeSucceeded, "")
	u := kubeUpgrader(t, kubectl, []string{"schema-version"})
	u.cfg.Kube.Container = "nosuchcontainer"
	p := newProgress(u.cfg.StatusFile, "main", Status{}, u.plan())
	defer p.stop()

	_, err := u.stageKube(context.Background(), p, "main")
	if err == nil {
		t.Fatal("stageKube succeeded without knowing what the Deployment runs as")
	}
	if !strings.Contains(err.Error(), "security context") {
		t.Errorf("error = %q, want it to name what could not be read", err)
	}
	if strings.Contains(calls(), "set image") {
		t.Error("the deployment was repointed anyway")
	}
}

func TestKubeRunnerRefusesToRehearseAMigration(t *testing.T) {
	// A rehearsal needs the new build to read this deployment's export
	// directory, which is on a claim only this pod can mount. Refusing
	// is what keeps a deployment from cutting over on a rehearsal that
	// never ran.
	u := kubeUpgrader(t, "/nonexistent", nil)
	err := kubeRunner{u: u, ref: "example/grain:main"}.rehearse(context.Background(), nil, "/data/schema-upgrade", nil)
	if err == nil {
		t.Fatal("kubeRunner.rehearse succeeded")
	}
	if !strings.Contains(err.Error(), "/data/schema-upgrade") {
		t.Errorf("error = %q, want it to name the directory it cannot hand over", err)
	}
}

func TestKubeUpgradeDoesNotRollOutWhenImageHasNotChanged(t *testing.T) {
	fake, calls, _ := fakeKubectl(t, kubeSucceeded, "")
	kubectl := kubectlAround(t, fake, "example/grain:main@"+kubeDigest, 0)
	u := kubeUpgrader(t, kubectl, []string{"schema-version"})
	var held, released bool
	u.cfg.Hold = func(string) (func(), error) {
		held = true
		return func() { released = true }, nil
	}
	if err := u.Start("main"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	status := waitForPhase(t, u, PhaseOK)
	waitForIdle(t, u)
	if !status.Unchanged {
		t.Errorf("status.Unchanged = false, want true (status=%+v)", status)
	}
	if !strings.Contains(status.Detail, "the version on main has not changed") {
		t.Errorf("status.Detail = %q, want it to say the version on main has not changed", status.Detail)
	}
	if strings.Contains(calls(), "set image") {
		t.Errorf("calls = %q, want no rollout when the Deployment already runs the image", calls())
	}
	if !held || !released {
		t.Errorf("hold: held=%v released=%v, want the queue hold taken and released since no rollout starts", held, released)
	}
}
