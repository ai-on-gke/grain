package sqlite_test

// Branch creation's own store tests (bwsalmon/agents#638), against a real
// embedded SQLite database -- the same discipline release_store_test.go
// already holds every Store method to.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/bwsalmon/grain/pkg/model"
)

func TestCreateBranchRejectsAnInvalidName(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	for _, name := range []string{"", "/leading-slash", "trailing-slash/", "double//slash", "dotdot..name", "-leading-dash"} {
		if _, err := store.CreateBranch(ctx, widgets, name, now); !errors.Is(err, model.ErrInvalidBranchName) {
			t.Errorf("CreateBranch(%q): got %v, want ErrInvalidBranchName", name, err)
		}
	}
}

func TestCreateBranchRecordsAPendingBranch(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	b, err := store.CreateBranch(ctx, widgets, "feature/foo", now)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if b.Repo != widgets || b.Name != "feature/foo" || b.Status != model.BranchPending || b.CreatedAt != now {
		t.Fatalf("got %+v", b)
	}
}

func TestCreateBranchTwiceForAPendingNameKeepsOneRow(t *testing.T) {
	t.Parallel()
	// The second add of a name still waiting on the reconciler is the
	// same request again, not another one: it answers with the row that
	// is already there and records nothing new.
	store, _, ctx := openStore(t)
	first, fresh, err := store.RequestBranch(ctx, widgets, "myfeat", now)
	if err != nil || !fresh {
		t.Fatalf("first request: fresh=%v, err=%v", fresh, err)
	}
	second, fresh, err := store.RequestBranch(ctx, widgets, "myfeat", now.Add(time.Minute))
	if err != nil {
		t.Fatalf("second request: %v", err)
	}
	if fresh || second.ID != first.ID || second.Status != model.BranchPending || !second.CreatedAt.Equal(now) {
		t.Fatalf("second request: fresh=%v, got %+v, want the first row %+v back", fresh, second, first)
	}
	got, err := store.ListBranches(ctx, widgets)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d branches, want 1: %+v", len(got), got)
	}
}

func TestCreateBranchTwiceForADoneNameKeepsOneRow(t *testing.T) {
	t.Parallel()
	for _, mark := range []func(*model.Store, int64) error{
		func(s *model.Store, id int64) error { return s.MarkBranchCreated(context.Background(), id) },
		func(s *model.Store, id int64) error { return s.MarkBranchAdopted(context.Background(), id) },
	} {
		store, _, ctx := openStore(t)
		b, err := store.CreateBranch(ctx, widgets, "myfeat", now)
		if err != nil {
			t.Fatal(err)
		}
		if err := mark(store, b.ID); err != nil {
			t.Fatal(err)
		}
		again, fresh, err := store.RequestBranch(ctx, widgets, "myfeat", now)
		if err != nil || fresh || again.ID != b.ID || again.Status == model.BranchPending {
			t.Fatalf("got fresh=%v %+v, %v; want the settled row back", fresh, again, err)
		}
		if got, err := store.ListBranches(ctx, widgets); err != nil || len(got) != 1 {
			t.Fatalf("got %+v, %v; want one row", got, err)
		}
		if pending, err := store.PendingBranches(ctx); err != nil || len(pending) != 0 {
			t.Fatalf("got %+v, %v; want nothing pending", pending, err)
		}
	}
}

func TestCreateBranchAgainRetriesAFailedReconcile(t *testing.T) {
	t.Parallel()
	// A row whose reconcile failed stays pending, so the reconciler
	// tries it again on its own; asking again clears the stale error so
	// the page says a retry is underway, and still adds no second row.
	store, _, ctx := openStore(t)
	b, err := store.CreateBranch(ctx, widgets, "myfeat", now)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.MarkBranchError(ctx, b.ID, "boom"); err != nil {
		t.Fatal(err)
	}
	again, fresh, err := store.RequestBranch(ctx, widgets, "myfeat", now)
	if err != nil || fresh || again.ID != b.ID || again.Status != model.BranchPending || again.LastError != "" {
		t.Fatalf("got fresh=%v %+v, %v; want the same row, pending, error cleared", fresh, again, err)
	}
	pending, err := store.PendingBranches(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].ID != b.ID || pending[0].LastError != "" {
		t.Fatalf("got %+v, want only the retried row pending", pending)
	}
}

func TestPendingBranchesReturnsOnlyPendingOnes(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	a, err := store.CreateBranch(ctx, widgets, "a", now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateBranch(ctx, widgets, "b", now); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkBranchCreated(ctx, a.ID); err != nil {
		t.Fatalf("mark created: %v", err)
	}

	pending, err := store.PendingBranches(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].Name != "b" {
		t.Fatalf("got %+v, want only %q pending", pending, "b")
	}
}

func TestMarkBranchCreatedClearsLastError(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	b, err := store.CreateBranch(ctx, widgets, "myfeat", now)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.MarkBranchError(ctx, b.ID, "boom"); err != nil {
		t.Fatalf("mark error: %v", err)
	}
	if err := store.MarkBranchCreated(ctx, b.ID); err != nil {
		t.Fatalf("mark created: %v", err)
	}

	got, err := store.ListBranches(ctx, widgets)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Status != model.BranchCreated || got[0].LastError != "" {
		t.Fatalf("got %+v, want created with no error", got)
	}
}

func TestMarkBranchAdoptedIsTerminalAndClearsLastError(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	b, err := store.CreateBranch(ctx, widgets, "myfeat", now)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.MarkBranchError(ctx, b.ID, "boom"); err != nil {
		t.Fatalf("mark error: %v", err)
	}
	if err := store.MarkBranchAdopted(ctx, b.ID); err != nil {
		t.Fatalf("mark adopted: %v", err)
	}

	got, err := store.ListBranches(ctx, widgets)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Status != model.BranchAdopted || got[0].LastError != "" {
		t.Fatalf("got %+v, want adopted with no error", got)
	}
	// Adopted is as done as created: the reconciler owes it nothing
	// further.
	pending, err := store.PendingBranches(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Fatalf("got %+v, want nothing left pending", pending)
	}
}

func TestListBranchesIsScopedToItsOwnRepo(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)
	if _, err := store.CreateBranch(ctx, widgets, "myfeat", now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateBranch(ctx, gadgets, "myfeat", now); err != nil {
		t.Fatal(err)
	}
	got, err := store.ListBranches(ctx, widgets)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Repo != widgets {
		t.Fatalf("got %+v, want exactly one branch scoped to %s", got, widgets)
	}
}
