package gcpsets_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"golang.org/x/oauth2/google"

	"github.com/ai-on-gke/grain/pkg/capability/gcpsets"
	"github.com/ai-on-gke/grain/pkg/capability/gcpsets/gcpfake"
	"github.com/ai-on-gke/grain/pkg/capability/protocol"
)

const proj = "grain-test-proj"

// dep and inst are the deployment id and installation id config gives
// every test, as grain fills them into configure.
const (
	dep  = "049f1e09"
	inst = "049f1e09c0ffee0123456789abcdef01"
)

// ours is this deployment's ownership marker for set.
func ours(set string) string { return gcpsets.OwnerDescription(dep, inst, set) }

const adminKey = `{"type":"service_account","client_email":"minter@grain-test-proj.iam.gserviceaccount.com"}`

func config(t *testing.T, extra map[string]any) *gcpsets.Config {
	t.Helper()
	cfg := map[string]any{
		protocol.DeploymentIDConfig: dep,
		protocol.InstanceIDConfig:   inst,
		"admins": []any{
			map[string]any{"name": "minter", "credential_source": "configured", "projects": []any{proj}, "role_ceiling": []any{"roles/storage.objectViewer", "roles/logging.viewer"}},
			map[string]any{"name": "me", "credential_source": "ambient", "projects": []any{proj}, "role_ceiling": []any{"roles/viewer"}},
		},
		"admin_credentials": `{"minter": ` + adminKey + `}`,
		"permission_sets": []any{
			map[string]any{"name": "reader", "admin": "minter", "project": proj, "roles": []any{"roles/storage.objectViewer"}, "pool_max": float64(2), "max_lease": "2h"},
			map[string]any{"name": "viewer", "admin": "me", "project": proj, "roles": []any{"roles/viewer"}},
		},
	}
	for k, v := range extra {
		cfg[k] = v
	}
	c, err := gcpsets.Parse(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func manager(t *testing.T, c *gcpsets.Config, cloud *gcpfake.Cloud, now *time.Time) *gcpsets.Manager {
	t.Helper()
	m := gcpsets.NewManager(c, cloud)
	m.Now = func() time.Time { return *now }
	cloud.Now = m.Now
	return m
}

func TestConfigValidationForBothCredentialSources(t *testing.T) {
	base := func() map[string]any {
		return map[string]any{
			protocol.DeploymentIDConfig: dep,
			"admins":                    []any{map[string]any{"name": "a", "credential_source": "configured", "projects": []any{proj}, "role_ceiling": []any{"roles/viewer"}}},
			"permission_sets":           []any{map[string]any{"name": "s", "admin": "a", "project": proj, "roles": []any{"roles/viewer"}}},
		}
	}
	for name, tc := range map[string]struct {
		edit func(map[string]any)
		want string
	}{
		"configured without a credential": {func(map[string]any) {}, "admin_credentials has no credential"},
		"configured with a credential": {func(c map[string]any) {
			c["admin_credentials"] = `{"a": ` + adminKey + `}`
		}, ""},
		"configured with an external-account config": {func(c map[string]any) {
			c["admin_credentials"] = `{"a": {"type":"external_account","audience":"x"}}`
		}, ""},
		"configured with something else": {func(c map[string]any) {
			c["admin_credentials"] = `{"a": {"type":"authorized_user"}}`
		}, "must be a service-account key"},
		"credentials not JSON": {func(c map[string]any) { c["admin_credentials"] = "nope" }, "not a JSON object"},
		"ambient": {func(c map[string]any) {
			c["admins"].([]any)[0].(map[string]any)["credential_source"] = "ambient"
		}, ""},
		"ambient with a credential": {func(c map[string]any) {
			c["admins"].([]any)[0].(map[string]any)["credential_source"] = "ambient"
			c["admin_credentials"] = `{"a": ` + adminKey + `}`
		}, "must not hold a credential"},
		"credential for nobody": {func(c map[string]any) {
			c["admin_credentials"] = `{"a": ` + adminKey + `, "ghost": ` + adminKey + `}`
		}, "ghost"},
		"set over another project": {func(c map[string]any) {
			c["admin_credentials"] = `{"a": ` + adminKey + `}`
			c["permission_sets"].([]any)[0].(map[string]any)["project"] = "other-project"
		}, "not one of admin"},
		"set over its admin's role ceiling": {func(c map[string]any) {
			c["admin_credentials"] = `{"a": ` + adminKey + `}`
			c["permission_sets"].([]any)[0].(map[string]any)["roles"] = []any{"roles/viewer", "roles/owner"}
		}, "roles/owner exceed(s) admin \"a\"'s role ceiling"},
		"unknown admin": {func(c map[string]any) {
			c["admin_credentials"] = `{"a": ` + adminKey + `}`
			c["permission_sets"].([]any)[0].(map[string]any)["admin"] = "b"
		}, `no admin is called "b"`},
		"bad scope": {func(c map[string]any) {
			c["admin_credentials"] = `{"a": ` + adminKey + `}`
			c["permission_sets"].([]any)[0].(map[string]any)["resource_scope"] = "topics/x"
		}, "buckets/<name>"},
	} {
		t.Run(name, func(t *testing.T) {
			c := base()
			tc.edit(c)
			_, err := gcpsets.Parse(c)
			if tc.want == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want one containing %q", err, tc.want)
			}
			if strings.Contains(err.Error(), "minter@") {
				t.Error("an error quoted the credential")
			}
		})
	}
}

func TestASetCannotExceedItsAdminsRoleCeiling(t *testing.T) {
	_, err := gcpsets.Parse(map[string]any{
		"admins":            []any{map[string]any{"name": "a", "credential_source": "configured", "projects": []any{proj}, "role_ceiling": []any{"roles/viewer"}}},
		"admin_credentials": `{"a": ` + adminKey + `}`,
		"permission_sets":   []any{map[string]any{"name": "s", "admin": "a", "project": proj, "roles": []any{"roles/viewer", "roles/owner"}}},
	})
	if err == nil || !strings.Contains(err.Error(), "roles/owner") || strings.Contains(err.Error(), "roles/viewer,") {
		t.Fatalf("err = %v, want roles/owner named as over the ceiling", err)
	}
}

func TestSimplifiedConfigDefaultsToAmbientAdmin(t *testing.T) {
	c, err := gcpsets.Parse(map[string]any{
		protocol.DeploymentIDConfig: dep,
		protocol.InstanceIDConfig:   inst,
		"permission_sets": []any{
			map[string]any{"name": "reader", "project": proj, "roles": []any{"roles/storage.objectViewer"}},
			map[string]any{"name": "logger", "project": "other-project", "roles": []any{"roles/logging.viewer"}},
		},
	})
	if err != nil {
		t.Fatalf("Parse simplified config: %v", err)
	}
	if len(c.Admins) != 1 {
		t.Fatalf("len(Admins) = %d, want 1", len(c.Admins))
	}
	a := c.Admins[0]
	if a.Name != gcpsets.DefaultAdmin || a.CredentialSource != gcpsets.SourceAmbient {
		t.Errorf("admin = %+v, want default ambient admin", a)
	}
	if got := strings.Join(a.Projects, ","); got != proj+",other-project" {
		t.Errorf("admin.Projects = %q, want %q", got, proj+",other-project")
	}
	if got := strings.Join(a.RoleCeiling, ","); got != "roles/storage.objectViewer,roles/logging.viewer" {
		t.Errorf("admin.RoleCeiling = %q", got)
	}
	for _, s := range c.Sets {
		if s.Admin != gcpsets.DefaultAdmin {
			t.Errorf("set %s.Admin = %q, want %q", s.Name, s.Admin, gcpsets.DefaultAdmin)
		}
	}
}

func TestSimplifiedConfigWithTopLevelConfiguredCredential(t *testing.T) {
	c, err := gcpsets.Parse(map[string]any{
		protocol.DeploymentIDConfig: dep,
		"admin_credential_source":   "configured",
		"admin_credential":          adminKey,
		"permission_sets": []any{
			map[string]any{"name": "reader", "project": proj, "roles": []any{"roles/storage.objectViewer"}},
		},
	})
	if err != nil {
		t.Fatalf("Parse top-level configured admin: %v", err)
	}
	if len(c.Admins) != 1 || c.Admins[0].CredentialSource != gcpsets.SourceConfigured || c.Admins[0].Credential != adminKey {
		t.Fatalf("Admins = %+v, want configured default admin", c.Admins)
	}

	// Supplying admin_credential when admin_credential_source was defaulted to ambient by schema validation also works.
	c2, err := gcpsets.Parse(map[string]any{
		protocol.DeploymentIDConfig: dep,
		"admin_credential_source":   "ambient",
		"admin_credential":          adminKey,
		"permission_sets": []any{
			map[string]any{"name": "reader", "project": proj, "roles": []any{"roles/storage.objectViewer"}},
		},
	})
	if err != nil {
		t.Fatalf("Parse top-level credential with default source: %v", err)
	}
	if c2.Admins[0].CredentialSource != gcpsets.SourceConfigured {
		t.Errorf("CredentialSource = %q, want configured", c2.Admins[0].CredentialSource)
	}

	// Configured without a credential fails validation.
	_, err = gcpsets.Parse(map[string]any{
		protocol.DeploymentIDConfig: dep,
		"admin_credential_source":   "configured",
		"permission_sets": []any{
			map[string]any{"name": "reader", "project": proj, "roles": []any{"roles/storage.objectViewer"}},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "has no credential") {
		t.Errorf("configured without credential: err = %v", err)
	}
}

// The deployment id names and owns the sets' service accounts, so it is
// read only from the identity field grain overwrites on every configure
// (protocol.WithIdentity): no other key may stand in for it.
func TestDeploymentIDComesOnlyFromGrainsIdentityField(t *testing.T) {
	props := gcpsets.Schema()["properties"].(map[string]any)
	for _, k := range []string{"_grain_deployment_id", "_grain_instance_id"} {
		if _, ok := props[k]; ok {
			t.Errorf("schema declares %s, which grain does not fill in or strip", k)
		}
	}
	_, err := gcpsets.Parse(map[string]any{
		"_grain_deployment_id": "otherdep",
		"permission_sets": []any{
			map[string]any{"name": "reader", "project": proj, "roles": []any{"roles/storage.objectViewer"}},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "configure carried none") {
		t.Errorf("deployment id from an operator-set key: err = %v", err)
	}
}

func TestExplicitAdminWithOptionalProjectsAndRoleCeiling(t *testing.T) {
	c, err := gcpsets.Parse(map[string]any{
		protocol.DeploymentIDConfig: dep,
		"admins": []any{
			map[string]any{"name": "custom", "credential_source": "ambient"},
		},
		"permission_sets": []any{
			map[string]any{"name": "reader", "project": proj, "roles": []any{"roles/storage.objectViewer"}},
		},
	})
	if err != nil {
		t.Fatalf("Parse explicit admin with omitted projects/role_ceiling/set.admin: %v", err)
	}
	if c.Sets[0].Admin != "custom" {
		t.Errorf("set.Admin = %q, want custom", c.Sets[0].Admin)
	}
	if got := strings.Join(c.Admins[0].Projects, ","); got != proj {
		t.Errorf("admin.Projects = %q, want %q", got, proj)
	}
	if got := strings.Join(c.Admins[0].RoleCeiling, ","); got != "roles/storage.objectViewer" {
		t.Errorf("admin.RoleCeiling = %q, want roles/storage.objectViewer", got)
	}
}

func TestSetNamesAreTheEnumInOrder(t *testing.T) {
	c := config(t, nil)
	if got := strings.Join(c.SetNames(), ","); got != "reader,viewer" {
		t.Errorf("SetNames = %s", got)
	}
}

func TestMintCreatesReusesAndReleases(t *testing.T) {
	cloud := gcpfake.New()
	now := time.Date(2026, 10, 23, 9, 0, 0, 0, time.UTC)
	m := manager(t, config(t, nil), cloud, &now)
	ctx := context.Background()

	key, l, err := m.Mint(ctx, "e1-1-0", "reader", now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	want := gcpsets.Email("grain-049f1e09-reader-1", proj)
	if l.Account != want || !strings.Contains(key, want) {
		t.Fatalf("lease %+v", l)
	}
	if got := cloud.Members("projects/"+proj, "roles/storage.objectViewer"); len(got) != 1 || got[0] != "serviceAccount:"+want {
		t.Errorf("bindings = %v", got)
	}
	if o, ok := gcpsets.OwnedBy(cloud.Accounts[proj][want].Description); !ok || o != (gcpsets.Owner{Deployment: dep, Instance: gcpsets.InstanceTag(inst), Set: "reader"}) {
		t.Errorf("the account is not marked as the set's")
	}
	// A second attempt reuses the account rather than creating another.
	if _, l2, err := m.Mint(ctx, "e2-1-0", "reader", now.Add(time.Hour)); err != nil || l2.Account != want {
		t.Fatalf("second mint: %+v %v", l2, err)
	}
	if cloud.Calls["CreateServiceAccount"] != 1 {
		t.Errorf("created %d accounts, want 1", cloud.Calls["CreateServiceAccount"])
	}
	if err := m.Release(ctx, "e1-1-0"); err != nil {
		t.Fatal(err)
	}
	if err := m.Release(ctx, "e1-1-0"); err != nil {
		t.Fatalf("a second release: %v", err)
	}
	if cloud.KeyCount(want) != 1 {
		t.Errorf("keys = %d after one release, want 1", cloud.KeyCount(want))
	}
}

func TestMintRefusesUnknownSetsAndLongDeadlines(t *testing.T) {
	now := time.Now()
	m := manager(t, config(t, nil), gcpfake.New(), &now)
	var r *gcpsets.Refusal
	if _, _, err := m.Mint(context.Background(), "e1", "nope", now.Add(time.Hour)); !errors.As(err, &r) {
		t.Errorf("unknown set: %v", err)
	}
	if _, _, err := m.Mint(context.Background(), "e1", "reader", now.Add(3*time.Hour)); !errors.As(err, &r) || !strings.Contains(err.Error(), "max_lease") {
		t.Errorf("deadline past max_lease: %v", err)
	}
}

// The pool spreads keys over the set's accounts, creating the next only
// when every account is at GCP's ten keys, and is exhausted at pool_max.
func TestThePoolAbsorbsTheKeyQuota(t *testing.T) {
	cloud := gcpfake.New()
	now := time.Now()
	m := manager(t, config(t, nil), cloud, &now)
	ctx := context.Background()
	for i := 0; i < 2*gcpsets.KeysPerAccount; i++ {
		if _, _, err := m.Mint(ctx, "e"+string(rune('a'+i)), "reader", now.Add(time.Hour)); err != nil {
			t.Fatalf("mint %d: %v", i, err)
		}
	}
	a1, a2 := gcpsets.Email("grain-049f1e09-reader-1", proj), gcpsets.Email("grain-049f1e09-reader-2", proj)
	if cloud.KeyCount(a1) != 10 || cloud.KeyCount(a2) != 10 {
		t.Fatalf("keys %d/%d", cloud.KeyCount(a1), cloud.KeyCount(a2))
	}
	_, _, err := m.Mint(ctx, "ez", "reader", now.Add(time.Hour))
	var ex *gcpsets.Exhausted
	if !errors.As(err, &ex) || ex.RetryAfter.IsZero() {
		t.Fatalf("mint past pool_max: %v", err)
	}
	if err := m.Release(ctx, "ea"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.Mint(ctx, "ez", "reader", now.Add(time.Hour)); err != nil {
		t.Fatalf("mint after a release: %v", err)
	}
}

// Keys someone else made fill an account between list and create: the
// mint moves on to the next account.
func TestAKeyQuotaErrorMovesToTheNextAccount(t *testing.T) {
	cloud := gcpfake.New()
	now := time.Now()
	m := manager(t, config(t, nil), cloud, &now)
	a1 := gcpsets.Email("grain-049f1e09-reader-1", proj)
	cloud.AddAccount(proj, a1, ours("reader"))
	cloud.AddAccount(proj, gcpsets.Email("grain-049f1e09-reader-2", proj), ours("reader"))
	for i := 0; i < 10; i++ {
		cloud.AddKey(a1, "old"+string(rune('a'+i)), now)
	}
	_, l, err := m.Mint(context.Background(), "e1", "reader", now.Add(time.Hour))
	if err != nil || l.Account != gcpsets.Email("grain-049f1e09-reader-2", proj) {
		t.Fatalf("%+v %v", l, err)
	}
}

func TestDriftIsReconciledAndForeignBindingsAreLeftAlone(t *testing.T) {
	cloud := gcpfake.New()
	now := time.Now()
	ctx := context.Background()
	mine := gcpsets.Email("grain-049f1e09-reader-1", proj)
	cloud.AddAccount(proj, mine, ours("reader"))
	// Foreign: an account named like ours but not marked, and a human.
	foreign := gcpsets.Email("grain-049f1e09-reader-2", proj)
	cloud.AddAccount(proj, foreign, "made by hand")
	cloud.Bind("projects/"+proj, "roles/logging.viewer", "serviceAccount:"+mine) // drift: a role the set no longer has
	cloud.Bind("projects/"+proj, "roles/logging.viewer", "user:alice@example.com")
	cloud.Bind("projects/"+proj, "roles/owner", "serviceAccount:"+foreign)

	m := manager(t, config(t, nil), cloud, &now)
	if err := m.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	if got := cloud.Members("projects/"+proj, "roles/logging.viewer"); len(got) != 1 || got[0] != "user:alice@example.com" {
		t.Errorf("logging.viewer = %v, want only alice", got)
	}
	if got := cloud.Members("projects/"+proj, "roles/storage.objectViewer"); len(got) != 1 || got[0] != "serviceAccount:"+mine {
		t.Errorf("objectViewer = %v", got)
	}
	if got := cloud.Members("projects/"+proj, "roles/owner"); len(got) != 1 {
		t.Errorf("the foreign binding was touched: %v", got)
	}
	accounts, _ := m.Accounts(ctx, m.Config.Set("reader"))
	if len(accounts) != 1 || accounts[0] != mine {
		t.Errorf("Accounts = %v, want only the marked one", accounts)
	}
	// A second pass changes nothing.
	before := cloud.Calls["SetPolicy"]
	if err := m.Reconcile(ctx); err != nil || cloud.Calls["SetPolicy"] != before {
		t.Errorf("a converged reconcile wrote the policy again (%v)", err)
	}
}

func TestReconcileRetriesALostPolicyRace(t *testing.T) {
	cloud := gcpfake.New()
	now := time.Now()
	cloud.AddAccount(proj, gcpsets.Email("grain-049f1e09-reader-1", proj), ours("reader"))
	cloud.ConflictOnce = true
	if err := manager(t, config(t, nil), cloud, &now).Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(cloud.Members("projects/"+proj, "roles/storage.objectViewer")) != 1 {
		t.Error("the binding was not written after the conflict")
	}
}

func TestAResourceScopeBindsOnTheBucketAndNotTheProject(t *testing.T) {
	cloud := gcpfake.New()
	now := time.Now()
	c := config(t, nil)
	c.Sets[0].ResourceScope = "buckets/grain-test-bucket"
	m := manager(t, c, cloud, &now)
	_, l, err := m.Mint(context.Background(), "e1", "reader", now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if got := cloud.Members("buckets/grain-test-bucket", "roles/storage.objectViewer"); len(got) != 1 || got[0] != "serviceAccount:"+l.Account {
		t.Errorf("bucket bindings = %v", got)
	}
	if got := cloud.Members("projects/"+proj, "roles/storage.objectViewer"); len(got) != 0 {
		t.Errorf("project bindings = %v", got)
	}
}

func TestTheReaperDeletesOnlyUnheldKeysPastTheirLease(t *testing.T) {
	cloud := gcpfake.New()
	now := time.Date(2026, 10, 23, 9, 0, 0, 0, time.UTC)
	m := manager(t, config(t, nil), cloud, &now)
	ctx := context.Background()
	_, held, err := m.Mint(ctx, "e1", "reader", now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	cloud.AddKey(held.Account, "stale", now.Add(-3*time.Hour)) // past 2h max_lease + grace
	cloud.AddKey(held.Account, "young", now.Add(-time.Hour))
	foreign := gcpsets.Email("someone", proj)
	cloud.AddAccount(proj, foreign, "not ours")
	cloud.AddKey(foreign, "theirs", now.Add(-100*time.Hour))
	now = now.Add(4 * time.Hour) // the held lease is old too, but held
	deleted, err := m.Reap(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(deleted, ",") != held.Account+"/stale,"+held.Account+"/young" {
		t.Errorf("reaped %v", deleted)
	}
	if cloud.KeyCount(held.Account) != 1 || cloud.KeyCount(foreign) != 1 {
		t.Errorf("keys left: ours %d, foreign %d", cloud.KeyCount(held.Account), cloud.KeyCount(foreign))
	}
}

func TestRemovedSetsAccountsAreOfferedNotDeleted(t *testing.T) {
	cloud := gcpfake.New()
	now := time.Now()
	ctx := context.Background()
	gone := gcpsets.Email("grain-049f1e09-old-1", proj)
	cloud.AddAccount(proj, gone, ours("old"))
	cloud.AddAccount(proj, gcpsets.Email("handmade", proj), "no marker")
	cloud.Bind("projects/"+proj, "roles/viewer", "serviceAccount:"+gone)

	m := manager(t, config(t, nil), cloud, &now)
	census, err := m.Census(ctx)
	orphans := census.Orphans
	if err != nil || len(orphans) != 1 || orphans[0].Email != gone {
		t.Fatalf("orphans = %+v %v", orphans, err)
	}
	if deleted, _ := m.DeleteOrphans(ctx, orphans); len(deleted) != 0 || cloud.Calls["DeleteServiceAccount"] != 0 {
		t.Fatal("an account was deleted without being asked for")
	}
	if r := m.Report(ctx, census, nil); !strings.Contains(r, "removed set old still has "+gone) {
		t.Errorf("report does not offer the orphan:\n%s", r)
	}

	m = manager(t, config(t, map[string]any{"delete_orphans": []any{"old"}}), cloud, &now)
	deleted, err := m.DeleteOrphans(ctx, orphans)
	if err != nil || len(deleted) != 1 {
		t.Fatalf("deleted %v %v", deleted, err)
	}
	if len(cloud.Accounts[proj]) != 1 || len(cloud.Members("projects/"+proj, "roles/viewer")) != 0 {
		t.Errorf("accounts %v, viewer %v", cloud.Accounts[proj], cloud.Members("projects/"+proj, "roles/viewer"))
	}
}

func TestReportNamesIdentitiesMissingPermissionsAndUsage(t *testing.T) {
	cloud := gcpfake.New()
	now := time.Now()
	cloud.Identities["minter"] = "minter@grain-test-proj.iam.gserviceaccount.com"
	cloud.Identities["me"] = "alice@example.com"
	cloud.Granted = map[string][]string{proj: gcpsets.AdminPermissions[:7]}
	m := manager(t, config(t, nil), cloud, &now)
	if _, _, err := m.Mint(context.Background(), "e1", "reader", now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	r := m.Report(context.Background(), gcpsets.Census{}, nil)
	for _, want := range []string{
		"admin me (ambient) is alice@example.com",
		"admin minter (configured) is minter@",
		"MISSING: grain-test-proj: resourcemanager.projects.get, resourcemanager.projects.getIamPolicy, resourcemanager.projects.setIamPolicy",
		"set reader (grain-test-proj, roles/storage.objectViewer): 1 of 2 account(s), 1 key(s) held now",
	} {
		if !strings.Contains(r, want) {
			t.Errorf("report lacks %q:\n%s", want, r)
		}
	}
	if strings.Contains(r, "private_key") {
		t.Error("the report holds a credential")
	}
}

func TestMigrationKeepsTheFixedPoolAsTheDefaultSet(t *testing.T) {
	c, err := gcpsets.Migrate("grain-sandbox-dev", []gcpsets.LegacyAccount{
		{Name: "default", Email: "grain-sb-agent-0@grain-sandbox-dev.iam.gserviceaccount.com", Count: 10},
		{Name: "staging", Email: "stage@grain-sandbox-dev.iam.gserviceaccount.com"},
	}, adminKey)
	if err != nil {
		t.Fatal(err)
	}
	if got := c.SetNames(); len(got) != 2 || got[0] != gcpsets.DefaultSet || got[1] != "staging" {
		t.Fatalf("sets = %v", got)
	}
	s := c.Sets[0]
	if len(s.Accounts) != 10 || s.Accounts[9] != "grain-sb-agent-9@grain-sandbox-dev.iam.gserviceaccount.com" || s.PoolMax != 10 {
		t.Fatalf("set = %+v", s)
	}
	// Minting over the old pool never creates or binds anything.
	cloud := gcpfake.New()
	for _, e := range s.Accounts {
		cloud.AddAccount("grain-sandbox-dev", e, "")
	}
	now := time.Now()
	m := manager(t, c, cloud, &now)
	if _, l, err := m.Mint(context.Background(), "e1", gcpsets.DefaultSet, now.Add(time.Hour)); err != nil || l.Account != s.Accounts[0] {
		t.Fatalf("%+v %v", l, err)
	}
	if cloud.Calls["CreateServiceAccount"]+cloud.Calls["SetPolicy"] != 0 {
		t.Errorf("migrated set changed GCP: %v", cloud.Calls)
	}
	if deleted, _ := m.Reap(context.Background()); len(deleted) != 0 {
		t.Errorf("reaped the old pool's keys: %v", deleted)
	}
}

func TestAmbientIdentityResolution(t *testing.T) {
	ctx := context.Background()
	me := gcpsets.Admin{Name: "me", CredentialSource: gcpsets.SourceAmbient}
	fakeADC := func(raw string) gcpsets.ADC {
		return func(ctx context.Context, scopes ...string) (*google.Credentials, error) {
			if raw == "" {
				return &google.Credentials{}, nil
			}
			return google.CredentialsFromJSON(ctx, []byte(raw), scopes...)
		}
	}
	// A container whose ADC is a service-account key file.
	g := gcpsets.GoogleConnector{ADC: fakeADC(`{"type":"service_account","client_email":"ctr@p.iam.gserviceaccount.com","private_key":"x"}`)}
	if id, err := g.Identity(ctx, me); err != nil || id != "ctr@p.iam.gserviceaccount.com" {
		t.Errorf("service-account ADC: %q %v", id, err)
	}
	// Workload identity impersonating a service account.
	g = gcpsets.GoogleConnector{ADC: fakeADC(`{"type":"external_account","audience":"//iam.googleapis.com/x","subject_token_type":"urn:ietf:params:oauth:token-type:jwt","token_url":"https://sts.googleapis.com/v1/token","credential_source":{"file":"/dev/null"},"service_account_impersonation_url":"https://iamcredentials.googleapis.com/v1/projects/-/serviceAccounts/wi@p.iam.gserviceaccount.com:generateAccessToken"}`)}
	if id, err := g.Identity(ctx, me); err != nil || id != "wi@p.iam.gserviceaccount.com" {
		t.Errorf("workload-identity ADC: %q %v", id, err)
	}
	// The metadata server's identity, when there is no credentials file.
	g = gcpsets.GoogleConnector{ADC: fakeADC(""), MetadataEmail: func(context.Context) (string, error) { return "vm@p.iam.gserviceaccount.com", nil }}
	if id, err := g.Identity(ctx, me); err != nil || id != "vm@p.iam.gserviceaccount.com" {
		t.Errorf("metadata ADC: %q %v", id, err)
	}
	// No ambient credentials at all.
	g = gcpsets.GoogleConnector{ADC: func(context.Context, ...string) (*google.Credentials, error) { return nil, errors.New("not found") }}
	if _, err := g.Identity(ctx, me); err == nil || !strings.Contains(err.Error(), "no ambient credentials") {
		t.Errorf("no ADC: %v", err)
	}
	// A configured admin resolves to its key's own account.
	if id, err := (gcpsets.GoogleConnector{}).Identity(ctx, gcpsets.Admin{Name: "m", CredentialSource: gcpsets.SourceConfigured, Credential: `{"type":"service_account","client_email":"m@p.iam.gserviceaccount.com","private_key":"x"}`}); err != nil || id != "m@p.iam.gserviceaccount.com" {
		t.Errorf("configured: %q %v", id, err)
	}
}
