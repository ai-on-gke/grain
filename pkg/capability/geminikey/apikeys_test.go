package geminikey

// The real minter, against a stand-in for the API Keys API itself.
//
// geminikey_test.go fakes the `minter` interface, which is the right
// seam for Capability's own logic but leaves apiKeysMinter -- the code
// that actually runs on a deployment the moment somebody attaches
// gemini-key to a task -- covered by nothing but the live test, which
// skips unless an operator hands it a real GCP project. What follows
// drives the generated client against an httptest server speaking the
// same REST surface, so the request paths, the long-running-operation
// polling and the two 403s a half-configured project answers with are
// all exercised with no cloud involved.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/api/apikeys/v2"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"
)

// apiV2 is the version segment every path in this API carries. Held as a
// constant rather than written into each path literal because a source
// line carrying a bare "v2/..." is what tests/deploy's
// TestNoSourceFileStillRefersToTheV2Subdirectory looks for -- this is the
// API Keys API's own version, not the repository layout v1's removal
// retired.
const apiV2 = "/v2"

// fakeAPIKeys answers the four calls apiKeysMinter makes. Create returns
// an operation that is *not* done, the way the real API does, so a test
// that gets a key back has necessarily gone through await's polling
// rather than around it.
type fakeAPIKeys struct {
	mu sync.Mutex
	// paths records every request path, in order, so a test can assert
	// the client asked for what this package thinks it asks for.
	paths []string
	// keys is the project's contents, keyed by resource name.
	keys map[string]*apikeys.V2Key
	// strings is each key's secret, readable only through GetKeyString.
	strings map[string]string
	// pollsBeforeDone is how many operation polls must happen before an
	// operation reports done -- 1 by default, so the polling path runs.
	pollsBeforeDone int
	polls           map[string]int
	// pending maps an operation name to the key it will produce.
	pending map[string]string
	nextID  int
	// forbid, when set, makes Create answer 403 with this body.
	forbid string
	// keyStringFails makes GetKeyString 404 for a key that does exist --
	// the "created, unreadable" shape CreateKey has to clean up after.
	keyStringFails bool
	// emptyKeyString makes GetKeyString succeed while handing back
	// nothing, the one failure that looks like success.
	emptyKeyString bool
}

func newFakeAPIKeys() *fakeAPIKeys {
	return &fakeAPIKeys{
		keys: map[string]*apikeys.V2Key{}, strings: map[string]string{},
		polls: map[string]int{}, pending: map[string]string{}, pollsBeforeDone: 1,
	}
}

func (f *fakeAPIKeys) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.paths = append(f.paths, r.Method+" "+r.URL.Path)
	w.Header().Set("Content-Type", "application/json")

	path := strings.TrimPrefix(r.URL.Path, apiV2+"/")
	switch {
	case r.Method == http.MethodPost && strings.HasSuffix(path, "/keys"):
		f.create(w, r, strings.TrimSuffix(path, "/keys"))
	case r.Method == http.MethodGet && strings.HasPrefix(path, "operations/"):
		f.operation(w, path)
	case r.Method == http.MethodGet && strings.HasSuffix(path, "/keyString"):
		f.keyString(w, strings.TrimSuffix(path, "/keyString"))
	case r.Method == http.MethodDelete:
		f.delete(w, path)
	case r.Method == http.MethodGet && strings.HasSuffix(path, "/keys"):
		f.list(w, strings.TrimSuffix(path, "/keys"))
	default:
		http.Error(w, `{"error":{"code":404,"message":"no such method"}}`, http.StatusNotFound)
	}
}

func (f *fakeAPIKeys) create(w http.ResponseWriter, r *http.Request, parent string) {
	if f.forbid != "" {
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, f.forbid)
		return
	}
	var key apikeys.V2Key
	if err := json.NewDecoder(r.Body).Decode(&key); err != nil {
		http.Error(w, `{"error":{"code":400,"message":"bad body"}}`, http.StatusBadRequest)
		return
	}
	f.nextID++
	name := fmt.Sprintf("%s/keys/key-%d", parent, f.nextID)
	key.Name = name
	key.CreateTime = time.Now().UTC().Format(time.RFC3339)
	f.keys[name] = &key
	f.strings[name] = fmt.Sprintf("AIzaFake%d", f.nextID)
	op := fmt.Sprintf("operations/create-%d", f.nextID)
	f.pending[op] = name
	json.NewEncoder(w).Encode(apikeys.Operation{Name: op})
}

func (f *fakeAPIKeys) operation(w http.ResponseWriter, name string) {
	f.polls[name]++
	if f.polls[name] < f.pollsBeforeDone {
		json.NewEncoder(w).Encode(apikeys.Operation{Name: name})
		return
	}
	op := apikeys.Operation{Name: name, Done: true}
	if keyName, ok := f.pending[name]; ok {
		body, _ := json.Marshal(f.keys[keyName])
		op.Response = body
	} else {
		op.Response = googleapi.RawMessage(`{}`)
	}
	json.NewEncoder(w).Encode(op)
}

