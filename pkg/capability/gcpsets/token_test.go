package gcpsets_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/oauth2/google"
	"google.golang.org/api/option"

	"github.com/bwsalmon/grain/pkg/capability/gcpsets"
)

// googleFake is Google's token endpoint and enough of IAM to list a
// project's service accounts.
type googleFake struct {
	*httptest.Server
	tokens atomic.Int32
	// token answers one token request; nil hands out a token that is
	// already within oauth2's expiry margin, so every call fetches anew.
	token func(n int32, w http.ResponseWriter, r *http.Request)
}

func newGoogleFake(t *testing.T) *googleFake {
	f := &googleFake{}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			n := f.tokens.Add(1)
			if f.token != nil {
				f.token(n, w, r)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"access_token":"tok-%d","token_type":"Bearer","expires_in":1}`, n)
			return
		}
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer tok-") {
			http.Error(w, `{"error":{"code":401,"message":"no token"}}`, http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"accounts":[{"email":"a@p.iam.gserviceaccount.com"}]}`)
	}))
	t.Cleanup(f.Close)
	return f
}

// connector is a GoogleConnector whose ambient admin is a user's gcloud
// credentials (authorized_user, as `gcloud auth application-default
// login` writes them) refreshed at the fake.
func (f *googleFake) connector(rt http.RoundTripper) gcpsets.GoogleConnector {
	adc := `{"type":"authorized_user","client_id":"c","client_secret":"s","refresh_token":"r","token_uri":"` + f.URL + `/token"}`
	return gcpsets.GoogleConnector{
		ADC: func(ctx context.Context, scopes ...string) (*google.Credentials, error) {
			return google.CredentialsFromJSON(ctx, []byte(adc), scopes...)
		},
		TokenTransport: rt,
		Options:        []option.ClientOption{option.WithEndpoint(f.URL + "/")},
	}
}

var ambientAdmin = gcpsets.Admin{Name: "ambient-host", CredentialSource: gcpsets.SourceAmbient, Projects: []string{"p"}}

