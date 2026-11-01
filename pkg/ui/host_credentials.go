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

package ui

import (
	"context"
	"log"
	"time"

	"github.com/ai-on-gke/grain/pkg/change"
	"github.com/ai-on-gke/grain/pkg/credhealth"
)

// HostCredentialsWarning is the deployment-health pane's one line for runs
// that could not start a host tool because this host's credentials
// have expired (model.EnvironmentOutcome, authfix.Error). Every task
// dispatched while it lasts fails the same way and is parked on it, so it
// is said here once, for the host, rather than only on each task.
type HostCredentialsWarning struct {
	// Detail is the most recent such run's own sentence: what expired
	// and the command to run, as <account> on <host> (authfix.Fix), with
	// the path that could not run.
	Detail string `json:"detail"`
	// Since and Last bound the spell; Runs is how many runs it has cost,
	// and TaskID the most recent one's task.
	Since  time.Time `json:"since"`
	Last   time.Time `json:"last"`
	Runs   int       `json:"runs"`
	TaskID string    `json:"taskId,omitempty"`
}

// hostCredentialsWarning is the current spell of expired-credential runs,
// or nil. A failed read is logged and reads as nil: the pane carries
// everything else it has rather than going down with it.
func (c *Client) hostCredentialsWarning(ctx context.Context) *HostCredentialsWarning {
	if c.Store == nil {
		return nil
	}
	f, err := c.Store.CurrentEnvironmentFailure(ctx)
	if err != nil {
		log.Printf("ui: reading runs stopped by the host's credentials: %v", err)
		return nil
	}
	if f == nil {
		return nil
	}
	return &HostCredentialsWarning{Detail: f.Detail, Since: f.Since, Last: f.Last, Runs: f.Runs, TaskID: f.TaskID}
}

// CredentialsHealth is the deployment-health pane's credentials section:
// everything this deployment knows to be stopped by the operator's
// credentials having lapsed, in one place, so grain health and
// read_deployment_health say so before somebody has to read run notes.
// Nil on the ordinary day, when none of its parts has anything to say.
type CredentialsHealth struct {
	// ChangeProviders is every change kind blocked on expired
	// credentials (change.Blocked): Submit, review, checks and
	// open_pull_request are refused for it until a Check passes.
	ChangeProviders []change.BlockedKind `json:"changeProviders,omitempty"`
	// Host is the current spell of runs that could not start a host tool
	// (hostCredentialsWarning), the same reading HostCredentials carries.
	Host *HostCredentialsWarning `json:"host,omitempty"`
	// Failures is every other source failing on the operator's
	// credentials (credhealth): host tools an agent called that failed
	// on auth, and any proactive check the deployment registers.
	Failures []credhealth.Failure `json:"failures,omitempty"`
}

// credentialsHealth gathers CredentialsHealth, with host the warning
// already read for the same response.
func credentialsHealth(host *HostCredentialsWarning) *CredentialsHealth {
	c := &CredentialsHealth{ChangeProviders: change.Blocked(), Host: host, Failures: credhealth.Failures()}
	if len(c.ChangeProviders) == 0 && c.Host == nil && len(c.Failures) == 0 {
		return nil
	}
	return c
}
