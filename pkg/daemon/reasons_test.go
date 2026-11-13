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
	"strings"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
)

// A queued task local policy holds reads as held by it in the UI's reason,
// naming the claim it was refused for and the -policy file the daemon was
// started with, and offers no "Run now".
func TestReasonClassifierNamesTheLocalPolicyFile(t *testing.T) {
	const file = "/home/someone/grain_policy.yml"
	wait := &model.GrantWait{TaskID: "3", Capability: model.PolicyWaitCapability,
		Resource: "local policy does not allow repository grain/config on this machine"}
	got := ReasonClassifier{PolicyFile: file}.TaskReason(model.Task{ID: "3"}, model.StateQueued, nil, nil, nil, wait, model.ReplyFacts{})
	if got.Reason != "queued/held_by_local_policy" || got.Control != "" {
		t.Errorf("TaskReason = %+v, want queued/held_by_local_policy with no control", got)
	}
	if !strings.Contains(got.Resource, file) || !strings.Contains(got.Resource, "repository grain/config") {
		t.Errorf("TaskReason.Resource = %q, want it to name %s and the refused repository", got.Resource, file)
	}
}
