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
	"github.com/ai-on-gke/grain/pkg/gates"
	"testing"
	"time"
)

func TestGateChecksDisplayRequiresCurrentResourceScope(t *testing.T) {
	f := gates.ChecksFacts{TaskID: "66", Subject: gates.Subject{Kind: "change", Authority: "github.com", Repository: "private/repo", ChangeKind: "pr", ID: "7"}}
	v := gates.UnavailableObservation("66", time.Now(), f)
	denied := withAccessRole(context.Background(), &accessRole{Who: "reader", Full: false})
	if observationVisible(denied, &v) {
		t.Fatal("CI evidence visible without CI read scope")
	}
	allowed := withAccessRole(context.Background(), &accessRole{Who: "operator", Full: true})
	if !observationVisible(allowed, &v) {
		t.Fatal("full operator cannot read observation")
	}
	// The same check is applied independently to historical views after a task's
	// links change; current task authorization is not authority over old repos.
	history := v
	history.Bindings = gates.Bindings("66")
	history.Results = history.Results[:2]
	if !observationVisible(denied, &history) || observationVisible(denied, &v) {
		t.Fatal("resource-aware projection changed")
	}
}
