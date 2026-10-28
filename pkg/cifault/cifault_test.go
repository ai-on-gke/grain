package cifault_test

// The judgement this package makes is read by three different audiences
// -- the CI pane, an agent's wait_for_checks, and the verdict comment on
// a red pull request -- so it is tested here, once, rather than in each
// of them.

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/cifault"
	"github.com/ai-on-gke/grain/pkg/model"
)

// fakeStore is cifault.Store with its answers written down: which pull
// requests are open, and whose task each belongs to.
type fakeStore struct {
	links []model.TaskPullRequestLink
	tasks map[string]*model.Task
	err   error
}

func (s *fakeStore) OpenPullRequestLinks(context.Context) ([]model.TaskPullRequestLink, error) {
	return s.links, s.err
}

func (s *fakeStore) GetTask(_ context.Context, id string) (*model.Task, error) {
	return s.tasks[id], nil
}

// open records a finished task with an open pull request on it -- the
// only kind of change a fault is ever allowed to name.
func (s *fakeStore) open(taskID, pullRequest, title string) {
	if s.tasks == nil {
		s.tasks = map[string]*model.Task{}
	}
	s.links = append(s.links, model.TaskPullRequestLink{TaskID: taskID, PullRequest: pullRequest})
	s.tasks[taskID] = &model.Task{ID: taskID, Title: title}
}

// fakeFiles is cifault.FileLister, counting how often it is asked.
type fakeFiles struct {
	files map[int][]string
	err   error
	calls int
}

func (f *fakeFiles) ListPullRequestFiles(_, _ string, number int) ([]string, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return f.files[number], nil
}

// mirrorFault is the failure the whole feature was written for: one
// cause printing on two branches, both of them naming the same file.
func mirrorFault() ci.SharedFault {
	return ci.SharedFault{
		Repo:  "acme/widgets",
		JobID: "image",
		Refs:  []string{"grain/task-238", "grain/task-246"},
		Paths: []string{"scripts/granule/guest-inputs.sh"},
	}
}

func TestFixesNamesTheOpenChangeThatTouchesTheFile(t *testing.T) {
	t.Parallel()
	store := &fakeStore{}
	store.open("26", "acme/widgets#26", "Stop the guest build reading the apt mirror")
	// An open change that touches something else entirely must not be
	// offered as the fix for this.
	store.open("31", "acme/widgets#31", "Redraw the runs table")
	files := &fakeFiles{files: map[int][]string{
		26: {"scripts/granule/guest-inputs.sh"},
		31: {"ui/src/state.js"},
	}}

	fixes := cifault.Fixes(context.Background(), store, files, mirrorFault())
	if len(fixes) != 1 {
		t.Fatalf("want exactly the change that touches the file, got %+v", fixes)
	}
	if fixes[0].PullRequest != "acme/widgets#26" || fixes[0].Task != "26" {
		t.Fatalf("named the wrong change: %+v", fixes[0])
	}
	if fixes[0].Title != "Stop the guest build reading the apt mirror" {
		t.Errorf("a reader cannot see what the change is: %+v", fixes[0])
	}
	if got := fixes[0].Where(); got != "scripts/granule/guest-inputs.sh" {
		t.Errorf("the claim does not say why it was made: %q", got)
	}
}

// A branch that is red with the fault plainly does not fix it, however
// well its own change matches the file.
func TestFixesDoesNotNameABranchTheFaultIsOn(t *testing.T) {
	t.Parallel()
	store := &fakeStore{}
	store.open("246", "acme/widgets#26", "Something about the guest inputs")
	files := &fakeFiles{files: map[int][]string{26: {"scripts/granule/guest-inputs.sh"}}}

	if fixes := cifault.Fixes(context.Background(), store, files, mirrorFault()); len(fixes) != 0 {
		t.Fatalf("a branch failing the fault was named as its fix: %+v", fixes)
	}
}

// Newest task first, where task ids are the store's increasing decimal
// strings: 1000 is newer than 999 even though it sorts lower as text.
func TestFixesNamesTheNewestTaskFirst(t *testing.T) {
	t.Parallel()
	store := &fakeStore{}
	store.open("999", "acme/widgets#26", "The older change")
	store.open("1000", "acme/widgets#27", "The newer change")
	files := &fakeFiles{files: map[int][]string{
		26: {"scripts/granule/guest-inputs.sh"},
		27: {"scripts/granule/guest-inputs.sh"},
	}}

	fixes := cifault.Fixes(context.Background(), store, files, mirrorFault())
	if len(fixes) != 2 || fixes[0].Task != "1000" || fixes[1].Task != "999" {
		t.Fatalf("want task 1000 then task 999, got %+v", fixes)
	}
}

