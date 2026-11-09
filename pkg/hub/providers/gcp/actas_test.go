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

package gcp

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"

	"github.com/ai-on-gke/grain/pkg/hub"
)

const testSelfPrincipal = "principal://iam.googleapis.com/projects/123456789/locations/global/workloadIdentityPools/test-proj.svc.id.goog/subject/ns/grain-hub/sa/grain-registry"

type selfActAsFixture struct {
	cp      *fakeGCPControlPlane
	gce     *fakeGCEState
	res     *GCPInstanceResources
	compute *GCECompute
	spec    hub.InstanceResourceSpec
	sleeps  *[]time.Duration
}

// newSelfActAsFixture wires the resources provider and the GCE provider to fakes
// that share one notion of IAM: the fake insert refuses with
// SERVICE_ACCOUNT_ACCESS_DENIED unless the registry holds roles/iam.serviceAccountUser
// on the instance's own service account.
func newSelfActAsFixture(t *testing.T, grant *bool) *selfActAsFixture {
	t.Helper()
	ctx := context.Background()
	cpSrv, cp := newFakeGCPControlPlaneServer(t)
	gceSrv, gce := newFakeGCEServer(t)
	gce.canActAs = func(saEmail string) bool { return cp.canActAs(testSelfPrincipal, saEmail) }

	cloudSQLAdmin := false
	bind := false
	cfg := hub.Config{
		Compute: hub.ComputeConfig{
			Provider:       "gce",
			Project:        "test-proj",
			Region:         "us-central1",
			Zones:          []string{"us-central1-a"},
			VMPrefix:       "grain-vm-",
			SAPrefix:       "gi-",
			RegistryID:     "grain.example.com",
			BindProjectIAM: &bind,
			GrantSelfActAs: grant,
			SelfPrincipal:  testSelfPrincipal,
		},
		Secrets: hub.SecretsConfig{Project: "test-proj", SecretPrefix: "grain-inst-"},
		InstanceSQL: hub.InstanceSQLConfig{
			CloudSQLAdmin: &cloudSQLAdmin,
			SQLInstances: []hub.SQLInstanceConfig{
				{Name: "sql-pool-01", ConnectionName: "test-proj:us-central1:sql-pool-01", PrivateIP: "10.8.0.5"},
			},
		},
	}
	var mu sync.Mutex
	var sleeps []time.Duration
	res, err := NewGCPInstanceResources(ctx, cfg,
		WithResourcesHTTPClient(cpSrv.Client()),
		WithIAMBaseURL(cpSrv.URL),
		WithResourceManagerBaseURL(cpSrv.URL),
		WithSQLAdminBaseURL(cpSrv.URL),
		WithSecretManagerBaseURL(cpSrv.URL),
		WithStorageBaseURL(cpSrv.URL),
		WithResourcesComputeBaseURL(cpSrv.URL+"/compute/v1"),
		WithIAMRetrySleep(func(_ context.Context, d time.Duration) error {
			mu.Lock()
			sleeps = append(sleeps, d)
			mu.Unlock()
			return nil
		}),
	)
	if err != nil {
		t.Fatalf("NewGCPInstanceResources: %v", err)
	}
	comp, err := NewGCECompute(ctx, cfg.Compute,
		WithComputeBaseURL(gceSrv.URL+"/compute/v1"),
		WithComputeHTTPClient(gceSrv.Client()),
	)
	if err != nil {
		t.Fatalf("NewGCECompute: %v", err)
	}
	return &selfActAsFixture{
		cp: cp, gce: gce, res: res, compute: comp, sleeps: &sleeps,
		spec: hub.InstanceResourceSpec{EntryID: "entry-59613b", Name: "grain-59613b", OwnerEmail: "alice@example.com"},
	}
}

func (f *selfActAsFixture) createVM(ctx context.Context, sa string) (*hub.InstanceHandle, error) {
	return f.compute.CreateInstance(ctx, hub.InstanceSpec{
		Name:           f.spec.Name,
		Zone:           "us-central1-a",
		Shape:          hub.Shape{Name: "n2-standard-2", MachineType: "n2-standard-2", BootDiskGB: 50},
		ServiceAccount: sa,
		Generation:     1,
	})
}

