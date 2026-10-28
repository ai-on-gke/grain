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

package orchestrator

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/model/sqlite/sqlitetest"
)

var preemptRepo = model.RepoRef{Owner: "acme", Name: "widgets"}

func preemptTask(id, base string) model.Task {
	return model.Task{
		ID: id, Intent: model.IntentImplement, Title: "task " + id,
		Origin:   model.Origin{Attribution: model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "h"}}, Reason: model.ReasonDirect},
		Binding:  model.BindingDirective,
		Approval: &model.Attribution{Actor: model.Principal{Kind: model.PrincipalHuman, ID: "h"}},
		Target:   &preemptRepo, Base: base,
	}
}

// fakeCIPreempter stops the run it is given once, then reports it
// finished when told to.
type fakeCIPreempter struct {
	run      string
	stopped  []string
	finished bool
}

func (f *fakeCIPreempter) PreemptBlockedCI(_ context.Context, repo, base, except, reason string) (string, string) {
	if f.run == "" {
		return "", ""
	}
	f.stopped = append(f.stopped, f.run)
	id := f.run
	f.run = ""
	return id, "run " + id
}

func (f *fakeCIPreempter) CIRunFinished(string) bool { return f.finished }

func TestPreempterStopsBlockedCIFirstThenTheNewestBlockedAgentRun(t *testing.T) {
	store, _ := sqlitetest.Open(t)
	ctx := context.Background()
	now := time.Date(2026, 10, 24, 1, 0, 0, 0, time.UTC)

	repair := preemptTask("r1", "main")
	model.SetFixesBaseBranch(&repair, true)
	otherRepair := preemptTask("r0", "main")
	model.SetFixesBaseBranch(&otherRepair, true)
	session := preemptTask("chat", "main")
	session.Interactive = true
	tasks := []model.Task{repair, otherRepair, session,
		preemptTask("old", "main"), preemptTask("new", "main"), preemptTask("elsewhere", "candidate")}
	for _, tk := range tasks {
		if err := store.PutTask(ctx, tk); err != nil {
			t.Fatal(err)
		}
	}
	cancellers := NewTaskCancellers()
	causes := map[string]error{}
	for i, id := range []string{"r0", "chat", "old", "elsewhere", "new"} {
		if err := store.StartRun(ctx, model.Run{ID: id + "-1", TaskID: id, Attempt: 1,
			StartedAt: now.Add(time.Duration(i) * time.Minute)}, model.Limits{}); err != nil {
			t.Fatal(err)
		}
		cancellers.register(id, func(cause error) { causes[id] = cause })
	}

	ciStop := &fakeCIPreempter{run: "ci-7"}
	deps := Deps{Store: store, Config: Config{
		Preemptions: NewPreemptions(), CIPreempter: ciStop, TaskCancellers: cancellers,
	}}
	base := model.BaseRepairBranchTag(preemptRepo, "main")

	what, fresh := preempter(deps, now)(ctx, repair, base)
	if !fresh || !strings.Contains(what, "ci-7") || len(causes) != 0 {
		t.Fatalf("first pre-emption = %q (fresh %v), agent runs cancelled %v; want the blocked CI run alone", what, fresh, causes)
	}
	// While that run is still winding down nothing more is taken.
	what, fresh = preempter(deps, now.Add(time.Second))(ctx, repair, base)
	if fresh || !strings.Contains(what, "ci-7") || len(ciStop.stopped) != 1 {
		t.Fatalf("second ask = %q (fresh %v), CI stopped %v; want the pending pre-emption reported again", what, fresh, ciStop.stopped)
	}

	// Once it has finished, and with no CI left to stop, the newest agent
	// run on main goes -- not the chat, not the other repair, not the run
	// on another branch.
	ciStop.finished = true
	what, fresh = preempter(deps, now.Add(2*time.Second))(ctx, repair, base)
	if !fresh || !strings.Contains(what, "new-1") {
		t.Fatalf("third pre-emption = %q (fresh %v), want run new-1", what, fresh)
	}
	if len(causes) != 1 || !errors.Is(causes["new"], errPreempted) {
		t.Fatalf("cancelled %v, want only new with errPreempted", causes)
	}
	comments, err := store.Comments(ctx, "new")
	if err != nil {
		t.Fatal(err)
	}
	if len(comments) != 1 || !strings.Contains(comments[0].Body, "pre-empted") ||
		!strings.Contains(comments[0].Body, "does not count against") {
		t.Fatalf("comments on new = %+v, want the pre-emption announced", comments)
	}
}

func TestPreemptibleSparesSessionsAndRepairs(t *testing.T) {
	base := model.BaseRepairBranchTag(preemptRepo, "main")
	repair := preemptTask("r", "main")
	model.SetFixesBaseBranch(&repair, true)
	chat := preemptTask("c", "main")
	chat.Interactive = true
	for _, c := range []struct {
		task model.Task
		want bool
	}{
		{preemptTask("w", "main"), true},
		{preemptTask("o", "candidate"), false},
		{repair, false},
		{chat, false},
	} {
		if got := preemptible(c.task, base); got != c.want {
			t.Errorf("preemptible(%s) = %v, want %v", c.task.ID, got, c.want)
		}
	}
}
