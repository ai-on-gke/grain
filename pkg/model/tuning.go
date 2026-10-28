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
	"fmt"
	"strings"
	"time"
)

// TuningRecommendation records a structured tuning proposal.
type TuningRecommendation struct {
	ID              int64     `json:"id"`
	TargetParameter string    `json:"target_parameter"`
	ProposedValue   string    `json:"proposed_value"`
	CooldownExpiry  time.Time `json:"cooldown_expiry"`
	Adopted         bool      `json:"adopted"`
	TaskID          string    `json:"task_id"`
	CreatedAt       time.Time `json:"created_at"`
}

// InfraReviewCooldownDays is how long a tuned parameter remains in cooldown (7 days).
const InfraReviewCooldownDays = 7

// RecognizedGrainConfigParameters is the allowlist of valid parameters in grain_config.
var RecognizedGrainConfigParameters = map[string]struct{}{
	"poll_interval_ms":               {},
	"max_workers":                    {},
	"max_mergers":                    {},
	"max_merges_per_repo":            {},
	"max_ci_jobs":                    {},
	"max_agent_turns":                {},
	"github_host":                    {},
	"github_insecure_http":           {},
	"gcp_project":                    {},
	"gcp_service_account_email":      {},
	"gcp_service_account_count":      {},
	"newest_first":                   {},
	"sandbox_backend":                {},
	"sandbox_cpus":                   {},
	"sandbox_memory_mb":              {},
	"sandbox_disk_gb":                {},
	"sandbox_cpu_overcommit":         {},
	"sandbox_memory_overcommit":      {},
	"sandbox_disk_overcommit":        {},
	"sandbox_memory_reserve_mb":      {},
	"sandbox_overcommit":             {},
	"sandbox_disk_floor_share":       {},
	"sandbox_disk_floor_min_mb":      {},
	"show_closed_by_default":         {},
	"default_filters":                {},
	"agent_backends":                 {},
	"default_model":                  {},
	"approved_by_default":            {},
	"auto_merge_by_default":          {},
	"task_defaults_on_backfilled":    {},
	"default_capabilities":           {},
	"environment_name":               {},
	"prompt_extension":               {},
	"task_branch_template":           {},
	"deployment_id":                  {},
	"time_zone":                      {},
	"upgrade_check_mode":             {},
	"upgrade_check_pauses":           {},
	"network_access":                 {},
	"console_sessions":               {},
	"sandbox_image":                  {},
	"package_cache_enabled":          {},
	"package_cache_size_gb":          {},
	"actions_cache_enabled":          {},
	"actions_cache_size_gb":          {},
	"ci_run_history":                 {},
	"ci_step_output_mb":              {},
	"ci_step_output_days":            {},
	"ci_run_output_kb":               {},
	"ci_jobs_per_run":                {},
	"ci_age_boost_ms":                {},
	"ci_reserve_after_ms":            {},
	"max_auto_submit_depth":          {},
	"max_auto_submit_fan_out":        {},
	"iap_backend_service":            {},
	"iap_resource":                   {},
	"repair_template_id":             {},
	"base_ci_red_streak":             {},
	"k8s_priority_class_interactive": {},
	"k8s_priority_class_agent":       {},
	"k8s_priority_class_ci":          {},
	"auto_upgrade_channel":           {},
	"auto_upgrade_time":              {},
	"judgement_model":                {},
	"password_auth_enabled":          {},
	"open_registration":              {},
	"google_oauth_enabled":           {},
	"google_oauth_client_id":         {},
	"google_oauth_client_secret":     {},
	"google_oauth_allowed_domains":   {},
}

// CanonicalizeTuningParameter trims and normalizes a parameter name (converting hyphens to underscores).
func CanonicalizeTuningParameter(param string) string {
	param = strings.TrimSpace(strings.ToLower(param))
	param = strings.ReplaceAll(param, "-", "_")
	return param
}

// IsRecognizedTuningParameter reports whether the parameter is a valid grain_config setting.
func IsRecognizedTuningParameter(param string) bool {
	norm := CanonicalizeTuningParameter(param)
	_, ok := RecognizedGrainConfigParameters[norm]
	return ok
}

// ValidateTuningParameter checks if the parameter is a recognized grain_config parameter.
func ValidateTuningParameter(param string) error {
	norm := CanonicalizeTuningParameter(param)
	if norm == "" {
		return fmt.Errorf("tuning parameter name cannot be empty")
	}
	if !IsRecognizedTuningParameter(norm) {
		return fmt.Errorf("unrecognized grain_config parameter %q", param)
	}
	return nil
}

