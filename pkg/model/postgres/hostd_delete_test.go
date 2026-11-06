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

package postgres_test

import (
	"errors"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// DeleteHostd removes only a revoked hostd, with every token row it has;
// the runs that used it keep its name, and the name can be registered
// again as a new hostd that inherits nothing.
func TestDeleteHostdRemovesARevokedHostdAndItsTokens(t *testing.T) {
	t.Parallel()
	store, db, ctx := openStore(t)
	tokenRows := func(name string) int {
		t.Helper()
		var n int
		if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM `hostd_token` WHERE `hostd` = ?", name).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	if _, _, err := store.CreateHostd(ctx, model.Hostd{
		Name: "ws-old", Owner: "alice@example.com", Labels: map[string]string{"os": "linux"}, CreatedBy: "admin",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RotateHostdToken(ctx, "ws-old"); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordTaskRunHostd(ctx, model.TaskRunHostd{RunID: "run-1", TaskID: "1", Hostd: "ws-old", At: time.Now()}); err != nil {
		t.Fatal(err)
	}

	if err := store.DeleteHostd(ctx, "ws-old"); !errors.Is(err, model.ErrHostdLive) {
		t.Fatalf("deleting a live hostd: %v, want ErrHostdLive", err)
	}
	if h, _ := store.GetHostd(ctx, "ws-old"); h == nil {
		t.Fatal("a refused delete removed the hostd")
	}
	if err := store.DeleteHostd(ctx, "no-such"); !errors.Is(err, model.ErrHostdNotFound) {
		t.Errorf("deleting an unknown hostd: %v, want ErrHostdNotFound", err)
	}
	if err := store.EnsureLocalHostd(ctx); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteHostd(ctx, model.LocalHostd); err == nil {
		t.Error("deleting the local hostd succeeded")
	}

	if _, err := store.RevokeHostd(ctx, "ws-old"); err != nil {
		t.Fatal(err)
	}
	if n := tokenRows("ws-old"); n != 2 {
		t.Fatalf("ws-old has %d token rows before delete, want 2", n)
	}
	if err := store.DeleteHostd(ctx, "ws-old"); err != nil {
		t.Fatal(err)
	}
	if h, err := store.GetHostd(ctx, "ws-old"); err != nil || h != nil {
		t.Fatalf("after delete GetHostd = %+v, %v; want nil", h, err)
	}
	if n := tokenRows("ws-old"); n != 0 {
		t.Errorf("ws-old has %d token rows after delete, want 0", n)
	}
	if r, err := store.RunHostd(ctx, "run-1"); err != nil || r == nil || r.Hostd != "ws-old" {
		t.Errorf("run-1's recorded hostd after delete = %+v, %v; want ws-old kept", r, err)
	}
	if err := store.DeleteHostd(ctx, "ws-old"); !errors.Is(err, model.ErrHostdNotFound) {
		t.Errorf("deleting ws-old twice: %v, want ErrHostdNotFound", err)
	}

	h, token, err := store.CreateHostd(ctx, model.Hostd{Name: "ws-old"})
	if err != nil {
		t.Fatalf("registering a deleted name again: %v", err)
	}
	if h.Owner != "" || len(h.Labels) != 0 || h.Revoked() || h.CreatedBy != "" {
		t.Errorf("re-registered ws-old = %+v, want nothing carried over", h)
	}
	if got, _ := store.HostdForToken(ctx, token); got == nil || got.Name != "ws-old" {
		t.Errorf("the new ws-old's token names %+v", got)
	}
	if n := tokenRows("ws-old"); n != 1 {
		t.Errorf("the new ws-old has %d token rows, want 1", n)
	}
}
