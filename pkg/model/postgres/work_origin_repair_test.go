package postgres_test

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite"
)

// TestOriginRepairScale times Store.Init on a store of 20,000 tasks in
// 2,000 trees, on PostgreSQL and SQLite: with no stale rows, with half
// the trees in the shape putTask wrote before it walked to the root, and
// once more after that store was repaired. It only times -- set
// GRAIN_ORIGIN_REPAIR_SCALE=1 to run it; the numbers are recorded in
// docs/test-runs/2026-10-07-work-tree-origin-repair.md.
func TestOriginRepairScale(t *testing.T) {
	if os.Getenv("GRAIN_ORIGIN_REPAIR_SCALE") == "" {
		t.Skip("set GRAIN_ORIGIN_REPAIR_SCALE=1 to time the repair at scale")
	}
	backends := map[string]func(t *testing.T) (*model.Store, *sql.DB){
		"postgres": func(t *testing.T) (*model.Store, *sql.DB) {
			store, db, _ := openStore(t)
			return store, db
		},
		"sqlite": func(t *testing.T) (*model.Store, *sql.DB) {
			db, err := sqlite.Open(sqlite.DefaultConfig(t.TempDir()))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { db.Close() })
			store := model.New(db)
			if err := store.Init(context.Background()); err != nil {
				t.Fatal(err)
			}
			return store, db
		},
	}
	for _, name := range []string{"sqlite", "postgres"} {
		for _, stale := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stale=%v", name, stale), func(t *testing.T) {
				store, db := backends[name](t)
				ctx := context.Background()
				seedTrees(t, ctx, db, 2000, stale)
				timeInit := func(what string) {
					start := time.Now()
					if err := store.Init(ctx); err != nil {
						t.Fatal(err)
					}
					t.Logf("%s: Init %s took %v", name, what, time.Since(start))
				}
				if stale {
					timeInit("repairing 7,000 stale rows")
					checkTreesRepaired(t, ctx, db)
					timeInit("already repaired")
				} else {
					timeInit("with no stale rows")
					timeInit("with no stale rows, again")
				}
			})
		}
	}
}

// seedTrees writes trees of ten tasks each, straight into the tables:
// a root r with three children, each with two children, and one task
// below r's first grandchild. A stale tree has the origins putTask wrote
// before it walked to the root (a root and children stored with none,
// grandchildren naming their parent, the great-grandchild naming the
// task its parent named); any other has every task below r naming r.
func seedTrees(t *testing.T, ctx context.Context, db *sql.DB, trees int, stale bool) {
	t.Helper()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	put := func(id, parent, origin string) {
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO `task` (`id`, `intent`, `title`, `body`, `origin_actor_kind`, `origin_actor_id`, "+
				"`origin_reason`, `binding`, `auto_merge`, `origin_ref`) VALUES (?,?,?,?,?,?,?,?,?,?)",
			id, string(model.IntentImplement), "task "+id, "", string(bot.Kind), bot.ID,
			string(model.ReasonDirect), string(model.BindingDirective), false, origin); err != nil {
			t.Fatal(err)
		}
		if parent != "" {
			if _, err := tx.ExecContext(ctx,
				"INSERT INTO `task_link` (`task_id`, `kind`, `target`, `blocks`) VALUES (?,?,?,?)",
				id, string(model.LinkProposedBy), parent, false); err != nil {
				t.Fatal(err)
			}
		}
	}
	for i := 0; i < trees; i++ {
		base := 100000 + i*20
		id := func(n int) string { return strconv.Itoa(base + n) }
		root := id(0)
		staleTree := stale && i%2 == 0
		under := func(parent, staleOrigin string) string {
			if staleTree {
				return staleOrigin
			}
			return "task:" + root
		}
		put(root, "", "")
		for c := 1; c <= 3; c++ {
			put(id(c), root, under(root, ""))
			for g := 0; g < 2; g++ {
				put(id(3+(c-1)*2+g+1), id(c), under(id(c), "task:"+id(c)))
			}
		}
		put(id(10), id(4), under(id(4), "task:"+id(1)))
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

// checkTreesRepaired is seedTrees' trees after a repair: no task origin
// names anything but a root.
func checkTreesRepaired(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	var n int
	if err := db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM `task` WHERE `origin_ref` LIKE 'task:%' AND `origin_ref` IN "+
			"(SELECT 'task:' || `task_id` FROM `task_link` WHERE `kind` = ?)", string(model.LinkProposedBy)).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("%d task origins still name a task that is not a root", n)
	}
}
