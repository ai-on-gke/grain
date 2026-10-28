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

// bwsalmon/agents#417: nothing in cmd/grain or pkg/orchestrator was named
// (or did) anything like restart/resume/recover/crash before this file --
// every other test here drives RunCycle/RunDispatch to completion within
// one process and one store connection, so none of them can tell a clean
// finish apart from a process that died partway through and got restarted
// against the same on-disk store. The two tests below kill a real sqlite
// connection (the only state a restart cannot recover -- see
// openStoreInDir) at the two points RunDispatch's own doc comments
// already worry about: after dispatch.Cycle's own durable write but
// before anything else runs at all, and after a capability is minted but
// before revokeAll/FinishRun ever get to record what became of it.
package orchestrator_test

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/capability/gcpkey"
	"github.com/ai-on-gke/grain/pkg/dispatch"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
)

// openStoreInDir is openStore (this package's own helper, above) with dir
// named explicitly and Close left to the caller, rather than t.TempDir()
// and t.Cleanup -- what a literal restart needs: the same on-disk sqlite
// file, reopened by a second, independent connection only after the first
// one has actually closed, the same shape cmd/grain's own openStore/
// TestOpenStorePersistsAcrossReopen already establish for a graind
// restart.
func openStoreInDir(t *testing.T, dir string) (*model.Store, *sql.DB) {
	t.Helper()
	db, err := sqlite.Open(sqlite.DefaultConfig(dir))
	if err != nil {
		t.Fatalf("opening embedded sqlite in %s: %v", dir, err)
	}
	store := model.New(db)
	if err := store.Init(context.Background()); err != nil {
		db.Close()
		t.Fatalf("applying schema: %v", err)
	}
	return store, db
}