// A popular file is not a diagnosis: past MaxFixes candidates the answer
// is silence, and the fault itself is still said by the caller.
func TestFixesSaysNothingWhenTooManyChangesTouchTheFile(t *testing.T) {
	t.Parallel()
	store := &fakeStore{}
	files := &fakeFiles{files: map[int][]string{}}
	for i := 1; i <= cifault.MaxFixes+1; i++ {
		store.open(fmt.Sprintf("t%d", i), fmt.Sprintf("acme/widgets#%d", i), "A change")
		files.files[i] = []string{"scripts/granule/guest-inputs.sh"}
	}

	if fixes := cifault.Fixes(context.Background(), store, files, mirrorFault()); len(fixes) != 0 {
		t.Fatalf("%d candidates were offered as a fix: %+v", len(fixes), fixes)
	}
}

// Nothing is asked of GitHub where there is nothing to match on: a fault
// whose branches agreed on no file (a network outage, say) has no fix to
// look for.
func TestFixesAsksNothingOfAFaultThatNamesNoFile(t *testing.T) {
	t.Parallel()
	store := &fakeStore{}
	store.open("26", "acme/widgets#26", "A change")
	files := &fakeFiles{files: map[int][]string{26: {"scripts/granule/guest-inputs.sh"}}}
	fault := mirrorFault()
	fault.Paths = nil

	if fixes := cifault.Fixes(context.Background(), store, files, fault); len(fixes) != 0 {
		t.Fatalf("a fault naming no file named a fix anyway: %+v", fixes)
	}
	if files.calls != 0 {
		t.Fatalf("asked GitHub %d times with nothing to match on", files.calls)
	}
}

// A change open against another repository is another repository's.
func TestFixesIgnoresAnotherRepositorysChange(t *testing.T) {
	t.Parallel()
	store := &fakeStore{}
	store.open("26", "other/repo#26", "The same file, elsewhere")
	files := &fakeFiles{files: map[int][]string{26: {"scripts/granule/guest-inputs.sh"}}}

	if fixes := cifault.Fixes(context.Background(), store, files, mirrorFault()); len(fixes) != 0 {
		t.Fatalf("named a change in another repository: %+v", fixes)
	}
	if files.calls != 0 {
		t.Fatalf("asked GitHub about another repository's change %d times", files.calls)
	}
}

// A deployment that cannot ask still gets the fault from its caller; it
// is the naming that goes quiet, and neither a missing GitHub client nor
// a store that will not answer may turn into a claim.
func TestFixesIsQuietWhereItCannotAsk(t *testing.T) {
	t.Parallel()
	store := &fakeStore{}
	store.open("26", "acme/widgets#26", "A change")
	files := &fakeFiles{files: map[int][]string{26: {"scripts/granule/guest-inputs.sh"}}}

	if fixes := cifault.Fixes(context.Background(), store, nil, mirrorFault()); len(fixes) != 0 {
		t.Fatalf("a deployment with no GitHub access named a fix: %+v", fixes)
	}
	if fixes := cifault.Fixes(context.Background(), nil, files, mirrorFault()); len(fixes) != 0 {
		t.Fatalf("a caller with no store named a fix: %+v", fixes)
	}
	broken := &fakeStore{err: errors.New("no")}
	if fixes := cifault.Fixes(context.Background(), broken, files, mirrorFault()); len(fixes) != 0 {
		t.Fatalf("a store that would not answer named a fix: %+v", fixes)
	}
	// And a GitHub call that fails costs that one candidate, not the
	// answer: the others are still judged.
	store.open("31", "acme/widgets#31", "The other change")
	files.files[31] = []string{"scripts/granule/guest-inputs.sh"}
	angry := &fakeFiles{files: files.files, err: errors.New("502")}
	if fixes := cifault.Fixes(context.Background(), store, angry, mirrorFault()); len(fixes) != 0 {
		t.Fatalf("a failed read was taken as a match: %+v", fixes)
	}
}

// Where is the half-sentence every reader of a fix writes.
func TestFixWhereSaysWhyTheChangeWasNamed(t *testing.T) {
	t.Parallel()
	cases := []struct {
		files []string
		want  string
	}{
		{nil, "a file this failure names"},
		{[]string{"go.mod"}, "go.mod"},
		{[]string{"go.mod", "go.sum"}, "2 of the files this failure names"},
	}
	for _, tc := range cases {
		if got := (cifault.Fix{Files: tc.files}).Where(); got != tc.want {
			t.Errorf("Where(%v) = %q, want %q", tc.files, got, tc.want)
		}
	}
}

type ownerFakeStore struct {
	fakeStore
	states map[string]model.State
	tags   map[string][]string
	obs    map[string]*model.Observation
}

func (s *ownerFakeStore) State(_ context.Context, id string) (model.State, error) {
	if s.states == nil {
		return model.StateQueued, nil
	}
	return s.states[id], nil
}

func (s *ownerFakeStore) OpenTasksWithTag(_ context.Context, tag string) ([]string, error) {
	if s.tags == nil {
		return nil, nil
	}
	return s.tags[tag], nil
}

