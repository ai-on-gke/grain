package intree

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/capability/gcpsets"
	"github.com/bwsalmon/grain/pkg/capability/gcpsets/gcpfake"
	"github.com/bwsalmon/grain/pkg/capability/protocol"
)

const testMinterKey = `{"type":"service_account","client_email":"minter@grain-test-proj.iam.gserviceaccount.com"}`

func TestGCPIsConfiguredThroughConfigure(t *testing.T) {
	cloud := gcpfake.New()
	cloud.Identities["minter"] = "minter@grain-test-proj.iam.gserviceaccount.com"
	cloud.Identities["me"] = "alice@example.com"
	// No flags and no environment: everything comes from configure.
	s, err := newConfigurableGCP(GCPConfig{Connector: cloud})
	if err != nil {
		t.Fatal(err)
	}
	config := map[string]any{
		"admins": []any{
			map[string]any{"name": "minter", "credential_source": "configured", "projects": []any{"grain-test-proj"}, "role_ceiling": []any{"roles/storage.objectViewer"}},
			map[string]any{"name": "me", "credential_source": "ambient", "projects": []any{"grain-test-proj"}, "role_ceiling": []any{"roles/viewer"}},
		},
		"admin_credentials": "$secret:gcp-admins",
		"permission_sets": []any{
			map[string]any{"name": "reader", "admin": "minter", "project": "grain-test-proj", "roles": []any{"roles/storage.objectViewer"}, "max_lease": "2h"},
			map[string]any{"name": "viewer", "admin": "me", "project": "grain-test-proj", "roles": []any{"roles/viewer"}},
		},
	}
	ps, err := attachConfigured(t, s, config, map[string]string{"gcp-admins": `{"minter": ` + testMinterKey + `}`})
	if err != nil {
		t.Fatal(err)
	}
	if got := protocol.SecretFields(ps.ConfigSchema); !reflect.DeepEqual(got, []string{"admin_credential", "admin_credentials", "minterKey"}) {
		t.Errorf("secret fields = %v", got)
	}
	// Test's report rides on the method's description.
	desc := ps.Methods["gcp-key"].Description
	for _, want := range []string{"admin me (ambient) is alice@example.com", "admin minter (configured) is minter@", "set viewer", "deployment 049f1e09 (instance 049f1e09c0ffee01): new service accounts are named grain-049f1e09-<set>-<n>"} {
		if !strings.Contains(desc, want) {
			t.Errorf("description lacks %q:\n%s", want, desc)
		}
	}
	if _, err := protocol.ValidateArgs(ps.Methods["gcp-key"].ArgsSchema, map[string]any{"permission_set": "viewer"}); err != nil {
		t.Errorf("a configured set was refused: %v", err)
	}
	resp := mint(t, ps, "gcp-key", "e1-1-0", nil)
	if resp.Status != protocol.StatusOK {
		t.Fatalf("mint = %+v", resp)
	}
	// max_lease came from configure: a 3h deadline is past it.
	late, err := ps.Mint(context.Background(), "gcp-key", "e1-2-0", time.Now().Add(3*time.Hour), validated(t, ps, "gcp-key", nil))
	if err != nil || late.Status != protocol.StatusRefused {
		t.Errorf("3h deadline = %+v, %v; want refused", late, err)
	}
	// A different configuration while a lease is held is refused.
	if err := ps.Configure(context.Background(), map[string]any{"project": "grain-test-proj", "minterKey": testMinterKey}); err == nil || !strings.Contains(err.Error(), "still held") {
		t.Errorf("reconfigure with a lease held: %v", err)
	}
	if _, err := ps.Release(context.Background(), "gcp-key", "e1-1-0"); err != nil {
		t.Fatal(err)
	}
}

// A config whose role exceeds its admin's ceiling is refused at
// configure, naming the role.
func TestGCPConfigureRefusesARoleOverTheCeiling(t *testing.T) {
	s, _ := newConfigurableGCP(GCPConfig{Connector: gcpfake.New()})
	_, err := attachConfigured(t, s, map[string]any{
		"admins":            []any{map[string]any{"name": "a", "credential_source": "ambient", "projects": []any{"grain-test-proj"}, "role_ceiling": []any{"roles/viewer"}}},
		"permission_sets":   []any{map[string]any{"name": "s", "admin": "a", "project": "grain-test-proj", "roles": []any{"roles/owner"}}},
		"admin_credentials": "$secret:none",
	}, map[string]string{"none": " "})
	if err == nil || !strings.Contains(err.Error(), "roles/owner") {
		t.Errorf("attach = %v, want the ceiling named", err)
	}
}

