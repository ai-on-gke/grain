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

package model_test

import (
	"context"
	"slices"
	"testing"

	"github.com/ai-on-gke/grain/pkg/model"
)

// TestTheDeploymentsSettingsFromTheTeamRowRoundTrip covers the four
// columns schema 24 moved onto grain_config from the default team's row.
func TestTheDeploymentsSettingsFromTheTeamRowRoundTrip(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	st := openTestStore(t)
	cfg := model.DefaultConfig()
	cfg.AgentFramework = model.AgentFrameworkCodex
	cfg.AgentGitName = "Grain Bot"
	cfg.AgentGitEmail = "bot@example.com"
	cfg.TargetRepos = []string{"acme/widgets", "acme/site"}
	if err := st.PutConfig(ctx, cfg); err != nil {
		t.Fatalf("PutConfig: %v", err)
	}
	got, err := st.GetConfig(ctx)
	if err != nil || got == nil {
		t.Fatalf("GetConfig: %v, %v", got, err)
	}
	if got.AgentFramework != cfg.AgentFramework || got.AgentGitName != cfg.AgentGitName ||
		got.AgentGitEmail != cfg.AgentGitEmail || !slices.Equal(got.TargetRepos, cfg.TargetRepos) {
		t.Errorf("GetConfig = framework %q, identity %q <%q>, target repos %v; want %q, %q <%q>, %v",
			got.AgentFramework, got.AgentGitName, got.AgentGitEmail, got.TargetRepos,
			cfg.AgentFramework, cfg.AgentGitName, cfg.AgentGitEmail, cfg.TargetRepos)
	}
	repos, err := st.TargetRepos(ctx)
	if err != nil || !slices.Equal(repos, cfg.TargetRepos) {
		t.Errorf("TargetRepos = %v, %v; want %v", repos, err, cfg.TargetRepos)
	}
}