// The Manager keeps an admin's Cloud past the call that built it -- in
// production, configure's, cancelled as configure returns. Its later
// token refreshes must not use that call's context: they did, and every
// mint an hour after configure failed with "Post .../token: context
// canceled" (task #1315).
func TestACloudOutlivesTheContextItWasBuiltIn(t *testing.T) {
	f := newGoogleFake(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	c, err := f.connector(nil).Connect(ctx, ambientAdmin)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.ListServiceAccounts(ctx, "p"); err != nil {
		t.Fatalf("listing inside the building call: %v", err)
	}
	cancel()
	// Past the token's expiry, so the next call must fetch a new one.
	time.Sleep(1100 * time.Millisecond)
	before := f.tokens.Load()
	if _, err := c.ListServiceAccounts(context.Background(), "p"); err != nil {
		t.Fatalf("listing after the building call's context ended: %v", err)
	}
	if n := f.tokens.Load(); n == before {
		t.Errorf("no token was fetched past the first one's expiry")
	}
}

// A token endpoint slower than the deadline of the call that built the
// Cloud still gives a token to a later call with time to wait for it.
func TestASlowTokenFetchStillLists(t *testing.T) {
	f := newGoogleFake(t)
	f.token = func(n int32, w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"access_token":"tok-%d","token_type":"Bearer","expires_in":3600}`, n)
	}
	short, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	c, err := f.connector(nil).Connect(short, ambientAdmin)
	if err != nil {
		t.Fatal(err)
	}
	<-short.Done()
	if _, err := c.ListServiceAccounts(context.Background(), "p"); err != nil {
		t.Fatalf("a slow token fetch: %v", err)
	}
}

// flakyTransport fails its first n round trips the way a cancelled fetch
// does, then passes the rest through.
type flakyTransport struct {
	fail  int32
	calls atomic.Int32
}

func (t *flakyTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if t.calls.Add(1) <= t.fail {
		return nil, context.Canceled
	}
	return http.DefaultTransport.RoundTrip(r)
}

func TestACancelledTokenFetchIsRetriedOnce(t *testing.T) {
	f := newGoogleFake(t)
	rt := &flakyTransport{fail: 1}
	c, err := f.connector(rt).Connect(context.Background(), ambientAdmin)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.ListServiceAccounts(context.Background(), "p"); err != nil {
		t.Fatalf("one cancelled fetch should be retried: %v", err)
	}
	if n := rt.calls.Load(); n != 2 {
		t.Errorf("token round trips = %d, want 2 (the cancelled one and its retry)", n)
	}

	// Twice cancelled is a transient refusal that says so.
	rt = &flakyTransport{fail: 2}
	c, err = f.connector(rt).Connect(context.Background(), ambientAdmin)
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.ListServiceAccounts(context.Background(), "p")
	ce, ok := gcpsets.AsCredentialError(err)
	if !ok || ce.Kind != gcpsets.CredentialTimeout || ce.NeedsOperator() {
		t.Fatalf("twice cancelled: %v (%#v)", err, ce)
	}
	if n := rt.calls.Load(); n != 2 {
		t.Errorf("token round trips = %d, want 2: one retry, no more", n)
	}
	if !strings.Contains(err.Error(), "transient") {
		t.Errorf("a timeout should say it is transient: %v", err)
	}
}

func TestExpiredAmbientCredentialsSayWhatToRun(t *testing.T) {
	f := newGoogleFake(t)
	f.token = func(n int32, w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error":"invalid_grant","error_description":"reauth related error (invalid_rapt)"}`)
	}
	c, err := f.connector(nil).Connect(context.Background(), ambientAdmin)
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.ListServiceAccounts(context.Background(), "p")
	ce, ok := gcpsets.AsCredentialError(err)
	if !ok || ce.Kind != gcpsets.CredentialExpired || !ce.NeedsOperator() {
		t.Fatalf("expired: %v", err)
	}
	if n := f.tokens.Load(); n != 1 {
		t.Errorf("token fetches = %d: a rejected credential is not retried", n)
	}
	for _, want := range []string{"ambient-host", "expired or revoked", "gcloud auth application-default login", "as the user grain was installed as"} {
		if !strings.Contains(ce.Error(), want) {
			t.Errorf("expired message lacks %q: %s", want, ce.Error())
		}
	}
	if strings.Contains(ce.Error(), "r\"") || strings.Contains(ce.Error(), "client_secret") {
		t.Errorf("message quotes the credential: %s", ce.Error())
	}

	// Test's own token fetch finds the same.
	chk, ok := c.(interface{ CheckToken(context.Context) error })
	if !ok {
		t.Fatal("the Google Cloud has no CheckToken")
	}
	if err := chk.CheckToken(context.Background()); err == nil || !strings.Contains(err.Error(), "expired or revoked") {
		t.Errorf("CheckToken: %v", err)
	}
}

func TestMissingAmbientCredentialsSayWhatToRun(t *testing.T) {
	g := gcpsets.GoogleConnector{ADC: func(context.Context, ...string) (*google.Credentials, error) {
		return nil, errors.New("google: could not find default credentials")
	}}
	_, err := g.Connect(context.Background(), ambientAdmin)
	ce, ok := gcpsets.AsCredentialError(err)
	if !ok || ce.Kind != gcpsets.CredentialMissing || !ce.NeedsOperator() {
		t.Fatalf("missing: %v", err)
	}
	for _, want := range []string{"no ambient credentials", "gcloud auth application-default login"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("missing message lacks %q: %s", want, err)
		}
	}
}

// Settings' Test fetches the admin's token the way a mint does, so stale
// ambient credentials show there before a task's dispatch is refused.
func TestTestReportsExpiredAmbientCredentials(t *testing.T) {
	f := newGoogleFake(t)
	f.token = func(n int32, w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error":"invalid_grant","error_description":"Token has been expired or revoked."}`)
	}
	cfg := config(t, nil)
	m := gcpsets.NewManager(cfg, f.connector(nil))
	for _, chk := range m.CheckAdmins(context.Background()) {
		if chk.Admin != "me" {
			continue
		}
		if !strings.Contains(chk.Error, "expired or revoked") || !strings.Contains(chk.Error, "gcloud auth application-default login") {
			t.Errorf("Test's finding for the ambient admin = %q", chk.Error)
		}
		return
	}
	t.Fatal("no finding for the ambient admin")
}
