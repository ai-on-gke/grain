package gcpkey

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/model"
)

// --- fakes -----------------------------------------------------------

// fakeMinter is an in-memory Minter, standing in for a real GCP project --
// the same "no sandbox and no cloud" bar model/capability_test.go's own
// test capabilities hold to.
type fakeMinter struct {
	nextID       int
	keys         map[string]KeyInfo            // id -> info
	accountKeys  map[string]map[string]KeyInfo // account -> (id -> info)
	createErr    error
	createErrs   map[string]error // account -> error
	deleteErr    map[string]error // id -> error, for one bad delete
	listErr      error
	listErrs     map[string]error // account -> error
	minterSeen   string           // last credential material CreateKey/DeleteKey/ListKeys was authenticated with
	accountsSeen []string
}

func newFakeMinter() *fakeMinter {
	return &fakeMinter{keys: map[string]KeyInfo{}, accountKeys: map[string]map[string]KeyInfo{}}
}

func (m *fakeMinter) CreateKey(ctx context.Context, account string) (string, string, error) {
	m.accountsSeen = append(m.accountsSeen, account)
	if err, ok := m.createErrs[account]; ok {
		return "", "", err
	}
	if m.createErr != nil {
		return "", "", m.createErr
	}
	m.nextID++
	id := "key-" + string(rune('a'-1+m.nextID))
	info := KeyInfo{ID: id, CreatedAt: time.Now()}
	m.keys[id] = info
	if m.accountKeys == nil {
		m.accountKeys = map[string]map[string]KeyInfo{}
	}
	if m.accountKeys[account] == nil {
		m.accountKeys[account] = map[string]KeyInfo{}
	}
	m.accountKeys[account][id] = info
	return id, `{"private_key_id":"` + id + `"}`, nil
}

func (m *fakeMinter) DeleteKey(ctx context.Context, account, keyID string) error {
	if err, ok := m.deleteErr[keyID]; ok {
		return err
	}
	if len(m.accountKeys) > 0 {
		if m.accountKeys[account] != nil {
			if _, ok := m.accountKeys[account][keyID]; ok {
				delete(m.accountKeys[account], keyID)
				delete(m.keys, keyID)
				return nil
			}
		}
		return errors.New("no such key on account " + account)
	}
	if _, ok := m.keys[keyID]; !ok {
		return errors.New("no such key")
	}
	delete(m.keys, keyID)
	return nil
}

func (m *fakeMinter) ListKeys(ctx context.Context, account string) ([]KeyInfo, error) {
	if err, ok := m.listErrs[account]; ok {
		return nil, err
	}
	if m.listErr != nil {
		return nil, m.listErr
	}
	if m.accountKeys != nil && m.accountKeys[account] != nil {
		out := make([]KeyInfo, 0, len(m.accountKeys[account]))
		for _, k := range m.accountKeys[account] {
			out = append(out, k)
		}
		return out, nil
	}
	out := make([]KeyInfo, 0, len(m.keys))
	for _, k := range m.keys {
		out = append(out, k)
	}
	return out, nil
}

// fakeCredentials resolves exactly the names it is seeded with, recording
// what was asked for -- CredentialResolver's own contract is "what a
// provider can reach is exactly what it names", so a test asserting on
// Resolved is asserting that contract held.
type fakeCredentials struct {
	material map[string]string
	err      error
	resolved []string
}

func (c *fakeCredentials) Resolve(ctx context.Context, name string) (string, error) {
	c.resolved = append(c.resolved, name)
	if c.err != nil {
		return "", c.err
	}
	v, ok := c.material[name]
	if !ok {
		return "", errors.New("no such credential: " + name)
	}
	return v, nil
}

func testProvider(minter *fakeMinter) *Provider {
	p := &Provider{
		Config: Config{
			ServiceAccountEmail: "agent@example-project.iam.gserviceaccount.com",
			ProjectID:           "example-project",
		},
	}
	p.NewMinter = func(ctx context.Context, credentialJSON string) (Minter, error) {
		minter.minterSeen = credentialJSON
		return minter, nil
	}
	return p
}

func testContext(creds model.CredentialResolver, now time.Time) model.CapabilityContext {
	return model.CapabilityContext{
		Task:        model.Task{ID: "t1"},
		Run:         model.Run{ID: "t1-1", TaskID: "t1"},
		Now:         now,
		Credentials: creds,
	}
}

// --- Spec / Resolve ----------------------------------------------------

func TestSpec(t *testing.T) {
	p := NewProvider(Config{MaxKeyAge: 2 * time.Hour})
	spec := p.Spec()
	if spec.Name != "gcp-key" {
		t.Errorf("Name = %q, want gcp-key", spec.Name)
	}
	if spec.Provision != model.ProvisionMint {
		t.Errorf("Provision = %q, want mint", spec.Provision)
	}
	if spec.MaxLease != 2*time.Hour {
		t.Errorf("MaxLease = %v, want the configured 2h", spec.MaxLease)
	}
}

func TestSpecMaxLeaseDefaultsTo24Hours(t *testing.T) {
	p := NewProvider(Config{})
	if got := p.Spec().MaxLease; got != 24*time.Hour {
		t.Errorf("MaxLease = %v, want 24h default", got)
	}
}