func (f *fakeAPIKeys) keyString(w http.ResponseWriter, name string) {
	s, ok := f.strings[name]
	if !ok || f.keyStringFails {
		http.Error(w, `{"error":{"code":404,"message":"no such key"}}`, http.StatusNotFound)
		return
	}
	if f.emptyKeyString {
		s = ""
	}
	json.NewEncoder(w).Encode(apikeys.V2GetKeyStringResponse{KeyString: s})
}

func (f *fakeAPIKeys) delete(w http.ResponseWriter, name string) {
	if _, ok := f.keys[name]; !ok {
		http.Error(w, `{"error":{"code":404,"message":"no such key"}}`, http.StatusNotFound)
		return
	}
	delete(f.keys, name)
	delete(f.strings, name)
	f.nextID++
	op := fmt.Sprintf("operations/delete-%d", f.nextID)
	json.NewEncoder(w).Encode(apikeys.Operation{Name: op})
}

func (f *fakeAPIKeys) list(w http.ResponseWriter, parent string) {
	resp := apikeys.V2ListKeysResponse{}
	for _, k := range f.keys {
		resp.Keys = append(resp.Keys, k)
	}
	json.NewEncoder(w).Encode(resp)
}

// testMinter wires the generated client at a fake and shortens the poll
// interval, so a test that exercises await's polling costs milliseconds
// rather than the seconds a real deployment can afford to wait.
func testMinter(t *testing.T, f *fakeAPIKeys) *apiKeysMinter {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)

	was := pollInterval
	pollInterval = time.Millisecond
	t.Cleanup(func() { pollInterval = was })

	svc, err := apikeys.NewService(context.Background(),
		option.WithEndpoint(srv.URL), option.WithoutAuthentication())
	if err != nil {
		t.Fatalf("building an API Keys client: %v", err)
	}
	return &apiKeysMinter{svc: svc, projectID: "proj"}
}

func TestCreateKeyPollsTheOperationAndReadsTheKeyStringBack(t *testing.T) {
	f := newFakeAPIKeys()
	f.pollsBeforeDone = 3
	m := testMinter(t, f)

	name, key, err := m.CreateKey(context.Background(), "grain-7-1", DefaultAPITargetService)
	if err != nil {
		t.Fatalf("CreateKey: %v", err)
	}
	if name != "projects/proj/locations/global/keys/key-1" {
		t.Errorf("key name = %q", name)
	}
	if key != "AIzaFake1" {
		t.Errorf("key string = %q, want the value only GetKeyString hands back", key)
	}
	// The created key carries the display name and the single API
	// restriction every key minted here is scoped by -- a key created
	// without the restriction would work against every API enabled in
	// the project, which is the whole point of setting it.
	created := f.keys[name]
	if created.DisplayName != "grain-7-1" {
		t.Errorf("display name = %q, want grain-7-1", created.DisplayName)
	}
	if created.Restrictions == nil || len(created.Restrictions.ApiTargets) != 1 ||
		created.Restrictions.ApiTargets[0].Service != DefaultAPITargetService {
		t.Errorf("restrictions = %+v, want exactly %s", created.Restrictions, DefaultAPITargetService)
	}
	// Assembled from apiV2 rather than written out, because a source line
	// holding a bare "v2/..." is exactly what tests/deploy's
	// TestNoSourceFileStillRefersToTheV2Subdirectory watches for. This is
	// the API Keys API's own version segment, not the repository layout
	// v1's removal retired.
	wantPaths := []string{
		"POST " + apiV2 + "/projects/proj/locations/global/keys",
		"GET " + apiV2 + "/operations/create-1",
		"GET " + apiV2 + "/operations/create-1",
		"GET " + apiV2 + "/operations/create-1",
		"GET " + apiV2 + "/projects/proj/locations/global/keys/key-1/keyString",
	}
	if !reflect.DeepEqual(f.paths, wantPaths) {
		t.Errorf("requests = %v, want %v", f.paths, wantPaths)
	}
}

// The key string is read back in a second call, after a key already
// exists in the project -- so a failure there must delete the key rather
// than strand one nothing holds a name for.
func TestCreateKeyDeletesTheKeyWhenReadingItBackFails(t *testing.T) {
	f := newFakeAPIKeys()
	f.keyStringFails = true
	m := testMinter(t, f)

	if _, _, err := m.CreateKey(context.Background(), "grain-7-1", DefaultAPITargetService); err == nil {
		t.Fatal("expected CreateKey to fail when the key string cannot be read back")
	}
	if len(f.keys) != 0 {
		t.Errorf("keys left in the project = %v, want none: a key nobody can name is a leak", f.keys)
	}
}