func TestGCPFallsBackToItsFlagsAndEnvironment(t *testing.T) {
	cloud := gcpfake.New()
	cloud.AddAccount("grain-test-proj", "agent@grain-test-proj.iam.gserviceaccount.com", "")
	cloud.AddAccount("grain-test-proj", "ci@grain-test-proj.iam.gserviceaccount.com", "")
	// An entry from before configure: flags and GCP_MINTER_KEY, no config,
	// migrated to the default set over the same account.
	fb, err := migrateGCP("grain-test-proj", []gcpsets.LegacyAccount{{Name: "default", Email: "agent@grain-test-proj.iam.gserviceaccount.com"}}, testMinterKey)
	if err != nil {
		t.Fatal(err)
	}
	s, err := newConfigurableGCP(GCPConfig{Fallback: fb, Connector: cloud})
	if err != nil {
		t.Fatal(err)
	}
	ps, err := attachConfigured(t, s, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	props := ps.Methods["gcp-key"].ArgsSchema["properties"].(map[string]any)
	if props["permission_set"].(map[string]any)["default"] != gcpsets.DefaultSet {
		t.Errorf("the migrated pool is not the default set: %v", props["permission_set"])
	}
	resp := mint(t, ps, "gcp-key", "e1-1-0", nil)
	if resp.Status != protocol.StatusOK || !strings.Contains(resp.PromptSection, "agent@grain-test-proj") {
		t.Fatalf("mint = %+v", resp)
	}
	if _, err := ps.Release(context.Background(), "gcp-key", "e1-1-0"); err != nil {
		t.Fatal(err)
	}

	// The old config fields still configure it, migrated the same way.
	s2, _ := newConfigurableGCP(GCPConfig{Connector: cloud})
	ps2, err := attachConfigured(t, s2, map[string]any{
		"project":   "grain-test-proj",
		"accounts":  []any{map[string]any{"name": "ci", "email": "ci@grain-test-proj.iam.gserviceaccount.com"}},
		"minterKey": "$secret:k",
	}, map[string]string{"k": testMinterKey})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := protocol.ValidateArgs(ps2.Methods["gcp-key"].ArgsSchema, map[string]any{"permission_set": "ci"}); err != nil {
		t.Errorf("the migrated pool was refused: %v", err)
	}

	// Neither: the attach fails with what is missing, through configure.
	s3, _ := newConfigurableGCP(GCPConfig{})
	_, err = attachConfigured(t, s3, nil, nil)
	var ce *protocol.ConfigureError
	if !errors.As(err, &ce) || !strings.Contains(err.Error(), "no permission sets are configured: add a permission set in Settings → Capabilities") {
		t.Errorf("unconfigured gcp: attach = %v, want a configure error pointing to Settings → Capabilities", err)
	}
	if strings.Contains(err.Error(), "admin account") {
		t.Errorf("unconfigured gcp still advises adding an admin account: %v", err)
	}
}

func TestGCPSimplifiedConfigureAmbientAndConfigured(t *testing.T) {
	cloud := gcpfake.New()
	cloud.DefaultIdentity = "ambient@example.com"
	cloud.Identities["default"] = "ambient@example.com"
	s, err := newConfigurableGCP(GCPConfig{Connector: cloud})
	if err != nil {
		t.Fatal(err)
	}
	ps, err := attachConfigured(t, s, map[string]any{
		"permission_sets": []any{
			map[string]any{"name": "reader", "project": "grain-test-proj", "roles": []any{"roles/storage.objectViewer"}},
		},
	}, nil)
	if err != nil {
		t.Fatalf("simplified ambient configure: %v", err)
	}
	if desc := ps.Methods["gcp-key"].Description; !strings.Contains(desc, "admin default (ambient) is ambient@example.com") {
		t.Errorf("description lacks default ambient admin:\n%s", desc)
	}
	resp := mint(t, ps, "gcp-key", "e1-1-0", nil)
	if resp.Status != protocol.StatusOK {
		t.Fatalf("mint = %+v", resp)
	}
	if _, err := ps.Release(context.Background(), "gcp-key", "e1-1-0"); err != nil {
		t.Fatal(err)
	}

	// Top-level configured admin credential without admins list.
	cloud2 := gcpfake.New()
	cloud2.Identities["default"] = "minter@grain-test-proj.iam.gserviceaccount.com"
	s2, err := newConfigurableGCP(GCPConfig{Connector: cloud2})
	if err != nil {
		t.Fatal(err)
	}
	ps2, err := attachConfigured(t, s2, map[string]any{
		"admin_credential_source": "configured",
		"admin_credential":        "$secret:gcp-admin",
		"permission_sets": []any{
			map[string]any{"name": "reader", "project": "grain-test-proj", "roles": []any{"roles/storage.objectViewer"}},
		},
	}, map[string]string{"gcp-admin": testMinterKey})
	if err != nil {
		t.Fatalf("simplified configured admin configure: %v", err)
	}
	if desc := ps2.Methods["gcp-key"].Description; !strings.Contains(desc, "admin default (configured) is minter@") {
		t.Errorf("description lacks default configured admin:\n%s", desc)
	}
	resp2 := mint(t, ps2, "gcp-key", "e2-1-0", nil)
	if resp2.Status != protocol.StatusOK {
		t.Fatalf("mint = %+v", resp2)
	}
	if _, err := ps2.Release(context.Background(), "gcp-key", "e2-1-0"); err != nil {
		t.Fatal(err)
	}
}
