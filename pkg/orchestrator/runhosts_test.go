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

package orchestrator_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/dispatch"
	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
	"github.com/ai-on-gke/grain/pkg/runhost"
)

// listedHosts is a runhost.Driver over a fixed listing that records what
// it was told to destroy.
type listedHosts struct {
	mu        sync.Mutex
	hosts     []runhost.Info
	destroyed []string
	// failDestroys is how many Destroy calls fail, the way a kubectl
	// delete the API server timed out does, before they succeed again.
	failDestroys int
}

func (l *listedHosts) Create(context.Context, runhost.Spec) error { return nil }
func (l *listedHosts) List(context.Context, string) ([]runhost.Info, error) {
	return l.hosts, nil
}
func (l *listedHosts) Destroy(_ context.Context, name string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.failDestroys > 0 {
		l.failDestroys--
		return errors.New("Error from server (Timeout): the server was unable to return a response in the time allotted")
	}
	l.destroyed = append(l.destroyed, name)
	return nil
}

// TestRecoverRunHostsByClaimNotByPrefix: a run host no live row claims is
// freed; a live row driven in a run host that is gone is finished with
// the listing's reason, not the generic one; a live row that never had a
// run host is left to RecoverOrphanedRuns; and a run host the adoption
// seam takes is left running.
func TestRecoverRunHostsByClaimNotByPrefix(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	gone := filedTask(t, ctx, store, "gone", repo)
	kept := filedTask(t, ctx, store, "kept", repo)
	local := filedTask(t, ctx, store, "local", repo)
	startRun(t, ctx, store, dispatch.Dispatch{TaskID: gone.ID, RunID: "gone-1", Attempt: 1}, baseTime)
	startRun(t, ctx, store, dispatch.Dispatch{TaskID: kept.ID, RunID: "kept-1", Attempt: 1}, baseTime)
	startRun(t, ctx, store, dispatch.Dispatch{TaskID: local.ID, RunID: "local-1", Attempt: 1}, baseTime)
	for _, run := range []string{"gone-1", "kept-1"} {
		if err := store.SetSpoolCursor(ctx, run, 0, baseTime); err != nil {
			t.Fatal(err)
		}
	}
	label := func(run string) runhost.Labels {
		return runhost.Labels{Deployment: "d", Run: run, Protocol: runhost.Protocol, Spool: t.TempDir()}
	}
	driver := &listedHosts{hosts: []runhost.Info{
		{Name: "rh-kept", Running: true, Labels: label("kept-1")},
		{Name: "rh-stray", Running: true, Labels: label("finished-long-ago")},
	}}
	var adopted []string
	plan, err := orchestrator.RecoverRunHosts(ctx, store, client, &runhost.Host{Driver: driver, Deployment: "d"}, baseTime.Add(time.Minute),
		func(a runhost.Adoption, cursor int64) bool { adopted = append(adopted, a.Claim.Run); return true })
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(adopted, ",") != "kept-1" || len(plan.Adopt) != 1 || plan.Adopt[0].Gap != time.Minute {
		t.Fatalf("adopted %v, plan %+v", adopted, plan)
	}
	if strings.Join(driver.destroyed, ",") != "rh-stray" {
		t.Fatalf("destroyed %v, want only the unclaimed run host", driver.destroyed)
	}
	runs, err := store.LiveRuns(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var live []string
	for _, r := range runs {
		live = append(live, r.ID)
	}
	if strings.Join(live, ",") != "kept-1,local-1" && strings.Join(live, ",") != "local-1,kept-1" {
		t.Fatalf("live runs after the pass = %v, want the adopted one and the one with no run host", live)
	}
	st, err := store.State(ctx, gone.ID)
	if err != nil || st != model.StateQueued {
		t.Fatalf("the task whose run host is gone is %q (%v), want queued", st, err)
	}
}

// Without the adoption seam, this build finishes a matched run rather
// than leaving a run host running that nothing tails.
func TestRecoverRunHostsWithoutAdoptionFinishesTheMatch(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	task := filedTask(t, ctx, store, "t", model.RepoRef{Owner: "acme", Name: "widgets"})
	startRun(t, ctx, store, dispatch.Dispatch{TaskID: task.ID, RunID: "t-1", Attempt: 1}, baseTime)
	driver := &listedHosts{hosts: []runhost.Info{{Name: "rh", Running: true,
		Labels: runhost.Labels{Deployment: "d", Run: "t-1", Protocol: runhost.Protocol, Spool: t.TempDir()}}}}
	if _, err := orchestrator.RecoverRunHosts(ctx, store, client, &runhost.Host{Driver: driver, Deployment: "d"}, baseTime.Add(time.Minute), nil); err != nil {
		t.Fatal(err)
	}
	if strings.Join(driver.destroyed, ",") != "rh" {
		t.Fatalf("destroyed %v", driver.destroyed)
	}
	if n, err := store.LiveRunCount(ctx); err != nil || n != 0 {
		t.Fatalf("live runs = %v (%v), want none", n, err)
	}
}

// The pass sweeps the spool and staging roots: a spool whose run host is
// gone -- here one created but never started, its env file still there --
// is removed with the run it belonged to, and so is a staging entry older
// than every live run; the adopted run's spool and staging, and anything
// written after the pass began, are left alone.
func TestRecoverRunHostsSweepsSpoolsNoRunHostOwns(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	_, client := newSim(t, "acme", "widgets", "main")
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	gone := filedTask(t, ctx, store, "gone", repo)
	kept := filedTask(t, ctx, store, "kept", repo)
	startRun(t, ctx, store, dispatch.Dispatch{TaskID: gone.ID, RunID: "gone-1", Attempt: 1}, baseTime)
	startRun(t, ctx, store, dispatch.Dispatch{TaskID: kept.ID, RunID: "kept-1", Attempt: 1}, baseTime)
	for _, run := range []string{"gone-1", "kept-1"} {
		if err := store.SetSpoolCursor(ctx, run, 0, baseTime); err != nil {
			t.Fatal(err)
		}
	}
	root := t.TempDir()
	host := &runhost.Host{Deployment: "d", SpoolRoot: filepath.Join(root, "spool"), StagingRoot: filepath.Join(root, "staging")}
	now := baseTime.Add(time.Minute)
	entry := func(dir, name string, at time.Time) string {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, "env"), []byte("CLAUDE_CODE_OAUTH_TOKEN=secret\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if !at.IsZero() {
			if err := os.Chtimes(path, at, at); err != nil {
				t.Fatal(err)
			}
		}
		return path
	}
	goneSpool := entry(host.SpoolRoot, "run-gone-1-111", baseTime.Add(10*time.Second))
	straySpool := entry(host.SpoolRoot, "run-finished-long-ago-222", baseTime.Add(-time.Hour))
	keptSpool := entry(host.SpoolRoot, "run-kept-1-333", baseTime.Add(10*time.Second))
	freshSpool := entry(host.SpoolRoot, "run-started-meanwhile-444", time.Time{})
	oldStaging := entry(host.StagingRoot, "home-555", baseTime.Add(-time.Hour))
	keptStaging := entry(host.StagingRoot, "home-666", baseTime.Add(10*time.Second))

	host.Driver = &listedHosts{hosts: []runhost.Info{
		{Name: "rh-kept", Running: true, Labels: runhost.Labels{Deployment: "d", Run: "kept-1", Protocol: runhost.Protocol, Spool: keptSpool}},
	}}
	if _, err := orchestrator.RecoverRunHosts(ctx, store, client, host, now,
		func(runhost.Adoption, int64) bool { return true }); err != nil {
		t.Fatal(err)
	}
	exists := func(path string) bool { _, err := os.Stat(path); return err == nil }
	for _, path := range []string{goneSpool, straySpool, oldStaging} {
		if exists(path) {
			t.Errorf("%s is still there; no run host owns it", path)
		}
	}
	for _, path := range []string{keptSpool, freshSpool, keptStaging} {
		if !exists(path) {
			t.Errorf("%s was removed", path)
		}
	}
}

