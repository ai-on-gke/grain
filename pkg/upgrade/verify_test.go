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

package upgrade

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// cosignPayload is one line of what `cosign verify` prints on stdout for
// a signature it accepted: the simple-signing payload, whose
// critical.image names the manifest digest that was signed.
func cosignPayload(repo, digest string) string {
	return `{"critical":{"identity":{"docker-reference":"` + repo + `"},"image":{"docker-manifest-digest":"` + digest + `"},"type":"cosign container image signature"},"optional":null}`
}

// fakeVerifyCmd stands in for `cosign verify --key ...`: it appends the
// ref it was handed to log, as one line, and exits with status. It
// prints nothing on stdout, so it is the shape of a verifier that only
// reports by its exit status -- which the image path, holding a digest
// already, accepts.
func fakeVerifyCmd(log string, status int) []string {
	return []string{"sh", "-c", `printf 'verify %s\n' "$1" >> ` + log + `; echo "cosign: talking about itself" >&2; exit ` + strconv.Itoa(status), "sh"}
}

// TestUpgraderImageVerifiesThePinnedDigestBeforeRunningIt: the verifier
// is asked about the digest the pull fetched -- never the tag, which can
// move -- and is asked before the health check, which is the first thing
// to run the image. The sandbox image the new build names is verified
// by its own digest before the pull of it counts.
func TestUpgraderImageVerifiesThePinnedDigestBeforeRunningIt(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "log")
	refFile := filepath.Join(dir, "image.env")
	u := newUpgrader(t, Config{
		Image: &ImageConfig{
			Repository: "ghcr.io/bwsalmon/grain/grain",
			RefFile:    refFile,
			PullCmd:    []string{"sh", "-c", `printf 'pull %s\n' "$1" >> ` + log, "sh"},
			RunCmd: []string{"sh", "-c", `printf 'run %s\n' "$1" >> ` + log + `;` +
				`case "$2" in sandbox-image) echo ghcr.io/bwsalmon/grain/guest:sha-abc1234;; *) echo 7;; esac`, "sh"},
			SandboxImageArgs: []string{"sandbox-image"},
			VerifyCmd:        fakeVerifyCmd(log, 0),
		},
		HealthCheckArgs: []string{"schema-version"},
		StatusFile:      filepath.Join(dir, "upgrade-status.json"),
	})
	if err := u.Start("main"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	status := waitForPhase(t, u, PhaseOK)

	const tag = "ghcr.io/bwsalmon/grain/grain:main"
	want := strings.Join([]string{
		"pull " + tag,
		"verify ghcr.io/bwsalmon/grain/grain@" + testDigest,
		"run " + tag + "@" + testDigest, // the health check
		"run " + tag + "@" + testDigest, // the sandbox question
		"pull ghcr.io/bwsalmon/grain/guest:sha-abc1234",
		"verify ghcr.io/bwsalmon/grain/guest@" + testDigest,
	}, "\n") + "\n"
	if got := readFile(t, log); got != want {
		t.Errorf("commands run, in order:\n%s\nwant:\n%s", got, want)
	}
	if got := stepState(t, status, stepVerify); got != StepDone {
		t.Errorf("signature step = %q, want %q", got, StepDone)
	}
	if got, want := readFile(t, refFile), "GRAIN_IMAGE="+tag+"@"+testDigest+"\n"; got != want {
		t.Errorf("ref file = %q, want %q", got, want)
	}
}

