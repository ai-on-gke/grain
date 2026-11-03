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

package release

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/capability/protocol"
	"github.com/ai-on-gke/grain/pkg/model"
)

// TestMethodsHoldTheirOwnRule shows each method refusing a mint its
// rule does not admit even when grain asked for it: outside a CI job, on
// an event the rule does not name, on a caller's own schedule run, and on
// a run that withholds event-gated capabilities -- without reading a
// credential.
func TestMethodsHoldTheirOwnRule(t *testing.T) {
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	for _, m := range Methods() {
		t.Run(m.Spec.Name, func(t *testing.T) {
			for _, tc := range []struct {
				name string
				ctx  context.Context
			}{
				{"no CI job", context.Background()},
				{"push", model.WithCIMint(context.Background(), model.CIMint{Repo: repo, RunID: "r", Event: "push"})},
				{"pull_request", model.WithCIMint(context.Background(), model.CIMint{Repo: repo, RunID: "r", Event: "pull_request"})},
				{"untrusted schedule", model.WithCIMint(context.Background(), model.CIMint{Repo: repo, RunID: "r", Event: "schedule"})},
				{"withheld release", model.WithCIMint(context.Background(), model.CIMint{Repo: repo, RunID: "r", Event: "release", WithholdEventGated: true})},
			} {
				resp, err := m.Spec.Mint(tc.ctx, "attempt-"+tc.name, time.Now().Add(time.Hour), map[string]any{})
				if err != nil {
					t.Fatalf("%s: %v", tc.name, err)
				}
				if resp.Status != protocol.StatusRefused || len(resp.Placements) != 0 {
					t.Fatalf("%s: got %+v, want a refusal placing nothing", tc.name, resp)
				}
				if !strings.Contains(resp.Reason, m.Spec.Name) {
					t.Errorf("%s: refusal %q does not name the method", tc.name, resp.Reason)
				}
			}
		})
	}
}
