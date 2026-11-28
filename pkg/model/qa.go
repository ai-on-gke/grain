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
	"unicode/utf8"
)

// Built-in quality control flow package identifiers.
const (
	QAFlowStandard              = "standard-workflow"
	QAFlowFast                  = "fast-baseline"
	QAFlowGatesOnly             = "gates-only"
	QAFlowGoldenV1Minimal       = "golden-v1-minimal"
	QAFlowEngineeringWorkflowV4 = "engineering-workflow-v4"
	QAFlowGoldenV3Enforced      = "golden-v3-enforced"
	QAFlowGoldenV3              = "golden-v3"
	QAFlowGoldenV2              = "golden-v2"
	QAFlowGoldenV2ChecksOnly    = "golden-v2-checks-only"
	QAFlowGoldenV2SkillOnly     = "golden-v2-skill-only"
	QAFlowDeterministicS3Only   = "deterministic-s3-only"
	QAFlowRepoCustom            = "repo-custom"

	DefaultQAFlowID = QAFlowStandard
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

	// QACheckIDS3OperatorBypass records an explicit human operator override of a blocked S3 gate.
	QACheckIDS3OperatorBypass = "qa:s3-operator-bypass"

	// QACheckIDBaseResolution records an explicit S3 base-commit resolution failure
	// (instead of silently falling back to the repository's initial commit).
	QACheckIDBaseResolution = "qa:base-resolution"
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
		QAFlowStandard,
		QAFlowFast,
		QAFlowGatesOnly,
	}
}

// CanonicalQAFlowKey maps built-in and legacy workflow references to their canonical family key.
func CanonicalQAFlowKey(flowID string) string {
	s := strings.TrimSpace(flowID)
	s = strings.TrimPrefix(s, "builtin:")
	if idx := strings.IndexByte(s, '#'); idx >= 0 {
		s = s[:idx]
	}
	if idx := strings.IndexByte(s, '@'); idx >= 0 {
		s = s[:idx]
	}
	switch s {
	case QAFlowStandard, QAFlowEngineeringWorkflowV4, "golden-v4-composable", QAFlowGoldenV3, QAFlowGoldenV3Enforced, QAFlowGoldenV2:
		return QAFlowStandard
	case QAFlowFast, QAFlowGoldenV1Minimal, QAFlowRepoCustom, QAFlowGoldenV2SkillOnly:
		return QAFlowFast
	case QAFlowGatesOnly, QAFlowDeterministicS3Only, QAFlowGoldenV2ChecksOnly:
		return QAFlowGatesOnly
	default:
		return s
	}
}