func (f *selfActAsFixture) saPolicy(email string) iamPolicy {
	f.cp.mu.Lock()
	defer f.cp.mu.Unlock()
	return f.cp.saPolicies[email]
}

func countMember(p iamPolicy, role, member string) int {
	n := 0
	for _, b := range p.Bindings {
		if b.Role != role {
			continue
		}
		for _, m := range b.Members {
			if m == member {
				n++
			}
		}
	}
	return n
}

const testInstanceSA = "gi-grain-59613b@test-proj.iam.gserviceaccount.com"

func TestProvisionGrantsSelfActAsSoTheVMCanBeCreated(t *testing.T) {
	ctx := context.Background()
	f := newSelfActAsFixture(t, nil) // unset: on by default

	bundle, err := f.res.Provision(ctx, f.spec)
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if bundle.ServiceAccountEmail != testInstanceSA {
		t.Fatalf("ServiceAccountEmail = %q, want %q", bundle.ServiceAccountEmail, testInstanceSA)
	}
	pol := f.saPolicy(testInstanceSA)
	if got := countMember(pol, roleServiceAccountUser, testSelfPrincipal); got != 1 {
		t.Fatalf("%s bindings for the registry on %s = %d, want 1 (policy %+v)", roleServiceAccountUser, testInstanceSA, got, pol)
	}
	for _, b := range pol.Bindings {
		if b.Condition != nil {
			t.Fatalf("self actAs binding carries a condition: %+v", b)
		}
	}

	handle, err := f.createVM(ctx, bundle.ServiceAccountEmail)
	if err != nil {
		t.Fatalf("CreateInstance after the grant: %v", err)
	}
	if handle.InstanceID != "grain-vm-grain-59613b" {
		t.Fatalf("handle = %+v", handle)
	}

	// The binding lives on the account: deleting the account deletes it too.
	if err := f.res.deleteServiceAccount(ctx, testInstanceSA); err != nil {
		t.Fatalf("deleteServiceAccount: %v", err)
	}
	f.cp.mu.Lock()
	_, left := f.cp.saPolicies[testInstanceSA]
	f.cp.mu.Unlock()
	if left {
		t.Fatal("the account's IAM policy outlived the account")
	}
}

func TestProvisionLeavesAnExistingSelfActAsBindingAlone(t *testing.T) {
	ctx := context.Background()
	f := newSelfActAsFixture(t, nil)
	f.cp.mu.Lock()
	f.cp.serviceAccounts[testInstanceSA] = true
	f.cp.saPolicies[testInstanceSA] = iamPolicy{
		Etag: "etag-preset",
		Bindings: []iamBinding{
			{Role: roleServiceAccountUser, Members: []string{testSelfPrincipal}},
			{Role: "roles/iam.serviceAccountTokenCreator", Members: []string{"user:ops@example.com"}},
		},
	}
	f.cp.mu.Unlock()

	if _, err := f.res.Provision(ctx, f.spec); err != nil {
		t.Fatalf("Provision: %v", err)
	}
	// Provision runs again on every reconcile retry; it must stay a no-op.
	if _, err := f.res.Provision(ctx, f.spec); err != nil {
		t.Fatalf("second Provision: %v", err)
	}
	f.cp.mu.Lock()
	sets, gets := f.cp.saSetIAMCalls, f.cp.saGetIAMCalls
	f.cp.mu.Unlock()
	if sets != 0 {
		t.Fatalf("setIamPolicy calls = %d, want 0 when the binding already exists", sets)
	}
	if gets != 2 {
		t.Fatalf("getIamPolicy calls = %d, want 2", gets)
	}
	pol := f.saPolicy(testInstanceSA)
	if pol.Etag != "etag-preset" || len(pol.Bindings) != 2 || countMember(pol, roleServiceAccountUser, testSelfPrincipal) != 1 {
		t.Fatalf("policy changed: %+v", pol)
	}
}

