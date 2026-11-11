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
	"sync"
	"time"

	"github.com/ai-on-gke/grain/pkg/mergelogins"
	"github.com/ai-on-gke/grain/pkg/model"
)

// mergeLoginsTTL is how long one check of the reviewed-merge logins
// (mergelogins.Check) stands: the settings move when an operator edits a
// repository variable, which is rare, and each check costs a forge read
// per repository and per login.
const mergeLoginsTTL = 15 * time.Minute

// mergeLoginWarnings is ui.Config.MergeLoginWarnings: the reviewed-merge
// logins that belong to a user account, for `grain health`, across the
// deployment's target repositories' GRAIN_MERGE_LOGINS and the daemon's
// own -upgrade-reviewed-merge-logins. A health read never waits on the
// forge: a stale answer is handed back while a fresh one is read in the
// background, so the first read after a start says nothing yet.
func mergeLoginWarnings(cfg Config, store *model.Store, forge mergelogins.Forge) func(context.Context) []mergelogins.Warning {
	if store == nil || forge == nil {
		return nil
	}
	var (
		mu         sync.Mutex
		last       []mergelogins.Warning
		at         time.Time
		refreshing bool
	)
	refresh := func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		got := mergelogins.Check(forge, mergeLoginSettings(ctx, cfg, store))
		mu.Lock()
		last, at, refreshing = got, time.Now(), false
		mu.Unlock()
	}
	return func(context.Context) []mergelogins.Warning {
		mu.Lock()
		defer mu.Unlock()
		if !refreshing && (at.IsZero() || time.Since(at) > mergeLoginsTTL) {
			refreshing = true
			go refresh()
		}
		return append([]mergelogins.Warning(nil), last...)
	}
}

// mergeLoginSettings is every place this deployment trusts logins as
// grain's merges: each target repository's GRAIN_MERGE_LOGINS variable,
// and -upgrade-reviewed-merge-logins for -upgrade-reviewed-merge-repo.
func mergeLoginSettings(ctx context.Context, cfg Config, store *model.Store) []mergelogins.Setting {
	var out []mergelogins.Setting
	seen := map[string]bool{}
	if settings, err := store.GetConfig(ctx); err == nil && settings != nil {
		for _, raw := range settings.TargetRepos {
			ref, err := model.ParseRepo(strings.TrimSpace(raw))
			if err != nil || ref.Owner == "" || ref.Name == "" || strings.Contains(ref.Name, "*") || model.IsConfigRepo(ref) {
				continue
			}
			if key := strings.ToLower(ref.String()); !seen[key] {
				seen[key] = true
				out = append(out, mergelogins.Setting{Repo: ref, Name: "vars." + mergelogins.Variable})
			}
		}
	}
	if repo := strings.TrimSpace(cfg.UpgradeReviewedMergeRepo); repo != "" && len(cfg.UpgradeReviewedMergeLogins) > 0 {
		if ref, err := model.ParseRepo(repo); err == nil {
			out = append(out, mergelogins.Setting{Repo: ref, Name: "-upgrade-reviewed-merge-logins",
				Logins: cfg.UpgradeReviewedMergeLogins})
		}
	}
	return out
}