// HumanQAFlowLabel returns a concise badge label for a workflow identifier.
func HumanQAFlowLabel(flowID string) string {
	key := CanonicalQAFlowKey(flowID)
	switch key {
	case "":
		return ""
	case QAFlowStandard:
		return "Standard"
	case QAFlowFast:
		return "Fast"
	case QAFlowGatesOnly:
		return "Gates only"
	default:
		if strings.HasPrefix(key, "sha256:") && len(key) > 14 {
			return key[:14]
		}
		clean := strings.NewReplacer("-", " ", "_", " ").Replace(key)
		clean = strings.TrimSpace(clean)
		if clean == "" {
			return key
		}
		firstRune, width := utf8.DecodeRuneInString(clean)
		if firstRune == utf8.RuneError && width <= 1 {
			return clean
		}
		return strings.ToUpper(string(firstRune)) + clean[width:]
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
		QAFlowStandard,
		QAFlowFast,
		QAFlowGatesOnly,
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

// TaskQASummary is the compact workflow, complexity, and blast-tier summary attached to Task responses.
type TaskQASummary struct {
	FlowID         string `json:"flowId"`
	FlowLabel      string `json:"flowLabel,omitempty"`
	Arm            string `json:"arm"`
	ComplexityTier string `json:"complexityTier,omitempty"`
	BlastTier      string `json:"blastTier,omitempty"`
	BlastScore     int    `json:"blastScore,omitempty"`
	TriageDecision string `json:"triageDecision,omitempty"`
	Verdict        string `json:"verdict,omitempty"`
	StageCount     int    `json:"stageCount,omitempty"`
	Pending        bool   `json:"pending,omitempty"`
}

// TaskQASummaries loads workflow assignments and aggregated check-run classifications for tasks in bounded batch queries.
func (s *Store) TaskQASummaries(
	ctx context.Context,
	tasks []Task,
	states map[string]State,
	assessComplexity func(Task, TaskSpecAttachments) string,
) (map[string]*TaskQASummary, error) {
	if s == nil || len(tasks) == 0 {
		return nil, nil
	}
	qaCfg, err := s.ConfigQA(ctx)
	if err != nil {
		qaCfg = QAConfig{}
	}

	rootSet := make(map[string]struct{}, len(tasks))
	var roots []string
	taskIDs := make([]string, 0, len(tasks))
	for _, t := range tasks {
		if t.ID == "" {
			continue
		}
		taskIDs = append(taskIDs, t.ID)
		ro := ResolveQARootOrigin(t.WorkOrigin, t.ID)
		if ro != "" {
			if _, ok := rootSet[ro]; !ok {
				rootSet[ro] = struct{}{}
				roots = append(roots, ro)
			}
		}
	}

	type lightEnroll struct {
		armID          string
		flowHash       string
		excludedReason string
	}
	assignByRoot := make(map[string]QAAssignment, len(roots))
	enrollByRoot := make(map[string]lightEnroll, len(roots))
	const batchChunk = 400
	for start := 0; start < len(roots); start += batchChunk {
		chunk := roots[start:min(start+batchChunk, len(roots))]
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(chunk)), ",")
		args := make([]any, len(chunk))
		for i, v := range chunk {
			args[i] = v
		}
		rows, qErr := s.db.QueryContext(ctx,
			"SELECT `root_origin`, `epoch`, `regime_id`, `arm`, `flow_id`, `rollout_pct_at_assign`, `complexity_tier`, `assigned_at` "+
				"FROM `task_qa_assignment` WHERE `root_origin` IN ("+placeholders+") ORDER BY `assigned_at` ASC, `epoch` ASC",
			args...,
		)
		if qErr != nil {
			return nil, fmt.Errorf("batch reading qa assignments: %w", qErr)
		}
		for rows.Next() {
			var a QAAssignment
			if sErr := rows.Scan(
				&a.RootOrigin, &a.Epoch, &a.RegimeID, &a.Arm, &a.FlowID,
				&a.RolloutPctAtAssign, &a.ComplexityTier, &a.AssignedAt,
			); sErr != nil {
				rows.Close()
				return nil, sErr
			}
			if _, exists := assignByRoot[a.RootOrigin]; !exists {
				assignByRoot[a.RootOrigin] = a
			}
		}
		if rErr := rows.Err(); rErr != nil {
			rows.Close()
			return nil, rErr
		}
		rows.Close()

		eRows, eErr := s.db.QueryContext(ctx,
			"SELECT `root_origin`, `arm_id`, `flow_hash`, `excluded_reason` "+
				"FROM `qa_enrollment` WHERE `root_origin` IN ("+placeholders+")",
			args...,
		)
		if eErr != nil {
			return nil, fmt.Errorf("batch reading qa enrollments: %w", eErr)
		}
		for eRows.Next() {
			var ro string
			var le lightEnroll
			if sErr := eRows.Scan(&ro, &le.armID, &le.flowHash, &le.excludedReason); sErr != nil {
				eRows.Close()
				return nil, sErr
			}
			enrollByRoot[ro] = le
		}
		if rErr := eRows.Err(); rErr != nil {
			eRows.Close()
			return nil, rErr
		}
		eRows.Close()
	}

	type lightRun struct {
		stage          string
		arm            string
		shadow         bool
		verdict        string
		commitSHA      string
		flowHash       string
		blastTier      string
		blastScore     int
		complexityTier string
		triageDecision string
	}
	runsByTask := make(map[string][]lightRun)
	for start := 0; start < len(taskIDs); start += batchChunk {
		chunk := taskIDs[start:min(start+batchChunk, len(taskIDs))]
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(chunk)), ",")
		args := make([]any, len(chunk))
		for i, v := range chunk {
			args[i] = v
		}
		rows, qErr := s.db.QueryContext(ctx,
			"SELECT `task_id`, `stage`, `arm`, `shadow`, `verdict`, COALESCE(`commit_sha`, ''), `flow_hash`, `blast_tier`, `blast_score`, `complexity_tier`, `triage_decision` "+
				"FROM `task_qa_check_run` WHERE `task_id` IN ("+placeholders+") ORDER BY `id` ASC",
			args...,
		)
		if qErr != nil {
			return nil, fmt.Errorf("batch reading qa check runs: %w", qErr)
		}
		for rows.Next() {
			var tid string
			var r lightRun
			var shadowInt int
			if sErr := rows.Scan(
				&tid, &r.stage, &r.arm, &shadowInt, &r.verdict, &r.commitSHA, &r.flowHash,
				&r.blastTier, &r.blastScore, &r.complexityTier, &r.triageDecision,
			); sErr != nil {
				rows.Close()
				return nil, sErr
			}
			r.shadow = shadowInt != 0
			runsByTask[tid] = append(runsByTask[tid], r)
		}
		if rErr := rows.Err(); rErr != nil {
			rows.Close()
			return nil, rErr
		}
		rows.Close()
	}

	verdictRank := func(v string) int {
		switch v {
		case "fail":
			return 4
		case "hold":
			return 3
		case "warn":
			return 2
		case "pass":
			return 1
		default:
			return 0
		}
	}
	hasQAHoldLink := func(t Task) bool {
		for _, l := range t.Links {
			if l.Kind == LinkQAHold {
				return true
			}
		}
		return false
	}
	complexityRank := func(tier string) int {
		switch strings.ToLower(strings.TrimSpace(tier)) {
		case QAComplexityHigh:
			return 3
		case QAComplexityMedium:
			return 2
		case QAComplexityLow:
			return 1
		default:
			return 0
		}
	}

	var specMap map[string]TaskSpecAttachments
	if assessComplexity != nil {
		var needSpecIDs []string
		for _, t := range tasks {
			if t.ID == "" {
				continue
			}
			ro := ResolveQARootOrigin(t.WorkOrigin, t.ID)
			enroll, hasEnroll := enrollByRoot[ro]
			if hasEnroll && (enroll.excludedReason != "" || enroll.armID == "" || enroll.armID == "excluded") {
				hasEnroll = false
			}
			assign, hasAssign := assignByRoot[ro]
			runs := runsByTask[t.ID]
			if len(runs) == 0 && hasAssign && assign.Epoch != qaCfg.EpochOrDefault() {
				hasAssign = false
			}
			if len(runs) > 0 || hasEnroll || (hasAssign && assign.Arm != "" && assign.Arm != QAArmOff) {
				hasRunTier := false
				for _, r := range runs {
					if r.complexityTier != "" {
						hasRunTier = true
						break
					}
				}
				if !hasRunTier {
					needSpecIDs = append(needSpecIDs, t.ID)
				}
				continue
			}
			st := states[t.ID]
			if st == StateClosed || !TaskEligibleForQAStaging(t) {
				continue
			}
			hasExplicitFlow := strings.TrimSpace(t.QAFlowRef) != ""
			if !hasExplicitFlow && (!qaCfg.Enabled || qaCfg.RolloutPct < 100) {
				continue
			}
			needSpecIDs = append(needSpecIDs, t.ID)
		}
		if len(needSpecIDs) > 0 {
			specMap, _ = s.TaskSpecAttachmentsMap(ctx, needSpecIDs, DefaultTaskSpecAttachmentMaxBytes)
		}
	}
	assessWithAttachments := func(t Task) string {
		if assessComplexity == nil {
			return ""
		}
		return assessComplexity(t, specMap[t.ID])
	}

	out := make(map[string]*TaskQASummary)
	for _, t := range tasks {
		ro := ResolveQARootOrigin(t.WorkOrigin, t.ID)
		enroll, hasEnroll := enrollByRoot[ro]
		if hasEnroll && (enroll.excludedReason != "" || enroll.armID == "" || enroll.armID == "excluded") {
			hasEnroll = false
		}
		assign, hasAssign := assignByRoot[ro]
		runs := runsByTask[t.ID]
		if len(runs) == 0 && hasAssign && assign.Epoch != qaCfg.EpochOrDefault() {
			hasAssign = false
		}

		if len(runs) > 0 || hasEnroll || (hasAssign && assign.Arm != "" && assign.Arm != QAArmOff) {
			arm := QAArmTreatment
			flowID := ""
			comp := ""
			hasRunComp := false
			if hasEnroll {
				if enroll.armID == QAArmControl || enroll.flowHash == "control" || enroll.flowHash == "" {
					arm = QAArmControl
				} else {
					arm = QAArmTreatment
					flowID = enroll.flowHash
				}
			} else if hasAssign {
				if assign.Arm != "" {
					arm = assign.Arm
				}
				flowID = assign.FlowID
				comp = assign.ComplexityTier
			} else {
				hasTreatmentRun := false
				hasControlRun := false
				for _, r := range runs {
					if !r.shadow && r.arm == QAArmTreatment {
						hasTreatmentRun = true
					}
					if r.arm == QAArmControl {
						hasControlRun = true
					}
				}
				if !hasTreatmentRun && hasControlRun {
					arm = QAArmControl
				}
			}
			stagesSeen := make(map[string]struct{}, 4)
			blastTier := ""
			blastScore := 0
			triage := ""
			verdict := ""
			latestCommitSHA := ""
			for _, r := range runs {
				if r.shadow && arm != QAArmControl {
					continue
				}
				if r.stage != "" {
					stagesSeen[r.stage] = struct{}{}
				}
				if flowID == "" && r.flowHash != "" {
					flowID = r.flowHash
				}
				if r.complexityTier != "" {
					if comp == "" || complexityRank(r.complexityTier) > complexityRank(comp) {
						comp = r.complexityTier
					}
					hasRunComp = true
				}
				if r.blastTier != "" {
					blastTier = r.blastTier
					blastScore = r.blastScore
				}
				if r.triageDecision != "" {
					triage = r.triageDecision
				}
				if r.verdict != "" {
					if r.commitSHA != "" && r.commitSHA != latestCommitSHA {
						latestCommitSHA = r.commitSHA
						verdict = r.verdict
					} else if r.commitSHA == latestCommitSHA && verdictRank(r.verdict) >= verdictRank(verdict) {
						verdict = r.verdict
					}
				}
			}
			if states[t.ID] != StateClosed && hasQAHoldLink(t) {
				verdict = "fail"
			}
			if triage == "" {
				for _, hr := range HumanReviewReasons(t) {
					if hr.Source == "qa-triage" {
						triage = "require_human"
						break
					}
				}
			}
			if !hasRunComp && assessComplexity != nil {
				hydrated := assessWithAttachments(t)
				if comp == "" || complexityRank(hydrated) > complexityRank(comp) {
					comp = hydrated
				}
			}
			if arm == QAArmControl {
				out[t.ID] = &TaskQASummary{
					FlowID:         "control",
					FlowLabel:      "Control",
					Arm:            QAArmControl,
					ComplexityTier: comp,
					BlastTier:      blastTier,
					BlastScore:     blastScore,
					TriageDecision: triage,
					Verdict:        verdict,
					StageCount:     len(stagesSeen),
				}
				continue
			}
			if flowID == "" && strings.TrimSpace(t.QAFlowRef) != "" {
				flowID = strings.TrimSpace(t.QAFlowRef)
			}
			if flowID == "" {
				flowID = qaCfg.FlowIDOrDefault()
			}
			canon := CanonicalQAFlowKey(flowID)
			out[t.ID] = &TaskQASummary{
				FlowID:         canon,
				FlowLabel:      HumanQAFlowLabel(flowID),
				Arm:            arm,
				ComplexityTier: comp,
				BlastTier:      blastTier,
				BlastScore:     blastScore,
				TriageDecision: triage,
				Verdict:        verdict,
				StageCount:     len(stagesSeen),
			}
			continue
		}

		st := states[t.ID]
		if st == StateClosed || !TaskEligibleForQAStaging(t) {
			continue
		}
		hasExplicitFlow := strings.TrimSpace(t.QAFlowRef) != ""
		if !hasExplicitFlow && (!qaCfg.Enabled || qaCfg.RolloutPct < 100) {
			continue
		}
		effFlow := qaCfg.FlowIDOrDefault()
		if hasExplicitFlow {
			effFlow = strings.TrimSpace(t.QAFlowRef)
		}
		comp := assessWithAttachments(t)
		out[t.ID] = &TaskQASummary{
			FlowID:         CanonicalQAFlowKey(effFlow),
			FlowLabel:      HumanQAFlowLabel(effFlow),
			Arm:            QAArmTreatment,
			ComplexityTier: comp,
			Pending:        true,
		}
	}
	return out, nil
}

