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

package reconcile

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/ai-on-gke/grain/pkg/registry"
	"github.com/ai-on-gke/grain/pkg/registry/api"
	"github.com/ai-on-gke/grain/pkg/registry/store"
)

var (
	pgOnce sync.Once
	pgDir  string
	pgURL  string
	pgDB   *sql.DB
	pgErr  error
	pgSeq  int
	pgMu   sync.Mutex
)

func TestMain(m *testing.M) {
	code := m.Run()
	if pgDB != nil {
		_ = pgDB.Close()
	}
	if pgDir != "" {
		pgCtl := findPGBin("pg_ctl")
		if pgCtl != "" {
			dataDir := filepath.Join(pgDir, "data")
			if os.Geteuid() == 0 {
				_ = exec.Command("runuser", "-u", "postgres", "--", pgCtl, "-D", dataDir, "-m", "immediate", "-w", "stop").Run()
			} else {
				_ = exec.Command(pgCtl, "-D", dataDir, "-m", "immediate", "-w", "stop").Run()
			}
		}
		_ = os.RemoveAll(pgDir)
	}
	os.Exit(code)
}

func findPGBin(name string) string {
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	matches, _ := filepath.Glob(filepath.Join("/usr/lib/postgresql", "*", "bin", name))
	if len(matches) > 0 {
		sort.Strings(matches)
		return matches[len(matches)-1]
	}
	return ""
}

func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	pgOnce.Do(func() {
		initdb := findPGBin("initdb")
		pgCtl := findPGBin("pg_ctl")
		if initdb == "" || pgCtl == "" {
			pgErr = errors.New("postgresql binaries not found")
			return
		}
		dir, err := os.MkdirTemp("/tmp", "grain-reconcile-pg-*")
		if err != nil {
			pgErr = err
			return
		}
		pgDir = dir
		dataDir := filepath.Join(dir, "data")
		sockDir := filepath.Join(dir, "sock")
		_ = os.MkdirAll(sockDir, 0o755)

		asPostgres := os.Geteuid() == 0
		if asPostgres {
			if u, err := user.Lookup("postgres"); err == nil {
				_ = exec.Command("chown", "-R", u.Username, dir).Run()
			} else {
				asPostgres = false
			}
		}
		runCmd := func(bin string, args ...string) error {
			var cmd *exec.Cmd
			if asPostgres {
				cmd = exec.Command("runuser", append([]string{"-u", "postgres", "--", bin}, args...)...)
			} else {
				cmd = exec.Command(bin, args...)
			}
			var out bytes.Buffer
			cmd.Stdout = &out
			cmd.Stderr = &out
			if err := cmd.Run(); err != nil {
				return fmt.Errorf("%s %v: %w (%s)", bin, args, err, strings.TrimSpace(out.String()))
			}
			return nil
		}
		if err := runCmd(initdb, "-D", dataDir, "-U", "postgres", "-A", "trust", "--no-instructions", "--encoding=UTF8", "--locale=C"); err != nil {
			pgErr = err
			return
		}
		opts := fmt.Sprintf("-F -c listen_addresses= -c unix_socket_directories=%s -c max_connections=100", sockDir)
		if err := runCmd(pgCtl, "-D", dataDir, "-l", filepath.Join(dir, "postgres.log"), "-o", opts, "-w", "start"); err != nil {
			pgErr = err
			return
		}
		q := make(url.Values)
		q.Set("host", sockDir)
		q.Set("sslmode", "disable")
		u := &url.URL{
			Scheme:   "postgres",
			User:     url.User("postgres"),
			Host:     "localhost",
			Path:     "/postgres",
			RawQuery: q.Encode(),
		}
		pgURL = u.String()
		pgDB, pgErr = sql.Open("pgx", pgURL)
	})
	if pgErr != nil {
		if os.Getenv("GRAIN_REQUIRE_POSTGRES") != "" {
			t.Fatalf("newTestStore: %v", pgErr)
		}
		t.Skipf("skipping postgres test: %v", pgErr)
	}

	pgMu.Lock()
	pgSeq++
	seq := pgSeq
	pgMu.Unlock()

	dbName := fmt.Sprintf("rec_test_%d_%d", time.Now().UnixNano(), seq)
	ctx := context.Background()
	if _, err := pgDB.ExecContext(ctx, fmt.Sprintf(`CREATE DATABASE %s`, dbName)); err != nil {
		t.Fatalf("CREATE DATABASE %s: %v", dbName, err)
	}
	u, _ := url.Parse(pgURL)
	u.Path = "/" + dbName

	st, err := store.Open(ctx, u.String(), store.DefaultThresholds())
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() {
		_ = st.Close()
		_, _ = pgDB.ExecContext(context.Background(), fmt.Sprintf(`DROP DATABASE IF EXISTS %s WITH (FORCE)`, dbName))
	})
	return st
}

type fakeCompute struct {
	mu         sync.Mutex
	backendURL string
	vms        map[string]registry.InstanceStatus
	specs      map[string]registry.InstanceSpec
	stopCalls  int
	delCalls   int
}

func newFakeCompute(backendURL string) *fakeCompute {
	return &fakeCompute{
		backendURL: backendURL,
		vms:        make(map[string]registry.InstanceStatus),
		specs:      make(map[string]registry.InstanceSpec),
	}
}

