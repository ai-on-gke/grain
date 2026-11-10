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
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ai-on-gke/grain/pkg/capability/grainci"
	"github.com/ai-on-gke/grain/pkg/ci"
	"github.com/ai-on-gke/grain/pkg/model"
)

// A CI job's declared capabilities are asked about before anything is
// minted (task 3971): one nobody accepted refuses the job naming what it
// waits for, the grain#ci grant every job carries is not the job's to be
// asked about, and grain's own builtin jobs are not asked at all.
func TestACIJobsUnacceptedDeclarationRefusesIt(t *testing.T) {
	repo := model.RepoRef{Owner: "acme", Name: "widgets"}
	gcp := `gcp#gcp-key={"permission_set":"publisher"}`
	var asked []string
	declarations := func(_ context.Context, _ model.RepoRef, job, _ string, caps []string, _ time.Time) (bool, []string, error) {
		asked = caps
		held := model.DeclaredCIJobHold(caps)
		return len(held) == 0, held, nil
	}
	req := ci.SandboxRequest{Name: "publish", Repo: repo.String(), Capabilities: []string{gcp, grainci.Grant}}
	err := ciDeclarationRefusal(context.Background(), declarations, repo, req, time.Now(), nil)
	if err == nil || !strings.Contains(err.Error(), gcp) || !strings.Contains(err.Error(), model.CIJobDeclarationID(repo, "publish")) {
		t.Fatalf("err = %v, want a refusal naming %s and the job", err, gcp)
	}
	if slices.Contains(asked, grainci.Grant) {
		t.Errorf("grain#ci was asked about: %v", asked)
	}
	req.Builtin = "grain-tests"
	if err := ciDeclarationRefusal(context.Background(), declarations, repo, req, time.Now(), nil); err != nil {
		t.Errorf("a builtin job was refused: %v", err)
	}
	if err := ciDeclarationRefusal(context.Background(), nil, repo, ci.SandboxRequest{Capabilities: []string{gcp}}, time.Now(), nil); err != nil {
		t.Errorf("a daemon with no store refused a job: %v", err)
	}
}