// RecordTuningRecommendation records a tuning recommendation in the store.
func (s *Store) RecordTuningRecommendation(ctx context.Context, rec TuningRecommendation) (int64, error) {
	if rec.CreatedAt.IsZero() {
		rec.CreatedAt = time.Now().UTC()
	}
	rec.TargetParameter = CanonicalizeTuningParameter(rec.TargetParameter)
	var id int64
	err := s.write(ctx, "record tuning recommendation for "+rec.TargetParameter,
		func(tx *sql.Tx) error {
			res, err := tx.ExecContext(ctx,
				"INSERT INTO `tuning_recommendation` (`target_parameter`,`proposed_value`,`cooldown_expiry`,`adopted`,`task_id`,`created_at`) "+
					"VALUES (?,?,?,?,?,?)",
				rec.TargetParameter, rec.ProposedValue, rec.CooldownExpiry.UTC(), rec.Adopted, rec.TaskID, rec.CreatedAt.UTC())
			if err != nil {
				return err
			}
			id, err = res.LastInsertId()
			return err
		})
	return id, err
}

// ListTuningRecommendations retrieves tuning recommendations, optionally filtered by parameter.
func (s *Store) ListTuningRecommendations(ctx context.Context, param string, limit int) ([]TuningRecommendation, error) {
	var query string
	var args []any
	param = CanonicalizeTuningParameter(param)
	if param != "" {
		query = "SELECT `id`,`target_parameter`,`proposed_value`,`cooldown_expiry`,`adopted`,`task_id`,`created_at` " +
			"FROM `tuning_recommendation` WHERE `target_parameter` = ? ORDER BY `id` DESC"
		args = append(args, param)
	} else {
		query = "SELECT `id`,`target_parameter`,`proposed_value`,`cooldown_expiry`,`adopted`,`task_id`,`created_at` " +
			"FROM `tuning_recommendation` ORDER BY `id` DESC"
	}
	if limit > 0 {
		query += " LIMIT ?"
		args = append(args, limit)
	}

	var out []TuningRecommendation
	err := each(ctx, s.db, query, args, func(rows *sql.Rows) error {
		var r TuningRecommendation
		var expiry, created time.Time
		if err := rows.Scan(&r.ID, &r.TargetParameter, &r.ProposedValue, &expiry, &r.Adopted, &r.TaskID, &created); err != nil {
			return err
		}
		r.CooldownExpiry = expiry.UTC()
		r.CreatedAt = created.UTC()
		out = append(out, r)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("listing tuning recommendations: %w", err)
	}
	return out, nil
}

// GetTuningRecommendation retrieves a tuning recommendation by ID.
func (s *Store) GetTuningRecommendation(ctx context.Context, id int64) (*TuningRecommendation, error) {
	var r TuningRecommendation
	var expiry, created time.Time
	err := s.db.QueryRowContext(ctx,
		"SELECT `id`,`target_parameter`,`proposed_value`,`cooldown_expiry`,`adopted`,`task_id`,`created_at` "+
			"FROM `tuning_recommendation` WHERE `id` = ?", id).
		Scan(&r.ID, &r.TargetParameter, &r.ProposedValue, &expiry, &r.Adopted, &r.TaskID, &created)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("reading tuning recommendation %d: %w", id, err)
	}
	r.CooldownExpiry = expiry.UTC()
	r.CreatedAt = created.UTC()
	return &r, nil
}

// UpdateTuningRecommendationAdopted updates the adopted status of a recommendation.
func (s *Store) UpdateTuningRecommendationAdopted(ctx context.Context, id int64, adopted bool) error {
	return s.write(ctx, fmt.Sprintf("update tuning recommendation %d adopted=%v", id, adopted),
		func(tx *sql.Tx) error {
			res, err := tx.ExecContext(ctx,
				"UPDATE `tuning_recommendation` SET `adopted` = ? WHERE `id` = ?", adopted, id)
			if err != nil {
				return err
			}
			n, err := res.RowsAffected()
			if err != nil {
				return err
			}
			if n == 0 {
				return fmt.Errorf("tuning recommendation %d not found", id)
			}
			return nil
		})
}

// LatestTuningRecommendationForParameter returns the newest recommendation recorded for a parameter.
func (s *Store) LatestTuningRecommendationForParameter(ctx context.Context, param string) (*TuningRecommendation, error) {
	recs, err := s.ListTuningRecommendations(ctx, param, 1)
	if err != nil {
		return nil, err
	}
	if len(recs) == 0 {
		return nil, nil
	}
	return &recs[0], nil
}

// IsTuningParameterInCooldown checks if a recommendation for parameter is currently within its cooldown period.
func (s *Store) IsTuningParameterInCooldown(ctx context.Context, param string, now time.Time) (bool, *TuningRecommendation, error) {
	latest, err := s.LatestTuningRecommendationForParameter(ctx, param)
	if err != nil {
		return false, nil, err
	}
	if latest == nil {
		return false, nil, nil
	}
	if now.Before(latest.CooldownExpiry) {
		return true, latest, nil
	}
	return false, latest, nil
}