func (f *fakeCompute) CreateInstance(_ context.Context, spec registry.InstanceSpec) (*registry.InstanceHandle, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	vmID := "grain-vm-" + spec.Name
	lbls := make(map[string]string, len(spec.Labels)+3)
	for k, v := range spec.Labels {
		lbls[k] = v
	}
	lbls["grain-registry"] = "grain.example.com"
	lbls["grain-entry"] = spec.Name
	lbls["grain-generation"] = strconv.FormatInt(spec.Generation, 10)

	f.specs[vmID] = spec
	f.vms[vmID] = registry.InstanceStatus{
		InstanceID: vmID,
		Name:       vmID,
		Zone:       spec.Zone,
		State:      "RUNNING",
		InternalIP: "10.128.0.50",
		BackendURL: f.backendURL,
		Labels:     lbls,
	}
	return &registry.InstanceHandle{
		InstanceID: vmID,
		Zone:       spec.Zone,
		InternalIP: "10.128.0.50",
		BackendURL: f.backendURL,
	}, nil
}

func (f *fakeCompute) StopInstance(_ context.Context, zone, instanceID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stopCalls++
	if vm, ok := f.vms[instanceID]; ok {
		vm.State = "TERMINATED"
		f.vms[instanceID] = vm
	}
	return nil
}

func (f *fakeCompute) DeleteInstance(_ context.Context, zone, instanceID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.delCalls++
	delete(f.vms, instanceID)
	return nil
}

func (f *fakeCompute) GetInstanceStatus(_ context.Context, zone, instanceID string) (*registry.InstanceStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	vm, ok := f.vms[instanceID]
	if !ok {
		return &registry.InstanceStatus{
			InstanceID: instanceID,
			Name:       instanceID,
			Zone:       zone,
			State:      "NOT_FOUND",
		}, nil
	}
	cp := vm
	return &cp, nil
}

func (f *fakeCompute) ListInstances(_ context.Context) ([]registry.InstanceStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []registry.InstanceStatus
	for _, vm := range f.vms {
		out = append(out, vm)
	}
	return out, nil
}

type fakeResources struct {
	mu           sync.Mutex
	st           *store.Store
	provisioned  map[string]registry.InstanceResourceBundle
	fenced       map[string]bool
	purged       map[string]bool
	failNextProv bool
}

func newFakeResources(st *store.Store) *fakeResources {
	return &fakeResources{
		st:          st,
		provisioned: make(map[string]registry.InstanceResourceBundle),
		fenced:      make(map[string]bool),
		purged:      make(map[string]bool),
	}
}

func (f *fakeResources) Provision(ctx context.Context, spec registry.InstanceResourceSpec) (*registry.InstanceResourceBundle, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failNextProv {
		f.failNextProv = false
		return nil, errors.New("simulated cloud resource provisioning failure")
	}
	sqlRow, dbName, err := f.st.AllocateSQLInstance(ctx, spec.EntryID, spec.Name)
	if err != nil {
		return nil, err
	}
	bundle := registry.InstanceResourceBundle{
		ServiceAccountEmail: fmt.Sprintf("grain-sa-%s@test-proj.iam.gserviceaccount.com", spec.Name),
		SQLInstance:         sqlRow.Name,
		SQLConnectionName:   sqlRow.ConnectionName,
		SQLPrivateIP:        sqlRow.PrivateIP,
		DatabaseName:        dbName,
		SecretPrefix:        fmt.Sprintf("grain-inst-%s-", spec.Name),
		GCSBucket:           "grain-state-bucket",
		GCSManagedFolder:    fmt.Sprintf("instances/%s/", spec.Name),
	}
	f.provisioned[spec.Name] = bundle
	delete(f.fenced, spec.Name)
	return &bundle, nil
}

func (f *fakeResources) Teardown(_ context.Context, spec registry.InstanceResourceSpec, _ registry.InstanceResourceBundle, retainDB bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if retainDB {
		f.fenced[spec.Name] = true
	} else {
		f.purged[spec.Name] = true
		delete(f.provisioned, spec.Name)
	}
	return nil
}

func (f *fakeResources) GrantSSH(_ context.Context, _, _, _ string) error  { return nil }
func (f *fakeResources) RevokeSSH(_ context.Context, _, _, _ string) error { return nil }

type fakeConfigRepos struct {
	mu       sync.Mutex
	ensured  map[string]string
	archived map[string]bool
}

func newFakeConfigRepos() *fakeConfigRepos {
	return &fakeConfigRepos{
		ensured:  make(map[string]string),
		archived: make(map[string]bool),
	}
}

func (f *fakeConfigRepos) EnsureRepo(_ context.Context, entryName, _ string) (*registry.ConfigRepoBundle, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	repoURL := fmt.Sprintf("git@github.com:acme-configs/grain-cfg-%s.git", entryName)
	f.ensured[entryName] = repoURL
	return &registry.ConfigRepoBundle{
		RepoURL:   repoURL,
		DeployKey: "ssh-ed25519-private-key",
	}, nil
}

func (f *fakeConfigRepos) ArchiveRepo(_ context.Context, entryName string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.archived[entryName] = true
	delete(f.ensured, entryName)
	return nil
}

