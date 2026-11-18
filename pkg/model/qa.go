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
	QAFlowGoldenV1Minimal       = "golden-v1-minimal"
	QAFlowEngineeringWorkflowV4 = "engineering-workflow-v4"
	QAFlowGoldenV3Enforced      = "golden-v3-enforced"
	QAFlowGoldenV3              = "golden-v3"
	QAFlowGoldenV2              = "golden-v2"
	QAFlowGoldenV2ChecksOnly    = "golden-v2-checks-only"
	QAFlowGoldenV2SkillOnly     = "golden-v2-skill-only"
	QAFlowDeterministicS3Only   = "deterministic-s3-only"
	QAFlowRepoCustom            = "repo-custom"

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

	// QAStageBranchPrefix namespaces pre-PR staging branches pushed by treatment-arm sandboxes
	// before S3/S4 quality verification promotes commits to the task's live PR branch.
	QAStageBranchPrefix = "grain-qa-stage/"
)

// ActiveQAResolution is a callback to avoid import cycles when reading the current QA arm for a task.
var ActiveQAResolution func(ctx context.Context, store *Store, taskID string) (string, error)

// QAStageBranchName returns the isolated staging branch ref name for taskID.
func QAStageBranchName(taskID string) string {
	return QAStageBranchPrefix + strings.TrimSpace(taskID)
}

// ResolveQARootOrigin normalizes a task's WorkOrigin into a sticky root-origin key.
//
// It preserves repository-qualified origins such as "acme/widgets#12" intact so
// Store.ActiveQAAssignmentForTask, Store.GitPushBranch, and qa.ResolveRootOrigin
// always agree on the same key.
func ResolveQARootOrigin(workOrigin WorkOrigin, taskID string) string {
	s := strings.TrimSpace(string(workOrigin))
	if s != "" {
		return s
	}
	id := strings.TrimSpace(taskID)
	if id == "" {
		return ""
	}
	return "task:" + id
}

// TaskEligibleForQAStaging reports whether task pushes its own candidate PR branch (and is not
// an interactive task, a priority-dispatch/durable session, an in-place review push, a comments-only review, or an external PR).
func TaskEligibleForQAStaging(task Task) bool {
	if task.Target == nil || IsConfigRepo(*task.Target) || task.ChangeTarget != nil {
		return false
	}
	if task.Interactive || task.HasPriorityDispatch() {
		return false
	}
	for _, l := range task.Links {
		switch l.Kind {
		case LinkPushesReviewedBranch, LinkReviewCommentsOnly, LinkExternalPR:
			return false
		}
	}
	return true
}

// ActiveQAAssignmentForTask returns the active sticky QAAssignment and QAConfig for taskID when
// deployment-wide QA is enabled and taskID is eligible for pre-PR quality gating.
func (s *Store) ActiveQAAssignmentForTask(ctx context.Context, taskID string) (*QAAssignment, QAConfig, error) {
	if s == nil || strings.TrimSpace(taskID) == "" {
		return nil, QAConfig{}, nil
	}
	var rawQA sql.NullString
	if err := s.db.QueryRowContext(ctx, "SELECT `qa` FROM `grain_config` WHERE `id` = 1").Scan(&rawQA); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, QAConfig{}, nil
		}
		return nil, QAConfig{}, err
	}
	qaCfg, err := DecodeQAConfig(rawQA.String)
	if err != nil || !qaCfg.Enabled {
		return nil, qaCfg, nil
	}
	task, err := s.GetTask(ctx, taskID)
	if err != nil || task == nil || !TaskEligibleForQAStaging(*task) {
		return nil, qaCfg, err
	}
	assign, err := s.QAAssignment(ctx, ResolveQARootOrigin(task.WorkOrigin, task.ID), qaCfg.EpochOrDefault())
	return assign, qaCfg, err
}

// QAFlowIDs returns the supported quality control flow identifiers in UI picker order.
func QAFlowIDs() []string {
	return []string{
		QAFlowGoldenV1Minimal,
		QAFlowEngineeringWorkflowV4,
		QAFlowGoldenV3,
		QAFlowGoldenV2,
		QAFlowDeterministicS3Only,
		QAFlowRepoCustom,
	}
}

// ValidQAFlowID reports whether id names a supported quality control flow package.
func ValidQAFlowID(id string) bool {
	trimmed := strings.TrimSpace(id)
	if trimmed == "" {
		return false
	}
	if trimmed == QAFlowDeterministicS3Only || trimmed == QAFlowRepoCustom {
		return true
	}
	base := strings.TrimSuffix(trimmed, "@1")
	return slices.Contains([]string{
		QAFlowGoldenV1Minimal,
		QAFlowEngineeringWorkflowV4,
		QAFlowGoldenV3,
		QAFlowGoldenV2,
		QAFlowGoldenV2ChecksOnly,
		QAFlowGoldenV2SkillOnly,
	}, base)
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
	return NormalizeQAConfigWithValidator(cfg, nil)
}

