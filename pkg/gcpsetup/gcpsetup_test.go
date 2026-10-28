package gcpsetup

import (
	"context"
	"errors"
	"testing"

	"google.golang.org/api/googleapi"
)

// fakeAdmin is Admin with no network involved -- the same "no sandbox and
// no cloud" bar gcpkey_test.go's own fake Minter holds to. Each method
// consults denied to decide whether to return a 403 (StepManual) or
// succeed, and records what it was asked to do so tests can assert on
// call shape, not just the returned Result.
type fakeAdmin struct {
	denied map[string]bool // keyed by a short label the test controls, e.g. "enable:iam.googleapis.com"

	enabledAPIs      []string
	createdAccounts  []string
	serviceGrants    []string // "role@email <- member"
	projectGrants    []string // "role <- member"
	mintedKeyFor     string
	existingSAGrants map[string]bool // pre-seeded "role@email <- member" already granted
}

func newFakeAdmin() *fakeAdmin {
	return &fakeAdmin{denied: map[string]bool{}, existingSAGrants: map[string]bool{}}
}

func (f *fakeAdmin) deny(label string) { f.denied[label] = true }

func forbidden() error {
	return &googleapi.Error{Code: 403, Message: "permission denied"}
}

func (f *fakeAdmin) EnableAPI(ctx context.Context, name string) error {
	if f.denied["enable:"+name] {
		return forbidden()
	}
	f.enabledAPIs = append(f.enabledAPIs, name)
	return nil
}

func (f *fakeAdmin) EnsureServiceAccount(ctx context.Context, accountID, displayName, description string) error {
	if f.denied["account:"+accountID] {
		return forbidden()
	}
	f.createdAccounts = append(f.createdAccounts, accountID)
	return nil
}

func (f *fakeAdmin) GrantServiceAccountRole(ctx context.Context, email, role, member string) error {
	key := role + "@" + email + " <- " + member
	if f.denied["sa-grant:"+key] {
		return forbidden()
	}
	if f.existingSAGrants[key] {
		return nil
	}
	f.serviceGrants = append(f.serviceGrants, key)
	return nil
}

func (f *fakeAdmin) GrantProjectRole(ctx context.Context, role, member string) error {
	key := role + " <- " + member
	if f.denied["project-grant:"+key] {
		return forbidden()
	}
	f.projectGrants = append(f.projectGrants, key)
	return nil
}

func (f *fakeAdmin) CreateServiceAccountKey(ctx context.Context, email string) (string, error) {
	if f.denied["key:"+email] {
		return "", forbidden()
	}
	f.mintedKeyFor = email
	return `{"type":"service_account"}`, nil
}

func TestEnsureInfrastructureRequiresAProjectID(t *testing.T) {
	_, err := EnsureInfrastructure(context.Background(), newFakeAdmin(), Options{})
	if err == nil {
		t.Fatal("expected an error with no ProjectID")
	}
}

func TestEnsureInfrastructureHappyPathCreatesEverythingAndGrantsTheKeyAdminRole(t *testing.T) {
	admin := newFakeAdmin()
	result, err := EnsureInfrastructure(context.Background(), admin, Options{ProjectID: "proj"})
	if err != nil {
		t.Fatalf("EnsureInfrastructure: %v", err)
	}

	if result.AgentEmail != "grain-agent@proj.iam.gserviceaccount.com" {
		t.Errorf("AgentEmail = %q", result.AgentEmail)
	}
	if result.MinterEmail != "grain-gcp-key-minter@proj.iam.gserviceaccount.com" {
		t.Errorf("MinterEmail = %q", result.MinterEmail)
	}
	wantAPIs := []string{"iam.googleapis.com", "iamcredentials.googleapis.com"}
	if !equalStrings(admin.enabledAPIs, wantAPIs) {
		t.Errorf("enabledAPIs = %v, want %v (gemini APIs must stay off without EnableGeminiKey)", admin.enabledAPIs, wantAPIs)
	}
	if !equalStrings(admin.createdAccounts, []string{"grain-agent", "grain-gcp-key-minter"}) {
		t.Errorf("createdAccounts = %v", admin.createdAccounts)
	}
	wantGrant := "roles/iam.serviceAccountKeyAdmin@grain-agent@proj.iam.gserviceaccount.com <- serviceAccount:grain-gcp-key-minter@proj.iam.gserviceaccount.com"
	if !equalStrings(admin.serviceGrants, []string{wantGrant}) {
		t.Errorf("serviceGrants = %v, want [%s]", admin.serviceGrants, wantGrant)
	}
	if len(admin.projectGrants) != 0 {
		t.Errorf("projectGrants = %v, want none (EnableGeminiKey is off)", admin.projectGrants)
	}
	if admin.mintedKeyFor != "" || result.MinterKeyJSON != "" {
		t.Errorf("a key was minted without MintMinterKey being set")
	}
	for _, s := range result.Steps {
		if s.Status != StepDone {
			t.Errorf("step %q was %s, want done: %s", s.Name, s.Status, s.Detail)
		}
	}
	if result.AllManual() {
		t.Error("AllManual() = true on an all-succeeded run")
	}
}

