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

package mcp

// Which repositories a run may read grain CI for (grain/task-172).
//
// list_ci_runs and get_ci_log answered about every repository this
// deployment remembers, whoever asked. That was never a decision: the
// filter vocabulary those two share with the CI pane (ci.RunFilter) was
// written to say which runs a reader is *asking* after and says so about
// itself -- "repo" was a convenience, not a boundary. This file is the
// boundary, and it is the same one every other tool on the roster
// already keeps: a run acts on its own work and reads about its own
// work. PullRequestScope fixes which branch of which repository
// pull_request_status may report on, GrainCIReader answers about this
// run's own task and no other, and daemonChat can only ever reach the
// conversation on the task the run belongs to. CI was the hole in that
// row.
//
// # What a run may read
//
// Its own repository -- the one its task targets, which is the only
// repository grain CI ever runs its branch in (orchestrator's
// reconcileTaskCI starts runs for task.Target and nothing else) -- and
// every branch of it, including the base branch its work has to merge
// into. That is the whole of what "is my change red, and is it red on
// its own or red like everything else here" needs, which is what these
// two tools exist for.
//
// A run holding the self-debug grant reads every repository's, because
// that grant already means "this task is here to work out what grain
// itself did": it carries read_grain_task and read_grain_task_transcript
// across every task in the deployment (pkg/capability/selfdebug), and a
// deployment-wide question about CI -- is this job flaky everywhere, did
// the guest image break every repository at once -- is the same kind of
// question asked of the same record. Naming it in the tool description
// is deliberate: a run that can see everything should know that is why
// it can, and a run that cannot should be told what it is missing rather
// than left to read an empty answer as a fact about CI.
//
// A run with no repository at all -- a task with no target, which is a
// real shape here rather than a misconfiguration -- may read nothing,
// and is told so in a sentence. Fail-closed is the only direction that
// fails safe: the cost of refusing is one answer a run can ask a person
// for, and the cost of the other default is that "no scope configured"
// silently means "all of them".
//
// # Refusing rather than answering empty
//
// A "repo" naming a repository outside the scope is refused with the one
// it may name, exactly as ParseRunState refuses "failed" with the words
// the vocabulary does have. An unknown word answered with an empty list
// is the single easiest way to get an answer that looks like "nothing is
// wrong", and that argument does not stop applying because the word is a
// repository rather than a conclusion.
//
// # Shared faults do not go round it
//
// Both tools annotate a red job with the other branches failing the same
// way (ci_faults.go, grain/task-299), and that annotation is fetched
// deployment-wide. It cannot name another repository's branches through
// this boundary: a failure's digest is taken over its repository as well
// as its output (ci.FailureSignature), so a fault only ever matches a
// job of the repository it was grouped in, and every ref and run id it
// names is one this reader may already read for itself. Nothing is
// fetched at all for a run with no failing job.
//
// get_ci_log takes a run id and no repository at all, so it is scoped
// after the lookup: the run is fetched, and a run belonging to somebody
// else is refused with a sentence saying so. Not a 404-shaped "no such
// run", which would be a plain falsehood about a run the reader may well
// have been given the id of, and which a reader would spend turns
// re-fetching and re-typing.

import (
	"fmt"
	"strings"

	"github.com/ai-on-gke/grain/pkg/ci"
)

// CIWideReadGrant is the capability whose holder reads every
// repository's CI rather than only its own task's.
//
// Restated here as a string rather than imported: pkg/capability/selfdebug
// imports pkg/mcp for the tools it grants, so pkg/mcp cannot import it
// back. cmd/grain holds both and keeps the two spellings equal
// (TestCIWideReadGrantIsSelfDebug), the same way daemonGrainCI keeps
// pkg/mcp's restatement of model.CIStatus honest.
const CIWideReadGrant = "self-debug"