// NormalizeQAConfigWithValidator validates and canonicalizes cfg using validFlow when non-nil
// (falling back to ValidQAFlowID when validFlow is nil).
func NormalizeQAConfigWithValidator(cfg QAConfig, validFlow func(string) bool) (QAConfig, error) {
	if cfg.IsZero() {
		return QAConfig{}, nil
	}
	if cfg.RolloutPct < 0 || cfg.RolloutPct > 100 {
		return QAConfig{}, fmt.Errorf("rolloutPct must be between 0 and 100 (got %d)", cfg.RolloutPct)
	}
	checkFlow := validFlow
	if checkFlow == nil {
		checkFlow = ValidQAFlowID
	}
	flow := strings.TrimSpace(cfg.FlowID)
	if flow == "" {
		flow = DefaultQAFlowID
	} else if !checkFlow(flow) {
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
	return AdvanceQAConfigWithValidator(prev, next, nil)
}

// AdvanceQAConfigWithValidator validates next against prev using validFlow when non-nil
// (falling back to ValidQAFlowID when validFlow is nil).
func AdvanceQAConfigWithValidator(prev, next QAConfig, validFlow func(string) bool) (QAConfig, error) {
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
	if validFlow == nil && !prev.IsZero() {
		prevFlow := strings.TrimSpace(prev.FlowID)
		nextFlow := strings.TrimSpace(next.FlowID)
		if nextFlow != "" && nextFlow == prevFlow && validStoredQAFlowRef(nextFlow) &&
			(!next.Enabled || (prev.Enabled && next.RolloutPct <= prev.RolloutPct && next.Epoch <= prev.EpochOrDefault())) {
			validFlow = func(flow string) bool {
				return strings.TrimSpace(flow) == prevFlow || ValidQAFlowID(flow)
			}
		}
	}
	norm, err := NormalizeQAConfigWithValidator(next, validFlow)
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

func validStoredQAFlowRef(id string) bool {
	trimmed := strings.TrimSpace(id)
	if trimmed == "" || len(trimmed) > 256 {
		return false
	}
	if ValidQAFlowID(trimmed) {
		return true
	}
	for _, r := range trimmed {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') ||
			r == '-' || r == '_' || r == '.' || r == '@' || r == ':' || r == '#' || r == '/' {
			continue
		}
		return false
	}
	return true
}

// EncodeQAConfig serializes cfg as compact JSON for grain_config.qa, or "" when zero.
func EncodeQAConfig(cfg QAConfig) string {
	norm, err := NormalizeQAConfigWithValidator(cfg, validStoredQAFlowRef)
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
	dec := json.NewDecoder(strings.NewReader(s))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		return QAConfig{}, fmt.Errorf("decoding grain_config.qa: %w", err)
	}
	if dec.More() {
		return QAConfig{}, fmt.Errorf("decoding grain_config.qa: trailing data")
	}
	norm, err := NormalizeQAConfigWithValidator(cfg, validStoredQAFlowRef)
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
	ID                int64     `json:"id"`
	TaskID            string    `json:"taskId"`
	RunID             string    `json:"runId"`
	CommitSHA         string    `json:"commitSha"`
	Stage             string    `json:"stage"`
	CheckID           string    `json:"checkId"`
	Arm               string    `json:"arm"`
	Shadow            bool      `json:"shadow"`
	Verdict           string    `json:"verdict"`
	DurationMS        int64     `json:"durationMs"`
	TokensIn          int64     `json:"tokensIn"`
	TokensOut         int64     `json:"tokensOut"`
	AllocBytes        int64     `json:"allocBytes"`
	DetailJSON        string    `json:"detailJson,omitempty"`
	ExperimentID      string    `json:"experimentId,omitempty"`
	ArmID             string    `json:"armId,omitempty"`
	FlowHash          string    `json:"flowHash,omitempty"`
	QAEngineRev       int       `json:"qaEngineRev,omitempty"`
	Model             string    `json:"model,omitempty"`
	BaseSHA           string    `json:"baseSha,omitempty"`
	BlastTier         string    `json:"blastTier,omitempty"`
	BlastScore        int       `json:"blastScore,omitempty"`
	ComplexityTier    string    `json:"complexityTier,omitempty"`
	TriageDecision    string    `json:"triageDecision,omitempty"`
	RepairedInNextSha int       `json:"repairedInNextSha,omitempty"`
	CreatedAt         time.Time `json:"createdAt"`
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
				"`duration_ms`, `tokens_in`, `tokens_out`, `alloc_bytes`, `detail_json`, "+
				"`experiment_id`, `arm_id`, `flow_hash`, `qa_engine_rev`, `model`, `base_sha`, `blast_tier`, `blast_score`, `complexity_tier`, `triage_decision`, `repaired_in_next_sha`, `created_at`) "+
				"VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
			run.TaskID, run.RunID, run.CommitSHA, run.Stage, run.CheckID, run.Arm, shadowInt, run.Verdict,
			run.DurationMS, run.TokensIn, run.TokensOut, run.AllocBytes, run.DetailJSON,
			run.ExperimentID, run.ArmID, run.FlowHash, run.QAEngineRev, run.Model, run.BaseSHA,
			run.BlastTier, run.BlastScore, run.ComplexityTier, run.TriageDecision, run.RepairedInNextSha, run.CreatedAt,
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

// MarkPriorQACheckRunsRepaired sets repaired_in_next_sha = 1 on prior non-shadow failed runs of this check on the same task.
func (s *Store) MarkPriorQACheckRunsRepaired(ctx context.Context, taskID, checkID, commitSHA string) error {
	return s.write(ctx, "mark prior qa check runs repaired", func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			"UPDATE `task_qa_check_run` SET `repaired_in_next_sha` = 1 "+
				"WHERE `task_id` = ? AND `check_id` = ? AND `verdict` = 'fail' AND `shadow` = 0 AND `commit_sha` != '' AND `commit_sha` != ?",
			taskID, checkID, commitSHA,
		)
		return err
	})
}

