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
	"strings"
	"time"
)

// QA experiment lifecycle states.
const (
	QAExpStateDraft    = "draft"
	QAExpStateRunning  = "running"
	QAExpStateStopped  = "stopped"
	QAExpStateArchived = "archived"
)

// Sentinel errors for qa_experiment operations.
var (
	ErrQAExperimentNotFound = errors.New("qa experiment not found")
	ErrQAExperimentConflict = errors.New("qa experiment revision or state conflict")
)

// QAExperimentArm defines one arm (control or treatment) of a QAExperiment.
type QAExperimentArm struct {
	ID         string `json:"id"`
	Label      string `json:"label"`
	FlowHash   string `json:"flowHash"`
	Weight     int    `json:"weight"`
	BaseBranch string `json:"baseBranch,omitempty"`
	Halted     bool   `json:"halted,omitempty"`
	HaltReason string `json:"haltReason,omitempty"`
	HaltedAt   string `json:"haltedAt,omitempty"`
}

// QAExperimentScope scopes an experiment to specific repositories (empty = all configured targetRepos).
type QAExperimentScope struct {
	Repos []string `json:"repos"`
}

// QAExperimentAllocation configures hash or explicit (blocked/paired) assignment.
type QAExperimentAllocation struct {
	Mode     string   `json:"mode"`
	Design   string   `json:"design,omitempty"`
	Seed     int64    `json:"seed,omitempty"`
	Strata   []string `json:"strata,omitempty"`
	ListHash string   `json:"listHash,omitempty"`
}

// QAExplicitAllocationEntry maps one opaque pre-generated key to an arm.
type QAExplicitAllocationEntry struct {
	Key     string `json:"key"`
	ArmID   string `json:"arm_id"`
	Stratum string `json:"stratum,omitempty"`
	PairID  string `json:"pair_id,omitempty"`
}

// QAExperimentPlan records pre-registered sample size, horizon, and alpha.
type QAExperimentPlan struct {
	PlannedOriginsPerArm int     `json:"plannedOriginsPerArm"`
	MaxDays              int     `json:"maxDays"`
	Alpha                float64 `json:"alpha"`
	MDEAck               bool    `json:"mdeAck"`
	HarmRefTau           float64 `json:"harmRefTau,omitempty"`
}

// QAExperimentOptions configures passive shadow scoring and repository memory isolation.
type QAExperimentOptions struct {
	ShadowOnControl bool   `json:"shadowOnControl"`
	Memory          string `json:"memory"`
}

// QAExperimentEngine pins the QAEngineRev active when the current segment started.
type QAExperimentEngine struct {
	QAEngineRev   int    `json:"qaEngineRev"`
	QAEngineHash  string `json:"qaEngineHash,omitempty"`
	DryRunViewed  bool   `json:"dryRunViewed,omitempty"`
	OverrideNotes string `json:"overrideNotes,omitempty"`
}

// QAExperimentDecision records the terminal decision when an experiment stops.
type QAExperimentDecision struct {
	Outcome   string `json:"outcome"`
	Arm       string `json:"arm,omitempty"`
	Reason    string `json:"reason"`
	DecidedBy string `json:"decidedBy,omitempty"`
	DecidedAt string `json:"decidedAt"`
}