// DefaultTaskSpecAttachmentMaxBytes bounds concatenated UTF-8 specification attachments read per task.
const DefaultTaskSpecAttachmentMaxBytes = 128 * 1024

// TaskSpecAttachments holds bounded plain-text/markdown attachment content for pre-dispatch complexity classification.
type TaskSpecAttachments struct {
	Text          string
	MarkdownCount int
}

// IsSpecTextAttachment reports whether an attachment is a plain-text or Markdown specification document
// (excluding HTML mocks, SVGs, images, PDFs, and binary archives).
func IsSpecTextAttachment(filename, contentType string) (isSpec bool, isMarkdown bool) {
	lowName := strings.ToLower(strings.TrimSpace(filename))
	lowCT := strings.ToLower(strings.TrimSpace(contentType))
	if idx := strings.IndexByte(lowCT, ';'); idx >= 0 {
		lowCT = strings.TrimSpace(lowCT[:idx])
	}
	for _, ext := range []string{
		".html", ".htm", ".svg", ".xml", ".png", ".jpg", ".jpeg", ".gif", ".webp", ".ico", ".pdf", ".zip", ".gz", ".tar",
	} {
		if strings.HasSuffix(lowName, ext) {
			return false, false
		}
	}
	if lowCT == "text/html" || strings.HasPrefix(lowCT, "image/") || strings.HasPrefix(lowCT, "audio/") ||
		strings.HasPrefix(lowCT, "video/") || lowCT == "application/pdf" || lowCT == "application/octet-stream" {
		return false, false
	}
	if strings.HasSuffix(lowName, ".md") || strings.HasSuffix(lowName, ".markdown") || lowCT == "text/markdown" || lowCT == "text/x-markdown" {
		return true, true
	}
	if strings.HasSuffix(lowName, ".txt") || lowCT == "text/plain" {
		return true, false
	}
	return false, false
}