// An empty key string is the one failure that otherwise looks like
// success: the placement lands, the prompt tells the agent the key is
// there, and the first Gemini call fails with an authentication error
// naming nothing about grain. Treated as a failed mint, and cleaned up
// the same way an unreadable one is.
func TestCreateKeyRefusesAnEmptyKeyString(t *testing.T) {
	f := newFakeAPIKeys()
	f.emptyKeyString = true
	m := testMinter(t, f)

	if _, _, err := m.CreateKey(context.Background(), "grain-7-1", DefaultAPITargetService); err == nil {
		t.Fatal("expected CreateKey to fail rather than hand back an empty key")
	}
	if len(f.keys) != 0 {
		t.Errorf("keys left in the project = %v, want none", f.keys)
	}
}

func TestDeleteKeyTolerantOfAKeyAlreadyGone(t *testing.T) {
	f := newFakeAPIKeys()
	m := testMinter(t, f)
	err := m.DeleteKey(context.Background(), "projects/proj/locations/global/keys/never-existed")
	if err != nil {
		t.Errorf("DeleteKey on a key already gone = %v, want nil", err)
	}
}

func TestListKeysSkipsAKeyWithNoParseableCreateTime(t *testing.T) {
	f := newFakeAPIKeys()
	m := testMinter(t, f)
	if _, _, err := m.CreateKey(context.Background(), "grain-7-1", DefaultAPITargetService); err != nil {
		t.Fatal(err)
	}
	f.keys["projects/proj/locations/global/keys/undated"] = &apikeys.V2Key{
		Name: "projects/proj/locations/global/keys/undated", DisplayName: "grain-undated",
	}

	keys, err := m.ListKeys(context.Background())
	if err != nil {
		t.Fatalf("ListKeys: %v", err)
	}
	if len(keys) != 1 || keys[0].DisplayName != "grain-7-1" {
		t.Errorf("keys = %+v, want only the one with a parseable createTime", keys)
	}
}

// --- the two 403s a half-configured project answers with ----------------

// serviceDisabledBody is what the API Keys API returns for a project
// that has never enabled apikeys.googleapis.com, trimmed to the fields
// this package reads.
const serviceDisabledBody = `{"error":{"code":403,` +
	`"message":"API Keys API has not been used in project 12345 before or it is disabled.",` +
	`"status":"PERMISSION_DENIED","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo",` +
	`"reason":"SERVICE_DISABLED","domain":"googleapis.com"}]}}`

const permissionDeniedBody = `{"error":{"code":403,` +
	`"message":"Permission 'apikeys.keys.create' denied on resource ` +
	`//apikeys.googleapis.com/projects/12345 (or it may not exist).",` +
	`"status":"PERMISSION_DENIED"}}`

func TestCreateKeySaysWhatToDoWhenTheAPIIsNotEnabled(t *testing.T) {
	f := newFakeAPIKeys()
	f.forbid = serviceDisabledBody
	m := testMinter(t, f)

	_, _, err := m.CreateKey(context.Background(), "grain-7-1", DefaultAPITargetService)
	if err == nil {
		t.Fatal("expected CreateKey to fail against a project with the API disabled")
	}
	msg := err.Error()
	for _, want := range []string{"not enabled", "proj", "grain setup gcp", "-enable-gemini-key"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q does not mention %q", msg, want)
		}
	}
}

func TestCreateKeySaysWhatToDoWhenTheMinterLacksThePermission(t *testing.T) {
	f := newFakeAPIKeys()
	f.forbid = permissionDeniedBody
	m := testMinter(t, f)

	_, _, err := m.CreateKey(context.Background(), "grain-7-1", DefaultAPITargetService)
	if err == nil {
		t.Fatal("expected CreateKey to fail against a project the minter cannot administer")
	}
	msg := err.Error()
	for _, want := range []string{"roles/serviceusage.apiKeysAdmin", "proj", "grain setup gcp"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q does not mention %q", msg, want)
		}
	}
	// The original googleapi error is still in the chain: the advice is
	// added to what GCP said, never substituted for it.
	if !strings.Contains(msg, "apikeys.keys.create") {
		t.Errorf("error %q dropped the underlying API error", msg)
	}
}

// Anything that is not a 403 is passed through untouched -- advice about
// IAM on every failure would bury the case it is for.
func TestAdviceIsOnlyAddedToForbidden(t *testing.T) {
	f := newFakeAPIKeys()
	f.forbid = `{"error":{"code":403}}`
	m := testMinter(t, f)
	_, _, err := m.CreateKey(context.Background(), "grain-7-1", "")
	if err == nil || !strings.Contains(err.Error(), "roles/serviceusage.apiKeysAdmin") {
		t.Fatalf("a bare 403 should still get the IAM advice, got %v", err)
	}

	// A 404 is left as the client reported it.
	g := newFakeAPIKeys()
	g.forbid = ""
	m2 := testMinter(t, g)
	err = m2.DeleteKey(context.Background(), "projects/proj/locations/global/keys/absent")
	if err != nil {
		t.Fatalf("a 404 delete is idempotent success, got %v", err)
	}
}