// ListQACheckRunsForTask returns all QA check runs for taskID ordered oldest to newest.
func (s *Store) ListQACheckRunsForTask(ctx context.Context, taskID string) ([]QACheckRun, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT `id`, `task_id`, `run_id`, `commit_sha`, `stage`, `check_id`, `arm`, `shadow`, `verdict`, "+
			"`duration_ms`, `tokens_in`, `tokens_out`, `alloc_bytes`, `detail_json`, "+
			"`experiment_id`, `arm_id`, `flow_hash`, `qa_engine_rev`, `model`, `base_sha`, `blast_tier`, `blast_score`, `complexity_tier`, `triage_decision`, `repaired_in_next_sha`, `created_at` "+
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
			&r.DurationMS, &r.TokensIn, &r.TokensOut, &r.AllocBytes, &r.DetailJSON,
			&r.ExperimentID, &r.ArmID, &r.FlowHash, &r.QAEngineRev, &r.Model, &r.BaseSHA,
			&r.BlastTier, &r.BlastScore, &r.ComplexityTier, &r.TriageDecision, &r.RepairedInNextSha, &r.CreatedAt,
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

// ListQAAssignments returns all sticky QA arm assignments ordered chronologically for
// Online-CUPED pre-filtration and CMH regime stratification.
func (s *Store) ListQAAssignments(ctx context.Context) ([]QAAssignment, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT `root_origin`, `epoch`, `regime_id`, `arm`, `flow_id`, `rollout_pct_at_assign`, `complexity_tier`, `assigned_at` "+
			"FROM `task_qa_assignment` ORDER BY `assigned_at` ASC, `root_origin` ASC",
	)
	if err != nil {
		return nil, fmt.Errorf("listing qa assignments: %w", err)
	}
	defer rows.Close()
	var out []QAAssignment
	for rows.Next() {
		var a QAAssignment
		if err := rows.Scan(
			&a.RootOrigin, &a.Epoch, &a.RegimeID, &a.Arm, &a.FlowID,
			&a.RolloutPctAtAssign, &a.ComplexityTier, &a.AssignedAt,
		); err != nil {
			return nil, err
		}
		a.AssignedAt = a.AssignedAt.UTC()
		out = append(out, a)
	}
	return out, rows.Err()
}

// ListAllQACheckRuns returns all recorded QA check runs ordered by id ASC.
func (s *Store) ListAllQACheckRuns(ctx context.Context) ([]QACheckRun, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT `id`, `task_id`, `run_id`, `commit_sha`, `stage`, `check_id`, `arm`, `shadow`, `verdict`, "+
			"`duration_ms`, `tokens_in`, `tokens_out`, `alloc_bytes`, `detail_json`, "+
			"`experiment_id`, `arm_id`, `flow_hash`, `qa_engine_rev`, `model`, `base_sha`, "+
			"`blast_tier`, `blast_score`, `complexity_tier`, `triage_decision`, `repaired_in_next_sha`, `created_at` "+
			"FROM `task_qa_check_run` ORDER BY `id` ASC",
	)
	if err != nil {
		return nil, fmt.Errorf("listing all qa check runs: %w", err)
	}
	defer rows.Close()
	var out []QACheckRun
	for rows.Next() {
		var r QACheckRun
		var shadowInt int
		if err := rows.Scan(
			&r.ID, &r.TaskID, &r.RunID, &r.CommitSHA, &r.Stage, &r.CheckID, &r.Arm, &shadowInt, &r.Verdict,
			&r.DurationMS, &r.TokensIn, &r.TokensOut, &r.AllocBytes, &r.DetailJSON,
			&r.ExperimentID, &r.ArmID, &r.FlowHash, &r.QAEngineRev, &r.Model, &r.BaseSHA,
			&r.BlastTier, &r.BlastScore, &r.ComplexityTier, &r.TriageDecision, &r.RepairedInNextSha, &r.CreatedAt,
		); err != nil {
			return nil, err
		}
		r.Shadow = shadowInt != 0
		r.CreatedAt = r.CreatedAt.UTC()
		out = append(out, r)
	}
	return out, rows.Err()
}