// TruncateValidUTF8 truncates s to at most maxBytes without splitting multi-byte UTF-8 runes.
func TruncateValidUTF8(s string, maxBytes int) string {
	if maxBytes <= 0 || len(s) <= maxBytes {
		return s
	}
	for maxBytes > 0 && !utf8.RuneStart(s[maxBytes]) {
		maxBytes--
	}
	return s[:maxBytes]
}

// TrimIncompleteTrailingUTF8Bytes bounds raw to at most maxBytes without leaving a split multi-byte UTF-8 rune at the end.
func TrimIncompleteTrailingUTF8Bytes(raw []byte, maxBytes int) []byte {
	if maxBytes <= 0 || len(raw) <= maxBytes {
		return raw
	}
	b := raw[:maxBytes]
	for i := 1; i < utf8.UTFMax && i <= len(b); i++ {
		idx := len(b) - i
		if utf8.RuneStart(b[idx]) {
			if !utf8.FullRune(b[idx:]) {
				return b[:idx]
			}
			break
		}
	}
	return b
}

// UpdateQAAssignmentComplexityTier monotonically upgrades complexity_tier on the sticky task_qa_assignment row
// for (rootOrigin, epoch) after pre-agent attachment and repository path hydration refines the initial tier.
func (s *Store) UpdateQAAssignmentComplexityTier(ctx context.Context, rootOrigin string, epoch int, complexityTier string) error {
	ro := strings.TrimSpace(rootOrigin)
	tier := strings.TrimSpace(complexityTier)
	if s == nil || ro == "" || epoch < 1 || tier == "" {
		return nil
	}
	rank := func(v string) int {
		switch strings.ToLower(strings.TrimSpace(v)) {
		case QAComplexityHigh:
			return 3
		case QAComplexityMedium:
			return 2
		case QAComplexityLow:
			return 1
		default:
			return 0
		}
	}
	newRank := rank(tier)
	if newRank == 0 {
		return nil
	}
	return s.write(ctx, "update qa assignment complexity tier", func(tx *sql.Tx) error {
		var cur string
		err := tx.QueryRowContext(ctx,
			"SELECT `complexity_tier` FROM `task_qa_assignment` WHERE `root_origin` = ? AND `epoch` = ?",
			ro, epoch,
		).Scan(&cur)
		if errors.Is(err, sql.ErrNoRows) || rank(cur) >= newRank {
			return nil
		}
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx,
			"UPDATE `task_qa_assignment` SET `complexity_tier` = ? WHERE `root_origin` = ? AND `epoch` = ?",
			tier, ro, epoch,
		)
		return err
	})
}

