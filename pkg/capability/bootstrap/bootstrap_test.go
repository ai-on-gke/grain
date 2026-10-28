package bootstrap

import (
	"context"
	"strings"
	"testing"

	"github.com/bwsalmon/grain/pkg/mcp"
	"github.com/bwsalmon/grain/pkg/model"
)

func TestSpecIsAGrantCapabilityNamedBootstrapPlaybooks(t *testing.T) {
	spec := New().Spec()
	if CapabilityName != "bootstrap-playbooks" || spec.Name != CapabilityName {
		t.Fatalf("Name = %q, want %q", spec.Name, "bootstrap-playbooks")
	}
	if spec.Provision != model.ProvisionGrant {
		t.Fatalf("Provision = %q, want %q", spec.Provision, model.ProvisionGrant)
	}
}

func TestResolveAlwaysHonours(t *testing.T) {
	res, err := New().Resolve(context.Background(), model.CapabilityContext{})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if res.Refused {
		t.Fatalf("Resolve refused: %+v", res)
	}
}

func callTool(t *testing.T, tools []mcp.Tool, name string, args map[string]any) mcp.Result {
	t.Helper()
	for _, tool := range tools {
		if tool.Name == name {
			return tool.Handler(context.Background(), args)
		}
	}
	t.Fatalf("no tool named %q", name)
	return mcp.Result{}
}

// TestListBootstrapPlaybooksListsEveryOneOfThem checks list_bootstrap_
// playbooks against exactly the four flows bwsalmon/agents#620 asked
// for -- if a fifth playbook is ever added under playbooks/, this needs
// updating too, on purpose: it's the one place a reviewer can check the
// full set is wired up correctly.
func TestListBootstrapPlaybooksListsEveryOneOfThem(t *testing.T) {
	res := callTool(t, PlaybookTools(), "list_bootstrap_playbooks", map[string]any{})
	if res.IsError {
		t.Fatalf("list_bootstrap_playbooks errored: %s", res.Text)
	}
	want := []string{"cloudsql-postgres", "gcp-capabilities", "github-connections", "github-test-repos", "gke-sandbox-cluster", "host-migration"}
	got := strings.Split(strings.TrimSpace(res.Text), "\n")
	if len(got) != len(want) {
		t.Fatalf("playbooks = %v, want %v", got, want)
	}
	for i, name := range want {
		if got[i] != name {
			t.Fatalf("playbooks = %v, want %v", got, want)
		}
	}
}

func TestReadGKESandboxClusterPlaybookFormattingAndSections(t *testing.T) {
	res := callTool(t, PlaybookTools(), "read_bootstrap_playbook", map[string]any{"name": "gke-sandbox-cluster"})
	if res.IsError {
		t.Fatalf("read_bootstrap_playbook(gke-sandbox-cluster) errored: %s", res.Text)
	}
	for _, required := range []string{
		"run_host_command",
		"grain init -platform=gke-sandboxes",
		"-dry-run",
		"--enable-nested-virtualization",
		"c4-standard-96-lssd",
		"hyperdisk-balanced",
		"n2-standard-",
		"third_party/granule/deploy/k8s/kvm-device-plugin.yaml",
		"pod-security.kubernetes.io/enforce=privileged",
		"/var/lib/grain/kubeconfig",
		"scripts/platform-verify/k8s-granule.sh",
		"[Settings → Sandboxes](/settings/sandboxes)",
		"kubernetes-granule",
		"constraints/compute.disableNestedVirtualization",
		"Rollback",
	} {
		if !strings.Contains(res.Text, required) {
			t.Errorf("gke-sandbox-cluster playbook missing %q", required)
		}
	}
}

func TestReadBootstrapPlaybookReadsAKnownOne(t *testing.T) {
	res := callTool(t, PlaybookTools(), "read_bootstrap_playbook", map[string]any{"name": "gcp-capabilities"})
	if res.IsError {
		t.Fatalf("read_bootstrap_playbook errored: %s", res.Text)
	}
	if !strings.Contains(res.Text, "run_host_command") {
		t.Fatalf("Text = %q, want it to mention run_host_command", res.Text)
	}
}

func TestReadBootstrapPlaybookRejectsAnUnknownName(t *testing.T) {
	res := callTool(t, PlaybookTools(), "read_bootstrap_playbook", map[string]any{"name": "does-not-exist"})
	if !res.IsError {
		t.Fatalf("expected an error for an unknown playbook, got: %s", res.Text)
	}
	if !strings.Contains(res.Text, "gcp-capabilities") {
		t.Fatalf("Text = %q, want it to list the available playbooks", res.Text)
	}
}

func TestReadBootstrapPlaybookRequiresAName(t *testing.T) {
	res := callTool(t, PlaybookTools(), "read_bootstrap_playbook", map[string]any{})
	if !res.IsError {
		t.Fatalf("expected an error for a missing name, got: %s", res.Text)
	}
}