func TestResolveRefusesWhenUnconfigured(t *testing.T) {
	p := NewProvider(Config{})
	res, err := p.Resolve(context.Background(), model.CapabilityContext{})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Refused || res.Reason == "" {
		t.Fatalf("got %+v, want a refusal with a human-facing reason", res)
	}
	// A Reason is posted to the task verbatim, so it has to name
	// something that exists: this one named `grain controller configure
	// --gcp-project-id`, which no build of grain has ever had.
	if !strings.Contains(res.Reason, "grain settings -gcp-project") {
		t.Errorf("reason %q does not name the command that actually sets the project", res.Reason)
	}
	if strings.Contains(res.Reason, "labelled") {
		t.Errorf("reason %q still speaks of issue labels, which v2 has none of", res.Reason)
	}
}

func TestResolveHonoursWhenConfigured(t *testing.T) {
	p := testProvider(newFakeMinter())
	creds := &fakeCredentials{material: map[string]string{DefaultMinterCredential: "minter-key-material"}}
	res, err := p.Resolve(context.Background(), testContext(creds, time.Now()))
	if err != nil {
		t.Fatal(err)
	}
	if res.Refused {
		t.Fatalf("got %+v, want honoured", res)
	}
}

// A deployment with both settings filled in and the minter secret never
// pasted in is the half-wired case Resolve exists to park rather than
// let Materialize discover: "materializing capabilities: gcpkey:
// resolving minter credential ..." is grain describing its own
// internals, where a task's own comment should name the secret to set.
func TestResolveRefusesWhenTheMinterCredentialIsUnset(t *testing.T) {
	p := testProvider(newFakeMinter())
	creds := &fakeCredentials{material: map[string]string{}}
	res, err := p.Resolve(context.Background(), testContext(creds, time.Now()))
	if err != nil {
		t.Fatal(err)
	}
	if !res.Refused {
		t.Fatal("expected a refusal when the minter credential resolves to nothing")
	}
	if !strings.Contains(res.Reason, DefaultMinterCredential) {
		t.Errorf("reason %q does not name the credential an operator has to set", res.Reason)
	}
	if !strings.Contains(res.Reason, "grain secrets set") {
		t.Errorf("reason %q does not name the command that sets it", res.Reason)
	}
}

func TestResolveNamesTheConfiguredMinterCredential(t *testing.T) {
	p := testProvider(newFakeMinter())
	p.Config.MinterCredential = "custom-minter"
	res, err := p.Resolve(context.Background(), testContext(&fakeCredentials{}, time.Now()))
	if err != nil {
		t.Fatal(err)
	}
	if !res.Refused || !strings.Contains(res.Reason, "custom-minter") {
		t.Errorf("got %+v, want a refusal naming this deployment's own minter credential", res)
	}
}

func TestResolveRefusesWithNoResolverAtAll(t *testing.T) {
	p := testProvider(newFakeMinter())
	res, err := p.Resolve(context.Background(), model.CapabilityContext{})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Refused {
		t.Fatal("expected a refusal rather than a panic when no resolver was wired up")
	}
}

// --- Materialize ---------------------------------------------------------

func TestMaterializeMintsAndPlacesTheKey(t *testing.T) {
	minter := newFakeMinter()
	p := testProvider(minter)
	creds := &fakeCredentials{material: map[string]string{DefaultMinterCredential: "minter-key-material"}}
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)

	m, err := p.Materialize(context.Background(), testContext(creds, now))
	if err != nil {
		t.Fatal(err)
	}

	if len(m.Placements) != 1 {
		t.Fatalf("placements = %+v, want exactly one", m.Placements)
	}
	placement := m.Placements[0]
	if placement.Side != model.SideSandbox {
		t.Errorf("side = %s, want sandbox", placement.Side)
	}
	if placement.Path != SandboxKeyPath {
		t.Errorf("path = %s, want %s", placement.Path, SandboxKeyPath)
	}
	if placement.EffectiveMode() != "600" {
		t.Errorf("mode = %s, want the safe default 600", placement.EffectiveMode())
	}
	if placement.Content == "" {
		t.Error("placement carries no key material")
	}

	if m.Lease == nil {
		t.Fatal("expected a lease")
	}
	if m.Lease.Capability != "gcp-key" {
		t.Errorf("lease capability = %s, want gcp-key", m.Lease.Capability)
	}
	if m.Lease.MintedBy.Name != DefaultMinterCredential {
		t.Errorf("lease minted-by = %s, want %s", m.Lease.MintedBy.Name, DefaultMinterCredential)
	}
	if m.Lease.ExpiresAt == nil || !m.Lease.ExpiresAt.Equal(now.Add(24*time.Hour)) {
		t.Errorf("lease expiry = %v, want now+24h", m.Lease.ExpiresAt)
	}
	if len(minter.keys) != 1 {
		t.Fatalf("fake project has %d keys, want exactly one minted", len(minter.keys))
	}
	if minter.minterSeen != "minter-key-material" {
		t.Errorf("minter authenticated with %q, want the resolved minter credential", minter.minterSeen)
	}
	if len(creds.resolved) != 1 || creds.resolved[0] != DefaultMinterCredential {
		t.Errorf("resolved credentials = %v, want exactly [%s]", creds.resolved, DefaultMinterCredential)
	}
}

func TestMaterializeUsesConfiguredMinterCredentialName(t *testing.T) {
	minter := newFakeMinter()
	p := testProvider(minter)
	p.Config.MinterCredential = "custom-minter"
	creds := &fakeCredentials{material: map[string]string{"custom-minter": "x"}}

	_, err := p.Materialize(context.Background(), testContext(creds, time.Now()))
	if err != nil {
		t.Fatal(err)
	}
	if len(creds.resolved) != 1 || creds.resolved[0] != "custom-minter" {
		t.Errorf("resolved = %v, want [custom-minter]", creds.resolved)
	}
}

