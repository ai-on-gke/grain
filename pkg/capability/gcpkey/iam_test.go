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

package gcpkey

// The real minter, against a stand-in for the IAM API itself.
//
// gcpkey_test.go fakes the Minter interface, which is the right seam for
// Provider's own logic but leaves iamMinter -- the code that actually
// runs on a deployment the moment somebody attaches gcp-key to a task --
// covered by nothing at all. What follows drives the generated client
// against an httptest server speaking the same REST surface, so the
// request paths, the base64 the private key arrives wrapped in, and each
// way a half-set-up project refuses a mint are exercised with no cloud
// involved.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	iam "google.golang.org/api/iam/v1"
	"google.golang.org/api/option"

	"github.com/ai-on-gke/grain/pkg/model"
)

// testAccount is the resource name every call below is made against, in
// the "projects/{project}/serviceAccounts/{email}" shape Minter's own
// contract fixes.
var testAccount = accountName("example-project", "agent@example-project.iam.gserviceaccount.com")

// fakeIAM answers the three calls iamMinter makes. Each failure mode is
// a field rather than a separate server, so a test names the one thing
// it is arranging.
type fakeIAM struct {
	mu sync.Mutex
	// requests records every call, in order, as "METHOD path".
	requests []string
	// queries records each request's raw query string, so a test can
	// assert ListKeys asked for user-managed keys only.
	queries []string
	// keys is the account's contents, keyed by resource name.
	keys map[string]*iam.ServiceAccountKey
	// privateKeyData overrides what a created key's PrivateKeyData
	// carries; empty means "a real credentials document, base64'd".
	privateKeyData *string
	// createStatus/createBody make Create fail with a real GCP error
	// body rather than succeed.
	createStatus int
	createBody   string
	// listStatus makes List fail, for the "cannot tell the two
	// FAILED_PRECONDITIONs apart" path.
	listStatus int
	nextID     int
}

func newFakeIAM() *fakeIAM {
	return &fakeIAM{keys: map[string]*iam.ServiceAccountKey{}}
}

func (f *fakeIAM) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, r.Method+" "+r.URL.Path)
	f.queries = append(f.queries, r.URL.RawQuery)
	w.Header().Set("Content-Type", "application/json")

	switch {
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/keys"):
		f.create(w)
	case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/keys"):
		f.list(w)
	case r.Method == http.MethodDelete:
		f.delete(w, r.URL.Path)
	default:
		writeAPIError(w, http.StatusNotFound, `{"error":{"code":404,"message":"no such method"}}`)
	}
}

func (f *fakeIAM) create(w http.ResponseWriter) {
	if f.createStatus != 0 {
		writeAPIError(w, f.createStatus, f.createBody)
		return
	}
	f.nextID++
	id := fmt.Sprintf("key-%d", f.nextID)
	key := &iam.ServiceAccountKey{
		Name:           testAccount + "/keys/" + id,
		ValidAfterTime: time.Now().UTC().Format(time.RFC3339),
		PrivateKeyData: base64.StdEncoding.EncodeToString(
			[]byte(`{"type":"service_account","private_key_id":"` + id + `"}`)),
	}
	if f.privateKeyData != nil {
		key.PrivateKeyData = *f.privateKeyData
	}
	f.keys[key.Name] = key
	json.NewEncoder(w).Encode(key)
}

func (f *fakeIAM) list(w http.ResponseWriter) {
	if f.listStatus != 0 {
		writeAPIError(w, f.listStatus, `{"error":{"code":403,"message":"nope"}}`)
		return
	}
	resp := iam.ListServiceAccountKeysResponse{}
	for _, k := range f.keys {
		resp.Keys = append(resp.Keys, k)
	}
	json.NewEncoder(w).Encode(resp)
}

func (f *fakeIAM) delete(w http.ResponseWriter, path string) {
	// The path carries the API's own version segment ahead of the
	// resource name; the fake keys its map by the resource name alone.
	name := strings.TrimPrefix(path, "/v1/")
	if _, ok := f.keys[name]; !ok {
		writeAPIError(w, http.StatusNotFound, `{"error":{"code":404,"message":"no such key"}}`)
		return
	}
	delete(f.keys, name)
	json.NewEncoder(w).Encode(iam.Empty{})
}

// held reports how many keys the fake account currently holds.
func (f *fakeIAM) held() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.keys)
}

// seed puts n keys into the fake account without going through Create --
// for the quota case, which is about what the account already holds.
func (f *fakeIAM) seed(n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := 0; i < n; i++ {
		f.nextID++
		name := fmt.Sprintf("%s/keys/seeded-%d", testAccount, f.nextID)
		f.keys[name] = &iam.ServiceAccountKey{Name: name}
	}
}