// TaskSpecAttachments loads bounded plain-text and Markdown attachments for taskID.
func (s *Store) TaskSpecAttachments(ctx context.Context, taskID string, maxBytes int) (TaskSpecAttachments, error) {
	tid := strings.TrimSpace(taskID)
	if s == nil || tid == "" {
		return TaskSpecAttachments{}, nil
	}
	m, err := s.TaskSpecAttachmentsMap(ctx, []string{tid}, maxBytes)
	if err != nil {
		return TaskSpecAttachments{}, err
	}
	return m[tid], nil
}

// TaskSpecAttachmentsMap batch-loads bounded plain-text and Markdown attachments for taskIDs.
func (s *Store) TaskSpecAttachmentsMap(ctx context.Context, taskIDs []string, maxBytesPerTask int) (map[string]TaskSpecAttachments, error) {
	if s == nil || len(taskIDs) == 0 {
		return nil, nil
	}
	if maxBytesPerTask <= 0 {
		maxBytesPerTask = DefaultTaskSpecAttachmentMaxBytes
	}
	substrLimit := maxBytesPerTask + utf8.UTFMax
	out := make(map[string]TaskSpecAttachments)
	const batchChunk = 400
	for start := 0; start < len(taskIDs); start += batchChunk {
		chunk := taskIDs[start:min(start+batchChunk, len(taskIDs))]
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(chunk)), ",")
		args := make([]any, 0, len(chunk)+1)
		args = append(args, substrLimit)
		for _, id := range chunk {
			args = append(args, id)
		}
		rows, err := s.db.QueryContext(ctx,
			"SELECT `task_id`, `filename`, `content_type`, SUBSTR(`content`, 1, ?) "+
				"FROM `task_attachment` WHERE `task_id` IN ("+placeholders+") "+
				"AND (LOWER(`filename`) LIKE '%.md' OR LOWER(`filename`) LIKE '%.markdown' OR LOWER(`filename`) LIKE '%.txt' "+
				"OR LOWER(`content_type`) LIKE 'text/markdown%' OR LOWER(`content_type`) LIKE 'text/x-markdown%' OR LOWER(`content_type`) LIKE 'text/plain%') "+
				"ORDER BY `id` ASC",
			args...,
		)
		if err != nil {
			return nil, fmt.Errorf("batch reading task specification attachments: %w", err)
		}
		for rows.Next() {
			var tid, fname, ct string
			var raw []byte
			if sErr := rows.Scan(&tid, &fname, &ct, &raw); sErr != nil {
				rows.Close()
				return nil, sErr
			}
			isSpec, isMD := IsSpecTextAttachment(fname, ct)
			if !isSpec || len(raw) == 0 || slices.Contains(raw, 0) {
				continue
			}
			raw = TrimIncompleteTrailingUTF8Bytes(raw, maxBytesPerTask)
			if len(raw) == 0 || !utf8.Valid(raw) {
				continue
			}
			trimmed := strings.TrimSpace(string(raw))
			if trimmed == "" {
				continue
			}
			cur := out[tid]
			if isMD {
				cur.MarkdownCount++
			}
			if len(cur.Text) < maxBytesPerTask {
				joined := trimmed
				if cur.Text != "" {
					joined = cur.Text + "\n\n" + trimmed
				}
				cur.Text = TruncateValidUTF8(joined, maxBytesPerTask)
			}
			out[tid] = cur
		}
		if rErr := rows.Err(); rErr != nil {
			rows.Close()
			return nil, rErr
		}
		rows.Close()
	}
	return out, nil
}