func TestMaterializeFailsIfCredentialUnresolvable(t *testing.T) {
	p := testProvider(newFakeMinter())
	creds := &fakeCredentials{err: errors.New("no such secret")}
	_, err := p.Materialize(context.Background(), testContext(creds, time.Now()))
	if err == nil {
		t.Fatal("expected an error when the minter credential cannot be resolved")
	}
}

func TestMaterializeFailsIfMintFails(t *testing.T) {
	minter := newFakeMinter()
	minter.createErr = errors.New("quota exceeded")
	p := testProvider(minter)
	creds := &fakeCredentials{material: map[string]string{DefaultMinterCredential: "x"}}
	_, err := p.Materialize(context.Background(), testContext(creds, time.Now()))
	if err == nil {
		t.Fatal("expected an error when minting fails")
	}
}

// refusedCredentialError is what a stored minter key GCP no longer holds
// the public half of actually produces -- an oauth2 transport error with
// the token endpoint's own body quoted inside it, no googleapi.Error and
// no status code anywhere errors.As can reach. Task 163's failure,
// verbatim but for the project name.
var refusedCredentialError = errors.New(
	`Post "https://iam.googleapis.com/v1/projects/example-project/serviceAccounts/` +
		`agent@example-project.iam.gserviceaccount.com/keys?alt=json&prettyPrint=false": ` +
		`auth: cannot fetch token: 400 Response: ` +
		`{"error":"invalid_grant","error_description":"Invalid JWT Signature."}`)

// The failure this whole explanation exists for. What an operator saw
// was Google's `invalid_grant`, which names no secret, no account and
// nothing about grain -- on a deployment reading **Ready**, because the
// secret is set and only GCP knows the key inside it has stopped
// working.
func TestMaterializeNamesTheSecretWhenGCPRefusesTheMinterCredential(t *testing.T) {
	minter := newFakeMinter()
	minter.createErr = refusedCredentialError
	p := testProvider(minter)
	creds := &fakeCredentials{material: map[string]string{DefaultMinterCredential: "x"}}

	_, err := p.Materialize(context.Background(), testContext(creds, time.Now()))
	if err == nil {
		t.Fatal("expected a refused credential to fail the mint")
	}
	if !strings.Contains(err.Error(), DefaultMinterCredential) {
		t.Errorf("error %q does not name the secret holding the dead credential", err)
	}
	if !strings.Contains(err.Error(), "grain secrets set") {
		t.Errorf("error %q does not name the command that replaces it", err)
	}
	// Never worse than the original: GCP's own words survive.
	if !strings.Contains(err.Error(), "invalid_grant") {
		t.Errorf("error %q dropped GCP's own message", err)
	}
}

// Revoke authenticates as whatever the lease itself names, so a lease
// minted under a renamed or rotated credential has to report *that*
// name -- pointing an operator at Config's current one would send them
// to replace a secret that is not the one this failed on.
func TestRevokeNamesTheLeasesOwnCredentialWhenGCPRefusesIt(t *testing.T) {
	minter := newFakeMinter()
	minter.keys["key-a"] = KeyInfo{ID: "key-a"}
	minter.deleteErr = map[string]error{"key-a": refusedCredentialError}
	p := testProvider(minter)
	creds := &fakeCredentials{material: map[string]string{"older-minter": "x"}}

	err := p.Revoke(context.Background(), testContext(creds, time.Now()), model.Lease{
		Capability: "gcp-key", Resource: "key-a",
		MintedBy: model.CredentialRef{Name: "older-minter"},
	})
	if err == nil {
		t.Fatal("expected a refused credential to fail the revoke")
	}
	if !strings.Contains(err.Error(), "older-minter") {
		t.Errorf("error %q does not name the credential this lease was minted under", err)
	}
}

// Anything else is passed through as it is: wrapping every failure in
// advice about a dead credential would bury the ones that say what they
// are, the same restraint explainCreateFailure shows.
func TestAnOrdinaryFailureIsNotBlamedOnTheCredential(t *testing.T) {
	minter := newFakeMinter()
	minter.createErr = errors.New("googleapi: Error 500: backend error")
	p := testProvider(minter)
	creds := &fakeCredentials{material: map[string]string{DefaultMinterCredential: "x"}}

	_, err := p.Materialize(context.Background(), testContext(creds, time.Now()))
	if err == nil {
		t.Fatal("expected the mint to fail")
	}
	if strings.Contains(err.Error(), "grain secrets set") {
		t.Errorf("error %q volunteers a credential replacement for a failure that is not one", err)
	}
}

// The mint path used to name the account twice -- iam.go's own
// "minting a key for <account>" wrapped again by Materialize's -- so
// forty duplicated characters of resource name stood between an
// operator and the clause that said what had gone wrong.
func TestMintFailureNamesTheAccountOnlyOnce(t *testing.T) {
	f := newFakeIAM()
	f.createStatus = http.StatusInternalServerError
	f.createBody = `{"error":{"code":500,"message":"backend error","status":"INTERNAL"}}`
	m := testIAMMinter(t, f)
	p := testProvider(newFakeMinter())
	p.NewMinter = func(ctx context.Context, credentialJSON string) (Minter, error) { return m, nil }
	creds := &fakeCredentials{material: map[string]string{DefaultMinterCredential: "x"}}

	_, err := p.Materialize(context.Background(), testContext(creds, time.Now()))
	if err == nil {
		t.Fatal("expected the mint to fail")
	}
	if n := strings.Count(err.Error(), "gcpkey: minting a key for"); n != 1 {
		t.Errorf("error %q says %q %d times, want once", err, "gcpkey: minting a key for", n)
	}
}

