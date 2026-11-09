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
			name:   "up rejects shell-expanding hosted-daemon-image ref",
			script: "up.sh",
			args:   []string{"--project", "my-sandbox", "--admin", "alice@example.com", "--hosted-daemon-image", "docker.io/grainai/grain:main;id"},
		},
		{
			name:   "up rejects missing option value",
			script: "up.sh",
			args:   []string{"--project"},
		},
		{
			name:   "instance-ctl rejects missing action",
			script: "instance-ctl.sh",
			args:   []string{"--project", "my-sandbox", "--entry", "alice-vm"},
		},
		{
			name:   "instance-ctl rejects malformed project",
			script: "instance-ctl.sh",
			args:   []string{"create", "--project", "../bad", "--entry", "alice-vm", "--owner", "alice@example.com"},
		},
		{
			name:   "instance-ctl rejects malformed entry",
			script: "instance-ctl.sh",
			args:   []string{"create", "--project", "my-sandbox", "--entry", "../bad", "--owner", "alice@example.com"},
		},
		{
			name:   "instance-ctl rejects shell-expanding owner",
			script: "instance-ctl.sh",
			args:   []string{"create", "--project", "my-sandbox", "--entry", "alice-vm", "--owner", "alice$(id)@example.com"},
		},
		{
			name:   "instance-ctl rejects disk-gb below minimum",
			script: "instance-ctl.sh",
			args:   []string{"create", "--project", "my-sandbox", "--entry", "alice-vm", "--owner", "alice@example.com", "--disk-gb", "10"},
		},
		{
			name:   "instance-ctl rejects disk-gb above maximum",
			script: "instance-ctl.sh",
			args:   []string{"create", "--project", "my-sandbox", "--entry", "alice-vm", "--owner", "alice@example.com", "--disk-gb", "5000"},
		},
		{
			name:   "instance-ctl rejects missing option value",
			script: "instance-ctl.sh",
			args:   []string{"status", "--project"},
		},
		{
			name:   "configure-oauth rejects missing option value",
			script: "configure-oauth.sh",
			args:   []string{"--project"},
		},
		{
			name:   "configure-oauth rejects malformed project",
			script: "configure-oauth.sh",
			args:   []string{"--project", "bad/project", "--client-id", "1234.apps.googleusercontent.com"},
		},
		{
			name:   "configure-oauth rejects malformed prefix",
			script: "configure-oauth.sh",
			args:   []string{"--project", "my-sandbox", "--prefix", "BAD_PREFIX", "--client-id", "1234.apps.googleusercontent.com"},
		},
		{
			name:   "configure-oauth rejects malformed client-id",
			script: "configure-oauth.sh",
			args:   []string{"--project", "my-sandbox", "--client-id", "bad-client-id"},
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

func TestConfigureOAuthFlagsFileSecretPassing(t *testing.T) {
	sandboxDir := filepath.Join("..", "..", "deploy", "test", "registry-sandbox")
	fakeBin := t.TempDir()
	logFile := filepath.Join(fakeBin, "gcloud.log")

	secretVal := "super-sensitive-oauth-secret-xyz123"
	secretFile := filepath.Join(t.TempDir(), "client_secret.txt")
	if err := os.WriteFile(secretFile, []byte(secretVal+"\n"), 0o600); err != nil {
		t.Fatalf("writing secret file: %v", err)
	}

	writeFake := func(name, content string) {
		p := filepath.Join(fakeBin, name)
		if err := os.WriteFile(p, []byte(content), 0o755); err != nil {
			t.Fatalf("writing fake %s: %v", name, err)
		}
	}

	writeFake("gcloud", `#!/usr/bin/env bash
set -euo pipefail

for arg in "$@"; do
  if [[ "$arg" == *"`+secretVal+`"* ]]; then
    echo "ERROR: secret found in argv: $arg" >&2
    exit 99
  fi
done

case "$*" in
  *"backend-services update"*)
    flags_file=""
    for arg in "$@"; do
      case "$arg" in
        --flags-file=*)
          flags_file="${arg#--flags-file=}"
          ;;
      esac
    done
    if [[ -z "${flags_file}" ]]; then
      echo "ERROR: missing --flags-file in update command" >&2
      exit 98
    fi
    content="$(cat "${flags_file}")"
    if [[ "${content}" != *"`+secretVal+`"* ]]; then
      echo "ERROR: flags file missing secret" >&2
      exit 97
    fi
    if [[ "${content}" != *"test-client.apps.googleusercontent.com"* ]]; then
      echo "ERROR: flags file missing client id" >&2
      exit 96
    fi
    echo "UPDATED" >> "`+logFile+`"
    exit 0
    ;;
  *"backend-services describe"*"format=value"*)
    echo "test-client.apps.googleusercontent.com"
    exit 0
    ;;
  *"backend-services describe"*"format=json"*)
    echo '{"iap":{"enabled":true,"oauth2ClientId":"test-client.apps.googleusercontent.com"}}'
    exit 0
    ;;
  *)
    exit 0
    ;;
esac
`)

	env := append(os.Environ(), "PATH="+fakeBin+":"+os.Getenv("PATH"))

	cmd := exec.Command("bash", filepath.Join(sandboxDir, "configure-oauth.sh"),
		"--project", "my-sandbox",
		"--client-id", "test-client.apps.googleusercontent.com",
		"--client-secret-file", secretFile,
	)
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("configure-oauth.sh failed: %v\n%s", err, out)
	}

	if !strings.Contains(string(out), "Done. IAP on grain-reg-iap-bs now uses OAuth client test-client.apps.googleusercontent.com") {
		t.Fatalf("unexpected output: %s", out)
	}

	logged, err := os.ReadFile(logFile)
	if err != nil || !strings.Contains(string(logged), "UPDATED") {
		t.Fatalf("backend-services update was not called as expected, logged: %q (err: %v)", string(logged), err)
	}

	showCmd := exec.Command("bash", filepath.Join(sandboxDir, "configure-oauth.sh"),
		"--project", "my-sandbox",
		"--show",
	)
	showCmd.Env = env
	showOut, err := showCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("configure-oauth.sh --show failed: %v\n%s", err, showOut)
	}
	if !strings.Contains(string(showOut), `"oauth2ClientId": "test-client.apps.googleusercontent.com"`) {
		t.Fatalf("unexpected --show output: %s", showOut)
	}
}

