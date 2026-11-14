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
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

// Built-in quality control flow package identifiers.
const (
	QAFlowGoldenV1Minimal     = "golden-v1-minimal"
	QAFlowDeterministicS3Only = "deterministic-s3-only"
	QAFlowRepoCustom          = "repo-custom"

	DefaultQAFlowID = QAFlowGoldenV1Minimal
)

// Experiment arm values assigned at dispatch per work-tree root origin.
const (
	QAArmOff       = "off"
	QAArmControl   = "control"
	QAArmTreatment = "treatment"
)

// Complexity tier labels recorded alongside sticky A/B assignments for CUPED stratification.
const (
	QAComplexityLow    = "low"
	QAComplexityMedium = "medium"
	QAComplexityHigh   = "high"
)

// QAFlowIDs returns the supported quality control flow identifiers in UI picker order.
func QAFlowIDs() []string {
	return []string{
		QAFlowGoldenV1Minimal,
		QAFlowDeterministicS3Only,
		QAFlowRepoCustom,
	}
}

// ValidQAFlowID reports whether id names a supported quality control flow package.
func ValidQAFlowID(id string) bool {
	return slices.Contains(QAFlowIDs(), strings.TrimSpace(id))
}

// QAConfig configures the deployment-wide quality control engine and single-instance
// PR rollout experiment.
//
// Zero value ships completely off (Enabled: false, RolloutPct: 0) with zero runtime
// overhead on task dispatch or git pushes.
type QAConfig struct {
	Enabled         bool   `json:"enabled" yaml:"enabled"`
	FlowID          string `json:"flowId,omitempty" yaml:"flow-id,omitempty"`
	RolloutPct      int    `json:"rolloutPct" yaml:"rollout-pct"`
	ShadowOnControl *bool  `json:"shadowOnControl,omitempty" yaml:"shadow-on-control,omitempty"`
	Epoch           int    `json:"epoch,omitempty" yaml:"epoch,omitempty"`
	RegimeID        int    `json:"regimeId,omitempty" yaml:"regime-id,omitempty"`
}

// BoolPtr returns a fresh pointer to b.
func BoolPtr(b bool) *bool { return &b }

// IsZero reports whether c carries no stored QA configuration.
func (c QAConfig) IsZero() bool {
	return !c.Enabled &&
		strings.TrimSpace(c.FlowID) == "" &&
		c.RolloutPct == 0 &&
		c.ShadowOnControl == nil &&
		c.Epoch == 0 &&
		c.RegimeID == 0
}

// FlowIDOrDefault returns c.FlowID when set, or DefaultQAFlowID.
func (c QAConfig) FlowIDOrDefault() string {
	if f := strings.TrimSpace(c.FlowID); f != "" {
		return f
	}
	return DefaultQAFlowID
}

// ShadowOnControlEnabled reports whether passive shadow checks run on control-arm PRs
// after final commit (default true when unset).
func (c QAConfig) ShadowOnControlEnabled() bool {
	if c.ShadowOnControl == nil {
		return true
	}
	return *c.ShadowOnControl
}

// EpochOrDefault returns c.Epoch when positive, or 1.
func (c QAConfig) EpochOrDefault() int {
	if c.Epoch >= 1 {
		return c.Epoch
	}
	return 1
}

// RegimeIDOrDefault returns c.RegimeID when positive, or 1.
func (c QAConfig) RegimeIDOrDefault() int {
	if c.RegimeID >= 1 {
		return c.RegimeID
	}
	return 1
}

// Clone returns a deep copy of c so pointer fields never alias across Config copies.
func (c QAConfig) Clone() QAConfig {
	out := c
	if c.ShadowOnControl != nil {
		out.ShadowOnControl = BoolPtr(*c.ShadowOnControl)
	}
	return out
}