func TestReconcilerLeaderLease(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	st.SetNow(func() time.Time { return now })

	rec1, err := New(Options{
		Store:    st,
		HolderID: "replica-1",
		LeaseTTL: 15 * time.Second,
		Now:      func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("New rec1: %v", err)
	}
	rec2, err := New(Options{
		Store:    st,
		HolderID: "replica-2",
		LeaseTTL: 15 * time.Second,
		Now:      func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("New rec2: %v", err)
	}

	// replica-1 acquires the lease.
	if err := rec1.RunOnce(ctx); err != nil {
		t.Fatalf("rec1.RunOnce: %v", err)
	}
	if !rec1.IsLeader() {
		t.Fatal("expected rec1 to be leader")
	}

	// replica-2 runs while replica-1's lease is active: must be a no-op and not leader.
	if err := rec2.RunOnce(ctx); err != nil {
		t.Fatalf("rec2.RunOnce: %v", err)
	}
	if rec2.IsLeader() {
		t.Fatal("expected rec2 NOT to be leader while rec1 holds valid lease")
	}

	// Advance clock past 15s LeaseTTL without renewing rec1: rec2 takes over!
	now = now.Add(16 * time.Second)
	if err := rec2.RunOnce(ctx); err != nil {
		t.Fatalf("rec2.RunOnce after expiry: %v", err)
	}
	if !rec2.IsLeader() {
		t.Fatal("expected rec2 to become leader after rec1 lease expired")
	}
}

func TestReconcilerCreatingReplacingDeletingPurgeAndPolling(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	st.SetNow(func() time.Time { return now })

	if err := st.SeedShapes(ctx, []store.ShapeRow{
		{Name: "medium", MachineType: "e2-standard-8", BootDiskGB: 100, QuotaUnits: 1},
		{Name: "large", MachineType: "n2-standard-16", BootDiskGB: 200, QuotaUnits: 2},
	}); err != nil {
		t.Fatalf("SeedShapes: %v", err)
	}
	if err := st.SeedSQLInstances(ctx, []store.SQLInstanceRow{
		{Name: "sql-pool-01", ConnectionName: "test-proj:us-central1:sql-pool-01", PrivateIP: "10.8.0.5", MaxDatabases: 50},
	}); err != nil {
		t.Fatalf("SeedSQLInstances: %v", err)
	}

	// Mock hosted VM HTTP server (`/healthz` and `/api/registry/status`).
	var (
		vmMu          sync.Mutex
		vmHealthy     = false
		vmStatusReply = api.InstanceStatusResponse{
			GrainVersion:       "v1.1.0",
			GrainCommit:        "abc1234",
			Health:             api.HealthOK,
			ActiveTasks:        2,
			AwaitingReplyTasks: 1,
			Scopes: []api.HeartbeatScope{
				{Repo: "acme/platform", PathPrefix: "services/billing", Role: api.RolePrimary},
			},
		}
	)
	vmSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		vmMu.Lock()
		healthy := vmHealthy
		reply := vmStatusReply
		vmMu.Unlock()

		switch r.URL.Path {
		case "/healthz":
			if !healthy {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ok"))
		case "/api/registry/status":
			if !healthy {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(reply)
		default:
			http.NotFound(w, r)
		}
	}))
	defer vmSrv.Close()

	comp := newFakeCompute(vmSrv.URL)
	res := newFakeResources(st)
	cfgRepos := newFakeConfigRepos()

	cfg := registry.Config{
		Host:           "grain.example.com",
		InstanceDomain: "instances.grain.example.com",
		Compute: registry.ComputeConfig{
			Project:       "test-proj",
			Region:        "us-central1",
			Zones:         []string{"us-central1-a"},
			VMPrefix:      "grain-vm-",
			SAPrefix:      "grain-sa-",
			BootImage:     "projects/test-proj/global/images/grain-base",
			DrainTimeout:  10 * time.Millisecond,
			HealthTimeout: 10 * time.Minute,
			RegistryID:    "grain.example.com",
		},
	}

	rec, err := New(Options{
		Config:            cfg,
		Store:             st,
		Compute:           comp,
		Resources:         res,
		ConfigRepos:       cfgRepos,
		HolderID:          "leader-1",
		HTTPClient:        vmSrv.Client(),
		DrainTimeout:      10 * time.Millisecond,
		HealthTimeout:     10 * time.Minute,
		StepByStepReplace: true,
		Now:               func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("New Reconciler: %v", err)
	}

	// 1. Create `alice-vm` in `creating` state.
	listedTrue := true
	entry, err := st.CreateEntry(ctx, store.CreateEntryInput{
		Name:       "alice-vm",
		Kind:       api.KindInstance,
		OwnerEmail: "alice@example.com",
		Shape:      "medium",
		Zone:       "us-central1-a",
		Listed:     &listedTrue,
	})
	if err != nil {
		t.Fatalf("CreateEntry alice-vm: %v", err)
	}

	// Pass 1: VM is provisioned and placement is stored, but `/healthz` is still 503 -> stays in `creating`.
	if err := rec.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce 1 (creating, unhealthy): %v", err)
	}
	eAfterCreate1, err := st.GetEntryByID(ctx, entry.ID)
	if err != nil {
		t.Fatalf("GetEntryByID: %v", err)
	}
	if eAfterCreate1.LifecycleState != api.StateCreating {
		t.Fatalf("expected entry to stay in creating while /healthz returns 503, got %s", eAfterCreate1.LifecycleState)
	}
	if eAfterCreate1.GCEInstanceID != "grain-vm-alice-vm" || eAfterCreate1.BackendURL != vmSrv.URL || eAfterCreate1.SQLInstance != "sql-pool-01" || eAfterCreate1.DBName != "grain_alice_vm" {
		t.Fatalf("unexpected placement after provisioning: %+v", eAfterCreate1)
	}
	if eAfterCreate1.ConfigRepo != "git@github.com:acme-configs/grain-cfg-alice-vm.git" {
		t.Fatalf("unexpected config_repo: %q", eAfterCreate1.ConfigRepo)
	}

	// Pass 2: `/healthz` and `/api/registry/status` become healthy -> transitions to `running` and records reported scopes.
	vmMu.Lock()
	vmHealthy = true
	vmMu.Unlock()

	if err := rec.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce 2 (creating -> running): %v", err)
	}
	eRunning, err := st.GetEntryByID(ctx, entry.ID)
	if err != nil {
		t.Fatalf("GetEntryByID running: %v", err)
	}
	if eRunning.LifecycleState != api.StateRunning {
		t.Fatalf("expected lifecycle_state=running, got %s (msg=%q)", eRunning.LifecycleState, eRunning.StatusReason)
	}
	if eRunning.GrainVersion != "v1.1.0" || eRunning.GrainCommit != "abc1234" || eRunning.ActiveTasks != 2 || eRunning.AwaitingReplyTasks != 1 {
		t.Fatalf("unexpected status fields on running entry: %+v", eRunning)
	}
	if len(eRunning.Scopes) != 1 || eRunning.Scopes[0].Repo != "acme/platform" || eRunning.Scopes[0].PathPrefix != "services/billing" || eRunning.Scopes[0].Source != string(api.SourceReported) {
		t.Fatalf("expected reported scope acme/platform:services/billing, got %+v", eRunning.Scopes)
	}

	// 2. 15 s `/api/registry/status` polling updates `entries` and `entry_scopes(source='reported')`.
	vmMu.Lock()
	vmStatusReply = api.InstanceStatusResponse{
		GrainVersion:       "v1.1.0",
		GrainCommit:        "abc1234",
		Health:             api.HealthOK,
		ActiveTasks:        5,
		AwaitingReplyTasks: 0,
		Scopes: []api.HeartbeatScope{
			{Repo: "acme/platform", PathPrefix: "services/payments", Role: api.RolePrimary},
		},
	}
	vmMu.Unlock()
	now = now.Add(15 * time.Second)
	if err := rec.PollRunningInstances(ctx); err != nil {
		t.Fatalf("PollRunningInstances: %v", err)
	}
	ePolled, err := st.GetEntryByID(ctx, entry.ID)
	if err != nil {
		t.Fatalf("GetEntryByID polled: %v", err)
	}
	if ePolled.ActiveTasks != 5 || len(ePolled.Scopes) != 1 || ePolled.Scopes[0].PathPrefix != "services/payments" {
		t.Fatalf("expected polled active_tasks=5 and scope services/payments, got tasks=%d scopes=%+v", ePolled.ActiveTasks, ePolled.Scopes)
	}

	// 3. Step-by-step `replacing` (`draining` -> `deleting_vm` -> `creating_vm` -> `probing` -> `running`).
	vmMu.Lock()
	vmStatusReply.GrainVersion = "v1.2.0"
	vmMu.Unlock()

	if _, err := st.RequestEntryReplace(ctx, ePolled.ID, ePolled.Version, "large", "v1.2.0"); err != nil {
		t.Fatalf("RequestEntryReplace: %v", err)
	}

	// While ActiveTasks > 0 within DrainTimeout, reconciler holds in `draining` without stopping the VM.
	if err := rec.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce replace draining wait: %v", err)
	}
	eDraining, _ := st.GetEntryByID(ctx, entry.ID)
	if eDraining.LifecycleState != api.StateReplacing || eDraining.ReplaceStep != string(api.ReplaceStepDraining) {
		t.Fatalf("expected replacing/draining while ActiveTasks > 0, got %s/%s", eDraining.LifecycleState, eDraining.ReplaceStep)
	}

	// Step 1: ActiveTasks drops to 0 -> draining -> deleting_vm (stops old VM).
	vmMu.Lock()
	vmStatusReply.ActiveTasks = 0
	vmMu.Unlock()
	if err := rec.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce replace step 1 (draining): %v", err)
	}
	eStep1, _ := st.GetEntryByID(ctx, entry.ID)
	if eStep1.LifecycleState != api.StateReplacing || eStep1.ReplaceStep != string(api.ReplaceStepDeletingVM) {
		t.Fatalf("expected replacing/deleting_vm after step 1, got %s/%s", eStep1.LifecycleState, eStep1.ReplaceStep)
	}

	// Step 2: deleting_vm -> creating_vm (deletes old VM).
	if err := rec.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce replace step 2 (deleting_vm): %v", err)
	}
	eStep2, _ := st.GetEntryByID(ctx, entry.ID)
	if eStep2.LifecycleState != api.StateReplacing || eStep2.ReplaceStep != string(api.ReplaceStepCreatingVM) {
		t.Fatalf("expected replacing/creating_vm after step 2, got %s/%s", eStep2.LifecycleState, eStep2.ReplaceStep)
	}

	// Step 3: creating_vm -> probing (creates new VM with shape=large and preserved SQL DB).
	if err := rec.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce replace step 3 (creating_vm): %v", err)
	}
	eStep3, _ := st.GetEntryByID(ctx, entry.ID)
	if eStep3.LifecycleState != api.StateReplacing || eStep3.ReplaceStep != string(api.ReplaceStepProbing) {
		t.Fatalf("expected replacing/probing after step 3, got %s/%s", eStep3.LifecycleState, eStep3.ReplaceStep)
	}

	// Step 4: probing -> running (health check passes, switches to running, clears replace_step).
	if err := rec.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce replace step 4 (probing -> running): %v", err)
	}
	eReplaced, _ := st.GetEntryByID(ctx, entry.ID)
	if eReplaced.LifecycleState != api.StateRunning || eReplaced.ReplaceStep != "" {
		t.Fatalf("expected running with empty replace_step after step 4, got %s/%s", eReplaced.LifecycleState, eReplaced.ReplaceStep)
	}
	if eReplaced.Shape != "large" || eReplaced.GrainVersion != "v1.2.0" || eReplaced.SQLInstance != "sql-pool-01" || eReplaced.DBName != "grain_alice_vm" {
		t.Fatalf("expected shape=large, grain_version=v1.2.0, and preserved SQL placement after replace, got %+v", eReplaced)
	}

	// 4. `deleting` -> `deleted` (deletes VM, fences DB with retainDB=true, sets 7d db_purge_after and 30d reserved_until).
	if _, err := st.BeginDeleteEntry(ctx, eReplaced.ID, eReplaced.Version, false); err != nil {
		t.Fatalf("BeginDeleteEntry: %v", err)
	}
	if err := rec.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce deleting: %v", err)
	}
	eDeleted, err := st.GetEntryByID(ctx, entry.ID)
	if err != nil {
		t.Fatalf("GetEntryByID deleted: %v", err)
	}
	if eDeleted.LifecycleState != api.StateDeleted || eDeleted.DeletedAt == nil || eDeleted.DBPurgeAfter == nil || eDeleted.ReservedUntil == nil {
		t.Fatalf("expected deleted state with DeletedAt, DBPurgeAfter, and ReservedUntil set, got %+v", eDeleted)
	}
	res.mu.Lock()
	isFenced := res.fenced["alice-vm"]
	isPurged := res.purged["alice-vm"]
	res.mu.Unlock()
	if !isFenced || isPurged {
		t.Fatalf("expected alice-vm resources to be fenced (retainDB=true) and not yet purged, got fenced=%v purged=%v", isFenced, isPurged)
	}

	// 5. Delayed `purge_after` / `db_purge_after` cleanup after 7 days:
	// Advance clock by 2 days (still within 7d retention): must NOT purge yet.
	now = now.Add(48 * time.Hour)
	if err := rec.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce before db_purge_after: %v", err)
	}
	res.mu.Lock()
	stillNotPurged := !res.purged["alice-vm"]
	res.mu.Unlock()
	if !stillNotPurged {
		t.Fatal("expected alice-vm resources NOT to be purged before db_purge_after")
	}

	// Advance clock past 7-day `db_purge_after` (e.g. +6 more days = 8 days total):
	// Reconciler archives config repo, calls `Teardown(..., retainDB=false)`, and clears `db_purge_after`.
	now = now.Add(6 * 24 * time.Hour)
	if err := rec.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce after db_purge_after: %v", err)
	}
	res.mu.Lock()
	nowPurged := res.purged["alice-vm"]
	res.mu.Unlock()
	cfgRepos.mu.Lock()
	repoArchived := cfgRepos.archived["alice-vm"]
	cfgRepos.mu.Unlock()
	if !nowPurged || !repoArchived {
		t.Fatalf("expected alice-vm resources and config repo to be purged after db_purge_after, got purged=%v repoArchived=%v", nowPurged, repoArchived)
	}

	eAfterResourcePurge, err := st.GetEntryByID(ctx, entry.ID)
	if err != nil {
		t.Fatalf("expected tombstone row to remain until 30d reserved_until, got %v", err)
	}
	if eAfterResourcePurge.DBPurgeAfter != nil || eAfterResourcePurge.SQLInstance != "" {
		t.Fatalf("expected DBPurgeAfter and SQLInstance cleared on tombstone after resource purge, got %+v", eAfterResourcePurge)
	}

	// Advance clock past 30-day `reserved_until`: tombstone row is deleted completely.
	now = now.Add(25 * 24 * time.Hour)
	if err := rec.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce after reserved_until: %v", err)
	}
	if _, err := st.GetEntryByID(ctx, entry.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expected expired tombstone row to be removed after reserved_until, got %v", err)
	}
}

