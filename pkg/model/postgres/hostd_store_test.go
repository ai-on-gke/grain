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

	"github.com/ai-on-gke/grain/pkg/model"
)

// A remote hostd's token names it, a rotation retires the token before
// it, and revoking the hostd retires every token it has.
func TestHostdRegistryTokens(t *testing.T) {
	t.Parallel()
	store, _, ctx := openStore(t)

	h, token, err := store.CreateHostd(ctx, model.Hostd{
		Name: "ws-1", Owner: " Alice@Example.COM ", Labels: map[string]string{"os": "linux"}, CreatedBy: "admin",
	})
	if err != nil {
		t.Fatal(err)
	}
	if h.Owner != "alice@example.com" {
		t.Errorf("owner = %q, want it lowercased and trimmed", h.Owner)
	}
	if got, err := store.HostdForToken(ctx, token); err != nil || got == nil || got.Name != "ws-1" {
		t.Fatalf("HostdForToken(minted) = %+v, %v; want ws-1", got, err)
	}
	if got, _ := store.HostdForToken(ctx, token+"x"); got != nil {
		t.Errorf("HostdForToken(wrong) = %+v, want nil", got)
	}
	if _, _, err := store.CreateHostd(ctx, model.Hostd{Name: "ws-1"}); !errors.Is(err, model.ErrHostdExists) {
		t.Errorf("creating ws-1 again: %v, want ErrHostdExists", err)
	}
	for _, bad := range []model.Hostd{{Name: "WS"}, {Name: "a_b"}, {Name: model.LocalHostd}, {Name: "ok", Owner: "alice"}} {
		if _, _, err := store.CreateHostd(ctx, bad); err == nil {
			t.Errorf("CreateHostd(%+v) succeeded, want refused", bad)
		}
	}

	rotated, err := store.RotateHostdToken(ctx, "ws-1")
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := store.HostdForToken(ctx, token); got != nil {
		t.Errorf("the token before a rotation still names %s", got.Name)
	}
	if got, _ := store.HostdForToken(ctx, rotated); got == nil || got.Name != "ws-1" {
		t.Errorf("the rotated token names %+v, want ws-1", got)
	}

	owner, labels := "bob@example.com", map[string]string{"gpu": "yes"}
	updated, err := store.UpdateHostd(ctx, "ws-1", &owner, &labels)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Owner != owner || updated.Labels["gpu"] != "yes" || len(updated.Labels) != 1 {
		t.Errorf("after update = %+v", updated)
	}

	revoked, err := store.RevokeHostd(ctx, "ws-1")
	if err != nil {
		t.Fatal(err)
	}
	if !revoked.Revoked() || revoked.TokenPrefix != "" {
		t.Errorf("revoked = %+v, want revoked with no live token", revoked)
	}
	if got, _ := store.HostdForToken(ctx, rotated); got != nil {
		t.Errorf("a revoked hostd's token still names %s", got.Name)
	}
	if _, err := store.RotateHostdToken(ctx, "ws-1"); !errors.Is(err, model.ErrHostdRevoked) {
		t.Errorf("rotating a revoked hostd: %v, want ErrHostdRevoked", err)
	}
	if _, err := store.RevokeHostd(ctx, "nope"); !errors.Is(err, model.ErrHostdNotFound) {
		t.Errorf("revoking an unknown hostd: %v, want ErrHostdNotFound", err)
	}
	if _, err := store.RevokeHostd(ctx, model.LocalHostd); err == nil {
		t.Error("revoking the local hostd succeeded")
	}

	if err := store.EnsureLocalHostd(ctx); err != nil {
		t.Fatal(err)
	}
	if err := store.EnsureLocalHostd(ctx); err != nil {
		t.Fatalf("ensuring local twice: %v", err)
	}
	all, err := store.ListHostds(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 || all[0].Name != model.LocalHostd || all[1].Name != "ws-1" {
		t.Errorf("ListHostds = %+v, want local and ws-1", all)
	}
}

// Migration 92 adds the registry to a schema-91 store, with the
// co-located hostd in it as local.
func TestMigration92AddsHostdRegistry(t *testing.T) {
	t.Parallel()
	store, db, ctx := openStore(t)
	for _, stmt := range []string{"DROP TABLE `hostd`", "DROP TABLE `hostd_token`"} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
	rewindSchemaTo(t, ctx, db, 91)
	if err := model.New(db).Init(ctx); err != nil {
		t.Fatalf("migrating a version-91 store: %v", err)
	}
	h, err := store.GetHostd(ctx, model.LocalHostd)
	if err != nil || h == nil {
		t.Fatalf("local hostd after migration 92 = %+v, %v", h, err)
	}
	if _, _, err := store.CreateHostd(ctx, model.Hostd{Name: "ws-2"}); err != nil {
		t.Fatalf("creating a hostd after migration 92: %v", err)
	}
	var version int
	if err := db.QueryRowContext(ctx, "SELECT `version` FROM `grain_schema` WHERE `id` = 1").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != model.SchemaVersion {
		t.Errorf("schema version = %d after migrating, want %d", version, model.SchemaVersion)
	}
}