// Equal reports whether c and o represent the same effective QA configuration.
func (c QAConfig) Equal(o QAConfig) bool {
	if c.IsZero() && o.IsZero() {
		return true
	}
	return c.Enabled == o.Enabled &&
		c.FlowIDOrDefault() == o.FlowIDOrDefault() &&
		c.RolloutPct == o.RolloutPct &&
		c.ShadowOnControlEnabled() == o.ShadowOnControlEnabled() &&
		c.EpochOrDefault() == o.EpochOrDefault() &&
		c.RegimeIDOrDefault() == o.RegimeIDOrDefault()
}

// NormalizeQAConfig validates and canonicalizes cfg. A completely zero QAConfig stays zero
// so untouched deployments write "" in grain_config.qa.
func NormalizeQAConfig(cfg QAConfig) (QAConfig, error) {
	if cfg.IsZero() {
		return QAConfig{}, nil
	}
	if cfg.RolloutPct < 0 || cfg.RolloutPct > 100 {
		return QAConfig{}, fmt.Errorf("rolloutPct must be between 0 and 100 (got %d)", cfg.RolloutPct)
	}
	flow := strings.TrimSpace(cfg.FlowID)
	if flow == "" {
		flow = DefaultQAFlowID
	} else if !ValidQAFlowID(flow) {
		return QAConfig{}, fmt.Errorf("flowId must be one of %s (got %q)", strings.Join(QAFlowIDs(), ", "), flow)
	}
	epoch := cfg.Epoch
	if epoch < 1 {
		epoch = 1
	}
	regime := cfg.RegimeID
	if regime < 1 {
		regime = 1
	}
	shadow := cfg.ShadowOnControlEnabled()
	return QAConfig{
		Enabled:         cfg.Enabled,
		FlowID:          flow,
		RolloutPct:      cfg.RolloutPct,
		ShadowOnControl: BoolPtr(shadow),
		Epoch:           epoch,
		RegimeID:        regime,
	}, nil
}

// AdvanceQAConfig validates next against prev, preserving Epoch/RegimeID and incrementing
// RegimeID automatically when an existing configuration changes rollout parameters so A/B
// telemetry segments cleanly across rollout percentage or flow changes.
func AdvanceQAConfig(prev, next QAConfig) (QAConfig, error) {
	if next.IsZero() && prev.IsZero() {
		return QAConfig{}, nil
	}
	if next.Epoch == 0 {
		next.Epoch = prev.EpochOrDefault()
	} else if next.Epoch < prev.EpochOrDefault() {
		return QAConfig{}, fmt.Errorf("epoch cannot decrease from %d to %d", prev.EpochOrDefault(), next.Epoch)
	}
	if next.RegimeID < prev.RegimeIDOrDefault() {
		next.RegimeID = prev.RegimeIDOrDefault()
	}
	norm, err := NormalizeQAConfig(next)
	if err != nil {
		return QAConfig{}, err
	}
	if !prev.IsZero() {
		policyChanged := prev.Enabled != norm.Enabled ||
			prev.FlowIDOrDefault() != norm.FlowID ||
			prev.RolloutPct != norm.RolloutPct ||
			prev.ShadowOnControlEnabled() != norm.ShadowOnControlEnabled() ||
			prev.EpochOrDefault() != norm.Epoch
		if policyChanged && norm.RegimeID <= prev.RegimeIDOrDefault() {
			norm.RegimeID = prev.RegimeIDOrDefault() + 1
		}
	}
	return norm, nil
}

// EncodeQAConfig serializes cfg as compact JSON for grain_config.qa, or "" when zero.
func EncodeQAConfig(cfg QAConfig) string {
	norm, err := NormalizeQAConfig(cfg)
	if err != nil || norm.IsZero() {
		return ""
	}
	b, err := json.Marshal(norm)
	if err != nil {
		return ""
	}
	return string(b)
}

