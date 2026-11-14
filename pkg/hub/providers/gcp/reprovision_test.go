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
	"testing"
)

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
	if err := res.provisionPostgresACLs(ctx, "sql-pool-01", "grain_retry_vm", iamUser, "first-password"); err != nil {
		t.Fatalf("provision #1 as non-superuser admin: %v", err)
	}
	// Between the two provisions the instance's daemon has run and created its
	// schema (objects the role owns), and the admin has created a table too.
	roleDB, err := open(iamUser, "grain_retry_vm")
	if err != nil {
		t.Fatalf("open as role: %v", err)
	}
	defer roleDB.Close()
	if _, err := roleDB.ExecContext(ctx, `CREATE TABLE task (id SERIAL PRIMARY KEY, title TEXT)`); err != nil {
		t.Fatalf("role creating its own table: %v", err)
	}
	adminInst, err := open("tenant_admin", "grain_retry_vm")
	if err != nil {
		t.Fatalf("open as admin: %v", err)
	}
	defer adminInst.Close()
	if _, err := adminInst.ExecContext(ctx, `CREATE TABLE restored (id INT)`); err != nil {
		t.Fatalf("admin creating a table: %v", err)
	}
	if err := res.provisionPostgresACLs(ctx, "sql-pool-01", "grain_retry_vm", iamUser, "second-password"); err != nil {
		t.Fatalf("provision #2 as non-superuser admin, after the daemon created tables: %v", err)
	}
	if _, err := roleDB.ExecContext(ctx, `INSERT INTO restored VALUES (1)`); err != nil {
		t.Fatalf("role writing the admin-created table after re-provision: %v", err)
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
	err = superRes.provisionPostgresACLs(ctx, "sql-pool-01", "grain_foreign_vm", foreign, "pw")
	if !errors.Is(err, ErrOutOfScope) {
		t.Fatalf("provision over a BYPASSRLS role: err = %v, want ErrOutOfScope", err)
	}
}