// CIRepoScope is which repositories one run may read grain CI for. Its
// zero value grants nothing, which is what a caller that has not been
// told anything about the run should get.
type CIRepoScope struct {
	// Repo is this run's own repository as "owner/name" -- cmd/grain's
	// -pr-repo, the repository whose branch the run is pushing to. Empty
	// for a task with no repository of its own.
	Repo string
	// Reads is the extra repositories this task is allowed to read (task.Reads).
	Reads []string
	// AllRepos is the wide grant: every repository this deployment
	// remembers CI for, whoever ran it. See CIWideReadGrant.
	AllRepos bool
}

// permits is whether one repository's runs are this run's to read.
//
// An empty repo on a run is not permitted by a scoped reader: a stored
// run that records no repository cannot be shown to belong to this one,
// and the boundary is only worth having if the ambiguous case falls
// outside it.
func (s CIRepoScope) permits(repo string) bool {
	if s.AllRepos {
		return true
	}
	r := strings.TrimSpace(repo)
	if r == "" {
		return false
	}
	if s.Repo != "" && strings.EqualFold(r, strings.TrimSpace(s.Repo)) {
		return true
	}
	for _, readRepo := range s.Reads {
		if strings.EqualFold(r, strings.TrimSpace(readRepo)) {
			return true
		}
	}
	return false
}

// mayReadAny is whether this run may read any CI at all. False only for
// a run with no repository of its own, whose calls are refused before
// any lookup rather than after one -- a lookup that answered would be a
// fact about somebody else's run.
func (s CIRepoScope) mayReadAny() bool {
	return s.AllRepos || strings.TrimSpace(s.Repo) != "" || s.readsList() != ""
}

// defaultRepo is the single repository an unscoped call defaults to: Repo when
// the run has one of its own in CI scope, or its sole Read repository when it
// has none of its own and reads exactly one. Empty when AllRepos is set with no
// Repo, when the run reads nothing, or when it reads several repositories with
// none of its own to choose between.
func (s CIRepoScope) defaultRepo() string {
	if r := strings.TrimSpace(s.Repo); r != "" {
		return r
	}
	if s.AllRepos {
		return ""
	}
	var sole string
	for _, r := range s.Reads {
		r = strings.TrimSpace(r)
		if r == "" {
			continue
		}
		if sole == "" {
			sole = r
		} else if !strings.EqualFold(sole, r) {
			return ""
		}
	}
	return sole
}

// refuseAll is what such a run is told, with both ways out of it: have a
// repository, or hold the wide grant.
func (s CIRepoScope) refuseAll() error {
	return fmt.Errorf("this run has no repository of its own, so there is no grain CI it may read. "+
		"A run reads its own repository's runs; a task holding the %s grant reads every repository's",
		CIWideReadGrant)
}

// refuseMissingRepo is what a caller omitting "repo" is told when the run has
// no own repository in CI scope and may read more than one repository, so an
// unfiltered query across the deployment cannot be sent on its behalf.
func (s CIRepoScope) refuseMissingRepo() error {
	return fmt.Errorf("specify \"repo\" (%s): this run has no repository of its own in CI scope and may read more than one",
		s.readsList())
}

// narrow is the scope applied to one list_ci_runs question: the filter a
// caller asked, pinned to what this run may see, or the refusal to hand
// back instead.
//
// A caller that named no repository gets its own (or its sole readable
// repository), rather than every repository's runs filtered down afterwards.
// Pinning it into the filter is what makes the answer's own heading honest --
// the list says "Filter: repo acme/widgets" above itself, so a reader can see
// what it was narrowed to without being told separately.
func (s CIRepoScope) narrow(f ci.RunFilter) (ci.RunFilter, error) {
	if s.AllRepos {
		return f, nil
	}
	if !s.mayReadAny() {
		return ci.RunFilter{}, s.refuseAll()
	}
	if f.Repo != "" && !s.permits(f.Repo) {
		return ci.RunFilter{}, s.refuseRepo(f.Repo)
	}
	// A repository the caller named and may read -- one of Reads -- is
	// kept as asked rather than swapped for this run's own.
	if f.Repo == "" {
		def := s.defaultRepo()
		if def == "" {
			return ci.RunFilter{}, s.refuseMissingRepo()
		}
		f.Repo = def
	}
	return f, nil
}