// DecodeQAConfig parses stored JSON from grain_config.qa, returning zero QAConfig
// for empty values and an error when non-empty JSON is malformed or out of bounds
// so `grain state check` refuses broken config-repo edits before they merge.
func DecodeQAConfig(raw string) (QAConfig, error) {
	s := strings.TrimSpace(raw)
	if s == "" || s == "{}" || s == "null" {
		return QAConfig{}, nil
	}
	var cfg QAConfig
	if err := json.Unmarshal([]byte(s), &cfg); err != nil {
		return QAConfig{}, fmt.Errorf("decoding grain_config.qa: %w", err)
	}
	norm, err := NormalizeQAConfig(cfg)
	if err != nil {
		return QAConfig{}, fmt.Errorf("invalid grain_config.qa: %w", err)
	}
	return norm, nil
}

// QAAssignment records the sticky arm assignment for one work-tree root origin within an epoch.
type QAAssignment struct {
	RootOrigin         string    `json:"rootOrigin"`
	Epoch              int       `json:"epoch"`
	RegimeID           int       `json:"regimeId"`
	Arm                string    `json:"arm"`
	FlowID             string    `json:"flowId"`
	RolloutPctAtAssign int       `json:"rolloutPctAtAssign"`
	ComplexityTier     string    `json:"complexityTier"`
	AssignedAt         time.Time `json:"assignedAt"`
}

// QACheckRun records one deterministic or semantic QA check execution for a task run.
type QACheckRun struct {
	ID         int64     `json:"id"`
	TaskID     string    `json:"taskId"`
	RunID      string    `json:"runId"`
	CommitSHA  string    `json:"commitSha"`
	Stage      string    `json:"stage"`
	CheckID    string    `json:"checkId"`
	Arm        string    `json:"arm"`
	Shadow     bool      `json:"shadow"`
	Verdict    string    `json:"verdict"`
	DurationMS int64     `json:"durationMs"`
	TokensIn   int64     `json:"tokensIn"`
	TokensOut  int64     `json:"tokensOut"`
	AllocBytes int64     `json:"allocBytes"`
	DetailJSON string    `json:"detailJson,omitempty"`
	CreatedAt  time.Time `json:"createdAt"`
}

// QAFinding records an individual actionable or advisory QA finding attached to a QACheckRun.
type QAFinding struct {
	ID         int64      `json:"id"`
	CheckRunID int64      `json:"checkRunId"`
	TaskID     string     `json:"taskId"`
	Stage      string     `json:"stage"`
	RuleID     string     `json:"ruleId"`
	Severity   string     `json:"severity"`
	FilePath   string     `json:"filePath,omitempty"`
	Line       int        `json:"line,omitempty"`
	Summary    string     `json:"summary"`
	Evidence   string     `json:"evidence,omitempty"`
	HumanVote  int        `json:"humanVote"`
	VotedBy    string     `json:"votedBy,omitempty"`
	VotedAt    *time.Time `json:"votedAt,omitempty"`
	CreatedAt  time.Time  `json:"createdAt"`
}

// QAAssignment fetches the sticky QA assignment for (rootOrigin, epoch), or nil if none exists.
func (s *Store) QAAssignment(ctx context.Context, rootOrigin string, epoch int) (*QAAssignment, error) {
	rootOrigin = strings.TrimSpace(rootOrigin)
	if rootOrigin == "" || epoch < 1 {
		return nil, nil
	}
	var a QAAssignment
	err := s.db.QueryRowContext(ctx,
		"SELECT `root_origin`, `epoch`, `regime_id`, `arm`, `flow_id`, `rollout_pct_at_assign`, `complexity_tier`, `assigned_at` "+
			"FROM `task_qa_assignment` WHERE `root_origin` = ? AND `epoch` = ?",
		rootOrigin, epoch,
	).Scan(&a.RootOrigin, &a.Epoch, &a.RegimeID, &a.Arm, &a.FlowID, &a.RolloutPctAtAssign, &a.ComplexityTier, &a.AssignedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading qa assignment (%s, %d): %w", rootOrigin, epoch, err)
	}
	return &a, nil
}

