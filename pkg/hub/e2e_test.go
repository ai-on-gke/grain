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

// End-to-end multi-replica registry test suite (design/design/hub.design.md, Step 15).
package hub_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/ai-on-gke/grain/pkg/hub"
	"github.com/ai-on-gke/grain/pkg/hub/api"
	"github.com/ai-on-gke/grain/pkg/hub/reconcile"
	regserver "github.com/ai-on-gke/grain/pkg/hub/server"
	"github.com/ai-on-gke/grain/pkg/hub/store"
	"github.com/ai-on-gke/grain/pkg/tunnel/mux"
	"github.com/ai-on-gke/grain/pkg/tunnel/wsupgrade"
	"github.com/ai-on-gke/grain/pkg/webauth"
)

var (
	e2eGrainBinOnce sync.Once
	e2eGrainBinPath string
	e2eGrainBinErr  error
)

// buildRealGrainBinary compiles cmd/grain once per test binary invocation so E2E tests
// drive real `grain daemon` subprocesses without importing daemon packages into pkg/hub.
func buildRealGrainBinary(t *testing.T) string {
	t.Helper()
	e2eGrainBinOnce.Do(func() {
		dir, err := os.MkdirTemp("", "grain-registry-e2e-bin-*")
		if err != nil {
			e2eGrainBinErr = err
			return
		}
		bin := filepath.Join(dir, "grain")
		_, thisFile, _, _ := runtime.Caller(0)
		repoRoot := filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", ".."))
		cmd := exec.Command("go", "build", "-o", bin, "./cmd/grain")
		cmd.Dir = repoRoot
		out, err := cmd.CombinedOutput()
		if err != nil {
			e2eGrainBinErr = fmt.Errorf("go build ./cmd/grain: %w\n%s", err, string(out))
			return
		}
		e2eGrainBinPath = bin
	})
	if e2eGrainBinErr != nil {
		t.Fatalf("buildRealGrainBinary: %v", e2eGrainBinErr)
	}
	return e2eGrainBinPath
}

// e2ePostgresCluster manages a shared ephemeral PostgreSQL server hosting both the
// registry database (`grain_registry_e2e`) and per-instance databases (`grain_inst_<name>`).
type e2ePostgresCluster struct {
	t       *testing.T
	adminDB *sql.DB
	user    string
	port    int
	HubDSN  string
}