func TestReconcilerHealthTimeoutOrphansAndExpiredReplicas(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	st.SetNow(func() time.Time { return now })

	if err := st.SeedShapes(ctx, []store.ShapeRow{
		{Name: "medium", MachineType: "e2-standard-8", BootDiskGB: 100, QuotaUnits: 1},
	}); err != nil {
		t.Fatalf("SeedShapes: %v", err)
	}
	if err := st.SeedSQLInstances(ctx, []store.SQLInstanceRow{
		{Name: "sql-pool-01", ConnectionName: "test-proj:us-central1:sql-pool-01", PrivateIP: "10.8.0.5", MaxDatabases: 50},
	}); err != nil {
		t.Fatalf("SeedSQLInstances: %v", err)
	}

	// Unhealthy backend server (always 503).
	unhealthySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer unhealthySrv.Close()

	comp := newFakeCompute(unhealthySrv.URL)
	res := newFakeResources(st)

	rec, err := New(Options{
		Config: registry.Config{
			Compute: registry.ComputeConfig{
				RegistryID: "grain.example.com",
			},
		},
		Store:         st,
		Compute:       comp,
		Resources:     res,
		HolderID:      "leader-1",
		HTTPClient:    unhealthySrv.Client(),
		HealthTimeout: 5 * time.Minute,
		Now:           func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("New Reconciler: %v", err)
	}

	// 1. Expired `replicas` cleanup (`DeleteExpiredReplicas` cascading to `tunnel_sessions`).
	connEntry, err := st.CreateEntry(ctx, store.CreateEntryInput{
		Name:       "conn-1",
		Kind:       api.KindConnected,
		OwnerEmail: "alice@example.com",
	})
	if err != nil {
		t.Fatalf("CreateEntry conn-1: %v", err)
	}
	if err := st.UpsertReplica(ctx, "rep-stale", "10.0.0.1:8082"); err != nil {
		t.Fatalf("UpsertReplica rep-stale: %v", err)
	}
	if _, err := st.AcquireTunnelSession(ctx, connEntry.ID, "rep-stale", "198.51.100.1:5555"); err != nil {
		t.Fatalf("AcquireTunnelSession: %v", err)
	}
	// Advance clock by 45s (past 30s DefaultReplicaTTL) and upsert a fresh replica `rep-live`.
	now = now.Add(45 * time.Second)
	if err := st.UpsertReplica(ctx, "rep-live", "10.0.0.2:8082"); err != nil {
		t.Fatalf("UpsertReplica rep-live: %v", err)
	}

	// 2. Create `stuck-vm` whose `/healthz` fails until `health_timeout` (5m) elapses.
	stuckEntry, err := st.CreateEntry(ctx, store.CreateEntryInput{
		Name:       "stuck-vm",
		Kind:       api.KindInstance,
		OwnerEmail: "bob@example.com",
		Shape:      "medium",
		Zone:       "us-central1-a",
	})
	if err != nil {
		t.Fatalf("CreateEntry stuck-vm: %v", err)
	}

	// First RunOnce: cleans up `rep-stale` (and its cascaded tunnel session) and provisions `stuck-vm`.
	if err := rec.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce 1: %v", err)
	}
	if _, err := st.GetActiveTunnelSession(ctx, connEntry.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expected tunnel session on expired replica rep-stale to be cleaned up, got %v", err)
	}

	// Advance clock past 5m `HealthTimeout`: `stuck-vm` transitions to `failed`.
	now = now.Add(6 * time.Minute)
	if err := rec.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce 2 (health timeout): %v", err)
	}
	stuckUpdated, err := st.GetEntryByID(ctx, stuckEntry.ID)
	if err != nil {
		t.Fatalf("GetEntryByID stuck-vm: %v", err)
	}
	if stuckUpdated.LifecycleState != api.StateFailed || !strings.Contains(stuckUpdated.StatusReason, "timed out") {
		t.Fatalf("expected stuck-vm to transition to failed on health_timeout, got state=%s msg=%q", stuckUpdated.LifecycleState, stuckUpdated.StatusReason)
	}

	// 3. Orphan VM detection and stale-generation cleanup:
	// Inject:
	// (a) `grain-vm-mystery` with `grain-entry=mystery` (no matching DB row -> reported in `rec.Orphans()`, NOT deleted)
	// (b) `grain-vm-old-stuck` with `grain-entry=stuck-vm` and `grain-generation=0` (stale generation -> auto-deleted!)
	comp.mu.Lock()
	comp.vms["grain-vm-mystery"] = registry.InstanceStatus{
		InstanceID: "grain-vm-mystery",
		Name:       "grain-vm-mystery",
		Zone:       "us-central1-a",
		State:      "RUNNING",
		InternalIP: "10.128.0.99",
		Labels: map[string]string{
			"grain-registry":   "grain.example.com",
			"grain-entry":      "mystery",
			"grain-generation": "1",
		},
	}
	comp.vms["grain-vm-old-stuck"] = registry.InstanceStatus{
		InstanceID: "grain-vm-old-stuck",
		Name:       "grain-vm-old-stuck",
		Zone:       "us-central1-a",
		State:      "RUNNING",
		InternalIP: "10.128.0.98",
		Labels: map[string]string{
			"grain-registry":   "grain.example.com",
			"grain-entry":      "stuck-vm",
			"grain-generation": "0",
		},
	}
	comp.mu.Unlock()

	if err := rec.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce 3 (orphans): %v", err)
	}

	orphans := rec.Orphans()
	if len(orphans) != 1 || orphans[0].InstanceID != "grain-vm-mystery" {
		t.Fatalf("expected Orphans() to report only grain-vm-mystery, got %+v", orphans)
	}
	comp.mu.Lock()
	_, mysteryStillExists := comp.vms["grain-vm-mystery"]
	_, staleGenStillExists := comp.vms["grain-vm-old-stuck"]
	comp.mu.Unlock()
	if !mysteryStillExists {
		t.Fatal("expected unknown orphan VM grain-vm-mystery to be reported, NOT auto-deleted")
	}
	if staleGenStillExists {
		t.Fatal("expected stale-generation VM grain-vm-old-stuck to be automatically deleted")
	}
}