func TestAccountNamesFixedNumberingScheme(t *testing.T) {
	// Single account (count <= 1)
	p1 := NewProvider(Config{
		ProjectID:           "test-proj",
		ServiceAccountEmail: "grain-agent@test-proj.iam.gserviceaccount.com",
		ServiceAccountCount: 1,
	})
	if accs := p1.accounts(); len(accs) != 1 || accs[0] != "projects/test-proj/serviceAccounts/grain-agent@test-proj.iam.gserviceaccount.com" {
		t.Errorf("accounts() = %v, want single account", accs)
	}
	if emails := p1.accountEmails(); len(emails) != 1 || emails[0] != "grain-agent@test-proj.iam.gserviceaccount.com" {
		t.Errorf("accountEmails() = %v, want single email", emails)
	}

	// Multiple accounts (count = 3) with prefix@domain
	p3 := NewProvider(Config{
		ProjectID:           "test-proj",
		ServiceAccountEmail: "grain-agent@test-proj.iam.gserviceaccount.com",
		ServiceAccountCount: 3,
	})
	wantEmails := []string{
		"grain-agent-0@test-proj.iam.gserviceaccount.com",
		"grain-agent-1@test-proj.iam.gserviceaccount.com",
		"grain-agent-2@test-proj.iam.gserviceaccount.com",
	}
	wantAccs := []string{
		"projects/test-proj/serviceAccounts/grain-agent-0@test-proj.iam.gserviceaccount.com",
		"projects/test-proj/serviceAccounts/grain-agent-1@test-proj.iam.gserviceaccount.com",
		"projects/test-proj/serviceAccounts/grain-agent-2@test-proj.iam.gserviceaccount.com",
	}
	if emails := p3.accountEmails(); !slicesEqual(emails, wantEmails) {
		t.Errorf("accountEmails() = %v, want %v", emails, wantEmails)
	}
	if accs := p3.accounts(); !slicesEqual(accs, wantAccs) {
		t.Errorf("accounts() = %v, want %v", accs, wantAccs)
	}

	// Multiple accounts with trailing hyphen prefix (prefix-@domain)
	pHyphen := NewProvider(Config{
		ProjectID:           "test-proj",
		ServiceAccountEmail: "grain-agent-@test-proj.iam.gserviceaccount.com",
		ServiceAccountCount: 3,
	})
	if emails := pHyphen.accountEmails(); !slicesEqual(emails, wantEmails) {
		t.Errorf("accountEmails() with trailing hyphen = %v, want %v", emails, wantEmails)
	}
	if accs := pHyphen.accounts(); !slicesEqual(accs, wantAccs) {
		t.Errorf("accounts() with trailing hyphen = %v, want %v", accs, wantAccs)
	}

	// Multiple accounts with trailing -0 index suffix (prefix-0@domain)
	pZero := NewProvider(Config{
		ProjectID:           "test-proj",
		ServiceAccountEmail: "grain-agent-0@test-proj.iam.gserviceaccount.com",
		ServiceAccountCount: 3,
	})
	if emails := pZero.accountEmails(); !slicesEqual(emails, wantEmails) {
		t.Errorf("accountEmails() with trailing -0 suffix = %v, want %v", emails, wantEmails)
	}
	if accs := pZero.accounts(); !slicesEqual(accs, wantAccs) {
		t.Errorf("accounts() with trailing -0 suffix = %v, want %v", accs, wantAccs)
	}

	// Bare name without @
	pBare := NewProvider(Config{
		ProjectID:           "test-proj",
		ServiceAccountEmail: "grain-agent",
		ServiceAccountCount: 2,
	})
	wantBareEmails := []string{
		"grain-agent-0@test-proj.iam.gserviceaccount.com",
		"grain-agent-1@test-proj.iam.gserviceaccount.com",
	}
	if emails := pBare.accountEmails(); !slicesEqual(emails, wantBareEmails) {
		t.Errorf("accountEmails() for bare name = %v, want %v", emails, wantBareEmails)
	}

	// Bare name with trailing hyphen (prefix-)
	pBareHyphen := NewProvider(Config{
		ProjectID:           "test-proj",
		ServiceAccountEmail: "grain-agent-",
		ServiceAccountCount: 2,
	})
	if emails := pBareHyphen.accountEmails(); !slicesEqual(emails, wantBareEmails) {
		t.Errorf("accountEmails() for bare name with trailing hyphen = %v, want %v", emails, wantBareEmails)
	}

	// Bare name with trailing -0 (prefix-0)
	pBareZero := NewProvider(Config{
		ProjectID:           "test-proj",
		ServiceAccountEmail: "grain-agent-0",
		ServiceAccountCount: 2,
	})
	if emails := pBareZero.accountEmails(); !slicesEqual(emails, wantBareEmails) {
		t.Errorf("accountEmails() for bare name with trailing -0 = %v, want %v", emails, wantBareEmails)
	}
}

func slicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestMaterializeWithMultipleAccountsTriesNextOnExhaustion(t *testing.T) {
	minter := newFakeMinter()
	p := testProvider(minter)
	p.Config.ServiceAccountCount = 3
	creds := &fakeCredentials{material: map[string]string{DefaultMinterCredential: "x"}}

	accs := p.accounts()
	acc0 := accs[0]
	acc1 := accs[1]

	// Account 0 is exhausted
	minter.createErrs = map[string]error{
		acc0: &model.ResourceExhaustedError{Resource: "GCP service-account keys", Err: errors.New("key cap reached")},
	}

	mat, err := p.Materialize(context.Background(), testContext(creds, time.Now()))
	if err != nil {
		t.Fatalf("Materialize failed: %v", err)
	}
	if mat.Lease == nil || mat.Lease.Resource == "" {
		t.Errorf("expected lease with valid resource, got %+v", mat.Lease)
	}
	// Account 1 should have received the key
	if len(minter.accountKeys[acc1]) != 1 {
		t.Errorf("expected key on account 1, got %v", minter.accountKeys[acc1])
	}
}

func TestMaterializeWithMultipleAccountsFailsWhenAllExhausted(t *testing.T) {
	minter := newFakeMinter()
	p := testProvider(minter)
	p.Config.ServiceAccountCount = 2
	creds := &fakeCredentials{material: map[string]string{DefaultMinterCredential: "x"}}

	accs := p.accounts()
	acc0 := accs[0]
	acc1 := accs[1]

	minter.createErrs = map[string]error{
		acc0: &model.ResourceExhaustedError{Resource: "GCP service-account keys", Err: errors.New("acc0 exhausted")},
		acc1: &model.ResourceExhaustedError{Resource: "GCP service-account keys", Err: errors.New("acc1 exhausted")},
	}

	_, err := p.Materialize(context.Background(), testContext(creds, time.Now()))
	if err == nil {
		t.Fatal("expected Materialize to fail when all accounts are exhausted")
	}
	if _, ok := model.ResourceExhausted(err); !ok {
		t.Errorf("expected ResourceExhaustedError, got %T: %v", err, err)
	}
}

func TestMaterializeWithMultipleAccountsFailsImmediatelyOnFatalError(t *testing.T) {
	minter := newFakeMinter()
	p := testProvider(minter)
	p.Config.ServiceAccountCount = 3
	creds := &fakeCredentials{material: map[string]string{DefaultMinterCredential: "x"}}

	accs := p.accounts()
	acc0 := accs[0]

	// Fatal error on account 0 (not resource exhaustion)
	minter.createErrs = map[string]error{
		acc0: errors.New("googleapi: Error 403: permission denied"),
	}

	_, err := p.Materialize(context.Background(), testContext(creds, time.Now()))
	if err == nil {
		t.Fatal("expected Materialize to fail immediately on fatal error")
	}
	if _, ok := model.ResourceExhausted(err); ok {
		t.Errorf("did not expect ResourceExhaustedError for 403, got %v", err)
	}
	// Account 1 and 2 should not have been tried
	if len(minter.accountsSeen) != 1 {
		t.Errorf("expected exactly 1 account attempted before aborting, saw %d: %v", len(minter.accountsSeen), minter.accountsSeen)
	}
}

func TestRevokeAcrossMultipleAccounts(t *testing.T) {
	minter := newFakeMinter()
	p := testProvider(minter)
	p.Config.ServiceAccountCount = 3
	creds := &fakeCredentials{material: map[string]string{DefaultMinterCredential: "x"}}

	acc1 := p.accounts()[1]
	minter.accountKeys = map[string]map[string]KeyInfo{
		acc1: {
			"key-test": {ID: "key-test", CreatedAt: time.Now()},
		},
	}
	minter.keys = map[string]KeyInfo{"key-test": {ID: "key-test", CreatedAt: time.Now()}}

	err := p.Revoke(context.Background(), testContext(creds, time.Now()), model.Lease{
		Capability: "gcp-key",
		Resource:   "key-test",
		MintedBy:   model.CredentialRef{Name: DefaultMinterCredential},
	})
	if err != nil {
		t.Fatalf("Revoke failed: %v", err)
	}
	if len(minter.accountKeys[acc1]) != 0 {
		t.Errorf("expected key to be deleted from account 1, remaining: %v", minter.accountKeys[acc1])
	}
}

func TestReapAcrossMultipleAccounts(t *testing.T) {
	minter := newFakeMinter()
	p := testProvider(minter)
	p.Config.ServiceAccountCount = 2
	creds := &fakeCredentials{material: map[string]string{DefaultMinterCredential: "x"}}

	accs := p.accounts()
	acc0 := accs[0]
	acc1 := accs[1]

	old := time.Now().Add(-2 * time.Hour)
	fresh := time.Now()

	minter.accountKeys = map[string]map[string]KeyInfo{
		acc0: {
			"key-old-0":   {ID: "key-old-0", CreatedAt: old},
			"key-fresh-0": {ID: "key-fresh-0", CreatedAt: fresh},
		},
		acc1: {
			"key-old-1":   {ID: "key-old-1", CreatedAt: old},
			"key-fresh-1": {ID: "key-fresh-1", CreatedAt: fresh},
		},
	}

	deleted, err := p.Reap(context.Background(), creds, time.Now().Add(-1*time.Hour))
	if err != nil {
		t.Fatalf("Reap failed: %v", err)
	}
	if len(deleted) != 2 {
		t.Errorf("expected 2 deleted keys across accounts, got %v", deleted)
	}
	if len(minter.accountKeys[acc0]) != 1 || minter.accountKeys[acc0]["key-fresh-0"].ID == "" {
		t.Errorf("expected fresh key retained in acc0, got %v", minter.accountKeys[acc0])
	}
	if len(minter.accountKeys[acc1]) != 1 || minter.accountKeys[acc1]["key-fresh-1"].ID == "" {
		t.Errorf("expected fresh key retained in acc1, got %v", minter.accountKeys[acc1])
	}
}

