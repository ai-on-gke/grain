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
	"fmt"
	"sort"
	"strconv"
	"time"
)

// A repository that has quietly stopped shipping, said out loud
// (grain/task-511).
//
// # The blind spot this is for
//
// grain starts two runs at a merged tip. The one that judges the tree is
// BaseBranchCI, which the merge queue reads and ui's basebranchgate.go
// next door reports on when nothing is judging a branch at all. The one
// that publishes is BaseBranchRelease, in a table the merge queue
// has never heard of -- and that is deliberate, ruled on by an operator
// and argued out in docs/merge-queue-behind.md: a gate whose own failure
// blocks the only route to fixing that gate deadlocks the deployment, so
// a failed release is recorded, logged, and left. No hold, no repair, no
// retry of the same commit.
//
// What that ruling left behind is this. The feedback path a broken
// publisher used to have was the merge queue stopping, which is how the
// incident before grain/task-230 was noticed at all -- releases had been
// failing on this deployment's own repository, and what a person saw was
// that nothing could merge. Removing the hold was right and it removed
// the only thing that ever said so: from then on the row was written
// every cycle and read by nothing a person sees. The failure mode is a
// repository that goes on merging perfectly happily and ships nothing,
// for as long as nobody happens to look at a release run.
//
// The operator's own words on the priority were *"not noticing a failed
// release is a 3rd order problem"*, and that is the size this is built
// to: a reading, off state grain has already written, on a poll that was
// being made anyway.
//
// # What it does not do
//
// It judges nothing, starts nothing and files nothing. It cannot hold
// the merge queue for the reason BaseBranchRelease cannot -- the
// queue reads a different table -- and it must not grow a repair task
// either: an agent sent at a failing publisher every cycle is the
// 25-minute image build this deployment declined to pay for. The repair
// is an operator's, as it is for every finding ui's basebranchgate.go makes.
//
// A single failed release is not a finding. One is noise -- this
// deployment's publisher has been flaky for weeks (grain/task-456) and a
// surface that fires on every red release is a surface that gets turned
// off -- so a finding needs either a run of them or a branch that has
// been shipping nothing for a day. A deployment whose releases are all
// fine renders none of this, which is the property that makes the ones
// it does render worth reading.

const (
	// BaseBranchPublishFailureRun is how many releases in a row have to
	// publish nothing before that is worth saying. Three, because one is
	// noise and two is a coincidence a flaky publisher produces most
	// weeks; by three, every merge since the first of them has shipped
	// nothing, and that is a statement about the repository rather than
	// about a run.
	BaseBranchPublishFailureRun = 3
	// BaseBranchNotPublishingFor is how long a branch may fail to publish
	// before the count stops mattering. It catches the repository the
	// rule above cannot: one that merges once or twice a week, whose
	// single failed release is the whole of the evidence there is and
	// which has still shipped nothing for a day.
	BaseBranchNotPublishingFor = 24 * time.Hour
)

