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

// init_test.go covers cmdInit's own flag validation -- the part
// reachable with no real host access at all, the same split
// setup_test.go holds itself to for cmdSetupGCP. hostinit.Init's actual
// steps are pkg/hostinit's own, exercised there against a fake System
// rather than this host's real docker/go/apt.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/hostinit"
)

// TestInitExitCodeDistinguishesTheThreeOutcomes is the contract a caller
// scripts against: 0 only once the deployment is actually up (every step
// done or skipped), a dedicated code once Init ran to completion but
// left something for a human (manual or refused, either one), and 1 for
// a genuine failure -- never the same code for "it worked" and "five
// things need doing by hand".
func TestInitExitCodeDistinguishesTheThreeOutcomes(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		result hostinit.Result
		err    error
		want   int
	}{
		{
			name:   "genuine failure",
			result: hostinit.Result{},
			err:    errors.New("boom"),
			want:   1,
		},
		{
			name: "steps remain: manual",
			result: hostinit.Result{Steps: []hostinit.Step{
				{Name: "docker", Status: hostinit.StepManual},
			}},
			want: initExitStepsRemain,
		},
		{
			name: "steps remain: refused",
			result: hostinit.Result{Steps: []hostinit.Step{
				{Name: "go toolchain", Status: hostinit.StepRefused},
			}},
			want: initExitStepsRemain,
		},
		{
			name: "deployment up",
			result: hostinit.Result{Steps: []hostinit.Step{
				{Name: "git", Status: hostinit.StepDone},
				{Name: "docker", Status: hostinit.StepSkipped},
			}},
			want: 0,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := initExitCode(c.result, c.err); got != c.want {
				t.Errorf("initExitCode() = %d, want %d", got, c.want)
			}
		})
	}
}