// QAExperiment is a pre-registered quality-control experiment stored in qa_experiment.
type QAExperiment struct {
	ID              string                      `json:"id"`
	Revision        int                         `json:"revision"`
	Name            string                      `json:"name"`
	Hypothesis      string                      `json:"hypothesis"`
	State           string                      `json:"state"`
	Arms            []QAExperimentArm           `json:"arms"`
	Scope           QAExperimentScope           `json:"scope"`
	Allocation      QAExperimentAllocation      `json:"allocation"`
	Salt            string                      `json:"salt,omitempty"`
	AllocationList  []QAExplicitAllocationEntry `json:"allocationList,omitempty"`
	PrimaryMetric   string                      `json:"primaryMetric"`
	CoPrimaries     []string                    `json:"coPrimaries"`
	PrimaryContrast string                      `json:"primaryContrast"`
	Secondaries     []string                    `json:"secondaries"`
	Guardrails      []string                    `json:"guardrails"`
	Plan            QAExperimentPlan            `json:"plan"`
	Options         QAExperimentOptions         `json:"options"`
	Engine          QAExperimentEngine          `json:"engine"`
	Segment         int                         `json:"segment"`
	EnrollmentHeld  string                      `json:"enrollmentHeld"`
	Decision        *QAExperimentDecision       `json:"decision,omitempty"`
	Readout         json.RawMessage             `json:"readout,omitempty"`
	ReferenceArmID  string                      `json:"referenceArmId,omitempty"`
	CreatedBy       string                      `json:"createdBy"`
	CreatedAt       time.Time                   `json:"createdAt"`
	StartedAt       *time.Time                  `json:"startedAt,omitempty"`
	StoppedAt       *time.Time                  `json:"stoppedAt,omitempty"`
	ArchivedAt      *time.Time                  `json:"archivedAt,omitempty"`
}

// QAEnrollment records the immutable arm assignment of one work-tree root origin.
type QAEnrollment struct {
	RootOrigin     string    `json:"rootOrigin"`
	ExperimentID   string    `json:"experimentId"`
	Segment        int       `json:"segment"`
	ArmID          string    `json:"armId"`
	FlowHash       string    `json:"flowHash"`
	QAEngineRev    int       `json:"qaEngineRev"`
	Model          string    `json:"model,omitempty"`
	GrainBuild     string    `json:"grainBuild,omitempty"`
	AllocationKey  string    `json:"allocationKey,omitempty"`
	Bucket         *int      `json:"bucket,omitempty"`
	ExcludedReason string    `json:"excludedReason,omitempty"`
	AssignedAt     time.Time `json:"assignedAt"`
}

type qaExperimentMetricsBlob struct {
	PrimaryMetric   string   `json:"primaryMetric"`
	CoPrimaries     []string `json:"coPrimaries,omitempty"`
	PrimaryContrast string   `json:"primaryContrast"`
	Secondaries     []string `json:"secondaries,omitempty"`
	Guardrails      []string `json:"guardrails,omitempty"`
	ReferenceArmID  string   `json:"referenceArmId,omitempty"`
}

const qaExperimentColumns = "`id`, `revision`, `name`, `hypothesis`, `state`, `arms_json`, `scope_json`, " +
	"`allocation_json`, `salt`, `allocation_list_json`, `metrics_json`, `plan_json`, `options_json`, " +
	"`engine_json`, `segment`, `enrollment_held`, `decision_json`, `readout_json`, `created_by`, " +
	"`created_at`, `started_at`, `stopped_at`, `archived_at`"

func encodeQAExperimentBlobs(exp QAExperiment) (armsJ, scopeJ, allocJ, listJ, metricsJ, planJ, optsJ, engJ, decJ, readJ string, err error) {
	marshal := func(v any) (string, error) {
		b, mErr := json.Marshal(v)
		if mErr != nil {
			return "", mErr
		}
		return string(b), nil
	}
	if exp.Arms == nil {
		exp.Arms = []QAExperimentArm{}
	}
	if exp.Scope.Repos == nil {
		exp.Scope.Repos = []string{}
	}
	if armsJ, err = marshal(exp.Arms); err != nil {
		return
	}
	if scopeJ, err = marshal(exp.Scope); err != nil {
		return
	}
	if allocJ, err = marshal(exp.Allocation); err != nil {
		return
	}
	if len(exp.AllocationList) > 0 {
		if listJ, err = marshal(exp.AllocationList); err != nil {
			return
		}
	}
	mb := qaExperimentMetricsBlob{
		PrimaryMetric:   exp.PrimaryMetric,
		CoPrimaries:     exp.CoPrimaries,
		PrimaryContrast: exp.PrimaryContrast,
		Secondaries:     exp.Secondaries,
		Guardrails:      exp.Guardrails,
		ReferenceArmID:  exp.ReferenceArmID,
	}
	if metricsJ, err = marshal(mb); err != nil {
		return
	}
	if planJ, err = marshal(exp.Plan); err != nil {
		return
	}
	if optsJ, err = marshal(exp.Options); err != nil {
		return
	}
	if engJ, err = marshal(exp.Engine); err != nil {
		return
	}
	if exp.Decision != nil {
		if decJ, err = marshal(exp.Decision); err != nil {
			return
		}
	}
	if len(exp.Readout) > 0 {
		readJ = string(exp.Readout)
	}
	return
}