// TestUpgraderImageRefusesAnImageWhoseSignatureDoesNotVerify is the
// point of the step: an image nobody with the release key signed is
// never run, not even as a health check, and the deployment stays on
// what it already runs.
func TestUpgraderImageRefusesAnImageWhoseSignatureDoesNotVerify(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "log")
	refFile := filepath.Join(dir, "image.env")
	writeTestFile(t, refFile, "GRAIN_IMAGE=ghcr.io/bwsalmon/grain/grain:old\n")
	u := newUpgrader(t, Config{
		Image: &ImageConfig{
			Repository: "ghcr.io/bwsalmon/grain/grain",
			RefFile:    refFile,
			PullCmd:    []string{"true"},
			RunCmd:     []string{"sh", "-c", `printf 'run %s\n' "$1" >> ` + log, "sh"},
			VerifyCmd:  fakeVerifyCmd(log, 1),
		},
		HealthCheckArgs: []string{"schema-version"},
		StatusFile:      filepath.Join(dir, "upgrade-status.json"),
	})
	if err := u.Start("main"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	status := waitForPhase(t, u, PhaseFailed)

	if got := readFile(t, log); strings.Contains(got, "run ") {
		t.Errorf("the unverified image was run anyway:\n%s", got)
	}
	if got := stepState(t, status, stepVerify); got != StepFailed {
		t.Errorf("signature step = %q, want %q", got, StepFailed)
	}
	if got := stepState(t, status, stepHealthCheck); got != StepPending {
		t.Errorf("health check step = %q, want it never started", got)
	}
	if !strings.Contains(status.Detail, "talking about itself") {
		t.Errorf("status detail = %q, want the verifier's own complaint in it", status.Detail)
	}
	if got := readFile(t, refFile); got != "GRAIN_IMAGE=ghcr.io/bwsalmon/grain/grain:old\n" {
		t.Errorf("ref file = %q, want it untouched", got)
	}
}

// TestUpgraderImageRefusesAVerifierThatVerifiedSomethingElse: a verifier
// that says which digest it verified is held to it. One that verified a
// different digest -- a command that ignores its argument, say -- has
// not verified this image.
func TestUpgraderImageRefusesAVerifierThatVerifiedSomethingElse(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "log")
	u := newUpgrader(t, Config{
		Image: &ImageConfig{
			Repository: "ghcr.io/bwsalmon/grain/grain",
			RefFile:    filepath.Join(dir, "image.env"),
			PullCmd:    []string{"true"},
			RunCmd:     []string{"sh", "-c", `printf 'run %s\n' "$1" >> ` + log, "sh"},
			VerifyCmd: []string{"sh", "-c", `printf '%s\n' '` +
				cosignPayload("ghcr.io/bwsalmon/grain/grain", "sha256:"+strings.Repeat("f", 64)) + `'`, "sh"},
		},
		HealthCheckArgs: []string{"schema-version"},
		StatusFile:      filepath.Join(dir, "upgrade-status.json"),
	})
	if err := u.Start("main"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	status := waitForPhase(t, u, PhaseFailed)
	if !strings.Contains(status.Detail, "not for the digest it was asked about") {
		t.Errorf("status detail = %q, want it to say the verifier verified another digest", status.Detail)
	}
	if got, _ := os.ReadFile(log); len(got) != 0 {
		t.Errorf("the image was run anyway:\n%s", got)
	}
}

