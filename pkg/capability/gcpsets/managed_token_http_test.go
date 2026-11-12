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

package gcpsets_test

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/capability/gcpsets"
	"github.com/ai-on-gke/grain/pkg/capability/gcpsets/gcpfake"
	"github.com/ai-on-gke/grain/pkg/capability/protocol"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

// signingAdminKey is a service-account key that can actually sign: the
// real credentials code fetches its token from token_uri, which the
// test endpoints send to the stub.
func signingAdminKey(t *testing.T, email string) string {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(k)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(map[string]string{
		"type":           "service_account",
		"project_id":     proj,
		"client_email":   email,
		"private_key_id": "stub-key-1",
		"private_key":    string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})),
		"token_uri":      "https://oauth2.googleapis.com/token",
	})
	return string(b)
}

func httpManagedTokenConfig(t *testing.T, key string, sets []any, extra map[string]any) *gcpsets.Config {
	t.Helper()
	creds, _ := json.Marshal(map[string]json.RawMessage{"minter": json.RawMessage(key)})
	cfg := map[string]any{
		protocol.DeploymentIDConfig: dep,
		protocol.InstanceIDConfig:   inst,
		"admins": []any{map[string]any{
			"name":              "minter",
			"credential_source": "configured",
			"projects":          []any{proj},
			"role_ceiling":      []any{"roles/artifactregistry.writer", "roles/storage.objectViewer"},
		}},
		"admin_credentials": string(creds),
		"permission_sets":   sets,
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

// The managed token reconcile end to end over HTTP: GoogleConnector's
// real IAM, Resource Manager, Artifact Registry and IAM Credentials
// clients, and the admin key's own token fetch, all pointed by Endpoints
// at gcpfake's HTTP stub -- the account created, its role bound on the
// Artifact Registry resource_scope, tokenCreator on the account alone,
// per-attempt tokens, and the binding taken away again when the set is
// removed with delete_orphans.
func TestManagedTokenReconcileOverHTTPStub(t *testing.T) {
	ctx := context.Background()
	cloud := gcpfake.New()
	cloud.RequireTokenCreator = true
	stub := gcpfake.NewServer(cloud)
	ts := httptest.NewServer(stub)
	defer ts.Close()
	endpoints, err := gcpsets.ParseEndpoints(gcpfake.Endpoints(ts.URL).String())
	if err != nil {
		t.Fatal(err)
	}
	conn := gcpsets.GoogleConnector{Endpoints: endpoints}

	const scope = "repositories/us-central1/images"
	arResource := "projects/" + proj + "/locations/us-central1/repositories/images"
	adminEmail := "minter@" + proj + ".iam.gserviceaccount.com"
	key := signingAdminKey(t, adminEmail)
	cloud.Granted = map[string][]string{arResource: gcpsets.ArtifactRegistryScopePermissions}

	set := map[string]any{
		"name": "ar-writer", "admin": "minter", "project": proj, "credential": "token",
		"roles": []any{"roles/artifactregistry.writer"}, "resource_scope": scope,
	}
	m := gcpsets.NewManager(httpManagedTokenConfig(t, key, []any{set}, nil), conn)
	if err := m.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	wantEmail := "grain-" + dep + "-ar-writer@" + proj + ".iam.gserviceaccount.com"
	if _, ok := cloud.Accounts[proj][wantEmail]; !ok {
		t.Fatalf("no managed account %s: %v", wantEmail, cloud.Accounts[proj])
	}
	if got := cloud.Members(arResource, "roles/artifactregistry.writer"); !slices.Equal(got, []string{"serviceAccount:" + wantEmail}) {
		t.Fatalf("AR repository bindings = %v", got)
	}
	if got := cloud.Members("projects/"+proj, "roles/artifactregistry.writer"); len(got) != 0 {
		t.Fatalf("project bindings = %v, want none for a scoped set", got)
	}
	saRes := "projects/" + proj + "/serviceAccounts/" + wantEmail
	if got := cloud.Members(saRes, "roles/iam.serviceAccountTokenCreator"); !slices.Equal(got, []string{"serviceAccount:" + adminEmail}) {
		t.Fatalf("tokenCreator on the account = %v", got)
	}

	// Test's permission checks go over the same stub.
	if report := m.Report(ctx, gcpsets.Census{}, nil); !strings.Contains(report, "has every permission it needs") || !strings.Contains(report, "short-lived tokens, no keys") {
		t.Errorf("report:\n%s", report)
	}

	deadline := time.Now().Add(2 * time.Hour)
	if _, lease, err := m.Mint(ctx, "att-1", "ar-writer", deadline); err != nil || lease.Account != wantEmail || lease.KeyID != "" {
		t.Fatalf("Mint = %+v, %v", lease, err)
	}
	tok1, _, err := m.AccessToken(ctx, "ar-writer", wantEmail, deadline)
	if err != nil || !strings.HasSuffix(tok1, wantEmail) {
		t.Fatalf("AccessToken = %q, %v", tok1, err)
	}
	if _, lease, err := m.Mint(ctx, "att-2", "ar-writer", deadline); err != nil || lease.Account != wantEmail {
		t.Fatalf("second Mint = %+v, %v", lease, err)
	}
	tok2, _, err := m.AccessToken(ctx, "ar-writer", wantEmail, deadline)
	if err != nil || tok2 == tok1 {
		t.Fatalf("second attempt's AccessToken = %q, %v; want a token of its own", tok2, err)
	}
	if cloud.LiveKeys() != 0 {
		t.Fatal("a token-mode set created a key")
	}

	// Removing the set and naming it in delete_orphans unbinds the
	// repository and deletes the account, over the same stub.
	other := map[string]any{
		"name": "other-reader", "admin": "minter", "project": proj, "credential": "token",
		"roles": []any{"roles/storage.objectViewer"},
	}
	mDel := gcpsets.NewManager(httpManagedTokenConfig(t, key, []any{other}, map[string]any{"delete_orphans": []any{"ar-writer"}}), conn)
	census, err := mDel.Census(ctx)
	if err != nil {
		t.Fatalf("Census: %v", err)
	}
	deleted, err := mDel.DeleteOrphans(ctx, census.Orphans)
	if err != nil || !slices.Equal(deleted, []string{wantEmail}) {
		t.Fatalf("DeleteOrphans = %v, %v", deleted, err)
	}
	if got := cloud.Members(arResource, "roles/artifactregistry.writer"); len(got) != 0 {
		t.Errorf("AR binding survived the set's deletion: %v", got)
	}
	if _, ok := cloud.Accounts[proj][wantEmail]; ok {
		t.Errorf("account %s survived the set's deletion", wantEmail)
	}

	// Every call went over HTTP, carrying a token the stub issued to the
	// admin's own key.
	reqs := strings.Join(stub.Requests(), "\n")
	for _, want := range []string{
		"POST /iam/v1/projects/" + proj + "/serviceAccounts",
		"POST /artifactregistry/v1/" + arResource + ":setIamPolicy",
		"POST /iam/v1/" + saRes + ":setIamPolicy",
		"POST /iamcredentials/v1/projects/-/serviceAccounts/" + wantEmail + ":generateAccessToken",
		"DELETE /iam/v1/" + saRes,
	} {
		if !strings.Contains(reqs, want) {
			t.Errorf("stub never saw %s; it saw:\n%s", want, reqs)
		}
	}
	issued := stub.Issued()
	if len(issued) == 0 {
		t.Fatal("the admin's token was never fetched from the stub")
	}
	for tok, who := range issued {
		if who != adminEmail {
			t.Errorf("stub issued %s to %q, want %s", tok, who, adminEmail)
		}
	}
}

func TestParseEndpointsRefusesAnythingButLoopbackStubsForEveryService(t *testing.T) {
	full := gcpfake.Endpoints("http://127.0.0.1:9999")
	if _, err := gcpsets.ParseEndpoints(full.String()); err != nil {
		t.Fatalf("ParseEndpoints(stub) = %v", err)
	}
	with := func(svc, url string) string {
		e := gcpsets.Endpoints{}
		for k, v := range full {
			e[k] = v
		}
		e[svc] = url
		return e.String()
	}
	for name, tc := range map[string]struct{ spec, want string }{
		"off-machine host":  {with("iam", "https://iam.example.com/"), "not on a loopback address"},
		"private address":   {with("sts", "http://10.0.0.1:80/"), "not on a loopback address"},
		"not http":          {with("storage", "file:///tmp/x"), "not an http(s) URL"},
		"credentials in it": {with("iam", "http://u:p@127.0.0.1:1/"), "more than a scheme"},
		"unknown service":   {full.String() + ",compute=http://127.0.0.1:1/", `unknown service "compute"`},
		"missing service":   {"iam=http://127.0.0.1:1/", "names no base URL for cloudresourcemanager"},
		"not a pair":        {"iam", "not service=base-url"},
		"named twice":       {full.String() + ",iam=http://127.0.0.1:2/", "iam is named twice"},
	} {
		if _, err := gcpsets.ParseEndpoints(tc.spec); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: ParseEndpoints = %v, want %q", name, err, tc.want)
		}
	}
	if e, err := gcpsets.ParseEndpoints(with("iam", "http://localhost:1/iam")); err != nil || e["iam"] != "http://localhost:1/iam/" {
		t.Errorf("localhost without a trailing slash = %v, %v", e, err)
	}
}

// An ambient admin whose credentials come from the metadata server --
// no credentials file -- is refused under test endpoints: the metadata
// client is not the one the endpoints rewrite, so its token would be a
// real one handed to the stub.
func TestTestEndpointsRefuseMetadataServerCredentials(t *testing.T) {
	ctx := context.Background()
	metadata := gcpsets.ADC(func(context.Context, ...string) (*google.Credentials, error) {
		return &google.Credentials{TokenSource: oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "real-vm-token"})}, nil
	})
	conn := gcpsets.GoogleConnector{ADC: metadata, Endpoints: gcpfake.Endpoints("http://127.0.0.1:1")}
	_, err := conn.Connect(ctx, gcpsets.Admin{Name: "vm", CredentialSource: gcpsets.SourceAmbient})
	if err == nil || !strings.Contains(err.Error(), "metadata server") {
		t.Fatalf("Connect = %v, want the metadata server refused", err)
	}
}