// TestRestartAfterACrashMidRunDoesNotDoubleDispatchOrLoseTheRun is the
// earliest possible crash point in a cycle: dispatch.Cycle's own
// StartRun has already committed (its own doc comment: "the store write
// is already durable by the time a Dispatch is returned"), but the
// process dies before reconcileDispatch ever calls runOne/RunDispatch for
// it -- no capability materialized, no agent run, no branch pushed. A
// restart must neither redispatch the same task into a second, concurrent
// run nor forget the run the crashed process already recorded.
func TestRestartAfterACrashMidRunDoesNotDoubleDispatchOrLoseTheRun(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	store1, db1 := openStoreInDir(t, dir)
	ctx := context.Background()

	task := dispatchTask(t, ctx, store1, "t1")

	dispatches, err := dispatch.Cycle(ctx, store1, model.Limits{Workers: 1}, baseTime)
	if err != nil {
		t.Fatalf("Cycle: %v", err)
	}
	if len(dispatches) != 1 {
		t.Fatalf("dispatches = %+v, want exactly one", dispatches)
	}

	// The process dies right here: closing this connection is as much of
	// "the process is gone" as one test process can simulate against a
	// real embedded database -- only the sqlite file on disk survives,
	// the same state a killed graind would leave behind.
	if err := db1.Close(); err != nil {
		t.Fatalf("closing the first connection: %v", err)
	}

	// Restart: a fresh process opens the very same store.
	store2, db2 := openStoreInDir(t, dir)
	defer db2.Close()

	// task_state reads a run with no finished_at as 'running' regardless
	// of which process is (or isn't) actually driving it, so task_ready
	// still excludes t1 -- this must hold across the restart exactly as
	// it holds within one process (dispatch_test.go's own
	// TestCycleLeavesAnAlreadyRunningTaskAlone).
	again, err := dispatch.Cycle(ctx, store2, model.Limits{Workers: 1}, baseTime.Add(time.Minute))
	if err != nil {
		t.Fatalf("Cycle after restart: %v", err)
	}
	if len(again) != 0 {
		t.Fatalf("Cycle after restart dispatched %+v, want nothing: %s must not be "+
			"double-dispatched while its crashed run is still marked live", again, task.ID)
	}

	// Nor is the original run lost: exactly the one row the crashed
	// process wrote is still there, and the slot it claimed still reads
	// occupied by it.
	if n, err := store2.Attempts(ctx, task.ID); err != nil || n != 1 {
		t.Fatalf("Attempts after restart = %d (%v), want exactly 1: the crashed run must not have vanished", n, err)
	}
	occupied, err := store2.LiveRunCount(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if occupied != 1 {
		t.Fatalf("live runs after restart = %d, want exactly 1, still held by the crashed run", occupied)
	}
	if st, err := store2.State(ctx, task.ID); err != nil || st != model.StateRunning {
		t.Fatalf("state after restart = %q (%v), want %q: neither finished nor forgotten", st, err, model.StateRunning)
	}
}

// fakeGCPMinter is a gcpkey.Minter local to this file: gcpkey's own
// fakeMinter (gcpkey_test.go) is unexported to that package, and this
// scenario is about pkg/orchestrator's own contract with a Reaper
// capability across a crash, not about gcpkey's internals, which its own
// tests already cover in isolation. CreateKey stamps every key with now
// rather than the real clock, so a test controls exactly how old a key
// reads to Reap without sleeping.
type fakeGCPMinter struct {
	now  time.Time
	keys map[string]gcpkey.KeyInfo
}

func (m *fakeGCPMinter) CreateKey(ctx context.Context, account string) (string, string, error) {
	id := fmt.Sprintf("key-%d", len(m.keys)+1)
	m.keys[id] = gcpkey.KeyInfo{ID: id, CreatedAt: m.now}
	return id, `{"private_key_id":"` + id + `"}`, nil
}

func (m *fakeGCPMinter) DeleteKey(ctx context.Context, account, keyID string) error {
	if _, ok := m.keys[keyID]; !ok {
		return fmt.Errorf("no such key: %s", keyID)
	}
	delete(m.keys, keyID)
	return nil
}

func (m *fakeGCPMinter) ListKeys(ctx context.Context, account string) ([]gcpkey.KeyInfo, error) {
	out := make([]gcpkey.KeyInfo, 0, len(m.keys))
	for _, k := range m.keys {
		out = append(out, k)
	}
	return out, nil
}

// fakeMinterCreds resolves exactly the credential names it is seeded
// with -- model.CredentialResolver's own contract, restated locally since
// no exported fake implements it.
type fakeMinterCreds struct{ material map[string]string }

func (c fakeMinterCreds) Resolve(ctx context.Context, name string) (string, error) {
	v, ok := c.material[name]
	if !ok {
		return "", fmt.Errorf("no such credential: %s", name)
	}
	return v, nil
}

// TestCrashAfterMaterializingACapabilityDoesNotLeakItPastReapsWindow is
// model.Reaper's own doc comment made concrete: "a controller crash
// between mint and store write". RunDispatch's prepareCapabilities calls
// model.MaterializeGrants -- minting a real resource -- long before
// RunDispatch ever reaches revokeAll or FinishRun; dispatch.Cycle's own
// Run carries no Leases (see dispatch.Cycle), and nothing in RunDispatch
// ever calls StartRun a second time to add one once Materialize succeeds,
// so a process killed in between leaves no store record of the mint at
// all -- there is nothing to "restore" from the store on restart. The
// only thing that ever notices a resource minted this way is Reap,
// consulting the cloud API's own listing rather than any store record
// (Reaper's own doc comment) -- this proves that backstop actually closes
// the window within its own declared bound, rather than merely being
// documented as intending to, and that it does not fire early against a
// key that may still be in honest use.
func TestCrashAfterMaterializingACapabilityDoesNotLeakItPastReapsWindow(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	store1, db1 := openStoreInDir(t, dir)
	ctx := context.Background()

	dispatchTask(t, ctx, store1, "t1", model.Grant{Capability: "gcp-key", Via: model.GrantByLabel})
	d := dispatch.Dispatch{TaskID: "t1", RunID: "r1", Attempt: 1}
	startRun(t, ctx, store1, d, baseTime)
	task, err := store1.GetTask(ctx, "t1")
	if err != nil || task == nil {
		t.Fatalf("reading task: %v", err)
	}

	minter := &fakeGCPMinter{now: baseTime, keys: map[string]gcpkey.KeyInfo{}}
	provider := gcpkey.NewProvider(gcpkey.Config{
		ServiceAccountEmail: "agent@example-project.iam.gserviceaccount.com",
		ProjectID:           "example-project",
	})
	provider.NewMinter = func(ctx context.Context, credentialJSON string) (gcpkey.Minter, error) { return minter, nil }
	creds := fakeMinterCreds{material: map[string]string{gcpkey.DefaultMinterCredential: "minter-material"}}
	reg := model.NewCapabilityRegistry(provider)
	cc := model.CapabilityContext{Task: *task, Run: model.Run{ID: d.RunID, TaskID: d.TaskID}, Now: baseTime, Credentials: creds}

	// Exactly what RunDispatch's own prepareCapabilities does before ever
	// running the agent: resolve, then materialize. A real key now
	// exists.
	resolved, err := model.ResolveGrants(ctx, reg, cc)
	if err != nil {
		t.Fatalf("ResolveGrants: %v", err)
	}
	materialized, err := model.MaterializeGrants(ctx, reg, cc, resolved)
	if err != nil {
		t.Fatalf("MaterializeGrants: %v", err)
	}
	if len(materialized) != 1 || materialized[0].Materialization.Lease == nil {
		t.Fatalf("materialized = %+v, want exactly one lease", materialized)
	}
	keyID := materialized[0].Materialization.Lease.Resource
	if len(minter.keys) != 1 {
		t.Fatalf("fake GCP has %d keys, want exactly one minted", len(minter.keys))
	}

	// The process dies right here: revokeAll and FinishRun -- the only
	// things that would otherwise revoke this key or finish the run --
	// never run.
	if err := db1.Close(); err != nil {
		t.Fatalf("closing the first connection: %v", err)
	}

	// Restart: a fresh process, the same store, and -- since Reap
	// consults GCP's own listing rather than the store -- the same fake
	// GCP project the crashed process minted into, exactly as a real
	// restart would still see the real key it left behind.
	store2, db2 := openStoreInDir(t, dir)
	defer db2.Close()

	// The run is still marked live; it is not lost by the crash.
	occupied, err := store2.LiveRunCount(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if occupied != 1 {
		t.Fatalf("live runs after restart = %v, want exactly one: the crashed run must not be lost", occupied)
	}

	// A boundary behind the still-live run must leave its key alone: the
	// shared trailing edge protects work regardless of how long it runs.
	deleted, err := provider.Reap(ctx, creds, baseTime.Add(-time.Minute))
	if err != nil {
		t.Fatalf("Reap (behind live run): %v", err)
	}
	if len(deleted) != 0 {
		t.Fatalf("Reap behind the live run deleted %v, want nothing yet", deleted)
	}
	if _, stillThere := minter.keys[keyID]; !stillThere {
		t.Fatal("the key was reaped while its run was still live")
	}

	// Once recovery has made that run non-live, an idle deployment's boundary
	// advances past the key and the leak is reclaimed promptly.
	deleted, err = provider.Reap(ctx, creds, baseTime.Add(time.Hour))
	if err != nil {
		t.Fatalf("Reap (past trailing edge): %v", err)
	}
	if len(deleted) != 1 || deleted[0] != keyID {
		t.Fatalf("Reap past the trailing edge deleted %v, want exactly [%s]", deleted, keyID)
	}
	if _, stillThere := minter.keys[keyID]; stillThere {
		t.Fatal("the leaked key was not reaped behind the trailing edge")
	}
}