func TestCheckCredentialAcrossMultipleAccounts(t *testing.T) {
	minter := newFakeMinter()
	p := testProvider(minter)
	p.Config.ServiceAccountCount = 2
	creds := &fakeCredentials{material: map[string]string{DefaultMinterCredential: "x"}}

	accs := p.accounts()
	acc0 := accs[0]
	acc1 := accs[1]

	minter.accountKeys = map[string]map[string]KeyInfo{
		acc0: {"k1": {ID: "k1"}},
		acc1: {"k2": {ID: "k2"}, "k3": {ID: "k3"}},
	}

	check, err := p.CheckCredential(context.Background(), creds)
	if err != nil {
		t.Fatalf("CheckCredential failed: %v", err)
	}
	if !strings.Contains(check.Detail, "3 user-managed key(s) across 2 service accounts") {
		t.Errorf("detail %q does not match expected multi-account summary", check.Detail)
	}
}

// Two keys held by one task must land in two files: the deployment key
// and every named key all used to share SandboxKeyPath, so the last one
// placed silently replaced the rest (grain/task-3770).
func TestMaterializeGivesEachNamedKeyItsOwnPath(t *testing.T) {
	creds := &fakeCredentials{material: map[string]string{
		DefaultMinterCredential:   "minter-key-material",
		"gcp-key-minter-deploy":   "deploy-minter-key-material",
		"gcp-key-minter-analysis": "analysis-minter-key-material",
	}}
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	seen := map[string]string{}
	for _, name := range []string{"", "deploy", "analysis"} {
		p := testProvider(newFakeMinter())
		p.Config.Name = name
		m, err := p.Materialize(context.Background(), testContext(creds, now))
		if err != nil {
			t.Fatalf("materializing %q: %v", name, err)
		}
		if len(m.Placements) != 1 {
			t.Fatalf("%q: placements = %+v, want exactly one", name, m.Placements)
		}
		path := m.Placements[0].Path
		if path != KeyPath(name) {
			t.Errorf("%q placed at %s, want %s", name, path, KeyPath(name))
		}
		if other, ok := seen[path]; ok {
			t.Errorf("%q and %q both placed at %s", other, name, path)
		}
		seen[path] = name
		text, err := p.PromptSection(context.Background(), model.CapabilityContext{}, m.Placements)
		if err != nil {
			t.Fatal(err)
		}
		if !contains(text, path) {
			t.Errorf("prompt section for %q = %q, want it to name %s", name, text, path)
		}
		if name != "" && !contains(text, "gcp-key:"+name) {
			t.Errorf("prompt section for %q = %q, want it to say which key it is", name, text)
		}
	}
	if KeyPath("") != SandboxKeyPath || KeyPath("default") != SandboxKeyPath {
		t.Errorf("the unnamed key moved: KeyPath(\"\") = %s, KeyPath(\"default\") = %s", KeyPath(""), KeyPath("default"))
	}
	if got, want := KeyPath("deploy"), "/home/debian/.gcp-service-account-deploy.json"; got != want {
		t.Errorf("KeyPath(deploy) = %s, want %s", got, want)
	}
}

func TestMaterializeRefusesANameThatIsNotAFileName(t *testing.T) {
	p := testProvider(newFakeMinter())
	p.Config.Name = "../escape"
	creds := &fakeCredentials{material: map[string]string{"gcp-key-minter-../escape": "x"}}
	if _, err := p.Materialize(context.Background(), testContext(creds, time.Now())); err == nil {
		t.Fatal("expected a key name with a path separator to be refused")
	}
}

// --- PromptSection ---------------------------------------------------

