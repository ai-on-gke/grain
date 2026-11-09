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
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
)

// The person-only refusal names the caller's kind with the right
// article: `"20" is an agent`, not `"20" is a agent`
// (docs/test-runs/2026-11-04-release-approval.md, F4).
func TestTheReleaseRefusalNamesTheCallersKindWithItsArticle(t *testing.T) {
	for kind, want := range map[model.PrincipalKind]string{
		model.PrincipalAgent:      "an agent",
		model.PrincipalAutomation: "an automation",
		model.PrincipalService:    "a service",
		"":                        "a caller nobody identified",
	} {
		if got := kindName(kind); got != want {
			t.Errorf("kindName(%q) = %q, want %q", kind, got, want)
		}
	}
	c := &Client{}
	ctx := WithPrincipal(context.Background(), model.Principal{Kind: model.PrincipalAgent, ID: "20"})
	_, err := c.RollbackRelease(ctx, "acme/widgets", "v2.4.0", ReleaseRollback{})
	if err == nil || !strings.Contains(err.Error(), `"20" is an agent, and a release is rolled back by a person`) {
		t.Errorf("an agent's rollback: %v, want the refusal naming it an agent", err)
	}
	_, err = c.DecideRelease(ctx, "acme/widgets", "v2.4.0", true, ReleaseApproval{})
	if err == nil || !strings.Contains(err.Error(), `"20" is an agent, and a release is made official by a person`) {
		t.Errorf("an agent's approval: %v, want the refusal naming it an agent", err)
	}
}
