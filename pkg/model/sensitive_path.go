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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
)

// Sensitive paths (grain/task-1714): a gating `.grain/triggers` file may
// say `sensitive: true`, and name who may release it (`approvers:`).
// They are path triggers in every other respect (path_trigger.go); what
// this file adds is the one thing a trigger could not say before -- *who*
// a `human: required` hold waits for -- and the identity of the rules a
// hold was decided under.
//
// Who may release a hold is recorded on the held task itself, as one
// LinkApprovalRequired per rule that demands a named approver, and each
// approval as a LinkApprovedBy. ui.Client.Submit reads them: a Submit by
// somebody who approves none of the outstanding rules is refused, one by
// an approver records their approval, and auto-merge comes back only once
// every rule has one (PendingApprovals). The links are rewritten every
// time the change is judged again, so an approval is about the head it was
// given at.

// TemplateNone is the `template:` a sensitive trigger writes when it wants
// no automated review at all -- only a person, so only `human: required`.
const TemplateNone = "none"

// Approver tokens, as recorded in LinkApprovalRequired's target.
const (
	// ApproverTeamPrefix is a whole team ("team:payments"). A
	// single-tenant grain has no team members (single-tenant reset,
	// 3e-iv-b2c-ii), so IsApprover reads it as ApproverOwner, provided
	// the team is one it has (CheckApprover).
	ApproverTeamPrefix = "team:"
	// ApproverTeamAdminsPrefix is the admins of one team
	// ("team-admins:default") -- what a sensitive trigger with no
	// `approvers` defaults to, for the team of the task it holds. Read as
	// ApproverTeamPrefix is.
	ApproverTeamAdminsPrefix = "team-admins:"
	// ApproverOwner is this deployment's owner: any person, since
	// whoever holds the access code is the owner (docs/access-code.md).
	// Spelt as the system admins it once named, so the requirements
	// already recorded on held tasks keep reading the same.
	ApproverOwner = "system-admins"
)

// ApproverTeamAdmins is the token for teamID's admins.
func ApproverTeamAdmins(teamID string) string {
	if teamID == "" {
		teamID = DefaultTeamID
	}
	return ApproverTeamAdminsPrefix + teamID
}

// ApprovalRequirement is one rule a held change needs a named approver's
// Submit for.
type ApprovalRequirement struct {
	// Rule identifies what demanded it: "trigger:<name>" for a repository's
	// sensitive trigger, "builtin:<category>" for one of grain's own.
	Rule string
	// Approvers is who may satisfy it, as tokens (a user name, or one of
	// the Approver* forms above).
	Approvers []string
	// ApprovedBy is who did, or "" while it is outstanding.
	ApprovedBy string
}

// Rule prefixes.
const (
	ApprovalRuleTrigger = "trigger:"
	ApprovalRuleBuiltin = "builtin:"
)

// ApprovalRequiredLink is the link recording that rule wants one of
// approvers.
func ApprovalRequiredLink(rule string, approvers []string) Link {
	return Link{Kind: LinkApprovalRequired, Target: rule + "=" + strings.Join(approvers, ",")}
}

// ApprovalRequirements reads task's requirements back, in the order they
// were recorded, each with the approval it has (if any).
func ApprovalRequirements(task Task) []ApprovalRequirement {
	var out []ApprovalRequirement
	index := map[string]int{}
	for _, l := range task.Links {
		if l.Kind != LinkApprovalRequired {
			continue
		}
		rule, list, _ := strings.Cut(l.Target, "=")
		var approvers []string
		for _, a := range strings.Split(list, ",") {
			if a = strings.TrimSpace(a); a != "" {
				approvers = append(approvers, a)
			}
		}
		if i, ok := index[rule]; ok {
			out[i].Approvers = approvers
			continue
		}
		index[rule] = len(out)
		out = append(out, ApprovalRequirement{Rule: rule, Approvers: approvers})
	}
	for _, l := range task.Links {
		if l.Kind != LinkApprovedBy {
			continue
		}
		rule, user, _ := strings.Cut(l.Target, "=")
		if i, ok := index[rule]; ok {
			out[i].ApprovedBy = user
		}
	}
	return out
}

// PendingApprovals is ApprovalRequirements without the ones already
// approved. Non-empty means no automatic release may hand task its
// auto-merge back, and only an approver's Submit moves it.
func PendingApprovals(task Task) []ApprovalRequirement {
	var out []ApprovalRequirement
	for _, r := range ApprovalRequirements(task) {
		if r.ApprovedBy == "" {
			out = append(out, r)
		}
	}
	return out
}

// WithoutApprovalRules drops the requirements, and approvals, whose rule
// starts with prefix -- what a re-judgement does before it writes its own.
func WithoutApprovalRules(links []Link, prefix string) []Link {
	out := links[:0:0]
	for _, l := range links {
		if (l.Kind == LinkApprovalRequired || l.Kind == LinkApprovedBy) && strings.HasPrefix(l.Target, prefix) {
			continue
		}
		out = append(out, l)
	}
	return out
}