// TestRegistrySandboxHostedProjectWiring checks the cross-project hosted VM
// mode: hosted VMs in a separate project get a peered VPC with CIDR-based
// firewall rules, and the registry config points compute at that project while
// keeping per-instance secrets in the registry project.
func TestRegistrySandboxHostedProjectWiring(t *testing.T) {
	sandboxDir := filepath.Join("..", "..", "deploy", "test", "registry-sandbox")
	read := func(name string) string {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(sandboxDir, name))
		if err != nil {
			t.Fatalf("reading %s: %v", name, err)
		}
		return string(data)
	}

	hosted := read("hosted.tf")
	for _, want := range []string{
		`resource "google_compute_network_peering" "registry_to_hosted"`,
		`resource "google_compute_network_peering" "hosted_to_registry"`,
		`resource "google_compute_router_nat" "hosted"`,
		`source_ranges = ["${google_compute_instance.registry.network_interface[0].network_ip}/32"]`,
		`source_ranges = [google_compute_subnetwork.hosted[0].ip_cidr_range]`,
		`"roles/compute.instanceAdmin.v1"`,
		`"roles/iam.serviceAccountAdmin"`,
	} {
		if !strings.Contains(hosted, want) {
			t.Errorf("hosted.tf missing %q", want)
		}
	}
	// The registry grants itself actAs on each instance account it creates
	// (compute.grant_self_act_as); a project-wide serviceAccountUser would let
	// it act as any account in the hosted project.
	for name, body := range map[string]string{"hosted.tf": hosted, "kms.tf": read("kms.tf")} {
		if strings.Contains(body, `"roles/iam.serviceAccountUser"`) {
			t.Errorf("%s must not grant the registry roles/iam.serviceAccountUser project-wide", name)
		}
	}
	if strings.Contains(hosted, "source_tags") {
		t.Errorf("hosted.tf must not use source_tags: tags do not match across VPC peering")
	}

	instance := read("instance.tf")
	for _, want := range []string{
		"compute_project_id  = local.hosted_project_id",
		"secrets_project_id  = trimspace(var.project_id)",
		"network_name        = local.hosted_network_name",
		"subnet_name         = local.hosted_subnet_name",
	} {
		if !strings.Contains(instance, want) {
			t.Errorf("instance.tf registry_vm_config missing %q", want)
		}
	}

	startup := read(filepath.Join("files", "startup.sh"))
	for _, want := range []string{
		"project: ${compute_project_id_json}",
		"secrets:\n  project: ${secrets_project_id_json}",
	} {
		if !strings.Contains(startup, want) {
			t.Errorf("files/startup.sh missing %q", want)
		}
	}

	up := read("up.sh")
	for _, want := range []string{"--hosted-project)", "--hosted-credentials)", "hosted_project_id: $hosted_project_id"} {
		if !strings.Contains(up, want) {
			t.Errorf("up.sh missing %q", want)
		}
	}
}

