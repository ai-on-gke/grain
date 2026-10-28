package model_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/bwsalmon/grain/pkg/model"
	"github.com/bwsalmon/grain/pkg/model/sqlite/sqlitetest"
)

// fileWithOrigin files task id as parent's proposal (or nobody's, for
// ""), naming origin the way propose_task's filing path does: the
// origin model.ExplicitOrigin returned, "" to inherit.
func fileWithOrigin(t *testing.T, store *model.Store, ctx context.Context, id, parent string, origin model.WorkOrigin) {
	t.Helper()
	task := model.Task{
		ID: id, Intent: model.IntentImplement, Title: id,
		Origin:     model.Origin{Attribution: model.Attribution{Actor: bot}, Reason: model.ReasonDirect},
		Binding:    model.BindingDirective,
		WorkOrigin: origin,
	}
	if parent != "" {
		task.Links = []model.Link{{Kind: model.LinkProposedBy, Target: parent}}
	}
	if err := store.PutTask(ctx, task); err != nil {
		t.Fatalf("filing %s: %v", id, err)
	}
}

// setOrigins writes origin_ref directly: the shape rows had before
// putTask walked to the root.
func setOrigins(t *testing.T, db *sql.DB, ctx context.Context, origins map[string]string) {
	t.Helper()
	for id, origin := range origins {
		if _, err := db.ExecContext(ctx, "UPDATE `task` SET `origin_ref` = ? WHERE `id` = ?", origin, id); err != nil {
			t.Fatal(err)
		}
	}
}

func wantOrigins(t *testing.T, store *model.Store, ctx context.Context, want map[string]model.WorkOrigin) {
	t.Helper()
	for id, w := range want {
		got, err := store.GetTask(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if got == nil {
			t.Errorf("task %s is gone", id)
			continue
		}
		if got.WorkOrigin != w {
			t.Errorf("task %s origin = %q, want %q", id, got.WorkOrigin, w)
		}
	}
}

// A proposed-by cycle is never written, but a store holding one must
// still open, and work filed from inside it must still be filed: the walk
// stops where it finds the cycle.
func TestAProposedByCycleNeitherHangsTheRepairNorAProposal(t *testing.T) {
	store, db := sqlitetest.Open(t)
	ctx := context.Background()
	fileWithOrigin(t, store, ctx, "70", "", "")
	fileWithOrigin(t, store, ctx, "71", "70", "")
	fileWithOrigin(t, store, ctx, "72", "71", "")
	// 70 filed by 72: 70 -> 71 -> 72 -> 70, every origin naming a task
	// in the cycle.
	if _, err := db.ExecContext(ctx,
		"INSERT INTO `task_link` (`task_id`, `kind`, `target`, `blocks`) VALUES ('70', ?, '72', 0)",
		string(model.LinkProposedBy)); err != nil {
		t.Fatal(err)
	}
	setOrigins(t, db, ctx, map[string]string{"70": "", "71": "task:72", "72": "task:71"})
	if err := store.Init(ctx); err != nil {
		t.Fatal(err)
	}
	fileWithOrigin(t, store, ctx, "73", "72", "")
	if got, _ := store.GetTask(ctx, "73"); got.WorkOrigin.Task() == "" {
		t.Errorf("a proposal from inside the cycle has origin %q, want a task in it", got.WorkOrigin)
	}
	// A second open finds the same rows and writes nothing new.
	before := originsOf(t, db, ctx)
	if err := store.Init(ctx); err != nil {
		t.Fatal(err)
	}
	if after := originsOf(t, db, ctx); !sameOrigins(before, after) {
		t.Errorf("second open moved origins: %v -> %v", before, after)
	}
}

// A task whose origin names a task that is no longer stored keeps it --
// a task that is not stored is taken as a root -- and so does work it
// files.
func TestAnOriginNamingADeletedTaskIsKept(t *testing.T) {
	store, db := sqlitetest.Open(t)
	ctx := context.Background()
	fileWithOrigin(t, store, ctx, "80", "", "")
	fileWithOrigin(t, store, ctx, "81", "80", "")
	fileWithOrigin(t, store, ctx, "82", "81", "")
	setOrigins(t, db, ctx, map[string]string{"81": "", "82": "task:81"})
	// 81 is deleted by hand, its own proposed-by link left behind.
	if _, err := db.ExecContext(ctx, "DELETE FROM `task` WHERE `id` = '81'"); err != nil {
		t.Fatal(err)
	}
	if err := store.Init(ctx); err != nil {
		t.Fatal(err)
	}
	wantOrigins(t, store, ctx, map[string]model.WorkOrigin{"80": "", "82": "task:81"})
	fileWithOrigin(t, store, ctx, "83", "82", "")
	wantOrigins(t, store, ctx, map[string]model.WorkOrigin{"83": "task:81"})
}

func originsOf(t *testing.T, db *sql.DB, ctx context.Context) map[string]string {
	t.Helper()
	rows, err := db.QueryContext(ctx, "SELECT `id`, `origin_ref` FROM `task`")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var id, origin string
		if err := rows.Scan(&id, &origin); err != nil {
			t.Fatal(err)
		}
		out[id] = origin
	}
	return out
}

func sameOrigins(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}
