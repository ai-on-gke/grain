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
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
)

func TestDelegatedTaskTrailersNameThePerson(t *testing.T) {
	serviceActor := model.Principal{
		Kind:   model.PrincipalService,
		ID:     "slack-bot@proj.iam.gserviceaccount.com",
		Source: "registry",
	}
	alice := model.Principal{
		Kind:   model.PrincipalHuman,
		ID:     "alice@example.com",
		Source: "registry",
	}
	bob := model.Principal{
		Kind:   model.PrincipalHuman,
		ID:     "bob@example.com",
		Source: "registry",
	}

	// 1. A delegated filing + delegated approval names the people in Requested-by
	// and Approved-by, never the delegating service account.
	delegatedTask := model.Task{
		Creator: "alice@example.com",
		Origin: model.Origin{
			Attribution: model.Attribution{
				Actor:      serviceActor,
				OnBehalfOf: &alice,
			},
		},
		Approval: &model.Attribution{
			Actor:      serviceActor,
			OnBehalfOf: &bob,
		},
	}
	got := strings.Join(identityTrailers(delegatedTask), "\n")
	want := "Requested-by: alice@example.com\nApproved-by: bob@example.com"
	if got != want {
		t.Fatalf("identityTrailers(delegated) = %q, want %q", got, want)
	}

	// 2. A direct service account (no OnBehalfOf) is never named as a person in
	// Requested-by or Approved-by.
	directServiceTask := model.Task{
		Creator: "slack-bot@proj.iam.gserviceaccount.com",
		Origin: model.Origin{
			Attribution: model.Attribution{
				Actor: serviceActor,
			},
		},
		Approval: &model.Attribution{
			Actor: serviceActor,
		},
	}
	if got := strings.Join(identityTrailers(directServiceTask), "\n"); got != "" {
		t.Fatalf("identityTrailers(direct service) = %q, want empty", got)
	}
}