// keepInScope drops any run from an answer that this run may not read,
// and takes the same number off what the answer claims matched.
//
// Belt and braces, deliberately. narrow pins the repository into the
// filter and the deployment answers it (ci.RunStore.Find), so in a
// working deployment this drops nothing -- but the pinning is an
// argument sent over HTTP to another process, and a boundary that is
// only as good as the far end's agreement to honour it is not one.
// Enforcing it again where the answer is rendered costs a comparison
// per run and makes the guarantee local to the thing that promises it.
//
// The counts move with the runs rather than being left as the daemon
// reported them: a heading saying twelve matched above nine runs would
// be an invitation to raise "limit" for three that will never come.
// Stored is left alone, since it is deliberately deployment-wide and is
// what tells an empty answer from an empty deployment.
func (s CIRepoScope) keepInScope(page ci.RunPage) ci.RunPage {
	kept := make([]ci.Run, 0, len(page.Runs))
	for _, run := range page.Runs {
		if s.permits(run.Repo) {
			kept = append(kept, run)
		}
	}
	page.Matched -= len(page.Runs) - len(kept)
	if page.Matched < len(kept) {
		page.Matched = len(kept)
	}
	page.Runs = kept
	return page
}

// refuseRepo is what a caller naming a repository outside the scope is
// told: which one it may name, and what the other answer would have
// been.
func (s CIRepoScope) refuseRepo(repo string) error {
	if reads := s.readsList(); reads != "" {
		own := ""
		if s.Repo != "" {
			own = s.Repo + ", its own repository, "
		}
		leaveOut := ""
		if s.defaultRepo() != "" {
			leaveOut = ", or leave \"repo\" out"
		}
		return fmt.Errorf("%q is not a repository this run may read CI for: use %s%s, which it may read%s. "+
			"(A task holding the %s grant reads every repository's runs; this one does not.)",
			repo, own, reads, leaveOut, CIWideReadGrant)
	}
	return fmt.Errorf("%q is not a repository this run may read CI for: use %s, its own repository, "+
		"or leave \"repo\" out. (A task holding the %s grant reads every repository's runs; this one does not.)",
		repo, s.Repo, CIWideReadGrant)
}

// refuseRun is what get_ci_log says about a run belonging to another
// repository -- named, so that a reader who was handed the id by a
// person or by an older note knows why it is refused rather than
// wondering whether the run is gone.
func (s CIRepoScope) refuseRun(run ci.Run) error {
	whose := "records no repository"
	if run.Repo != "" {
		whose = "is " + run.Repo + "'s"
	}
	return fmt.Errorf("grain CI run %s %s, and this run may only read CI for %s -- so this is a refusal, "+
		"not a run that does not exist. list_ci_runs reports the runs it may read", run.ID, whose, s.readable())
}

// listedRepos is the clause list_ci_runs' first sentence uses to say
// what it is a list of. It names the wide grant in both directions: a
// run that holds it should know that is why it can see everything, and a
// run that does not should know what the boundary is rather than reading
// an empty answer as a fact about CI.
func (s CIRepoScope) listedRepos() string {
	switch {
	case s.AllRepos:
		return fmt.Sprintf("across all repositories and branches -- every repository, because this run holds the %s grant", CIWideReadGrant)
	case s.readsList() != "" && s.Repo != "":
		return fmt.Sprintf("for %s, this run's own repository, and %s, which it may read, across all of their branches -- any other repository's runs are not this run's to read, and naming one is refused rather than answered empty", s.Repo, s.readsList())
	case s.readsList() != "":
		return fmt.Sprintf("for %s, which this run may read, across all of their branches -- this run has no repository of its own, any other repository's runs are not this run's to read, and naming one is refused rather than answered empty", s.readsList())
	case s.Repo != "":
		return fmt.Sprintf("for %s, this run's own repository, across all of its branches -- another repository's runs are not this run's to read, and naming one is refused rather than answered empty", s.Repo)
	default:
		return fmt.Sprintf("for no repository at all: this run has none of its own, so every call is refused. A run reads its own repository's CI, and a task holding the %s grant reads every repository's", CIWideReadGrant)
	}
}

