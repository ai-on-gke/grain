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

package gates

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
)

type QAStageConfig struct {
	Stage        string `json:"stage"`
	FlowRevision string `json:"flow_revision"`
	Assignment   string `json:"assignment"`
}

// ReleaseQualificationConfig deliberately separates these predicates from
// qa_stage and from repository/nightly/publication health.
type ReleaseQualificationConfig struct {
	Predicate      string `json:"predicate"`
	ExpectedCommit string `json:"expected_commit"`
	Version        string `json:"release_version"`
	PolicyRevision string `json:"policy_revision"`
}

var releasePredicates = []string{"suite", "provenance", "soak", "person", "breaking_change", "signing_version", "publication_readiness", "promotion_policy"}
var releaseVersion = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+$`)
var exactDigest = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
var exactSHA = regexp.MustCompile(`^[0-9a-f]{40}$`)

type StageCheck struct {
	Phase                string
	Exempt               bool
	ApplicabilityVersion string
	Stage, ID, Severity  string
	Conditional          bool
}
type StageFinding struct{ ID, Version, Severity, Summary string }
type StageRun struct {
	Findings                                                                                  []StageFinding
	ID, Stage, CheckID, Head, FlowRevision, Arm, Experiment, Verdict, TriageDecision, Version string
	Engine                                                                                    int
	Shadow                                                                                    bool
}
type StageFacts struct {
	TaskID, Head, FlowRevision, Arm, Experiment, AssignmentVersion, PolicyVersion string
	Engine                                                                        int
	Shadow, Complete, Supported                                                   bool
	Reason                                                                        string
	Checks                                                                        []StageCheck
	Runs                                                                          []StageRun
	// A nil requirement is different from a proven exemption (false).
	S4Required             *bool
	S5Shadow               bool
	S5Enabled              bool
	HumanFloors            []string
	RepairDescription      string
	ObservedAt, ValidUntil time.Time
}
type ReleaseSuite struct {
	ID, Head, App, ExpectedApp, ExternalID, ExpectedExternalID, Status, Conclusion, Version string
	Complete                                                                                bool
}
type ReleasePerson struct {
	Identity, Kind, Decision, Commit, Version string
	Verified                                  bool
	IssuedAt, ExpiresAt                       time.Time
}
type ReleaseArtifact struct {
	Commit, Version, Digest string
	SignatureVerified       *bool
}
type ReleasePromotionPolicy struct {
	Veto, VersionOccupied, Complete bool
	PolicyHead, OccupiedHead        string
	Version                         string
}
type ReleaseFacts struct {
	PromotionPolicy                                                            *ReleasePromotionPolicy
	TaskID, Repository, Candidate, Head, Version, PolicyVersion, SourceVersion string
	PredicateReasons                                                           map[string]string
	Complete                                                                   bool
	Reason                                                                     string
	Suite                                                                      *ReleaseSuite
	// These optional values may only be supplied by a domain reader that
	// actually verified the corresponding fact. Tag prose is not a signature.
	Provenance             *bool
	RecordedSoakSeconds    *int64
	RequiredSoakSeconds    int64
	QualificationBased     bool
	Person                 *ReleasePerson
	BreakingAllowed        *bool
	Artifact               *ReleaseArtifact
	PublicationReady       *bool
	ObservedAt, ValidUntil time.Time
}

// QualificationCollection is an in-process value from ordinary domain reads,
// not an evaluator RPC or snapshot-transport protocol.
type QualificationCollection struct {
	StoreFence string
	QA         *StageFacts
	Release    *ReleaseFacts
}

func (c QualificationCollection) Bindings(taskID string) []Binding {
	out := Bindings(taskID)
	if f := c.QA; f != nil {
		flow := f.FlowRevision
		if flow == "" {
			flow = "unavailable"
		}
		assignment := "treatment"
		if f.Shadow || f.Arm == "control" || f.Arm == "baseline" {
			assignment = "shadow"
		}
		for _, stage := range []string{"s3", "s4", "s5"} {
			stageAssignment := assignment
			if stage == "s5" && f.S5Shadow {
				stageAssignment = "shadow"
			}
			raw, _ := json.Marshal(QAStageConfig{stage, flow, stageAssignment})
			out = append(out, Binding{ID: "qa-" + stage, Type: "qa_stage", Version: 1, Purpose: "monitor",
				Subject: Subject{Kind: "task", TaskID: taskID}, Config: raw, FreshnessSeconds: 60,
				Origin: Origin{Kind: "task", ID: taskID, Revision: Implementation}})
		}
	}
	if f := c.Release; f != nil {
		head, version, policy := f.Head, f.Version, f.PolicyVersion
		if !exactSHA.MatchString(head) {
			head = strings.Repeat("0", 40)
		}
		if version == "" {
			version = "v0.0.0"
		}
		if policy == "" {
			policy = "unavailable"
		}
		ref := f.Candidate
		if ref == "" {
			ref = "unavailable"
		}
		for _, predicate := range releasePredicates {
			raw, _ := json.Marshal(ReleaseQualificationConfig{predicate, head, version, policy})
			out = append(out, Binding{ID: "release-" + predicate, Type: "release_qualification", Version: 1, Purpose: "monitor",
				Subject: Subject{Kind: "repository", Repository: f.Repository, Ref: ref},
				Config:  raw, FreshnessSeconds: 60, Origin: Origin{Kind: "task", ID: taskID, Revision: Implementation}})
		}
	}
	return out
}

func qualificationResult(b Binding, taskID, version string, complete bool, observed, until, now time.Time, generation uint64, head string) Result {
	r := Evaluate(Bindings(taskID)[0], Facts{TaskID: taskID, SourceVersion: version, ObservedAt: observed, ValidUntil: until}, now, generation)
	r.Type, r.Version, r.BindingID = b.Type, b.Version, "task/"+taskID+"/"+b.ID
	r.ContextDigest = Digest(struct {
		Binding       Binding
		Source, Build string
	}{b, version, Implementation})
	r.EvaluationID = "shadow-" + r.ContextDigest[7:]
	r.Summary = "Qualification evidence is incomplete."
	var exactHead *string
	if head != "" {
		exactHead = &head
	}
	if r.ValidUntil.Before(now) {
		r.ValidUntil = now
	}
	r.Dependencies = []Dependency{{Key: r.BindingID, Subject: b.Subject, Head: exactHead, Version: version,
		CollectionGeneration: version, ScopeDigest: Digest(b.Subject), Complete: complete, ObservedAt: observed,
		ValidUntil: until, DirtyGeneration: generation, CoveredGeneration: generation}}
	return r
}

func block(r *Result, code, summary string) {
	if len(summary) > 1024 {
		summary = summary[:1024]
	}
	r.Blockers = append(r.Blockers, Blocker{Code: code, Summary: summary, Target: r.Dependencies[0].Subject})
}
func next(r *Result, text string) { text = "Shadow prediction only: " + text; r.HumanText = &text }
func qualificationReady(r *Result, b Binding, complete bool, reason string, observed, until, now time.Time) bool {
	switch {
	case b.Required || b.Purpose != "monitor" || b.Action != nil:
		block(r, "unsupported", "Qualification effects and enforcement are not activated.")
	case !complete:
		if reason == "" {
			reason = "Missing, denied, or incomplete source collection."
		}
		block(r, "incomplete", reason)
	case observed.After(now) || !now.Before(until):
		block(r, "stale", "Qualification evidence is stale or from a future clock; refresh required.")
	default:
		return true
	}
	r.Summary = r.Blockers[0].Summary
	next(r, "inspect the existing scoped evidence or retry the diagnostic read; no operational action is requested.")
	return false
}

func EvaluateStage(b Binding, f StageFacts, now time.Time, generation uint64) Result {
	version := Digest(f)
	r := qualificationResult(b, f.TaskID, version, f.Complete, f.ObservedAt, f.ValidUntil, now, generation, f.Head)
	var cfg QAStageConfig
	if _, err := Validate(b.Type, b.Version, b.Config); err != nil || json.Unmarshal(b.Config, &cfg) != nil {
		block(&r, "unsupported", "Unsupported or malformed QA configuration.")
		return r
	}
	if b.Type != "qa_stage" || b.Subject.Kind != "task" || b.Subject.TaskID != f.TaskID {
		block(&r, "permission", "QA evidence does not match this task subject.")
		r.Summary = r.Blockers[0].Summary
		return r
	}
	if !qualificationReady(&r, b, f.Complete, f.Reason, f.ObservedAt, f.ValidUntil, now) {
		return r
	}
	if !f.Supported || f.PolicyVersion == "" || f.FlowRevision == "" || f.AssignmentVersion == "" || f.Engine <= 0 || f.Arm == "" || !exactSHA.MatchString(f.Head) || cfg.FlowRevision != f.FlowRevision {
		block(&r, "incomplete", "Exact head, supported flow, engine, policy and assignment evidence are required.")
		if f.Reason != "" {
			block(&r, "unsupported", f.Reason)
		}
		r.Summary = r.Blockers[0].Summary
		next(&r, "inspect the existing QA assignment and stage evidence; no treatment is started.")
		return r
	}
	if cfg.Assignment == "treatment" && (f.Shadow || f.Arm == "control" || f.Arm == "baseline" || f.Arm == "excluded") {
		block(&r, "permission", "Control/shadow evidence cannot satisfy a treatment prerequisite.")
		r.Summary = r.Blockers[0].Summary
		return r
	}
	assignment := Evidence{Kind: "qa", ID: "assignment", Version: f.AssignmentVersion, Subject: b.Subject, Head: &f.Head, Digest: f.AssignmentVersion}
	r.Evidence = append(r.Evidence, assignment, Evidence{Kind: "policy", ID: f.FlowRevision, Version: f.PolicyVersion, Subject: b.Subject, Head: &f.Head, Digest: f.PolicyVersion})
	required := []StageCheck{}
	switch cfg.Stage {
	case "s3":
		for _, check := range f.Checks {
			if check.Phase != "s4" {
				required = append(required, check)
			}
		}
	case "s4":
		for _, check := range f.Checks {
			if check.Phase == "s4" {
				required = append(required, check)
			}
		}
		if f.S4Required == nil {
			block(&r, "incomplete", "S4 applicability lacks exact-head blast-tier evidence.")
		} else if *f.S4Required {
			required = append(required, StageCheck{Stage: "S4", ID: "golden:s4-orthogonal-verifier", Severity: "blocking"})
		}
	case "s5":
		if f.S5Enabled {
			required = []StageCheck{{Stage: "S5", ID: "qa:merge-triage", Severity: "blocking"}}
		}
	}
	latest := map[string]StageRun{}
	for _, run := range f.Runs {
		if run.Head != f.Head || run.FlowRevision != f.FlowRevision || run.Engine != f.Engine || run.Arm != f.Arm || run.Experiment != f.Experiment || run.Shadow != (f.Shadow || (cfg.Stage == "s5" && f.S5Shadow)) {
			continue
		}
		// The scoped reader supplies records in durable ID order.
		latest[strings.ToLower(run.Stage)+"/"+run.CheckID] = run
	}
	pending, failed, errored := false, false, false
	for _, check := range required {
		if check.Exempt {
			if check.ApplicabilityVersion == "" {
				block(&r, "incomplete", check.ID+": missing exact applicability proof.")
			} else {
				r.Evidence = append(r.Evidence, Evidence{Kind: "qa", ID: check.ID, Version: check.ApplicabilityVersion, Subject: b.Subject, Head: &f.Head, Digest: check.ApplicabilityVersion})
			}
			continue
		}
		run, ok := latest[strings.ToLower(check.Stage)+"/"+check.ID]
		if !ok || run.ID == "" || run.Version == "" {
			for i := len(f.Runs) - 1; i >= 0; i-- {
				candidate := f.Runs[i]
				if strings.EqualFold(candidate.Stage, check.Stage) && candidate.CheckID == check.ID && candidate.ID != "" && candidate.Version != "" {
					actualHead := candidate.Head
					var actual *string
					if actualHead != "" {
						actual = &actualHead
					}
					r.Evidence = append(r.Evidence, Evidence{Kind: "qa", ID: candidate.ID, Version: candidate.Version, Subject: b.Subject, Head: actual, Digest: candidate.Version})
					block(&r, "incomplete", fmt.Sprintf("%s/%s record #%s has flow %s, engine %d, arm %s, shadow=%t; its provenance does not match the required assignment.", check.Stage, check.ID, candidate.ID, candidate.FlowRevision, candidate.Engine, candidate.Arm, candidate.Shadow))
					break
				}
			}
			block(&r, "incomplete", fmt.Sprintf("%s/%s: no authentic result for this head/flow/engine/arm (conditional absence is not a pass).", check.Stage, check.ID))
			continue
		}
		r.Evidence = append(r.Evidence, Evidence{Kind: "qa", ID: run.ID, Version: run.Version, Subject: b.Subject, Head: &f.Head, Digest: run.Version})
		switch run.Verdict {
		case "pass", "warn", "shadow":
		case "pending", "running":
			pending = true
			block(&r, "unsatisfied", check.Stage+"/"+check.ID+": stage is still running.")
		case "fail", "hold", "failed", "canceled", "cancelled":
			failed = true
			block(&r, "unsatisfied", check.Stage+"/"+check.ID+": "+run.Verdict+".")
		case "error":
			errored = true
			block(&r, "unavailable", check.Stage+"/"+check.ID+": stage reported an error.")
		default:
			block(&r, "outcome_unknown", check.Stage+"/"+check.ID+": unsupported or missing stage disposition.")
		}
		for _, finding := range run.Findings {
			if (run.Verdict == "fail" || run.Verdict == "hold") && (finding.Severity == "blocking" || finding.Severity == "human" || finding.Severity == "error" || finding.Severity == "") {
				block(&r, "unsatisfied", check.Stage+"/"+check.ID+" finding #"+finding.ID+": "+finding.Summary)
				failed = true
			}
		}
		if cfg.Stage == "s5" {
			switch run.TriageDecision {
			case "require_human":
				failed = true
				block(&r, "person_required", "S5 requires eligible human triage; a pass row does not grant merge approval.")
			case "auto_merge":
			default:
				block(&r, "outcome_unknown", "S5 has no supported triage decision.")
			}
		}
	}

	if cfg.Stage == "s3" {
		for _, run := range f.Runs {
			if run.Head != f.Head || run.FlowRevision != f.FlowRevision || run.Engine != f.Engine || run.Arm != f.Arm || run.Experiment != f.Experiment || run.Shadow != f.Shadow || run.Verdict != "fail" {
				continue
			}
			if !slices.ContainsFunc(required, func(check StageCheck) bool {
				return strings.EqualFold(check.Stage, run.Stage) && check.ID == run.CheckID
			}) {
				continue
			}
			if current, ok := latest[strings.ToLower(run.Stage)+"/"+run.CheckID]; ok && current.ID != run.ID {
				failed = true
				block(&r, "unsatisfied", run.Stage+"/"+run.CheckID+" earlier same-head result #"+run.ID+" failed; legacy S3 retains this failure.")
				r.Evidence = append(r.Evidence, Evidence{Kind: "qa", ID: run.ID, Version: run.Version, Subject: b.Subject, Head: &f.Head, Digest: run.Version})
			}
		}
	}
	if cfg.Stage == "s5" {
		for _, floor := range f.HumanFloors {
			failed = true
			block(&r, "person_required", floor)
		}
	}
	r.Status = "met"
	r.Summary = "Shadow QA " + cfg.Stage + " predicate is met for the observed head; legacy authority is unchanged."
	if len(required) == 0 && len(r.Blockers) == 0 {
		r.Summary = "Shadow QA " + cfg.Stage + ": pinned flow proves this stage is not required."
	}
	if cfg.Assignment == "shadow" {
		r.Summary += " Control/shadow monitoring confers no treatment authority."
	}
	if len(r.Blockers) > 0 {
		switch {
		case errored:
			r.Status = "error"
			r.Error = &EvaluationError{Code: "unavailable", Message: "A recorded QA stage could not be evaluated.", Retryable: true}
		case failed:
			r.Status = "unmet"
		case pending:
			r.Status = "pending"
		default:
			r.Status = "unknown"
		}
		r.Summary = "Shadow QA " + cfg.Stage + ": " + fmt.Sprint(len(r.Blockers)) + " observed blocker(s); legacy QA remains authoritative."
		next(&r, "use existing bounded QA repair or eligible human triage. "+f.RepairDescription+" No repair, review, wait or promotion is requested.")
	}
	return r
}

func EvaluateRelease(b Binding, f ReleaseFacts, now time.Time, generation uint64) Result {
	version := Digest(f)
	r := qualificationResult(b, f.TaskID, version, f.Complete, f.ObservedAt, f.ValidUntil, now, generation, f.Head)
	var cfg ReleaseQualificationConfig
	if _, err := Validate(b.Type, b.Version, b.Config); err != nil || json.Unmarshal(b.Config, &cfg) != nil {
		block(&r, "unsupported", "Unsupported or malformed release configuration.")
		return r
	}
	if b.Type != "release_qualification" || b.Subject.Kind != "repository" || b.Subject.Repository != f.Repository || b.Subject.Ref != f.Candidate {
		block(&r, "permission", "Release evidence does not match this repository/candidate subject.")
		r.Summary = r.Blockers[0].Summary
		return r
	}
	if !qualificationReady(&r, b, f.Complete, f.Reason, f.ObservedAt, f.ValidUntil, now) {
		return r
	}
	if !exactSHA.MatchString(f.Head) || cfg.ExpectedCommit != f.Head || f.PolicyVersion == "" || cfg.PolicyRevision != f.PolicyVersion || !releaseVersion.MatchString(f.Version) || cfg.Version != f.Version {
		block(&r, "incomplete", "Exact RC head, release version and policy version are required.")
		r.Summary = r.Blockers[0].Summary
		return r
	}
	r.Evidence = append(r.Evidence, Evidence{Kind: "policy", ID: f.Version, Version: f.PolicyVersion, Subject: b.Subject, Head: &f.Head, Digest: f.PolicyVersion})
	state, reason := "unknown", "Required typed release evidence is unavailable."
	if f.PredicateReasons[cfg.Predicate] != "" {
		reason = f.PredicateReasons[cfg.Predicate]
	}
	switch cfg.Predicate {
	case "suite":
		if s := f.Suite; s != nil && s.Complete {
			digest := Digest(s)
			var actualHead *string
			if s.Head != "" {
				actualHead = &s.Head
			}
			r.Evidence = append(r.Evidence, Evidence{Kind: "check", ID: s.ID, Version: s.Version, Subject: b.Subject, Head: actualHead, Digest: digest})
			if s.Head != f.Head || s.ID == "" || s.Version == "" || s.ExpectedApp == "" || s.ExpectedExternalID == "" || s.App != s.ExpectedApp || s.ExternalID != s.ExpectedExternalID {
				state, reason = "unknown", "Qualification verdict lacks matching RC head, trusted issuer or exact attempt provenance."
			} else if s.Status != "completed" {
				state, reason = "pending", "The existing qualification suite is still running."
			} else {
				switch s.Conclusion {
				case "success":
					state, reason = "met", "The existing authenticated qualification verdict passed for this RC."
				case "failure":
					state, reason = "unmet", "The existing qualification verdict confirms failure after its bounded retries."
				case "neutral", "cancelled", "canceled", "timed_out":
					state, reason = "pending", "Qualification is infrastructure-blocked/canceled; this is not success."
				default:
					reason = "Qualification has no supported successful verdict."
				}
			}
		}
	case "provenance":
		if f.Provenance != nil {
			if *f.Provenance {
				state, reason = "met", "RC tag and immutable candidate provenance match."
			} else {
				state, reason = "unmet", "RC/head provenance does not match."
			}
		}
	case "soak":
		if f.QualificationBased {
			state, reason = "met", "Qualification-based candidate: the legacy soak prerequisite does not apply."
		} else if f.RecordedSoakSeconds != nil && f.RequiredSoakSeconds > 0 {
			if *f.RecordedSoakSeconds >= f.RequiredSoakSeconds {
				state, reason = "met", "Recorded candidate soak meets the pinned minimum."
			} else {
				state, reason = "unmet", "Recorded candidate soak is short; only an eligible person's explicit short-soak answer may authorize the legacy path."
			}
		}
	case "person":
		if p := f.Person; p != nil {
			if p.Kind != "human" || !p.Verified || strings.TrimSpace(p.Identity) == "" {
				state, reason = "unmet", "Only an identified, verified person may approve this release."
			} else if p.Commit != f.Head || p.Version != f.Version {
				state, reason = "unknown", "Person decision is for another RC head or version."
			} else if p.IssuedAt.After(now.Add(time.Minute)) || !now.Before(p.ExpiresAt) {
				state, reason = "unmet", "Person approval is expired or not yet valid."
			} else if p.Decision == "approve" {
				state, reason = "met", "A current verified person's approval covers this exact release head."
			} else {
				state, reason = "unmet", "This release has no positive person approval."
			}
		}
	case "breaking_change":
		if f.BreakingAllowed != nil {
			if *f.BreakingAllowed {
				state, reason = "met", "Pinned version/breaking-change policy is satisfied."
			} else {
				state, reason = "unmet", "Breaking-change/version policy requires an eligible person's explicit answer."
			}
		}
	case "signing_version":
		if a := f.Artifact; a != nil && a.SignatureVerified != nil {
			if !*a.SignatureVerified || a.Commit != f.Head || a.Version != f.Version || !exactDigest.MatchString(a.Digest) {
				state, reason = "unmet", "Artifact is unsigned, invalid, or belongs to another commit/version."
			} else {
				state, reason = "met", "Verified artifact signature, digest, version and RC head match."
			}
		}
	case "promotion_policy":
		if p := f.PromotionPolicy; p != nil {
			if p.PolicyHead != "" && p.Version != "" {
				subject := b.Subject
				subject.Ref = "main"
				r.Evidence = append(r.Evidence, Evidence{Kind: "policy", ID: "scripts/release.conf", Version: p.Version, Subject: subject, Head: &p.PolicyHead, Digest: p.Version})
			}
			if p.VersionOccupied && p.OccupiedHead != "" {
				subject := b.Subject
				subject.Ref = f.Version
				proof := Digest([]string{f.Version, p.OccupiedHead})
				r.Evidence = append(r.Evidence, Evidence{Kind: "integration", ID: f.Version, Version: proof, Subject: subject, Head: &p.OccupiedHead, Digest: proof})
			}
			if p.VersionOccupied {
				state, reason = "unmet", "This release version is already tagged and cannot be reused."
			}
			if p.Veto {
				block(&r, "unsatisfied", "The exact current-main RELEASE_NEVER_PROMOTE declaration vetoes this candidate/version.")
			}
			if !p.VersionOccupied {
				if !p.Complete || p.Version == "" {
					reason = "Effective release policy/overrides are not retained; inspect the exact committed policy and existing train state."
				} else if p.Veto {
					state, reason = "unmet", "The effective release policy vetoes this candidate/version."
				} else {
					state, reason = "met", "The complete pinned release promotion policy is satisfied."
				}
			}
		}
	case "publication_readiness":
		if f.PublicationReady != nil {
			if *f.PublicationReady {
				state, reason = "met", "Complete required target/asset set is ready for the exact RC; this is not publication."
			} else {
				state, reason = "unmet", "Required candidate assets or publication prerequisites are missing."
			}
		}
	}
	if state == "met" && cfg.Predicate == "person" && f.Person != nil && f.Person.ExpiresAt.Before(r.ValidUntil) {
		r.ValidUntil = f.Person.ExpiresAt
		r.Dependencies[0].ValidUntil = f.Person.ExpiresAt
	}
	if cfg.Predicate == "person" && f.Person != nil {
		proof := Digest(f.Person)
		r.Evidence = append(r.Evidence, Evidence{Kind: "approval", ID: "person", Version: proof, Subject: b.Subject, Head: &f.Head, Digest: proof})
	}
	if cfg.Predicate == "signing_version" && f.Artifact != nil {
		proof := Digest(f.Artifact)
		r.Evidence = append(r.Evidence, Evidence{Kind: "approval", ID: "artifact", Version: proof, Subject: b.Subject, Head: &f.Head, Digest: proof})
	}
	r.Status = state
	r.Summary = "Shadow release " + cfg.Predicate + ": " + reason
	if state != "met" {
		code := "unsatisfied"
		if state == "unknown" {
			code = "incomplete"
		}
		if cfg.Predicate == "person" {
			code = "person_required"
		}
		block(&r, code, reason)
		next(&r, "inspect the existing qualification/release evidence and obtain any required person's answer through the legacy flow. No release is cut, promoted, signed or published.")
	}
	if f.SourceVersion != "" {
		r.Evidence = append(r.Evidence, Evidence{Kind: "qa", ID: f.Candidate, Version: f.SourceVersion, Subject: b.Subject, Head: &f.Head, Digest: f.SourceVersion})
	}
	return r
}

func (c QualificationCollection) Evaluate(b Binding, now time.Time, generation uint64) Result {
	if b.Type == "qa_stage" && c.QA != nil {
		return EvaluateStage(b, *c.QA, now, generation)
	}
	if b.Type == "release_qualification" && c.Release != nil {
		return EvaluateRelease(b, *c.Release, now, generation)
	}
	return Result{}
}

// ValidateQualification checks just the typed profile. The common closed-object
// token decoder in Validate also rejects duplicate/case-folded/extra fields.
func validateQualification(typ string, raw []byte) (json.RawMessage, error) {
	if typ == "qa_stage" {
		var c QAStageConfig
		if err := json.Unmarshal(raw, &c); err != nil {
			return nil, err
		}
		if !slices.Contains([]string{"s3", "s4", "s5"}, c.Stage) || c.FlowRevision == "" || len(c.FlowRevision) > 256 || !slices.Contains([]string{"treatment", "shadow"}, c.Assignment) {
			return nil, fmt.Errorf("unsupported QA stage profile")
		}
		return json.Marshal(c)
	}
	var c ReleaseQualificationConfig
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, err
	}
	if !slices.Contains(releasePredicates, c.Predicate) || !exactSHA.MatchString(c.ExpectedCommit) || !releaseVersion.MatchString(c.Version) || len(c.Version) > 256 || c.PolicyRevision == "" || len(c.PolicyRevision) > 256 {
		return nil, fmt.Errorf("unsupported release qualification profile")
	}
	return json.Marshal(c)
}

// SourceVersion excludes collection clock heartbeats. Exact source revisions,
// complete-set state, head, flow, assignment and policy changes invalidate cache.
func (c QualificationCollection) SourceVersion() string {
	if c.QA != nil {
		f := *c.QA
		f.ObservedAt = time.Time{}
		f.ValidUntil = time.Time{}
		c.QA = &f
	}
	if c.Release != nil {
		f := *c.Release
		f.ObservedAt = time.Time{}
		f.ValidUntil = time.Time{}
		c.Release = &f
	}
	return Digest(c)
}

// UnavailableQualifications rebuilds rejected results from typed binding shapes
// only. It never carries accepted evidence, waits or predictions into a failure.
func UnavailableQualifications(taskID string, now time.Time, c QualificationCollection) ObservationView {
	v := UnavailableObservation(taskID, now)
	v.Bindings = c.Bindings(taskID)
	for _, b := range v.Bindings[2:] {
		r := qualificationResult(b, taskID, Implementation, false, now, now, now, 0, "")
		r.Dependencies[0].Head = nil
		r.Status = "error"
		r.Summary = "Qualification observation collection unavailable; legacy behavior is unaffected."
		r.Error = &EvaluationError{Code: "unavailable", Message: "Diagnostic read failed or timed out.", Retryable: true}
		retry := now.Add(time.Second)
		v.Results = append(v.Results, EvaluationState{EvaluationID: r.EvaluationID, Disposition: "rejected", Result: r, RetryAt: &retry})
	}
	return v
}

// BoundQualificationView prevents a custom flow/collection from multiplying the
// existing bounded history into unbounded payloads. It drops no legacy result.
func BoundQualificationView(v *ObservationView, now time.Time) {
	projection := ObservationView{Bindings: []Binding{}, Results: []EvaluationState{}}
	for i, b := range v.Bindings {
		if b.Type == "qa_stage" || b.Type == "release_qualification" {
			projection.Bindings = append(projection.Bindings, b)
			projection.Results = append(projection.Results, v.Results[i])
		}
	}
	data, _ := json.Marshal(projection)
	oversized := len(data) > 64*1024
	for i, b := range v.Bindings {
		if b.Type != "qa_stage" && b.Type != "release_qualification" {
			continue
		}
		prior := v.Results[i].Result
		if !oversized && len(prior.Evidence) <= 128 && len(prior.Blockers) <= 64 && len([]rune(prior.Summary)) <= 240 {
			continue
		}
		r := qualificationResult(b, b.Origin.ID, Implementation, false, now, now, now, v.Results[i].Revision, "")
		r.Dependencies[0].Head = nil
		r.Summary = "Qualification observation exceeds the persisted-view or canonical result limits; inspect the existing QA/release evidence."
		block(&r, "resource_limit", r.Summary)
		r.EvaluationID = v.Results[i].EvaluationID
		v.Results[i].Result = r
	}
}

// Invalidate retains the collected shape but cannot produce a positive result.
func (c QualificationCollection) Invalidate(reason string) QualificationCollection {
	if c.QA != nil {
		f := *c.QA
		f.Complete = false
		f.Reason = reason
		c.QA = &f
	}
	if c.Release != nil {
		f := *c.Release
		f.Complete = false
		f.Reason = reason
		c.Release = &f
	}
	return c
}

// SourceVersionAt treats a proof's validity boundary as a diagnostic source
// event, while ordinary collection-clock heartbeats remain coalesced.
func (c QualificationCollection) SourceVersionAt(now time.Time) string {
	qaExpired, releaseExpired, personExpired, personFuture := false, false, false, false
	if c.QA != nil {
		qaExpired = !now.Before(c.QA.ValidUntil) || c.QA.ObservedAt.After(now)
	}
	if c.Release != nil {
		releaseExpired = !now.Before(c.Release.ValidUntil) || c.Release.ObservedAt.After(now)
		if p := c.Release.Person; p != nil {
			personExpired = !now.Before(p.ExpiresAt)
			personFuture = p.IssuedAt.After(now.Add(time.Minute))
		}
	}
	return Digest(struct {
		Source                                                 string
		QAExpired, ReleaseExpired, PersonExpired, PersonFuture bool
	}{c.SourceVersion(), qaExpired, releaseExpired, personExpired, personFuture})
}