// TestSweepOrphanRunHostsReapsWhatAFailedDestroyLeftBehind: a run host
// whose run finished but whose own Destroy failed -- a Succeeded pod the
// API server timed out deleting -- is freed by the next periodic sweep,
// spool and all, while run hosts a live run or an in-flight run claims
// are spared, and a sweep whose Destroy fails keeps the spool for the
// next one.
func TestSweepOrphanRunHostsReapsWhatAFailedDestroyLeftBehind(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	done := filedTask(t, ctx, store, "done", repo)
	live := filedTask(t, ctx, store, "live", repo)
	startRun(t, ctx, store, dispatch.Dispatch{TaskID: done.ID, RunID: "done-1", Attempt: 1}, baseTime)
	startRun(t, ctx, store, dispatch.Dispatch{TaskID: live.ID, RunID: "live-1", Attempt: 1}, baseTime)
	if err := store.FinishRun(ctx, "done-1", baseTime.Add(time.Minute), "succeeded", ""); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	host := &runhost.Host{Deployment: "d", SpoolRoot: filepath.Join(root, "spool"), StagingRoot: filepath.Join(root, "staging")}
	spool := func(run string) string {
		path := filepath.Join(host.SpoolRoot, "run-"+run+"-1")
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
		return path
	}
	info := func(name, run string, running bool) runhost.Info {
		return runhost.Info{Name: name, Running: running,
			Labels: runhost.Labels{Deployment: "d", Run: run, Protocol: runhost.Protocol, Spool: spool(run)}}
	}
	doneHost := info("rh-done", "done-1", false)
	driver := &listedHosts{hosts: []runhost.Info{
		doneHost,
		info("rh-live", "live-1", true),
		info("rh-flying", "flying-1", true),
	}}
	host.Driver = driver
	inFlight := func() []string { return []string{"flying-1"} }
	now := baseTime.Add(time.Hour)

	driver.failDestroys = 1
	reaped, err := orchestrator.SweepOrphanRunHosts(ctx, store, host, inFlight, now)
	if err == nil || reaped != 0 {
		t.Fatalf("sweep with a failing Destroy = (%d, %v), want (0, an error)", reaped, err)
	}
	if _, err := os.Stat(doneHost.Labels.Spool); err != nil {
		t.Fatalf("the spool of a run host that could not be destroyed was removed: %v", err)
	}

	reaped, err = orchestrator.SweepOrphanRunHosts(ctx, store, host, inFlight, now)
	if err != nil || reaped != 1 {
		t.Fatalf("next sweep = (%d, %v), want (1, nil)", reaped, err)
	}
	if strings.Join(driver.destroyed, ",") != "rh-done" {
		t.Fatalf("destroyed %v, want only the finished run's run host", driver.destroyed)
	}
	if _, err := os.Stat(doneHost.Labels.Spool); !os.IsNotExist(err) {
		t.Fatalf("the finished run host's spool is still there (%v)", err)
	}
	for _, run := range []string{"live-1", "flying-1"} {
		if _, err := os.Stat(filepath.Join(host.SpoolRoot, "run-"+run+"-1")); err != nil {
			t.Errorf("the spool of claimed run %s was removed: %v", run, err)
		}
	}
}

// Sandbox reconciliation runs the sweep when it has a run host, and
// counts what it freed as orphans reaped.
func TestReconcileSandboxesSweepsOrphanRunHosts(t *testing.T) {
	t.Parallel()
	store, ctx := openStore(t)
	driver := &listedHosts{hosts: []runhost.Info{{Name: "rh-stray", Running: false,
		Labels: runhost.Labels{Deployment: "d", Run: "finished-long-ago", Protocol: runhost.Protocol}}}}
	deps := orchestrator.Deps{Store: store, RunHost: &runhost.Host{Driver: driver, Deployment: "d"}}
	if err := orchestrator.ReconcileSandboxes(ctx, deps, baseTime); err != nil {
		t.Fatal(err)
	}
	if strings.Join(driver.destroyed, ",") != "rh-stray" {
		t.Fatalf("destroyed %v, want the unclaimed run host", driver.destroyed)
	}
}