func TestReconcilerStopStartAutoWakeAndScheduledFleetUpgrades(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	// Start at 01:00 UTC (outside maintenance window 02:00-04:00).
	now := time.Date(2026, 11, 7, 1, 0, 0, 0, time.UTC)
	st.SetNow(func() time.Time { return now })

	if err := st.SeedShapes(ctx, []store.ShapeRow{
		{Name: "standard", MachineType: "e2-standard-4", BootDiskGB: 50, QuotaUnits: 1},
	}); err != nil {
		t.Fatalf("SeedShapes: %v", err)
	}
	if err := st.SeedSQLInstances(ctx, []store.SQLInstanceRow{
		{Name: "sql-pool-01", ConnectionName: "test-proj:us-central1:sql-pool-01", PrivateIP: "10.8.0.5", MaxDatabases: 50},
	}); err != nil {
		t.Fatalf("SeedSQLInstances: %v", err)
	}
	// Give alice enough quota units for 3 VMs.
	if err := st.SetQuota(ctx, "alice@example.com", 10, 20, "admin@example.com"); err != nil {
		t.Fatalf("SetQuota: %v", err)
	}

	var statusMu sync.Mutex
	reportedVersion := "v1.0.0"
	activeTasks := 0

	instanceSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/healthz":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ok"))
		case "/api/registry/status":
			statusMu.Lock()
			ver := reportedVersion
			tasks := activeTasks
			statusMu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(api.InstanceStatusResponse{
				GrainVersion: ver,
				GrainCommit:  "abc1234",
				Health:       api.HealthOK,
				ActiveTasks:  tasks,
			})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer instanceSrv.Close()

	comp := newFakeCompute(instanceSrv.URL)
	res := newFakeResources(st)

	rec, err := New(Options{
		Config: registry.Config{
			Host:           "grain.example.com",
			InstanceDomain: "instances.example.com",
			Compute: registry.ComputeConfig{
				Project:               "test-proj",
				Region:                "us-central1",
				Zones:                 []string{"us-central1-a"},
				DefaultShape:          "standard",
				VMPrefix:              "grain-vm-",
				RegistryID:            "grain.example.com",
				AutoUpgrade:           string(api.AutoUpgradeMinor),
				TargetVersion:         "v1.0.0",
				MaintenanceWindow:     "02:00-04:00",
				MaxConcurrentUpgrades: 1,
			},
			Secrets: registry.SecretsConfig{
				SecretPrefix: "grain-inst-",
			},
		},
		Store:        st,
		Compute:      comp,
		Resources:    res,
		HolderID:     "leader-1",
		HTTPClient:   instanceSrv.Client(),
		DrainTimeout: 5 * time.Minute,
		Now:          func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("New Reconciler: %v", err)
	}

	// Create 3 instances:
	// - vm-minor: inherits fleet AutoUpgrade=minor
	// - vm-notify: overrides AutoUpgrade=notify
	// - vm-off: overrides AutoUpgrade=off
	eMinor, err := st.CreateEntry(ctx, store.CreateEntryInput{
		Name:       "vm-minor",
		Kind:       api.KindInstance,
		OwnerEmail: "alice@example.com",
		Shape:      "standard",
		Zone:       "us-central1-a",
	})
	if err != nil {
		t.Fatalf("CreateEntry vm-minor: %v", err)
	}
	eNotify, err := st.CreateEntry(ctx, store.CreateEntryInput{
		Name:        "vm-notify",
		Kind:        api.KindInstance,
		OwnerEmail:  "alice@example.com",
		Shape:       "standard",
		Zone:        "us-central1-a",
		AutoUpgrade: string(api.AutoUpgradeNotify),
	})
	if err != nil {
		t.Fatalf("CreateEntry vm-notify: %v", err)
	}
	eOff, err := st.CreateEntry(ctx, store.CreateEntryInput{
		Name:        "vm-off",
		Kind:        api.KindInstance,
		OwnerEmail:  "alice@example.com",
		Shape:       "standard",
		Zone:        "us-central1-a",
		AutoUpgrade: string(api.AutoUpgradeOff),
	})
	if err != nil {
		t.Fatalf("CreateEntry vm-off: %v", err)
	}

	// Reconcile creating -> running for all 3 VMs at v1.0.0.
	if err := rec.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce initial creating: %v", err)
	}
	eMinor, _ = st.GetEntryByID(ctx, eMinor.ID)
	if eMinor.LifecycleState != api.StateRunning || eMinor.GrainVersion != "v1.0.0" {
		t.Fatalf("expected vm-minor running at v1.0.0, got %s/%s", eMinor.LifecycleState, eMinor.GrainVersion)
	}

	// 1. Test Stop -> Stopped (draining wait when activeTasks > 0, then stop+delete VM while keeping externalized state) -> Wake/Start -> Running.
	statusMu.Lock()
	activeTasks = 2
	statusMu.Unlock()
	eMinor, err = st.BeginStopEntry(ctx, eMinor.ID, eMinor.Version, "stopping vm-minor")
	if err != nil {
		t.Fatalf("BeginStopEntry: %v", err)
	}
	now = now.Add(10 * time.Second)
	if err := rec.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce stopping while busy: %v", err)
	}
	eMinor, _ = st.GetEntryByID(ctx, eMinor.ID)
	if eMinor.LifecycleState != api.StateStopping {
		t.Fatalf("expected vm-minor to stay in stopping while active_tasks=2 within drain_timeout, got %s", eMinor.LifecycleState)
	}

	// Tasks finish -> next RunOnce stops & deletes VM and transitions to stopped.
	statusMu.Lock()
	activeTasks = 0
	statusMu.Unlock()
	now = now.Add(10 * time.Second)
	if err := rec.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce stopping after drain: %v", err)
	}
	eMinor, _ = st.GetEntryByID(ctx, eMinor.ID)
	if eMinor.LifecycleState != api.StateStopped || eMinor.GCEInstanceID != "" || eMinor.BackendURL != "" {
		t.Fatalf("expected vm-minor stopped with cleared VM placement, got %+v", eMinor)
	}
	if eMinor.SQLInstance != "sql-pool-01" || eMinor.DBName != "grain_vm_minor" || eMinor.ServiceAccount == "" {
		t.Fatalf("expected externalized state preserved while stopped, got %+v", eMinor)
	}

	// Auto-wake transitions stopped -> creating, and reconciler boots a new VM -> running.
	_, didWake, err := st.WakeStoppedEntry(ctx, eMinor.ID, "bob@example.com")
	if err != nil || !didWake {
		t.Fatalf("WakeStoppedEntry: (%v, %v)", didWake, err)
	}
	now = now.Add(10 * time.Second)
	if err := rec.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce waking: %v", err)
	}
	eMinor, _ = st.GetEntryByID(ctx, eMinor.ID)
	if eMinor.LifecycleState != api.StateRunning || eMinor.GCEInstanceID == "" || eMinor.BackendURL == "" {
		t.Fatalf("expected vm-minor back to running after wake, got %+v", eMinor)
	}

	// 2. Test Scheduled Fleet Upgrades:
	// First test major version bump (v1.0.0 -> v2.0.0): `minor` policy must NOT auto-replace on a major bump, only set upgrade_available.
	rec.cfg.Compute.TargetVersion = "v2.0.0"
	now = time.Date(2026, 11, 7, 2, 30, 0, 0, time.UTC) // inside 02:00-04:00 window
	if err := rec.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce major version check: %v", err)
	}
	eMinor, _ = st.GetEntryByID(ctx, eMinor.ID)
	if eMinor.LifecycleState != api.StateRunning || eMinor.GrainVersion != "v1.0.0" || eMinor.UpgradeAvailable != "v2.0.0" {
		t.Fatalf("expected major version bump v2.0.0 not to auto-replace under minor policy, got state=%s ver=%s avail=%s",
			eMinor.LifecycleState, eMinor.GrainVersion, eMinor.UpgradeAvailable)
	}

	// Now set target version to a minor bump `v1.1.0`, but move clock outside maintenance window (01:30 UTC).
	rec.cfg.Compute.TargetVersion = "v1.1.0"
	now = time.Date(2026, 11, 7, 1, 30, 0, 0, time.UTC)
	if err := rec.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce outside maintenance window: %v", err)
	}
	eMinor, _ = st.GetEntryByID(ctx, eMinor.ID)
	eNotify, _ = st.GetEntryByID(ctx, eNotify.ID)
	eOff, _ = st.GetEntryByID(ctx, eOff.ID)
	if eMinor.GrainVersion != "v1.0.0" || eMinor.UpgradeAvailable != "v1.1.0" {
		t.Fatalf("expected vm-minor to wait outside maintenance window with upgrade_available=v1.1.0, got ver=%s avail=%s",
			eMinor.GrainVersion, eMinor.UpgradeAvailable)
	}
	if eNotify.GrainVersion != "v1.0.0" || eNotify.UpgradeAvailable != "v1.1.0" {
		t.Fatalf("expected vm-notify to record upgrade_available=v1.1.0 without replacing, got ver=%s avail=%s",
			eNotify.GrainVersion, eNotify.UpgradeAvailable)
	}
	if eOff.UpgradeAvailable != "" {
		t.Fatalf("expected vm-off to ignore fleet upgrades, got avail=%q", eOff.UpgradeAvailable)
	}

	// Move inside maintenance window (02:15 UTC), but mark instance busy (`activeTasks = 1`): must wait until idle.
	now = time.Date(2026, 11, 7, 2, 15, 0, 0, time.UTC)
	statusMu.Lock()
	activeTasks = 1
	statusMu.Unlock()
	if err := rec.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce inside maintenance window while busy: %v", err)
	}
	eMinor, _ = st.GetEntryByID(ctx, eMinor.ID)
	if eMinor.GrainVersion != "v1.0.0" {
		t.Fatalf("expected busy vm-minor not to auto-upgrade while active_tasks=1, got ver=%s", eMinor.GrainVersion)
	}

	comp.mu.Lock()
	stopsBefore := comp.stopCalls
	comp.mu.Unlock()
	statusMu.Lock()
	activeTasks = 0
	reportedVersion = "v1.0.0"
	statusMu.Unlock()

	// Use stepByStepReplace=true so we can observe the transition to replacing, then switch reportedVersion to v1.1.0 when the new VM boots.
	rec.stepByStepReplace = true
	if err := rec.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce trigger auto-upgrade: %v", err)
	}
	eMinor, _ = st.GetEntryByID(ctx, eMinor.ID)
	if eMinor.LifecycleState != api.StateReplacing || eMinor.TargetVersion != "v1.1.0" {
		t.Fatalf("expected vm-minor to enter replacing for target_version=v1.1.0, got state=%s target=%s",
			eMinor.LifecycleState, eMinor.TargetVersion)
	}

	// Complete the remaining replace steps (deleting_vm -> creating_vm -> probing -> running with v1.1.0).
	rec.stepByStepReplace = false
	statusMu.Lock()
	reportedVersion = "v1.1.0"
	statusMu.Unlock()
	if err := rec.RunOnce(ctx); err != nil {
		t.Fatalf("RunOnce complete auto-upgrade: %v", err)
	}
	eMinor, _ = st.GetEntryByID(ctx, eMinor.ID)
	if eMinor.LifecycleState != api.StateRunning || eMinor.GrainVersion != "v1.1.0" || eMinor.UpgradeAvailable != "" {
		t.Fatalf("expected vm-minor running at v1.1.0 with empty upgrade_available after auto-upgrade, got state=%s ver=%s avail=%q",
			eMinor.LifecycleState, eMinor.GrainVersion, eMinor.UpgradeAvailable)
	}
	comp.mu.Lock()
	stopsAfter := comp.stopCalls
	comp.mu.Unlock()
	if stopsAfter <= stopsBefore {
		t.Fatalf("expected old VM stop during auto-upgrade (before=%d after=%d)", stopsBefore, stopsAfter)
	}
}