func TestProvisionRetriesSelfActAsOnEtagConflicts(t *testing.T) {
	ctx := context.Background()
	f := newSelfActAsFixture(t, nil)
	f.cp.mu.Lock()
	f.cp.saConcurrentWrites = 2
	f.cp.mu.Unlock()

	if _, err := f.res.Provision(ctx, f.spec); err != nil {
		t.Fatalf("Provision: %v", err)
	}
	f.cp.mu.Lock()
	sets := f.cp.saSetIAMCalls
	f.cp.mu.Unlock()
	if sets != 3 {
		t.Fatalf("setIamPolicy calls = %d, want 3 (two lost races, then success)", sets)
	}
	pol := f.saPolicy(testInstanceSA)
	if got := countMember(pol, roleServiceAccountUser, testSelfPrincipal); got != 1 {
		t.Fatalf("registry bindings = %d, want 1 (policy %+v)", got, pol)
	}
	concurrent := 0
	for _, b := range pol.Bindings {
		if b.Role == "roles/iam.serviceAccountTokenCreator" {
			concurrent += len(b.Members)
		}
	}
	if concurrent != 2 {
		t.Fatalf("the concurrent writers' bindings were lost: %+v", pol)
	}
	if len(*f.sleeps) != 2 {
		t.Fatalf("backoff sleeps = %v, want 2", *f.sleeps)
	}
}

func TestProvisionWaitsForANewAccountToReachIAM(t *testing.T) {
	ctx := context.Background()
	f := newSelfActAsFixture(t, nil)
	f.cp.mu.Lock()
	f.cp.saGetIAMNotFound = 3
	f.cp.mu.Unlock()

	if _, err := f.res.Provision(ctx, f.spec); err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if got := countMember(f.saPolicy(testInstanceSA), roleServiceAccountUser, testSelfPrincipal); got != 1 {
		t.Fatalf("registry bindings = %d, want 1", got)
	}
	if len(*f.sleeps) != 3 {
		t.Fatalf("backoff sleeps = %v, want 3", *f.sleeps)
	}
}

func TestCreateInstanceRetriesWhileTheActAsGrantPropagates(t *testing.T) {
	ctx := context.Background()
	f := newSelfActAsFixture(t, nil)
	bundle, err := f.res.Provision(ctx, f.spec)
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	// The binding is set, but GCE does not see it for the first two inserts.
	var mu sync.Mutex
	stale := 2
	f.gce.canActAs = func(saEmail string) bool {
		mu.Lock()
		defer mu.Unlock()
		if stale > 0 {
			stale--
			return false
		}
		return f.cp.canActAs(testSelfPrincipal, saEmail)
	}

	for i := 0; i < 2; i++ {
		_, err := f.createVM(ctx, bundle.ServiceAccountEmail)
		if err == nil {
			t.Fatalf("insert %d succeeded before the grant propagated", i+1)
		}
		if !errors.Is(err, hub.ErrServiceAccountNotReady) {
			t.Fatalf("insert %d error = %v, want it to wrap registry.ErrServiceAccountNotReady so the reconciler retries", i+1, err)
		}
		for _, want := range []string{"SERVICE_ACCOUNT_ACCESS_DENIED", testInstanceSA, "iam.serviceAccounts.actAs"} {
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("insert %d error = %v, want it to mention %q", i+1, err, want)
			}
		}
	}
	if _, err := f.createVM(ctx, bundle.ServiceAccountEmail); err != nil {
		t.Fatalf("insert after propagation: %v", err)
	}
	f.gce.mu.Lock()
	attempts := f.gce.insertAttempts
	f.gce.mu.Unlock()
	if attempts != 3 {
		t.Fatalf("insert attempts = %d, want 3", attempts)
	}
}