func (s *ownerFakeStore) GetObservation(_ context.Context, id string) (*model.Observation, error) {
	if s.obs == nil {
		return nil, nil
	}
	return s.obs[id], nil
}

func TestResolveOwnerOrdersByQueueOrderAndHonorsFixesBaseBranch(t *testing.T) {
	t.Parallel()
	store := &ownerFakeStore{
		fakeStore: fakeStore{
			tasks: map[string]*model.Task{
				"246": {ID: "246", OrderKey: 10, Base: "main"},
				"238": {ID: "238", OrderKey: 20, Base: "main"},
			},
		},
		states: map[string]model.State{
			"246": model.StateQueued,
			"238": model.StateQueued,
		},
	}
	fault := ci.SharedFault{
		Repo:  "acme/widgets",
		JobID: "image",
		Refs:  []string{"grain/task-238", "grain/task-246"},
		Runs: []ci.FaultRun{
			{RunID: "r1", Ref: "grain/task-238", TaskID: "238"},
			{RunID: "r2", Ref: "grain/task-246", TaskID: "246"},
		},
		OwnerTaskID: "238",
		OwnerRef:    "grain/task-238",
	}

	// Task 246 has a lower OrderKey (10 < 20), so it is first in queue order
	// even though 238 has a lower numeric ID.
	ownerID, ownerRef := cifault.ResolveOwner(context.Background(), store, fault)
	if ownerID != "246" || ownerRef != "grain/task-246" {
		t.Fatalf("ResolveOwner = (%q, %q), want (246, grain/task-246)", ownerID, ownerRef)
	}

	// When an open, approved FixesBaseBranch task exists for the lane, it
	// supersedes queue-order ownership.
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	tag := model.BaseRepairBranchTag(repo, "main")
	store.tasks["500"] = &model.Task{
		ID:       "500",
		Target:   &repo,
		Base:     "main",
		Tags:     []string{tag, model.MarkedBaseRepairTag},
		Approval: &model.Attribution{},
	}
	store.states["500"] = model.StateRunning
	store.tags = map[string][]string{tag: {"500"}}

	ownerID, ownerRef = cifault.ResolveOwner(context.Background(), store, fault)
	if ownerID != "500" || ownerRef != "grain/task-500" {
		t.Fatalf("ResolveOwner with FixesBaseBranch = (%q, %q), want (500, grain/task-500)", ownerID, ownerRef)
	}
}

func TestResolveOwnerSkipsBlockedAndFailedTasksAndScopesByBaseBranch(t *testing.T) {
	t.Parallel()
	now := time.Now()
	store := &ownerFakeStore{
		fakeStore: fakeStore{
			tasks: map[string]*model.Task{
				"10": {ID: "10", OrderKey: 10, Base: "main"},
				"20": {ID: "20", OrderKey: 20, Base: "main"},
				"30": {ID: "30", OrderKey: 5, Base: "candidate"},
			},
		},
		states: map[string]model.State{
			"10": model.StateAwaitingSubmit,
			"20": model.StateAwaitingSubmit,
			"30": model.StateAwaitingSubmit,
		},
		obs: map[string]*model.Observation{
			"10": {MergeQueueBlockedAt: &now},
		},
	}
	fault := ci.SharedFault{
		Repo:  "acme/widgets",
		JobID: "lint",
		Refs:  []string{"grain/task-10", "grain/task-20", "grain/task-30"},
		Runs: []ci.FaultRun{
			{RunID: "r1", Ref: "grain/task-10", TaskID: "10"},
			{RunID: "r2", Ref: "grain/task-20", TaskID: "20"},
			{RunID: "r3", Ref: "grain/task-30", TaskID: "30"},
		},
		OwnerTaskID: "10",
		OwnerRef:    "grain/task-10",
	}

	// On the main lane, task 10 is merge-blocked and task 30 targets candidate,
	// so task 20 is the owner for main.
	ownerID, ownerRef := cifault.ResolveOwnerForBase(context.Background(), store, fault, "main")
	if ownerID != "20" || ownerRef != "grain/task-20" {
		t.Fatalf("ResolveOwnerForBase(main) = (%q, %q), want (20, grain/task-20)", ownerID, ownerRef)
	}

	// On the candidate lane, task 30 is the owner.
	ownerID, ownerRef = cifault.ResolveOwnerForBase(context.Background(), store, fault, "candidate")
	if ownerID != "30" || ownerRef != "grain/task-30" {
		t.Fatalf("ResolveOwnerForBase(candidate) = (%q, %q), want (30, grain/task-30)", ownerID, ownerRef)
	}

	// If task 20 also fails, no eligible owner remains on main.
	store.states["20"] = model.StateFailed
	ownerID, ownerRef = cifault.ResolveOwnerForBase(context.Background(), store, fault, "main")
	if ownerID != "" || ownerRef != "" {
		t.Fatalf("ResolveOwnerForBase(main) when all main tasks blocked/failed = (%q, %q), want empty", ownerID, ownerRef)
	}
}