func writeAPIError(w http.ResponseWriter, status int, body string) {
	w.WriteHeader(status)
	fmt.Fprint(w, body)
}

// testIAMMinter wires the generated client at a fake, with no
// credentials involved: NewIAMMinter's own job is turning credential
// material into a client, which is exactly the part that needs a real
// GCP to mean anything.
func testIAMMinter(t *testing.T, f *fakeIAM) *iamMinter {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)

	svc, err := iam.NewService(context.Background(),
		option.WithEndpoint(srv.URL), option.WithoutAuthentication())
	if err != nil {
		t.Fatalf("building an IAM client: %v", err)
	}
	return &iamMinter{svc: svc}
}

// --- the happy path ---------------------------------------------------

func TestCreateKeyDecodesThePrivateKeyDocument(t *testing.T) {
	f := newFakeIAM()
	m := testIAMMinter(t, f)

	id, keyJSON, err := m.CreateKey(context.Background(), testAccount)
	if err != nil {
		t.Fatalf("CreateKey: %v", err)
	}
	// The bare id, never the full resource name: Lease.Resource and
	// KeyInfo.ID both carry this, and DeleteKey reassembles the rest.
	if id != "key-1" {
		t.Errorf("id = %q, want the bare key id", id)
	}
	// PrivateKeyData arrives base64-encoded even for
	// TYPE_GOOGLE_CREDENTIALS_FILE; the decoded bytes are what belongs at
	// SandboxKeyPath. A placement carrying the raw field would look like
	// a key and authenticate as nothing.
	if !strings.HasPrefix(keyJSON, `{"type":"service_account"`) {
		t.Errorf("key material = %q, want the decoded credentials document", keyJSON)
	}
	want := "POST /v1/" + testAccount + "/keys"
	if f.requests[0] != want {
		t.Errorf("request = %q, want %q", f.requests[0], want)
	}
}

func TestDeleteKeyAddressesTheKeyUnderItsAccount(t *testing.T) {
	f := newFakeIAM()
	m := testIAMMinter(t, f)

	id, _, err := m.CreateKey(context.Background(), testAccount)
	if err != nil {
		t.Fatalf("CreateKey: %v", err)
	}
	if err := m.DeleteKey(context.Background(), testAccount, id); err != nil {
		t.Fatalf("DeleteKey: %v", err)
	}
	if f.held() != 0 {
		t.Errorf("the account still holds %d key(s) after a delete", f.held())
	}
	if err := m.DeleteKey(context.Background(), testAccount, "gone"); err == nil {
		t.Error("expected deleting a key that is not there to report as an error")
	}
}

func TestListKeysReportsIDsAndCreationTimes(t *testing.T) {
	f := newFakeIAM()
	m := testIAMMinter(t, f)
	if _, _, err := m.CreateKey(context.Background(), testAccount); err != nil {
		t.Fatalf("CreateKey: %v", err)
	}

	keys, err := m.ListKeys(context.Background(), testAccount)
	if err != nil {
		t.Fatalf("ListKeys: %v", err)
	}
	if len(keys) != 1 {
		t.Fatalf("keys = %+v, want exactly one", keys)
	}
	if keys[0].ID != "key-1" {
		t.Errorf("id = %q, want the bare key id", keys[0].ID)
	}
	if keys[0].CreatedAt.IsZero() {
		t.Error("CreatedAt is zero, so Reap would leave this key alone forever")
	}
	// Google-managed keys cannot be listed, downloaded or deleted through
	// this API at all, so a listing that asked for them would be counting
	// keys nothing here can act on -- against the same cap
	// explainCreateFailure reasons about.
	if q := f.queries[len(f.queries)-1]; !strings.Contains(q, "keyTypes=USER_MANAGED") {
		t.Errorf("list query = %q, want it restricted to USER_MANAGED", q)
	}
}

// A key created but carrying no material is the one failure here that
// looks like success: an empty file at SandboxKeyPath, described to the
// agent as a working key. It fails the mint, and takes the useless key
// back out of the account rather than leaving it against the cap.
func TestCreateKeyRejectsAKeyWithNoMaterialAndDeletesIt(t *testing.T) {
	f := newFakeIAM()
	empty := ""
	f.privateKeyData = &empty
	m := testIAMMinter(t, f)

	if _, _, err := m.CreateKey(context.Background(), testAccount); err == nil {
		t.Fatal("expected an empty private key to fail the mint")
	}
	if f.held() != 0 {
		t.Errorf("the account still holds %d useless key(s)", f.held())
	}
}

