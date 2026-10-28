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
	"database/sql"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/guardtest"
	"github.com/ai-on-gke/grain/pkg/model/postgres/postgrestest"
)

// The single-tenant startup guard (docs/single-tenant-reset.md, stage 5)
// on Postgres: Init and the migration rehearsal `grain state check
// -migrate` runs both refuse a multi-tenant deployment's rows, and both
// take a single-tenant one.
func TestSingleTenantGuard(t *testing.T) {
	guardtest.Run(t, func(t *testing.T) (*model.Store, *sql.DB) { return postgrestest.Open(t) })
}