func TestEnsureInfrastructureEnableGeminiKeyGrantsTheProjectRoleAndEnablesItsAPIs(t *testing.T) {
	admin := newFakeAdmin()
	result, err := EnsureInfrastructure(context.Background(), admin, Options{ProjectID: "proj", EnableGeminiKey: true})
	if err != nil {
		t.Fatalf("EnsureInfrastructure: %v", err)
	}
	for _, api := range []string{"generativelanguage.googleapis.com", "apikeys.googleapis.com"} {
		if !contains(admin.enabledAPIs, api) {
			t.Errorf("enabledAPIs = %v, missing %s", admin.enabledAPIs, api)
		}
	}
	wantGrant := "roles/serviceusage.apiKeysAdmin <- serviceAccount:grain-gcp-key-minter@proj.iam.gserviceaccount.com"
	if !equalStrings(admin.projectGrants, []string{wantGrant}) {
		t.Errorf("projectGrants = %v, want [%s]", admin.projectGrants, wantGrant)
	}
	_ = result
}

func TestEnsureInfrastructureMintMinterKeyReturnsTheRawJSON(t *testing.T) {
	admin := newFakeAdmin()
	result, err := EnsureInfrastructure(context.Background(), admin, Options{ProjectID: "proj", MintMinterKey: true})
	if err != nil {
		t.Fatalf("EnsureInfrastructure: %v", err)
	}
	if result.MinterKeyJSON == "" {
		t.Error("MinterKeyJSON is empty despite MintMinterKey")
	}
	if admin.mintedKeyFor != result.MinterEmail {
		t.Errorf("minted a key for %q, want %q", admin.mintedKeyFor, result.MinterEmail)
	}
}

func TestEnsureInfrastructureIsIdempotentAgainstAlreadyGrantedBindings(t *testing.T) {
	admin := newFakeAdmin()
	admin.existingSAGrants["roles/iam.serviceAccountKeyAdmin@grain-agent@proj.iam.gserviceaccount.com <- serviceAccount:grain-gcp-key-minter@proj.iam.gserviceaccount.com"] = true

	result, err := EnsureInfrastructure(context.Background(), admin, Options{ProjectID: "proj"})
	if err != nil {
		t.Fatalf("EnsureInfrastructure: %v", err)
	}
	if len(admin.serviceGrants) != 0 {
		t.Errorf("serviceGrants = %v, want none: the binding already existed", admin.serviceGrants)
	}
	// Still reported as done -- an operator watching this run should see
	// "granted (or already was)", not silence, for a binding it turns out
	// it didn't need to add.
	for _, s := range result.Steps {
		if s.Name == "grant grain-gcp-key-minter@proj.iam.gserviceaccount.com roles/iam.serviceAccountKeyAdmin on grain-agent@proj.iam.gserviceaccount.com" && s.Status != StepDone {
			t.Errorf("step status = %s, want done", s.Status)
		}
	}
}

func TestEnsureInfrastructureRecordsAPermissionDeniedStepAsManualAndContinues(t *testing.T) {
	admin := newFakeAdmin()
	admin.deny("account:grain-agent")

	result, err := EnsureInfrastructure(context.Background(), admin, Options{ProjectID: "proj"})
	if err != nil {
		t.Fatalf("EnsureInfrastructure: %v (a 403 must not abort the run)", err)
	}

	var accountStep, grantStep *Step
	for i := range result.Steps {
		s := &result.Steps[i]
		if s.Name == "create service account grain-agent" {
			accountStep = s
		}
		if s.Name == "grant grain-gcp-key-minter@proj.iam.gserviceaccount.com roles/iam.serviceAccountKeyAdmin on grain-agent@proj.iam.gserviceaccount.com" {
			grantStep = s
		}
	}
	if accountStep == nil || accountStep.Status != StepManual || accountStep.Command == "" {
		t.Fatalf("account step = %+v, want a StepManual with a Command", accountStep)
	}
	// The grant step still runs, naming the deterministic email, even
	// though creating the account it grants against was itself manual --
	// EnsureInfrastructure's whole point is to get as far as it can.
	if grantStep == nil || grantStep.Status != StepDone {
		t.Fatalf("grant step = %+v, want it to still run and succeed", grantStep)
	}
	if !contains(admin.createdAccounts, "grain-gcp-key-minter") {
		t.Error("the minter account was not created despite only the agent account being denied")
	}
}