// DescribeApprover is a token as a reader would say it. Every token is
// the deployment's owner, as IsApprover reads it.
func DescribeApprover(string) string {
	return "a person holding the access code"
}

// DescribeApprovers joins DescribeApprover over tokens with "or", each
// description once.
func DescribeApprovers(tokens []string) string {
	var out []string
	for _, t := range tokens {
		if d := DescribeApprover(t); !slices.Contains(out, d) {
			out = append(out, d)
		}
	}
	if len(out) == 0 {
		return "nobody"
	}
	return strings.Join(out, " or ")
}

// IsApprover reports whether user is one of what token names. user is a
// person's name, or "" for any caller that is not a person (an agent, a
// sandbox or executor token), which approves nothing: a `human:
// required` hold is there to wait for a person. Every person is the
// deployment's owner -- whoever holds the access code is
// (docs/access-code.md) -- so a person is every approver a token can
// name: the owner, a team (a single-tenant grain has no team members,
// single-tenant reset 3e-iv-b2c-ii) or a user name, which on a grain
// with no accounts names no one else.
func (s *Store) IsApprover(_ context.Context, user, _ string) (bool, error) {
	return strings.TrimSpace(user) != "", nil
}

// resolveTeamID is the id of the team named by ref, or "" for none. The
// one team there is is the default one, named by its id or (case aside)
// by defaultTeamName, the name every build gave its row until schema 24
// dropped the team table.
func (s *Store) resolveTeamID(_ context.Context, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if strings.EqualFold(ref, DefaultTeamID) || strings.EqualFold(ref, defaultTeamName) {
		return DefaultTeamID, nil
	}
	return "", nil
}

// defaultTeamName is what the default team was called: an `approvers:`
// entry may still name it so.
const defaultTeamName = "admin"

// CheckApprover says what is wrong with an `approvers:` entry against this
// deployment: a team other than the default one, or an empty user. Any user name
// is accepted: the deployment has no accounts to check it against, and
// IsApprover lets the owner release whatever it names.
func (s *Store) CheckApprover(ctx context.Context, token string) error {
	if name, ok := strings.CutPrefix(token, ApproverTeamPrefix); ok {
		team, err := s.resolveTeamID(ctx, name)
		if err != nil {
			return err
		}
		if team == "" {
			return fmt.Errorf("%q names a team, and grain has no teams: write %q for the deployment's owner",
				token, ApproverOwner)
		}
		return nil
	}
	if strings.TrimSpace(strings.TrimPrefix(token, "user:")) == "" {
		return fmt.Errorf("an approver names no user")
	}
	return nil
}

// RepoRules is the identity of the rules a repository declared for its
// pull requests, as a hold records it beside trustboundary.Version: the
// base commit they were read at, and a hash of what they say (Hash, also
// alone what a clearance is pinned to -- a base that moves without
// touching its triggers does not unpin every clearance in the repository).
type RepoRules struct {
	Commit string
	Hash   string
	// Broken is the declaration sync's error when the base's trigger
	// files do not parse (TriggerDeclarationsBroken): every self-merging
	// change in the repository is then held for a human.
	Broken string
}

// String is the form a hold records (LinkRepoRules' target).
func (r RepoRules) String() string {
	s := "repo-rules@" + r.Commit + ":" + r.Hash
	if r.Broken != "" {
		s += ":broken"
	}
	return s
}

// Empty reports a repository that declares no triggers and has none
// failing to parse -- one that behaves exactly as before sensitive paths.
func (r RepoRules) Empty() bool { return r.Hash == "" && r.Broken == "" }

// RepoRulesOf is the identity of triggers (one repository's).
func RepoRulesOf(triggers []RepoPathTrigger) RepoRules {
	if len(triggers) == 0 {
		return RepoRules{}
	}
	sorted := append([]RepoPathTrigger(nil), triggers...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Trigger.Name < sorted[j].Trigger.Name })
	h := sha256.New()
	commit := ""
	for _, t := range sorted {
		rule, _ := json.Marshal(t.Trigger)
		h.Write(rule)
		h.Write([]byte{0})
		commit = t.Commit
	}
	return RepoRules{Commit: commit, Hash: "sha256-" + hex.EncodeToString(h.Sum(nil))[:16]}
}

// TriggerDeclarationsBroken reports whether a declaration sync's recorded
// error is (at least partly) a trigger file the base cannot parse, or one
// naming a template or approver this deployment does not have -- every
// such error names the file it is about, and every trigger file is under
// TriggerDeclarationDir.
//
// A merge-rules file (MergeRulesPaths) counts too: it decides what CI and
// whose Submit a change needs, so a base whose rules do not parse is held
// the same way, failing closed (grain/task-1659).
func TriggerDeclarationsBroken(lastError string) bool {
	if strings.Contains(lastError, TriggerDeclarationDir+"/") {
		return true
	}
	for _, p := range MergeRulesPaths {
		if strings.Contains(lastError, p) {
			return true
		}
	}
	return false
}