// EnsureQAAssignment atomically inserts candidate if (rootOrigin, epoch) has no assignment yet,
// returning whichever row won so concurrent child dispatches in the same work tree never diverge.
func (s *Store) EnsureQAAssignment(ctx context.Context, candidate QAAssignment) (QAAssignment, error) {
	candidate.RootOrigin = strings.TrimSpace(candidate.RootOrigin)
	if candidate.RootOrigin == "" {
		return QAAssignment{}, fmt.Errorf("qa assignment root_origin cannot be empty")
	}
	if candidate.Epoch < 1 {
		candidate.Epoch = 1
	}
	if candidate.RegimeID < 1 {
		candidate.RegimeID = 1
	}
	if candidate.ComplexityTier == "" {
		candidate.ComplexityTier = QAComplexityMedium
	}
	if candidate.AssignedAt.IsZero() {
		candidate.AssignedAt = time.Now().UTC()
	} else {
		candidate.AssignedAt = candidate.AssignedAt.UTC()
	}

	var out QAAssignment
	err := s.write(ctx, "ensure qa assignment", func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			"INSERT OR IGNORE INTO `task_qa_assignment` "+
				"(`root_origin`, `epoch`, `regime_id`, `arm`, `flow_id`, `rollout_pct_at_assign`, `complexity_tier`, `assigned_at`) "+
				"VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
			candidate.RootOrigin, candidate.Epoch, candidate.RegimeID, candidate.Arm,
			candidate.FlowID, candidate.RolloutPctAtAssign, candidate.ComplexityTier, candidate.AssignedAt,
		); err != nil {
			return err
		}
		return tx.QueryRowContext(ctx,
			"SELECT `root_origin`, `epoch`, `regime_id`, `arm`, `flow_id`, `rollout_pct_at_assign`, `complexity_tier`, `assigned_at` "+
				"FROM `task_qa_assignment` WHERE `root_origin` = ? AND `epoch` = ?",
			candidate.RootOrigin, candidate.Epoch,
		).Scan(&out.RootOrigin, &out.Epoch, &out.RegimeID, &out.Arm, &out.FlowID, &out.RolloutPctAtAssign, &out.ComplexityTier, &out.AssignedAt)
	})
	if err != nil {
		return QAAssignment{}, err
	}
	return out, nil
}

// RecordQACheckRun inserts a QACheckRun and its findings in one transaction, returning the assigned IDs.
func (s *Store) RecordQACheckRun(ctx context.Context, run QACheckRun, findings []QAFinding) (int64, error) {
	if strings.TrimSpace(run.TaskID) == "" {
		return 0, fmt.Errorf("qa check run task_id cannot be empty")
	}
	if strings.TrimSpace(run.Stage) == "" || strings.TrimSpace(run.CheckID) == "" {
		return 0, fmt.Errorf("qa check run stage and check_id are required")
	}
	if run.CreatedAt.IsZero() {
		run.CreatedAt = time.Now().UTC()
	} else {
		run.CreatedAt = run.CreatedAt.UTC()
	}
	shadowInt := 0
	if run.Shadow {
		shadowInt = 1
	}

	var runRowID int64
	err := s.write(ctx, "record qa check run", func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			"INSERT INTO `task_qa_check_run` "+
				"(`task_id`, `run_id`, `commit_sha`, `stage`, `check_id`, `arm`, `shadow`, `verdict`, "+
				"`duration_ms`, `tokens_in`, `tokens_out`, `alloc_bytes`, `detail_json`, `created_at`) "+
				"VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
			run.TaskID, run.RunID, run.CommitSHA, run.Stage, run.CheckID, run.Arm, shadowInt, run.Verdict,
			run.DurationMS, run.TokensIn, run.TokensOut, run.AllocBytes, run.DetailJSON, run.CreatedAt,
		)
		if err != nil {
			return err
		}
		id, err := res.LastInsertId()
		if err != nil {
			return err
		}
		runRowID = id
		for _, f := range findings {
			created := f.CreatedAt
			if created.IsZero() {
				created = run.CreatedAt
			} else {
				created = created.UTC()
			}
			if _, err := tx.ExecContext(ctx,
				"INSERT INTO `task_qa_finding` "+
					"(`check_run_id`, `task_id`, `stage`, `rule_id`, `severity`, `file_path`, `line`, `summary`, `evidence`, `human_vote`, `voted_by`, `voted_at`, `created_at`) "+
					"VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
				runRowID, run.TaskID, firstNonEmpty(f.Stage, run.Stage), f.RuleID, f.Severity,
				f.FilePath, f.Line, f.Summary, f.Evidence, f.HumanVote, f.VotedBy, f.VotedAt, created,
			); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return runRowID, nil
}

// ListQACheckRunsForTask returns all QA check runs for taskID ordered oldest to newest.
func (s *Store) ListQACheckRunsForTask(ctx context.Context, taskID string) ([]QACheckRun, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT `id`, `task_id`, `run_id`, `commit_sha`, `stage`, `check_id`, `arm`, `shadow`, `verdict`, "+
			"`duration_ms`, `tokens_in`, `tokens_out`, `alloc_bytes`, `detail_json`, `created_at` "+
			"FROM `task_qa_check_run` WHERE `task_id` = ? ORDER BY `id` ASC",
		taskID,
	)
	if err != nil {
		return nil, fmt.Errorf("listing qa check runs for %s: %w", taskID, err)
	}
	defer rows.Close()
	var out []QACheckRun
	for rows.Next() {
		var r QACheckRun
		var shadowInt int
		if err := rows.Scan(
			&r.ID, &r.TaskID, &r.RunID, &r.CommitSHA, &r.Stage, &r.CheckID, &r.Arm, &shadowInt, &r.Verdict,
			&r.DurationMS, &r.TokensIn, &r.TokensOut, &r.AllocBytes, &r.DetailJSON, &r.CreatedAt,
		); err != nil {
			return nil, err
		}
		r.Shadow = shadowInt != 0
		out = append(out, r)
	}
	return out, rows.Err()
}