func TestPromptSectionNamesThePathAndNoSecret(t *testing.T) {
	p := testProvider(newFakeMinter())
	text, err := p.PromptSection(context.Background(), model.CapabilityContext{}, []model.Placement{
		{Side: model.SideSandbox, Path: SandboxKeyPath, Content: "super-secret-private-key"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !contains(text, SandboxKeyPath) {
		t.Errorf("prompt section = %q, want it to name %s", text, SandboxKeyPath)
	}
	if contains(text, "super-secret-private-key") {
		t.Fatalf("prompt section leaked key material: %q", text)
	}
}

// A key with no project selected is a key every gcloud command fails on,
// with a message that names no credential -- see PromptSection's own doc
// comment.
func TestPromptSectionNamesTheProject(t *testing.T) {
	p := testProvider(newFakeMinter())
	text, err := p.PromptSection(context.Background(), model.CapabilityContext{}, []model.Placement{
		{Side: model.SideSandbox, Path: SandboxKeyPath, Content: "super-secret-private-key"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !contains(text, "gcloud config set project example-project") {
		t.Errorf("prompt section = %q, want it to set the project", text)
	}
	if !contains(text, "GOOGLE_CLOUD_PROJECT=example-project") {
		t.Errorf("prompt section = %q, want it to name the project for SDKs too", text)
	}
}

func TestNamedGCPKeyProviderSpecAndMaterialize(t *testing.T) {
	minter := newFakeMinter()
	p := NewProvider(Config{
		Name:                "deploy",
		ProjectID:           "prod-deploy",
		ServiceAccountEmail: "deployer@prod-deploy.iam.gserviceaccount.com",
		ServiceAccountCount: 2,
		MinterCredential:    "custom-minter-secret",
	})
	p.NewMinter = func(ctx context.Context, credentialJSON string) (Minter, error) {
		return minter, nil
	}

	spec := p.Spec()
	if spec.Name != "gcp-key:deploy" {
		t.Errorf("Spec.Name = %q, want %q", spec.Name, "gcp-key:deploy")
	}
	if spec.Label != "grain-gcp-key-deploy" {
		t.Errorf("Spec.Label = %q, want %q", spec.Label, "grain-gcp-key-deploy")
	}
	if len(spec.Requires) != 1 || spec.Requires[0] != "custom-minter-secret" {
		t.Errorf("Spec.Requires = %v, want [custom-minter-secret]", spec.Requires)
	}

	creds := &fakeCredentials{material: map[string]string{"custom-minter-secret": `{"client_email":"minter@proj"}`}}
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	mat, err := p.Materialize(context.Background(), testContext(creds, now))
	if err != nil {
		t.Fatalf("Materialize failed: %v", err)
	}
	if mat.Lease == nil || mat.Lease.Capability != "gcp-key:deploy" {
		t.Errorf("Lease.Capability = %v, want gcp-key:deploy", mat.Lease)
	}
	if mat.Lease.MintedBy.Name != "custom-minter-secret" {
		t.Errorf("Lease.MintedBy.Name = %q, want custom-minter-secret", mat.Lease.MintedBy.Name)
	}
}

func TestPromptSectionRejectsWrongPlacementCount(t *testing.T) {
	p := testProvider(newFakeMinter())
	if _, err := p.PromptSection(context.Background(), model.CapabilityContext{}, nil); err == nil {
		t.Fatal("expected an error for zero placements")
	}
}

func contains(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// --- Revoke ------------------------------------------------------------

func TestRevokeDeletesTheLeasedKey(t *testing.T) {
	minter := newFakeMinter()
	minter.keys["key-a"] = KeyInfo{ID: "key-a", CreatedAt: time.Now()}
	p := testProvider(minter)
	creds := &fakeCredentials{material: map[string]string{"minted-by-cred": "material"}}

	err := p.Revoke(context.Background(), testContext(creds, time.Now()), model.Lease{
		Resource: "key-a",
		MintedBy: model.CredentialRef{Name: "minted-by-cred"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, stillThere := minter.keys["key-a"]; stillThere {
		t.Error("key-a should have been deleted")
	}
	if len(creds.resolved) != 1 || creds.resolved[0] != "minted-by-cred" {
		t.Errorf("resolved = %v, want [minted-by-cred] -- Revoke should use the lease's own MintedBy", creds.resolved)
	}
}

func TestRevokePropagatesDeleteFailure(t *testing.T) {
	minter := newFakeMinter()
	minter.deleteErr = map[string]error{"key-a": errors.New("already gone")}
	p := testProvider(minter)
	creds := &fakeCredentials{material: map[string]string{"c": "material"}}

	err := p.Revoke(context.Background(), testContext(creds, time.Now()), model.Lease{
		Resource: "key-a", MintedBy: model.CredentialRef{Name: "c"},
	})
	if err == nil {
		t.Fatal("expected the delete failure to propagate")
	}
}

// --- Reap ----------------------------------------------------------------

func TestReapDeletesOnlyKeysBeforeBoundary(t *testing.T) {
	minter := newFakeMinter()
	boundary := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	minter.keys["old"] = KeyInfo{ID: "old", CreatedAt: boundary.Add(-time.Nanosecond)}
	minter.keys["boundary"] = KeyInfo{ID: "boundary", CreatedAt: boundary}
	minter.keys["fresh"] = KeyInfo{ID: "fresh", CreatedAt: boundary.Add(time.Hour)}
	minter.keys["unknown"] = KeyInfo{ID: "unknown"}

	p := testProvider(minter)
	creds := &fakeCredentials{material: map[string]string{DefaultMinterCredential: "x"}}

	deleted, err := p.Reap(context.Background(), creds, boundary)
	if err != nil {
		t.Fatal(err)
	}
	if len(deleted) != 1 || deleted[0] != "old" {
		t.Fatalf("deleted = %v, want exactly [old]", deleted)
	}
	for _, id := range []string{"boundary", "fresh", "unknown"} {
		if _, ok := minter.keys[id]; !ok {
			t.Errorf("%s should not have been reaped", id)
		}
	}
}

func TestReapReportsOneBadDeleteAndContinues(t *testing.T) {
	minter := newFakeMinter()
	boundary := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	minter.keys["bad"] = KeyInfo{ID: "bad", CreatedAt: boundary.Add(-time.Hour)}
	minter.keys["good"] = KeyInfo{ID: "good", CreatedAt: boundary.Add(-time.Hour)}
	minter.deleteErr = map[string]error{"bad": errors.New("transient GCP error")}

	p := testProvider(minter)
	creds := &fakeCredentials{material: map[string]string{DefaultMinterCredential: "x"}}

	deleted, err := p.Reap(context.Background(), creds, boundary)
	if err == nil {
		t.Fatal("one failed delete should remain visible")
	}
	if len(deleted) != 1 || deleted[0] != "good" {
		t.Fatalf("deleted = %v, want exactly [good] despite the other failure", deleted)
	}
	if _, stillThere := minter.keys["bad"]; !stillThere {
		t.Error("bad should still be present -- its delete failed")
	}
}

func TestReapDoesNotApplyConfiguredMaxAgeBehindBoundary(t *testing.T) {
	minter := newFakeMinter()
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	minter.keys["k"] = KeyInfo{ID: "k", CreatedAt: now.Add(-2 * time.Hour)}
	p := testProvider(minter)
	p.Config.MaxKeyAge = time.Hour
	creds := &fakeCredentials{material: map[string]string{DefaultMinterCredential: "x"}}

	deleted, err := p.Reap(context.Background(), creds, now.Add(-3*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(deleted) != 0 {
		t.Fatalf("deleted = %v; the shared boundary, not MaxKeyAge, controls reaping", deleted)
	}
}

func TestReapFailsIfListingFails(t *testing.T) {
	minter := newFakeMinter()
	minter.listErr = errors.New("network error")
	p := testProvider(minter)
	creds := &fakeCredentials{material: map[string]string{DefaultMinterCredential: "x"}}
	if _, err := p.Reap(context.Background(), creds, time.Now()); err == nil {
		t.Fatal("expected an error when listing keys fails")
	}
}

// --- CheckCredential --------------------------------------------------

// The success case is the one worth being fussy about: a check that
// reported nothing but "ok" would be as unfalsifiable as the **Ready**
// badge it exists to go beyond, so the detail has to carry the evidence
// -- which credential answered, and what it saw.
func TestCheckCredentialReportsWhatGCPAnswered(t *testing.T) {
	minter := newFakeMinter()
	minter.keys["key-a"] = KeyInfo{ID: "key-a", CreatedAt: time.Now()}
	minter.keys["key-b"] = KeyInfo{ID: "key-b", CreatedAt: time.Now()}
	p := testProvider(minter)
	creds := &fakeCredentials{material: map[string]string{DefaultMinterCredential: "minter-key-material"}}

	check, err := p.CheckCredential(context.Background(), creds)
	if err != nil {
		t.Fatalf("CheckCredential: %v", err)
	}
	if len(check.Credentials) != 1 || check.Credentials[0] != DefaultMinterCredential {
		t.Errorf("Credentials = %v, want just %q", check.Credentials, DefaultMinterCredential)
	}
	if !strings.Contains(check.Detail, "2 user-managed key(s)") {
		t.Errorf("detail %q does not say what GCP actually listed", check.Detail)
	}
	if !strings.Contains(check.Detail, p.Config.ServiceAccountEmail) {
		t.Errorf("detail %q does not name the account that was checked", check.Detail)
	}
	// The check is a listing and nothing else: an operator is expected to
	// press it repeatedly, and one that minted would leave a key behind
	// for the reaper every time.
	if len(minter.keys) != 2 {
		t.Errorf("the check changed the account's keys: %v", minter.keys)
	}
	if minter.minterSeen != "minter-key-material" {
		t.Errorf("authenticated with %q, want the minter credential's own material", minter.minterSeen)
	}
}

// The failure this whole action exists for: the secret is set, the
// project and account are set, and GCP no longer holds the public half
// of the key inside it. What comes back has to be the sentence naming
// the dead secret, not Google's bare invalid_grant.
func TestCheckCredentialNamesTheSecretWhenGCPRefusesIt(t *testing.T) {
	minter := newFakeMinter()
	minter.listErr = refusedCredentialError
	p := testProvider(minter)
	creds := &fakeCredentials{material: map[string]string{DefaultMinterCredential: "x"}}

	check, err := p.CheckCredential(context.Background(), creds)
	if err == nil {
		t.Fatal("expected a refused credential to fail the check")
	}
	if !strings.Contains(err.Error(), DefaultMinterCredential) {
		t.Errorf("error %q does not name the secret holding the dead credential", err)
	}
	if !strings.Contains(err.Error(), "grain secrets set") {
		t.Errorf("error %q does not name the command that replaces it", err)
	}
	if !strings.Contains(err.Error(), "invalid_grant") {
		t.Errorf("error %q dropped GCP's own message", err)
	}
	// Reported even on the failure path: the remedy is "replace what is
	// in this secret", so the caller has to be able to say which.
	if len(check.Credentials) != 1 || check.Credentials[0] != DefaultMinterCredential {
		t.Errorf("Credentials = %v, want the checked credential named even on failure", check.Credentials)
	}
}

func TestCheckCredentialFailsWhenUnconfigured(t *testing.T) {
	p := NewProvider(Config{})
	creds := &fakeCredentials{material: map[string]string{DefaultMinterCredential: "x"}}
	_, err := p.CheckCredential(context.Background(), creds)
	if err == nil {
		t.Fatal("expected a check against an unconfigured deployment to fail")
	}
	if !strings.Contains(err.Error(), "grain settings -gcp-project") {
		t.Errorf("error %q does not name what is missing or how to set it", err)
	}
}

func TestCheckCredentialFailsWhenTheSecretIsUnset(t *testing.T) {
	p := testProvider(newFakeMinter())
	_, err := p.CheckCredential(context.Background(), &fakeCredentials{material: map[string]string{}})
	if err == nil {
		t.Fatal("expected a check with no minter credential stored to fail")
	}
	if !strings.Contains(err.Error(), DefaultMinterCredential) {
		t.Errorf("error %q does not name the credential that is missing", err)
	}
}

// --- Reaper interface ------------------------------------------------

func TestProviderSatisfiesReaper(t *testing.T) {
	var _ model.Reaper = (*Provider)(nil)
}

func TestProviderSatisfiesCredentialChecker(t *testing.T) {
	var _ model.CredentialChecker = (*Provider)(nil)
}

func TestProviderSatisfiesCapabilityProvider(t *testing.T) {
	var _ model.CapabilityProvider = (*Provider)(nil)
}
