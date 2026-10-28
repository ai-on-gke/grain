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
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestDeployGKEScriptSyntaxAndHelp(t *testing.T) {
	root := repoRoot(t)
	script := filepath.Join(root, "scripts", "deploy-gke.sh")

	checkCmd := exec.Command("bash", "-n", script)
	if out, err := checkCmd.CombinedOutput(); err != nil {
		t.Fatalf("bash -n scripts/deploy-gke.sh failed: %v\n%s", err, out)
	}

	helpCmd := exec.Command(script, "--help")
	out, err := helpCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("scripts/deploy-gke.sh --help failed: %v\n%s", err, out)
	}
	helpText := string(out)
	for _, flag := range []string{
		"--config",
		"--project",
		"--region",
		"--cluster",
		"--adopt-cluster",
		"--dns-name",
		"--iap-principal",
		"--image",
		"--sandbox-image",
		"--postgres",
		"--dry-run",
		"--migrate",
		"--cutover",
		"--rollback",
		"--check",
	} {
		contains(t, helpText, flag)
	}
}

func TestDeployGKEScriptDryRunAllModes(t *testing.T) {
	root := repoRoot(t)
	script := filepath.Join(root, "scripts", "deploy-gke.sh")

	runDry := func(t *testing.T, env []string, args ...string) string {
		t.Helper()
		cmd := exec.Command(script, args...)
		cmd.Dir = root
		cmd.Env = append(os.Environ(), env...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("deploy-gke.sh %v failed: %v\n%s", args, err, out)
		}
		return string(out)
	}

	t.Run("default cloudsql deploy", func(t *testing.T) {
		out := runDry(t, nil,
			"--project=example-proj",
			"--region=us-central1",
			"--cluster=grain-test",
			"--dns-name=grain.example.com",
			"--iap-principal=user:alice@example.com",
			"--iap-principal=user:bob@example.com",
			"--hostd-name=workstation-1",
			"--dry-run",
		)
		// 1. Cluster + nested virt + Workload Identity + Namespace + PriorityClasses/quotas.
		contains(t, out, "gcloud container clusters create grain-test")
		contains(t, out, "--workload-pool=example-proj.svc.id.goog")
		contains(t, out, "--enable-nested-virtualization")
		contains(t, out, "--workload-metadata=GKE_METADATA")
		contains(t, out, "pod-security.kubernetes.io/enforce=privileged")
		// 2. Dependencies: Cloud SQL + Secret Manager + Workload Identity IAM.
		contains(t, out, "gcloud sql instances create grain-postgres")
		contains(t, out, "gcloud secrets create grain-secrets-key")
		contains(t, out, "gcloud secrets create grain-sandbox-ca-key")
		contains(t, out, "roles/secretmanager.secretAccessor")
		contains(t, out, "roles/iam.workloadIdentityUser")
		// 3. Digest-pinned image from ghcr.io/bwsalmon/grain/...
		contains(t, out, "ghcr.io/bwsalmon/grain/grain@sha256:")
		contains(t, out, "ghcr.io/bwsalmon/grain/granule@sha256:")
		// 4. IAP Gateway + SSL cert + backend-service-scoped IAM bindings (never project-wide).
		contains(t, out, "grain-gateway-ip")
		contains(t, out, "grain-gateway-cert")
		contains(t, out, "user:alice@example.com")
		contains(t, out, "user:bob@example.com")
		contains(t, out, "gcloud iap web add-iam-policy-binding --resource-type=backend-services")
		contains(t, out, "roles/iap.httpsResourceAccessor")
		contains(t, out, "GRAIN_IAP_AUDIENCE=")
		// 5. Connects hostd on the existing host without stopping grain-daemon.
		contains(t, out, "/etc/grain/hostd.token")
		contains(t, out, "gcloud secrets versions add grain-hostd-token")
		contains(t, out, "https://grain.example.com/api/hostd/connect")
		contains(t, out, "/etc/systemd/system/grain-hostd.service.d/remote.conf")
		contains(t, out, "systemctl restart grain-hostd.service")
		absent(t, out, "systemctl stop grain-daemon.service")
	})

	t.Run("config file rejects shell command substitution", func(t *testing.T) {
		badCfg := filepath.Join(t.TempDir(), "bad.env")
		if err := os.WriteFile(badCfg, []byte("GRAIN_GKE_PROJECT=\"$(id)\"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(script, "--config", badCfg, "--dry-run")
		cmd.Dir = root
		out, err := cmd.CombinedOutput()
		if err == nil {
			t.Fatalf("expected --config with command substitution to be rejected, got:\n%s", out)
		}
		contains(t, string(out), "refusing command substitution")
	})

	t.Run("in-cluster postgres and config file", func(t *testing.T) {
		cfgFile := filepath.Join(t.TempDir(), "deploy.env")
		cfgContent := strings.Join([]string{
			`GRAIN_GKE_PROJECT="cfg-proj"`,
			`GRAIN_GKE_DNS_NAME="grain-cfg.example.com"`,
			`GRAIN_GKE_POSTGRES="in-cluster"`,
			`GRAIN_GKE_ADOPT_CLUSTER="true"`,
		}, "\n") + "\n"
		if err := os.WriteFile(cfgFile, []byte(cfgContent), 0o600); err != nil {
			t.Fatal(err)
		}
		out := runDry(t, nil, "--config", cfgFile, "--dry-run")
		contains(t, out, "Adopting existing GKE cluster")
		contains(t, out, "postgres-incluster.yaml")
		absent(t, out, "gcloud sql instances create")
	})

	t.Run("migrate mode", func(t *testing.T) {
		out := runDry(t, nil,
			"--project=example-proj",
			"--dns-name=grain.example.com",
			"--migrate",
			"--dry-run",
		)
		contains(t, out, "/api/database/upgrade")
		contains(t, out, "gcloud secrets versions add grain-secrets-key")
		contains(t, out, "transcripts")
		contains(t, out, "artifacts")
		contains(t, out, "Excluding repos/, home/, and cache/jobs/")
		contains(t, out, "grain-data-seed")
	})

	t.Run("cutover mode and dual-active daemon refusal", func(t *testing.T) {
		out := runDry(t,
			[]string{
				"GRAIN_GKE_ASSUME_CLOUDTOP_ACTIVE=true",
				"GRAIN_GKE_ASSUME_GKE_REPLICAS=0",
			},
			"--project=example-proj",
			"--dns-name=grain.example.com",
			"--cutover",
			"--dry-run",
		)
		contains(t, out, "/api/dispatch/pause")
		contains(t, out, "systemctl stop grain-daemon.service")
		contains(t, out, "scale deployment/grain --replicas=1")

		// Refuses when both daemons would be active against the same state.
		cmd := exec.Command(script,
			"--project=example-proj",
			"--dns-name=grain.example.com",
			"--cutover",
			"--dry-run",
		)
		cmd.Dir = root
		cmd.Env = append(os.Environ(),
			"GRAIN_GKE_ASSUME_CLOUDTOP_ACTIVE=true",
			"GRAIN_GKE_ASSUME_GKE_REPLICAS=1",
		)
		refusalOut, err := cmd.CombinedOutput()
		if err == nil {
			t.Fatalf("expected --cutover to refuse when both cloudtop and GKE daemons are active, but it exited 0:\n%s", refusalOut)
		}
		contains(t, string(refusalOut), "refusing cutover")
	})

	t.Run("rollback mode", func(t *testing.T) {
		out := runDry(t, nil,
			"--project=example-proj",
			"--dns-name=grain.example.com",
			"--rollback",
			"--dry-run",
		)
		contains(t, out, "scale deployment/grain --replicas=0")
		contains(t, out, "rm -f /etc/grain/hostd-remote.env")
		contains(t, out, "systemctl enable --now grain-daemon.service")
	})

	t.Run("check mode", func(t *testing.T) {
		out := runDry(t, nil,
			"--project=example-proj",
			"--dns-name=grain.example.com",
			"--check",
			"--dry-run",
		)
		contains(t, out, "https://grain.example.com/api/auth/access-code")
		contains(t, out, "http://127.0.0.1:8080/api/executors")
		contains(t, out, "--access=dialout")
		contains(t, out, "io.k8s.core.v1.pods.")
		contains(t, out, "exec/attach/portforward")
	})
}

func TestDeployGKEManifestsAndBaseSync(t *testing.T) {
	root := repoRoot(t)

	// Base copies in deploy/kubernetes/gke/ must match deploy/kubernetes/ so Kustomize
	// builds cleanly under LoadRestrictionsRootOnly without drifting.
	for _, name := range []string{"grain-granule.yaml", "github-webhook.yaml"} {
		base := read(t, "deploy", "kubernetes", name)
		overlayBase := read(t, "deploy", "kubernetes", "gke", name)
		if base != overlayBase {
			t.Errorf("deploy/kubernetes/gke/%s drifted from deploy/kubernetes/%s", name, name)
		}
	}

	if _, err := exec.LookPath("kubectl"); err != nil {
		t.Skip("kubectl not on PATH; skipping kubectl kustomize check")
	}

	for _, rel := range []string{
		filepath.Join("deploy", "kubernetes", "gke"),
		filepath.Join("deploy", "gke"),
		filepath.Join("deploy", "gke", "manifests"),
	} {
		cmd := exec.Command("kubectl", "kustomize", filepath.Join(root, rel))
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("kubectl kustomize %s failed: %v\n%s", rel, err, out)
		}
		rendered := string(out)
		contains(t, rendered, "kind: Gateway")
		contains(t, rendered, "gke-l7-global-external-managed")
		contains(t, rendered, "kind: HTTPRoute")
		contains(t, rendered, "kind: GCPBackendPolicy")
		contains(t, rendered, "timeoutSec: 86400")
		contains(t, rendered, "kind: SecretProviderClass")
		contains(t, rendered, "name: grain-access")
		contains(t, rendered, "port: 4244")
		contains(t, rendered, "name: grain-sandbox-egress")
		contains(t, rendered, "name: grain-access-ingress")
		contains(t, rendered, "name: grain-webhook-isolation")
		contains(t, rendered, "name: grain-quota")
		contains(t, rendered, "ghcr.io/bwsalmon/grain/grain@sha256:")
		contains(t, rendered, "-k8s-granule-image=ghcr.io/bwsalmon/grain/granule@sha256:")
		contains(t, rendered, "gcr.io/cloud-sql-connectors/cloud-sql-proxy:2.14.1@sha256:")
	}

	// Verify NetworkPolicies match pkg/k8s.Manifest sandbox labels and isolate grain-webhook.
	netpolYAML := stripComments(read(t, "deploy", "kubernetes", "gke", "networkpolicy.yaml"))
	contains(t, netpolYAML, "app.kubernetes.io/managed-by: grain")
	contains(t, netpolYAML, "app.kubernetes.io/component: sandbox")
	absent(t, netpolYAML, "app: grain-sandbox")
	// NetworkPolicies are additive: the open-internet egress policy must
	// exclude network:none pods, or it would hand them the network
	// grain-sandbox-network-none withholds.
	contains(t, netpolYAML, "key: grain.dev/network\n        operator: NotIn\n        values: [\"none\"]")

	// Verify in-cluster Postgres image is digest-pinned and isolated by NetworkPolicy.
	pgYAML := stripComments(read(t, "deploy", "kubernetes", "gke", "postgres-incluster.yaml"))
	contains(t, pgYAML, "postgres:16-bookworm@sha256:")
	contains(t, pgYAML, "name: grain-postgres-isolation")

	// Verify ServiceAccount/grain-sandbox has automountServiceAccountToken: false and
	// no Workload Identity annotation.
	sandboxSA := stripComments(read(t, "deploy", "kubernetes", "gke", "sandbox-sa.yaml"))
	contains(t, sandboxSA, "automountServiceAccountToken: false")
	absent(t, sandboxSA, "iam.gke.io/gcp-service-account")

	// Verify Service/grain-access is never routed by the Gateway HTTPRoute.
	gatewayYAML := read(t, "deploy", "kubernetes", "gke", "gateway.yaml")
	absent(t, stripComments(gatewayYAML), "grain-access")
}

func TestDeployGKETerraformModule(t *testing.T) {
	vars := read(t, "deploy", "gke", "terraform", "variables.tf")
	for _, v := range []string{
		`variable "project_id"`,
		`variable "region"`,
		`variable "cluster_name"`,
		`variable "adopt_existing_cluster"`,
		`variable "postgres_mode"`,
		`variable "domain"`,
		`variable "iap_allowed_members"`,
		`variable "iap_backend_services"`,
	} {
		contains(t, vars, v)
	}

	clusterTF := read(t, "deploy", "gke", "terraform", "cluster.tf")
	contains(t, clusterTF, "enable_nested_virtualization = true")
	contains(t, clusterTF, `mode = "GKE_METADATA"`)

	iamTF := read(t, "deploy", "gke", "terraform", "iam.tf")
	contains(t, iamTF, "roles/iam.workloadIdentityUser")
	contains(t, iamTF, "roles/cloudsql.client")
	contains(t, iamTF, "roles/cloudsql.instanceUser")

	iapTF := stripComments(read(t, "deploy", "gke", "terraform", "iap.tf"))
	contains(t, iapTF, "google_iap_web_backend_service_iam_member")
	absent(t, iapTF, "google_iap_web_iam_member ")
}