func scanQAExperiment(sc rowScanner) (QAExperiment, error) {
	var (
		exp                                                        QAExperiment
		armsJ, scopeJ, allocJ, listJ, metricsJ, planJ, optsJ, engJ string
		decJ, readJ                                                string
		startedAt, stoppedAt, archivedAt                           sql.NullTime
	)
	err := sc.Scan(
		&exp.ID, &exp.Revision, &exp.Name, &exp.Hypothesis, &exp.State,
		&armsJ, &scopeJ, &allocJ, &exp.Salt, &listJ, &metricsJ, &planJ, &optsJ,
		&engJ, &exp.Segment, &exp.EnrollmentHeld, &decJ, &readJ, &exp.CreatedBy,
		&exp.CreatedAt, &startedAt, &stoppedAt, &archivedAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return QAExperiment{}, ErrQAExperimentNotFound
		}
		return QAExperiment{}, err
	}
	exp.CreatedAt = exp.CreatedAt.UTC()
	exp.StartedAt = timePtr(startedAt)
	exp.StoppedAt = timePtr(stoppedAt)
	exp.ArchivedAt = timePtr(archivedAt)

	_ = json.Unmarshal([]byte(armsJ), &exp.Arms)
	_ = json.Unmarshal([]byte(scopeJ), &exp.Scope)
	_ = json.Unmarshal([]byte(allocJ), &exp.Allocation)
	if strings.TrimSpace(listJ) != "" {
		_ = json.Unmarshal([]byte(listJ), &exp.AllocationList)
	}
	var mb qaExperimentMetricsBlob
	if json.Unmarshal([]byte(metricsJ), &mb) == nil {
		exp.PrimaryMetric = mb.PrimaryMetric
		exp.CoPrimaries = mb.CoPrimaries
		exp.PrimaryContrast = mb.PrimaryContrast
		exp.Secondaries = mb.Secondaries
		exp.Guardrails = mb.Guardrails
		exp.ReferenceArmID = mb.ReferenceArmID
	}
	_ = json.Unmarshal([]byte(planJ), &exp.Plan)
	_ = json.Unmarshal([]byte(optsJ), &exp.Options)
	_ = json.Unmarshal([]byte(engJ), &exp.Engine)
	if strings.TrimSpace(decJ) != "" {
		var d QAExperimentDecision
		if json.Unmarshal([]byte(decJ), &d) == nil {
			exp.Decision = &d
		}
	}
	if strings.TrimSpace(readJ) != "" {
		exp.Readout = json.RawMessage(readJ)
	}
	if exp.CoPrimaries == nil {
		exp.CoPrimaries = []string{}
	}
	if exp.Secondaries == nil {
		exp.Secondaries = []string{}
	}
	if exp.Guardrails == nil {
		exp.Guardrails = []string{}
	}
	return exp, nil
}