func TestProvisionReportsARefusedSelfActAsGrantPlainly(t *testing.T) {
	for _, tc := range []struct {
		name       string
		refuseGet  bool
		permission string
	}{
		{"setIamPolicy", false, "iam.serviceAccounts.setIamPolicy"},
		{"getIamPolicy", true, "iam.serviceAccounts.getIamPolicy"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			f := newSelfActAsFixture(t, nil)
			f.cp.mu.Lock()
			f.cp.saRefuseSetIAM = true
			f.cp.saRefuseGetIAM = tc.refuseGet
			f.cp.mu.Unlock()

			_, err := f.res.Provision(ctx, f.spec)
			if err == nil {
				t.Fatal("Provision succeeded although IAM refused the grant")
			}
			for _, want := range []string{
				"missing " + tc.permission + " on service account " + testInstanceSA,
				"iam.serviceAccounts.setIamPolicy on the instance service accounts (gi-*)",
				"compute.grant_self_act_as: false",
				testSelfPrincipal,
			} {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("Provision error = %v\nwant it to contain %q", err, want)
				}
			}
			if len(*f.sleeps) != 0 {
				t.Fatalf("a refusal was retried: sleeps %v", *f.sleeps)
			}
		})
	}
}

func TestProvisionWithGrantSelfActAsOffNeverTouchesTheAccountPolicy(t *testing.T) {
	ctx := context.Background()
	off := false
	f := newSelfActAsFixture(t, &off)
	f.res.principalResolver = func(context.Context) (string, error) {
		t.Fatal("principal detection ran with compute.grant_self_act_as: false")
		return "", nil
	}
	bundle, err := f.res.Provision(ctx, f.spec)
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	f.cp.mu.Lock()
	gets, sets := f.cp.saGetIAMCalls, f.cp.saSetIAMCalls
	f.cp.mu.Unlock()
	if gets != 0 || sets != 0 {
		t.Fatalf("serviceAccounts getIamPolicy/setIamPolicy calls = %d/%d, want 0/0", gets, sets)
	}
	// Without a grant made out of band GCE refuses -- this is the deployment's to fix.
	if _, err := f.createVM(ctx, bundle.ServiceAccountEmail); err == nil || !strings.Contains(err.Error(), "SERVICE_ACCOUNT_ACCESS_DENIED") {
		t.Fatalf("CreateInstance error = %v, want SERVICE_ACCOUNT_ACCESS_DENIED", err)
	}
}

func TestProvisionSaysHowToSetSelfPrincipalWhenDetectionFails(t *testing.T) {
	ctx := context.Background()
	f := newSelfActAsFixture(t, nil)
	f.res.selfPrincipal = ""
	f.res.principalResolver = func(context.Context) (string, error) {
		return "", errors.New("no metadata server")
	}
	_, err := f.res.Provision(ctx, f.spec)
	if err == nil || !strings.Contains(err.Error(), "compute.self_principal") || !strings.Contains(err.Error(), "no metadata server") {
		t.Fatalf("Provision error = %v, want it to point at compute.self_principal", err)
	}
}

func TestNormalizeSelfPrincipal(t *testing.T) {
	for _, tc := range []struct {
		in, want string
		wantErr  bool
	}{
		{"", "", false},
		{"registry@p.iam.gserviceaccount.com", "serviceAccount:registry@p.iam.gserviceaccount.com", false},
		{" serviceAccount:registry@p.iam.gserviceaccount.com ", "serviceAccount:registry@p.iam.gserviceaccount.com", false},
		{testSelfPrincipal, testSelfPrincipal, false},
		{"user:ops@example.com", "user:ops@example.com", false},
		{"ops@example.com", "", true},
		{"serviceAccount:", "", true},
		{"p.svc.id.goog[ns/ksa]", "", true},
	} {
		got, err := normalizeSelfPrincipal(tc.in)
		if (err != nil) != tc.wantErr || got != tc.want {
			t.Errorf("normalizeSelfPrincipal(%q) = %q, %v; want %q, err=%v", tc.in, got, err, tc.want, tc.wantErr)
		}
	}
}

