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
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/registry"
)

// A per-instance service account is created moments before the VM insert, and
// GCE can refuse the insert with EXTERNAL_RESOURCE_NOT_FOUND until IAM has
// propagated it. CreateInstance must wait that out instead of failing the entry.
func TestCreateInstanceRetriesWhileServiceAccountPropagates(t *testing.T) {
	srv, state := newFakeGCEServer(t)
	ctx := context.Background()
	cfg := registry.ComputeConfig{
		Provider: "gce",
		Project:  "test-proj",
		Region:   "us-central1",
		Zones:    []string{"us-central1-a"},
		VMPrefix: "grain-vm-",
		SAPrefix: "grain-sa-",
	}
	spec := registry.InstanceSpec{
		Name:           "sa-lag",
		Zone:           "us-central1-a",
		Shape:          registry.Shape{Name: "n2-standard-2", MachineType: "n2-standard-2", BootDiskGB: 50},
		ServiceAccount: "grain-sa-sa-lag@test-proj.iam.gserviceaccount.com",
		Generation:     1,
	}

	comp, err := NewGCECompute(ctx, cfg,
		WithComputeBaseURL(srv.URL+"/compute/v1"),
		WithComputeHTTPClient(srv.Client()),
		WithServiceAccountPropagationRetry(4, time.Millisecond),
	)
	if err != nil {
		t.Fatalf("NewGCECompute: %v", err)
	}
	state.mu.Lock()
	state.saNotFoundInserts = 2
	state.mu.Unlock()
	h, err := comp.CreateInstance(ctx, spec)
	if err != nil {
		t.Fatalf("CreateInstance with a propagating service account: %v", err)
	}
	if h.InstanceID != "grain-vm-sa-lag" {
		t.Fatalf("InstanceID = %q, want grain-vm-sa-lag", h.InstanceID)
	}
	state.mu.Lock()
	inserts := state.inserts
	state.mu.Unlock()
	if inserts != 3 {
		t.Fatalf("inserts = %d, want 3 (two refused, one accepted)", inserts)
	}

	// A service account that never shows up still fails, after the configured
	// number of attempts, with GCE's own error.
	spec.Name = "sa-missing"
	spec.ServiceAccount = "grain-sa-sa-missing@test-proj.iam.gserviceaccount.com"
	state.mu.Lock()
	state.saNotFoundInserts = 100
	state.inserts = 0
	state.mu.Unlock()
	_, err = comp.CreateInstance(ctx, spec)
	if err == nil || !strings.Contains(err.Error(), "EXTERNAL_RESOURCE_NOT_FOUND") {
		t.Fatalf("CreateInstance with a missing service account: err = %v, want EXTERNAL_RESOURCE_NOT_FOUND", err)
	}
	state.mu.Lock()
	inserts = state.inserts
	state.mu.Unlock()
	if inserts != 4 {
		t.Fatalf("inserts = %d, want 4 (the configured attempts)", inserts)
	}
}

func TestIsServiceAccountNotFound(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want bool
	}{
		{nil, false},
		{errors.New("GCE operation op-1 failed (EXTERNAL_RESOURCE_NOT_FOUND): The resource 'gi-a@p.iam.gserviceaccount.com' of type 'serviceAccount' was not found."), true},
		{errors.New(`HTTP 400: {"error":{"message":"The resource 'gi-a@p.iam.gserviceaccount.com' of type 'serviceAccount' was not found."}}`), true},
		{errors.New("GCE operation op-1 failed (ZONE_RESOURCE_POOL_EXHAUSTED): no capacity"), false},
		{errors.New("GCE operation op-1 failed (EXTERNAL_RESOURCE_NOT_FOUND): The resource 'projects/p/global/images/x' was not found"), false},
	} {
		if got := isServiceAccountNotFound(tc.err); got != tc.want {
			t.Errorf("isServiceAccountNotFound(%v) = %v, want %v", tc.err, got, tc.want)
		}
	}
}

// Cloud SQL never hands out a superuser: the tenant admin is an ordinary role
// with CREATEROLE and CREATEDB. Provisioning the same entry a second time (a
// retried create, a replace) finds its role already there and must be able to
// bring it back to the expected attributes as that admin.
func TestProvisionPostgresACLsTwiceAsNonSuperuserAdmin(t *testing.T) {
	_, sockDir, superDB := startLocalPostgresForGCPTest(t)
	ctx := context.Background()

	if _, err := superDB.ExecContext(ctx, `CREATE ROLE tenant_admin WITH LOGIN CREATEROLE CREATEDB`); err != nil {
		t.Fatalf("CREATE ROLE tenant_admin: %v", err)
	}
	open := func(role, dbName string) (*sql.DB, error) {
		q := make(url.Values)
		q.Set("host", sockDir)
		q.Set("sslmode", "disable")
		u := &url.URL{Scheme: "postgres", User: url.User(role), Host: "localhost", Path: "/" + dbName, RawQuery: q.Encode()}
		return sql.Open("pgx", u.String())
	}
	res := &GCPInstanceResources{
		sqlOpener: func(_ context.Context, _ string, dbName string) (*sql.DB, error) {
			return open("tenant_admin", dbName)
		},
	}

	const iamUser = "grain-sa-retry-vm@test-proj.iam"
	for i, pw := range []string{"first-password", "second-password"} {
		if err := res.provisionPostgresACLs(ctx, "sql-pool-01", "grain_retry_vm", iamUser, pw); err != nil {
			t.Fatalf("provision #%d as non-superuser admin: %v", i+1, err)
		}
	}
	var connLimit int
	var rolSuper, rolCreateDB, rolCreateRole, rolCanLogin bool
	if err := superDB.QueryRowContext(ctx,
		`SELECT rolconnlimit, rolsuper, rolcreatedb, rolcreaterole, rolcanlogin FROM pg_roles WHERE rolname = $1`, iamUser,
	).Scan(&connLimit, &rolSuper, &rolCreateDB, &rolCreateRole, &rolCanLogin); err != nil {
		t.Fatalf("querying pg_roles: %v", err)
	}
	if connLimit != DefaultRoleConnectionLimit || rolSuper || rolCreateDB || rolCreateRole || !rolCanLogin {
		t.Fatalf("role attributes after re-provision: connlimit=%d super=%v createdb=%v createrole=%v login=%v",
			connLimit, rolSuper, rolCreateDB, rolCreateRole, rolCanLogin)
	}

	// A pre-existing role carrying an attribute the provisioner never grants is
	// refused, not silently reused.
	const foreign = "grain-sa-foreign-vm@test-proj.iam"
	if _, err := superDB.ExecContext(ctx, fmt.Sprintf(`CREATE ROLE %s WITH LOGIN BYPASSRLS`, quotePGIdent(foreign))); err != nil {
		t.Fatalf("CREATE ROLE foreign: %v", err)
	}
	superRes := &GCPInstanceResources{
		sqlOpener: func(_ context.Context, _ string, dbName string) (*sql.DB, error) {
			return open("postgres", dbName)
		},
	}
	err := superRes.provisionPostgresACLs(ctx, "sql-pool-01", "grain_foreign_vm", foreign, "pw")
	if !errors.Is(err, ErrOutOfScope) {
		t.Fatalf("provision over a BYPASSRLS role: err = %v, want ErrOutOfScope", err)
	}
}
