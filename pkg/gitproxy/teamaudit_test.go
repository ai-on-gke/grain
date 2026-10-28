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

package gitproxy

import (
	"context"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

type fakeTeamAuditSink struct {
	live     map[string]model.AuditAttribution
	recorded []model.CredentialAccessEntry
}

func (f *fakeTeamAuditSink) SandboxAuditAttribution(_ context.Context, sandbox string) (model.AuditAttribution, bool, error) {
	a, ok := f.live[sandbox]
	return a, ok, nil
}

func (f *fakeTeamAuditSink) RecordCredentialAccess(_ context.Context, e model.CredentialAccessEntry) error {
	f.recorded = append(f.recorded, e)
	return nil
}

func TestTeamAuditLogChargesEachEntryToTheTeamWhoseWorkMadeIt(t *testing.T) {
	sink := &fakeTeamAuditSink{live: map[string]model.AuditAttribution{
		"task-sandbox": {TaskID: "a1b2", RunID: "r1"},
	}}
	teams := NewSandboxTeams()
	teams.Grant("ci-sandbox", "ops")
	next := &RecordingAuditLog{}
	log := &TeamAuditLog{Next: next, Sink: sink, SandboxTeams: teams}
	at := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)

	log.Record(AuditEntry{Time: at, Sandbox: "task-sandbox", Owner: "acme", Repo: "widgets",
		Action: "git-upload-pack", Credential: "eng-github", Outcome: "forwarded: 200"})
	log.Record(AuditEntry{Time: at, Sandbox: "ci-sandbox", Host: "ghe.example.com", Owner: "acme", Repo: "infra",
		Action: "git-receive-pack", Credential: "ops-github", Outcome: "forwarded: 200"})
	log.Record(AuditEntry{Time: at, Sandbox: "idle-sandbox", Owner: "acme", Repo: "widgets",
		Action: "info/refs", Outcome: "denied: not in scope"})

	if len(next.Entries) != 3 {
		t.Fatalf("deployment audit log got %d entries, want every one of 3", len(next.Entries))
	}
	if len(sink.recorded) != 2 {
		t.Fatalf("team audit recorded %+v, want the task's and the CI job's only", sink.recorded)
	}
	task, ci := sink.recorded[0], sink.recorded[1]
	if task.TaskID != "a1b2" || task.RunID != "r1" || task.Resource != "acme/widgets" ||
		task.Credential != "eng-github" || task.Kind != model.CredentialAccessGit || !task.Timestamp.Equal(at) {
		t.Fatalf("task sandbox entry = %+v", task)
	}
	if ci.TaskID != "" || ci.Resource != "ghe.example.com/acme/infra" || ci.Action != "git-receive-pack" {
		t.Fatalf("CI sandbox entry = %+v", ci)
	}
}
