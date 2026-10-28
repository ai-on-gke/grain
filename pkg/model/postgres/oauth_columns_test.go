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
	"context"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
)

// setOAuthColumns writes grain_config's Google OAuth client id and
// secret by hand. Nothing in grain sets them any more (Google sign-in
// went with docs/single-tenant-reset.md's stage 3d), but an upgraded
// installation's row can still hold a secret until stage 6 drops the
// columns, and staterepo still withholds it, so these tests keep one
// there.
func setOAuthColumns(t *testing.T, ctx context.Context, store *model.Store, clientID, secret string) {
	t.Helper()
	if _, err := store.DB().ExecContext(ctx,
		"UPDATE `grain_config` SET `google_oauth_client_id` = ?, `google_oauth_client_secret` = ? WHERE `id` = 1",
		clientID, secret); err != nil {
		t.Fatalf("setting the OAuth columns: %v", err)
	}
}

// configColumn is grain_config's column of that name, as stored.
func configColumn(t *testing.T, ctx context.Context, store *model.Store, column string) string {
	t.Helper()
	var v string
	if err := store.DB().QueryRowContext(ctx, "SELECT `"+column+"` FROM `grain_config` WHERE `id` = 1").Scan(&v); err != nil {
		t.Fatalf("reading grain_config.%s: %v", column, err)
	}
	return v
}