// CreateQAExperiment inserts a new QAExperiment row (revision = 1).
func (s *Store) CreateQAExperiment(ctx context.Context, exp QAExperiment) error {
	if strings.TrimSpace(exp.ID) == "" {
		return fmt.Errorf("qa experiment id is required")
	}
	if exp.Revision < 1 {
		exp.Revision = 1
	}
	if exp.Segment < 1 {
		exp.Segment = 1
	}
	if exp.State == "" {
		exp.State = QAExpStateDraft
	}
	if exp.CreatedAt.IsZero() {
		exp.CreatedAt = time.Now().UTC()
	} else {
		exp.CreatedAt = exp.CreatedAt.UTC()
	}
	armsJ, scopeJ, allocJ, listJ, metricsJ, planJ, optsJ, engJ, decJ, readJ, err := encodeQAExperimentBlobs(exp)
	if err != nil {
		return err
	}
	return s.write(ctx, "create qa experiment "+exp.ID, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			"INSERT INTO `qa_experiment` ("+qaExperimentColumns+") VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)",
			exp.ID, exp.Revision, exp.Name, exp.Hypothesis, exp.State,
			armsJ, scopeJ, allocJ, exp.Salt, listJ, metricsJ, planJ, optsJ,
			engJ, exp.Segment, exp.EnrollmentHeld, decJ, readJ, exp.CreatedBy,
			exp.CreatedAt, exp.StartedAt, exp.StoppedAt, exp.ArchivedAt,
		)
		return err
	})
}

// GetQAExperiment reads one QAExperiment by ID.
func (s *Store) GetQAExperiment(ctx context.Context, id string) (QAExperiment, error) {
	row := s.db.QueryRowContext(ctx, "SELECT "+qaExperimentColumns+" FROM `qa_experiment` WHERE `id` = ?", strings.TrimSpace(id))
	return scanQAExperiment(row)
}

// GetRunningQAExperiment returns the single running QAExperiment, or (nil, nil) if none is running.
func (s *Store) GetRunningQAExperiment(ctx context.Context) (*QAExperiment, error) {
	row := s.db.QueryRowContext(ctx, "SELECT "+qaExperimentColumns+" FROM `qa_experiment` WHERE `state` = 'running' LIMIT 1")
	exp, err := scanQAExperiment(row)
	if err != nil {
		if errors.Is(err, ErrQAExperimentNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &exp, nil
}

// ListQAExperiments lists QAExperiment rows filtered by optional state and bounded by limit.
func (s *Store) ListQAExperiments(ctx context.Context, state string, limit int) ([]QAExperiment, error) {
	query := "SELECT " + qaExperimentColumns + " FROM `qa_experiment`"
	var args []any
	if st := strings.TrimSpace(state); st != "" {
		query += " WHERE `state` = ?"
		args = append(args, st)
	}
	query += " ORDER BY `created_at` DESC, `id` ASC"
	if limit > 0 {
		query += fmt.Sprintf(" LIMIT %d", limit)
	}
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []QAExperiment
	for rows.Next() {
		exp, err := scanQAExperiment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, exp)
	}
	return out, rows.Err()
}

// UpdateQAExperiment mutates an experiment atomically, checking expectedRev when > 0 and incrementing revision.
func (s *Store) UpdateQAExperiment(ctx context.Context, id string, expectedRev int, fn func(*QAExperiment) error) (QAExperiment, error) {
	var updated QAExperiment
	err := s.write(ctx, "update qa experiment "+id, func(tx *sql.Tx) error {
		row := tx.QueryRowContext(ctx, "SELECT "+qaExperimentColumns+" FROM `qa_experiment` WHERE `id` = ?", strings.TrimSpace(id))
		cur, err := scanQAExperiment(row)
		if err != nil {
			return err
		}
		if expectedRev > 0 && cur.Revision != expectedRev {
			return fmt.Errorf("%w: expected revision %d, got %d", ErrQAExperimentConflict, expectedRev, cur.Revision)
		}
		if err := fn(&cur); err != nil {
			return err
		}
		if cur.State == QAExpStateRunning {
			var otherID string
			rErr := tx.QueryRowContext(ctx,
				"SELECT `id` FROM `qa_experiment` WHERE `state` = 'running' AND `id` != ? LIMIT 1", cur.ID,
			).Scan(&otherID)
			if rErr == nil && otherID != "" {
				return fmt.Errorf("%w: experiment %s is already running", ErrQAExperimentConflict, otherID)
			}
		}
		cur.Revision++
		armsJ, scopeJ, allocJ, listJ, metricsJ, planJ, optsJ, engJ, decJ, readJ, err := encodeQAExperimentBlobs(cur)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx,
			"UPDATE `qa_experiment` SET `revision` = ?, `name` = ?, `hypothesis` = ?, `state` = ?, "+
				"`arms_json` = ?, `scope_json` = ?, `allocation_json` = ?, `salt` = ?, `allocation_list_json` = ?, "+
				"`metrics_json` = ?, `plan_json` = ?, `options_json` = ?, `engine_json` = ?, `segment` = ?, "+
				"`enrollment_held` = ?, `decision_json` = ?, `readout_json` = ?, `started_at` = ?, "+
				"`stopped_at` = ?, `archived_at` = ? WHERE `id` = ?",
			cur.Revision, cur.Name, cur.Hypothesis, cur.State,
			armsJ, scopeJ, allocJ, cur.Salt, listJ, metricsJ, planJ, optsJ,
			engJ, cur.Segment, cur.EnrollmentHeld, decJ, readJ,
			cur.StartedAt, cur.StoppedAt, cur.ArchivedAt, cur.ID,
		)
		if err != nil {
			return err
		}
		updated = cur
		return nil
	})
	return updated, err
}