// ListQAFindingsForTask returns all QA findings for taskID ordered oldest to newest.
func (s *Store) ListQAFindingsForTask(ctx context.Context, taskID string) ([]QAFinding, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT `id`, `check_run_id`, `task_id`, `stage`, `rule_id`, `severity`, `file_path`, `line`, "+
			"`summary`, `evidence`, `human_vote`, `voted_by`, `voted_at`, `created_at` "+
			"FROM `task_qa_finding` WHERE `task_id` = ? ORDER BY `id` ASC",
		taskID,
	)
	if err != nil {
		return nil, fmt.Errorf("listing qa findings for %s: %w", taskID, err)
	}
	defer rows.Close()
	var out []QAFinding
	for rows.Next() {
		var f QAFinding
		var votedAt sql.NullTime
		if err := rows.Scan(
			&f.ID, &f.CheckRunID, &f.TaskID, &f.Stage, &f.RuleID, &f.Severity, &f.FilePath, &f.Line,
			&f.Summary, &f.Evidence, &f.HumanVote, &f.VotedBy, &votedAt, &f.CreatedAt,
		); err != nil {
			return nil, err
		}
		if votedAt.Valid {
			t := votedAt.Time.UTC()
			f.VotedAt = &t
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// VoteQAFinding records an operator signal (+1 useful catch, -1 false positive, 0 clear) on a finding.
func (s *Store) VoteQAFinding(ctx context.Context, findingID int64, vote int, votedBy string, at time.Time) error {
	if vote < -1 || vote > 1 {
		return fmt.Errorf("qa finding vote must be -1, 0, or 1 (got %d)", vote)
	}
	var votedAt any
	if vote != 0 {
		if at.IsZero() {
			at = time.Now().UTC()
		} else {
			at = at.UTC()
		}
		votedAt = at
	} else {
		votedBy = ""
	}
	return s.write(ctx, "vote qa finding", func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			"UPDATE `task_qa_finding` SET `human_vote` = ?, `voted_by` = ?, `voted_at` = ? WHERE `id` = ?",
			vote, strings.TrimSpace(votedBy), votedAt, findingID,
		)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			return sql.ErrNoRows
		}
		return nil
	})
}

func firstNonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}