// --- what a half-set-up project actually says -------------------------

// The four failures below all reach an operator as a task's own failure
// detail, on a deployment whose Settings pane says Ready. Each assertion
// is on the sentence that names the remedy, because that sentence is the
// entire point of the classification.
func TestCreateFailureNamesTheDisabledAPI(t *testing.T) {
	f := newFakeIAM()
	f.createStatus = http.StatusForbidden
	f.createBody = `{"error":{"code":403,"message":"IAM API has not been used in project example-project before or it is disabled.","status":"PERMISSION_DENIED","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"SERVICE_DISABLED"}]}}`
	m := testIAMMinter(t, f)

	_, _, err := m.CreateKey(context.Background(), testAccount)
	if err == nil {
		t.Fatal("expected a 403 to fail the mint")
	}
	if !strings.Contains(err.Error(), "IAM API is not enabled") {
		t.Errorf("error %q does not say the API was never enabled", err)
	}
	if !strings.Contains(err.Error(), "grain setup gcp -project example-project") {
		t.Errorf("error %q does not name the command that enables it", err)
	}
}

func TestCreateFailureNamesTheMissingRole(t *testing.T) {
	f := newFakeIAM()
	f.createStatus = http.StatusForbidden
	f.createBody = `{"error":{"code":403,"message":"Permission 'iam.serviceAccountKeys.create' denied on resource (or it may not exist).","status":"PERMISSION_DENIED"}}`
	m := testIAMMinter(t, f)

	_, _, err := m.CreateKey(context.Background(), testAccount)
	if err == nil {
		t.Fatal("expected a 403 to fail the mint")
	}
	if !strings.Contains(err.Error(), "roles/iam.serviceAccountKeyAdmin") {
		t.Errorf("error %q does not name the role the minter is missing", err)
	}
}

func TestCreateFailureNamesAServiceAccountThatIsNotThere(t *testing.T) {
	f := newFakeIAM()
	f.createStatus = http.StatusNotFound
	f.createBody = `{"error":{"code":404,"message":"Service account projects/-/serviceAccounts/agent@example-project.iam.gserviceaccount.com does not exist.","status":"NOT_FOUND"}}`
	m := testIAMMinter(t, f)

	_, _, err := m.CreateKey(context.Background(), testAccount)
	if err == nil {
		t.Fatal("expected a 404 to fail the mint")
	}
	if !strings.Contains(err.Error(), "has no service account") ||
		!strings.Contains(err.Error(), "Settings -> Capabilities") {
		t.Errorf("error %q does not point at the setting that names the account", err)
	}
}

// The regression this whole classification exists for: an organization
// that forbids service-account keys refuses with FAILED_PRECONDITION,
// and every FAILED_PRECONDITION used to be reported as the key cap --
// telling an operator with zero keys that grain was minting them faster
// than it revokes them.
func TestCreateFailureNamesTheOrgPolicyRatherThanTheKeyCap(t *testing.T) {
	f := newFakeIAM()
	f.createStatus = http.StatusBadRequest
	f.createBody = `{"error":{"code":400,"message":"Key creation is not allowed on this service account.","status":"FAILED_PRECONDITION","details":[{"@type":"type.googleapis.com/google.rpc.PreconditionFailure","violations":[{"type":"constraints/iam.disableServiceAccountKeyCreation"}]}]}}`
	m := testIAMMinter(t, f)

	_, _, err := m.CreateKey(context.Background(), testAccount)
	if err == nil {
		t.Fatal("expected a FAILED_PRECONDITION to fail the mint")
	}
	if !strings.Contains(err.Error(), "constraints/iam.disableServiceAccountKeyCreation") {
		t.Errorf("error %q does not name the constraint that forbade this", err)
	}
	if strings.Contains(err.Error(), "faster than either is releasing them") {
		t.Errorf("error %q still blames the key cap for an org-policy refusal", err)
	}
}

func TestCreateFailureExplainsTheKeyCapWhenTheAccountIsActuallyAtIt(t *testing.T) {
	f := newFakeIAM()
	f.seed(maxUserManagedKeys)
	f.createStatus = http.StatusBadRequest
	f.createBody = `{"error":{"code":400,"message":"Precondition check failed.","status":"FAILED_PRECONDITION"}}`
	m := testIAMMinter(t, f)

	_, _, err := m.CreateKey(context.Background(), testAccount)
	if err == nil {
		t.Fatal("expected a FAILED_PRECONDITION to fail the mint")
	}
	if !strings.Contains(err.Error(), "at most 10 user-managed keys") {
		t.Errorf("error %q does not explain the cap the account is at", err)
	}
	if !strings.Contains(err.Error(), "currently has 10") {
		t.Errorf("error %q does not say how many keys the account holds", err)
	}
	var exhausted *model.ResourceExhaustedError
	if !errors.As(err, &exhausted) {
		t.Errorf("key-cap error = %v, want ResourceExhaustedError so the job waits", err)
	}
}