func TestEnsureInfrastructureAllManualReportsAWhollyBlockedRun(t *testing.T) {
	admin := newFakeAdmin()
	for _, api := range requiredAPIs {
		admin.deny("enable:" + api)
	}
	admin.deny("account:grain-agent")
	admin.deny("account:grain-gcp-key-minter")
	admin.deny("sa-grant:roles/iam.serviceAccountKeyAdmin@grain-agent@proj.iam.gserviceaccount.com <- serviceAccount:grain-gcp-key-minter@proj.iam.gserviceaccount.com")

	result, err := EnsureInfrastructure(context.Background(), admin, Options{ProjectID: "proj"})
	if err != nil {
		t.Fatalf("EnsureInfrastructure: %v", err)
	}
	if !result.AllManual() {
		t.Error("AllManual() = false on a run where every step was denied")
	}
}

func TestEnsureInfrastructureNonPermissionErrorAborts(t *testing.T) {
	admin := &erroringAdmin{err: errors.New("network is down")}
	_, err := EnsureInfrastructure(context.Background(), admin, Options{ProjectID: "proj"})
	if err == nil {
		t.Fatal("expected a non-403 error to abort EnsureInfrastructure")
	}
}

// erroringAdmin fails its very first call with a non-403 error --
// EnsureInfrastructure must propagate that, not swallow it the way a 403
// is swallowed into a manual step.
type erroringAdmin struct{ err error }

func (e *erroringAdmin) EnableAPI(ctx context.Context, name string) error { return e.err }
func (e *erroringAdmin) EnsureServiceAccount(ctx context.Context, accountID, displayName, description string) error {
	return e.err
}
func (e *erroringAdmin) GrantServiceAccountRole(ctx context.Context, email, role, member string) error {
	return e.err
}
func (e *erroringAdmin) GrantProjectRole(ctx context.Context, role, member string) error {
	return e.err
}
func (e *erroringAdmin) CreateServiceAccountKey(ctx context.Context, email string) (string, error) {
	return "", e.err
}

func equalStrings(a, b []string) bool {
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

func contains(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

func TestAgentAccountIDs(t *testing.T) {
	if ids := AgentAccountIDs("grain-agent", 0); len(ids) != 1 || ids[0] != "grain-agent" {
		t.Errorf("AgentAccountIDs(grain-agent, 0) = %v, want [grain-agent]", ids)
	}
	if ids := AgentAccountIDs("grain-agent", 1); len(ids) != 1 || ids[0] != "grain-agent" {
		t.Errorf("AgentAccountIDs(grain-agent, 1) = %v, want [grain-agent]", ids)
	}
	want3 := []string{"grain-agent-0", "grain-agent-1", "grain-agent-2"}
	if ids := AgentAccountIDs("grain-agent", 3); !equalStrings(ids, want3) {
		t.Errorf("AgentAccountIDs(grain-agent, 3) = %v, want %v", ids, want3)
	}
}

func TestEnsureInfrastructureMultipleAgentAccounts(t *testing.T) {
	admin := newFakeAdmin()
	res, err := EnsureInfrastructure(context.Background(), admin, Options{
		ProjectID:         "test-proj",
		AgentAccountCount: 3,
	})
	if err != nil {
		t.Fatalf("EnsureInfrastructure failed: %v", err)
	}
	if len(res.AgentEmails) != 3 {
		t.Fatalf("expected 3 agent emails, got %v", res.AgentEmails)
	}
	wantEmails := []string{
		"grain-agent-0@test-proj.iam.gserviceaccount.com",
		"grain-agent-1@test-proj.iam.gserviceaccount.com",
		"grain-agent-2@test-proj.iam.gserviceaccount.com",
	}
	if !equalStrings(res.AgentEmails, wantEmails) {
		t.Errorf("res.AgentEmails = %v, want %v", res.AgentEmails, wantEmails)
	}
	// Check accounts created
	for _, id := range []string{"grain-agent-0", "grain-agent-1", "grain-agent-2", "grain-gcp-key-minter"} {
		if !contains(admin.createdAccounts, id) {
			t.Errorf("expected account %s created, got %v", id, admin.createdAccounts)
		}
	}
	// Check grants on all 3 accounts
	for _, email := range wantEmails {
		wantGrant := "roles/iam.serviceAccountKeyAdmin@" + email + " <- serviceAccount:grain-gcp-key-minter@test-proj.iam.gserviceaccount.com"
		if !contains(admin.serviceGrants, wantGrant) {
			t.Errorf("expected grant %s, got %v", wantGrant, admin.serviceGrants)
		}
	}
}

func TestEnsureInfrastructure_CustomAccountIDs(t *testing.T) {
	admin := newFakeAdmin()
	res, err := EnsureInfrastructure(context.Background(), admin, Options{
		ProjectID:       "proj",
		AgentAccountID:  "custom-agent",
		MinterAccountID: "custom-minter",
	})
	if err != nil {
		t.Fatalf("EnsureInfrastructure: %v", err)
	}
	if res.AgentEmail != "custom-agent@proj.iam.gserviceaccount.com" {
		t.Errorf("AgentEmail = %q", res.AgentEmail)
	}
	if res.MinterEmail != "custom-minter@proj.iam.gserviceaccount.com" {
		t.Errorf("MinterEmail = %q", res.MinterEmail)
	}
}
