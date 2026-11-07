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

package postgres

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrepareDumpTargetStripsPasswordAndSearchPath(t *testing.T) {
	cases := []struct {
		name       string
		cfg        Config
		wantConn   string
		wantPass   string
		wantSchema string
	}{
		{
			name:     "local setup.sh URL DSN",
			cfg:      Config{URL: "postgres://grain:s3cr3t@127.0.0.1:5432/grain?sslmode=disable"},
			wantConn: "postgres://grain@127.0.0.1:5432/grain?sslmode=disable",
			wantPass: "s3cr3t",
		},
		{
			name:       "URL DSN with search_path query parameter",
			cfg:        Config{URL: "postgresql://grain:pw@127.0.0.1:5432/grain?search_path=tenant_a&sslmode=disable"},
			wantConn:   "postgresql://grain@127.0.0.1:5432/grain?sslmode=disable",
			wantPass:   "pw",
			wantSchema: "tenant_a",
		},
		{
			name:       "struct fields with isolated schema",
			cfg:        Config{Host: "127.0.0.1", Port: 5433, Database: "graintest", User: "grain", Password: "pw2", SearchPath: `"test_123", public`},
			wantConn:   "postgres://grain@127.0.0.1:5433/graintest?sslmode=disable",
			wantPass:   "pw2",
			wantSchema: "test_123",
		},
		{
			name:     "unix socket Cloud SQL style",
			cfg:      Config{Host: "/cloudsql/proj:reg:inst", Database: "grain", User: "grain@proj.iam"},
			wantConn: "postgres://grain%40proj.iam@/grain?host=%2Fcloudsql%2Fproj%3Areg%3Ainst&sslmode=disable",
		},
		{
			name:     "unix socket embedded style with custom port",
			cfg:      Config{Host: "/tmp/grain-pg", Port: 41235, Database: "postgres", User: "postgres"},
			wantConn: "postgres://postgres@/postgres?host=%2Ftmp%2Fgrain-pg&port=41235&sslmode=disable",
		},
		{
			name:       "key-value DSN with quoted password and search_path",
			cfg:        Config{URL: `host=127.0.0.1 port=5432 user=grain password='my\'secret' dbname=grain search_path=isolated sslmode=disable`},
			wantConn:   "host=127.0.0.1 port=5432 user=grain dbname=grain sslmode=disable",
			wantPass:   "my'secret",
			wantSchema: "isolated",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			conn, pass, schema, err := prepareDumpTarget(tc.cfg)
			if err != nil {
				t.Fatalf("prepareDumpTarget: %v", err)
			}
			if conn != tc.wantConn {
				t.Errorf("connString = %q, want %q", conn, tc.wantConn)
			}
			if pass != tc.wantPass {
				t.Errorf("password = %q, want %q", pass, tc.wantPass)
			}
			if schema != tc.wantSchema {
				t.Errorf("schema = %q, want %q", schema, tc.wantSchema)
			}
			if tc.wantPass != "" && strings.Contains(conn, tc.wantPass) {
				t.Errorf("connString %q still contains password", conn)
			}
		})
	}
}

func TestDumpPassesPasswordViaEnvAndRedactsErrors(t *testing.T) {
	stubDir := t.TempDir()
	argsFile := filepath.Join(stubDir, "args.txt")
	envFile := filepath.Join(stubDir, "env.txt")
	stubPath := filepath.Join(stubDir, "pg_dump")
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$@\" > \"" + argsFile + "\"\n" +
		"env > \"" + envFile + "\"\n" +
		"if [ \"${FAIL_DUMP:-}\" = \"1\" ]; then\n" +
		"  echo \"connection failed for ${PGPASSWORD}\" >&2\n" +
		"  exit 1\n" +
		"fi\n" +
		"for arg in \"$@\"; do\n" +
		"  case \"$arg\" in --file=*) printf 'PGDMP-stub' > \"${arg#--file=}\" ;; esac\n" +
		"done\n"
	if err := os.WriteFile(stubPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", stubDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GRAIN_SECRET_API_TOKEN", "must-not-leak-to-pg-dump")

	dest := filepath.Join(t.TempDir(), "grain.dump")
	dsn := "postgres://grain:SuperSecretPass99@127.0.0.1:5432/grain?sslmode=disable&search_path=custom_schema"
	if err := Dump(context.Background(), nil, dsn, dest); err != nil {
		t.Fatalf("Dump: %v", err)
	}

	gotBytes, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("reading output dump: %v", err)
	}
	if string(gotBytes) != "PGDMP-stub" {
		t.Fatalf("dump content = %q, want PGDMP-stub", gotBytes)
	}

	gotArgsBytes, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	gotArgs := string(gotArgsBytes)
	if strings.Contains(gotArgs, "SuperSecretPass99") {
		t.Fatalf("pg_dump argv leaked database password:\n%s", gotArgs)
	}
	for _, want := range []string{
		"--format=custom",
		"--no-password",
		"--file=" + dest,
		"--schema=custom_schema",
		"--dbname=postgres://grain@127.0.0.1:5432/grain?sslmode=disable",
	} {
		if !strings.Contains(gotArgs, want) {
			t.Errorf("pg_dump argv missing %q:\n%s", want, gotArgs)
		}
	}

	gotEnvBytes, err := os.ReadFile(envFile)
	if err != nil {
		t.Fatal(err)
	}
	gotEnv := string(gotEnvBytes)
	if !strings.Contains(gotEnv, "PGPASSWORD=SuperSecretPass99\n") {
		t.Errorf("pg_dump env missing PGPASSWORD")
	}
	if strings.Contains(gotEnv, "GRAIN_SECRET_API_TOKEN") {
		t.Errorf("pg_dump inherited daemon secret env var:\n%s", gotEnv)
	}

	// Verify stderr redacts the password if pg_dump echoes it on failure.
	stubFail := filepath.Join(stubDir, "pg_dump")
	failScript := strings.Replace(script, `if [ "${FAIL_DUMP:-}" = "1" ]; then`, `if true; then`, 1)
	if err := os.WriteFile(stubFail, []byte(failScript), 0o755); err != nil {
		t.Fatal(err)
	}
	err = Dump(context.Background(), nil, dsn, filepath.Join(t.TempDir(), "fail.dump"))
	if err == nil {
		t.Fatal("expected error when pg_dump exits 1")
	}
	if strings.Contains(err.Error(), "SuperSecretPass99") {
		t.Fatalf("Dump error leaked password: %v", err)
	}
	if !strings.Contains(err.Error(), "connection failed for ******") {
		t.Fatalf("Dump error = %v, want redacted stderr", err)
	}
}

func TestDumpRequiresDSNOrPostgresDB(t *testing.T) {
	err := Dump(context.Background(), nil, "", filepath.Join(t.TempDir(), "out.dump"))
	if err == nil || !strings.Contains(err.Error(), "postgres.Open") {
		t.Fatalf("Dump(nil, \"\") = %v, want clear error", err)
	}
}