// ListAllQAFindings returns all recorded QA findings ordered by id ASC.
func (s *Store) ListAllQAFindings(ctx context.Context) ([]QAFinding, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT `id`, `check_run_id`, `task_id`, `stage`, `rule_id`, `severity`, `file_path`, `line`, "+
			"`summary`, `evidence`, `human_vote`, `voted_by`, `voted_at`, `created_at` "+
			"FROM `task_qa_finding` ORDER BY `id` ASC",
	)
	if err != nil {
		return nil, fmt.Errorf("listing all qa findings: %w", err)
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
		f.CreatedAt = f.CreatedAt.UTC()
		if votedAt.Valid {
			t := votedAt.Time.UTC()
			f.VotedAt = &t
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// CIRunOutcome is one stored grain CI run's conclusion, used by QA telemetry to compute
// first-push CI pass rates per branch without loading job step output.
type CIRunOutcome struct {
	ID         string
	Repo       string
	Ref        string
	CommitSHA  string
	Event      string
	Conclusion string
	Recheck    bool
	StartedAt  *time.Time
	FinishedAt *time.Time
}

// CIRunOutcomes returns all stored grain CI run conclusions ordered chronologically.
func (s *Store) CIRunOutcomes(ctx context.Context) ([]CIRunOutcome, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT `id`, `repo`, `ref`, `commit_sha`, `event`, `conclusion`, `recheck`, `started_at`, `finished_at` "+
			"FROM `ci_run` ORDER BY COALESCE(`started_at`, `finished_at`), `id`",
	)
	if err != nil {
		return nil, fmt.Errorf("listing ci run outcomes: %w", err)
	}
	defer rows.Close()
	var out []CIRunOutcome
	for rows.Next() {
		var r CIRunOutcome
		var recheckInt int
		var started, finished sql.NullTime
		if err := rows.Scan(
			&r.ID, &r.Repo, &r.Ref, &r.CommitSHA, &r.Event, &r.Conclusion, &recheckInt, &started, &finished,
		); err != nil {
			return nil, err
		}
		r.Recheck = recheckInt != 0
		r.StartedAt = timePtr(started)
		r.FinishedAt = timePtr(finished)
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) OriginHasPriorRuns(ctx context.Context, rootOrigin string, excludeRunID string) (bool, error) {
	var count int
	query := "SELECT 1 FROM `task` t JOIN `task_run` r ON t.`id` = r.`task_id` WHERE t.`origin_ref` = ? AND r.`id` != ? LIMIT 1"
	err := s.db.QueryRowContext(ctx, query, rootOrigin, excludeRunID).Scan(&count)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func (s *Store) EarliestQAAssignment(ctx context.Context, rootOrigin string) (*QAAssignment, error) {
	var a QAAssignment
	err := s.db.QueryRowContext(ctx,
		"SELECT `root_origin`, `epoch`, `regime_id`, `arm`, `flow_id`, `rollout_pct_at_assign`, `complexity_tier`, `assigned_at` "+
			"FROM `task_qa_assignment` WHERE `root_origin` = ? ORDER BY `assigned_at` ASC LIMIT 1", rootOrigin,
	).Scan(
		&a.RootOrigin, &a.Epoch, &a.RegimeID, &a.Arm, &a.FlowID,
		&a.RolloutPctAtAssign, &a.ComplexityTier, &a.AssignedAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	a.AssignedAt = a.AssignedAt.UTC()
	return &a, nil
}

func (s *Store) ConfigQA(ctx context.Context) (QAConfig, error) {
	var rawQA sql.NullString
	if err := s.db.QueryRowContext(ctx, "SELECT `qa` FROM `grain_config` WHERE `id` = 1").Scan(&rawQA); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return QAConfig{}, nil
		}
		return QAConfig{}, err
	}
	return DecodeQAConfig(rawQA.String)
}