func fakeMetadataServer(t *testing.T, email string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Metadata-Flavor") != "Google" {
			http.Error(w, "missing Metadata-Flavor", http.StatusForbidden)
			return
		}
		switch r.URL.Path {
		case "/computeMetadata/v1/instance/service-accounts/default/email":
			_, _ = w.Write([]byte(email))
		case "/computeMetadata/v1/project/numeric-project-id":
			_, _ = w.Write([]byte("123456789"))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestADCPrincipalDetector(t *testing.T) {
	ctx := context.Background()
	noJSON := func(context.Context) (*google.Credentials, error) { return &google.Credentials{}, nil }

	t.Run("workload identity without a Google service account", func(t *testing.T) {
		dir := t.TempDir()
		payload := base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"system:serviceaccount:grain-hub:grain-registry"}`))
		if err := os.WriteFile(filepath.Join(dir, "token"), []byte("eyJhbGciOiJSUzI1NiJ9."+payload+".sig"), 0o600); err != nil {
			t.Fatal(err)
		}
		d := &adcPrincipalDetector{
			metadataBaseURL: fakeMetadataServer(t, "test-proj.svc.id.goog").URL + "/computeMetadata/v1",
			k8sSADir:        dir,
			httpClient:      http.DefaultClient,
			findCredentials: noJSON,
		}
		got, err := d.detect(ctx)
		if err != nil || got != testSelfPrincipal {
			t.Fatalf("detect = %q, %v; want %q", got, err, testSelfPrincipal)
		}
	})

	t.Run("workload identity without a mounted token", func(t *testing.T) {
		d := &adcPrincipalDetector{
			metadataBaseURL: fakeMetadataServer(t, "test-proj.svc.id.goog").URL + "/computeMetadata/v1",
			k8sSADir:        t.TempDir(),
			httpClient:      http.DefaultClient,
			findCredentials: noJSON,
		}
		if got, err := d.detect(ctx); err == nil {
			t.Fatalf("detect = %q, want an error", got)
		}
	})

	t.Run("metadata server service account", func(t *testing.T) {
		d := &adcPrincipalDetector{
			metadataBaseURL: fakeMetadataServer(t, "registry@test-proj.iam.gserviceaccount.com").URL + "/computeMetadata/v1",
			httpClient:      http.DefaultClient,
			findCredentials: noJSON,
		}
		got, err := d.detect(ctx)
		if err != nil || got != "serviceAccount:registry@test-proj.iam.gserviceaccount.com" {
			t.Fatalf("detect = %q, %v", got, err)
		}
	})

	t.Run("tokeninfo gets the token in the body, never the URL", func(t *testing.T) {
		const secret = "ya29.registry-secret-token"
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost || strings.Contains(r.URL.RawQuery, secret) {
				http.Error(w, "token in URL", http.StatusBadRequest)
				return
			}
			if err := r.ParseForm(); err != nil || r.PostForm.Get("access_token") != secret {
				http.Error(w, "no token in body", http.StatusBadRequest)
				return
			}
			_, _ = w.Write([]byte(`{"email":"someone@example.com"}`))
		}))
		t.Cleanup(srv.Close)
		ts := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: secret})
		d := &adcPrincipalDetector{tokenInfoURL: srv.URL, httpClient: srv.Client()}
		got, err := d.fromTokenInfo(ctx, ts)
		if err != nil || got != "user:someone@example.com" {
			t.Fatalf("fromTokenInfo = %q, %v", got, err)
		}

		// A transport failure must not quote the token back in its error.
		d = &adcPrincipalDetector{tokenInfoURL: "http://127.0.0.1:1/tokeninfo", httpClient: http.DefaultClient}
		_, err = d.fromTokenInfo(ctx, ts)
		if err == nil || strings.Contains(err.Error(), secret) {
			t.Fatalf("fromTokenInfo error = %v, want an error without the access token", err)
		}
	})

	for name, js := range map[string]string{
		"service account key": `{"type":"service_account","client_email":"registry@test-proj.iam.gserviceaccount.com"}`,
		"impersonation":       `{"type":"impersonated_service_account","service_account_impersonation_url":"https://iamcredentials.googleapis.com/v1/projects/-/serviceAccounts/registry@test-proj.iam.gserviceaccount.com:generateAccessToken"}`,
	} {
		t.Run(name, func(t *testing.T) {
			d := &adcPrincipalDetector{
				findCredentials: func(context.Context) (*google.Credentials, error) {
					return &google.Credentials{JSON: []byte(js)}, nil
				},
			}
			got, err := d.detect(ctx)
			if err != nil || got != "serviceAccount:registry@test-proj.iam.gserviceaccount.com" {
				t.Fatalf("detect = %q, %v", got, err)
			}
		})
	}
}