// repoArgument is the "repo" argument's own description, which says
// which repositories the word may name here.
func (s CIRepoScope) repoArgument() string {
	switch {
	case s.AllRepos:
		return "one repository, as \"owner/name\" -- any this deployment remembers"
	case s.readsList() != "" && s.Repo != "":
		return fmt.Sprintf("one repository, as \"owner/name\": %s, which is this run's own, or %s, which it may read. "+
			"Leaving it out means %s; naming any other is refused", s.Repo, s.readsList(), s.Repo)
	case s.readsList() != "":
		return fmt.Sprintf("one repository, as \"owner/name\": %s, which this run may read. Naming any other is refused", s.readsList())
	case s.Repo != "":
		return fmt.Sprintf("one repository, as \"owner/name\": %s, which is this run's own and the only one it may read. "+
			"Leaving it out means the same thing; naming any other is refused", s.Repo)
	default:
		return "one repository, as \"owner/name\" -- this run may name none"
	}
}

// logScopeSentence is what get_ci_log's description says about whose
// runs it will read, since that tool takes a run id and no repository
// and so cannot say it through an argument.
func (s CIRepoScope) logScopeSentence() string {
	switch {
	case s.AllRepos:
		return fmt.Sprintf(" This run holds the %s grant, so any remembered run's log may be read.", CIWideReadGrant)
	case s.readsList() != "":
		return fmt.Sprintf(" It reads the runs of %s: a run id belonging to any other repository is refused with a sentence saying so.", s.readable())
	case s.Repo != "":
		return fmt.Sprintf(" It reads %s's runs, this run's own repository: a run id belonging to another repository is refused with a sentence saying so.", s.Repo)
	default:
		return fmt.Sprintf(" This run has no repository of its own, so there is no run it may read: a task reads its own repository's CI, and a task holding the %s grant reads every repository's.", CIWideReadGrant)
	}
}

// describeFilter is the filter as the answer's own heading says it, with
// the repository marked as the boundary it is rather than as something
// the caller chose.
//
// The heading matters more here than anywhere else in the answer,
// because the counts beside it are deployment-wide: "2 of 40 remembered
// runs match" on a scoped run invites a reader to widen a filter they
// never set and cannot widen. Naming it as theirs -- and as the only one
// -- is what turns that into a fact they can act on.
func (s CIRepoScope) describeFilter(f ci.RunFilter) string {
	text := f.Describe()
	if s.AllRepos || f.Repo == "" {
		return text
	}
	mark := ""
	switch {
	case s.Repo != "" && strings.EqualFold(strings.TrimSpace(f.Repo), strings.TrimSpace(s.Repo)):
		mark = "this run's own, and the only one it may read"
		if reads := s.readsList(); reads != "" {
			mark = "this run's own; it may also read " + reads
		}
	case s.permits(f.Repo):
		mark = "one this run may read, besides its own"
		if s.Repo == "" {
			mark = "one this run may read"
		}
	default:
		return text
	}
	return strings.Replace(text, "repo "+f.Repo, "repo "+f.Repo+" ("+mark+")", 1)
}

// readsList is the repositories this run may read besides its own, as
// one phrase -- empty when there are none, which is every run whose task
// declares no reads and whose every sentence above then reads exactly as
// it did before reads were honoured here.
func (s CIRepoScope) readsList() string {
	var names []string
	for _, r := range s.Reads {
		r = strings.TrimSpace(r)
		if r == "" || strings.EqualFold(r, strings.TrimSpace(s.Repo)) {
			continue
		}
		names = append(names, r)
	}
	return strings.Join(names, ", ")
}

// readable is every repository this run may read CI for, own first.
func (s CIRepoScope) readable() string {
	reads := s.readsList()
	switch {
	case reads == "":
		return s.Repo
	case s.Repo == "":
		return reads
	default:
		return s.Repo + ", " + reads
	}
}
