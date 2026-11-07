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

import (
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestRegistrySandboxScriptsAndTerraform(t *testing.T) {
	sandboxDir := filepath.Join("..", "..", "deploy", "test", "registry-sandbox")

	var scripts []string
	err := filepath.WalkDir(sandboxDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".terraform" {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(d.Name(), ".sh") {
			scripts = append(scripts, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", sandboxDir, err)
	}
	if len(scripts) == 0 {
		t.Fatalf("no shell scripts found under %s", sandboxDir)
	}

	jqSecretArg := regexp.MustCompile(`--arg\s+\w*secret\w*\b`)
	curlInlineBearer := regexp.MustCompile(`-H\s+["']?(Authorization|Proxy-Authorization):\s*Bearer\s+\$`)

	for _, script := range scripts {
		if out, err := exec.Command("bash", "-n", script).CombinedOutput(); err != nil {
			t.Errorf("bash -n %s: %v\n%s", script, err, out)
		}
		data, err := os.ReadFile(script)
		if err != nil {
			t.Fatalf("reading %s: %v", script, err)
		}
		for i, line := range strings.Split(string(data), "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "#") {
				continue
			}
			if jqSecretArg.MatchString(line) {
				t.Errorf("%s:%d passes secret in jq argv (--arg secret); pipe via stdin instead: %s", script, i+1, trimmed)
			}
			if curlInlineBearer.MatchString(line) {
				t.Errorf("%s:%d passes bearer token in curl argv (-H \"Authorization: Bearer ...\"); use -H @<(...) instead: %s", script, i+1, trimmed)
			}
		}
	}

	if scPath, err := exec.LookPath("shellcheck"); err == nil {
		if out, err := exec.Command(scPath, scripts...).CombinedOutput(); err != nil {
			t.Errorf("shellcheck on %s scripts failed: %v\n%s", sandboxDir, err, out)
		}
	}

	if tfPath, err := exec.LookPath("terraform"); err == nil {
		if out, err := exec.Command(tfPath, "-chdir="+sandboxDir, "fmt", "-check", "-diff").CombinedOutput(); err != nil {
			t.Errorf("terraform fmt -check on %s failed: %v\n%s", sandboxDir, err, out)
		}
	}

	netTF, err := os.ReadFile(filepath.Join(sandboxDir, "network.tf"))
	if err != nil {
		t.Fatalf("reading network.tf: %v", err)
	}
	if !strings.Contains(string(netTF), `ports    = ["22"]`) {
		t.Errorf("network.tf should restrict allow-iap-tcp firewall to port 22 only")
	}

	for _, s := range []string{"up.sh", "verify.sh", "down.sh", "daemon-up.sh", "daemon-down.sh"} {
		body, err := os.ReadFile(filepath.Join(sandboxDir, s))
		if err != nil {
			t.Fatalf("reading %s: %v", s, err)
		}
		if !strings.Contains(string(body), "value(projectNumber)") {
			t.Errorf("%s should verify GCS state bucket projectNumber matches PROJECT_ID", s)
		}
	}
}

func TestRegistrySandboxScriptFlagValidation(t *testing.T) {
	sandboxDir := filepath.Join("..", "..", "deploy", "test", "registry-sandbox")

	cases := []struct {
		name   string
		script string
		args   []string
	}{
		{
			name:   "register-entry rejects malformed project",
			script: "register-entry.sh",
			args:   []string{"--project", "../bad", "--entry", "alice-dev", "--owner", "alice@example.com"},
		},
		{
			name:   "register-entry rejects malformed prefix",
			script: "register-entry.sh",
			args:   []string{"--project", "my-sandbox", "--prefix", "BAD_PREFIX", "--entry", "alice-dev", "--owner", "alice@example.com"},
		},
		{
			name:   "register-entry rejects flag-injecting owner",
			script: "register-entry.sh",
			args:   []string{"--project", "my-sandbox", "--entry", "alice-dev", "--owner", "alice@example.com -v /:/host"},
		},
		{
			name:   "register-entry rejects shell-expanding owner",
			script: "register-entry.sh",
			args:   []string{"--project", "my-sandbox", "--entry", "alice-dev", "--owner", "alice$(id)@example.com"},
		},
		{
			name:   "register-entry rejects malformed allow-principal",
			script: "register-entry.sh",
			args:   []string{"--project", "my-sandbox", "--entry", "alice-dev", "--owner", "alice@example.com", "--allow-principal", "user:bad\"quote@example.com"},
		},
		{
			name:   "register-entry rejects unsupported domain allow-principal",
			script: "register-entry.sh",
			args:   []string{"--project", "my-sandbox", "--entry", "alice-dev", "--owner", "alice@example.com", "--allow-principal", "domain:example.com"},
		},
		{
			name:   "register-entry rejects malformed registry-host",
			script: "register-entry.sh",
			args:   []string{"--project", "my-sandbox", "--entry", "alice-dev", "--owner", "alice@example.com", "--registry-host", "bad\" -v /:/host"},
		},
		{
			name:   "register-entry rejects malformed instance-domain",
			script: "register-entry.sh",
			args:   []string{"--project", "my-sandbox", "--entry", "alice-dev", "--owner", "alice@example.com", "--instance-domain", "bad/domain"},
		},
		{
			name:   "verify rejects malformed project",
			script: "verify.sh",
			args:   []string{"--project", "bad/project"},
		},
		{
			name:   "verify rejects malformed entry",
			script: "verify.sh",
			args:   []string{"--project", "my-sandbox", "--entry", "../etc"},
		},
		{
			name:   "verify rejects wildcard state-bucket",
			script: "verify.sh",
			args:   []string{"--project", "my-sandbox", "--state-bucket", "my-sandbox-*"},
		},
		{
			name:   "down rejects malformed project",
			script: "down.sh",
			args:   []string{"--project", "-bad-flag"},
		},
		{
			name:   "down rejects wildcard state-bucket",
			script: "down.sh",
			args:   []string{"--project", "my-sandbox", "--state-bucket", "*"},
		},
		{
			name:   "up rejects non-numeric cert-timeout",
			script: "up.sh",
			args:   []string{"--project", "my-sandbox", "--admin", "alice@example.com", "--cert-timeout", "a[$(id)]"},
		},
		{
			name:   "up rejects wildcard state-bucket",
			script: "up.sh",
			args:   []string{"--project", "my-sandbox", "--admin", "alice@example.com", "--state-bucket", "bucket-*"},
		},
		{
			name:   "up rejects malformed entry spec",
			script: "up.sh",
			args:   []string{"--project", "my-sandbox", "--admin", "alice@example.com", "--entry", "../bad"},
		},
		{
			name:   "up rejects shell-expanding image ref",
			script: "up.sh",
			args:   []string{"--project", "my-sandbox", "--admin", "alice@example.com", "--image", "us-docker.pkg.dev/my-sandbox/grain/grain-registry:tag;id"},
		},
		{
			name:   "up rejects missing option value",
			script: "up.sh",
			args:   []string{"--project"},
		},
		{
			name:   "configure-oauth rejects missing option value",
			script: "configure-oauth.sh",
			args:   []string{"--project"},
		},
		{
			name:   "daemon-up rejects malformed project",
			script: "daemon-up.sh",
			args:   []string{"--project", "../bad", "--owner", "alice@example.com"},
		},
		{
			name:   "daemon-up rejects malformed entry",
			script: "daemon-up.sh",
			args:   []string{"--project", "my-sandbox", "--owner", "alice@example.com", "--entry", "../bad"},
		},
		{
			name:   "daemon-up rejects shell-expanding owner",
			script: "daemon-up.sh",
			args:   []string{"--project", "my-sandbox", "--owner", "alice$(id)@example.com"},
		},
		{
			name:   "daemon-up rejects malformed allow-principal",
			script: "daemon-up.sh",
			args:   []string{"--project", "my-sandbox", "--owner", "alice@example.com", "--allow-principal", "domain:example.com"},
		},
		{
			name:   "daemon-up rejects malformed repo",
			script: "daemon-up.sh",
			args:   []string{"--project", "my-sandbox", "--owner", "alice@example.com", "--repo", "bad/repo/extra"},
		},
		{
			name:   "daemon-up rejects wildcard state-bucket",
			script: "daemon-up.sh",
			args:   []string{"--project", "my-sandbox", "--owner", "alice@example.com", "--state-bucket", "bucket-*"},
		},
		{
			name:   "daemon-up rejects shell-expanding image ref",
			script: "daemon-up.sh",
			args:   []string{"--project", "my-sandbox", "--owner", "alice@example.com", "--image", "us-docker.pkg.dev/my-sandbox/grain/grain-daemon:tag;id"},
		},
		{
			name:   "daemon-up rejects missing option value",
			script: "daemon-up.sh",
			args:   []string{"--project"},
		},
		{
			name:   "daemon-down rejects malformed project",
			script: "daemon-down.sh",
			args:   []string{"--project", "-bad-flag"},
		},
		{
			name:   "daemon-down rejects malformed entry",
			script: "daemon-down.sh",
			args:   []string{"--project", "my-sandbox", "--entry", "../bad"},
		},
		{
			name:   "daemon-down rejects --all combined with --entry",
			script: "daemon-down.sh",
			args:   []string{"--project", "my-sandbox", "--all", "--entry", "sandbox-daemon"},
		},
		{
			name:   "daemon-down rejects wildcard state-bucket",
			script: "daemon-down.sh",
			args:   []string{"--project", "my-sandbox", "--state-bucket", "*"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.Command("bash", append([]string{filepath.Join(sandboxDir, tc.script)}, tc.args...)...)
			out, err := cmd.CombinedOutput()
			if err == nil {
				t.Fatalf("expected %s %v to fail validation, succeeded with:\n%s", tc.script, tc.args, out)
			}
			exitErr, ok := err.(*exec.ExitError)
			if !ok || exitErr.ExitCode() != 2 {
				t.Fatalf("expected exit code 2 from %s %v, got %v:\n%s", tc.script, tc.args, err, out)
			}
		})
	}
}

func TestRegistrySandboxCrossProjectStateBucketRejected(t *testing.T) {
	sandboxDir := filepath.Join("..", "..", "deploy", "test", "registry-sandbox")
	fakeBin := t.TempDir()
	marker := filepath.Join(fakeBin, "touched")

	writeFake := func(name, content string) {
		p := filepath.Join(fakeBin, name)
		if err := os.WriteFile(p, []byte(content), 0o755); err != nil {
			t.Fatalf("writing fake %s: %v", name, err)
		}
	}

	writeFake("gcloud", `#!/usr/bin/env bash
set -euo pipefail
case "$*" in
  *"auth list"*)
    echo "deployer@my-sandbox.iam.gserviceaccount.com"
    ;;
  *"auth print-access-token"*)
    echo "fake-token"
    ;;
  *"services enable"*)
    ;;
  *"projects describe"*)
    echo "111111111111"
    ;;
  *"storage buckets describe"*)
    echo "222222222222"
    ;;
  *"storage rm"*|*"storage buckets create"*|*"storage buckets update"*|*"compute instances create"*|*"compute instances delete"*)
    touch "`+marker+`"
    exit 99
    ;;
  *)
    exit 0
    ;;
esac
`)

	writeFake("terraform", `#!/usr/bin/env bash
touch "`+marker+`"
exit 99
`)

	writeFake("docker", `#!/usr/bin/env bash
exit 0
`)

	env := append(os.Environ(), "PATH="+fakeBin+":"+os.Getenv("PATH"))

	for _, tc := range []struct {
		script string
		args   []string
	}{
		{
			script: "up.sh",
			args:   []string{"--project", "my-sandbox", "--admin", "alice@example.com", "--skip-image-build", "--image", "us-docker.pkg.dev/my-sandbox/grain/grain-registry:test"},
		},
		{
			script: "verify.sh",
			args:   []string{"--project", "my-sandbox"},
		},
		{
			script: "down.sh",
			args:   []string{"--project", "my-sandbox"},
		},
		{
			script: "daemon-up.sh",
			args:   []string{"--project", "my-sandbox", "--owner", "alice@example.com", "--skip-image-build", "--image", "us-docker.pkg.dev/my-sandbox/grain/grain-daemon:test"},
		},
		{
			script: "daemon-down.sh",
			args:   []string{"--project", "my-sandbox"},
		},
	} {
		t.Run(tc.script, func(t *testing.T) {
			cmd := exec.Command("bash", append([]string{filepath.Join(sandboxDir, tc.script)}, tc.args...)...)
			cmd.Env = env
			out, err := cmd.CombinedOutput()
			if err == nil {
				t.Fatalf("expected %s to reject cross-project state bucket, succeeded:\n%s", tc.script, out)
			}
			if !strings.Contains(string(out), "bucket belongs to project number 222222222222, not my-sandbox (111111111111)") {
				t.Fatalf("unexpected error output from %s:\n%s", tc.script, out)
			}
			if _, statErr := os.Stat(marker); statErr == nil {
				t.Fatalf("%s invoked terraform or mutated GCS despite cross-project state bucket", tc.script)
			}
		})
	}
}
