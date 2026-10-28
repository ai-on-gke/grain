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