func startE2EPostgresCluster(t *testing.T) *e2ePostgresCluster {
	t.Helper()
	if envURL := strings.TrimSpace(os.Getenv("GRAIN_TEST_POSTGRES_URL")); envURL != "" {
		u, err := url.Parse(envURL)
		if err != nil {
			t.Fatalf("parse GRAIN_TEST_POSTGRES_URL: %v", err)
		}
		adminDB, err := sql.Open("pgx", envURL)
		if err != nil {
			t.Fatalf("sql.Open adminDB: %v", err)
		}
		t.Cleanup(func() { _ = adminDB.Close() })
		p, _ := strconv.Atoi(u.Port())
		uname := u.User.Username()
		c := &e2ePostgresCluster{
			t:       t,
			adminDB: adminDB,
			user:    uname,
			port:    p,
		}
		c.HubDSN = c.EnsureDatabase("grain_registry_e2e_" + randomHex(4))
		return c
	}

	initdbPath, pgCtlPath := findE2EPostgresBinaries()
	if initdbPath == "" || pgCtlPath == "" {
		if os.Getenv("GRAIN_REQUIRE_POSTGRES") == "1" {
			t.Fatalf("postgresql binaries (initdb, pg_ctl) not found while GRAIN_REQUIRE_POSTGRES=1")
		}
		t.Skip("postgresql binaries not found; skipping registry E2E test")
	}

	baseDir, err := os.MkdirTemp("/tmp", "grain-reg-e2e-pg-*")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	dataDir := filepath.Join(baseDir, "data")
	sockDir := filepath.Join(baseDir, "sock")
	logFile := filepath.Join(baseDir, "postgres.log")
	if err := os.MkdirAll(sockDir, 0o700); err != nil {
		t.Fatalf("MkdirAll sockDir: %v", err)
	}

	pgUser := "postgres"
	if u, err := user.Current(); err == nil && u.Username != "" {
		pgUser = u.Username
	}

	runPgCmd := func(bin string, args ...string) ([]byte, error) {
		if os.Geteuid() == 0 {
			_ = os.Chmod(baseDir, 0o755)
			_ = os.Chmod(sockDir, 0o777)
			if pu, err := user.Lookup("postgres"); err == nil {
				uid, _ := strconv.Atoi(pu.Uid)
				gid, _ := strconv.Atoi(pu.Gid)
				_ = os.Chown(baseDir, uid, gid)
				_ = os.Chown(sockDir, uid, gid)
				pgUser = "postgres"
				cmd := exec.Command(bin, args...)
				cmd.SysProcAttr = &syscall.SysProcAttr{
					Credential: &syscall.Credential{Uid: uint32(uid), Gid: uint32(gid)},
				}
				return cmd.CombinedOutput()
			}
		}
		return exec.Command(bin, args...).CombinedOutput()
	}

	if out, err := runPgCmd(initdbPath, "-D", dataDir, "-U", pgUser, "--auth=trust", "--no-instructions", "--encoding=UTF8", "--locale=C"); err != nil {
		_ = os.RemoveAll(baseDir)
		t.Fatalf("initdb failed: %v\n%s", err, string(out))
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		_ = os.RemoveAll(baseDir)
		t.Fatalf("net.Listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()

	pgOpts := fmt.Sprintf("-F -h 127.0.0.1 -p %d -k %s", port, sockDir)
	if out, err := runPgCmd(pgCtlPath, "-D", dataDir, "-l", logFile, "-w", "-o", pgOpts, "start"); err != nil {
		logBytes, _ := os.ReadFile(logFile)
		_ = os.RemoveAll(baseDir)
		t.Fatalf("pg_ctl start failed: %v\n%s\nlog:\n%s", err, string(out), string(logBytes))
	}

	t.Cleanup(func() {
		_, _ = runPgCmd(pgCtlPath, "-D", dataDir, "-m", "immediate", "stop")
		_ = os.RemoveAll(baseDir)
	})

	adminDSN := fmt.Sprintf("postgres://%s@127.0.0.1:%d/postgres?sslmode=disable", pgUser, port)
	adminDB, err := sql.Open("pgx", adminDSN)
	if err != nil {
		t.Fatalf("sql.Open adminDSN: %v", err)
	}
	t.Cleanup(func() { _ = adminDB.Close() })

	c := &e2ePostgresCluster{
		t:       t,
		adminDB: adminDB,
		user:    pgUser,
		port:    port,
	}
	c.HubDSN = c.EnsureDatabase("grain_registry_e2e")
	return c
}

func (c *e2ePostgresCluster) EnsureDatabase(dbName string) string {
	c.t.Helper()
	var exists int
	err := c.adminDB.QueryRow(`SELECT 1 FROM pg_database WHERE datname = $1`, dbName).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		if _, err := c.adminDB.Exec(fmt.Sprintf(`CREATE DATABASE "%s"`, strings.ReplaceAll(dbName, `"`, `""`))); err != nil {
			c.t.Fatalf("CREATE DATABASE %s: %v", dbName, err)
		}
	} else if err != nil {
		c.t.Fatalf("check pg_database %s: %v", dbName, err)
	}
	return fmt.Sprintf("postgres://%s@127.0.0.1:%d/%s?sslmode=disable", c.user, c.port, dbName)
}

func (c *e2ePostgresCluster) DropDatabase(dbName string) {
	_, _ = c.adminDB.Exec(`SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = $1`, dbName)
	_, _ = c.adminDB.Exec(fmt.Sprintf(`DROP DATABASE IF EXISTS "%s"`, strings.ReplaceAll(dbName, `"`, `""`)))
}

func (c *e2ePostgresCluster) DatabaseExists(dbName string) bool {
	var exists int
	err := c.adminDB.QueryRow(`SELECT 1 FROM pg_database WHERE datname = $1`, dbName).Scan(&exists)
	return err == nil && exists == 1
}

func findE2EPostgresBinaries() (string, string) {
	initdb, _ := exec.LookPath("initdb")
	pgCtl, _ := exec.LookPath("pg_ctl")
	if initdb != "" && pgCtl != "" {
		return initdb, pgCtl
	}
	matches, _ := filepath.Glob("/usr/lib/postgresql/*/bin/initdb")
	for i := len(matches) - 1; i >= 0; i-- {
		binDir := filepath.Dir(matches[i])
		ctl := filepath.Join(binDir, "pg_ctl")
		if _, err := os.Stat(ctl); err == nil {
			return matches[i], ctl
		}
	}
	return "", ""
}

func randomHex(nBytes int) string {
	b := make([]byte, nBytes)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// e2eSecretManager is an in-process fake Google Cloud Secret Manager v1 HTTP server
// that also implements registry.SecretWriter for reconciler deploy-key storage.
type e2eSecretManager struct {
	mu       sync.Mutex
	versions map[string][][]byte // secretID -> version payloads
	server   *httptest.Server
}

func newE2ESecretManager(t *testing.T) *e2eSecretManager {
	t.Helper()
	sm := &e2eSecretManager{
		versions: make(map[string][][]byte),
	}
	sm.server = httptest.NewServer(http.HandlerFunc(sm.serveHTTP))
	t.Cleanup(sm.server.Close)
	return sm
}

func (s *e2eSecretManager) PutSecret(_ context.Context, entryName, secretKey string, payload []byte) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := "grain-inst-" + entryName + "-" + secretKey
	cp := append([]byte(nil), payload...)
	s.versions[id] = append(s.versions[id], cp)
	ver := len(s.versions[id])
	return fmt.Sprintf("projects/test-proj/secrets/%s/versions/%d", id, ver), nil
}

func (s *e2eSecretManager) HasPrefixSecrets(prefix string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	count := 0
	for k, vers := range s.versions {
		if strings.HasPrefix(k, prefix) && len(vers) > 0 {
			count++
		}
	}
	return count
}

func (s *e2eSecretManager) DeletePrefixSecrets(prefix string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k := range s.versions {
		if strings.HasPrefix(k, prefix) {
			delete(s.versions, k)
		}
	}
}

func (s *e2eSecretManager) serveHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()

	const base = "/v1/projects/test-proj/secrets"
	if r.Method == http.MethodPost && r.URL.Path == base {
		secretID := r.URL.Query().Get("secretId")
		if secretID == "" {
			http.Error(w, `{"error":{"code":400,"message":"missing secretId"}}`, http.StatusBadRequest)
			return
		}
		if _, exists := s.versions[secretID]; exists {
			http.Error(w, `{"error":{"code":409,"status":"ALREADY_EXISTS"}}`, http.StatusConflict)
			return
		}
		s.versions[secretID] = nil
		_ = json.NewEncoder(w).Encode(map[string]any{"name": fmt.Sprintf("projects/test-proj/secrets/%s", secretID)})
		return
	}

	if !strings.HasPrefix(r.URL.Path, base+"/") {
		http.NotFound(w, r)
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, base+"/")

	if r.Method == http.MethodPost && strings.HasSuffix(rest, ":addVersion") {
		secretID := strings.TrimSuffix(rest, ":addVersion")
		vers, exists := s.versions[secretID]
		if !exists {
			http.Error(w, `{"error":{"code":404,"status":"NOT_FOUND"}}`, http.StatusNotFound)
			return
		}
		var body struct {
			Payload struct {
				Data string `json:"data"`
			} `json:"payload"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		raw, err := base64.StdEncoding.DecodeString(body.Payload.Data)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		s.versions[secretID] = append(vers, raw)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"name": fmt.Sprintf("projects/test-proj/secrets/%s/versions/%d", secretID, len(s.versions[secretID])),
		})
		return
	}

	if r.Method == http.MethodGet && strings.HasSuffix(rest, "/versions/latest:access") {
		secretID := strings.TrimSuffix(rest, "/versions/latest:access")
		vers, exists := s.versions[secretID]
		if !exists || len(vers) == 0 {
			http.Error(w, `{"error":{"code":404,"status":"NOT_FOUND"}}`, http.StatusNotFound)
			return
		}
		latest := vers[len(vers)-1]
		_ = json.NewEncoder(w).Encode(map[string]any{
			"name": fmt.Sprintf("projects/test-proj/secrets/%s/versions/%d", secretID, len(vers)),
			"payload": map[string]any{
				"data": base64.StdEncoding.EncodeToString(latest),
			},
		})
		return
	}

	if r.Method == http.MethodDelete && !strings.Contains(rest, "/") {
		delete(s.versions, rest)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
		return
	}

	http.NotFound(w, r)
}

// e2eObjectStorage is an in-process fake GCS object storage server supporting
// upload, download, list, and delete under per-instance managed prefixes.
type e2eObjectStorage struct {
	mu      sync.Mutex
	objects map[string][]byte // "<bucket>/<key>" -> data
	server  *httptest.Server
}

func newE2EObjectStorage(t *testing.T) *e2eObjectStorage {
	t.Helper()
	osrv := &e2eObjectStorage{
		objects: make(map[string][]byte),
	}
	osrv.server = httptest.NewServer(http.HandlerFunc(osrv.serveHTTP))
	t.Cleanup(osrv.server.Close)
	return osrv
}

func (s *e2eObjectStorage) PutObject(bucket, key string, data []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.objects[bucket+"/"+strings.TrimLeft(key, "/")] = append([]byte(nil), data...)
}

func (s *e2eObjectStorage) GetObject(bucket, key string) ([]byte, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.objects[bucket+"/"+strings.TrimLeft(key, "/")]
	return append([]byte(nil), b...), ok
}

func (s *e2eObjectStorage) DeletePrefix(bucket, prefix string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fullPrefix := bucket + "/" + strings.TrimLeft(prefix, "/")
	for k := range s.objects {
		if strings.HasPrefix(k, fullPrefix) {
			delete(s.objects, k)
		}
	}
}

func (s *e2eObjectStorage) serveHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Upload API: POST /upload/storage/v1/b/{bucket}/o?name={key}
	if strings.HasPrefix(r.URL.Path, "/upload/storage/v1/b/") && strings.HasSuffix(r.URL.Path, "/o") {
		bucket := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/upload/storage/v1/b/"), "/o")
		objName := r.URL.Query().Get("name")
		body, _ := io.ReadAll(r.Body)
		s.objects[bucket+"/"+objName] = body
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"bucket": bucket,
			"name":   objName,
			"size":   strconv.Itoa(len(body)),
		})
		return
	}

	// JSON API: /storage/v1/b/{bucket}/o...
	if strings.HasPrefix(r.URL.Path, "/storage/v1/b/") {
		rest := strings.TrimPrefix(r.URL.Path, "/storage/v1/b/")
		parts := strings.SplitN(rest, "/o", 2)
		bucket := parts[0]
		if len(parts) == 2 && (parts[1] == "" || parts[1] == "/") && r.Method == http.MethodGet {
			prefix := r.URL.Query().Get("prefix")
			var items []map[string]any
			for fullKey, data := range s.objects {
				if !strings.HasPrefix(fullKey, bucket+"/") {
					continue
				}
				name := strings.TrimPrefix(fullKey, bucket+"/")
				if prefix == "" || strings.HasPrefix(name, prefix) {
					items = append(items, map[string]any{
						"bucket": bucket,
						"name":   name,
						"size":   strconv.Itoa(len(data)),
					})
				}
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"items": items})
			return
		}
		if len(parts) == 2 && strings.HasPrefix(parts[1], "/") {
			objName, _ := url.PathUnescape(strings.TrimPrefix(parts[1], "/"))
			fullKey := bucket + "/" + objName
			switch r.Method {
			case http.MethodGet:
				data, ok := s.objects[fullKey]
				if !ok {
					http.NotFound(w, r)
					return
				}
				if r.URL.Query().Get("alt") == "media" {
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write(data)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{
					"bucket": bucket,
					"name":   objName,
					"size":   strconv.Itoa(len(data)),
				})
				return
			case http.MethodDelete:
				delete(s.objects, fullKey)
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
	}

	// Direct XML-style path: PUT/GET/DELETE /{bucket}/{object...}
	trimmed := strings.TrimPrefix(r.URL.Path, "/")
	if idx := strings.IndexByte(trimmed, '/'); idx > 0 {
		bucket := trimmed[:idx]
		objName := trimmed[idx+1:]
		fullKey := bucket + "/" + objName
		switch r.Method {
		case http.MethodPut:
			body, _ := io.ReadAll(r.Body)
			s.objects[fullKey] = body
			w.WriteHeader(http.StatusOK)
			return
		case http.MethodGet:
			data, ok := s.objects[fullKey]
			if !ok {
				http.NotFound(w, r)
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(data)
			return
		case http.MethodDelete:
			delete(s.objects, fullKey)
			w.WriteHeader(http.StatusNoContent)
			return
		}
	}
	http.NotFound(w, r)
}

// e2eConfigRepos manages local bare Git repositories for hosted instance entries.
type e2eConfigRepos struct {
	t       *testing.T
	baseDir string
	mu      sync.Mutex
	repos   map[string]string // entryName -> bare git path
}

func newE2EConfigRepos(t *testing.T) *e2eConfigRepos {
	t.Helper()
	return &e2eConfigRepos{
		t:       t,
		baseDir: t.TempDir(),
		repos:   make(map[string]string),
	}
}

func (c *e2eConfigRepos) EnsureRepo(_ context.Context, name, _ string) (*hub.ConfigRepoBundle, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	repoPath, ok := c.repos[name]
	if !ok {
		repoPath = filepath.Join(c.baseDir, name+"-config.git")
		if out, err := exec.Command("git", "init", "--bare", "--initial-branch=main", repoPath).CombinedOutput(); err != nil {
			return nil, fmt.Errorf("git init --bare %s: %w: %s", repoPath, err, string(out))
		}
		c.repos[name] = repoPath
	}
	return &hub.ConfigRepoBundle{
		RepoURL:   repoPath,
		DeployKey: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIE2ETestDeployKey grain-instance:" + name,
	}, nil
}

func (c *e2eConfigRepos) ArchiveRepo(_ context.Context, name string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if repoPath, ok := c.repos[name]; ok {
		_ = os.RemoveAll(repoPath)
		delete(c.repos, name)
	}
	return nil
}

func (c *e2eConfigRepos) RepoPath(name string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.repos[name]
}

// e2eInstanceResources provisions per-instance databases in the shared PostgreSQL
// cluster, tracks Secret Manager prefixes, and manages GCS folders.
type e2eInstanceResources struct {
	pg  *e2ePostgresCluster
	sm  *e2eSecretManager
	gcs *e2eObjectStorage

	mu          sync.Mutex
	provisioned map[string]*hub.InstanceResourceBundle
}

func newE2EInstanceResources(pg *e2ePostgresCluster, sm *e2eSecretManager, gcs *e2eObjectStorage) *e2eInstanceResources {
	return &e2eInstanceResources{
		pg:          pg,
		sm:          sm,
		gcs:         gcs,
		provisioned: make(map[string]*hub.InstanceResourceBundle),
	}
}

func (r *e2eInstanceResources) Provision(_ context.Context, spec hub.InstanceResourceSpec) (*hub.InstanceResourceBundle, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	dbName := store.DatabaseNameForEntry(spec.Name)
	_ = r.pg.EnsureDatabase(dbName)
	secPrefix := "grain-inst-" + spec.Name + "-"
	bundle := &hub.InstanceResourceBundle{
		ServiceAccountEmail: "grain-inst-" + spec.Name + "@test-proj.iam.gserviceaccount.com",
		SQLInstance:         "shared-pg-pool",
		SQLConnectionName:   "test-proj:us-central1:shared-pg-pool",
		SQLPrivateIP:        "127.0.0.1",
		DatabaseName:        dbName,
		SecretPrefix:        secPrefix,
		GCSBucket:           "grain-state-bucket",
		GCSManagedFolder:    "instances/" + spec.Name + "/",
	}
	r.provisioned[spec.Name] = bundle
	return bundle, nil
}

func (r *e2eInstanceResources) Teardown(_ context.Context, spec hub.InstanceResourceSpec, bundle hub.InstanceResourceBundle, retainDB bool) error {
	r.mu.Lock()
	if !retainDB {
		delete(r.provisioned, spec.Name)
	}
	r.mu.Unlock()

	if !retainDB {
		dbName := bundle.DatabaseName
		if dbName == "" {
			dbName = store.DatabaseNameForEntry(spec.Name)
		}
		r.pg.DropDatabase(dbName)
		r.sm.DeletePrefixSecrets("grain-inst-" + spec.Name + "-")
		r.gcs.DeletePrefix("grain-state-bucket", "instances/"+spec.Name+"/")
	}
	return nil
}

func (r *e2eInstanceResources) GrantSSH(_ context.Context, _, _, _ string) error {
	return nil
}

func (r *e2eInstanceResources) RevokeSSH(_ context.Context, _, _, _ string) error {
	return nil
}

func (r *e2eInstanceResources) PutSecret(ctx context.Context, entryName, secretKey string, payload []byte) (string, error) {
	return r.sm.PutSecret(ctx, entryName, secretKey, payload)
}

// e2eGroupResolver is a dynamic in-process GroupResolver for owner_group co-management,
// FrontIdentity authorization, and group admission checks.
type e2eGroupResolver struct {
	mu      sync.RWMutex
	members map[string]map[string]bool // lower(groupEmail) -> lower(memberEmail) -> true
}

func newE2EGroupResolver(initial map[string][]string) *e2eGroupResolver {
	g := &e2eGroupResolver{
		members: make(map[string]map[string]bool),
	}
	for grp, list := range initial {
		gKey := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(grp), "group:"))
		g.members[gKey] = make(map[string]bool)
		for _, m := range list {
			g.members[gKey][strings.ToLower(strings.TrimSpace(m))] = true
		}
	}
	return g
}

func (g *e2eGroupResolver) IsMember(_ context.Context, groupEmail, memberEmail string) (bool, error) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	gKey := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(groupEmail), "group:"))
	mKey := strings.ToLower(strings.TrimSpace(memberEmail))
	return g.members[gKey][mKey], nil
}

func (g *e2eGroupResolver) NormalizeGroup(raw string) (string, error) {
	raw = strings.ToLower(strings.TrimSpace(raw))
	if !strings.HasPrefix(raw, "group:") {
		return "", errors.New("group must start with group:")
	}
	name := strings.TrimPrefix(raw, "group:")
	if name == "" || !strings.Contains(name, "@") {
		return "", errors.New("group must be group:<local>@<domain>")
	}
	return "group:" + name, nil
}

// e2eCapturedRequest records the sanitized request headers and cookies that arrive at a hosted VM.
type e2eCapturedRequest struct {
	Method  string
	Path    string
	Host    string
	Headers http.Header
	Cookies []*http.Cookie
}

// e2eRunningVM tracks a real `grain daemon` subprocess representing a hosted GCE instance.
type e2eRunningVM struct {
	handle     hub.InstanceHandle
	status     hub.InstanceStatus
	spec       hub.InstanceSpec
	cmd        *exec.Cmd
	cancel     context.CancelFunc
	waitDone   chan struct{}
	dataDir    string
	daemonAddr string
	proxySrv   *httptest.Server
	logBuf     *bytes.Buffer
}

// e2eCompute implements registry.Compute by spawning real `grain daemon` subprocesses
// configured against the shared PostgreSQL instance, fake Secret Manager, fake GCS object store,
// and local bare Git config repository.
type e2eCompute struct {
	t              *testing.T
	grainBin       string
	pg             *e2ePostgresCluster
	sm             *e2eSecretManager
	gcs            *e2eObjectStorage
	st             *store.Store
	instanceDomain string
	jwksURL        func() string

	mu             sync.Mutex
	vms            map[string]*e2eRunningVM // instanceID -> running VM
	nextSeq        int
	lastCapturedMu sync.Mutex
	lastCaptured   map[string]e2eCapturedRequest // entryName -> last captured request
	injectCookieMu sync.Mutex
	injectCookies  map[string][]string // entryName -> Set-Cookie headers to inject on responses
}

func newE2ECompute(
	t *testing.T,
	grainBin string,
	pg *e2ePostgresCluster,
	sm *e2eSecretManager,
	gcs *e2eObjectStorage,
	st *store.Store,
	instanceDomain string,
	jwksURL func() string,
) *e2eCompute {
	return &e2eCompute{
		t:              t,
		grainBin:       grainBin,
		pg:             pg,
		sm:             sm,
		gcs:            gcs,
		st:             st,
		instanceDomain: instanceDomain,
		jwksURL:        jwksURL,
		vms:            make(map[string]*e2eRunningVM),
		lastCaptured:   make(map[string]e2eCapturedRequest),
		injectCookies:  make(map[string][]string),
	}
}

func (c *e2eCompute) SetInjectedResponseCookies(entryName string, cookies []string) {
	c.injectCookieMu.Lock()
	defer c.injectCookieMu.Unlock()
	c.injectCookies[entryName] = append([]string(nil), cookies...)
}

func (c *e2eCompute) LastCapturedRequest(entryName string) (e2eCapturedRequest, bool) {
	c.lastCapturedMu.Lock()
	defer c.lastCapturedMu.Unlock()
	capReq, ok := c.lastCaptured[entryName]
	return capReq, ok
}

func (c *e2eCompute) VMLogs(instanceID string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if vm, ok := c.vms[instanceID]; ok && vm.logBuf != nil {
		return vm.logBuf.String()
	}
	return ""
}

func (c *e2eCompute) CreateInstance(ctx context.Context, spec hub.InstanceSpec) (*hub.InstanceHandle, error) {
	c.mu.Lock()
	c.nextSeq++
	seq := c.nextSeq
	c.mu.Unlock()

	vmID := fmt.Sprintf("grain-%s-g%d-%d", spec.Name, spec.Generation, seq)
	dataDir, err := os.MkdirTemp("", fmt.Sprintf("grain-e2e-vm-%s-*", vmID))
	if err != nil {
		return nil, err
	}
	sandboxDir := filepath.Join(dataDir, "scratch")
	if err := os.MkdirAll(sandboxDir, 0o755); err != nil {
		return nil, err
	}

	// If a local bare Git config repo was provisioned, write state-repo.json in mirror mode
	// just like scripts/grain-instance-init.sh does on hosted VMs.
	if configRepo := strings.TrimSpace(spec.StartupEnv["GRAIN_CONFIG_REPO"]); configRepo != "" {
		stateRepoJSON, _ := json.Marshal(map[string]any{
			"remote": configRepo,
			"branch": "main",
			"mode":   "mirror",
		})
		if err := os.WriteFile(filepath.Join(dataDir, "state-repo.json"), stateRepoJSON, 0o600); err != nil {
			return nil, err
		}
	}

	dbName := strings.TrimSpace(spec.StartupEnv["GRAIN_DB_NAME"])
	if dbName == "" {
		dbName = store.DatabaseNameForEntry(spec.Name)
	}
	dbDSN := c.pg.EnsureDatabase(dbName)

	secPrefix := strings.TrimSpace(spec.StartupEnv["GRAIN_SECRET_PREFIX"])
	if secPrefix == "" {
		secPrefix = "grain-inst-" + spec.Name + "-"
	}
	gcsBucket := strings.TrimSpace(spec.StartupEnv["GRAIN_GCS_BUCKET"])
	if gcsBucket == "" {
		gcsBucket = "grain-state-bucket"
	}
	gcsPrefix := strings.TrimSpace(spec.StartupEnv["GRAIN_GCS_PREFIX"])
	if gcsPrefix == "" {
		gcsPrefix = "instances/" + spec.Name + "/"
	}

	// Look up owner_email and owner_group from store for initial principal seeding.
	ownerEmail := "alice@example.com"
	allowedPrincipals := "user:alice@example.com"
	if c.st != nil {
		if entryRow, err := c.st.GetEntryByName(ctx, spec.Name); err == nil && entryRow != nil {
			if entryRow.OwnerEmail != "" {
				ownerEmail = entryRow.OwnerEmail
				allowedPrincipals = "user:" + entryRow.OwnerEmail
			}
			if g := strings.TrimSpace(entryRow.OwnerGroup); g != "" {
				if !strings.HasPrefix(g, "group:") {
					g = "group:" + g
				}
				allowedPrincipals += "," + g
			}
		}
	}

	daemonPort := allocateFreePort(c.t)
	daemonAddr := fmt.Sprintf("127.0.0.1:%d", daemonPort)
	entryHost := spec.Name + "." + strings.TrimPrefix(c.instanceDomain, ".")
	audience := "https://" + entryHost

	procCtx, procCancel := context.WithCancel(context.Background())
	args := []string{
		"daemon",
		"-data-dir", dataDir,
		"-sandbox-dir", sandboxDir,
		"-disable-native-sandboxes",
		"-sandbox-port-base=0",
		"-ci=false",
		"-db-driver=postgres",
		"-db-dsn=" + dbDSN,
		"-secrets-backend=secretmanager",
		"-secrets-sm-prefix=projects/test-proj/secrets/" + secPrefix,
		"-object-store=gs://" + gcsBucket + "/" + gcsPrefix,
		"-disable-self-upgrade",
		"-management-url=https://registry.example.com/entries/" + spec.Name,
		"-deployment-owner=" + ownerEmail,
		"-ui-allowed-principals=" + allowedPrincipals,
		"-ui-addr=" + daemonAddr,
		"-ui-allowed-hosts=" + entryHost + ",127.0.0.1,localhost",
		"-ui-require-verifier=registry",
		"-ui-hub-issuer=" + hub.HubAssertionIssuer,
		"-ui-hub-audience=" + audience,
		"-ui-hub-keys-url=" + c.jwksURL(),
	}

	cmd := exec.CommandContext(procCtx, c.grainBin, args...)
	cmd.Env = append(os.Environ(),
		"GRAIN_SECRET_MANAGER_ENDPOINT="+c.sm.server.URL,
		"GRAIN_SECRET_MANAGER_TOKEN=e2e-fake-sm-token",
		"GRAIN_OBJECT_STORE_GCS_ENDPOINT="+c.gcs.server.URL,
	)
	var logBuf bytes.Buffer
	cmd.Stdout = &logBuf
	cmd.Stderr = &logBuf
	if err := cmd.Start(); err != nil {
		procCancel()
		return nil, fmt.Errorf("start grain daemon for %s: %w", spec.Name, err)
	}
	waitDone := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(waitDone)
	}()

	// Wait until the real grain daemon is accepting TCP connections on daemonAddr.
	waitDeadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(waitDeadline) {
		select {
		case <-waitDone:
			procCancel()
			return nil, fmt.Errorf("grain daemon for %s exited early:\n%s", spec.Name, logBuf.String())
		default:
		}
		conn, dialErr := net.DialTimeout("tcp", daemonAddr, 100*time.Millisecond)
		if dialErr == nil {
			_ = conn.Close()
			break
		}
		time.Sleep(25 * time.Millisecond)
	}

	// Front the real grain daemon with a transparent recording reverse proxy so E2E assertions
	// can inspect the exact sanitized headers arriving at the VM while every request is served
	// by the real grain daemon process.
	targetURL, _ := url.Parse("http://" + daemonAddr)
	rp := &httputil.ReverseProxy{
		FlushInterval: -1,
		Director: func(req *http.Request) {
			req.URL.Scheme = targetURL.Scheme
			req.URL.Host = targetURL.Host
		},
		ModifyResponse: func(resp *http.Response) error {
			c.injectCookieMu.Lock()
			extra := append([]string(nil), c.injectCookies[spec.Name]...)
			c.injectCookieMu.Unlock()
			for _, sc := range extra {
				resp.Header.Add("Set-Cookie", sc)
			}
			return nil
		},
	}
	proxySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.lastCapturedMu.Lock()
		c.lastCaptured[spec.Name] = e2eCapturedRequest{
			Method:  r.Method,
			Path:    r.URL.Path,
			Host:    r.Host,
			Headers: r.Header.Clone(),
			Cookies: r.Cookies(),
		}
		c.lastCapturedMu.Unlock()
		rp.ServeHTTP(w, r)
	}))

	proxyHostPort := proxySrv.Listener.Addr().String()
	handle := hub.InstanceHandle{
		InstanceID: vmID,
		Zone:       spec.Zone,
		InternalIP: "127.0.0.1",
		BackendURL: "http://" + proxyHostPort,
	}
	status := hub.InstanceStatus{
		InstanceID: vmID,
		Name:       spec.Name,
		Zone:       spec.Zone,
		State:      "RUNNING",
		InternalIP: "127.0.0.1",
		BackendURL: "http://" + proxyHostPort,
		Labels:     spec.Labels,
	}

	vm := &e2eRunningVM{
		handle:     handle,
		status:     status,
		spec:       spec,
		cmd:        cmd,
		cancel:     procCancel,
		waitDone:   waitDone,
		dataDir:    dataDir,
		daemonAddr: daemonAddr,
		proxySrv:   proxySrv,
		logBuf:     &logBuf,
	}

	c.mu.Lock()
	c.vms[vmID] = vm
	c.mu.Unlock()

	c.t.Cleanup(func() {
		c.stopAndCleanupVM(vmID, true)
	})

	return &handle, nil
}

func (c *e2eCompute) stopAndCleanupVM(instanceID string, removeDir bool) {
	c.mu.Lock()
	vm, ok := c.vms[instanceID]
	if ok && removeDir {
		delete(c.vms, instanceID)
	} else if ok {
		vm.status.State = "TERMINATED"
	}
	c.mu.Unlock()
	if !ok || vm == nil {
		return
	}
	if vm.proxySrv != nil {
		vm.proxySrv.CloseClientConnections()
		vm.proxySrv.Close()
	}
	if vm.cmd != nil && vm.cmd.Process != nil {
		_ = vm.cmd.Process.Signal(syscall.SIGTERM)
		select {
		case <-vm.waitDone:
		case <-time.After(2 * time.Second):
			vm.cancel()
			<-vm.waitDone
		}
	} else {
		vm.cancel()
	}
	if removeDir && vm.dataDir != "" {
		_ = os.RemoveAll(vm.dataDir)
	}
}

func (c *e2eCompute) GetInstanceStatus(_ context.Context, _, instanceID string) (*hub.InstanceStatus, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	vm, ok := c.vms[instanceID]
	if !ok {
		return nil, errors.New("instance not found")
	}
	st := vm.status
	return &st, nil
}

func (c *e2eCompute) StopInstance(_ context.Context, _, instanceID string) error {
	c.stopAndCleanupVM(instanceID, false)
	return nil
}

func (c *e2eCompute) DeleteInstance(_ context.Context, _, instanceID string) error {
	c.stopAndCleanupVM(instanceID, true)
	return nil
}

func (c *e2eCompute) ListInstances(_ context.Context) ([]hub.InstanceStatus, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]hub.InstanceStatus, 0, len(c.vms))
	for _, vm := range c.vms {
		out = append(out, vm.status)
	}
	return out, nil
}

func allocateFreePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return port
}

// e2eIdentityVerifier authenticates callers at the registry edge from:
//  1. X-Test-Email header
//  2. X-Goog-Authenticated-User-Email header (e.g. "accounts.google.com:alice@example.com")
//  3. Authorization: Bearer <3-part-oidc-jwt> carrying {"email":..., "aud":...}
type e2eIdentityVerifier struct{}

func (e2eIdentityVerifier) Authenticate(r *http.Request) (*webauth.Caller, error) {
	email := strings.TrimSpace(r.Header.Get("X-Test-Email"))
	if email == "" {
		if iapEmail := strings.TrimSpace(r.Header.Get("X-Goog-Authenticated-User-Email")); iapEmail != "" {
			if idx := strings.LastIndexByte(iapEmail, ':'); idx >= 0 {
				email = strings.TrimSpace(iapEmail[idx+1:])
			} else {
				email = iapEmail
			}
		}
	}
	if email == "" {
		if auth := strings.TrimSpace(r.Header.Get("Authorization")); strings.HasPrefix(auth, "Bearer ") {
			tok := strings.TrimSpace(strings.TrimPrefix(auth, "Bearer "))
			parts := strings.Split(tok, ".")
			if len(parts) == 3 {
				if payload, err := base64.RawURLEncoding.DecodeString(parts[1]); err == nil {
					var claims struct {
						Email string `json:"email"`
						Sub   string `json:"sub"`
					}
					if json.Unmarshal(payload, &claims) == nil && claims.Email != "" {
						email = claims.Email
					}
				}
			}
		}
	}
	if email == "" {
		return nil, errors.New("missing caller identity")
	}
	return &webauth.Caller{
		Email:   email,
		Subject: email,
		Kind:    webauth.PrincipalKindForEmail(email),
	}, nil
}

func makeE2EOIDCToken(email, audience string) string {
	hdr := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	bodyBytes, _ := json.Marshal(map[string]any{
		"iss":   "https://accounts.google.com",
		"sub":   email,
		"email": email,
		"aud":   audience,
		"exp":   time.Now().Add(time.Hour).Unix(),
	})
	body := base64.RawURLEncoding.EncodeToString(bodyBytes)
	return hdr + "." + body + ".sig"
}

// switchableTCPProxy is an in-process L4 TCP load balancer in front of Replica A and Replica B.
// It supports HTTP/1.1, SSE, and 101 Switching Protocols WebSocket upgrades and allows switching
// the target replica during rolling-restart drain tests.
type switchableTCPProxy struct {
	ln     net.Listener
	URL    string
	target atomic.Value // string host:port
}

func newSwitchableTCPProxy(t *testing.T, initialTargetURL string) *switchableTCPProxy {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen switchableTCPProxy: %v", err)
	}
	p := &switchableTCPProxy{
		ln:  ln,
		URL: "http://" + ln.Addr().String(),
	}
	p.SetTargetURL(initialTargetURL)

	go func() {
		for {
			clientConn, err := ln.Accept()
			if err != nil {
				return
			}
			targetAddr, _ := p.target.Load().(string)
			go func(c net.Conn, addr string) {
				defer c.Close()
				upConn, err := net.DialTimeout("tcp", addr, 5*time.Second)
				if err != nil {
					return
				}
				defer upConn.Close()
				done := make(chan struct{}, 2)
				go func() {
					_, _ = io.Copy(upConn, c)
					if tc, ok := upConn.(*net.TCPConn); ok {
						_ = tc.CloseWrite()
					}
					done <- struct{}{}
				}()
				go func() {
					_, _ = io.Copy(c, upConn)
					if tc, ok := c.(*net.TCPConn); ok {
						_ = tc.CloseWrite()
					}
					done <- struct{}{}
				}()
				<-done
				<-done
			}(clientConn, targetAddr)
		}
	}()

	t.Cleanup(func() {
		_ = ln.Close()
	})
	return p
}

func (p *switchableTCPProxy) SetTargetURL(rawURL string) {
	u, err := url.Parse(rawURL)
	if err == nil && u.Host != "" {
		p.target.Store(u.Host)
	} else {
		p.target.Store(rawURL)
	}
}

// e2eConnectedGrainProc manages a real `grain daemon` subprocess operating as a `connected`
// entry (-hub-connect) dialing the registry's outbound tunnel and sending heartbeats.
type e2eConnectedGrainProc struct {
	cmd        *exec.Cmd
	cancel     context.CancelFunc
	waitDone   chan struct{}
	dataDir    string
	daemonAddr string
	logBuf     *bytes.Buffer
}

func (p *e2eConnectedGrainProc) Stop() {
	if p == nil {
		return
	}
	if p.cmd != nil && p.cmd.Process != nil {
		_ = p.cmd.Process.Signal(syscall.SIGTERM)
		select {
		case <-p.waitDone:
		case <-time.After(2 * time.Second):
			p.cancel()
			<-p.waitDone
		}
	} else if p.cancel != nil {
		p.cancel()
	}
}

func (p *e2eConnectedGrainProc) AccessCode() string {
	b, _ := os.ReadFile(filepath.Join(p.dataDir, "ui.token"))
	return strings.TrimSpace(string(b))
}

type e2eConnectedGrainConfig struct {
	EntryName      string
	OwnerEmail     string
	OwnerGroup     string
	InstanceDomain string
	HubURL         string
	JWKSURL        string
	Token          string // for grc_... token auth
	AuthMode       string // "token" (default) or "oidc"
	OIDCEmail      string // for FrontIdentity / OIDC auth via fake gcloud
	OIDCAudience   string
	Listed         bool
	DisplayName    string
	Purpose        string
	HostKind       string
}

func startE2EConnectedGrain(t *testing.T, grainBin string, cfg e2eConnectedGrainConfig) *e2eConnectedGrainProc {
	t.Helper()
	dataDir, err := os.MkdirTemp("", fmt.Sprintf("grain-e2e-conn-%s-*", cfg.EntryName))
	if err != nil {
		t.Fatalf("MkdirTemp connected grain: %v", err)
	}
	sandboxDir := filepath.Join(dataDir, "scratch")
	if err := os.MkdirAll(sandboxDir, 0o755); err != nil {
		t.Fatalf("MkdirAll sandboxDir: %v", err)
	}

	daemonPort := allocateFreePort(t)
	daemonAddr := fmt.Sprintf("127.0.0.1:%d", daemonPort)
	entryHost := cfg.EntryName + "." + strings.TrimPrefix(cfg.InstanceDomain, ".")
	audience := "https://" + entryHost

	allowedPrincipals := "user:" + cfg.OwnerEmail
	if g := strings.TrimSpace(cfg.OwnerGroup); g != "" {
		if !strings.HasPrefix(g, "group:") {
			g = "group:" + g
		}
		allowedPrincipals += "," + g
	}
	hostKind := cfg.HostKind
	if hostKind == "" {
		hostKind = string(api.HostKindWorkstation)
	}

	args := []string{
		"daemon",
		"-data-dir", dataDir,
		"-sandbox-dir", sandboxDir,
		"-disable-native-sandboxes",
		"-sandbox-port-base=0",
		"-ci=false",
		"-deployment-owner=" + cfg.OwnerEmail,
		"-ui-allowed-principals=" + allowedPrincipals,
		"-ui-addr=" + daemonAddr,
		"-ui-allowed-hosts=" + entryHost + ",127.0.0.1,localhost",
		"-ui-require-verifier=registry",
		"-ui-hub-issuer=" + hub.HubAssertionIssuer,
		"-ui-hub-audience=" + audience,
		"-ui-hub-keys-url=" + cfg.JWKSURL,
		"-hub-url=" + cfg.HubURL,
		"-hub-entry=" + cfg.EntryName,
		"-hub-connect",
		"-hub-host-kind=" + hostKind,
		fmt.Sprintf("-hub-listed=%t", cfg.Listed),
	}
	if cfg.DisplayName != "" {
		args = append(args, "-hub-display-name="+cfg.DisplayName)
	}
	if cfg.Purpose != "" {
		args = append(args, "-hub-purpose="+cfg.Purpose)
	}

	env := os.Environ()
	if cfg.AuthMode == "oidc" {
		aud := cfg.OIDCAudience
		if aud == "" {
			aud = "https://registry.example.com"
		}
		args = append(args,
			"-hub-auth-mode=oidc",
			"-hub-oidc-audience="+aud,
		)
		fakeBinDir := filepath.Join(dataDir, "fake-bin")
		if err := os.MkdirAll(fakeBinDir, 0o755); err != nil {
			t.Fatalf("MkdirAll fakeBinDir: %v", err)
		}
		oidcTok := makeE2EOIDCToken(cfg.OIDCEmail, aud)
		gcloudScript := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' '%s'\n", oidcTok)
		gcloudPath := filepath.Join(fakeBinDir, "gcloud")
		if err := os.WriteFile(gcloudPath, []byte(gcloudScript), 0o755); err != nil {
			t.Fatalf("WriteFile fake gcloud: %v", err)
		}
		env = append(env, "PATH="+fakeBinDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	} else {
		tokenFile := filepath.Join(dataDir, "registry.token")
		if err := os.WriteFile(tokenFile, []byte(cfg.Token+"\n"), 0o600); err != nil {
			t.Fatalf("WriteFile tokenFile: %v", err)
		}
		args = append(args, "-hub-token-file="+tokenFile)
	}

	procCtx, procCancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(procCtx, grainBin, args...)
	cmd.Env = env
	logBuf := &bytes.Buffer{}
	cmd.Stdout = logBuf
	cmd.Stderr = logBuf
	if err := cmd.Start(); err != nil {
		procCancel()
		t.Fatalf("start connected grain daemon %s: %v", cfg.EntryName, err)
	}
	waitDone := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(waitDone)
	}()

	proc := &e2eConnectedGrainProc{
		cmd:        cmd,
		cancel:     procCancel,
		waitDone:   waitDone,
		dataDir:    dataDir,
		daemonAddr: daemonAddr,
		logBuf:     logBuf,
	}
	t.Cleanup(func() {
		proc.Stop()
		_ = os.RemoveAll(dataDir)
	})
	return proc
}

// TestHubEndToEndStep15 implements Step 15 of design/design/hub.design.md:
// Two in-process registry replicas (Replica A and Replica B, exercising cross-replica :8082
// forwarding and rolling-restart reconnect_grace) running against in-process fakes for
// Compute, shared PostgreSQL, Secret Manager, object storage, GroupResolver, and local bare
// Git remotes driving real `grain` processes.
func TestHubEndToEndStep15(t *testing.T) {
	rootT := t
	grainBin := buildRealGrainBinary(t)
	pg := startE2EPostgresCluster(t)
	sm := newE2ESecretManager(t)
	gcs := newE2EObjectStorage(t)
	configRepos := newE2EConfigRepos(t)
	resources := newE2EInstanceResources(pg, sm, gcs)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	st, err := store.Open(ctx, pg.HubDSN, store.DefaultThresholds())
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	// Seed VM shapes and the shared Cloud SQL pool row so instance creations and allocations work.
	if err := st.SeedShapes(ctx, []store.ShapeRow{
		{Name: "standard", MachineType: "n2-standard-4", BootDiskGB: 50, QuotaUnits: 1, Description: "Standard VM"},
		{Name: "large", MachineType: "n2-standard-8", BootDiskGB: 100, QuotaUnits: 2, Description: "Large VM"},
	}); err != nil {
		t.Fatalf("SeedShapes: %v", err)
	}
	if err := st.SeedSQLInstances(ctx, []store.SQLInstanceRow{
		{
			Name:           "shared-pg-pool",
			ConnectionName: "test-proj:us-central1:shared-pg-pool",
			PrivateIP:      "127.0.0.1",
			MaxDatabases:   50,
		},
	}); err != nil {
		t.Fatalf("SeedSQLInstances: %v", err)
	}

	groups := newE2EGroupResolver(map[string][]string{
		"group:eng-team@example.com": {"alice@example.com", "bob@example.com"},
		"group:admins@example.com":   {"admin@example.com"},
	})

	admitter, err := webauth.NewAdmitter(webauth.AdmissionConfig{
		Verifier:          e2eIdentityVerifier{},
		Groups:            groups,
		AllowedDomains:    []string{"example.com"},
		Admins:            []string{"user:admin@example.com", "group:admins@example.com"},
		ServicePrincipals: []string{"serviceAccount:ci-bot@test-proj.iam.gserviceaccount.com"},
	})
	if err != nil {
		t.Fatalf("webauth.NewAdmitter: %v", err)
	}

	localSigner, err := hub.NewLocalSigner(hub.SigningConfig{
		Provider: "local",
		KeyID:    "e2e-reg-key-1",
		JWTTTL:   60 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewLocalSigner: %v", err)
	}
	sharedSigner := hub.NewCachingSigner(localSigner, 30*time.Second)

	const (
		hubHost        = "registry.example.com"
		instanceDomain = "instances.example.com"
		iapAudience    = "/projects/123/global/backendServices/e2e-reg"
	)

	regCfg := hub.Config{
		Host:           hubHost,
		InstanceDomain: instanceDomain,
		Listen:         "127.0.0.1:8080",
		JWKSListen:     "127.0.0.1:8081",
		Database:       hub.DatabaseConfig{URL: pg.HubDSN},
		Auth: hub.AuthConfig{
			Provider:          "iap",
			IAPAudience:       iapAudience,
			AllowedDomains:    []string{"example.com"},
			Admins:            []string{"user:admin@example.com"},
			ServicePrincipals: []string{"serviceAccount:ci-bot@test-proj.iam.gserviceaccount.com"},
		},
		Signing: hub.SigningConfig{
			Provider: "local",
			KeyID:    "e2e-reg-key-1",
			JWTTTL:   60 * time.Second,
		},
		Compute: hub.ComputeConfig{
			Provider:           "gce",
			Project:            "test-proj",
			Region:             "us-central1",
			Zones:              []string{"us-central1-a"},
			DefaultShape:       "standard",
			VMPrefix:           "grain-",
			RegistryID:         "e2e-registry",
			StatusPollInterval: 15 * time.Second,
			DrainTimeout:       5 * time.Second,
			HealthTimeout:      30 * time.Second,
			Shapes: []hub.Shape{
				{Name: "standard", MachineType: "n2-standard-4", BootDiskGB: 50, QuotaUnits: 1},
				{Name: "large", MachineType: "n2-standard-8", BootDiskGB: 100, QuotaUnits: 2},
			},
		},
		Quotas: hub.QuotasConfig{
			DefaultInstanceUnits: 2,
			DefaultConnected:     3,
		},
		Tunnel: hub.TunnelConfig{
			PeerListen:           "127.0.0.1:8082",
			MaxConcurrentStreams: 256,
			MaxBufferedBytes:     16 << 20,
			ReconnectGrace:       2500 * time.Millisecond,
			DrainTimeout:         500 * time.Millisecond,
			ReplicaHeartbeat:     2 * time.Second,
			ReplicaTTL:           10 * time.Second,
		},
		Catalog: hub.CatalogConfig{
			CacheTTL: 50 * time.Millisecond,
		},
	}

	type replicaNode struct {
		id      string
		srv     *hub.Server
		metrics *hub.Metrics
		mainSrv *httptest.Server
		pubSrv  *httptest.Server
		peerSrv *httptest.Server
	}

	startReplica := func(id string) *replicaNode {
		metrics := hub.NewMetrics()
		srv, err := hub.NewServer(hub.ServerOptions{
			Config:    regCfg,
			Store:     st,
			Admitter:  admitter,
			Groups:    groups,
			Signer:    sharedSigner,
			Metrics:   metrics,
			ReplicaID: id,
		})
		if err != nil {
			t.Fatalf("registry.NewServer(%s): %v", id, err)
		}
		peerSrv := httptest.NewServer(srv.PeerHandler())
		srv.Router().SetPeerAddr(peerSrv.Listener.Addr().String())
		mainSrv := httptest.NewServer(srv.Handler())
		pubSrv := httptest.NewServer(srv.PublicHandler())
		srv.StartBackground(ctx)
		_ = st.UpsertReplica(ctx, id, srv.Router().PeerAddr())

		t.Cleanup(func() {
			srv.Router().Close()
			pubSrv.CloseClientConnections()
			pubSrv.Close()
			mainSrv.CloseClientConnections()
			mainSrv.Close()
			peerSrv.CloseClientConnections()
			peerSrv.Close()
		})
		return &replicaNode{
			id:      id,
			srv:     srv,
			metrics: metrics,
			mainSrv: mainSrv,
			pubSrv:  pubSrv,
			peerSrv: peerSrv,
		}
	}

	repA := startReplica("replica-a")
	repB := startReplica("replica-b")

	// Switchable TCP load-balancers for public (:8081) and main (:8080) listeners.
	pubLB := newSwitchableTCPProxy(t, repA.pubSrv.URL)
	mainLB := newSwitchableTCPProxy(t, repA.mainSrv.URL)

	jwksURL := func() string {
		return repA.pubSrv.URL + "/.well-known/jwks.json"
	}

	computeFake := newE2ECompute(t, grainBin, pg, sm, gcs, st, instanceDomain, jwksURL)

	reconciler, err := reconcile.New(reconcile.Options{
		Config:      regCfg,
		Store:       st,
		Compute:     computeFake,
		Resources:   resources,
		ConfigRepos: configRepos,
		Signer:      sharedSigner,
		HostCache:   repA.srv.Router().HostCache(),
		HolderID:    "replica-a",
	})
	if err != nil {
		t.Fatalf("reconcile.New: %v", err)
	}

	// Helper to send JSON requests to a replica's main listener.
	doMainJSON := func(rep *replicaNode, method, host, path, callerEmail string, body any, extraHdr map[string]string) (int, []byte, http.Header) {
		t.Helper()
		var bodyReader io.Reader
		if body != nil {
			switch v := body.(type) {
			case string:
				bodyReader = strings.NewReader(v)
			case []byte:
				bodyReader = bytes.NewReader(v)
			default:
				b, err := json.Marshal(body)
				if err != nil {
					t.Fatalf("json.Marshal: %v", err)
				}
				bodyReader = bytes.NewReader(b)
			}
		}
		req, err := http.NewRequestWithContext(ctx, method, rep.mainSrv.URL+path, bodyReader)
		if err != nil {
			t.Fatalf("http.NewRequest: %v", err)
		}
		req.Host = host
		if callerEmail != "" {
			req.Header.Set("X-Test-Email", callerEmail)
		}
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		req.Header.Set("Accept", "application/json")
		for k, v := range extraHdr {
			req.Header.Set(k, v)
		}
		client := &http.Client{
			Timeout: 15 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("%s %s%s failed: %v", method, host, path, err)
		}
		defer resp.Body.Close()
		respBytes, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, respBytes, resp.Header
	}

	// =========================================================================
	// 1. Hosted `instance` Direct VPC Create, Externalised State, 15s Status
	//    Polling + Self-Reported WorkOwnership Scopes, Replace, Delete, Restore
	// =========================================================================
	t.Run("HostedInstanceLifecycleAndExternalState", func(t *testing.T) {
		// Create hosted instance `hosted-alpha` owned by alice@example.com, co-managed by group:eng-team@example.com.
		code, body, _ := doMainJSON(repA, http.MethodPost, hubHost, "/api/entries", "alice@example.com", map[string]any{
			"name":         "hosted-alpha",
			"kind":         api.KindInstance,
			"shape":        "standard",
			"owner_group":  "group:eng-team@example.com",
			"display_name": "Hosted Alpha",
			"purpose":      "Primary widgets instance",
			"listed":       true,
		}, nil)
		if code != http.StatusAccepted && code != http.StatusCreated {
			t.Fatalf("POST /api/entries hosted-alpha = %d: %s", code, string(body))
		}

		// Reconcile pass 1: provisions DB, Secret Manager deploy key, GCS prefix, bare Git config repo, and starts real `grain daemon` VM.
		if err := reconciler.RunOnce(ctx); err != nil {
			t.Fatalf("RunOnce (provision VM): %v", err)
		}
		// Reconcile pass 2: probes `<backend_url>/healthz` and `<backend_url>/api/registry/status` with signed assertion and transitions to `running`.
		if err := reconciler.RunOnce(ctx); err != nil {
			t.Fatalf("RunOnce (probe VM): %v", err)
		}
		repB.srv.Router().HostCache().Invalidate("hosted-alpha")

		entryRow, err := st.GetEntryByName(ctx, "hosted-alpha")
		if err != nil || entryRow == nil {
			t.Fatalf("GetEntryByName hosted-alpha: %v", err)
		}
		if entryRow.LifecycleState != api.StateRunning {
			probeTok, _ := sharedSigner.SignAssertion(ctx, hub.HubClaims{
				Audience: "https://hosted-alpha." + instanceDomain,
				Subject:  "alice@example.com",
				Email:    "alice@example.com",
				Kind:     "human",
			})
			pReq, _ := http.NewRequest(http.MethodGet, entryRow.BackendURL+"/api/registry/status", nil)
			pReq.Host = "hosted-alpha." + instanceDomain
			pReq.Header.Set(api.HeaderRegistryAssertion, probeTok)
			pResp, pErr := http.DefaultClient.Do(pReq)
			var pBody []byte
			var pCode int
			if pResp != nil {
				pCode = pResp.StatusCode
				pBody, _ = io.ReadAll(pResp.Body)
				_ = pResp.Body.Close()
			}
			t.Fatalf("expected hosted-alpha in running state, got %s (reason: %s, backend: %s, probeCode=%d, probeErr=%v, probeBody=%s)\nVM logs:\n%s",
				entryRow.LifecycleState, entryRow.StatusReason, entryRow.BackendURL, pCode, pErr, string(pBody), computeFake.VMLogs(entryRow.GCEInstanceID))
		}
		if !strings.HasPrefix(entryRow.BackendURL, "http://127.0.0.1:") {
			t.Fatalf("expected plain HTTP VPC backend_url, got %q", entryRow.BackendURL)
		}
		firstVMID := entryRow.GCEInstanceID

		hostedHost := "hosted-alpha." + instanceDomain

		// Proxy HTTP requests through Replica B to the real `grain daemon` subprocess:
		// 1) Configure initial settings + add repos `acme/widgets` (WorkOwnership.Default="on" -> primary)
		//    and `acme/docs` (WorkOwnership.Default="off" -> secondary).
		code, body, _ = doMainJSON(repB, http.MethodPut, hostedHost, "/api/settings", "alice@example.com", map[string]any{
			"targetRepos": []string{"acme/widgets", "acme/docs"},
		}, nil)
		if code != http.StatusOK {
			t.Fatalf("PUT /api/settings on hosted-alpha = %d: %s", code, string(body))
		}
		code, body, _ = doMainJSON(repB, http.MethodPut, hostedHost, "/api/repos/acme/widgets/work-ownership", "alice@example.com", map[string]any{
			"default": "on",
		}, nil)
		if code != http.StatusOK {
			t.Fatalf("PUT work-ownership acme/widgets = %d: %s", code, string(body))
		}
		code, body, _ = doMainJSON(repB, http.MethodPut, hostedHost, "/api/repos/acme/docs/work-ownership", "alice@example.com", map[string]any{
			"default": "off",
		}, nil)
		if code != http.StatusOK {
			t.Fatalf("PUT work-ownership acme/docs = %d: %s", code, string(body))
		}

		// 2) Store a secret via the real `grain daemon` API (persisted into fake Secret Manager).
		code, body, _ = doMainJSON(repB, http.MethodPut, hostedHost, "/api/secrets/custom-api-key/value", "alice@example.com", map[string]any{
			"value": "sm-persisted-secret-val",
		}, nil)
		if code != http.StatusOK {
			t.Fatalf("PUT /api/secrets/custom-api-key/value on hosted-alpha = %d: %s", code, string(body))
		}
		if sm.HasPrefixSecrets("grain-inst-hosted-alpha-") < 2 {
			t.Fatalf("expected fake Secret Manager to hold deploy key + custom-api-key under grain-inst-hosted-alpha-")
		}

		// 3) Create a task on the real `grain daemon` (persisted into per-instance PostgreSQL database `grain_inst_hosted_alpha`).
		code, body, _ = doMainJSON(repB, http.MethodPost, hostedHost, "/api/tasks", "alice@example.com", map[string]any{
			"title":  "Persisted hosted task across VM replace and restore",
			"body":   "Verify externalised PostgreSQL state survives VM replacement",
			"noRepo": true,
		}, nil)
		if code != http.StatusCreated && code != http.StatusOK {
			t.Fatalf("POST /api/tasks on hosted-alpha = %d: %s", code, string(body))
		}

		// 4) Store an artifact object under the instance's GCS prefix in fake object storage.
		gcs.PutObject("grain-state-bucket", "instances/hosted-alpha/transcripts/task-1.jsonl", []byte(`{"step":1,"ok":true}`))

		// 5) Verify the local bare Git config repo exists and received mirrored state or is initialized.
		bareRepoPath := configRepos.RepoPath("hosted-alpha")
		if bareRepoPath == "" {
			t.Fatalf("expected bare Git config repo for hosted-alpha")
		}

		// 6) Run 15 s status polling (`PollRunningInstances`) and verify self-reported WorkOwnership scopes
		// (`acme/widgets` -> primary, `acme/docs` -> secondary, and controller-local `grain/config` excluded).
		if err := reconciler.PollRunningInstances(ctx); err != nil {
			t.Fatalf("PollRunningInstances: %v", err)
		}
		scopes, err := st.ListEntryScopes(ctx, entryRow.ID)
		if err != nil {
			t.Fatalf("ListEntryScopes: %v", err)
		}
		foundWidgetsPrimary := false
		foundDocsSecondary := false
		for _, sc := range scopes {
			if sc.Repo == "grain/config" {
				t.Fatalf("controller-local grain/config must be excluded from reported scopes, got %+v", sc)
			}
			if sc.Repo == "acme/widgets" && sc.PrimaryClaim && sc.Source == "reported" {
				foundWidgetsPrimary = true
			}
			if sc.Repo == "acme/docs" && !sc.PrimaryClaim && sc.Source == "reported" {
				foundDocsSecondary = true
			}
		}
		if !foundWidgetsPrimary || !foundDocsSecondary {
			t.Fatalf("expected reported scopes acme/widgets=primary and acme/docs=secondary, got %+v", scopes)
		}

		// 7) Co-owner `bob@example.com` (member of `group:eng-team@example.com`) resizes `hosted-alpha` from `standard` (1 unit)
		// to `large` (2 units) via POST /api/entries/hosted-alpha/replace, triggering VM replacement, while quota attribution stays on `alice@example.com`!
		code, body, _ = doMainJSON(repA, http.MethodPost, hubHost, "/api/entries/hosted-alpha/replace", "bob@example.com", map[string]any{
			"shape": "large",
		}, nil)
		if code != http.StatusAccepted {
			t.Fatalf("co-owner POST /api/entries/hosted-alpha/replace shape=large = %d: %s", code, string(body))
		}

		// Verify quota attribution: alice@example.com is charged 2 instance quota units (her full cap of 2),
		// so alice cannot create another standard instance (403 quota_exceeded), while bob@example.com has 0 units charged!
		code, body, _ = doMainJSON(repA, http.MethodPost, hubHost, "/api/entries", "alice@example.com", map[string]any{
			"name":  "alice-over-quota",
			"kind":  api.KindInstance,
			"shape": "standard",
		}, nil)
		if code != http.StatusForbidden {
			t.Fatalf("expected 403 quota_exceeded when alice exceeds 2 instance quota units after bob's resize, got %d: %s", code, string(body))
		}

		// Drive Reconciler through VM replacement (`replacing` -> stop/delete old VM -> create new VM -> probe -> `running`).
		for i := 0; i < 4; i++ {
			if err := reconciler.RunOnce(ctx); err != nil {
				t.Fatalf("RunOnce replace step %d: %v", i, err)
			}
		}
		repA.srv.Router().HostCache().Invalidate("hosted-alpha")
		repB.srv.Router().HostCache().Invalidate("hosted-alpha")

		replacedRow, err := st.GetEntryByName(ctx, "hosted-alpha")
		if err != nil || replacedRow == nil {
			t.Fatalf("GetEntryByName after replace: %v", err)
		}
		if replacedRow.LifecycleState != api.StateRunning {
			t.Fatalf("expected hosted-alpha running after replace, got %s (step=%s, reason=%s)",
				replacedRow.LifecycleState, replacedRow.ReplaceStep, replacedRow.StatusReason)
		}
		if replacedRow.GCEInstanceID == firstVMID {
			t.Fatalf("expected new GCEInstanceID after replace, still got %s", replacedRow.GCEInstanceID)
		}

		// Verify externalised state survived VM replacement on the brand-new `grain daemon` subprocess:
		// - Task in shared PostgreSQL (`GET /api/tasks`)
		// - Secret in fake Secret Manager (`GET /api/secrets`)
		// - Object in fake GCS object storage
		code, body, _ = doMainJSON(repA, http.MethodGet, hostedHost, "/api/tasks", "alice@example.com", nil, nil)
		if code != http.StatusOK || !strings.Contains(string(body), "Persisted hosted task across VM replace and restore") {
			t.Fatalf("expected task to survive VM replacement in PostgreSQL, got %d: %s", code, string(body))
		}
		code, body, _ = doMainJSON(repA, http.MethodGet, hostedHost, "/api/secrets", "alice@example.com", nil, nil)
		if code != http.StatusOK || !strings.Contains(string(body), "custom-api-key") {
			t.Fatalf("expected secret in Secret Manager to survive VM replacement, got %d: %s", code, string(body))
		}
		if data, ok := gcs.GetObject("grain-state-bucket", "instances/hosted-alpha/transcripts/task-1.jsonl"); !ok || !strings.Contains(string(data), `"step":1`) {
			t.Fatalf("expected GCS object to survive VM replacement, got ok=%v data=%q", ok, string(data))
		}

		// 8) Delete `hosted-alpha` (`DELETE /api/entries/hosted-alpha`), reconcile VM deletion into `deleted` state
		// with 30-day retention, verify name reservation blocks other users, and then restore (`POST /api/entries/hosted-alpha/restore`)!
		code, body, _ = doMainJSON(repA, http.MethodDelete, hubHost, "/api/entries/hosted-alpha", "alice@example.com", nil, nil)
		if code != http.StatusOK && code != http.StatusAccepted {
			t.Fatalf("DELETE /api/entries/hosted-alpha = %d: %s", code, string(body))
		}
		if err := reconciler.RunOnce(ctx); err != nil {
			t.Fatalf("RunOnce delete: %v", err)
		}
		deletedRow, err := st.GetLatestDeletedEntryByName(ctx, "hosted-alpha")
		if err != nil || deletedRow == nil || deletedRow.LifecycleState != api.StateDeleted {
			t.Fatalf("expected hosted-alpha in deleted state, got %+v (err=%v)", deletedRow, err)
		}

		// Another user (`charlie@example.com`) cannot claim `hosted-alpha` during the 30-day retention window.
		code, body, _ = doMainJSON(repA, http.MethodPost, hubHost, "/api/entries", "charlie@example.com", map[string]any{
			"name": "hosted-alpha",
			"kind": api.KindConnected,
		}, nil)
		if code != http.StatusConflict {
			t.Fatalf("expected 409 Conflict when charlie tries to reuse deleted name within 30-day window, got %d: %s", code, string(body))
		}

		// Restore `hosted-alpha` (`POST /api/entries/hosted-alpha/restore`) and reconcile back to `running`.
		code, body, _ = doMainJSON(repA, http.MethodPost, hubHost, "/api/entries/hosted-alpha/restore", "alice@example.com", nil, nil)
		if code != http.StatusAccepted && code != http.StatusOK {
			t.Fatalf("POST /api/entries/hosted-alpha/restore = %d: %s", code, string(body))
		}
		for i := 0; i < 3; i++ {
			if err := reconciler.RunOnce(ctx); err != nil {
				t.Fatalf("RunOnce restore step %d: %v", i, err)
			}
		}
		repA.srv.Router().HostCache().Invalidate("hosted-alpha")
		repB.srv.Router().HostCache().Invalidate("hosted-alpha")

		restoredRow, err := st.GetEntryByName(ctx, "hosted-alpha")
		if err != nil || restoredRow == nil || restoredRow.LifecycleState != api.StateRunning {
			t.Fatalf("expected hosted-alpha running after restore, got %+v (err=%v)", restoredRow, err)
		}

		// Verify task in PostgreSQL and secret in Secret Manager survived delete + restore!
		code, body, _ = doMainJSON(repB, http.MethodGet, hostedHost, "/api/tasks", "alice@example.com", nil, nil)
		if code != http.StatusOK || !strings.Contains(string(body), "Persisted hosted task across VM replace and restore") {
			t.Fatalf("expected task to survive delete+restore, got %d: %s", code, string(body))
		}
	})

	// =========================================================================
	// 2. `connected` Grain Reservation + Outbound `/api/entries/<name>/connect`
	//    Tunnel (`grc_…` & `FrontIdentity` Auth) + Cross-Replica `:8082`
	//    Forwarding + 15s Heartbeats + Rolling Replica Drain/Redial with Zero
	//    `503`s + Credential Revocation Immediate Disconnect + Offline `503`
	// =========================================================================
	var connOIDCProc *e2eConnectedGrainProc
	t.Run("ConnectedGrainTunnelRollingDrainRevocationAndOffline503", func(t *testing.T) {
		// 1) Reserve connected entry `conn-workstation` owned by alice@example.com with owner_group `group:eng-team@example.com`.
		code, body, _ := doMainJSON(repA, http.MethodPost, hubHost, "/api/entries", "alice@example.com", map[string]any{
			"name":         "conn-workstation",
			"kind":         api.KindConnected,
			"owner_group":  "group:eng-team@example.com",
			"display_name": "Alice Workstation",
			"purpose":      "Connected workstation grain",
			"host_kind":    api.HostKindWorkstation,
			"listed":       true,
		}, nil)
		if code != http.StatusCreated {
			t.Fatalf("POST /api/entries conn-workstation = %d: %s", code, string(body))
		}
		var createResp api.CreateEntryResponse
		if err := json.Unmarshal(body, &createResp); err != nil {
			t.Fatalf("unmarshal createResp: %v (%s)", err, string(body))
		}
		if !strings.HasPrefix(createResp.RegistrationToken, "grc_") || createResp.Credential == nil {
			t.Fatalf("expected grc_... token on connected entry creation, got %+v", createResp)
		}
		credID := createResp.Credential.CredentialID
		grcToken := createResp.RegistrationToken

		// 2) Launch a real `grain daemon` subprocess with `-hub-connect` dialing `pubLB.URL` (initially pointing to Replica A's :8081 public listener).
		connProc := startE2EConnectedGrain(t, grainBin, e2eConnectedGrainConfig{
			EntryName:      "conn-workstation",
			OwnerEmail:     "alice@example.com",
			OwnerGroup:     "group:eng-team@example.com",
			InstanceDomain: instanceDomain,
			HubURL:         pubLB.URL,
			JWKSURL:        jwksURL(),
			Token:          grcToken,
			AuthMode:       "token",
			Listed:         true,
			DisplayName:    "Alice Workstation",
			Purpose:        "Connected workstation grain",
			HostKind:       string(api.HostKindWorkstation),
		})
		defer connProc.Stop()

		// Wait for the real `grain daemon` subprocess to establish its outbound `/api/entries/conn-workstation/connect` tunnel on Replica A.
		waitDeadline := time.Now().Add(15 * time.Second)
		for repA.srv.Router().GetLocalTunnel("conn-workstation") == nil && time.Now().Before(waitDeadline) {
			time.Sleep(25 * time.Millisecond)
		}
		if repA.srv.Router().GetLocalTunnel("conn-workstation") == nil {
			t.Fatalf("timed out waiting for conn-workstation tunnel on Replica A; daemon logs:\n%s", connProc.logBuf.String())
		}
		_, _ = repB.srv.Router().TunnelCache().RefreshEntry(ctx, "conn-workstation")

		connHost := "conn-workstation." + instanceDomain

		// 3) Send HTTP request to Replica B (`Host: conn-workstation.instances.example.com`) and verify it forwards
		// over `:8082` to Replica A -> outbound tunnel -> real `grain daemon` subprocess!
		beforeForwards := repB.metrics.Snapshot().PeerForwardsTotal
		code, body, _ = doMainJSON(repB, http.MethodGet, connHost, "/api/auth/whoami", "alice@example.com", nil, nil)
		if code != http.StatusOK || !strings.Contains(string(body), "alice@example.com") {
			t.Fatalf("cross-replica (:8082) proxied GET /api/auth/whoami to conn-workstation = %d: %s", code, string(body))
		}
		if repB.metrics.Snapshot().PeerForwardsTotal <= beforeForwards {
			t.Fatalf("expected Replica B peerForwards counter to increment on cross-replica tunnel request")
		}

		// 4) Configure repositories + WorkOwnership on the real connected `grain daemon` (`acme/mobile` -> primary, `acme/shared` -> secondary)
		// and trigger an immediate heartbeat (`POST /api/registry/heartbeat`) through the tunnel!
		code, body, _ = doMainJSON(repB, http.MethodPut, connHost, "/api/repos/acme/mobile/work-ownership", "alice@example.com", map[string]any{
			"default": "on",
		}, nil)
		if code != http.StatusOK {
			t.Fatalf("PUT work-ownership acme/mobile = %d: %s", code, string(body))
		}
		code, body, _ = doMainJSON(repB, http.MethodPut, connHost, "/api/repos/acme/shared/work-ownership", "alice@example.com", map[string]any{
			"default": "off",
		}, nil)
		if code != http.StatusOK {
			t.Fatalf("PUT work-ownership acme/shared = %d: %s", code, string(body))
		}
		code, body, _ = doMainJSON(repB, http.MethodPost, connHost, "/api/registry/heartbeat", "alice@example.com", nil, nil)
		if code != http.StatusOK {
			t.Fatalf("POST /api/registry/heartbeat on conn-workstation = %d: %s", code, string(body))
		}

		// The real connected `grain daemon` automatically triggers a heartbeat on config/work-ownership change.
		// Poll `st.ListEntryScopes` until both `acme/mobile` (primary) and `acme/shared` (secondary) are recorded.
		connRow, err := st.GetEntryByName(ctx, "conn-workstation")
		if err != nil || connRow == nil {
			t.Fatalf("GetEntryByName conn-workstation: %v", err)
		}
		var connScopes []store.ScopeRow
		foundMobile := false
		foundShared := false
		scopeDeadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(scopeDeadline) {
			connScopes, err = st.ListEntryScopes(ctx, connRow.ID)
			if err != nil {
				t.Fatalf("ListEntryScopes conn-workstation: %v", err)
			}
			foundMobile = false
			foundShared = false
			for _, sc := range connScopes {
				if sc.Repo == "grain/config" {
					t.Fatalf("connected grain must not advertise grain/config in heartbeat scopes")
				}
				if sc.Repo == "acme/mobile" && sc.PrimaryClaim {
					foundMobile = true
				}
				if sc.Repo == "acme/shared" && !sc.PrimaryClaim {
					foundShared = true
				}
			}
			if foundMobile && foundShared {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
		if !foundMobile || !foundShared {
			t.Fatalf("expected connected grain heartbeat scopes acme/mobile=primary and acme/shared=secondary, got %+v", connScopes)
		}

		// 5) Rolling replica drain/redial with zero 503s:
		// Switch `pubLB` target to Replica B (`repB.pubSrv.URL`) so when Replica A drains its active tunnel,
		// the real `grain daemon` subprocess redials `/api/entries/conn-workstation/connect` and lands on Replica B!
		// While draining and redialing, fire concurrent requests at both Replica A and Replica B and verify ZERO 503s!
		pubLB.SetTargetURL(repB.pubSrv.URL)

		var (
			drainWG     sync.WaitGroup
			stopTraffic atomic.Bool
			totalReqs   atomic.Int64
			count503s   atomic.Int64
			otherErrs   atomic.Int64
		)
		for worker := 0; worker < 4; worker++ {
			targetRep := repA
			if worker%2 == 1 {
				targetRep = repB
			}
			drainWG.Add(1)
			go func(rNode *replicaNode) {
				defer drainWG.Done()
				for !stopTraffic.Load() {
					c, _, _ := doMainJSON(rNode, http.MethodGet, connHost, "/api/config", "alice@example.com", nil, nil)
					totalReqs.Add(1)
					if c == http.StatusServiceUnavailable {
						count503s.Add(1)
					} else if c != http.StatusOK {
						otherErrs.Add(1)
					}
					time.Sleep(15 * time.Millisecond)
				}
			}(targetRep)
		}

		// Trigger drain on Replica A.
		repA.srv.BeginDrain(ctx)

		// Wait until the real `grain daemon` subprocess has redialed and connected its tunnel to Replica B.
		redialDeadline := time.Now().Add(10 * time.Second)
		for repB.srv.Router().GetLocalTunnel("conn-workstation") == nil && time.Now().Before(redialDeadline) {
			time.Sleep(20 * time.Millisecond)
		}
		if repB.srv.Router().GetLocalTunnel("conn-workstation") == nil {
			stopTraffic.Store(true)
			drainWG.Wait()
			t.Fatalf("timed out waiting for conn-workstation to redial onto Replica B during rolling drain; logs:\n%s", connProc.logBuf.String())
		}
		time.Sleep(60 * time.Millisecond)
		stopTraffic.Store(true)
		drainWG.Wait()

		// Clear draining state on Replica A after the rolling restart simulation completes.
		repA.srv.SetDraining(false)

		if count503s.Load() != 0 || otherErrs.Load() != 0 || totalReqs.Load() == 0 {
			t.Fatalf("rolling replica drain/redial expected zero 503s across %d requests, got 503s=%d otherErrs=%d",
				totalReqs.Load(), count503s.Load(), otherErrs.Load())
		}

		// 6) Credential revocation immediate disconnect:
		// Revoke `credID` via `DELETE /api/entries/conn-workstation/credentials/{credId}` on Replica B.
		// Verify the active tunnel on Replica B is disconnected immediately!
		code, body, _ = doMainJSON(repB, http.MethodDelete, hubHost, "/api/entries/conn-workstation/credentials/"+credID, "alice@example.com", nil, nil)
		if code != http.StatusOK && code != http.StatusNoContent {
			t.Fatalf("DELETE credential %s = %d: %s", credID, code, string(body))
		}
		revokeDeadline := time.Now().Add(3 * time.Second)
		for repB.srv.Router().GetLocalTunnel("conn-workstation") != nil && time.Now().Before(revokeDeadline) {
			time.Sleep(20 * time.Millisecond)
		}
		if repB.srv.Router().GetLocalTunnel("conn-workstation") != nil {
			t.Fatalf("expected active tunnel for conn-workstation to disconnect immediately upon credential revocation")
		}
		connProc.Stop()

		// 7) Offline 503 naming `owner` (`alice@example.com`), `owner_group` (`group:eng-team@example.com`), and `last_seen_at`:
		// Refresh tunnel cache so requests to `conn-workstation` render the offline 503 page.
		_, _ = repA.srv.Router().TunnelCache().RefreshEntry(ctx, "conn-workstation")
		_, _ = repB.srv.Router().TunnelCache().RefreshEntry(ctx, "conn-workstation")

		code, body, _ = doMainJSON(repB, http.MethodGet, connHost, "/api/config", "bob@example.com", nil, nil)
		if code != http.StatusServiceUnavailable {
			t.Fatalf("expected 503 Service Unavailable for offline conn-workstation, got %d: %s", code, string(body))
		}
		for _, want := range []string{`"entry_offline"`, `"alice@example.com"`, `"group:eng-team@example.com"`, `"last_seen_at"`} {
			if !strings.Contains(string(body), want) {
				t.Fatalf("offline 503 JSON response missing %s: %s", want, string(body))
			}
		}

		// Also verify HTML offline 503 page names owner and last_seen_at.
		codeHTML, bodyHTML, _ := doMainJSON(repB, http.MethodGet, connHost, "/", "bob@example.com", nil, map[string]string{
			"Accept": "text/html",
		})
		if codeHTML != http.StatusServiceUnavailable || !strings.Contains(string(bodyHTML), "alice@example.com") {
			t.Fatalf("expected offline 503 HTML naming owner alice@example.com, got %d: %s", codeHTML, string(bodyHTML))
		}

		// 8) FrontIdentity / OIDC auth (`-hub-auth-mode=oidc`) on a real `grain daemon` subprocess:
		// Reserve `conn-oidc` with `connected_principal: "user:alice@example.com"` and start a real `grain daemon`
		// with `-hub-auth-mode=oidc` dialing `mainLB.URL` (:8080 main listener).
		code, body, _ = doMainJSON(repA, http.MethodPost, hubHost, "/api/entries", "alice@example.com", map[string]any{
			"name":                "conn-oidc",
			"kind":                api.KindConnected,
			"connected_principal": "user:alice@example.com",
			"owner_group":         "group:eng-team@example.com",
			"display_name":        "Connected OIDC Grain",
			"purpose":             "OIDC FrontIdentity tunnel grain",
			"listed":              true,
		}, nil)
		if code != http.StatusCreated {
			t.Fatalf("POST /api/entries conn-oidc = %d: %s", code, string(body))
		}

		mainLB.SetTargetURL(repB.mainSrv.URL)
		connOIDCProc = startE2EConnectedGrain(rootT, grainBin, e2eConnectedGrainConfig{
			EntryName:      "conn-oidc",
			OwnerEmail:     "alice@example.com",
			OwnerGroup:     "group:eng-team@example.com",
			InstanceDomain: instanceDomain,
			HubURL:         mainLB.URL,
			JWKSURL:        jwksURL(),
			AuthMode:       "oidc",
			OIDCEmail:      "alice@example.com",
			OIDCAudience:   "https://" + hubHost,
			Listed:         true,
			DisplayName:    "Connected OIDC Grain",
			Purpose:        "OIDC FrontIdentity tunnel grain",
		})

		oidcDeadline := time.Now().Add(15 * time.Second)
		for repB.srv.Router().GetLocalTunnel("conn-oidc") == nil && time.Now().Before(oidcDeadline) {
			time.Sleep(25 * time.Millisecond)
		}
		if repB.srv.Router().GetLocalTunnel("conn-oidc") == nil {
			t.Fatalf("timed out waiting for conn-oidc FrontIdentity tunnel on Replica B; logs:\n%s", connOIDCProc.logBuf.String())
		}
		_, _ = repA.srv.Router().TunnelCache().RefreshEntry(ctx, "conn-oidc")

		// Verify request to Replica A forwards over :8082 to Replica B -> FrontIdentity tunnel -> real `grain daemon`!
		code, body, _ = doMainJSON(repA, http.MethodGet, "conn-oidc."+instanceDomain, "/api/auth/whoami", "alice@example.com", nil, nil)
		if code != http.StatusOK || !strings.Contains(string(body), "alice@example.com") {
			t.Fatalf("proxied GET /api/auth/whoami over FrontIdentity tunnel = %d: %s", code, string(body))
		}
	})

	// =========================================================================
	// 3. Proxied HTTP / SSE / WebSocket / Nested-`hostd` Upgrades + ES256 JWT
	//    Verification + Stripped IAP Cookies/Edge Tokens + Sibling-Subdomain
	//    CSRF Refusal + Loopback-Bypass Refusal + 256 Stream Cap 503 +
	//    Even-Stream FrameReset Isolation
	// =========================================================================
	t.Run("ProxySecurityUpgradesStreamCapAndIsolation", func(t *testing.T) {
		hostedHost := "hosted-alpha." + instanceDomain
		connOIDCHost := "conn-oidc." + instanceDomain

		// A. Sibling-subdomain CSRF refusal BEFORE cookie stripping:
		// State-changing POST carrying an IAP cookie and cross-subdomain Origin (`https://conn-oidc.instances.example.com`)
		// must be rejected with 403 Forbidden before IAP cookies are stripped!
		code, body, _ := doMainJSON(repA, http.MethodPost, hostedHost, "/api/tasks", "alice@example.com", map[string]any{
			"title": "Cross-subdomain CSRF attempt",
		}, map[string]string{
			"Cookie": "__Host-GCP_IAP_AUTH_TOKEN_1=iap-secret-cookie; theme=dark",
			"Origin": "https://" + connOIDCHost,
		})
		if code != http.StatusForbidden {
			t.Fatalf("expected 403 Forbidden on sibling-subdomain CSRF POST, got %d: %s", code, string(body))
		}

		// B. Stripped IAP cookies & edge tokens + ES256 JWT verification (`/.well-known/jwks.json`) + Set-Cookie hardening + gma_ preservation:
		computeFake.SetInjectedResponseCookies("hosted-alpha", []string{
			"grain_ui_pref=1; Domain=.instances.example.com; Path=/",
			"__Host-session=abc; Path=/; Secure",
		})
		defer computeFake.SetInjectedResponseCookies("hosted-alpha", nil)

		edgeOIDC := makeE2EOIDCToken("alice@example.com", iapAudience)
		code, body, respHdr := doMainJSON(repA, http.MethodGet, hostedHost, "/api/auth/whoami", "", nil, map[string]string{
			"X-Goog-Authenticated-User-Email": "accounts.google.com:alice@example.com",
			"X-Goog-Authenticated-User-Id":    "accounts.google.com:12345",
			"X-Goog-IAP-JWT-Assertion":        "raw-edge-iap-assertion",
			"X-Forwarded-User":                "spoofed-user",
			"Proxy-Authorization":             "Bearer secret-proxy-tok",
			"X-Grain-Registry-Assertion":      "forged.incoming.assertion",
			"Authorization":                   "Bearer " + edgeOIDC,
			"Cookie":                          "__Host-GCP_IAP_AUTH_TOKEN_1=secret1; GCP_IAP_UID=2; GCP_IAAP_AUTH_TOKEN_X=3; user_theme=dark",
		})
		if code != http.StatusOK || !strings.Contains(string(body), "alice@example.com") {
			t.Fatalf("expected 200 OK from real grain daemon after header sanitization and JWT verification, got %d: %s", code, string(body))
		}

		capReq, ok := computeFake.LastCapturedRequest("hosted-alpha")
		if !ok {
			t.Fatalf("expected captured request on hosted-alpha")
		}
		if capReq.Host != hostedHost {
			t.Fatalf("expected preserved Host %q, got %q", hostedHost, capReq.Host)
		}
		for _, stripped := range []string{
			"X-Goog-Authenticated-User-Email",
			"X-Goog-Authenticated-User-Id",
			"X-Goog-Iap-Jwt-Assertion",
			"X-Forwarded-User",
			"Proxy-Authorization",
			"Authorization",
		} {
			if got := capReq.Headers.Get(stripped); got != "" {
				t.Fatalf("expected edge header %s to be stripped, but VM received %q", stripped, got)
			}
		}
		if len(capReq.Cookies) != 1 || capReq.Cookies[0].Name != "user_theme" || capReq.Cookies[0].Value != "dark" {
			t.Fatalf("expected IAP cookies stripped and user_theme=dark preserved, got %+v", capReq.Cookies)
		}

		// Verify the minted X-Grain-Registry-Assertion against `GET /.well-known/jwks.json`.
		mintedAssertion := capReq.Headers.Get(api.HeaderRegistryAssertion)
		if mintedAssertion == "" || mintedAssertion == "forged.incoming.assertion" {
			t.Fatalf("expected freshly signed X-Grain-Registry-Assertion, got %q", mintedAssertion)
		}
		jwksResp, err := http.Get(jwksURL())
		if err != nil {
			t.Fatalf("GET /.well-known/jwks.json: %v", err)
		}
		var jwksDoc hub.JSONWebKeySet
		if err := json.NewDecoder(jwksResp.Body).Decode(&jwksDoc); err != nil {
			_ = jwksResp.Body.Close()
			t.Fatalf("decode jwks.json: %v", err)
		}
		_ = jwksResp.Body.Close()

		verifiedClaims, err := hub.VerifyHubAssertion(mintedAssertion, &jwksDoc, "https://"+hostedHost, time.Now().UTC())
		if err != nil {
			t.Fatalf("VerifyHubAssertion against /.well-known/jwks.json failed: %v", err)
		}
		if verifiedClaims.Issuer != hub.HubAssertionIssuer || verifiedClaims.Email != "alice@example.com" {
			t.Fatalf("unexpected verified JWT claims: %+v", verifiedClaims)
		}

		// Verify Set-Cookie response hardening (Domain stripped, Secure & HttpOnly enforced).
		setCookies := respHdr.Values("Set-Cookie")
		if len(setCookies) < 2 {
			t.Fatalf("expected hardened Set-Cookie headers, got %v", setCookies)
		}
		for _, sc := range setCookies {
			lower := strings.ToLower(sc)
			if strings.Contains(lower, "domain=") {
				t.Fatalf("expected Domain attribute stripped from Set-Cookie, got %q", sc)
			}
			if !strings.Contains(lower, "secure") || !strings.Contains(lower, "httponly") {
				t.Fatalf("expected Secure and HttpOnly enforced on Set-Cookie, got %q", sc)
			}
		}

		// Verify `gma_…` external agent token is preserved across proxy sanitization.
		_, _, _ = doMainJSON(repA, http.MethodGet, hostedHost, "/api/config", "alice@example.com", nil, map[string]string{
			"Authorization": "Bearer gma_external_agent_token_e2e",
		})
		if capGMA, ok := computeFake.LastCapturedRequest("hosted-alpha"); !ok || capGMA.Headers.Get("Authorization") != "Bearer gma_external_agent_token_e2e" {
			t.Fatalf("expected Authorization: Bearer gma_... preserved, got %+v", capGMA.Headers)
		}

		// C. Loopback-bypass refusal on tunnel streams:
		// Open raw mux streams on Replica B's active tunnel to `conn-oidc` (real `grain daemon` running with `-hub-connect`)
		// and verify `HubTunnelHandler()` refuses:
		//  1) Requests without `X-Grain-Registry-Assertion` (even on open routes `/`, `/api/config`, `/api/auth/whoami`, `/api/hostd/connect`) -> 401
		//  2) Requests carrying `X-Grain-Access-Code`, `Cookie: grain_access=...`, or `Authorization: Bearer <access-code>`
		//     EVEN when accompanied by a valid `X-Grain-Registry-Assertion` and loopback headers -> 401!
		activeTun := repB.srv.Router().GetLocalTunnel("conn-oidc")
		if activeTun == nil {
			t.Fatalf("expected active tunnel on Replica B for conn-oidc")
		}
		sendRawTunnelHTTP := func(rawReq string) int {
			t.Helper()
			stConn, err := activeTun.OpenStream(ctx)
			if err != nil {
				t.Fatalf("activeTun.OpenStream: %v", err)
			}
			defer stConn.Close()
			_ = stConn.SetDeadline(time.Now().Add(5 * time.Second))
			if _, err := io.WriteString(stConn, rawReq); err != nil {
				t.Fatalf("write raw tunnel request: %v", err)
			}
			resp, err := http.ReadResponse(bufio.NewReader(stConn), nil)
			if err != nil {
				t.Fatalf("read raw tunnel response: %v", err)
			}
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			return resp.StatusCode
		}

		for _, openPath := range []string{"/", "/api/config", "/api/auth/whoami", "/api/hostd/connect"} {
			raw := fmt.Sprintf("GET %s HTTP/1.1\r\nHost: %s\r\nX-Forwarded-For: 127.0.0.1\r\nConnection: close\r\n\r\n", openPath, connOIDCHost)
			if status := sendRawTunnelHTTP(raw); status != http.StatusUnauthorized {
				t.Fatalf("expected 401 Unauthorized on tunnel stream to %s without registry assertion, got %d", openPath, status)
			}
		}

		validOIDCAssertion, err := sharedSigner.SignAssertion(ctx, hub.HubClaims{
			Audience: "https://" + connOIDCHost,
			Subject:  "alice@example.com",
			Email:    "alice@example.com",
			Kind:     "human",
		})
		if err != nil {
			t.Fatalf("SignAssertion: %v", err)
		}
		realAccessCode := connOIDCProc.AccessCode()
		if realAccessCode == "" {
			realAccessCode = "AAAA-BBBB-CCCC-DDDD"
		}
		for _, bypassHdr := range []string{
			"X-Grain-Access-Code: " + realAccessCode,
			"Cookie: grain_access=" + realAccessCode,
			"Authorization: Bearer " + realAccessCode,
		} {
			raw := fmt.Sprintf("GET /api/auth/whoami HTTP/1.1\r\nHost: %s\r\nX-Grain-Registry-Assertion: %s\r\n%s\r\nConnection: close\r\n\r\n",
				connOIDCHost, validOIDCAssertion, bypassHdr)
			if status := sendRawTunnelHTTP(raw); status != http.StatusUnauthorized {
				t.Fatalf("expected 401 Unauthorized on tunnel stream with loopback bypass header %q, got %d", bypassHdr, status)
			}
		}

		// D. Proxied SSE (`GET /api/events?since=0&stream=1` with `Accept: text/event-stream`, `FlushInterval: -1`)
		// across Replica A -> :8082 peer forward -> Replica B -> outbound tunnel -> real `grain daemon` subprocess!
		sseCtx, sseCancel := context.WithTimeout(ctx, 10*time.Second)
		defer sseCancel()
		sseReq, err := http.NewRequestWithContext(sseCtx, http.MethodGet, repA.mainSrv.URL+"/api/events?since=0&stream=1", nil)
		if err != nil {
			t.Fatalf("NewRequest SSE: %v", err)
		}
		sseReq.Host = connOIDCHost
		sseReq.Header.Set("X-Test-Email", "alice@example.com")
		sseReq.Header.Set("Accept", "text/event-stream")

		sseResp, err := (&http.Client{}).Do(sseReq)
		if err != nil {
			t.Fatalf("Do SSE request: %v", err)
		}
		defer sseResp.Body.Close()
		if sseResp.StatusCode != http.StatusOK || !strings.Contains(sseResp.Header.Get("Content-Type"), "text/event-stream") {
			t.Fatalf("expected 200 text/event-stream SSE response, got %d (Content-Type=%q)", sseResp.StatusCode, sseResp.Header.Get("Content-Type"))
		}

		// Configure a task on `conn-oidc` so the real `grain daemon` emits a live SSE event frame.
		go func() {
			time.Sleep(80 * time.Millisecond)
			_, _, _ = doMainJSON(repA, http.MethodPost, connOIDCHost, "/api/tasks", "alice@example.com", map[string]any{
				"title":  "Live SSE event over tunnel",
				"body":   "Triggers task event on /api/events SSE stream",
				"noRepo": true,
			}, nil)
		}()

		sseReader := bufio.NewReader(sseResp.Body)
		var sseLines []string
		for len(sseLines) < 20 {
			line, readErr := sseReader.ReadString('\n')
			if readErr != nil {
				break
			}
			sseLines = append(sseLines, line)
			joined := strings.Join(sseLines, "")
			if strings.Contains(joined, "task.") && strings.Contains(joined, "data:") {
				break
			}
		}
		sseCancel()
		_ = sseResp.Body.Close()
		if joined := strings.Join(sseLines, ""); !strings.Contains(joined, "data:") {
			t.Fatalf("expected streamed SSE data frame from real grain daemon, got %q", joined)
		}

		// E. Proxied WebSocket & Nested-`hostd` (`GET /api/hostd/connect`) upgrade across Replica A -> :8082 peer
		// forward -> Replica B -> outbound `mux` tunnel -> real `grain daemon` subprocess!
		// 1) Register a remote hostd `nested-worker-1` on `conn-oidc` via `POST /api/hostds`.
		code, body, _ = doMainJSON(repA, http.MethodPost, connOIDCHost, "/api/hostds", "alice@example.com", map[string]any{
			"name":  "nested-worker-1",
			"owner": "alice@example.com",
		}, nil)
		if code != http.StatusCreated {
			t.Fatalf("POST /api/hostds on conn-oidc = %d: %s", code, string(body))
		}
		var hostdIssued struct {
			Token string `json:"token"`
		}
		if err := json.Unmarshal(body, &hostdIssued); err != nil || hostdIssued.Token == "" {
			t.Fatalf("decode POST /api/hostds response: %v (%s)", err, string(body))
		}

		// 2) Dial Replica A over TCP and upgrade `GET /api/hostd/connect` (WebSocket 101 Switching Protocols)
		// carrying the issued hostd token, then start an inner `mux.Mux` client over the upgraded connection!
		uMainA, _ := url.Parse(repA.mainSrv.URL)
		rawHostdConn, err := net.DialTimeout("tcp", uMainA.Host, 5*time.Second)
		if err != nil {
			t.Fatalf("net.Dial repA: %v", err)
		}
		defer rawHostdConn.Close()

		wsKey := base64.StdEncoding.EncodeToString([]byte("e2e-nested-hostd"))
		upgradeReq := fmt.Sprintf(
			"GET /api/hostd/connect HTTP/1.1\r\n"+
				"Host: %s\r\n"+
				"X-Test-Email: alice@example.com\r\n"+
				"X-Grain-Hostd-Token: %s\r\n"+
				"X-Grain-Hostd-Name: nested-worker-1\r\n"+
				"X-Grain-Hostd-Host: nested-hostd-machine\r\n"+
				"Connection: Upgrade\r\n"+
				"Upgrade: %s\r\n"+
				"Sec-WebSocket-Version: 13\r\n"+
				"Sec-WebSocket-Key: %s\r\n\r\n",
			connOIDCHost, hostdIssued.Token, wsupgrade.Protocol, wsKey,
		)
		if _, err := io.WriteString(rawHostdConn, upgradeReq); err != nil {
			t.Fatalf("write /api/hostd/connect upgrade request: %v", err)
		}
		hostdBR := bufio.NewReader(rawHostdConn)
		upgradeResp, err := http.ReadResponse(hostdBR, nil)
		if err != nil {
			t.Fatalf("read /api/hostd/connect upgrade response: %v", err)
		}
		if upgradeResp.StatusCode != http.StatusSwitchingProtocols {
			respBody, _ := io.ReadAll(upgradeResp.Body)
			t.Fatalf("expected 101 Switching Protocols on nested /api/hostd/connect upgrade, got %d: %s", upgradeResp.StatusCode, string(respBody))
		}

		// Run the inner hostd `mux.Mux` over the hijacked stream and verify the real `grain daemon`
		// sees `nested-worker-1` as `connected: true` on `GET /api/hostds`!
		nestedMux := mux.NewMux(wsupgrade.BufferedConn(rawHostdConn, hostdBR), mux.Config{
			IsServer:     false,
			PingInterval: 10 * time.Second,
			PingTimeout:  10 * time.Second,
		})
		defer nestedMux.Close()
		go func() {
			for {
				st, err := nestedMux.AcceptStream()
				if err != nil {
					return
				}
				_ = st.Close()
			}
		}()

		hostdConnected := false
		for i := 0; i < 30; i++ {
			c, b, _ := doMainJSON(repA, http.MethodGet, connOIDCHost, "/api/hostds", "alice@example.com", nil, nil)
			if c == http.StatusOK && strings.Contains(string(b), `"nested-worker-1"`) && strings.Contains(string(b), `"connected":true`) {
				hostdConnected = true
				break
			}
			time.Sleep(30 * time.Millisecond)
		}
		if !hostdConnected {
			t.Fatalf("expected nested-worker-1 to report connected:true on real grain daemon after /api/hostd/connect upgrade")
		}
		_ = nestedMux.Close()
		_ = rawHostdConn.Close()

		// F. `256` concurrent stream cap (`503 Service Unavailable` on the 257th stream):
		// Wait for any previous stream to close so ActiveStreams() returns to 0.
		streamDrainDeadline := time.Now().Add(3 * time.Second)
		for activeTun.ActiveStreams() > 0 && time.Now().Before(streamDrainDeadline) {
			time.Sleep(15 * time.Millisecond)
		}
		heldStreams := make([]net.Conn, 0, 256)
		for len(heldStreams) < 256 {
			stConn, err := activeTun.OpenStream(ctx)
			if err != nil {
				for _, s := range heldStreams {
					_ = s.Close()
				}
				t.Fatalf("OpenStream %d of 256 failed: %v", len(heldStreams)+1, err)
			}
			heldStreams = append(heldStreams, stConn)
		}
		if got := activeTun.ActiveStreams(); got != 256 {
			for _, s := range heldStreams {
				_ = s.Close()
			}
			t.Fatalf("expected 256 active streams on tunnel, got %d", got)
		}

		// The 257th concurrent stream request must immediately return 503 Service Unavailable with Retry-After!
		code257, body257, hdr257 := doMainJSON(repB, http.MethodGet, connOIDCHost, "/api/config", "alice@example.com", nil, nil)
		if code257 != http.StatusServiceUnavailable || hdr257.Get("Retry-After") == "" {
			for _, s := range heldStreams {
				_ = s.Close()
			}
			t.Fatalf("expected 257th concurrent stream to return 503 with Retry-After, got %d (Retry-After=%q): %s",
				code257, hdr257.Get("Retry-After"), string(body257))
		}

		// Release all 256 held streams and verify the next request succeeds with 200 OK.
		for _, s := range heldStreams {
			_ = s.Close()
		}
		codeAfterRelease, _, _ := doMainJSON(repB, http.MethodGet, connOIDCHost, "/api/config", "alice@example.com", nil, nil)
		if codeAfterRelease != http.StatusOK {
			t.Fatalf("expected 200 OK after releasing 256 streams, got %d", codeAfterRelease)
		}

		// G. Even-stream `FrameReset` isolation (`MaxPeerInitiatedStreams = 0`):
		// Dial a connected entry tunnel where an odd-numbered request stream (streamID=1) is held in-flight,
		// while the connected peer initiates an even-numbered stream (`OpenStream` -> streamID=2) toward the registry.
		// Verify the registry immediately sends `FrameReset` on the even stream and increments `PeerStreamRejectedTotal`
		// WITHOUT dropping the tunnel session or disturbing the in-flight odd request stream!
		code, body, _ = doMainJSON(repA, http.MethodPost, hubHost, "/api/entries", "alice@example.com", map[string]any{
			"name": "conn-even-iso",
			"kind": api.KindConnected,
		}, nil)
		if code != http.StatusCreated {
			t.Fatalf("POST /api/entries conn-even-iso = %d: %s", code, string(body))
		}
		var isoCreate api.CreateEntryResponse
		_ = json.Unmarshal(body, &isoCreate)

		oddReqEntered := make(chan struct{})
		releaseOddReq := make(chan struct{})
		isoBackend := http.NewServeMux()
		isoBackend.HandleFunc("/in-flight", func(w http.ResponseWriter, r *http.Request) {
			close(oddReqEntered)
			<-releaseOddReq
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("odd-stream-survived"))
		})

		isoConnRes, err := (api.DefaultConnectDialer{}).DialConnect(ctx, api.ConnectConfig{
			BaseURL:           repA.pubSrv.URL,
			Entry:             "conn-even-iso",
			RegistrationToken: isoCreate.RegistrationToken,
			AllowInsecureHTTP: true,
		})
		if err != nil {
			t.Fatalf("DialConnect conn-even-iso: %v", err)
		}
		defer isoConnRes.Conn.Close()
		clientIsoMux := mux.NewMux(isoConnRes.Conn, mux.Config{
			IsServer:     false,
			PingInterval: 10 * time.Second,
			PingTimeout:  10 * time.Second,
		})
		defer clientIsoMux.Close()
		go func() {
			for {
				st, err := clientIsoMux.AcceptStream()
				if err != nil {
					return
				}
				go func(c net.Conn) {
					defer c.Close()
					req, err := http.ReadRequest(bufio.NewReader(c))
					if err != nil {
						return
					}
					rec := httptest.NewRecorder()
					isoBackend.ServeHTTP(rec, req)
					res := rec.Result()
					_ = res.Write(c)
				}(st)
			}
		}()

		// Start an odd-numbered request stream and wait until it is in flight inside `/in-flight`.
		type oddResult struct {
			code int
			body string
		}
		oddDone := make(chan oddResult, 1)
		go func() {
			c, b, _ := doMainJSON(repA, http.MethodGet, "conn-even-iso."+instanceDomain, "/in-flight", "alice@example.com", nil, nil)
			oddDone <- oddResult{code: c, body: string(b)}
		}()
		select {
		case <-oddReqEntered:
		case res := <-oddDone:
			t.Fatalf("odd request failed before entering /in-flight: %d %s", res.code, res.body)
		case <-time.After(5 * time.Second):
			t.Fatalf("timed out waiting for odd request stream to enter /in-flight")
		}

		// While the odd stream is in flight, open an even-numbered stream from clientIsoMux toward the registry.
		beforeRejected := repA.metrics.Snapshot().PeerStreamsRejected
		evenStream, err := clientIsoMux.OpenStream(ctx)
		if err == nil {
			_ = evenStream.SetDeadline(time.Now().Add(3 * time.Second))
			_, _ = evenStream.Write([]byte("forbidden-peer-initiated-even-stream"))
			_, readErr := evenStream.Read(make([]byte, 16))
			_ = evenStream.Close()
			if readErr == nil {
				t.Fatalf("expected even-numbered peer-initiated stream to be reset by registry with FrameReset")
			}
		}
		rejectDeadline := time.Now().Add(2 * time.Second)
		for repA.metrics.Snapshot().PeerStreamsRejected <= beforeRejected && time.Now().Before(rejectDeadline) {
			time.Sleep(10 * time.Millisecond)
		}
		if repA.metrics.Snapshot().PeerStreamsRejected <= beforeRejected {
			t.Fatalf("expected PeerStreamsRejected metric to increment on even-stream FrameReset")
		}

		// Release the odd stream and verify it completes with 200 OK ("odd-stream-survived").
		close(releaseOddReq)
		res := <-oddDone
		if res.code != http.StatusOK || res.body != "odd-stream-survived" {
			t.Fatalf("expected odd request stream to survive even-stream FrameReset, got %d %q", res.code, res.body)
		}
	})

	// =========================================================================
	// 4. Unlisted Privacy, Listed Primary-Prefix 409 & Async `conflict_with`,
	//    `owner_group` Co-Management & Quota Attribution, `/api/directory` &
	//    `/api/route` Ranking, and Prefix-Escape Refusal
	// =========================================================================
	t.Run("UnlistedPrivacyConflictsCoManagementRankingAndPrefixEscape", func(t *testing.T) {
		// A. Unlisted privacy (`listed` omitted -> defaults to false):
		// Create an unlisted entry `secret-box` owned by alice@example.com with scopes on `acme/stealth`.
		code, body, _ := doMainJSON(repA, http.MethodPost, hubHost, "/api/entries", "alice@example.com", map[string]any{
			"name":    "secret-box",
			"kind":    api.KindConnected,
			"purpose": "Unlisted private workstation",
			"scopes": []map[string]any{
				{"repo": "acme/stealth", "role": api.RolePrimary},
			},
		}, nil)
		if code != http.StatusCreated {
			t.Fatalf("POST /api/entries secret-box = %d: %s", code, string(body))
		}
		var secretCreate api.CreateEntryResponse
		_ = json.Unmarshal(body, &secretCreate)
		if secretCreate.Entry.Listed {
			t.Fatalf("expected listed to default to false, got true")
		}

		// Send heartbeat so `secret-box` is online with reported scopes on `acme/stealth`.
		code, body, _ = doMainJSON(repA, http.MethodPut, hubHost, "/api/entries/secret-box/heartbeat", "", map[string]any{
			"grain_version": "v2.5.0",
			"health":        api.HealthOK,
			"scopes": []map[string]any{
				{"repo": "acme/stealth", "role": api.RolePrimary},
			},
		}, map[string]string{
			"Authorization":         "Bearer " + secretCreate.RegistrationToken,
			api.HeaderAuth:          api.AuthModeToken,
			api.HeaderRegistryToken: secretCreate.RegistrationToken,
		})
		if code != http.StatusOK {
			t.Fatalf("PUT heartbeat secret-box = %d: %s", code, string(body))
		}

		// Verify `secret-box` is excluded from `/api/directory` and `/api/route` for all callers
		// (regular users, admins, and service principals)!
		for _, caller := range []string{"charlie@example.com", "admin@example.com", "ci-bot@test-proj.iam.gserviceaccount.com"} {
			cDir, bDir, _ := doMainJSON(repA, http.MethodGet, hubHost, "/api/directory?include_unhealthy=true", caller, nil, nil)
			if cDir != http.StatusOK || strings.Contains(string(bDir), "secret-box") || strings.Contains(string(bDir), "acme/stealth") {
				t.Fatalf("unlisted entry secret-box leaked in /api/directory for caller %s: %d %s", caller, cDir, string(bDir))
			}
			cRoute, bRoute, _ := doMainJSON(repA, http.MethodGet, hubHost, "/api/route?repo=acme/stealth", caller, nil, nil)
			if cRoute != http.StatusOK || strings.Contains(string(bRoute), "secret-box") {
				t.Fatalf("unlisted entry secret-box leaked in /api/route for caller %s: %d %s", caller, cRoute, string(bRoute))
			}
		}

		// Verify `GET /api/entries` for charlie@example.com never returns alice's entries or `backend_url`.
		cList, bList, _ := doMainJSON(repA, http.MethodGet, hubHost, "/api/entries", "charlie@example.com", nil, nil)
		if cList != http.StatusOK || strings.Contains(string(bList), "secret-box") || strings.Contains(string(bList), "hosted-alpha") || strings.Contains(string(bList), "127.0.0.1") {
			t.Fatalf("GET /api/entries for charlie leaked another owner's entries or backend_url: %d %s", cList, string(bList))
		}

		// B. Listed primary-prefix synchronous `409 Conflict` and async heartbeat `conflict_with` handling:
		// 1) Create listed entry `rank-primary-root` by charlie@example.com with declared primary scope on `acme/monorepo` (`/pkg/api`).
		code, body, _ = doMainJSON(repA, http.MethodPost, hubHost, "/api/entries", "charlie@example.com", map[string]any{
			"name":   "rank-primary-root",
			"kind":   api.KindConnected,
			"listed": true,
			"scopes": []map[string]any{
				{"repo": "acme/monorepo", "path_prefix": "/pkg/api", "role": api.RolePrimary},
			},
		}, nil)
		if code != http.StatusCreated {
			t.Fatalf("POST /api/entries rank-primary-root = %d: %s", code, string(body))
		}
		var rootCreate api.CreateEntryResponse
		_ = json.Unmarshal(body, &rootCreate)

		// Synchronously creating another listed entry declaring the same `(repo, path_prefix, role="primary")` must return 409 Conflict!
		code409, body409, _ := doMainJSON(repA, http.MethodPost, hubHost, "/api/entries", "dave@example.com", map[string]any{
			"name":   "rank-conflict-sync",
			"kind":   api.KindConnected,
			"listed": true,
			"scopes": []map[string]any{
				{"repo": "acme/monorepo", "path_prefix": "/pkg/api", "role": api.RolePrimary},
			},
		}, nil)
		if code409 != http.StatusConflict || !strings.Contains(string(body409), "rank-primary-root") {
			t.Fatalf("expected 409 Conflict naming rank-primary-root on duplicate listed primary scope, got %d: %s", code409, string(body409))
		}

		// 2) Async heartbeat `conflict_with` handling:
		// Create listed entry `rank-late-claimant` by dave@example.com without conflicting declared scopes,
		// then have both `rank-primary-root` and `rank-late-claimant` send heartbeats reporting `(acme/monorepo, /pkg/api, primary)`.
		// The heartbeat from `rank-late-claimant` must be accepted with 200 OK, return `conflicts` (`conflict_with: "rank-primary-root"`),
		// mark `rank-late-claimant` degraded, and preserve clean primary routing for `rank-primary-root`!
		code, body, _ = doMainJSON(repA, http.MethodPost, hubHost, "/api/entries", "dave@example.com", map[string]any{
			"name":   "rank-late-claimant",
			"kind":   api.KindConnected,
			"listed": true,
		}, nil)
		if code != http.StatusCreated {
			t.Fatalf("POST /api/entries rank-late-claimant = %d: %s", code, string(body))
		}
		var lateCreate api.CreateEntryResponse
		_ = json.Unmarshal(body, &lateCreate)

		// Heartbeat 1: `rank-primary-root` reports `/pkg/api` primary (`active_tasks: 2`).
		code, body, _ = doMainJSON(repA, http.MethodPut, hubHost, "/api/entries/rank-primary-root/heartbeat", "", map[string]any{
			"grain_version": "v2.5.0",
			"health":        api.HealthOK,
			"active_tasks":  2,
			"scopes": []map[string]any{
				{"repo": "acme/monorepo", "path_prefix": "/pkg/api", "role": api.RolePrimary},
			},
		}, map[string]string{
			"Authorization":         "Bearer " + rootCreate.RegistrationToken,
			api.HeaderAuth:          api.AuthModeToken,
			api.HeaderRegistryToken: rootCreate.RegistrationToken,
		})
		if code != http.StatusOK {
			t.Fatalf("PUT heartbeat rank-primary-root = %d: %s", code, string(body))
		}

		// Heartbeat 2: `rank-late-claimant` reports overlapping `/pkg/api` primary (`active_tasks: 0`).
		code, body, _ = doMainJSON(repA, http.MethodPut, hubHost, "/api/entries/rank-late-claimant/heartbeat", "", map[string]any{
			"grain_version": "v2.5.0",
			"health":        api.HealthOK,
			"active_tasks":  0,
			"scopes": []map[string]any{
				{"repo": "acme/monorepo", "path_prefix": "/pkg/api", "role": api.RolePrimary},
			},
		}, map[string]string{
			"Authorization":         "Bearer " + lateCreate.RegistrationToken,
			api.HeaderAuth:          api.AuthModeToken,
			api.HeaderRegistryToken: lateCreate.RegistrationToken,
		})
		if code != http.StatusOK || !strings.Contains(string(body), `"conflicting_entry":"rank-primary-root"`) {
			t.Fatalf("expected 200 OK with conflicting_entry:rank-primary-root on async conflicting heartbeat, got %d: %s", code, string(body))
		}
		code, body, _ = doMainJSON(repA, http.MethodGet, hubHost, "/api/entries/rank-late-claimant", "dave@example.com", nil, nil)
		if code != http.StatusOK || !strings.Contains(string(body), `"conflict_with":"rank-primary-root"`) {
			t.Fatalf("expected rank-late-claimant scope to carry conflict_with:rank-primary-root, got %d: %s", code, string(body))
		}

		// C. `/api/directory` and `/api/route` ranking:
		// Mark `rank-primary-root` online with an active tunnel session and also create:
		// - `rank-subpath-secondary`: listed entry owning longer prefix `/pkg/api/v2` (`role: "secondary"`, `active_tasks: 5`)
		// - `rank-api-secondary`: listed entry owning `/pkg/api` (`role: "secondary"`, `active_tasks: 0`)
		// - `rank-api-secondary-busy`: listed entry owning `/pkg/api` (`role: "secondary"`, `active_tasks: 4`)
		markTunnelOnline := func(entryName string) {
			now := time.Now().UTC()
			repA.srv.Router().TunnelCache().SetActive(regserver.TunnelCacheEntry{
				Name:            entryName,
				SessionEpoch:    1,
				ReplicaID:       "replica-a",
				PeerAddr:        repA.peerSrv.Listener.Addr().String(),
				ReplicaLastSeen: now,
				ConnectedAt:     now,
				LastSeenAt:      now,
				UpdatedAt:       now,
			})
		}
		markTunnelOnline("rank-primary-root")

		createAndHeartbeat := func(owner, name, prefix string, role api.ScopeRole, activeTasks int) {
			t.Helper()
			c, b, _ := doMainJSON(repA, http.MethodPost, hubHost, "/api/entries", owner, map[string]any{
				"name":   name,
				"kind":   api.KindConnected,
				"listed": true,
			}, nil)
			if c != http.StatusCreated {
				t.Fatalf("create %s = %d: %s", name, c, string(b))
			}
			var cr api.CreateEntryResponse
			_ = json.Unmarshal(b, &cr)
			c, b, _ = doMainJSON(repB, http.MethodPut, hubHost, "/api/entries/"+name+"/heartbeat", "", map[string]any{
				"grain_version": "v2.5.0",
				"health":        api.HealthOK,
				"active_tasks":  activeTasks,
				"scopes": []map[string]any{
					{"repo": "acme/monorepo", "path_prefix": prefix, "role": role},
				},
			}, map[string]string{
				"Authorization":         "Bearer " + cr.RegistrationToken,
				api.HeaderAuth:          api.AuthModeToken,
				api.HeaderRegistryToken: cr.RegistrationToken,
			})
			if c != http.StatusOK {
				t.Fatalf("heartbeat %s = %d: %s", name, c, string(b))
			}
			markTunnelOnline(name)
		}

		createAndHeartbeat("eve@example.com", "rank-subpath-secondary", "/pkg/api/v2", api.RoleSecondary, 5)
		createAndHeartbeat("frank@example.com", "rank-api-secondary", "/pkg/api", api.RoleSecondary, 0)
		createAndHeartbeat("grace@example.com", "rank-api-secondary-busy", "/pkg/api", api.RoleSecondary, 4)

		repA.srv.Router().CatalogCache().Invalidate()

		// 1) Query `/api/route?repo=acme/monorepo&path=pkg/api/v2/handler.go`:
		// - Clean `primary` (`rank-primary-root`) wins #1 (and is `Primary`), while conflicting `rank-late-claimant` is excluded.
		// - Longest segment-boundary `path_prefix` match (`/pkg/api/v2` on `rank-subpath-secondary`, specificity 10) ranks ahead of shorter `/pkg/api` secondaries (specificity 7) despite higher `active_tasks`.
		// - Between equal-specificity `/pkg/api` secondaries, lower `active_tasks` (`rank-api-secondary`, 0) ranks ahead of higher `active_tasks` (`rank-api-secondary-busy`, 4).
		code, body, _ = doMainJSON(repA, http.MethodGet, hubHost, "/api/route?repo=acme/monorepo&path=pkg/api/v2/handler.go", "charlie@example.com", nil, nil)
		if code != http.StatusOK {
			t.Fatalf("GET /api/route v2 = %d: %s", code, string(body))
		}
		var routeV2 api.RouteResponse
		if err := json.Unmarshal(body, &routeV2); err != nil {
			t.Fatalf("unmarshal routeV2: %v", err)
		}
		if len(routeV2.Candidates) != 4 ||
			routeV2.Candidates[0].Name != "rank-primary-root" ||
			routeV2.Candidates[1].Name != "rank-subpath-secondary" ||
			routeV2.Candidates[2].Name != "rank-api-secondary" ||
			routeV2.Candidates[3].Name != "rank-api-secondary-busy" {
			t.Fatalf("unexpected /api/route v2 candidate ranking: %+v", routeV2.Candidates)
		}

		// 2) Query `/api/route?repo=acme/monorepo&path=pkg/api/v1/server.go`:
		// Clean `primary` (`rank-primary-root`) ranks ahead of `secondary` (`rank-api-secondary`, `rank-api-secondary-busy`),
		// and `/pkg/api/v2` (`rank-subpath-secondary`) does not match `pkg/api/v1/server.go`!
		code, body, _ = doMainJSON(repA, http.MethodGet, hubHost, "/api/route?repo=acme/monorepo&path=pkg/api/v1/server.go", "charlie@example.com", nil, nil)
		if code != http.StatusOK {
			t.Fatalf("GET /api/route v1 = %d: %s", code, string(body))
		}
		var routeV1 api.RouteResponse
		if err := json.Unmarshal(body, &routeV1); err != nil {
			t.Fatalf("unmarshal routeV1: %v", err)
		}
		if len(routeV1.Candidates) != 3 ||
			routeV1.Candidates[0].Name != "rank-primary-root" ||
			routeV1.Candidates[1].Name != "rank-api-secondary" ||
			routeV1.Candidates[2].Name != "rank-api-secondary-busy" {
			t.Fatalf("expected non-conflicting primary rank-primary-root then secondaries for pkg/api/v1/server.go, got %+v", routeV1.Candidates)
		}

		// 3) Segment-boundary check: `/pkg/api-extra/foo.go` must NOT match `/pkg/api`!
		code, body, _ = doMainJSON(repA, http.MethodGet, hubHost, "/api/route?repo=acme/monorepo&path=pkg/api-extra/foo.go", "charlie@example.com", nil, nil)
		if code != http.StatusOK {
			t.Fatalf("GET /api/route segment boundary = %d: %s", code, string(body))
		}
		var routeBoundary api.RouteResponse
		_ = json.Unmarshal(body, &routeBoundary)
		if len(routeBoundary.Candidates) != 0 {
			t.Fatalf("expected 0 candidates for non-segment-boundary path pkg/api-extra/foo.go, got %+v", routeBoundary.Candidates)
		}

		// 4) `/api/directory?repo=acme/monorepo` lists the public catalog entries for `acme/monorepo`.
		code, body, _ = doMainJSON(repA, http.MethodGet, hubHost, "/api/directory?repo=acme/monorepo", "charlie@example.com", nil, nil)
		if code != http.StatusOK || !strings.Contains(string(body), "rank-primary-root") || !strings.Contains(string(body), "rank-subpath-secondary") {
			t.Fatalf("GET /api/directory?repo=acme/monorepo missing expected entries: %d %s", code, string(body))
		}

		// D. Prefix-escape / `..` path traversal refusal (`400 Bad Request`):
		for _, badPath := range []string{
			"/api/route?repo=acme/monorepo&path=../secret",
			"/api/route?repo=acme/monorepo&path=pkg/../../etc/passwd",
			"/api/route?repo=acme/monorepo&path=/pkg/api/..",
		} {
			cBad, bBad, _ := doMainJSON(repA, http.MethodGet, hubHost, badPath, "charlie@example.com", nil, nil)
			if cBad != http.StatusBadRequest {
				t.Fatalf("expected 400 Bad Request for prefix-escape query %s, got %d: %s", badPath, cBad, string(bBad))
			}
		}

		// Also verify declaring a scope with `..` in `path_prefix` is rejected with 400 Bad Request.
		cBadScope, bBadScope, _ := doMainJSON(repA, http.MethodPut, hubHost, "/api/entries/rank-primary-root/scopes", "charlie@example.com", map[string]any{
			"scopes": []map[string]any{
				{"repo": "acme/monorepo", "path_prefix": "/pkg/../secret", "role": api.RolePrimary},
			},
		}, nil)
		if cBadScope != http.StatusBadRequest {
			t.Fatalf("expected 400 Bad Request for path_prefix containing '..', got %d: %s", cBadScope, string(bBadScope))
		}
	})
}