func TestCreateFailureBlamesKeyCapWhenAccountHoldsKeysNearCap(t *testing.T) {
	f := newFakeIAM()
	f.seed(9)
	f.createStatus = http.StatusBadRequest
	f.createBody = `{"error":{"code":400,"message":"Precondition check failed.","status":"FAILED_PRECONDITION"}}`
	m := testIAMMinter(t, f)

	_, _, err := m.CreateKey(context.Background(), testAccount)
	if err == nil {
		t.Fatal("expected a FAILED_PRECONDITION to fail the mint")
	}
	if !strings.Contains(err.Error(), "at most 10 user-managed keys") {
		t.Errorf("error %q does not explain the cap the account is at", err)
	}
	if !strings.Contains(err.Error(), "currently has 9") {
		t.Errorf("error %q does not say how many keys the account holds", err)
	}
	var exhausted *model.ResourceExhaustedError
	if !errors.As(err, &exhausted) {
		t.Errorf("key-cap error = %v, want ResourceExhaustedError so the job waits", err)
	}
}

// The same opaque refusal on an empty account holding 0 keys is not the
// cap -- so the message says so and names the likelier cause instead of
// asserting a leak.
func TestCreateFailureDoesNotBlameTheKeyCapWhenTheAccountIsEmpty(t *testing.T) {
	f := newFakeIAM()
	f.createStatus = http.StatusBadRequest
	f.createBody = `{"error":{"code":400,"message":"Precondition check failed.","status":"FAILED_PRECONDITION"}}`
	m := testIAMMinter(t, f)

	_, _, err := m.CreateKey(context.Background(), testAccount)
	if err == nil {
		t.Fatal("expected a FAILED_PRECONDITION to fail the mint")
	}
	if !strings.Contains(err.Error(), "not the 10-key cap") {
		t.Errorf("error %q does not rule the cap out", err)
	}
	if !strings.Contains(err.Error(), "holds 0") {
		t.Errorf("error %q does not say the account holds 0 keys", err)
	}
	if !strings.Contains(err.Error(), "constraints/iam.disableServiceAccountKeyCreation") {
		t.Errorf("error %q does not name the likelier cause", err)
	}
}

func TestCreateFailureNamesBothWhenItCannotCountTheKeys(t *testing.T) {
	f := newFakeIAM()
	f.createStatus = http.StatusBadRequest
	f.createBody = `{"error":{"code":400,"message":"Precondition check failed.","status":"FAILED_PRECONDITION"}}`
	f.listStatus = http.StatusForbidden
	m := testIAMMinter(t, f)

	_, _, err := m.CreateKey(context.Background(), testAccount)
	if err == nil {
		t.Fatal("expected a FAILED_PRECONDITION to fail the mint")
	}
	if !strings.Contains(err.Error(), "10-key cap") ||
		!strings.Contains(err.Error(), "constraints/iam.disableServiceAccountKeyCreation") {
		t.Errorf("error %q does not name both possibilities", err)
	}
	// Never worse than the original: GCP's own words survive whichever
	// way the diagnosis goes.
	if !strings.Contains(err.Error(), "Precondition check failed") {
		t.Errorf("error %q dropped GCP's own message", err)
	}
}

// Anything else already says what it is, and wrapping every failure in
// advice about IAM would bury the cases above rather than help.
func TestCreateFailurePassesAnythingElseThroughUnexplained(t *testing.T) {
	f := newFakeIAM()
	f.createStatus = http.StatusInternalServerError
	f.createBody = `{"error":{"code":500,"message":"backend error","status":"INTERNAL"}}`
	m := testIAMMinter(t, f)

	_, _, err := m.CreateKey(context.Background(), testAccount)
	if err == nil {
		t.Fatal("expected a 500 to fail the mint")
	}
	if !strings.Contains(err.Error(), "backend error") {
		t.Errorf("error %q dropped GCP's own message", err)
	}
	for _, advice := range []string{"grain setup gcp", "10-key cap", "Settings ->"} {
		if strings.Contains(err.Error(), advice) {
			t.Errorf("error %q volunteers %q for a failure that is none of those", err, advice)
		}
	}
}