// TestRegistrySandboxInstancePoolConnectionsRequireTLS guards the shared
// hosted-instance PostgreSQL pool against cleartext connections: the
// registry's admin connection carries the pool superuser password and every
// generated role password (ALTER ROLE ... PASSWORD), and hosted VMs send
// their own password and every query, so none of them may cross the VPC
// with sslmode=disable, and the pool must serve TLS for them to connect.
func TestRegistrySandboxInstancePoolConnectionsRequireTLS(t *testing.T) {
	filesDir := filepath.Join("..", "..", "deploy", "test", "registry-sandbox", "files")
	read := func(name string) string {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(filesDir, name))
		if err != nil {
			t.Fatalf("reading %s: %v", name, err)
		}
		return string(data)
	}

	cleartextRemoteDSN := regexp.MustCompile(`postgres://[^"'\s]*@([^:/"'\s]+)[^"'\s]*sslmode=disable`)
	for _, name := range []string{"startup.sh", "hosted-vm-startup.sh", "sql-startup.sh"} {
		for i, line := range strings.Split(read(name), "\n") {
			m := cleartextRemoteDSN.FindStringSubmatch(line)
			if m == nil || m[1] == "127.0.0.1" || m[1] == "localhost" {
				continue
			}
			t.Errorf("files/%s:%d connects to %s with sslmode=disable; use sslmode=require: %s", name, i+1, m[1], strings.TrimSpace(line))
		}
	}

	if startup := read("startup.sh"); !regexp.MustCompile(`admin_dsn_template: "postgres://[^"]*\?sslmode=require"`).MatchString(startup) {
		t.Errorf("files/startup.sh admin_dsn_template must use sslmode=require")
	}
	hosted := read("hosted-vm-startup.sh")
	if !strings.Contains(hosted, "?sslmode=require") {
		t.Errorf("files/hosted-vm-startup.sh GRAIN_DB_DSN must use sslmode=require")
	}
	if strings.Contains(hosted, ".GRAIN_DB_PASSWORD //") {
		t.Errorf("files/hosted-vm-startup.sh must not read the database password from grain-config metadata")
	}
	sqlStartup := read("sql-startup.sh")
	for _, want := range []string{"-c ssl=on", "-c ssl_cert_file=", "-c ssl_key_file="} {
		if !strings.Contains(sqlStartup, want) {
			t.Errorf("files/sql-startup.sh must start postgres with %q", want)
		}
	}
}
