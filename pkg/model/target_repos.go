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

package model

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// TargetRepos is the repositories this deployment dispatches work into
// (Config.TargetRepos), nil for all of them and for a store with no
// settings row yet.
func (s *Store) TargetRepos(ctx context.Context) ([]string, error) {
	return targetReposIn(ctx, s.db)
}

func targetReposIn(ctx context.Context, q querier) ([]string, error) {
	var raw string
	err := q.QueryRowContext(ctx, "SELECT `target_repos` FROM `grain_config` WHERE `id` = 1").Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading the deployment's target repos: %w", err)
	}
	return splitCSV(raw), nil
}

// SetTargetRepos replaces the deployment's target repos
// (Config.TargetRepos), leaving every other setting as it was.
func (s *Store) SetTargetRepos(ctx context.Context, repos []string) error {
	return s.UpdateConfig(ctx, func(c *Config) error {
		c.TargetRepos = repos
		return nil
	})
}