// BaseBranchPublishing is one base branch grain has published nothing
// from for long enough to be worth telling somebody about.
//
// One per (repo, branch) it is true of, and only for those: a deployment
// whose releases are working reports none of these, exactly as
// ui.BaseBranchGate reports nothing about a branch that is being judged.
// The two are separate readings on purpose -- a branch nothing judges is
// a merge queue problem and a branch nothing publishes is not -- and a
// reader may well have one without the other.
type BaseBranchPublishing struct {
	// Repo is the repository, as `owner/name`, and Branch the base branch
	// grain has been publishing from.
	Repo   string `json:"repo"`
	Branch string `json:"branch"`
	// Failures is how many releases in a row have ended as anything but
	// success (BaseBranchRelease.Failures), counting an interrupted
	// one: what this measures is merges that published nothing, and a run
	// lost with the daemon under it published just as little as one whose
	// image build went red.
	Failures int `json:"failures"`
	// FailingSince is when the first of those was started, and
	// FailingForMS how long ago that is as of this reading. That instant
	// is the last time grain tried to publish this branch and got
	// anywhere, which is as close to "when did this repository last ship"
	// as the row can say.
	FailingSince *time.Time `json:"failingSince,omitempty"`
	FailingForMS int64      `json:"failingForMS,omitempty"`
	// Conclusion is how the newest *finished* release ended -- pkg/ci's
	// own word for it -- and is empty while a fresh one is in flight,
	// which is the ordinary state on a branch that keeps merging. The
	// streak above is what carries across that, which is why it and not
	// this is what the finding is made of.
	Conclusion string `json:"conclusion,omitempty"`
	// Commit is the tip the newest release was started for, and RunID
	// that run -- the one to open, whether it is still going or is the
	// latest to have failed.
	Commit string `json:"commit,omitempty"`
	RunID  string `json:"runID,omitempty"`
	// RunInFlight is whether that run has yet to conclude. A finding with
	// this set is not stale: the streak behind it is finished releases,
	// and this one may still clear it.
	RunInFlight bool `json:"runInFlight"`
	// TriageTaskID is the active release triage task investigating a release failure on this branch.
	TriageTaskID string `json:"triageTaskID,omitempty"`
	// Gated is whether subsequent releases on this branch are held pending resolution of TriageTaskID.
	Gated bool `json:"gated"`
	// Detail is the finding in the words a person needs, the contract
	// ui.BaseBranchGate.Detail and ui.SandboxGuestPublishing.Detail keep: what
	// is true, what it costs, and what is explicitly not held up by it.
	Detail string `json:"detail"`
}

// BaseBranchPublishingFindings is every base branch this deployment has
// stopped publishing from, as of now: the finding the deployment health
// response (and through it read_deployment_health) shows, and the one the
// orchestrator raises `pipeline.not-publishing` notifications from
// (design/design/durable-tasks.design.md, DT-5), so the two cannot disagree.
func (s *Store) BaseBranchPublishingFindings(ctx context.Context, now time.Time) ([]BaseBranchPublishing, error) {
	rows, err := s.ListBaseBranchRelease(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing base branch releases: %w", err)
	}
	configs, err := s.ListRepoConfigs(ctx)
	if err != nil {
		return nil, fmt.Errorf("reading repository settings: %w", err)
	}
	// Whether a merge into a repository publishes at all is one question
	// with one answer, and RunsWorkItem is where it is asked -- the same
	// expression orchestrator.baseBranchReleaseWanted starts releases from
	// (grain/task-508's work items). A repository with no row of its own
	// runs its releases, so it is absent from here and is read below as a
	// repository that is trying to publish.
	off := map[string]bool{}
	for i := range configs {
		if !RunsWorkItem(&configs[i], ReleaseItem) {
			off[configs[i].Repo.String()] = true
		}
	}
	var out []BaseBranchPublishing
	for _, row := range rows {
		// A repository whose releases an operator has switched off
		// publishes nothing on purpose, and its row stops moving where
		// the last release left it. Reporting that would be a warning
		// about a deployment doing exactly what it was told, which is
		// the mistake ui's basebranchgate.go warns against.
		if off[row.Repo.String()] {
			continue
		}
		if finding, ok := BaseBranchPublishingOf(row, now); ok {
			triageTag := BaseReleaseTriageBranchTag(row.Repo, row.Branch)
			if triage, err := s.OpenTaskWithTag(ctx, triageTag); err == nil && triage != nil {
				finding.TriageTaskID = triage.ID
				finding.Gated = true
				finding.Detail = baseBranchPublishingDetail(finding)
			}
			out = append(out, finding)
		}
	}
	// A stable order, so a pane redrawing on every poll does not shuffle
	// its own alerts under the reader's cursor.
	sort.Slice(out, func(i, j int) bool {
		if out[i].Repo != out[j].Repo {
			return out[i].Repo < out[j].Repo
		}
		return out[i].Branch < out[j].Branch
	})
	return out, nil
}

