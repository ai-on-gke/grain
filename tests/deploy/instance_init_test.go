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

package deploy

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestGrainInstanceInitSkipsWhenNoMetadataPresent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()

	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	etcDir := filepath.Join(root, "etc")
	runDir := filepath.Join(root, "run-secrets")

	script := filepath.Join(repoRoot(t), "scripts", "grain-instance-init.sh")
	runBash(t, "bash "+script,
		"GRAIN_METADATA_URL="+srv.URL+"/computeMetadata/v1",
		"GRAIN_DATA_DIR="+dataDir,
		"GRAIN_ETC_DIR="+etcDir,
		"GRAIN_SECRETS_RUN_DIR="+runDir,
		"GRAIN_SKIP_TMPFS_MOUNT=1",
		"GRAIN_SKIP_SYSTEMCTL=1",
	)

	if _, err := os.Stat(filepath.Join(etcDir, "daemon.env")); !os.IsNotExist(err) {
		t.Fatalf("daemon.env should not be written when grain-config metadata is 404, got err=%v", err)
	}
}

func TestGrainInstanceInitFirstBootAndReplacementBoot(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}

	// Create a bare config repository with an initial commit on main.
	bareRepo := filepath.Join(t.TempDir(), "grain-inst-alpha.git")
	if out, err := exec.Command("git", "init", "--bare", "--initial-branch=main", bareRepo).CombinedOutput(); err != nil {
		t.Fatalf("git init --bare: %v: %s", err, out)
	}
	seedWork := filepath.Join(t.TempDir(), "seed-work")
	if out, err := exec.Command("git", "clone", bareRepo, seedWork).CombinedOutput(); err != nil {
		t.Fatalf("git clone: %v: %s", err, out)
	}
	for _, kv := range [][2]string{{"user.name", "grain"}, {"user.email", "grain@localhost"}} {
		if out, err := exec.Command("git", "-C", seedWork, "config", kv[0], kv[1]).CombinedOutput(); err != nil {
			t.Fatalf("git config: %v: %s", err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(seedWork, "README.md"), []byte("# alpha config\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "-C", seedWork, "add", "README.md").CombinedOutput(); err != nil {
		t.Fatalf("git add: %v: %s", err, out)
	}
	if out, err := exec.Command("git", "-C", seedWork, "commit", "-m", "initial config").CombinedOutput(); err != nil {
		t.Fatalf("git commit: %v: %s", err, out)
	}
	if out, err := exec.Command("git", "-C", seedWork, "push", "origin", "HEAD:main").CombinedOutput(); err != nil {
		t.Fatalf("git push: %v: %s", err, out)
	}

	var mu sync.Mutex
	secrets := map[string]string{
		"projects/my-proj/secrets/grain-inst-alpha-config-repo-key": "-----BEGIN OPENSSH PRIVATE KEY-----\ntest-deploy-key-material\n-----END OPENSSH PRIVATE KEY-----",
	}
	addVersionCalls := 0

	grainConfig := map[string]string{
		"registry_id":      "reg-1234",
		"instance":         "alpha",
		"deployment_owner": "alice@example.com",
		"owner_group":      "eng@example.com",
		"issuer":           "https://grain.example.com",
		"instance_domain":  "instances.grain.example.com",
		"sql_instance":     "my-proj:us-central1:grain-shared-pg",
		"sm_prefix":        "projects/my-proj/secrets/grain-inst-alpha-",
		"object_store":     "gs://grain-artifacts/instances/alpha/",
		"config_repo":      bareRepo,
	}
	grainConfigBytes, err := json.Marshal(grainConfig)
	if err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/computeMetadata/v1/instance/attributes/grain-config":
			if r.Header.Get("Metadata-Flavor") != "Google" {
				http.Error(w, "missing Metadata-Flavor", http.StatusForbidden)
				return
			}
			_, _ = w.Write(grainConfigBytes)
		case r.URL.Path == "/computeMetadata/v1/instance/service-accounts/default/token":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"test-oauth-token","token_type":"Bearer","expires_in":3600}`))
		case r.URL.Path == "/computeMetadata/v1/instance/service-accounts/default/email":
			_, _ = w.Write([]byte("grain-vm-alpha@my-proj.iam.gserviceaccount.com"))
		case strings.HasPrefix(r.URL.Path, "/v1/") && strings.HasSuffix(r.URL.Path, "/versions/latest:access"):
			if r.Header.Get("Authorization") != "Bearer test-oauth-token" {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			secretName := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v1/"), "/versions/latest:access")
			mu.Lock()
			val, ok := secrets[secretName]
			mu.Unlock()
			if !ok {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			b64 := base64.StdEncoding.EncodeToString([]byte(val))
			_, _ = w.Write([]byte(`{"payload":{"data":"` + b64 + `"}}`))
		case strings.HasPrefix(r.URL.Path, "/v1/") && strings.HasSuffix(r.URL.Path, ":addVersion") && r.Method == http.MethodPost:
			if r.Header.Get("Authorization") != "Bearer test-oauth-token" {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			secretName := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v1/"), ":addVersion")
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
			mu.Lock()
			secrets[secretName] = string(decoded)
			addVersionCalls++
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"name":"` + secretName + `/versions/1"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	// 1. First boot: local-api-token does not exist in Secret Manager yet (404),
	// so grain-instance-init generates it, adds version 1, writes /run/grain-secrets,
	// clones config_repo, and writes /etc/grain/daemon.env.
	root1 := t.TempDir()
	dataDir1 := filepath.Join(root1, "data")
	etcDir1 := filepath.Join(root1, "etc")
	runDir1 := filepath.Join(root1, "run-secrets")
	cloudsqlDir1 := filepath.Join(root1, "cloudsql")

	script := filepath.Join(repoRoot(t), "scripts", "grain-instance-init.sh")
	runBash(t, "bash "+script,
		"GRAIN_METADATA_URL="+srv.URL+"/computeMetadata/v1",
		"GRAIN_SM_API_URL="+srv.URL,
		"GRAIN_DATA_DIR="+dataDir1,
		"GRAIN_ETC_DIR="+etcDir1,
		"GRAIN_SECRETS_RUN_DIR="+runDir1,
		"GRAIN_CLOUDSQL_DIR="+cloudsqlDir1,
		"GRAIN_SKIP_TMPFS_MOUNT=1",
		"GRAIN_SKIP_SYSTEMCTL=1",
	)

	mu.Lock()
	generatedToken := secrets["projects/my-proj/secrets/grain-inst-alpha-local-api-token"]
	callsAfterFirstBoot := addVersionCalls
	mu.Unlock()
	if callsAfterFirstBoot != 1 {
		t.Fatalf("addVersionCalls after first boot = %d, want 1", callsAfterFirstBoot)
	}
	if len(generatedToken) != 64 {
		t.Fatalf("generated local-api-token length = %d (%q), want 64 hex chars", len(generatedToken), generatedToken)
	}

	runTokenBytes, err := os.ReadFile(filepath.Join(runDir1, "local-api-token"))
	if err != nil {
		t.Fatalf("reading run-secrets/local-api-token: %v", err)
	}
	if strings.TrimSpace(string(runTokenBytes)) != generatedToken {
		t.Fatalf("run-secrets/local-api-token = %q, want %q", strings.TrimSpace(string(runTokenBytes)), generatedToken)
	}
	if info, err := os.Stat(filepath.Join(runDir1, "local-api-token")); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("run-secrets/local-api-token mode = %v, err = %v; want 0600", info.Mode().Perm(), err)
	}

	keyBytes, err := os.ReadFile(filepath.Join(runDir1, "config-repo-key"))
	if err != nil {
		t.Fatalf("reading run-secrets/config-repo-key: %v", err)
	}
	contains(t, string(keyBytes), "test-deploy-key-material")
	if info, err := os.Stat(filepath.Join(runDir1, "config-repo-key")); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("run-secrets/config-repo-key mode = %v, err = %v; want 0600", info.Mode().Perm(), err)
	}

	// Verify config_repo was cloned into dataDir1/state-repo and state-repo.json was written in mirror mode.
	clonedReadme, err := os.ReadFile(filepath.Join(dataDir1, "state-repo", "README.md"))
	if err != nil {
		t.Fatalf("reading cloned state-repo/README.md: %v", err)
	}
	contains(t, string(clonedReadme), "# alpha config")

	stateRepoJSON, err := os.ReadFile(filepath.Join(dataDir1, "state-repo.json"))
	if err != nil {
		t.Fatalf("reading state-repo.json: %v", err)
	}
	contains(t, string(stateRepoJSON), `"mode": "mirror"`)
	contains(t, string(stateRepoJSON), `"remote": "`+bareRepo+`"`)
	contains(t, string(stateRepoJSON), `"sshKeyFile": "`+filepath.Join(runDir1, "config-repo-key")+`"`)

	// Verify /etc/grain/cloud-sql-proxy.env and /etc/grain/daemon.env.
	proxyEnv, err := os.ReadFile(filepath.Join(etcDir1, "cloud-sql-proxy.env"))
	if err != nil {
		t.Fatalf("reading cloud-sql-proxy.env: %v", err)
	}
	contains(t, string(proxyEnv), "CLOUD_SQL_INSTANCE=my-proj:us-central1:grain-shared-pg")

	daemonEnvBytes, err := os.ReadFile(filepath.Join(etcDir1, "daemon.env"))
	if err != nil {
		t.Fatalf("reading daemon.env: %v", err)
	}
	daemonEnv := string(daemonEnvBytes)
	for _, want := range []string{
		"GRAIN_DEPLOYMENT_OWNER=alice@example.com",
		"GRAIN_EXTRA_DAEMON_ARGS=-deployment-owner=alice@example.com",
		"GRAIN_ALLOWED_PRINCIPALS=user:alice@example.com,group:eng@example.com",
		"GRAIN_REGISTRY_ISSUER=https://grain.example.com",
		"GRAIN_REGISTRY_AUDIENCE=https://alpha.instances.grain.example.com",
		"GRAIN_REGISTRY_KEYS_URL=https://grain.example.com/.well-known/jwks.json",
		"GRAIN_REQUIRE_VERIFIER=registry",
		"GRAIN_DISABLE_SELF_UPGRADE=true",
		"GRAIN_MANAGEMENT_URL=https://grain.example.com/entries/alpha",
		"GRAIN_DB_DRIVER=postgres",
		"GRAIN_DB_DSN=postgres://grain-vm-alpha@my-proj.iam@/grain_alpha?host=" + cloudsqlDir1 + "/my-proj:us-central1:grain-shared-pg",
		"GRAIN_SECRETS_BACKEND=secretmanager",
		"GRAIN_SECRETS_SM_PREFIX=projects/my-proj/secrets/grain-inst-alpha-",
		"GRAIN_LOCAL_API_TOKEN_FILE=" + filepath.Join(runDir1, "local-api-token"),
		"GRAIN_STATE_REPO_SSH_KEY=" + filepath.Join(runDir1, "config-repo-key"),
		"GRAIN_OBJECT_STORE=gs://grain-artifacts/instances/alpha/",
	} {
		contains(t, daemonEnv, want)
	}

	// 2. Replacement VM boot: local-api-token already exists in Secret Manager,
	// so grain-instance-init fetches it without calling addVersion again.
	root2 := t.TempDir()
	runDir2 := filepath.Join(root2, "run-secrets")
	runBash(t, "bash "+script,
		"GRAIN_METADATA_URL="+srv.URL+"/computeMetadata/v1",
		"GRAIN_SM_API_URL="+srv.URL,
		"GRAIN_DATA_DIR="+filepath.Join(root2, "data"),
		"GRAIN_ETC_DIR="+filepath.Join(root2, "etc"),
		"GRAIN_SECRETS_RUN_DIR="+runDir2,
		"GRAIN_CLOUDSQL_DIR="+filepath.Join(root2, "cloudsql"),
		"GRAIN_SKIP_TMPFS_MOUNT=1",
		"GRAIN_SKIP_SYSTEMCTL=1",
	)
	mu.Lock()
	callsAfterSecondBoot := addVersionCalls
	mu.Unlock()
	if callsAfterSecondBoot != 1 {
		t.Fatalf("addVersionCalls after replacement boot = %d, want 1 (existing token reused)", callsAfterSecondBoot)
	}
	runToken2, err := os.ReadFile(filepath.Join(runDir2, "local-api-token"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(runToken2)) != generatedToken {
		t.Fatalf("replacement VM token = %q, want %q", strings.TrimSpace(string(runToken2)), generatedToken)
	}
}

func TestBakeGCEReleaseImagePrepareGuestAndFamily(t *testing.T) {
	bakeRoot := t.TempDir()
	systemdDir := filepath.Join(bakeRoot, "etc", "systemd", "system")
	if err := os.MkdirAll(systemdDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Seed a grain-daemon.service matching setup.sh's bounded shutdown drain and daemon.env hook,
	// plus builder-local artifacts that --prepare-guest must scrub.
	unit := `[Unit]
Description=grain daemon

[Service]
Type=simple
EnvironmentFile=/etc/grain/image.env
EnvironmentFile=-/etc/grain/daemon.env
EnvironmentFile=-/etc/default/grain-daemon
ExecStart=/usr/bin/docker run --name grain-daemon ${GRAIN_IMAGE} daemon
ExecStop=-/usr/bin/docker stop --time 300 grain-daemon
TimeoutStopSec=330
`
	if err := os.WriteFile(filepath.Join(systemdDir, "grain-daemon.service"), []byte(unit), 0o644); err != nil {
		t.Fatal(err)
	}
	dataDir := filepath.Join(bakeRoot, "var", "lib", "grain", "data")
	if err := os.MkdirAll(filepath.Join(dataDir, "secrets"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "grain.db"), []byte("stale-builder-db"), 0o600); err != nil {
		t.Fatal(err)
	}

	bakeScript := filepath.Join(repoRoot(t), "scripts", "bake-gce-release-image.sh")
	runBash(t, "bash "+bakeScript+" --prepare-guest --version v2.5.0",
		"GRAIN_BAKE_ROOT="+bakeRoot,
		"GRAIN_BAKE_SKIP_SETUP=1",
		"GRAIN_BAKE_SKIP_DOWNLOAD=1",
	)

	// Verify cloud-sql-proxy.service uses --auto-iam-authn and /cloudsql unix socket.
	sqlUnit, err := os.ReadFile(filepath.Join(systemdDir, "cloud-sql-proxy.service"))
	if err != nil {
		t.Fatalf("reading cloud-sql-proxy.service: %v", err)
	}
	contains(t, string(sqlUnit), "ExecStart=/usr/local/bin/cloud-sql-proxy --auto-iam-authn --unix-socket /cloudsql ${CLOUD_SQL_INSTANCE}")
	contains(t, string(sqlUnit), "ConditionPathExists=/etc/grain/cloud-sql-proxy.env")

	// Verify grain-instance-init.service ordering relative to grain-block-metadata.service and grain-daemon.service.
	initUnit, err := os.ReadFile(filepath.Join(systemdDir, "grain-instance-init.service"))
	if err != nil {
		t.Fatalf("reading grain-instance-init.service: %v", err)
	}
	contains(t, string(initUnit), "After=network-online.target grain-block-metadata.service docker.service")
	contains(t, string(initUnit), "Before=grain-daemon.service grain-hostd.service")
	contains(t, string(initUnit), "ExecStart=/usr/local/lib/grain/grain-instance-init.sh")

	// Verify release-image stamp and family grain-release.
	releaseStamp, err := os.ReadFile(filepath.Join(bakeRoot, "etc", "grain", "release-image"))
	if err != nil {
		t.Fatalf("reading /etc/grain/release-image: %v", err)
	}
	contains(t, string(releaseStamp), "GRAIN_RELEASE_VERSION=v2.5.0")
	contains(t, string(releaseStamp), "GRAIN_IMAGE_FAMILY=grain-release")

	// Verify builder-local grain.db was scrubbed.
	if _, err := os.Stat(filepath.Join(dataDir, "grain.db")); !os.IsNotExist(err) {
		t.Fatalf("builder grain.db was not scrubbed by --prepare-guest: err=%v", err)
	}

	// Verify orchestrator mode publishes into --family=grain-release via gcloud.
	binDir := t.TempDir()
	gcloudLog := filepath.Join(t.TempDir(), "gcloud.log")
	mockGcloud := filepath.Join(binDir, "gcloud")
	if err := os.WriteFile(mockGcloud, []byte("#!/usr/bin/env bash\necho \"gcloud $*\" >> "+gcloudLog+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	runBash(t, "bash "+bakeScript+" --project my-registry-proj --zone us-central1-a --version v2.5.0",
		"PATH="+binDir+":"+os.Getenv("PATH"),
	)
	logged, err := os.ReadFile(gcloudLog)
	if err != nil {
		t.Fatal(err)
	}
	contains(t, string(logged), "gcloud compute instances create")
	contains(t, string(logged), "--prepare-guest")
	contains(t, string(logged), "gcloud compute instances stop")
	contains(t, string(logged), "gcloud compute images create")
	contains(t, string(logged), "--family=grain-release")
	contains(t, string(logged), "gcloud compute instances delete")
}

// A Secret Manager blip at boot must not cost the VM its daemon.env:
// grain-instance-init.service runs once and grain-daemon.service starts after
// it either way. 429/5xx reads of the database password are retried, a
// persistent one still fails closed, and a 403 is not retried (task 3731).
func TestGrainInstanceInitRetriesTransientSecretManagerErrors(t *testing.T) {
	const secret = "projects/made-up-proj/secrets/grain-inst-walk1-db-password"
	const password = "walk-password-0123"
	cases := []struct {
		name      string
		failCode  int
		failures  int // -1: every read fails
		wantOK    bool
		wantReads int
	}{
		{name: "503 twice then served", failCode: http.StatusServiceUnavailable, failures: 2, wantOK: true, wantReads: 3},
		{name: "429 once then served", failCode: http.StatusTooManyRequests, failures: 1, wantOK: true, wantReads: 2},
		{name: "503 on every attempt", failCode: http.StatusServiceUnavailable, failures: -1, wantOK: false, wantReads: 4},
		{name: "403 is not retried", failCode: http.StatusForbidden, failures: -1, wantOK: false, wantReads: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			reads, failures := 0, tc.failures
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/computeMetadata/v1/instance/attributes/grain-config":
					_ = json.NewEncoder(w).Encode(map[string]string{
						"instance": "walk1", "deployment_owner": "alice@example.com",
						"db_user": "gi_walk1", "db_name": "grain_walk1", "sql_private_ip": "10.0.0.5",
						"db_password_secret": secret,
					})
				case "/computeMetadata/v1/instance/service-accounts/default/token":
					_, _ = w.Write([]byte(`{"access_token":"vm-token"}`))
				case "/v1/" + secret + "/versions/latest:access":
					mu.Lock()
					reads++
					fail := failures != 0
					if failures > 0 {
						failures--
					}
					mu.Unlock()
					if fail {
						http.Error(w, `{"error":{"status":"UNAVAILABLE"}}`, tc.failCode)
						return
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"payload": map[string]string{
						"data": base64.StdEncoding.EncodeToString([]byte(password)),
					}})
				default:
					http.NotFound(w, r)
				}
			}))
			defer srv.Close()

			me, err := user.Current()
			if err != nil {
				t.Fatal(err)
			}
			root := t.TempDir()
			etcDir := filepath.Join(root, "etc")
			cmd := exec.Command("bash", filepath.Join(repoRoot(t), "scripts", "grain-instance-init.sh"))
			cmd.Env = append(os.Environ(),
				"GRAIN_METADATA_URL="+srv.URL+"/computeMetadata/v1",
				"GRAIN_SM_API_URL="+srv.URL,
				"GRAIN_DATA_DIR="+filepath.Join(root, "data"),
				"GRAIN_ETC_DIR="+etcDir,
				"GRAIN_SECRETS_RUN_DIR="+filepath.Join(root, "run-secrets"),
				"GRAIN_SKIP_TMPFS_MOUNT=1",
				"GRAIN_SKIP_SYSTEMCTL=1",
				"GRAIN_SM_ATTEMPTS=4",
				"GRAIN_SM_RETRY_DELAY=0",
				"GRAIN_USER="+me.Username,
			)
			out, err := cmd.CombinedOutput()
			if strings.Contains(string(out), password) {
				t.Fatalf("instance init printed the database password:\n%s", out)
			}
			env, readErr := os.ReadFile(filepath.Join(etcDir, "daemon.env"))
			if tc.wantOK {
				if err != nil || readErr != nil {
					t.Fatalf("instance init: %v (daemon.env: %v)\n%s", err, readErr, out)
				}
				if !strings.Contains(string(env), "GRAIN_DB_DSN=postgres://gi_walk1:"+password+"@10.0.0.5:5432/grain_walk1?sslmode=require") {
					t.Fatalf("daemon.env = %s, want the password DSN with sslmode=require", env)
				}
			} else {
				if err == nil || readErr == nil {
					t.Fatalf("instance init should fail closed without daemon.env: err=%v daemon.env err=%v\n%s", err, readErr, out)
				}
				if !strings.Contains(string(out), secret) {
					t.Fatalf("failure output does not name the secret:\n%s", out)
				}
			}
			mu.Lock()
			defer mu.Unlock()
			if reads != tc.wantReads {
				t.Fatalf("Secret Manager reads = %d, want %d\n%s", reads, tc.wantReads, out)
			}
		})
	}
}
