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

package reconcile_test

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/ai-on-gke/grain/pkg/hub"
	"github.com/ai-on-gke/grain/pkg/hub/reconcile"
	"github.com/ai-on-gke/grain/pkg/hub/store"
)

// TestGrainInstanceInitBootsFromReconcilerMetadata feeds the grain-config
// metadata the registry reconciler builds for a VM (reconcile.InstanceGrainConfig,
// the map createVMForEntry hands to Compute.CreateInstance) to
// scripts/grain-instance-init.sh through a fake metadata server and a fake
// Secret Manager, and checks the daemon.env grain-daemon.service would start with.
func TestGrainInstanceInitBootsFromReconcilerMetadata(t *testing.T) {
	cfg := hub.Config{
		Host:           "registry.example.com",
		InstanceDomain: "instances.example.com",
		Compute:        hub.ComputeConfig{Project: "test-proj", Region: "us-central1"},
		Secrets:        hub.SecretsConfig{SecretPrefix: "grain-inst-"},
	}
	entry := &store.EntryRow{
		Name:       "alpha",
		OwnerEmail: "alice@example.com",
		OwnerGroup: "eng@example.com",
	}

	for _, tc := range []struct {
		name   string
		bundle hub.InstanceResourceBundle
		// secrets seeds the fake Secret Manager, by resource name.
		secrets map[string]string
		want    []string
	}{
		{
			name: "cloud sql iam auth",
			bundle: hub.InstanceResourceBundle{
				ServiceAccountEmail: "grain-sa-alpha@test-proj.iam.gserviceaccount.com",
				SQLInstance:         "grain-shared-pg",
				SQLConnectionName:   "test-proj:us-central1:grain-shared-pg",
				SQLPrivateIP:        "10.0.0.5",
				DatabaseName:        "grain_alpha",
				SecretPrefix:        "grain-inst-alpha-",
				GCSBucket:           "grain-state",
				GCSManagedFolder:    "instances/alpha/",
			},
			want: []string{
				"GRAIN_DB_DSN=postgres://grain-sa-alpha@test-proj.iam@/grain_alpha?host={cloudsql}/test-proj:us-central1:grain-shared-pg",
			},
		},
		{
			name: "built-in password user",
			bundle: hub.InstanceResourceBundle{
				SQLInstance:            "grain-shared-pg",
				SQLPrivateIP:           "10.0.0.5",
				DatabaseName:           "grain_alpha",
				DatabaseUser:           "grain-sa-alpha@test-proj.iam",
				DatabasePasswordSecret: "projects/test-proj/secrets/grain-inst-alpha-db-password",
				SecretPrefix:           "grain-inst-alpha-",
				GCSBucket:              "grain-state",
				GCSManagedFolder:       "instances/alpha/",
			},
			// The password is served only by Secret Manager under the
			// instance's prefix; grain-config never carries it.
			secrets: map[string]string{
				"projects/test-proj/secrets/grain-inst-alpha-db-password": "p@ss/word\n",
			},
			want: []string{
				"GRAIN_DB_DSN=postgres://grain-sa-alpha%40test-proj.iam:p%40ss%2Fword@10.0.0.5:5432/grain_alpha?sslmode=require",
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			grainConfig, err := json.Marshal(reconcile.InstanceGrainConfig(cfg, entry, &tc.bundle, ""))
			if err != nil {
				t.Fatal(err)
			}

			for _, pw := range []string{"p@ss/word", "p%40ss%2Fword"} {
				if strings.Contains(string(grainConfig), pw) {
					t.Fatalf("grain-config metadata carries the database password %q:\n%s", pw, grainConfig)
				}
			}
			if strings.Contains(string(grainConfig), "sslmode=disable") {
				t.Fatalf("grain-config metadata carries a cleartext DSN:\n%s", grainConfig)
			}

			var mu sync.Mutex
			secrets := map[string]string{}
			for k, v := range tc.secrets {
				secrets[k] = v
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.URL.Path == "/computeMetadata/v1/instance/attributes/grain-config":
					if r.Header.Get("Metadata-Flavor") != "Google" {
						http.Error(w, "missing Metadata-Flavor", http.StatusForbidden)
						return
					}
					_, _ = w.Write(grainConfig)
				case r.URL.Path == "/computeMetadata/v1/instance/service-accounts/default/token":
					_, _ = w.Write([]byte(`{"access_token":"test-oauth-token","token_type":"Bearer","expires_in":3600}`))
				case r.URL.Path == "/computeMetadata/v1/instance/service-accounts/default/email":
					_, _ = w.Write([]byte(tc.bundle.ServiceAccountEmail))
				case strings.HasPrefix(r.URL.Path, "/v1/projects/test-proj/secrets/grain-inst-alpha-") && strings.HasSuffix(r.URL.Path, "/versions/latest:access"):
					name := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v1/"), "/versions/latest:access")
					mu.Lock()
					val, ok := secrets[name]
					mu.Unlock()
					if !ok {
						http.NotFound(w, r)
						return
					}
					_, _ = w.Write([]byte(`{"payload":{"data":"` + base64.StdEncoding.EncodeToString([]byte(val)) + `"}}`))
				case strings.HasPrefix(r.URL.Path, "/v1/projects/test-proj/secrets/grain-inst-alpha-") && strings.HasSuffix(r.URL.Path, ":addVersion") && r.Method == http.MethodPost:
					var body struct {
						Payload struct {
							Data string `json:"data"`
						} `json:"payload"`
					}
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						http.Error(w, err.Error(), http.StatusBadRequest)
						return
					}
					decoded, err := base64.StdEncoding.DecodeString(body.Payload.Data)
					if err != nil {
						http.Error(w, err.Error(), http.StatusBadRequest)
						return
					}
					name := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v1/"), ":addVersion")
					mu.Lock()
					secrets[name] = string(decoded)
					mu.Unlock()
					_, _ = w.Write([]byte(`{"name":"` + name + `/versions/1"}`))
				default:
					t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer srv.Close()

			root := t.TempDir()
			etcDir := filepath.Join(root, "etc")
			runDir := filepath.Join(root, "run-secrets")
			cloudsqlDir := filepath.Join(root, "cloudsql")
			cmd := exec.Command("bash", filepath.Join("..", "..", "..", "scripts", "grain-instance-init.sh"))
			cmd.Env = append(os.Environ(),
				"GRAIN_METADATA_URL="+srv.URL+"/computeMetadata/v1",
				"GRAIN_SM_API_URL="+srv.URL,
				"GRAIN_DATA_DIR="+filepath.Join(root, "data"),
				"GRAIN_ETC_DIR="+etcDir,
				"GRAIN_SECRETS_RUN_DIR="+runDir,
				"GRAIN_CLOUDSQL_DIR="+cloudsqlDir,
				"GRAIN_SKIP_TMPFS_MOUNT=1",
				"GRAIN_SKIP_SYSTEMCTL=1",
			)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("grain-instance-init.sh: %v\n%s", err, out)
			}

			mu.Lock()
			token := secrets["projects/test-proj/secrets/grain-inst-alpha-local-api-token"]
			mu.Unlock()
			if len(token) != 64 {
				t.Fatalf("local-api-token stored in Secret Manager = %q, want 64 hex chars", token)
			}

			proxyEnv, err := os.ReadFile(filepath.Join(etcDir, "cloud-sql-proxy.env"))
			if err != nil {
				t.Fatalf("reading cloud-sql-proxy.env: %v", err)
			}
			if !strings.Contains(string(proxyEnv), "CLOUD_SQL_INSTANCE=test-proj:us-central1:grain-shared-pg\n") {
				t.Errorf("cloud-sql-proxy.env does not name the connection name:\n%s", proxyEnv)
			}

			daemonEnvBytes, err := os.ReadFile(filepath.Join(etcDir, "daemon.env"))
			if err != nil {
				t.Fatalf("reading daemon.env: %v", err)
			}
			daemonEnv := string(daemonEnvBytes)
			want := append([]string{
				"GRAIN_DEPLOYMENT_OWNER=alice@example.com",
				"GRAIN_EXTRA_DAEMON_ARGS=-deployment-owner=alice@example.com",
				"GRAIN_ALLOWED_PRINCIPALS=user:alice@example.com,group:eng@example.com",
				"GRAIN_REGISTRY_ISSUER=" + hub.RegistryAssertionIssuer,
				"GRAIN_REGISTRY_AUDIENCE=https://alpha.instances.example.com",
				"GRAIN_REGISTRY_KEYS_URL=https://registry.example.com/.well-known/jwks.json",
				"GRAIN_REQUIRE_VERIFIER=registry",
				"GRAIN_DISABLE_SELF_UPGRADE=true",
				"GRAIN_MANAGEMENT_URL=https://registry.example.com/entries/alpha",
				"GRAIN_DB_DRIVER=postgres",
				"GRAIN_SECRETS_BACKEND=secretmanager",
				"GRAIN_SECRETS_SM_PREFIX=projects/test-proj/secrets/grain-inst-alpha-",
				"GRAIN_LOCAL_API_TOKEN_FILE=" + filepath.Join(runDir, "local-api-token"),
				"GRAIN_OBJECT_STORE=gs://grain-state/instances/alpha/",
			}, tc.want...)
			for _, line := range want {
				if line = strings.ReplaceAll(line, "{cloudsql}", cloudsqlDir); !strings.Contains(daemonEnv, line+"\n") {
					t.Errorf("daemon.env is missing %q:\n%s", line, daemonEnv)
				}
			}
		})
	}
}