// BaseBranchPublishingOf reads one branch's row. ok is false when there
// is nothing to report about it, which is every branch on a deployment
// whose releases are publishing -- and every branch whose releases have
// only just started failing, which is the difference between a reading
// people keep and an alarm they silence.
func BaseBranchPublishingOf(row BaseBranchRelease, now time.Time) (BaseBranchPublishing, bool) {
	if !row.Failing() || row.FailingSince == nil {
		return BaseBranchPublishing{}, false
	}
	failingFor := max(now.Sub(*row.FailingSince), 0)
	if row.Failures < BaseBranchPublishFailureRun && failingFor < BaseBranchNotPublishingFor {
		return BaseBranchPublishing{}, false
	}
	since := *row.FailingSince
	finding := BaseBranchPublishing{
		Repo: row.Repo.String(), Branch: row.Branch,
		Failures: row.Failures, FailingSince: &since,
		FailingForMS: failingFor.Milliseconds(),
		Conclusion:   row.Conclusion, Commit: row.Commit,
		RunID: row.RunID, RunInFlight: row.InFlight(),
	}
	finding.Detail = baseBranchPublishingDetail(finding)
	return finding, true
}

// baseBranchPublishingDetail writes the finding out in the sentence that
// was missing: that this repository has shipped nothing since a
// particular moment, that nothing is holding it up, and that the repair
// is a person's.
//
// It says "nothing is held by this" in as many words on purpose. The
// reader most likely to act on this is one who remembers when a red
// release did stop the queue, and the worst thing this surface could do
// is talk them into putting that back.
func baseBranchPublishingDetail(p BaseBranchPublishing) string {
	branch := p.Branch
	if branch == "" {
		branch = "its default branch"
	}
	var b []string
	b = append(b, "Nothing has published from "+p.Repo+" "+branch+" since "+
		p.FailingSince.UTC().Format("2006-01-02 15:04 MST")+", "+
		publishingAge(time.Duration(p.FailingForMS)*time.Millisecond)+" ago: "+
		baseBranchPublishingCount(p)+".")
	b = append(b, "Nothing is held up by that -- the merge queue is gated on this branch's own"+
		" CI run and never on its release (docs/merge-queue-behind.md) -- so this repository is"+
		" merging changes and shipping none of them, and will go on doing so until somebody"+
		" looks at the publisher.")
	if p.Gated && p.TriageTaskID != "" {
		b = append(b, "Further releases on this branch are held pending resolution of triage task "+p.TriageTaskID+".")
	} else if p.RunInFlight && p.RunID != "" {
		b = append(b, "The release grain has in flight now is run "+p.RunID+"; if it publishes,"+
			" this clears itself.")
	} else if p.RunID != "" {
		b = append(b, "Run "+p.RunID+" is the latest attempt to read, and the branch's next merge"+
			" starts another -- no retry is owed to the commit that failed.")
	}
	return joinPublishingSentences(b)
}

// baseBranchPublishingCount is how many releases have published nothing
// and, where there is one to name, what the last of them ended as.
func baseBranchPublishingCount(p BaseBranchPublishing) string {
	count := "one release has been started since and it published nothing"
	if p.Failures > 1 {
		count = strconv.Itoa(p.Failures) + " releases have been started since and every one of" +
			" them published nothing"
	}
	if p.Conclusion != "" {
		count += ", the last of them ending " + p.Conclusion
	}
	return count
}

// publishingAge is d as the health page's other findings say an age: in
// its largest whole unit.
func publishingAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		return strconv.Itoa(int(d.Seconds())) + "s"
	case d < time.Hour:
		return strconv.Itoa(int(d.Minutes())) + "m"
	default:
		return strconv.Itoa(int(d.Hours())) + "h"
	}
}

// joinPublishingSentences puts a detail's pieces together with one space
// between them, dropping any that came back empty.
func joinPublishingSentences(parts []string) string {
	out := ""
	for _, part := range parts {
		if part == "" {
			continue
		}
		if out != "" {
			out += " "
		}
		out += part
	}
	return out
}
