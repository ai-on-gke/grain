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
	"github.com/ai-on-gke/grain/pkg/model/postgres/postgrestest"
	"github.com/ai-on-gke/grain/pkg/model/v2baseline"
)

// A store grain 2.0.0 wrote, and a state repository it dumped, open in
// this build on Postgres (pkg/model/v2baseline).
func TestAStoreGrain2_0WroteOpensInThisBuild(t *testing.T) {
	v2baseline.Run(t, v2baseline.Engine{
		Schema: "schema.postgres.sql",
		Empty:  func(t *testing.T) *sql.DB { return postgrestest.OpenEmpty(t) },
		Fresh:  func(t *testing.T) (*model.Store, *sql.DB) { return postgrestest.Open(t) },
	})
}
