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

// The team-scoped copy of the audit log. audit.log is the deployment's,
// read through /api/logs by a system admin; a team admin needs to see
// when their own team's credentials went out through the proxy without
// being shown everyone else's. TeamAuditLog charges each entry to the
// team whose work made the request -- the live task on the sandbox, or
// the team a CI job's sandbox runs as -- and records it in the model,
// where GET /api/deployment/audit-logs reads it back.

import (
	"context"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
)

// TeamAuditSink attributes a sandbox to the task, run and team it is
// working for, and records a credential access against that team.
// *model.Store implements it.
type TeamAuditSink interface {
	SandboxAuditAttribution(ctx context.Context, sandbox string) (model.AuditAttribution, bool, error)
	RecordCredentialAccess(ctx context.Context, entry model.CredentialAccessEntry) error
}

// teamAuditTimeout bounds the two store round trips one entry costs, so a
// slow database delays a git request by at most this much.
const teamAuditTimeout = 5 * time.Second

// TeamAuditLog records every entry to Next and, when the request can be
// charged to a team, to Sink as that team's credential access. An entry
// with no team behind it -- an unregistered sandbox with no live task --
// stays in Next alone.
type TeamAuditLog struct {
	Next AuditLog
	Sink TeamAuditSink
	// SandboxTeams names the team of a CI job's sandbox, which has no
	// task for Sink to attribute it through.
	SandboxTeams *SandboxTeams
}

func (l *TeamAuditLog) Record(entry AuditEntry) {
	if l.Next != nil {
		l.Next.Record(entry)
	}
	if l.Sink == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), teamAuditTimeout)
	defer cancel()

	attribution, ok, err := l.Sink.SandboxAuditAttribution(ctx, entry.Sandbox)
	if err != nil {
		return // an audit failure must not take the proxy itself down
	}
	if !ok {
		if _, registered := l.SandboxTeams.Team(entry.Sandbox); !registered {
			return
		}
		attribution = model.AuditAttribution{}
	}
	if entry.Task != "" {
		attribution.TaskID = entry.Task
	}
	ts := entry.Time
	if ts.IsZero() {
		ts = time.Now()
	}
	_ = l.Sink.RecordCredentialAccess(ctx, model.CredentialAccessEntry{
		Timestamp:  ts,
		TaskID:     attribution.TaskID,
		RunID:      attribution.RunID,
		Sandbox:    entry.Sandbox,
		Kind:       model.CredentialAccessGit,
		Resource:   auditResource(entry),
		Action:     entry.Action,
		Credential: entry.Credential,
		Outcome:    entry.Outcome,
	})
}

// auditResource is the repository an entry is for, as host/owner/repo off
// the default host, with the ref appended for a ref grant.
func auditResource(entry AuditEntry) string {
	resource := repoName(entry.Host, entry.Owner, entry.Repo)
	if entry.Ref != "" {
		resource += " " + entry.Ref
	}
	return resource
}