// TestUpgraderImageRefusesAnUnsignedSandboxImage: the guest is run by
// every task this deployment dispatches, so an unsigned one stops the
// upgrade exactly as an unsigned grain image does -- before the ref file
// names a build whose guest nobody verified.
func TestUpgraderImageRefusesAnUnsignedSandboxImage(t *testing.T) {
	dir := t.TempDir()
	refFile := filepath.Join(dir, "image.env")
	writeTestFile(t, refFile, "GRAIN_IMAGE=ghcr.io/bwsalmon/grain/grain:old\n")
	u := newUpgrader(t, Config{
		Image: &ImageConfig{
			Repository: "ghcr.io/bwsalmon/grain/grain",
			RefFile:    refFile,
			PullCmd:    []string{"true"},
			RunCmd: []string{"sh", "-c",
				`case "$2" in sandbox-image) echo ghcr.io/bwsalmon/grain/guest:sha-abc1234;; *) echo 7;; esac`, "sh"},
			SandboxImageArgs: []string{"sandbox-image"},
			VerifyCmd:        []string{"sh", "-c", `case "$1" in *guest*) echo "no signatures found" >&2; exit 1;; esac`, "sh"},
		},
		HealthCheckArgs: []string{"schema-version"},
		StatusFile:      filepath.Join(dir, "upgrade-status.json"),
	})
	if err := u.Start("main"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	status := waitForPhase(t, u, PhaseFailed)
	if got := stepState(t, status, stepSandbox); got != StepFailed {
		t.Errorf("sandbox step = %q, want %q", got, StepFailed)
	}
	if !strings.Contains(status.Detail, "no signatures found") {
		t.Errorf("status detail = %q, want the verifier's complaint", status.Detail)
	}
	if got := readFile(t, refFile); got != "GRAIN_IMAGE=ghcr.io/bwsalmon/grain/grain:old\n" {
		t.Errorf("ref file = %q, want it untouched", got)
	}
}

// TestUpgraderImageWarnsWhenNothingVerifiesSignatures: with no verifier
// configured the upgrade goes ahead -- nothing published before signing
// began is signed, and refusing it would strand every deployment -- but
// the step says, in words, that what is about to run was not verified.
func TestUpgraderImageWarnsWhenNothingVerifiesSignatures(t *testing.T) {
	dir := t.TempDir()
	u := newUpgrader(t, Config{
		Image: &ImageConfig{
			Repository: "ghcr.io/bwsalmon/grain/grain",
			RefFile:    filepath.Join(dir, "image.env"),
			PullCmd:    []string{"true"},
			RunCmd:     []string{"true"},
		},
		HealthCheckArgs: []string{"schema-version"},
		StatusFile:      filepath.Join(dir, "upgrade-status.json"),
	})
	if err := u.Start("main"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	status := waitForPhase(t, u, PhaseOK)
	if got := stepState(t, status, stepVerify); got != StepSkipped {
		t.Errorf("signature step = %q, want %q", got, StepSkipped)
	}
	if got := stepDetail(t, status, stepVerify); !strings.Contains(got, "no signature verification configured") {
		t.Errorf("signature step detail = %q, want it to say nothing was verified", got)
	}
}

// TestStageKubeRunsTheCheckPodFromTheVerifiedDigest: on the Kubernetes
// path the first thing to fetch the image is a pod that runs it, so the
// verifier is asked about the tag, says which digest it verified, and
// every pod -- the health check's included -- runs that digest.
func TestStageKubeRunsTheCheckPodFromTheVerifiedDigest(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "verified")
	const sandboxDigest = "sha256:5a4db0c5"
	kubectl, _, manifests := fakeKubectl(t, kubeSucceeded, "example/sandbox:main")
	u := kubeUpgrader(t, kubectl, []string{"schema-version"})
	u.cfg.Kube.SandboxImageArgs = []string{"k8s-sandbox-image"}
	u.cfg.Kube.VerifyCmd = []string{"sh", "-c", `printf '%s\n' "$1" >> ` + log + `; case "$1" in ` +
		`*sandbox*) printf '[%s]\n' '` + cosignPayload("example/sandbox", sandboxDigest) + `';; ` +
		`*) printf '[%s]\n' '` + cosignPayload("example/grain", kubeDigest) + `';; esac`, "sh"}
	p := newProgress(u.cfg.StatusFile, "main", Status{}, u.plan())
	defer p.stop()

	detail, err := u.stageKube(context.Background(), p, "main")
	if err != nil {
		t.Fatalf("stageKube: %v", err)
	}
	if got, want := readFile(t, log), "example/grain:main\nexample/sandbox:main\n"; got != want {
		t.Errorf("verified %q, want %q", got, want)
	}
	applied := manifests()
	if len(applied) == 0 {
		t.Fatal("no pod was applied")
	}
	if got, want := podImage(t, applied[0]), "example/grain:main@"+kubeDigest; got != want {
		t.Errorf("the health check's pod ran %s, want the verified %s", got, want)
	}
	var sawSandbox bool
	for _, manifest := range applied {
		if image := podImage(t, manifest); strings.Contains(image, "sandbox") {
			sawSandbox = true
			if image != "example/sandbox:main@"+sandboxDigest {
				t.Errorf("the sandbox check pod ran %s, want the verified digest", image)
			}
		}
	}
	if !sawSandbox {
		t.Error("no pod ran the sandbox image")
	}
	if !strings.Contains(detail, "example/grain:main@"+kubeDigest) {
		t.Errorf("detail = %q, want the verified digest rolled out", detail)
	}
}

// TestStageKubeRefusesBeforeAnyPodRuns: a verifier that refuses -- or
// one that exits 0 without saying what it verified, which leaves the
// pod free to pull whatever the tag names by then -- stops the upgrade
// before anything is created in the cluster.
func TestStageKubeRefusesBeforeAnyPodRuns(t *testing.T) {
	for name, verify := range map[string][]string{
		"refused":         {"sh", "-c", `echo "no matching signatures" >&2; exit 1`, "sh"},
		"names no digest": {"true"},
	} {
		t.Run(name, func(t *testing.T) {
			kubectl, calls, manifests := fakeKubectl(t, kubeSucceeded, "")
			u := kubeUpgrader(t, kubectl, []string{"schema-version"})
			u.cfg.Kube.VerifyCmd = verify
			p := newProgress(u.cfg.StatusFile, "main", Status{}, u.plan())
			defer p.stop()

			if _, err := u.stageKube(context.Background(), p, "main"); err == nil {
				t.Fatal("stageKube succeeded on an image whose signature did not verify")
			}
			if got := manifests(); len(got) != 0 {
				t.Errorf("%d pods were created for an unverified image", len(got))
			}
			if strings.Contains(calls(), "set image") {
				t.Errorf("the Deployment was repointed: %q", calls())
			}
		})
	}
}

func TestVerifiedDigestsReadsCosignOutput(t *testing.T) {
	a, b := "sha256:aaaa", "sha256:bbbb"
	for out, want := range map[string]string{
		"[" + cosignPayload("r", a) + "," + cosignPayload("r", a) + "]":     a,
		cosignPayload("r", a) + "\n" + cosignPayload("r", b) + "\n":         a + "," + b,
		"Verification for r:main --\nThe following checks were performed\n": "",
		"": "",
	} {
		if got := strings.Join(verifiedDigests(out), ","); got != want {
			t.Errorf("verifiedDigests(%q) = %q, want %q", out, got, want)
		}
	}
}

// stepState is the state a status reports for the step called name.
func stepState(t *testing.T, status Status, name string) StepState {
	t.Helper()
	for _, step := range status.Steps {
		if step.Name == name {
			return step.State
		}
	}
	t.Fatalf("no %q step in %+v", name, status.Steps)
	return ""
}

// pinnedSandboxDigest is the digest a release stamps its sandbox
// reference with (scripts/release-images.sh), different from testDigest
// so that a test can tell which of the two an upgrade checked.
const pinnedSandboxDigest = "sha256:fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210"

// TestUpgraderImageVerifiesAPinnedSandboxImageAsItIsNamed: a release
// build names its sandbox by digest, and that digest is the one pulled,
// the one verified, and the one every VM is created from afterwards --
// nothing is appended to a reference that already says what it means.
func TestUpgraderImageVerifiesAPinnedSandboxImageAsItIsNamed(t *testing.T) {
	const sandbox = "ghcr.io/bwsalmon/grain/guest:sha-abc1234@" + pinnedSandboxDigest
	dir := t.TempDir()
	log := filepath.Join(dir, "log")
	u := newUpgrader(t, Config{
		Image: &ImageConfig{
			Repository: "ghcr.io/bwsalmon/grain/grain",
			RefFile:    filepath.Join(dir, "image.env"),
			PullCmd:    []string{"sh", "-c", `printf 'pull %s\n' "$1" >> ` + log, "sh"},
			DigestCmd: []string{"sh", "-c", `printf '%s\n%s\n' ` +
				`ghcr.io/bwsalmon/grain/grain@` + testDigest + ` ghcr.io/bwsalmon/grain/guest@` + pinnedSandboxDigest, "sh"},
			RunCmd: []string{"sh", "-c",
				`case "$2" in sandbox-image) echo ` + sandbox + `;; *) echo 7;; esac`, "sh"},
			SandboxImageArgs: []string{"sandbox-image"},
			VerifyCmd:        fakeVerifyCmd(log, 0),
		},
		HealthCheckArgs: []string{"schema-version"},
		StatusFile:      filepath.Join(dir, "upgrade-status.json"),
	})
	if err := u.Start("main"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	status := waitForPhase(t, u, PhaseOK)

	const tag = "ghcr.io/bwsalmon/grain/grain:main"
	want := strings.Join([]string{
		"pull " + tag,
		"verify ghcr.io/bwsalmon/grain/grain@" + testDigest,
		"pull " + sandbox,
		"verify ghcr.io/bwsalmon/grain/guest@" + pinnedSandboxDigest,
	}, "\n") + "\n"
	if got := readFile(t, log); got != want {
		t.Errorf("commands run, in order:\n%s\nwant:\n%s", got, want)
	}
	if got := stepDetail(t, status, stepSandbox); got != "pulled "+sandbox {
		t.Errorf("sandbox step detail = %q, want it to name the pinned reference unchanged", got)
	}
}

// TestUpgraderImageRefusesAPinnedSandboxImageItDoesNotHold: a pinned
// reference whose digest is not what this host holds after the pull is
// not one anything can be verified against -- whatever the pull left
// behind is some other image.
func TestUpgraderImageRefusesAPinnedSandboxImageItDoesNotHold(t *testing.T) {
	const sandbox = "ghcr.io/bwsalmon/grain/guest:sha-abc1234@" + pinnedSandboxDigest
	dir := t.TempDir()
	refFile := filepath.Join(dir, "image.env")
	writeTestFile(t, refFile, "GRAIN_IMAGE=ghcr.io/bwsalmon/grain/grain:old\n")
	u := newUpgrader(t, Config{
		Image: &ImageConfig{
			Repository: "ghcr.io/bwsalmon/grain/grain",
			RefFile:    refFile,
			PullCmd:    []string{"true"},
			DigestCmd: []string{"sh", "-c", `printf '%s\n%s\n' ` +
				`ghcr.io/bwsalmon/grain/grain@` + testDigest + ` ghcr.io/bwsalmon/grain/guest@` + testDigest, "sh"},
			RunCmd: []string{"sh", "-c",
				`case "$2" in sandbox-image) echo ` + sandbox + `;; *) echo 7;; esac`, "sh"},
			SandboxImageArgs: []string{"sandbox-image"},
			VerifyCmd:        []string{"true"},
		},
		HealthCheckArgs: []string{"schema-version"},
		StatusFile:      filepath.Join(dir, "upgrade-status.json"),
	})
	if err := u.Start("main"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	status := waitForPhase(t, u, PhaseFailed)
	if got := stepState(t, status, stepSandbox); got != StepFailed {
		t.Errorf("sandbox step = %q, want %q", got, StepFailed)
	}
	if !strings.Contains(status.Detail, "not on this host by that digest") {
		t.Errorf("status detail = %q, want it to say the digest is not here", status.Detail)
	}
	if got := readFile(t, refFile); got != "GRAIN_IMAGE=ghcr.io/bwsalmon/grain/grain:old\n" {
		t.Errorf("ref file = %q, want it untouched", got)
	}
}

// TestStageKubeVerifiesAPinnedSandboxImageByItsOwnDigest: on the
// Kubernetes path a pinned sandbox reference is verified by the digest
// it names, and the check pod runs exactly that reference rather than
// one with a second digest appended.
func TestStageKubeVerifiesAPinnedSandboxImageByItsOwnDigest(t *testing.T) {
	const sandbox = "example/sandbox:sha-abc1234@" + pinnedSandboxDigest
	dir := t.TempDir()
	log := filepath.Join(dir, "verified")
	kubectl, _, manifests := fakeKubectl(t, kubeSucceeded, sandbox)
	u := kubeUpgrader(t, kubectl, []string{"schema-version"})
	u.cfg.Kube.SandboxImageArgs = []string{"k8s-sandbox-image"}
	u.cfg.Kube.VerifyCmd = []string{"sh", "-c", `printf '%s\n' "$1" >> ` + log + `; case "$1" in ` +
		`*sandbox*) printf '[%s]\n' '` + cosignPayload("example/sandbox", pinnedSandboxDigest) + `';; ` +
		`*) printf '[%s]\n' '` + cosignPayload("example/grain", kubeDigest) + `';; esac`, "sh"}
	p := newProgress(u.cfg.StatusFile, "main", Status{}, u.plan())
	defer p.stop()

	if _, err := u.stageKube(context.Background(), p, "main"); err != nil {
		t.Fatalf("stageKube: %v", err)
	}
	if got, want := readFile(t, log), "example/grain:main\nexample/sandbox@"+pinnedSandboxDigest+"\n"; got != want {
		t.Errorf("verified %q, want %q", got, want)
	}
	var sawSandbox bool
	for _, manifest := range manifests() {
		if image := podImage(t, manifest); strings.Contains(image, "sandbox") {
			sawSandbox = true
			if image != sandbox {
				t.Errorf("the sandbox check pod ran %s, want the pinned %s", image, sandbox)
			}
		}
	}
	if !sawSandbox {
		t.Error("no pod ran the sandbox image")
	}
}