const qaEnrollmentColumns = "`root_origin`, `experiment_id`, `segment`, `arm_id`, `flow_hash`, " +
	"`qa_engine_rev`, `model`, `grain_build`, `allocation_key`, `bucket`, `excluded_reason`, `assigned_at`"

func scanQAEnrollment(sc rowScanner) (QAEnrollment, error) {
	var (
		e      QAEnrollment
		bucket sql.NullInt64
	)
	err := sc.Scan(
		&e.RootOrigin, &e.ExperimentID, &e.Segment, &e.ArmID, &e.FlowHash,
		&e.QAEngineRev, &e.Model, &e.GrainBuild, &e.AllocationKey, &bucket, &e.ExcludedReason, &e.AssignedAt,
	)
	if err != nil {
		return QAEnrollment{}, err
	}
	e.AssignedAt = e.AssignedAt.UTC()
	if bucket.Valid {
		v := int(bucket.Int64)
		e.Bucket = &v
	}
	return e, nil
}

// GetQAEnrollment returns the sticky enrollment for rootOrigin, or (nil, nil) if not enrolled.
func (s *Store) GetQAEnrollment(ctx context.Context, rootOrigin string) (*QAEnrollment, error) {
	row := s.db.QueryRowContext(ctx,
		"SELECT "+qaEnrollmentColumns+" FROM `qa_enrollment` WHERE `root_origin` = ?",
		strings.TrimSpace(rootOrigin),
	)
	e, err := scanQAEnrollment(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &e, nil
}

// IsAllocationKeyUsed reports whether key has already been consumed by a non-excluded enrollment in experimentID.
func (s *Store) IsAllocationKeyUsed(ctx context.Context, experimentID, key string) (bool, error) {
	if strings.TrimSpace(key) == "" {
		return false, nil
	}
	var one int
	err := s.db.QueryRowContext(ctx,
		"SELECT 1 FROM `qa_enrollment` WHERE `experiment_id` = ? AND `allocation_key` = ? AND `arm_id` != 'excluded' LIMIT 1",
		experimentID, strings.TrimSpace(key),
	).Scan(&one)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// InsertQAEnrollment atomically enrolls rootOrigin if not already enrolled, returning the stored row and whether it was newly created.
func (s *Store) InsertQAEnrollment(ctx context.Context, candidate QAEnrollment) (QAEnrollment, bool, error) {
	candidate.RootOrigin = strings.TrimSpace(candidate.RootOrigin)
	if candidate.RootOrigin == "" {
		return QAEnrollment{}, false, fmt.Errorf("qa enrollment root_origin is required")
	}
	if candidate.Segment < 1 {
		candidate.Segment = 1
	}
	if candidate.AssignedAt.IsZero() {
		candidate.AssignedAt = time.Now().UTC()
	} else {
		candidate.AssignedAt = candidate.AssignedAt.UTC()
	}
	var (
		out     QAEnrollment
		created bool
	)
	err := s.write(ctx, "enroll qa origin "+candidate.RootOrigin, func(tx *sql.Tx) error {
		var bucketVal any
		if candidate.Bucket != nil {
			bucketVal = *candidate.Bucket
		}
		res, err := tx.ExecContext(ctx,
			"INSERT INTO `qa_enrollment` ("+qaEnrollmentColumns+") VALUES (?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT (`root_origin`) DO NOTHING",
			candidate.RootOrigin, candidate.ExperimentID, candidate.Segment, candidate.ArmID, candidate.FlowHash,
			candidate.QAEngineRev, candidate.Model, candidate.GrainBuild, candidate.AllocationKey, bucketVal, candidate.ExcludedReason, candidate.AssignedAt,
		)
		if err != nil {
			return err
		}
		affected, rErr := res.RowsAffected()
		if rErr == nil && affected > 0 {
			out = candidate
			created = true
			return nil
		}
		row := tx.QueryRowContext(ctx,
			"SELECT "+qaEnrollmentColumns+" FROM `qa_enrollment` WHERE `root_origin` = ?",
			candidate.RootOrigin,
		)
		existing, sErr := scanQAEnrollment(row)
		if sErr != nil {
			return sErr
		}
		out = existing
		created = false
		return nil
	})
	return out, created, err
}

// ListQAEnrollments returns all enrollments for experimentID ordered chronologically.
func (s *Store) ListQAEnrollments(ctx context.Context, experimentID string) ([]QAEnrollment, error) {
	rows, err := s.db.QueryContext(ctx,
		"SELECT "+qaEnrollmentColumns+" FROM `qa_enrollment` "+
			"WHERE `experiment_id` = ? ORDER BY `assigned_at` ASC, `root_origin` ASC",
		strings.TrimSpace(experimentID),
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []QAEnrollment
	for rows.Next() {
		e, err := scanQAEnrollment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// HoldRunningExperimentForEngineRev checks if a running experiment's QAEngineRev differs from currentRev
// and sets enrollment_held = 'engine-change' when an engine change occurred across restarts.
func (s *Store) HoldRunningExperimentForEngineRev(ctx context.Context, currentRev int) (*QAExperiment, error) {
	running, err := s.GetRunningQAExperiment(ctx)
	if err != nil || running == nil {
		return nil, err
	}
	if running.Engine.QAEngineRev == 0 || running.Engine.QAEngineRev == currentRev || running.EnrollmentHeld == "engine-change" {
		return nil, nil
	}
	updated, err := s.UpdateQAExperiment(ctx, running.ID, 0, func(exp *QAExperiment) error {
		if exp.State == QAExpStateRunning && exp.Engine.QAEngineRev != currentRev {
			exp.EnrollmentHeld = "engine-change"
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &updated, nil
}

// CheckMemoryFrozenForActor refuses memory writes from agent runs whose work tree is enrolled in a running
// experiment with options.memory == "frozen".
func (s *Store) CheckMemoryFrozenForActor(ctx context.Context, actor Principal) error {
	if s == nil || actor.Kind != PrincipalAgent || strings.TrimSpace(actor.ID) == "" {
		return nil
	}
	task, err := s.GetTask(ctx, strings.TrimSpace(actor.ID))
	if err != nil || task == nil {
		return nil
	}
	root := ResolveQARootOrigin(task.WorkOrigin, task.ID)
	enr, err := s.GetQAEnrollment(ctx, root)
	if err != nil || enr == nil || enr.ArmID == "excluded" {
		return nil
	}
	exp, err := s.GetQAExperiment(ctx, enr.ExperimentID)
	if err != nil {
		return nil
	}
	if exp.State == QAExpStateRunning && strings.EqualFold(exp.Options.Memory, "frozen") {
		return fmt.Errorf("Repo memory is read-only during experiment %s", exp.ID)
	}
	return nil
}