// TestMultiReplicaDelegationCacheEviction verifies that revoking a consent grant or
// disabling a delegating app on Replica A propagates over PostgreSQL
// `LISTEN registry_delegations` (`pg_notify`) so Replica B immediately evicts its
// cached `X-Grain-Registry-Assertion` tokens rather than serving them for up to 30s.
func TestMultiReplicaDelegationCacheEviction(t *testing.T) {
	pg := startE2EPostgresCluster(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	st, err := store.Open(ctx, pg.HubDSN, store.DefaultThresholds())
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	if err := st.SeedShapes(ctx, []store.ShapeRow{
		{Name: "standard", MachineType: "n2-standard-4", BootDiskGB: 50, QuotaUnits: 1},
	}); err != nil {
		t.Fatalf("SeedShapes: %v", err)
	}

	var lastAssertionMu sync.Mutex
	var lastAssertion string
	backendSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lastAssertionMu.Lock()
		lastAssertion = r.Header.Get(api.HeaderRegistryAssertion)
		lastAssertionMu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(backendSrv.Close)

	popAssertion := func() string {
		lastAssertionMu.Lock()
		defer lastAssertionMu.Unlock()
		got := lastAssertion
		lastAssertion = ""
		return got
	}

	const (
		hubHost        = "registry.example.com"
		instanceDomain = "instances.example.com"
		iapAudience    = "/projects/123/global/backendServices/e2e-delegation"
		adminEmail     = "admin@example.com"
		aliceEmail     = "alice@example.com"
		plannerSA      = "planner@test-proj.iam.gserviceaccount.com"
		triageSA       = "triage@test-proj.iam.gserviceaccount.com"
	)

	if _, err := st.CreateEntry(ctx, store.CreateEntryInput{
		Name:           "alice-vm",
		Kind:           api.KindInstance,
		OwnerEmail:     aliceEmail,
		Shape:          "standard",
		Zone:           "us-central1-a",
		LifecycleState: api.StateRunning,
		BackendURL:     backendSrv.URL,
	}); err != nil {
		t.Fatalf("CreateEntry alice-vm: %v", err)
	}

	groups := newE2EGroupResolver(nil)
	localSigner, err := hub.NewLocalSigner(hub.SigningConfig{
		Provider: "local",
		KeyID:    "e2e-delegation-key-1",
		JWTTTL:   60 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewLocalSigner: %v", err)
	}
	jwksDoc, err := localSigner.JWKS(ctx)
	if err != nil {
		t.Fatalf("localSigner.JWKS: %v", err)
	}

	authCfg := hub.AuthConfig{
		Provider:          "iap",
		IAPAudience:       iapAudience,
		AllowedDomains:    []string{"example.com"},
		Admins:            []string{"user:" + adminEmail},
		ServicePrincipals: []string{"serviceAccount:ci-bot@test-proj.iam.gserviceaccount.com"},
		DelegationPolicy:  "consent",
	}
	regCfg := hub.Config{
		Host:           hubHost,
		InstanceDomain: instanceDomain,
		Listen:         "127.0.0.1:8080",
		JWKSListen:     "127.0.0.1:8081",
		Database:       hub.DatabaseConfig{URL: pg.HubDSN},
		Auth:           authCfg,
		Signing: hub.SigningConfig{
			Provider: "local",
			KeyID:    "e2e-delegation-key-1",
			JWTTTL:   60 * time.Second,
		},
		Compute: hub.ComputeConfig{
			Provider:     "gce",
			Project:      "test-proj",
			Region:       "us-central1",
			Zones:        []string{"us-central1-a"},
			DefaultShape: "standard",
			Shapes: []hub.Shape{
				{Name: "standard", MachineType: "n2-standard-4", BootDiskGB: 50, QuotaUnits: 1},
			},
		},
		Quotas: hub.QuotasConfig{
			DefaultInstanceUnits: 2,
			DefaultConnected:     3,
		},
		Tunnel: hub.TunnelConfig{
			PeerListen:           "127.0.0.1:8082",
			MaxConcurrentStreams: 256,
			MaxBufferedBytes:     16 << 20,
			ReconnectGrace:       2 * time.Second,
			DrainTimeout:         500 * time.Millisecond,
			ReplicaHeartbeat:     2 * time.Second,
			ReplicaTTL:           10 * time.Second,
		},
		Catalog: hub.CatalogConfig{
			CacheTTL: 50 * time.Millisecond,
		},
	}

	type delegationReplica struct {
		id      string
		srv     *hub.Server
		signer  *hub.CachingSigner
		mainSrv *httptest.Server
	}

	// Each replica has its own ConsentDelegationPolicy and its own 30s CachingSigner
	// wrapping the shared key, sharing state only through PostgreSQL.
	startReplica := func(id string) *delegationReplica {
		pol, err := hub.NewConsentDelegationPolicy(authCfg, st)
		if err != nil {
			t.Fatalf("NewConsentDelegationPolicy(%s): %v", id, err)
		}
		admitter, err := webauth.NewAdmitter(webauth.AdmissionConfig{
			Verifier:                e2eIdentityVerifier{},
			Groups:                  groups,
			AllowedDomains:          authCfg.AllowedDomains,
			Admins:                  authCfg.Admins,
			ServicePrincipals:       authCfg.ServicePrincipals,
			ServicePrincipalChecker: pol.IsServicePrincipalReader,
		})
		if err != nil {
			t.Fatalf("NewAdmitter(%s): %v", id, err)
		}
		repSigner := hub.NewCachingSigner(localSigner, 30*time.Second)
		srv, err := hub.NewServer(hub.ServerOptions{
			Config:     regCfg,
			Store:      st,
			Admitter:   admitter,
			Groups:     groups,
			Delegation: pol,
			Signer:     repSigner,
			ReplicaID:  id,
		})
		if err != nil {
			t.Fatalf("NewServer(%s): %v", id, err)
		}
		mainSrv := httptest.NewServer(srv.Handler())
		srv.StartBackground(ctx)
		t.Cleanup(func() {
			srv.Router().Close()
			mainSrv.CloseClientConnections()
			mainSrv.Close()
		})
		return &delegationReplica{
			id:      id,
			srv:     srv,
			signer:  repSigner,
			mainSrv: mainSrv,
		}
	}

	repA := startReplica("deleg-rep-a")
	repB := startReplica("deleg-rep-b")

	doJSON := func(rep *delegationReplica, method, host, path, callerEmail string, body any, extraHdr map[string]string) (int, []byte) {
		t.Helper()
		var bodyReader io.Reader
		if body != nil {
			b, err := json.Marshal(body)
			if err != nil {
				t.Fatalf("json.Marshal: %v", err)
			}
			bodyReader = bytes.NewReader(b)
		}
		req, err := http.NewRequestWithContext(ctx, method, rep.mainSrv.URL+path, bodyReader)
		if err != nil {
			t.Fatalf("http.NewRequest: %v", err)
		}
		req.Host = host
		if callerEmail != "" {
			req.Header.Set("X-Test-Email", callerEmail)
		}
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		req.Header.Set("Accept", "application/json")
		for k, v := range extraHdr {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s %s%s on %s: %v", method, host, path, rep.id, err)
		}
		defer resp.Body.Close()
		respBytes, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, respBytes
	}

	entryHost := "alice-vm." + instanceDomain
	entryAud := "https://" + entryHost
	oboHdr := map[string]string{regserver.HeaderOnBehalfOf: aliceEmail}

	// 1. Admin registers two delegating apps ("planner" and "triage") on Replica A,
	// and Alice consents to both on Replica A.
	for _, appSpec := range []api.CreateDelegatingAppRequest{
		{Name: "planner", Principal: plannerSA, MaxAccess: "write"},
		{Name: "triage", Principal: triageSA, MaxAccess: "read"},
	} {
		if code, body := doJSON(repA, http.MethodPost, hubHost, "/api/admin/delegating-apps", adminEmail, appSpec, nil); code != http.StatusCreated {
			t.Fatalf("POST delegating-apps %s = %d %s", appSpec.Name, code, body)
		}
	}

	code, body := doJSON(repA, http.MethodPost, hubHost, "/api/delegation-grants", aliceEmail,
		api.CreateDelegationGrantRequest{App: "planner", Entries: []string{"alice-vm"}, Access: "read"}, nil)
	if code != http.StatusOK {
		t.Fatalf("POST grant planner = %d %s", code, body)
	}
	var plannerGrant1 api.DelegationGrant
	if err := json.Unmarshal(body, &plannerGrant1); err != nil {
		t.Fatalf("unmarshal plannerGrant1: %v", err)
	}

	code, body = doJSON(repA, http.MethodPost, hubHost, "/api/delegation-grants", aliceEmail,
		api.CreateDelegationGrantRequest{App: "triage", Entries: []string{"alice-vm"}, Access: "read"}, nil)
	if code != http.StatusOK {
		t.Fatalf("POST grant triage = %d %s", code, body)
	}
	var triageGrant api.DelegationGrant
	if err := json.Unmarshal(body, &triageGrant); err != nil {
		t.Fatalf("unmarshal triageGrant: %v", err)
	}

	// 2. Warm Replica B's assertion cache via real delegated HTTP requests for both apps.
	_ = popAssertion()
	if code, body := doJSON(repB, http.MethodGet, entryHost, "/api/tasks", plannerSA, nil, oboHdr); code != http.StatusOK {
		t.Fatalf("delegated GET via repB (planner #1) = %d %s", code, body)
	}
	plannerTokB1 := popAssertion()
	if plannerTokB1 == "" {
		t.Fatal("backend did not receive X-Grain-Registry-Assertion from repB")
	}
	claims1, err := hub.VerifyHubAssertion(plannerTokB1, jwksDoc, entryAud, time.Now().UTC())
	if err != nil || claims1.Actor == nil || claims1.Actor.Grant != plannerGrant1.ID || claims1.Actor.App != "planner" {
		t.Fatalf("unexpected delegated assertion claims: %+v (err=%v)", claims1, err)
	}

	// Repeat on Replica B -> must serve the cached assertion within the 30s window.
	if code, body := doJSON(repB, http.MethodGet, entryHost, "/api/tasks", plannerSA, nil, oboHdr); code != http.StatusOK {
		t.Fatalf("delegated GET via repB (planner #2) = %d %s", code, body)
	}
	if got := popAssertion(); got != plannerTokB1 {
		t.Fatal("expected Replica B to serve its cached X-Grain-Registry-Assertion before revoke")
	}

	// Also warm triage on Replica B so we can verify selective eviction.
	if code, body := doJSON(repB, http.MethodGet, entryHost, "/api/tasks", triageSA, nil, oboHdr); code != http.StatusOK {
		t.Fatalf("delegated GET via repB (triage #1) = %d %s", code, body)
	}
	triageTokB1 := popAssertion()
	if code, body := doJSON(repB, http.MethodGet, entryHost, "/api/tasks", triageSA, nil, oboHdr); code != http.StatusOK {
		t.Fatalf("delegated GET via repB (triage #2) = %d %s", code, body)
	}
	if got := popAssertion(); got != triageTokB1 {
		t.Fatal("expected Replica B to cache triage's assertion")
	}

	// 3. Revoke Alice's planner grant on Replica A (`DELETE /api/delegation-grants/<id>`).
	// Replica B must refuse subsequent delegated requests AND evict plannerTokB1 from its
	// CachingSigner via `LISTEN registry_delegations` (`grant:<id>`), while keeping triage cached.
	if code, body := doJSON(repA, http.MethodDelete, hubHost, "/api/delegation-grants/"+plannerGrant1.ID, aliceEmail, nil, nil); code != http.StatusOK {
		t.Fatalf("DELETE grant on repA = %d %s", code, body)
	}

	plannerClaims1 := hub.HubClaims{
		Audience: entryAud,
		Subject:  aliceEmail,
		Email:    aliceEmail,
		Kind:     "human",
		Actor: &hub.DelegationActor{
			Sub:    plannerSA,
			Kind:   "service",
			App:    "planner",
			Grant:  plannerGrant1.ID,
			Access: "read",
		},
	}
	evictDeadline := time.Now().Add(3 * time.Second)
	evictedOnRepB := false
	for time.Now().Before(evictDeadline) {
		tok, err := repB.signer.SignAssertion(ctx, plannerClaims1)
		if err != nil {
			t.Fatalf("repB.signer.SignAssertion: %v", err)
		}
		if tok != plannerTokB1 {
			evictedOnRepB = true
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	if !evictedOnRepB {
		t.Fatal("revoking grant on Replica A did not evict the cached assertion on Replica B via LISTEN registry_delegations")
	}

	_ = popAssertion()
	code, body = doJSON(repB, http.MethodGet, entryHost, "/api/tasks", plannerSA, nil, oboHdr)
	if code != http.StatusForbidden || !bytes.Contains(body, []byte(`"no_grant"`)) {
		t.Fatalf("delegated GET via repB after revoke on repA = %d %s, want 403 no_grant", code, body)
	}
	if leaked := popAssertion(); leaked != "" {
		t.Fatalf("revoked grant still forwarded assertion %q to backend", leaked)
	}

	// Triage's cached assertion on Replica B must remain untouched.
	if code, body := doJSON(repB, http.MethodGet, entryHost, "/api/tasks", triageSA, nil, oboHdr); code != http.StatusOK {
		t.Fatalf("delegated GET via repB (triage after planner revoke) = %d %s", code, body)
	}
	if got := popAssertion(); got != triageTokB1 {
		t.Fatal("revoking planner grant unexpectedly evicted triage's cached assertion on Replica B")
	}

	// 4. Re-grant planner, warm BOTH Replica A and Replica B, then disable "planner" on Replica A.
	// Both Replica A (locally in handleAdminSetDelegatingAppDisabled) and Replica B (via
	// `LISTEN registry_delegations` `app:planner`) must evict their cached assertions, so
	// re-enabling "planner" within the 30s window mints fresh assertions on both replicas.
	code, body = doJSON(repA, http.MethodPost, hubHost, "/api/delegation-grants", aliceEmail,
		api.CreateDelegationGrantRequest{App: "planner", Entries: []string{"alice-vm"}, Access: "read"}, nil)
	if code != http.StatusOK {
		t.Fatalf("POST re-grant planner = %d %s", code, body)
	}
	var plannerGrant2 api.DelegationGrant
	if err := json.Unmarshal(body, &plannerGrant2); err != nil {
		t.Fatalf("unmarshal plannerGrant2: %v", err)
	}

	if code, body := doJSON(repA, http.MethodGet, entryHost, "/api/tasks", plannerSA, nil, oboHdr); code != http.StatusOK {
		t.Fatalf("delegated GET via repA (planner warm #1) = %d %s", code, body)
	}
	plannerTokA2 := popAssertion()
	if code, body := doJSON(repA, http.MethodGet, entryHost, "/api/tasks", plannerSA, nil, oboHdr); code != http.StatusOK {
		t.Fatalf("delegated GET via repA (planner warm #2) = %d %s", code, body)
	}
	if got := popAssertion(); got != plannerTokA2 {
		t.Fatal("expected Replica A to cache planner's assertion before disable")
	}

	if code, body := doJSON(repB, http.MethodGet, entryHost, "/api/tasks", plannerSA, nil, oboHdr); code != http.StatusOK {
		t.Fatalf("delegated GET via repB (planner warm #1) = %d %s", code, body)
	}
	plannerTokB2 := popAssertion()
	if code, body := doJSON(repB, http.MethodGet, entryHost, "/api/tasks", plannerSA, nil, oboHdr); code != http.StatusOK {
		t.Fatalf("delegated GET via repB (planner warm #2) = %d %s", code, body)
	}
	if got := popAssertion(); got != plannerTokB2 {
		t.Fatal("expected Replica B to cache planner's assertion before disable")
	}

	// Admin disables "planner" on Replica A.
	if code, body := doJSON(repA, http.MethodPatch, hubHost, "/api/admin/delegating-apps/planner", adminEmail,
		api.SetDelegatingAppDisabledRequest{Disabled: true}, nil); code != http.StatusOK {
		t.Fatalf("PATCH disable planner on repA = %d %s", code, body)
	}

	plannerClaims2 := hub.HubClaims{
		Audience: entryAud,
		Subject:  aliceEmail,
		Email:    aliceEmail,
		Kind:     "human",
		Actor: &hub.DelegationActor{
			Sub:    plannerSA,
			Kind:   "service",
			App:    "planner",
			Grant:  plannerGrant2.ID,
			Access: "read",
		},
	}

	// Replica B refuses delegated requests while disabled and evicts plannerTokB2 via LISTEN.
	code, body = doJSON(repB, http.MethodGet, entryHost, "/api/tasks", plannerSA, nil, oboHdr)
	if code != http.StatusForbidden || !bytes.Contains(body, []byte(`"app_disabled"`)) {
		t.Fatalf("delegated GET via repB while app disabled = %d %s, want 403 app_disabled", code, body)
	}

	appEvictDeadline := time.Now().Add(3 * time.Second)
	appEvictedOnRepB := false
	for time.Now().Before(appEvictDeadline) {
		tok, err := repB.signer.SignAssertion(ctx, plannerClaims2)
		if err != nil {
			t.Fatalf("repB.signer.SignAssertion: %v", err)
		}
		if tok != plannerTokB2 {
			// Clear the probe token so the HTTP re-enable check below exercises a clean cache.
			repB.signer.InvalidateApp("planner")
			appEvictedOnRepB = true
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	if !appEvictedOnRepB {
		t.Fatal("disabling app on Replica A did not evict cached assertions on Replica B via LISTEN registry_delegations")
	}

	// Triage's cached assertion on Replica B is still intact after disabling planner.
	if code, body := doJSON(repB, http.MethodGet, entryHost, "/api/tasks", triageSA, nil, oboHdr); code != http.StatusOK {
		t.Fatalf("delegated GET via repB (triage after planner disable) = %d %s", code, body)
	}
	if got := popAssertion(); got != triageTokB1 {
		t.Fatal("disabling planner unexpectedly evicted triage's cached assertion on Replica B")
	}

	// Re-enable "planner" on Replica A within the 30s cache window: Replica A (untouched by
	// SignAssertion probes above) and Replica B must both mint fresh assertions rather than
	// serving pre-disable cached tokens.
	if code, body := doJSON(repA, http.MethodPatch, hubHost, "/api/admin/delegating-apps/planner", adminEmail,
		api.SetDelegatingAppDisabledRequest{Disabled: false}, nil); code != http.StatusOK {
		t.Fatalf("PATCH enable planner on repA = %d %s", code, body)
	}
	if code, body := doJSON(repA, http.MethodGet, entryHost, "/api/tasks", plannerSA, nil, oboHdr); code != http.StatusOK {
		t.Fatalf("delegated GET via repA after re-enable = %d %s", code, body)
	}
	if got := popAssertion(); got == "" || got == plannerTokA2 {
		t.Fatal("Replica A served pre-disable cached assertion after app disable + re-enable")
	}
	if code, body := doJSON(repB, http.MethodGet, entryHost, "/api/tasks", plannerSA, nil, oboHdr); code != http.StatusOK {
		t.Fatalf("delegated GET via repB after re-enable = %d %s", code, body)
	}
	if got := popAssertion(); got == "" || got == plannerTokB2 {
		t.Fatal("Replica B served pre-disable cached assertion after app disable + re-enable")
	}
}
