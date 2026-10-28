package gcpkey

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/model/sqlite"
)

// TestKeyProviderLooksTheKeyUpByNameForTheDeployment is stage 4e of the
// single-tenant reset: gcp-key:<name> mints with the deployment's key of
// that name whatever team the task carries, and a name the deployment has
// no key for is refused for every task alike.
func TestKeyProviderLooksTheKeyUpByNameForTheDeployment(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	db, err := sqlite.Open(sqlite.DefaultConfig(t.TempDir()))
	if err != nil {
		t.Fatalf("opening sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	store := model.New(db)
	if err := store.Init(ctx); err != nil {
		t.Fatalf("init store: %v", err)
	}

	if err := store.PutGCPKey(ctx, model.GCPKey{
		Name:                "deploy",
		ProjectID:           "proj-a",
		ServiceAccountEmail: "sa-a@proj-a.iam.gserviceaccount.com",
		ServiceAccountCount: 1,
		MinterCredential:    "minter-a",
	}); err != nil {
		t.Fatalf("PutGCPKey: %v", err)
	}

	dyn := DynamicProvider(store)

	if _, ok := dyn("gcp-key:unknown"); ok {
		t.Error("expected unknown key to return false")
	}

	prov, ok := dyn("gcp-key:deploy")
	if !ok || prov == nil {
		t.Fatal("expected deploy provider to be found")
	}

	spec := prov.Spec()
	if spec.Name != "gcp-key:deploy" {
		t.Errorf("Spec.Name = %q, want gcp-key:deploy", spec.Name)
	}
	if len(spec.Requires) != 1 || spec.Requires[0] != "minter-a" {
		t.Errorf("Spec.Requires = %v, want [minter-a]", spec.Requires)
	}

	minter := newFakeMinter()
	kp := prov.(*KeyProvider)
	kp.NewMinter = func(ctx context.Context, credentialJSON string) (Minter, error) {
		return minter, nil
	}

	creds := &fakeCredentials{material: map[string]string{
		"minter-a": `{"client_email":"minter-a@proj-a"}`,
	}}

	// A task names no team: every task mints with the deployment's key.
	{
		cc := model.CapabilityContext{
			Task:        model.Task{},
			Now:         time.Now(),
			Credentials: creds,
		}
		res, err := prov.Resolve(ctx, cc)
		if err != nil || res.Refused {
			t.Fatalf("Resolve failed: %v, res: %+v", err, res)
		}
		mat, err := prov.Materialize(ctx, cc)
		if err != nil {
			t.Fatalf("Materialize failed: %v", err)
		}
		if mat.Lease.MintedBy.Name != "minter-a" {
			t.Errorf("Lease.MintedBy.Name = %q, want minter-a", mat.Lease.MintedBy.Name)
		}
	}
	for _, seen := range minter.accountsSeen {
		if seen != "projects/proj-a/serviceAccounts/sa-a@proj-a.iam.gserviceaccount.com" {
			t.Errorf("minter saw account %q", seen)
		}
	}

	// A key deleted after its provider was handed out is refused by name.
	if err := store.DeleteGCPKey(ctx, "deploy"); err != nil {
		t.Fatalf("DeleteGCPKey: %v", err)
	}
	res, err := prov.Resolve(ctx, model.CapabilityContext{Task: model.Task{}, Now: time.Now(), Credentials: creds})
	if err != nil {
		t.Fatalf("Resolve after delete: %v", err)
	}
	if !res.Refused || !strings.Contains(res.Reason, "this deployment has not configured") {
		t.Errorf("Resolve after delete = %+v, want the deployment's missing-key refusal", res)
	}
	if _, ok := dyn("gcp-key:deploy"); ok {
		t.Error("a deleted key still has a provider")
	}
}