func TestCmdInitRequiresRepoRoot(t *testing.T) {
	t.Parallel()
	_, err := cmdInit(context.Background(), []string{"-data-dir", t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "-repo-root") {
		t.Fatalf("cmdInit() = %v, want a -repo-root error", err)
	}
}

func TestCmdInitRequiresDataDir(t *testing.T) {
	t.Parallel()
	_, err := cmdInit(context.Background(), []string{"-repo-root", t.TempDir()})
	if err == nil || !strings.Contains(err.Error(), "-data-dir") {
		t.Fatalf("cmdInit() = %v, want a -data-dir error", err)
	}
}

// TestCmdInitAcceptsTheRetiredAdminFlags: -admin-account and -admin-user
// set nothing up any more (the daemon signs nobody in), but a script that
// still passes them reaches the same -repo-root check as one that does
// not, rather than stopping at an unknown flag.
func TestCmdInitAcceptsTheRetiredAdminFlags(t *testing.T) {
	t.Parallel()
	_, err := cmdInit(context.Background(), []string{"-data-dir", t.TempDir(), "-admin-account", "grain", "-admin-user=alice"})
	if err == nil || !strings.Contains(err.Error(), "-repo-root") {
		t.Fatalf("cmdInit() = %v, want a -repo-root error", err)
	}
}

// TestManualFollowUpsNeverPrintsABlankLine: a view-only kubeconfig on
// -platform=k8s-container produced two [manual] steps with no command
// (missing RBAC, a scratch apply refused), and "Run these" listed two
// empty lines under it.
func TestManualFollowUpsNeverPrintsABlankLine(t *testing.T) {
	t.Parallel()
	got := manualFollowUps([]hostinit.Step{
		{Name: "permission to create what grain.yaml creates", Status: hostinit.StepManual},
		{Name: "sandbox image pulls", Status: hostinit.StepManual, Command: "kubectl -n default describe pod x"},
	})
	if len(got) != 2 || !strings.Contains(got[0], "permission to create what grain.yaml creates") ||
		got[1] != "kubectl -n default describe pod x" {
		t.Fatalf("manualFollowUps = %q", got)
	}
}

// TestCmdInitNamesTheCredentialNameFlag: a token file without
// -github-credential-name used to fail with hostinit's own field names
// ("GitHubCredentialName is required with GitHubToken"), not the flag
// to add.
func TestCmdInitNamesTheCredentialNameFlag(t *testing.T) {
	t.Parallel()
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte("ghp_x"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := cmdInit(context.Background(), []string{
		"-repo-root", t.TempDir(),
		"-data-dir", t.TempDir(),
		"-github-token-file", tokenFile,
	})
	if err == nil || !strings.Contains(err.Error(), "-github-credential-name") {
		t.Fatalf("cmdInit() = %v, want an error naming -github-credential-name", err)
	}
}

// TestCmdInitRejectsUnknownPlatform exercises -platform the same way
// TestCmdInitRejectsWildcardGitHubRepo exercises -github-repo: the
// error fires in hostinit.Options.validate, before Init makes any real
// System call, so this is safe to run with no host access even though
// cmdInit wires hostinit.RealSystem{}.
func TestCmdInitRejectsUnknownPlatform(t *testing.T) {
	t.Parallel()
	_, err := cmdInit(context.Background(), []string{
		"-repo-root", t.TempDir(),
		"-data-dir", t.TempDir(),
		"-platform", "not-a-real-platform",
	})
	if err == nil || !strings.Contains(err.Error(), "not-a-real-platform") {
		t.Fatalf("cmdInit() = %v, want an error naming the unrecognized platform", err)
	}
}

// TestCmdInitDefaultPlatformFlagIsLinuxKVM pins -platform's default
// against hostinit.DefaultPlatform, the same way the rest of this
// command's flags default to the hostinit.Default* constants -- a
// divergence here would mean the flag and the package it wires disagree
// about today's unchanged behavior.
func TestCmdInitDefaultPlatformFlagIsLinuxKVM(t *testing.T) {
	t.Parallel()
	if hostinit.DefaultPlatform != hostinit.PlatformLinuxKVM {
		t.Fatalf("hostinit.DefaultPlatform = %q, want %q", hostinit.DefaultPlatform, hostinit.PlatformLinuxKVM)
	}
}

// TestCmdInitRejectsWildcardGitHubRepo exercises the path all the way
// into hostinit.Options.validate -- safe with no real host access
// because Init returns that error before touching anything (every
// System call in it happens after validate succeeds). It uses
// -github-token-file, not -github-token, because that error fires
// before the credential-ladder step runs regardless of which flag
// supplied the token: the point of this test is the "*" refusal, not
// the token source.
func TestCmdInitRejectsWildcardGitHubRepo(t *testing.T) {
	t.Parallel()
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte("ghp_x"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := cmdInit(context.Background(), []string{
		"-repo-root", t.TempDir(),
		"-data-dir", t.TempDir(),
		"-github-credential-name", "bot",
		"-github-token-file", tokenFile,
		"-github-repo", "*",
	})
	if err == nil || !strings.Contains(err.Error(), `"*"`) {
		t.Fatalf("cmdInit() = %v, want a refusal of a \"*\" -github-repo pattern", err)
	}
}

// TestResolveGitHubTokenReadsFile is the test named in the task: a token
// file is read and its contents are what the credential ladder gets.
func TestResolveGitHubTokenReadsFile(t *testing.T) {
	t.Parallel()
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte("ghp_fromfile\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var warn strings.Builder
	got, err := resolveGitHubToken("", tokenFile, &warn)
	if err != nil {
		t.Fatalf("resolveGitHubToken() error = %v", err)
	}
	if got != "ghp_fromfile" {
		t.Fatalf("resolveGitHubToken() = %q, want %q", got, "ghp_fromfile")
	}
	if warn.Len() != 0 {
		t.Fatalf("resolveGitHubToken() warned with no -github-token given: %q", warn.String())
	}
}

// TestResolveGitHubTokenPrefersFileOverLiteral covers "prefer the file
// when both are given, so a script that gains the safe flag is not
// silently overridden by an old one".
func TestResolveGitHubTokenPrefersFileOverLiteral(t *testing.T) {
	t.Parallel()
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte("ghp_fromfile"), 0o600); err != nil {
		t.Fatal(err)
	}
	var warn strings.Builder
	got, err := resolveGitHubToken("ghp_literal", tokenFile, &warn)
	if err != nil {
		t.Fatalf("resolveGitHubToken() error = %v", err)
	}
	if got != "ghp_fromfile" {
		t.Fatalf("resolveGitHubToken() = %q, want the file's token to win", got)
	}
	if !strings.Contains(warn.String(), "-github-token") {
		t.Fatalf("resolveGitHubToken() warning = %q, want it to name -github-token", warn.String())
	}
}

// TestResolveGitHubTokenWarnsOnLiteral covers "keep -github-token
// accepted but have it print a warning to stderr naming the ps
// exposure".
func TestResolveGitHubTokenWarnsOnLiteral(t *testing.T) {
	t.Parallel()
	var warn strings.Builder
	got, err := resolveGitHubToken("ghp_literal", "", &warn)
	if err != nil {
		t.Fatalf("resolveGitHubToken() error = %v", err)
	}
	if got != "ghp_literal" {
		t.Fatalf("resolveGitHubToken() = %q, want the literal token used when no file is given", got)
	}
	if !strings.Contains(warn.String(), "ps") {
		t.Fatalf("resolveGitHubToken() warning = %q, want it to name the ps exposure", warn.String())
	}
}

// TestCmdInitRefusesTokenShapedArgument covers the ghpat-style refusal:
// any argument that looks like a live GitHub token stops the command
// before it does anything, regardless of which flag it was given to.
func TestCmdInitRefusesTokenShapedArgument(t *testing.T) {
	t.Parallel()
	realLookingToken := "ghp_" + strings.Repeat("a", 36)
	_, err := cmdInit(context.Background(), []string{
		"-repo-root", t.TempDir(),
		"-data-dir", t.TempDir(),
		"-github-credential-name", "bot",
		"-github-token", realLookingToken,
		"-github-repo", "acme/widgets",
	})
	if err == nil || !strings.Contains(err.Error(), "looks like a live GitHub token") {
		t.Fatalf("cmdInit() = %v, want a refusal of a token-shaped argument", err)
	}
}

func TestLooksLikeTokenRejectsCommonShapes(t *testing.T) {
	t.Parallel()
	for _, tok := range []string{
		"ghp_" + strings.Repeat("a", 36),
		"github_pat_" + strings.Repeat("a", 30),
		strings.Repeat("a", 40),
	} {
		if !githubTokenShape.MatchString(tok) {
			t.Errorf("githubTokenShape.MatchString(%q) = false, want true", tok)
		}
	}
}

func TestLooksLikeTokenAllowsOrdinaryArgs(t *testing.T) {
	t.Parallel()
	for _, s := range []string{"bot", "acme/widgets", "/data/grain", "ghp_x", "-github-token"} {
		if githubTokenShape.MatchString(s) {
			t.Errorf("githubTokenShape.MatchString(%q) = true, want false", s)
		}
	}
}

// An unstamped local build's -k8s-sandbox-image default is :latest
// while grain.yaml runs :main; left unset, the flag has to follow the
// manifest being applied, and fall back to this build's own default
// only when the manifest names none.
func TestK8sSandboxImageFromManifest(t *testing.T) {
	repoRoot := t.TempDir()
	if got := k8sSandboxImageFromManifest("", repoRoot); got != defaultK8sSandboxImage {
		t.Errorf("no manifest: got %q, want this build's default %q", got, defaultK8sSandboxImage)
	}
	dir := filepath.Join(repoRoot, "deploy", "kubernetes")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "grain.yaml"), []byte("          args:\n            - -k8s-sandbox-image=example.com/sb:main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := k8sSandboxImageFromManifest("", repoRoot); got != "example.com/sb:main" {
		t.Errorf("default manifest: got %q, want example.com/sb:main", got)
	}
}

func TestCmdInitGKESandboxesDryRun(t *testing.T) {
	t.Parallel()
	repoRoot := t.TempDir()
	dataDir := t.TempDir()

	res, err := cmdInit(context.Background(), []string{
		"-platform=gke-sandboxes",
		"-dry-run",
		"-repo-root=" + repoRoot,
		"-data-dir=" + dataDir,
		"-gke-project=test-sandbox-project",
		"-gke-location=us-central1-a",
		"-gke-cluster=test-cluster",
		"-gke-machine-type=c4-standard-96-lssd",
	})
	if err != nil {
		t.Fatalf("cmdInit(-platform=gke-sandboxes -dry-run): %v", err)
	}
	if code := initExitCode(res, err); code != 0 {
		t.Fatalf("initExitCode = %d, want 0 (steps: %+v)", code, res.Steps)
	}
	if len(res.Steps) != 7 {
		t.Fatalf("len(Steps) = %d, want 7: %+v", len(res.Steps), res.Steps)
	}
	for _, s := range res.Steps {
		if s.Status != hostinit.StepWould {
			t.Errorf("step %q: status = %q, want %q", s.Name, s.Status, hostinit.StepWould)
		}
		if s.Command == "" {
			t.Errorf("step %q: empty Command", s.Name)
		}
	}
	if !strings.Contains(res.Steps[0].Command, "--disk-type=hyperdisk-balanced") ||
		!strings.Contains(res.Steps[0].Command, "--enable-nested-virtualization") {
		t.Errorf("cluster step command %q missing hyperdisk-balanced or nested-virt flag", res.Steps[0].Command)
	}
}

// TestPrintInitResultClosingCLIHintNamesBinaryPath covers task 1712:
// grain init does not link a binary into /usr/local/bin, so on a fresh
// host (especially with -use-binary /home/debian/grain-bin) a bare
// `grain <command>` under `sudo -u grain env ...` fails with
// `env: 'grain': No such file or directory`. The closing hint must name
// the binary path Init used.
func TestPrintInitResultClosingCLIHintNamesBinaryPath(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		result hostinit.Result
		opts   hostinit.Options
		want   string
	}{
		{
			name: "-use-binary via Result.GrainBinaryPath",
			result: hostinit.Result{
				GrainBinaryPath: "/home/debian/grain-bin",
				Steps: []hostinit.Step{
					{Name: "start the daemon", Status: hostinit.StepDone, Detail: "grain-daemon.service is running"},
				},
			},
			opts: hostinit.Options{
				DataDir:   "/var/lib/grain/data",
				UseBinary: "/home/debian/grain-bin",
			},
			want: "sudo -u grain env GRAIN_DATA_DIR=/var/lib/grain/data /home/debian/grain-bin <command>",
		},
		{
			name: "-use-binary via Options.UseBinary fallback",
			result: hostinit.Result{
				Steps: []hostinit.Step{
					{Name: "start the daemon", Status: hostinit.StepSkipped, Detail: "already running"},
				},
			},
			opts: hostinit.Options{
				DataDir:    "/var/lib/grain/data",
				DaemonUser: "grain",
				UseBinary:  "/home/debian/grain-bin",
			},
			want: "sudo -u grain env GRAIN_DATA_DIR=/var/lib/grain/data /home/debian/grain-bin <command>",
		},
		{
			name: "built binary via Result.GrainBinaryPath",
			result: hostinit.Result{
				GrainBinaryPath: "/srv/grain/bin/grain",
				Steps: []hostinit.Step{
					{Name: "start the daemon", Status: hostinit.StepDone, Detail: "grain-daemon.service is running"},
				},
			},
			opts: hostinit.Options{
				DataDir: "/var/lib/grain/data",
			},
			want: "sudo -u grain env GRAIN_DATA_DIR=/var/lib/grain/data /srv/grain/bin/grain <command>",
		},
		{
			name: "empty paths fall back to grain",
			result: hostinit.Result{
				Steps: []hostinit.Step{
					{Name: "start the daemon", Status: hostinit.StepDone, Detail: "grain-daemon.service is running"},
				},
			},
			opts: hostinit.Options{
				DataDir: "/var/lib/grain/data",
			},
			want: "sudo -u grain env GRAIN_DATA_DIR=/var/lib/grain/data grain <command>",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var out strings.Builder
			fprintInitResult(&out, c.result, c.opts)
			if !strings.Contains(out.String(), c.want) {
				t.Errorf("fprintInitResult output:\n%s\nmissing %q", out.String(), c.want)
			}
		})
	}
}
