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

package daemon

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/model"
	"github.com/ai-on-gke/grain/pkg/orchestrator"
)

func TestConsoleSessionBypassesConnectionConcurrencyAndTakesNoSlot(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	conn := model.AgentBackend{
		ID:          "agy-single-slot",
		Framework:   model.AgentFrameworkAntigravity,
		AuthMode:    model.AntigravityAuthAmbient,
		Models:      []string{"gemini-3.1-pro"},
		Concurrency: 1,
	}
	pool := providerDefaultPool(t, "gemini-3.1-pro", conn)

	workerTask := model.Task{ID: "worker-1", Title: "ordinary work"}
	workerChoice := orchestrator.ChoiceFor(workerTask)
	if workerChoice.Priority {
		t.Fatalf("ChoiceFor(workerTask).Priority = true, want false")
	}

	sessionTask := model.Task{
		ID:          "director-1",
		Title:       "Director",
		Origin:      model.Origin{Reason: model.ReasonConsoleSession},
		Interactive: true,
		Tags:        []string{model.ConsoleSessionTag(model.DirectorSessionID)},
		Grants:      model.ConsoleSessionGrants(model.ConsoleSession{ID: model.DirectorSessionID}),
	}
	if !sessionTask.IsConsoleSession() {
		t.Fatalf("sessionTask.IsConsoleSession() = false, want true")
	}
	sessionChoice := orchestrator.ChoiceFor(sessionTask)
	if !sessionChoice.Priority {
		t.Fatalf("ChoiceFor(sessionTask).Priority = false, want true")
	}

	// 1. A console session built while the connection is idle does not
	// increment inUse or occupy the sole concurrency slot.
	sessionBuilt, err := pool.Build(ctx, sessionChoice)
	if err != nil {
		t.Fatalf("Build(sessionChoice) on idle pool: %v", err)
	}
	sessionFW := sessionBuilt.(*BackendFramework)
	if sessionFW.Lease != nil {
		t.Fatalf("sessionFW.Lease = %+v, want nil so an open session holds no slot", sessionFW.Lease)
	}
	pool.mu.Lock()
	inUse := pool.inUseLocked(conn, time.Now())
	pool.mu.Unlock()
	if inUse != 0 {
		t.Fatalf("inUseLocked with open console session = %d, want 0", inUse)
	}

	// 2. An ordinary worker can still take the sole concurrency slot while
	// the console session is open.
	if reason := pool.Availability(ctx, workerChoice); reason != "" {
		t.Fatalf("Availability(workerChoice) with open session = %q, want ready", reason)
	}
	workerBuilt, err := pool.Build(ctx, workerChoice)
	if err != nil {
		t.Fatalf("Build(workerChoice) beside open session: %v", err)
	}
	workerFW := workerBuilt.(*BackendFramework)
	defer workerFW.Release()

	pool.mu.Lock()
	inUse = pool.inUseLocked(conn, time.Now())
	pool.mu.Unlock()
	if inUse != 1 {
		t.Fatalf("inUseLocked with worker active = %d, want 1", inUse)
	}

	// 3. With the connection's concurrency slot full (1 of 1), another
	// ordinary worker must wait, but a console session reports ModelReady
	// ("") and succeeds in Build without incrementing inUse.
	if reason := pool.Availability(ctx, workerChoice); !strings.Contains(reason, "at its concurrency limit (1 of 1 runs)") {
		t.Fatalf("Availability(workerChoice) at 1/1 = %q, want concurrency limit", reason)
	}
	if reason := pool.Availability(ctx, sessionChoice); reason != "" {
		t.Fatalf("Availability(sessionChoice) at 1/1 = %q, want empty (ModelReady)", reason)
	}
	secondSessionBuilt, err := pool.Build(ctx, sessionChoice)
	if err != nil {
		t.Fatalf("Build(sessionChoice) at 1/1 concurrency: %v", err)
	}
	secondSessionFW := secondSessionBuilt.(*BackendFramework)
	if secondSessionFW.Lease != nil {
		t.Fatalf("secondSessionFW.Lease = %+v, want nil", secondSessionFW.Lease)
	}
	pool.mu.Lock()
	inUse = pool.inUseLocked(conn, time.Now())
	pool.mu.Unlock()
	if inUse != 1 {
		t.Fatalf("inUseLocked after building console session at 1/1 = %d, want 1", inUse)
	}
}

// A task holding priority-dispatch is admitted onto a connection at its
// concurrency limit exactly as a console session is: the pool asks the
// one predicate (model.Task.HasPriorityDispatch), not "is this a session".
func TestAPriorityDispatchTaskIsAdmittedOntoAConnectionAtItsLimit(t *testing.T) {
	ctx := context.Background()
	conn := model.AgentBackend{
		ID:          "agy-single-slot",
		Framework:   model.AgentFrameworkAntigravity,
		AuthMode:    model.AntigravityAuthAmbient,
		Models:      []string{"gemini-3.1-pro"},
		Concurrency: 1,
	}
	pool := providerDefaultPool(t, "gemini-3.1-pro", conn)

	workerChoice := orchestrator.ChoiceFor(model.Task{ID: "worker-1"})
	steward := model.Task{ID: "steward-1", Grants: []model.Grant{
		{Capability: model.PriorityDispatchCapability, Via: model.GrantByGrain}}}
	stewardChoice := orchestrator.ChoiceFor(steward)
	if !stewardChoice.Priority {
		t.Fatalf("ChoiceFor(a priority-dispatch holder).Priority = false, want true")
	}

	workerBuilt, err := pool.Build(ctx, workerChoice)
	if err != nil {
		t.Fatalf("Build(workerChoice): %v", err)
	}
	defer workerBuilt.(*BackendFramework).Release()

	if reason := pool.Availability(ctx, workerChoice); !strings.Contains(reason, "at its concurrency limit") {
		t.Fatalf("Availability(workerChoice) at 1/1 = %q, want the concurrency limit", reason)
	}
	if reason := pool.Availability(ctx, stewardChoice); reason != "" {
		t.Fatalf("Availability(stewardChoice) at 1/1 = %q, want ready", reason)
	}
	built, err := pool.Build(ctx, stewardChoice)
	if err != nil {
		t.Fatalf("Build(stewardChoice) at 1/1: %v", err)
	}
	if fw := built.(*BackendFramework); fw.Lease != nil {
		t.Fatalf("the priority run took a connection slot: %+v", fw.Lease)
	}
}

func TestNoBackendErrorWithZeroConfiguredHasNoEmptyClause(t *testing.T) {
	t.Parallel()
	if got := noBackendError("default", "", "", 0, backendExclusions{}).Error(); got != "no configured agent backend is free: of 0 configured -- add one in Settings -> Models (/settings/models)" {
		t.Errorf("noBackendError(0 configured) = %q", got)
	}
	if got := noBackendError("default", "claude", "claude-sonnet-4", 0, backendExclusions{}).Error(); got != `no configured agent backend matches framework "claude", model "claude-sonnet-4": of 0 configured -- add one in Settings -> Models (/settings/models)` {
		t.Errorf("noBackendError(0 configured with want) = %q", got)
	}
}
